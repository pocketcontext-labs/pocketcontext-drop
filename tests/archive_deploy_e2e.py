#!/usr/bin/env python3
"""Exercise the published archive recipes through the hosted reader and real Drop agent.
Run on Linux amd64 with Chromium, aiosmtpd, e2fsprogs, static BusyBox and a
Firecracker executable that can open /dev/kvm. Supply the verified NibRunner
kernel and rootfs directory. No production service or machine-wide setting
is used. The guest consumes the real Drop agent's rendered command and layer.

The fixture supplies cached generic inspector decisions and the NibRunner
report/log protocol. Its TLS proxy forwards readiness checks to each real VM.
By default the browser downloads the pinned releases through the real Drop
HTTPS importer. For an offline run pass --archives with the pinned files
named pocketbase.zip and zeroclaw.tar.gz. Only the upstream import response
is replaced in that mode; both hashes are checked again before updating.
"""

import argparse
import base64
import hashlib
import io
import json
import os
import secrets
import shutil
import subprocess
import tarfile
import tempfile
import time
import zipfile
from pathlib import Path

from archive_guest import boot, public_gateway
from drop_e2e import HOST_ID, NODE, Node, Server, read_json, wait_for
from e2e import APP_DOMAIN, InvitedUser, hosted_mailbox, isolated_env

HERE = Path(__file__).resolve().parent
RECIPES = [
    {
        "name": "pocketbase",
        "source": "pocketbase.zip",
        "updated": "pocketbase-update.zip",
        "env": {},
        "assets": ["fixture-version.txt"],
        "executable": "pocketbase",
        "port": 8090,
        "url": "https://github.com/pocketbase/pocketbase/releases/download/v0.40.6/pocketbase_0.40.6_linux_amd64.zip",
        "sha256": "0e1f609158ed945f809f8a02ae012aa0427129c66ebb63c1aee76f2c0309373f",
        "args": [
            "serve",
            "--http=0.0.0.0:8090",
            "--dir=./data/pb_data",
            "--publicDir=./data/pb_public",
        ],
    },
    {
        "name": "zeroclaw",
        "source": "zeroclaw.tar.gz",
        "updated": "zeroclaw-update.tar.gz",
        "env": {
            "ZEROCLAW_CONFIG_DIR": "/app/data/zeroclaw",
            "ZEROCLAW_DATA_DIR": "/app/data/zeroclaw",
        },
        "assets": ["web/dist"],
        "executable": "zeroclaw",
        "port": 42617,
        "url": "https://github.com/zeroclaw-labs/zeroclaw/releases/download/v0.9.0/zeroclaw-x86_64-unknown-linux-musl.tar.gz",
        "sha256": "8b9bc6735946158a8effbaa3c130efd8d4c1c983504cf20f08bb745af3544fd0",
        "args": ["daemon", "--host", "0.0.0.0", "--port", "42617"],
    },
]


def update_archive(directory, recipe):
    src = directory / recipe["source"]
    dest = directory / recipe["updated"]
    with src.open("rb") as stream:
        assert hashlib.file_digest(stream, "sha256").hexdigest() == recipe["sha256"]
    marker = b"archive acceptance update"
    if recipe["name"] == "pocketbase":
        shutil.copyfile(src, dest)
        with zipfile.ZipFile(dest, "a") as release:
            release.writestr("fixture-version.txt", marker)
    else:
        changed = False
        with (
            tarfile.open(src, "r:gz") as original,
            tarfile.open(dest, "w:gz") as updated,
        ):
            for entry in original:
                body = original.extractfile(entry) if entry.isfile() else None
                if entry.name.lstrip("./") == "web/dist/index.html":
                    content = body.read() + b"<!-- " + marker + b" -->"
                    entry.size = len(content)
                    body = io.BytesIO(content)
                    changed = True
                updated.addfile(entry, body)
        assert changed


def run_browser(server, directory, *argv):
    child = subprocess.Popen(
        [
            "node",
            HERE / "archive_deploy_browser.mjs",
            directory,
            str(server.port),
            *argv,
        ],
        stdout=subprocess.PIPE,
        text=True,
    )
    until = time.monotonic() + 300
    try:
        while child.poll() is None:
            for req in directory.glob("probe-*"):
                sha = req.name.removeprefix("probe-")
                if len(sha) != 64:
                    raise ValueError("Invalid test digest")
                done = directory / ("approved-" + sha)
                if done.exists():
                    continue
                claim = server.owner(
                    "op", "beta.claim", "-", stdin=json.dumps({"sha256": sha})
                )
                if "attempt" in claim:
                    server.owner(
                        "op",
                        "beta.complete",
                        "-",
                        stdin=json.dumps(
                            {
                                "sha256": sha,
                                "attempt": claim["attempt"],
                                "outcome": "invalid",
                            }
                        ),
                    )
                done.touch()
            if time.monotonic() > until:
                raise TimeoutError("reader deployment")
            time.sleep(0.2)
        if child.returncode:
            raise RuntimeError("reader deployment failed")
        return child.stdout.read().splitlines()
    finally:
        if child.poll() is None:
            child.terminate()
            child.wait(timeout=10)


