#!/usr/bin/env python3
"""Build, assemble and publish the Drop release assets.

Adapted from the vendored server's release process (github.com/pocketcontext/pocketcontext).
Drop is Linux only: static musl binaries for amd64 and arm64.
"""

import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import re
import shutil
import struct
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]
REPOSITORY = "pocketcontext-labs/pocketcontext-drop"
PLATFORMS = ("linux_amd64", "linux_arm64")
# build-tags.txt is the one list of Go build tags: SQLite features and
# tailscale.com tags that Drop does not use (copied from pocketcontext-core).
TAGS = (ROOT / "build-tags.txt").read_text(encoding="utf-8").strip()
GO_VERSION = "go1.27.3"
GO_IMAGE = "golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb"
PACKAGE = "github.com/pocketcontext-labs/pocketcontext-drop/internal/buildinfo"
VERSION_RE = re.compile(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\Z")
COMMIT_RE = re.compile(r"[0-9a-f]{40}\Z")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def output(args, **kwargs):
    # Tool output (go list JSON, pnpm) is UTF-8.
    return run(args, text=True, encoding="utf-8", capture_output=True, **kwargs).stdout.strip()


def version(value):
    require(VERSION_RE.fullmatch(value), "Version must be X.Y.Z without a leading v or prerelease suffix")
    return value


def commit(value):
    require(COMMIT_RE.fullmatch(value), "Commit must be a full lowercase Git SHA")
    return value


def executable(target):
    return "drop"


def archive_name(release_version, target):
    return f"drop_{release_version}_{target}.tar.gz"


def sha256(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def json_objects(raw):
    decoder = json.JSONDecoder()
    while raw.strip():
        raw = raw.lstrip()
        item, end = decoder.raw_decode(raw)
        yield item
        raw = raw[end:]


def legal_files(directory):
    return sorted(path for path in directory.rglob("*") if path.is_file()
                  and re.fullmatch(r"(?:LICENSE|LICENCE|COPYING|COPYRIGHT|NOTICE)(?:[._-].*)?", path.name, re.I))


def third_party_notices(target):
    """License and notice files of linked Go modules, bundled reader packages and runtimes."""
    environment = dict(os.environ, GOOS=target.split("_")[0], GOARCH=target.split("_")[1], CGO_ENABLED="1")
    packages = json_objects(output(["go", "list", "-deps", "-json", "-tags", TAGS, "./cmd/drop"], cwd=ROOT, env=environment))
    modules = {}
    for package in packages:
        module = package.get("Module")
        if module and not module.get("Main"):
            require("Replace" not in module, "Release dependencies must not use local module replacements")
            modules[module["Path"]] = module
    sections = ["Drop third-party license and copyright notices\n",
                "\n--- PocketContext server (vendored in internal/pocketcontext) ---\n\n"
                + (ROOT / "internal/pocketcontext/LICENSE").read_text(encoding="utf-8")]
    for name, module in sorted(modules.items()):
        directory = Path(module["Dir"])
        files = legal_files(directory)
        require(bool(files), f"No license file found for linked module {name}")
        for path in files:
            sections.append(f"\n--- {name} {module['Version']} / {path.relative_to(directory).as_posix()} ---\n\n" + path.read_text(encoding="utf-8"))
    goroot = Path(output(["go", "env", "GOROOT"]))
    for path in [goroot / "LICENSE", goroot / "PATENTS", *legal_files(goroot / "src/vendor")]:
        if path.is_file():
            sections.append(f"\n--- Go {GO_VERSION.removeprefix('go')} / {path.relative_to(goroot).as_posix()} ---\n\n" + path.read_text(encoding="utf-8"))
    # The reader embeds a JavaScript bundle built from these production packages.
    pnpm = shutil.which("pnpm") or shutil.which("pnpm.cmd")
    require(pnpm is not None, "pnpm is required to collect reader package notices")
    licenses = json.loads(output([pnpm, "licenses", "list", "--prod", "--json"], cwd=ROOT / "ui"))
    for declared, items in sorted(licenses.items()):
        for item in sorted(items, key=lambda entry: entry["name"]):
            for package_path, package_version in zip(item.get("paths", []), item.get("versions", [])):
                files = [path for path in legal_files(Path(package_path)) if "node_modules" not in path.relative_to(package_path).parts]
                header = f"\n--- npm {item['name']} {package_version} ({declared}) ---\n\n"
                if files:
                    sections.extend(header + path.read_text(encoding="utf-8", errors="replace") for path in files)
                else:
                    sections.append(header + f"Declared license: {declared}. The package contains no license file.\n")
    for name in ("musl-COPYRIGHT.txt", "gcc-COPYING.RUNTIME.txt", "gcc-COPYING3.txt"):
        sections.append(f"\n--- Static runtime / {name} ---\n\n" + (ROOT / "scripts/licenses" / name).read_text(encoding="utf-8"))
    return "\n".join(sections).encode("utf-8")


def metadata(args):
    event = os.environ.get("GITHUB_EVENT_NAME", "")
    ref = os.environ.get("GITHUB_REF", "")
    require(event in ("push", "workflow_dispatch"), "Only tag pushes and manual build validation are supported")
    if event == "push":
        require(ref.startswith("refs/tags/v"), "Publication requires a vX.Y.Z tag push")
        release_version = version(ref.removeprefix("refs/tags/v"))
    else:
        require(ref == "refs/heads/main", "Manual validation must run from main")
        release_version = version(args.version)
    head = commit(output(["git", "rev-parse", "HEAD"], cwd=ROOT))
    run(["git", "merge-base", "--is-ancestor", head, "origin/main"], cwd=ROOT)
    if event == "push":
        require(output(["git", "rev-parse", f"{ref}^{{commit}}"], cwd=ROOT) == head,
                "Tag does not identify the checked-out commit")
    values = {"version": release_version, "commit": head, "publish": str(event == "push").lower()}
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as stream:
            for key, value in values.items():
                stream.write(f"{key}={value}\n")
    print(json.dumps(values, sort_keys=True))


def verify_binary(binary, target):
    with binary.open("rb") as stream:
        header = stream.read(64)
        require(header[:6] == b"\x7fELF\x02\x01", "Expected a little-endian ELF64 binary")
        machine = struct.unpack_from("<H", header, 18)[0]
        require(machine == {"linux_amd64": 62, "linux_arm64": 183}[target], "ELF architecture mismatch")
        offset = struct.unpack_from("<Q", header, 32)[0]
        size, count = struct.unpack_from("<HH", header, 54)
        require(size >= 56 and count > 0, "Invalid ELF program headers")
        for index in range(count):
            stream.seek(offset + index * size)
            kind = struct.unpack("<I", stream.read(4))[0]
            require(kind not in (2, 3), "Linux release must be static: dynamic/interpreter program header found")


def native_platform():
    system = platform.system().lower()
    machine = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine().lower(), platform.machine())
    return f"{system}_{machine}"


def build(args):
    release_version, revision = version(args.version), commit(args.commit)
    require(output(["git", "rev-parse", "HEAD"], cwd=ROOT) == revision, "Build commit does not match checkout")
    require(not output(["git", "status", "--porcelain", "--untracked-files=normal"], cwd=ROOT),
            "Release builds require a clean checkout so the embedded commit identifies all compiled source")
    require((ROOT / "ui/dist/index.html").is_file(), "Build the reader (ui/dist) before the binary")
    target = args.platform
    require(native_platform() == target, f"Release builds must be native ({native_platform()} != {target})")
    destination = Path(args.output).resolve()
    destination.mkdir(parents=True, exist_ok=True)
    binary_dir = destination / target
    binary_dir.mkdir(exist_ok=True)
    binary = binary_dir / executable(target)
    flags = f"-s -w -X {PACKAGE}.Version={release_version} -X {PACKAGE}.Commit={revision}"
    flags += " -linkmode external -extldflags '-static'"
    # The image is an immutable multiarchitecture index. apk provides the
    # native musl compiler; CGO remains enabled for the SQLite authorizer.
    script = f'''apk add --no-cache build-base
test "$(go env GOVERSION)" = {GO_VERSION}
CGO_ENABLED=1 go build -trimpath -buildvcs=false -tags "$1" -ldflags "$2" -o /out/drop ./cmd/drop
chown "$3:$4" /out/drop
'''
    run(["docker", "run", "--rm", "--mount", f"type=bind,src={ROOT},dst=/src,readonly",
         "--mount", f"type=bind,src={binary_dir},dst=/out", "--workdir", "/src",
         GO_IMAGE, "sh", "-ec", script, "build", TAGS,
         flags, str(os.getuid()), str(os.getgid())])
    verify_binary(binary, target)
    notices = third_party_notices(target)
    stamp = int(output(["git", "show", "-s", "--format=%ct", revision], cwd=ROOT))
    archive = destination / archive_name(release_version, target)
    members = [(executable(target), binary.read_bytes(), 0o755), ("THIRD_PARTY_NOTICES.txt", notices, 0o644)]
    with archive.open("wb") as raw, gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=stamp) as compressed:
        with tarfile.open(fileobj=compressed, mode="w") as bundle:
            for name, content, mode in members:
                entry = tarfile.TarInfo(name)
                entry.size, entry.mode, entry.mtime = len(content), mode, stamp
                bundle.addfile(entry, io.BytesIO(content))
    manifest = {"version": release_version, "commit": revision, "platform": target,
                "archive": archive.name, "sha256": sha256(archive)}
    (destination / f"{target}.json").write_text(json.dumps(manifest, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(manifest, sort_keys=True))


def check_archive(archive, target):
    expected = {executable(target), "THIRD_PARTY_NOTICES.txt"}
    modes = {"drop": 0o755, "THIRD_PARTY_NOTICES.txt": 0o644}
    with tarfile.open(archive, "r:gz") as bundle:
        entries = bundle.getmembers()
        require(len(entries) == len(expected) and {entry.name for entry in entries} == expected
                and all(entry.isreg() and entry.mode == modes[entry.name] and entry.size > 0 for entry in entries),
                f"Unexpected archive layout: {archive.name}")


INSTALLERS = ("install.sh",)


def assemble(args):
    release_version, revision = version(args.version), commit(args.commit)
    artifacts, destination = Path(args.artifacts), Path(args.output)
    require(not destination.exists() or not list(destination.iterdir()), "Assembly output must be empty")
    destination.mkdir(parents=True, exist_ok=True)
    for target in PLATFORMS:
        manifests = list(artifacts.rglob(f"{target}.json"))
        require(len(manifests) == 1, f"Expected exactly one manifest for {target}")
        manifest = json.loads(manifests[0].read_text(encoding="utf-8"))
        name = archive_name(release_version, target)
        require(manifest.get("version") == release_version and manifest.get("commit") == revision
                and manifest.get("platform") == target and manifest.get("archive") == name,
                f"Inconsistent release manifest for {target}")
        archive = manifests[0].parent / name
        require(sha256(archive) == manifest.get("sha256"), f"Artifact checksum mismatch for {target}")
        check_archive(archive, target)
        shutil.copyfile(archive, destination / name)
    write_installers(destination, release_version)
    files = sorted(destination.iterdir())
    (destination / "checksums.txt").write_text("".join(f"{sha256(path)}  {path.name}\n" for path in files), encoding="utf-8")
    validate_assets(destination, release_version)
    print(f"Assembled {len(files) + 1} assets for v{release_version}")


def write_installers(destination, release_version):
    for installer in INSTALLERS:
        template = (ROOT / "scripts" / (installer + ".in")).read_text(encoding="utf-8")
        require("@VERSION@" in template, f"{installer} version placeholder is missing")
        rendered = template.replace("@VERSION@", release_version)
        (destination / installer).write_bytes(rendered.encode("utf-8"))
    (destination / "install.sh").chmod(0o755)


def expected_names(release_version):
    return {archive_name(release_version, target) for target in PLATFORMS} | set(INSTALLERS) | {"checksums.txt"}


def validate_assets(directory, release_version):
    names = expected_names(release_version)
    require({path.name for path in directory.iterdir()} == names, f"Release must contain exactly the {len(names)} expected assets")
    checksums = {}
    for line in (directory / "checksums.txt").read_text(encoding="utf-8").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9_.-]+)", line)
        require(match is not None, "Malformed checksum line")
        digest, name = match.groups()
        require(name not in checksums, "Duplicate checksum entry")
        checksums[name] = digest
    require(set(checksums) == names - {"checksums.txt"}, "Checksum manifest has missing or unexpected assets")
    for name, digest in checksums.items():
        require(sha256(directory / name) == digest, f"Checksum mismatch: {name}")
    for target in PLATFORMS:
        check_archive(directory / archive_name(release_version, target), target)
    return {name: sha256(directory / name) for name in names}


