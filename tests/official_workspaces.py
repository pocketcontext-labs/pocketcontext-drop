#!/usr/bin/env python3
"""Exercise two admitted people on actual Drop/agent/Firecracker guests in a private mount namespace.

The namespace must supply an EMPTY, caller-writable tmpfs /run. The test uses
fake AWS and node reports, and accepts only the exact three published artifacts.
Never supply a live node runtime, credential directory or SMTP endpoint.
"""

import argparse
import contextlib
import hashlib
import json
import os
import secrets
import socket
import subprocess
import tempfile
import threading
import time
from pathlib import Path

from archive_guest import boot, guest_request, login
from drop_e2e import HOST_ID, NODE, Node, Server, read_json, wait_for
from e2e import APP_DOMAIN, hosted_mailbox, hosted_request, isolated_env
from hosted_mail import read_mail

# Filename, product, version, binary SHA-256, rehearsal receipt SHA-256, receipt asset
RELEASES = [
    (
        "baseline",
        "crew",
        "0.45.4-drop.5",
        "edeb0740870ba881c005e6a309f444206c0c133e821f69b190bcc13563923b6a",
        "2043c9f6bf904a236f0c427087b25d2621ef5d63cb318263b643780806991816",
        "crew-baseline-rehearsal.json",
    ),
    (
        "atlas",
        "atlas",
        "0.25.0-drop.1",
        "fedc73f5d4149e6378ae218cb309b59126ae27601910683337fdd0dcaeae3cd3",
        "0857f2522786d5743f753a728bfe425bd89d38621a0a5a1e41639f8a388c59ea",
        "atlas-release-evidence.json",
    ),
    (
        "crew",
        "crew",
        "0.45.4-drop.6",
        "33482c60306b5efa186f0b65b7e9883e5a68a2e6fe623e9691e01d38e891c197",
        "c0852ccc9fb5ee1049afc14c10e61aa5b04aa6e002286e6c58fde6033489360d",
        "crew-rehearsal.json",
    ),
]


def digest(data):
    return hashlib.sha256(data).hexdigest()