def exercise(user, server, node, work, recipe, app, gateway, nib, firecracker, relay):
    guests, proxy, _ = gateway
    work.mkdir()
    credentials = {"email": "owner@example.test", "password": secrets.token_hex(24)}
    previous_volume = previous_digest = ""
    for phase in ["initial", "updated"]:
        if phase == "updated":
            update_archive(server.root, recipe)
            run_browser(server, server.root, "update", recipe["name"], app)
        release = user.op("release.list", app=app)["releases"][0]
        source = server.root / (
            recipe["source"] if phase == "initial" else recipe["updated"]
        )
        with source.open("rb") as stream:
            expected = hashlib.file_digest(stream, "sha256").hexdigest()
        assert (
            release["archive_sha256"] == expected
            and release["executable_path"] == recipe["executable"]
        )
        if phase == "initial":
            assert expected == recipe["sha256"]

        def staged():
            for candidate in (read_json(node.desired) or {}).get("instances", []):
                if (
                    candidate["appId"] == "d-" + app
                    and candidate["layers"][0]["digest"] == release["sha256"]
                ):
                    return candidate

        instance = wait_for("agent staging " + recipe["name"], staged)
        assert instance["config"]["httpPort"] == recipe["port"]
        layer = instance["layers"][0]
        artifact = node.artifacts / layer["objectKey"]
        with artifact.open("rb") as stream:
            assert (
                hashlib.file_digest(stream, "sha256").hexdigest() == release["sha256"]
            )
        volume = next(
            v["volumeId"]
            for v in read_json(node.desired)["volumes"]
            if v["appId"] == instance["appId"]
        )
        if previous_volume:
            assert volume == previous_volume and release["sha256"] != previous_digest
        previous_volume, previous_digest = volume, release["sha256"]
        hostname = instance["hostnames"][0]["hostname"]
        with boot(
            work / phase,
            artifact,
            instance,
            firecracker,
            nib,
            relay,
            work / (volume + ".ext4"),
        ) as guest:
            guests[hostname] = guest
            stamp = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
            output = guest.log()
            if phase == "initial":
                shape = (
                    "pbinstall/"
                    if recipe["name"] == "pocketbase"
                    else "X-Pairing-Code:"
                )
                output = wait_for(
                    "guest private setup output",
                    lambda: (lambda x: x if shape in x else None)(guest.log()),
                    timeout=20,
                )
            logdir = node.nib / "logs"
            logdir.mkdir(exist_ok=True)
            # NibRunner's app-log and report protocol, using the real VM's output.
            file = logdir / (instance["appId"] + ".jsonl")
            with file.open("a") as logs:
                for line in output.splitlines():
                    logs.write(
                        json.dumps(
                            {
                                "appId": instance["appId"],
                                "deploymentId": instance["deploymentId"],
                                "kind": "data",
                                "stream": "stdout",
                                "text": line,
                                "observedAt": stamp,
                            }
                        )
                        + "\n"
                    )
            node.write_reported(
                [
                    {
                        "appId": instance["appId"],
                        "deploymentId": instance["deploymentId"],
                        "state": "running",
                        "restartCount": 0,
                        "convergedAt": stamp,
                    }
                ],
                accepted=node.revision(),
            )
            current = read_json(node.reported)
            current["versions"]["cpuQuarterQuota"] = True
            node.reported.write_text(json.dumps(current))

            def ready():
                status = user.op("status", app=app, verify_url=True)
                if status.get("url_ready") and (
                    phase == "updated" or status.get("first_run")
                ):
                    return status

            status = wait_for(
                "HTTP readiness and applicable private first-run action",
                ready,
                timeout=35,
            )
            if phase == "initial":
                assert "first_run" not in server.owner("app", "status", app)
                run_browser(
                    server, server.root, "setup", recipe["name"], app, hostname, proxy
                )
                token = status["first_run"]["value"]

            def call(route, obj=None, headers=None):
                code, body = guest.request(route, obj, headers)
                assert 200 <= code < 300, (route, code)
                return json.loads(body)

            if recipe["name"] == "pocketbase":
                if phase == "initial":
                    call(
                        "/api/collections/_superusers/records",
                        {**credentials, "passwordConfirm": credentials["password"]},
                        {"Authorization": token},
                    )
                auth = {
                    "Authorization": call(
                        "/api/collections/_superusers/auth-with-password",
                        {
                            "identity": credentials["email"],
                            "password": credentials["password"],
                        },
                    )["token"]
                }
                if phase == "initial":
                    call(
                        "/api/collections",
                        {
                            "name": "fixture",
                            "type": "base",
                            "fields": [{"name": "title", "type": "text"}],
                        },
                        auth,
                    )
                    record = call(
                        "/api/collections/fixture/records", {"title": "saved"}, auth
                    )["id"]
                else:
                    assert (
                        call("/api/collections/fixture/records/" + record, None, auth)[
                            "title"
                        ]
                        == "saved"
                    )
            else:
                dashboard = guest.request("/")[1].lower()
                assert b"<html" in dashboard
                if phase == "initial":
                    pairing = call("/pair", {}, {"X-Pairing-Code": token})["token"]
                else:
                    assert b"archive acceptance update" in dashboard
                assert (
                    guest.request(
                        "/api/status", None, {"Authorization": "Bearer " + pairing}
                    )[0]
                    == 200
                )
            del guests[hostname]
            print(
                f"{recipe['name']}: {phase} release passed real guest HTTP, setup and persistence checks",
                flush=True,
            )


