#!/usr/bin/env python3
"""Exercise Crew's portable backup hooks between two real offline Firecracker apps.

Build tests/fixtures/archive-relay and supply the Crew Drop executable, Firecracker,
and a NibRunner guest image directory containing vmlinux and rootfs.ext4. Requires
/dev/kvm, static /bin/busybox, resize2fs, mke2fs and debugfs. All writes are under
a new temporary directory. The two guests have no network interfaces.
"""
import argparse
import base64
import hashlib
import http.client
import json
import os
import secrets
import tempfile
import time
from contextlib import contextmanager
from pathlib import Path

from archive_guest import boot, guest_request, login, vsock

OWNER = "backup-drill@example.test"


def sha256(data):
    return hashlib.sha256(data).hexdigest()


class Crew:
    def __init__(self, guest, hostname, key):
        self.guest = guest
        self.hostname = hostname
        self.hook_token = key
        self.sign_in()

    def sign_in(self):
        # Each OTP goes to the disposable VM's local vsock mailbox.
        Path(str(self.guest.path) + "_51006").unlink(missing_ok=True)
        self.token = login(self.guest, self.hostname, OWNER)

    def request(self, method, path, body=None):
        return guest_request(self.guest, self.hostname, method, path, body, self.token)

    def write(self, action, data):
        return json.loads(self.request("POST", "/api/crew/write", {
            "action": action, "reported_agent": "backup drill", "data": data,
        }))

    def read(self, collection, **fields):
        return json.loads(self.request("POST", "/api/crew/read", {
            "collection": collection, **fields,
        }))["items"]

    def hook(self, method, path, job, body=None, **headers):
        conn = http.client.HTTPConnection("127.0.0.1")
        conn.sock = vsock(self.guest.path, 10003)
        conn.sock.settimeout(120)
        try:
            conn.request(method, path, body, {
                "Authorization": "Bearer " + self.hook_token, "X-Drop-Job": job, **headers,
            })
            response = conn.getresponse()
            return response.status, response.read()
        finally:
            conn.close()


@contextmanager
def start_app(args, directory, app_id, stage):
    hostname = app_id + ".apps.example.test"
    key = secrets.token_hex(16)
    env = {
        "NIBRUN_DATA_DIR": "/app/data", "CREW_DROP_APP_ID": app_id,
        "AWS_REGION": "eu-north-1", "AWS_WORKLOAD_IDENTITY": "async",
        "AWS_EC2_METADATA_DISABLED": "true", "CREW_DROP_BOOTSTRAP": "1",
        "NIBRUN_HOOK_TOKEN": key, "NIBRUN_HOOK_PORT": "8081",
        "POCKETCONTEXT_DOMAIN": hostname, "DROP_OWNER_EMAIL": OWNER,
        "HOME": "/tmp", "TMPDIR": "/tmp",
    }
    instance = {
        "layers": [{"destinationPath": "/app/crew"}],
        "config": {
            "httpPort": 8080, "hooks": {"port": 8081}, "resources": {"memoryMib": 1024},
            "command": {"program": "/app/crew", "args": ["drop-serve"],
                        "workingDirectory": "/app", "environment": env},
        },
    }
    volume = directory / (app_id + ".ext4")
    with boot(directory / stage, args.crew, instance, args.firecracker, args.nib, args.relay, volume) as guest:
        yield Crew(guest, hostname, key)


def verify_content(app, note, attachment_id, original):
    actual = app.read("notes", id=note["id"])[0]
    assert actual["title"] == note["title"] and actual["body"] == note["body"]
    assert app.read("attachments", parent=note["id"])[0]["id"] == attachment_id
    payload = app.request("GET", "/api/crew/attachment?id=" + attachment_id)
    assert payload == original, "restored attachment bytes differ"
    return {"note_body_sha256": sha256(actual["body"].encode()), "attachment_sha256": sha256(payload)}


def rehearse(args, directory):
    start = time.monotonic()
    source_id, target_id = ["d-" + secrets.token_hex(8)[:15] for _ in range(2)]
    job = "restore-" + secrets.token_hex(8)
    original = os.urandom(8192)
    body = "Crew backup drill " + secrets.token_hex(12)
    with start_app(args, directory, source_id, "source") as source:
        bucket = source.write("bucket.create", {"name": "Backup drill", "purpose": "Synthetic restore verification"})
        note = source.write("note.create", {
            "bucket": bucket["id"], "title": "Restored note", "body": body,
            "files": [{"name": "payload.bin", "content_base64": base64.b64encode(original).decode()}],
        })
        assert body in note["body"]
        attachment = source.read("attachments", parent=note["id"])[0]["id"]
        before = verify_content(source, note, attachment, original)
        checkpoint = time.monotonic()
        status, archive = source.hook("GET", "/control/backup", job)
        assert status == 200, ("backup", status)
        backup_seconds = round(time.monotonic() - checkpoint, 2)
        exported = directory / "backup.zip"
        exported.write_bytes(archive)

    with start_app(args, directory, target_id, "target") as target:
        assert all(item["id"] != note["id"] for item in target.read("notes"))
        checkpoint = time.monotonic()
        status, _ = target.hook("POST", "/control/restore", job, exported.read_bytes(), **{
            "Content-Type": "application/zip", "X-Drop-Source-App": source_id,
        })
        assert status == 202, ("stage restore", status)
        while True:
            status, _ = target.hook("GET", "/control/restore-status", job)
            if status == 200:
                break
            assert status in (202, 503) and time.monotonic() < checkpoint + 120, ("restore status", status)
            time.sleep(0.25)
        restore_seconds = round(time.monotonic() - checkpoint, 2)
        target.sign_in()
        assert verify_content(target, note, attachment, original) == before

    with start_app(args, directory, target_id, "cold") as cold:
        after = verify_content(cold, note, attachment, original)
        assert after == before

    return {
        "source_app": source_id, "destination_app": target_id, "note_id": note["id"],
        "crew_binary_sha256": sha256(args.crew.read_bytes()), "backup_sha256": sha256(archive),
        "backup_bytes": len(archive), "attachment_bytes": len(original), "source": before,
        "destination_after_cold_boot": after, "backup_seconds": backup_seconds,
        "restore_seconds": restore_seconds, "total_seconds": round(time.monotonic() - start, 2),
    }


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("crew", "firecracker", "nib", "relay"):
        parser.add_argument("--" + name, type=lambda p: Path(p).resolve(strict=True), required=True)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="crew-backup-") as scratch:
        print(json.dumps(rehearse(args, Path(scratch)), sort_keys=True))