def filehash(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def owner(server, name, **fields):
    return server.owner("op", name, "-", stdin=json.dumps(fields))


def admit(server, email):
    def send(route, data):
        status, body = hosted_request(
            server.port, "POST", route, data, headers={"X-Drop-Accept-Terms": "e2e"}
        )
        assert status in (200, 202), (route, status)
        return json.loads(body)

    send(
        "/api/drop/waitlist/request",
        {"email": email, "terms_version": "2026-11-26-waitlist"},
    )
    send(
        "/api/drop/waitlist/verify",
        {"email": email, "code": read_mail(server.root / (email + ".waitlist"))},
    )
    record = owner(server, "waitlist.list", search=email)["entries"][0]
    owner(server, "waitlist.invite", id=record["id"])
    assert read_mail(server.root / (email + ".welcome")) == "received"
    otp = send("/api/collections/users/request-otp", {"email": email})
    return send(
        "/api/collections/users/auth-with-otp-code",
        {"otpId": otp["otpId"], "code": read_mail(server.root / email)},
    )["token"]


def operation(server, token, operation_name, expected=200, key="", **fields):
    status, body = hosted_request(
        server.port,
        "POST",
        "/api/drop/op",
        {"op": operation_name, "input": fields, "idempotency_key": key},
        headers={"Authorization": token},
    )
    assert status == expected, (operation_name, status, body[:300])
    return json.loads(body).get("result")


def publish(server, assets, release, baseline=""):
    name, product, version, expected, report_hash, receipt_name = release
    artifact = assets / name
    receipt = assets / receipt_name
    assert filehash(artifact) == expected and filehash(receipt) == report_hash
    # The receipt importer is exercised separately from the isolation properties of the KVM inspector.
    manifest = json.loads(
        subprocess.check_output([artifact, "--drop-manifest"], timeout=10)
    )
    assert manifest["app"] == product and manifest["version"] == version
    link = owner(server, "catalog.upload", product=product, version=version)
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as listener:
        listener.bind("/run/drop-manifest-probe.sock")
        listener.listen(1)

        def respond():
            with listener.accept()[0] as peer, peer.makefile("rb") as reader:
                assert (
                    reader.readline(150).decode().strip()
                    == f"{expected} {artifact.stat().st_size}"
                )
                remaining = artifact.stat().st_size
                h = hashlib.sha256()
                while remaining:
                    chunk = reader.read(min(remaining, 1048576))
                    assert chunk
                    remaining -= len(chunk)
                    h.update(chunk)
                assert h.hexdigest() == expected
                peer.sendall(
                    json.dumps({"outcome": "success", "manifest": manifest}).encode()
                    + b"\n"
                )

        worker = threading.Thread(target=respond, daemon=True)
        worker.start()
        status, body = hosted_request(
            server.port, "PUT", link["path"], raw=artifact.read_bytes()
        )
        assert status == 200, (status, body[:300])
        worker.join(5)
    Path("/run/drop-manifest-probe.sock").unlink()
    data = json.loads(receipt.read_bytes())
    tag = RELEASES[2][2] if name == "baseline" else version
    url = f"https://github.com/pocketcontext-labs/pocketcontext-{product}-drop/releases/download/v{tag}/{receipt_name}"
    proof = {
        k: data[k]
        for k in (
            "before_records_sha256",
            "after_records_sha256",
            "before_attachments_sha256",
            "after_attachments_sha256",
            "records",
            "attachments",
        )
    }
    owner(
        server,
        "catalog.rehearse",
        sha256=expected,
        baseline_sha256=baseline,
        evidence_url=url,
        evidence_sha256=report_hash,
        **proof,
    )
    owner(server, "catalog.publish", sha256=expected)


def sample(g, host, product, token, previous=None):
    def api(method, path, body=None, content_type="application/json"):
        return guest_request(g, host, method, path, body, token, content_type)

    original = ("Disposable official " + product + " attachment.\n").encode()
    if previous is None:
        fields = {
            "@jsonPayload": json.dumps(
                {
                    "action": "task.create",
                    "data": {"title": "Official rehearsal"},
                    "reported_agent": "test",
                }
            )
        }
        route, file_key = "/api/crew/write", "files"
        if product == "atlas":
            space = json.loads(api("GET", "/api/spaces/default"))["id"]
            fields = {
                "space": space,
                "title": "Official rehearsal",
                "media_type": "text/plain",
                "original_name": "fixture.txt",
            }
            route, file_key = "/api/collections/sources/records", "original"
        boundary = secrets.token_hex(12)
        body = b""
        for key, value in fields.items():
            body += f'--{boundary}\r\nContent-Disposition: form-data; name="{key}"\r\n\r\n{value}\r\n'.encode()
        body += (
            f'--{boundary}\r\nContent-Disposition: form-data; name="{file_key}"; filename="fixture.txt"\r\nContent-Type: text/plain\r\n\r\n'.encode()
            + original
            + f"\r\n--{boundary}--\r\n".encode()
        )
        created = json.loads(
            api("POST", route, body, "multipart/form-data; boundary=" + boundary)
        )
        previous = (
            created["id"],
            created["attachments"][0]["id"]
            if product == "crew"
            else created["original"],
        )
    rid, fid = previous
    if product == "crew":
        row = json.loads(
            api("POST", "/api/crew/read", {"collection": "tasks", "id": rid})
        )["items"][0]
        row = {k: row[k] for k in ("id", "title", "description", "status", "revision")}
        blob = api("GET", "/api/crew/attachment?id=" + fid)
    else:
        row = json.loads(api("GET", "/api/collections/sources/records/" + rid))
        ft = json.loads(api("POST", "/api/files/token", {}))["token"]
        blob = api("GET", f"/api/files/sources/{rid}/{fid}?token={ft}")
    assert blob == original
    return previous, {
        "record_sha256": digest(
            json.dumps(row, sort_keys=True, separators=(",", ":")).encode()
        ),
        "attachment_sha256": digest(blob),
    }


def run(args, root):
    assert os.path.ismount("/run") and Path("/run/official-test-namespace").is_file()
    assert not Path("/run/drop-manifest-probe.sock").exists()
    home, server_root = root / "home", root / "server"
    home.mkdir()
    server_root.mkdir()
    env = isolated_env(home)
    env.update(
        DROP_OWNER_EMAIL="owner@example.test",
        DROP_APP_DOMAIN=APP_DOMAIN,
        DROP_SMTP_FROM="login@example.test",
        DROP_BETA_NODES=NODE,
        DROP_CREW_REPLICA_BUCKET="fixture",
        DROP_AWS_REGION="eu-north-1",
    )
    mailbox = hosted_mailbox(server_root, env)
    server = Server(args.drop, server_root, env)
    evidence = {}
    with contextlib.ExitStack() as stack:
        stack.callback(mailbox.stop)
        stack.callback(server.stop)
        server.start(init=True)
        emails = ["desktop@example.test", "mobile@example.test"]
        users = [admit(server, email) for email in emails]
        created = server.owner("node", "create", NODE, "--host-id", HOST_ID)
        node = Node(args.drop, root / "node", server, created["password"], env)
        node.write_empty()
        reports = {}

        def report():
            node.write_reported(reports.values(), accepted=node.revision())
            doc = read_json(node.reported)
            doc["versions"]["cpuQuarterQuota"] = True
            node.reported.write_text(json.dumps(doc))

        report()
        node.start()
        stack.callback(node.stop)
        wait_for(
            "agent online", lambda: server.owner("node", "list")["nodes"][0]["online"]
        )
        for release in RELEASES[:2]:
            publish(server, args.assets, release)
        owner(server, "catalog.activate")
        apps = [
            operation(
                server,
                user,
                "workspace.create",
                product=r[1],
                name="Disposable " + r[1],
                multiplier=size,
            )["app"]
            for user, r, size in zip(users, RELEASES, [4, 1])
        ]
        assert (
            apps[0]["hostname"] != apps[1]["hostname"]
            and apps[0]["volume_id"] != apps[1]["volume_id"]
        )
        operation(server, users[1], "app.get", expected=404, app=apps[0]["id"])
        sessions, records, before = {}, {}, {}
        for phase, index, release in [
            ("original-crew", 0, RELEASES[0]),
            ("original-atlas", 1, RELEASES[1]),
            ("resized-atlas", 1, RELEASES[1]),
            ("upgraded-crew", 0, RELEASES[2]),
        ]:
            app = apps[index]
            if phase == "upgraded-crew":
                publish(server, args.assets, release, RELEASES[0][3])
                operation(
                    server,
                    users[index],
                    "workspace.upgrade",
                    app=app["id"],
                    sha256=release[3],
                )
            if phase == "resized-atlas":
                operation(
                    server,
                    users[index],
                    "workspace.resize",
                    app=app["id"],
                    multiplier=2,
                )

            def staged(app=app, release=release, phase=phase):
                doc = read_json(node.desired) or {}
                return next(
                    (
                        i
                        for i in doc.get("instances", [])
                        if i["appId"] == app["nib_app_id"]
                        and i["layers"][0]["digest"] == release[3]
                        and (
                            phase != "resized-atlas"
                            or i["config"]["resources"]["memoryMib"] == 512
                        )
                    ),
                    None,
                )

            inst = wait_for(phase + " staged", staged)
            host = inst["hostnames"][0]["hostname"]
            assert host == app["hostname"] and inst["volumeId"] == app["volume_id"]
            artifact = node.artifacts / inst["layers"][0]["objectKey"]
            assert filehash(artifact) == release[3]
            # In production NibRunner injects these hook variables at boot.
            inst["config"]["command"]["environment"].update(
                NIBRUN_HOOK_TOKEN=secrets.token_hex(32),
                NIBRUN_HOOK_PORT=str(inst["config"]["hooks"]["port"]),
            )
            with boot(
                root / phase,
                artifact,
                inst,
                args.firecracker,
                args.nib,
                args.relay,
                root / (inst["volumeId"] + ".ext4"),
            ) as guest:
                if index not in sessions:
                    sessions[index] = login(guest, host, emails[index])
                else:
                    guest_request(
                        guest,
                        host,
                        "GET",
                        "/api/workspace/copies",
                        token=sessions[1 - index],
                        expected=401,
                    )
                records[index], sample_hash = sample(
                    guest, host, release[1], sessions[index], records.get(index)
                )
                assert sample_hash == before.setdefault(index, sample_hash)
                evidence[phase] = {
                    "sha256": release[3],
                    "address": host,
                    "volume": inst["volumeId"],
                    **sample_hash,
                }
                reports[index] = {
                    "appId": inst["appId"],
                    "deploymentId": inst["deploymentId"],
                    "state": "running",
                    "restartCount": 0,
                    "convergedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                }
                report()
                wait_for(
                    "installed version reported",
                    lambda app=app, user=users[index], release=release: (
                        operation(server, user, "app.get", app=app["id"])["app"].get(
                            "installed_version"
                        )
                        == release[2]
                    ),
                )
            print(phase + " passed", flush=True)
        atlas_id = apps[1]["id"]
        stopped = operation(server, users[1], "app.stop", app=atlas_id)["app"]
        assert stopped["desired_state"] == "stopped"
        resumed = operation(server, users[1], "app.start", app=atlas_id)["app"]
        assert resumed["desired_state"] != "stopped"
        assert resumed["volume_id"] == apps[1]["volume_id"]
        bought = operation(server, users[1], "credits.buy", key="fixture-top-up")
        replay = operation(server, users[1], "credits.buy", key="fixture-top-up")
        assert bought["balance"] == replay["balance"] and bought["balance"] > 0
        assert operation(server, users[0], "credits.get")["state"]["balance"] == 0
        evidence["atlas-controls"] = {
            "user_stop_start": True,
            "purchase_replay": True,
            "other_owner_balance_unchanged": True,
        }
    args.report.write_text(
        json.dumps(
            {
                "schema": "drop.official-kvm/v1",
                "observations": evidence,
                "fixtures": [
                    "loopback hosted SMTP",
                    "per-guest sign-in delivery endpoint",
                    "fake AWS",
                    "pinned manifest responder",
                    "simulated NibRunner reports",
                    "disposable ext4 and guest init",
                ],
            },
            indent=2,
        )
        + "\n"
    )


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for key in ("drop", "assets", "firecracker", "nib", "relay", "report"):
        parser.add_argument("--" + key, type=Path, required=True)
    args = parser.parse_args()
    for key, value in vars(args).items():
        setattr(args, key, value.resolve())
    with tempfile.TemporaryDirectory(prefix="drop-kvm-") as tmp:
        run(args, Path(tmp))