def scenario(binary, root, nib, firecracker, gateway):
    home = root / "home"
    home.mkdir()
    server_root = root / "server"
    server_root.mkdir()
    env = isolated_env(home)
    env.update(
        DROP_OWNER_EMAIL="owner@example.test",
        DROP_APP_DOMAIN=APP_DOMAIN,
        DROP_SMTP_FROM="login@example.test",
        DROP_BETA_NODES=NODE,
        DROP_CREW_REPLICA_BUCKET="replicas-example",
        DROP_AWS_REGION="eu-north-1",
    )
    mailbox = hosted_mailbox(server_root, env)
    _, proxy, cert = gateway
    ca = root / "public-ca.pem"
    ca.write_bytes(Path(env["SSL_CERT_FILE"]).read_bytes() + cert.read_bytes())
    env.update(
        SSL_CERT_FILE=str(ca),
        HTTPS_PROXY=proxy,
        https_proxy=proxy,
        NO_PROXY="127.0.0.1,localhost",
        no_proxy="127.0.0.1,localhost",
    )
    relay = root / "guest-relay"
    subprocess.run(
        ["go", "build", "-o", relay, "./tests/fixtures/archive-relay"],
        check=True,
        cwd=HERE.parent,
        env={**os.environ, "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0"},
    )
    server = Server(binary, server_root, env)
    node = None
    try:
        server.start(init=True)
        server.owner("invite", "create", "invitee@example.test", "--role", "developer")
        user = InvitedUser(server.port, server_root)
        token_payload = json.loads(
            base64.urlsafe_b64decode(user.token.split(".")[1] + "===")
        )
        (server_root / "browser.json").write_text(
            json.dumps(
                {
                    "token": user.token,
                    "record": {
                        "id": token_payload["id"],
                        "collectionName": "users",
                        "email": "invitee@example.test",
                    },
                }
            )
        )
        (server_root / "recipes.json").write_text(json.dumps(RECIPES))
        created = server.owner("node", "create", NODE, "--host-id", HOST_ID)
        node = Node(binary, root / "node", server, created["password"], env)
        node.write_empty()
        node.write_reported()
        report = read_json(node.reported)
        report["versions"]["cpuQuarterQuota"] = True
        node.reported.write_text(json.dumps(report))
        node.start()
        wait_for(
            "agent initial handshake",
            lambda: server.owner("node", "list")["nodes"][0]["online"],
        )
        lines = run_browser(server, server_root)
        for recipe, line in zip(RECIPES, lines, strict=True):
            result = json.loads(line)
            app = result["app"]
            assert result["name"] == recipe["name"]
            print(
                recipe["name"]
                + ": hosted GET /deploy, pinned archive import and deployment accepted",
                flush=True,
            )
            exercise(
                user,
                server,
                node,
                root / recipe["name"],
                recipe,
                app,
                gateway,
                nib,
                firecracker,
                relay,
            )
    finally:
        if node:
            node.stop()
        server.stop()
        mailbox.stop()


if __name__ == "__main__":
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--drop", required=True, type=Path)
    p.add_argument(
        "--work",
        type=Path,
        help="Keep fixture files, including synthetic account credentials, in this new private directory.",
    )
    p.add_argument(
        "--archives",
        type=Path,
        help="Use the two pinned local archives in place of the upstream HTTPS download.",
    )
    p.add_argument(
        "--nibrunner",
        required=True,
        type=Path,
        help="Verified NibRunner release kernel and rootfs directory.",
    )
    p.add_argument(
        "--firecracker",
        required=True,
        type=Path,
        help="Firecracker executable with access to /dev/kvm.",
    )
    args = p.parse_args()
    if args.archives:
        os.environ["DROP_ARCHIVE_FIXTURES"] = str(args.archives.resolve())
    else:
        os.environ.pop("DROP_ARCHIVE_FIXTURES", None)

    def run(root):
        with public_gateway(root) as gateway:
            scenario(
                args.drop.resolve(),
                root,
                args.nibrunner.resolve(),
                args.firecracker.resolve(),
                gateway,
            )

    if args.work:
        args.work.mkdir(mode=0o700)
        run(args.work.resolve())
    else:
        with tempfile.TemporaryDirectory(prefix="drop-archive-") as directory:
            run(Path(directory))
