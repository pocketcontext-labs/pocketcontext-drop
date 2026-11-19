"""Release assembly and validation with synthetic archives."""
import argparse
import gzip
import io
import json
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))
import release  # noqa: E402

VERSION, COMMIT = "1.2.3", "a" * 40


def fake_archive(directory, target, commit=COMMIT, extra=()):
    name = release.archive_name(VERSION, target)
    path = directory / name
    members = [(release.executable(target), b"binary", 0o755), ("THIRD_PARTY_NOTICES.txt", b"notices", 0o644)] + list(extra)
    with path.open("wb") as raw, gzip.GzipFile(fileobj=raw, mode="wb") as compressed, tarfile.open(fileobj=compressed, mode="w") as bundle:
        for member, content, mode in members:
            entry = tarfile.TarInfo(member)
            entry.size, entry.mode = len(content), mode
            bundle.addfile(entry, io.BytesIO(content))
    manifest = {"version": VERSION, "commit": commit, "platform": target, "archive": name, "sha256": release.sha256(path)}
    (directory / f"{target}.json").write_text(json.dumps(manifest))


class AssembleTest(unittest.TestCase):
    """Linux binaries and the installer."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        self.artifacts, self.output = root / "native", root / "release"
        for target in release.PLATFORMS:
            (self.artifacts / target).mkdir(parents=True)
            fake_archive(self.artifacts / target, target)

    def tearDown(self):
        self.temp.cleanup()

    def assemble(self):
        release.assemble(argparse.Namespace(version=VERSION, commit=COMMIT, artifacts=str(self.artifacts), output=str(self.output)))

    def test_complete_release(self):
        self.assemble()
        names = {path.name for path in self.output.iterdir()}
        self.assertEqual(names, release.expected_names(VERSION))
        self.assertEqual({name for name in names if name.startswith("drop_")}, {
            "drop_1.2.3_linux_amd64.tar.gz", "drop_1.2.3_linux_arm64.tar.gz"})
        self.assertEqual(names, {"drop_1.2.3_linux_amd64.tar.gz", "drop_1.2.3_linux_arm64.tar.gz", "install.sh", "checksums.txt"})
        text = (self.output / "install.sh").read_text(encoding="utf-8")
        self.assertNotIn("darwin", text.lower())
        self.assertNotIn("macos", text.lower())
        for installer in release.INSTALLERS:
            text = (self.output / installer).read_text(encoding="utf-8")
            self.assertNotIn("@VERSION@", text)
            self.assertIn(VERSION, text)

    def test_tampered_asset_is_rejected(self):
        self.assemble()
        with (self.output / "install.sh").open("a", encoding="utf-8") as stream:
            stream.write("# changed\n")
        with self.assertRaisesRegex(ValueError, "Checksum mismatch"):
            release.validate_assets(self.output, VERSION)

    def test_unexpected_archive_member_is_rejected(self):
        target = "linux_arm64"
        fake_archive(self.artifacts / target, target, extra=[("extra.so", b"x", 0o644)])
        with self.assertRaisesRegex(ValueError, "Unexpected archive layout"):
            self.assemble()

    def test_inconsistent_manifest_is_rejected(self):
        manifest = self.artifacts / "linux_amd64" / "linux_amd64.json"
        data = json.loads(manifest.read_text())
        data["commit"] = "b" * 40
        manifest.write_text(json.dumps(data))
        with self.assertRaisesRegex(ValueError, "Inconsistent release manifest"):
            self.assemble()


class NamesTest(unittest.TestCase):
    def test_names(self):
        self.assertEqual(release.REPOSITORY, "pocketcontext-labs/pocketcontext-drop")
        self.assertEqual(release.executable("linux_amd64"), "drop")
        self.assertEqual(release.archive_name("0.4.0", "linux_arm64"), "drop_0.4.0_linux_arm64.tar.gz")
        self.assertEqual(release.PLATFORMS, ("linux_amd64", "linux_arm64"))


class VersionTest(unittest.TestCase):
    def test_versions(self):
        self.assertEqual(release.version("0.1.0"), "0.1.0")
        for bad in ("v0.1.0", "0.1", "0.1.0-rc1", "01.1.0"):
            with self.assertRaises(ValueError):
                release.version(bad)


if __name__ == "__main__":
    unittest.main()