def api(path, method="GET", body=None, missing_ok=False):
    command = ["gh", "api", f"repos/{REPOSITORY}/{path}", "--method", method,
               "-H", "X-GitHub-Api-Version: 2026-03-10", "-H", "Cache-Control: no-cache"]
    if body is not None:
        command += ["--input", "-"]
    result = subprocess.run(command, input=None if body is None else json.dumps(body), text=True, capture_output=True)
    if result.returncode:
        if missing_ok and "(HTTP 404)" in result.stderr:
            return None
        # Do not echo raw CLI output or environment on API failures.
        raise RuntimeError(f"GitHub {method} {path} failed (exit {result.returncode})")
    return json.loads(result.stdout) if result.stdout.strip() else None


def remote_commit(tag):
    item = api(f"git/ref/tags/{tag}")["object"]
    for _ in range(8):
        if item["type"] == "commit":
            return item["sha"]
        require(item["type"] == "tag", "Release tag does not resolve to a commit")
        item = api(f"git/tags/{item['sha']}")["object"]
    raise ValueError("Excessive annotated tag nesting")


def verify_remote(release, tag, expected):
    assets = release.get("assets", [])
    require(len(assets) == len(expected) and {a["name"] for a in assets} == set(expected),
            "Remote release assets are incomplete or unexpected")
    require(all(asset["state"] == "uploaded" for asset in assets), "Remote upload is incomplete")
    with tempfile.TemporaryDirectory(prefix="drop-release-verify-") as temporary:
        run(["gh", "release", "download", tag, "--repo", REPOSITORY, "--dir", temporary], capture_output=True)
        require({p.name for p in Path(temporary).iterdir()} == set(expected), "Downloaded assets differ from expected assets")
        for name, digest in expected.items():
            require(sha256(Path(temporary) / name) == digest, f"Uploaded asset checksum mismatch: {name}")


def find_release(tag):
    # The tag endpoint only finds published releases. Authorized list requests
    # also return drafts, allowing an interrupted upload to resume by release ID.
    published = api(f"releases/tags/{tag}", missing_ok=True)
    if published is not None:
        return published
    page = 1
    while True:
        releases = api(f"releases?per_page=100&page={page}")
        matches = [item for item in releases if item["tag_name"] == tag]
        require(len(matches) <= 1, "Multiple releases found for the requested tag")
        if matches:
            return api(f"releases/{matches[0]['id']}")
        if len(releases) < 100:
            return None
        page += 1


def publish(args):
    release_version, revision = version(args.version), commit(args.commit)
    tag = f"v{release_version}"
    require(os.environ.get("GITHUB_EVENT_NAME") == "push" and os.environ.get("GITHUB_REF") == f"refs/tags/{tag}",
            "Publishing is only permitted for the matching tag-push workflow")
    require(os.environ.get("GITHUB_REPOSITORY") == REPOSITORY, "Unexpected publishing repository")
    require(remote_commit(tag) == revision, "Remote tag changed since validation")
    directory = Path(args.assets)
    expected = validate_assets(directory, release_version)
    marker = f"<!-- drop-release:{revision} -->"
    release = find_release(tag)
    if release is not None:
        require(marker in (release.get("body") or ""), "Existing release was not created for this validated commit")
        if not release["draft"]:
            require(release.get("immutable") is True, "Published release is not immutable; refusing to modify it")
            verify_remote(release, tag, expected)
            print(f"Published {tag} already matches all validated assets; no changes made")
            return
    else:
        base = f"https://github.com/{REPOSITORY}/releases/download/{tag}"
        body = (f"Drop {release_version}: server, reader and CLI in one binary.\n\n"
                "Linux only: static musl binaries for amd64 and arm64.\n\n"
                f"`curl -fsSL {base}/install.sh | sh -s -- /absolute/folder` installs this release. To download "
                f"the archives, use `gh release download {tag} -R {REPOSITORY}` and check it against checksums.txt.\n\n"
                f"See the [README](https://github.com/{REPOSITORY}/blob/{tag}/README.md).\n\n{marker}")
        notes = ROOT / "docs" / "releases" / f"{release_version}.md"
        if notes.is_file():
            body = notes.read_text(encoding="utf-8").strip() + "\n\n---\n\n" + body
        release = api("releases", "POST", {"tag_name": tag, "target_commitish": revision,
                      "name": f"Drop {release_version}", "body": body, "draft": True, "prerelease": False})
    require({asset["name"] for asset in release.get("assets", [])} <= set(expected), "Draft contains unexpected assets")
    # Retry only repairs this unpublished, commit-marked draft. Published assets
    # never pass this branch and are never overwritten or deleted.
    for asset in release.get("assets", []):
        require(api(f"releases/{release['id']}")["draft"], "Release was published while preparing uploads")
        api(f"releases/assets/{asset['id']}", "DELETE")
    run(["gh", "release", "upload", tag, "--repo", REPOSITORY,
         *[str(directory / name) for name in sorted(expected)]], capture_output=True)
    release = api(f"releases/{release['id']}")
    require(release["draft"], "Release was published before upload verification")
    verify_remote(release, tag, expected)
    require(remote_commit(tag) == revision, "Remote tag changed before publication")
    latest = api("releases/latest", missing_ok=True)
    make_latest = True
    if latest:
        latest_version = latest["tag_name"].removeprefix("v")
        require(VERSION_RE.fullmatch(latest_version), "Latest release has an unrecognized version; refusing automatic promotion")
        make_latest = tuple(map(int, release_version.split("."))) > tuple(map(int, latest_version.split(".")))
    published = api(f"releases/{release['id']}", "PATCH", {"draft": False, "prerelease": False,
                    "make_latest": str(make_latest).lower()})
    require(published.get("immutable") is True, "Release was published without immutability; check repository settings immediately")
    print(f"Published immutable {tag}; latest={str(make_latest).lower()}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    prepare = commands.add_parser("metadata")
    prepare.add_argument("--version", default="")
    prepare.set_defaults(function=metadata)
    for name, handler in (("build", build), ("assemble", assemble), ("publish", publish)):
        command = commands.add_parser(name)
        command.add_argument("--version", required=True)
        command.add_argument("--commit", required=True)
        if name == "build":
            command.add_argument("--platform", choices=PLATFORMS, required=True)
            command.add_argument("--output", default="dist")
        elif name == "assemble":
            command.add_argument("--artifacts", required=True)
            command.add_argument("--output", required=True)
        else:
            command.add_argument("--assets", required=True)
        command.set_defaults(function=handler)
    args = parser.parse_args()
    try:
        args.function(args)
    except (ValueError, RuntimeError, OSError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"Release failed: {error}\n")


if __name__ == "__main__":
    main()
