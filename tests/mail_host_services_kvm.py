#!/usr/bin/env python3
"""Four hosted sign-ins through the receive-only Crew SMTP fixture and a real NibRunner VM.

Run inside a disposable root PID/mount/network namespace. Requires adjacent
pocketcontext-crew-drop checkout and the matching runner guest image and versions.
From the Drop checkout run the following on a disposable KVM host:
    sudo unshare --mount --net --pid --fork --kill-child --mount-proc --propagation private \
      python3 -B tests/mail_host_services_kvm.py --drop /path/to/drop --runner /path/to/nibrunnerd \
      --crew /path/to/crew --guest /path/to/guest --versions /path/to/versions.json --agent-mail
Omit --agent-mail only when exercising a baseline runner that handles SMTP itself.
"""

import argparse
import contextlib
import hashlib
import json
import os
import secrets
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path
from types import SimpleNamespace

from drop_e2e import (
    APP_DOMAIN,
    HOSTED_DOMAIN,
    Server,
    isolated_env,
    listening,
    read_json,
    wait_for,
)

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT.parent / "pocketcontext-crew-drop/drop/acceptance"))
from managed_signin import api, configure_runner, login, start, stop

ADMIN = "admin@crew-drop.invalid"
USER = "user@crew-drop.invalid"
DEV = "developer@crew-drop.invalid"
FROM = "drop@crew-drop.invalid"


def capture(stack, root, env):
    mail = root / "mail"
    mail.mkdir()
    for name in ("password", "drop-password"):
        (mail / name).write_text(secrets.token_urlsafe(32))
    cert = mail / "mail.crt"
    subprocess.run(
        [
            "openssl",
            "req",
            "-x509",
            "-newkey",
            "rsa:2048",
            "-nodes",
            "-days",
            "1",
            "-subj",
            "/CN=127.0.0.1",
            "-addext",
            f"subjectAltName=IP:127.0.0.1,DNS:*.{APP_DOMAIN}",
            "-addext",
            "basicConstraints=critical,CA:FALSE",
            "-addext",
            "extendedKeyUsage=serverAuth",
            "-keyout",
            mail / "mail.key",
            "-out",
            cert,
        ],
        check=True,
        capture_output=True,
    )
    receiver = Path(sys.modules["managed_signin"].__file__).with_name("smtp_sink.py")
    proc = start(
        [
            sys.executable,
            receiver,
            "--listen",
            "127.0.0.1",
            "--base",
            mail,
            "--owner",
            ADMIN,
            "--owner",
            USER,
            "--owner",
            DEV,
            "--drop-sender",
            FROM,
        ],
        root / "mail.log",
        env,
    )
    stack.callback(stop, proc)
    wait_for("test mailbox", lambda: listening(2525))
    return mail


def check_drop(stack, args, root, mail, env):
    directory = root / "server"
    directory.mkdir()
    server = Server(
        args.drop,
        directory,
        {
            **env,
            "DROP_OWNER_EMAIL": ADMIN,
            "DROP_APP_DOMAIN": APP_DOMAIN,
            "DROP_SMTP_FROM": FROM,
            "DROP_SMTP_HOST": "127.0.0.1",
            "DROP_SMTP_PORT": "2525",
            "DROP_SMTP_USERNAME": "fixture-drop",
            "DROP_SMTP_PASSWORD": (mail / "drop-password").read_text(),
            "DROP_SMTP_TLS": "tls",
            "SSL_CERT_FILE": str(mail / "mail.crt"),
        },
    )
    server.start(init=True)
    stack.callback(server.stop)

    def op(token, name, **fields):
        return api(
            server.port,
            HOSTED_DOMAIN,
            "POST",
            "/api/drop/op",
            {"op": name, "input": fields},
            token,
        )["result"]

    def check_login(address, role):
        token = login(server.port, HOSTED_DOMAIN, address, mail)
        me = op(token, "me")
        assert (me["role"], me["email"]) == (role, address)
        print("PASS Drop", role, flush=True)
        return token

    admin = check_login(ADMIN, "admin")
    for role, address in (("user", USER), ("developer", DEV)):
        op(admin, "invite.create", role=role, email=address)
        check_login(address, role)


def check_guest(stack, args, root, mail, env):
    nib = root / "nib"
    artifacts = root / "artifacts"
    nib.mkdir()
    artifacts.mkdir()
    desired = nib / "desired.json"
    runner_env = configure_runner(
        root,
        args,
        SimpleNamespace(nib=nib, desired=desired, artifacts=artifacts),
        mail,
        (mail / "password").read_text(),
    )
    hostname = "crew." + APP_DOMAIN
    doc = json.loads(
        (ROOT / "internal/render/testdata/crew-managed.golden.json").read_text()
    )
    app_id = "d-" + secrets.token_hex(8)[:15]
    instance = doc["instances"][0]
    volume_id = app_id + "-vol"
    instance.update(
        appId=app_id,
        deploymentId=app_id + "-dep-1",
        volumeId=volume_id,
        signInEmail={"recipient": DEV},
        hostnames=[{"kind": "platform", "hostname": hostname}],
    )
    doc["volumes"][0].update(appId=app_id, volumeId=volume_id, sizeBytes=256 << 20)
    cmd = instance["config"]["command"]
    cmd.update(
        args=["drop-serve"],
        environment={
            "HOME": "/tmp",
            "NIBRUN_DATA_DIR": "/app/data",
            "CREW_DROP_BOOTSTRAP": "1",
            "CREW_DROP_APP_ID": app_id,
            "DROP_OWNER_EMAIL": DEV,
            "POCKETCONTEXT_DOMAIN": hostname,
            "NIBRUN_HOOK_TOKEN": secrets.token_urlsafe(32),
        },
    )
    with args.crew.open("rb") as stream:
        digest = hashlib.file_digest(stream, "sha256").hexdigest()
    instance["layers"][0].update(digest=digest, objectKey=digest)
    shutil.copyfile(args.crew, artifacts / digest)
    desired.write_text(json.dumps(doc))
    config = root / "config.toml"
    sock = root / "host.sock"
    with config.open("a") as f:
        f.write(
            f'\n[host_security]\nallow_active_smt = true\n[host_services]\nsocket = "{sock}"\n'
        )
    if args.agent_mail:
        agent_env = {
            **env,
            "CREDENTIALS_DIRECTORY": runner_env.pop("CREDENTIALS_DIRECTORY"),
            "DROP_AGENT_DESIRED": str(desired),
            "DROP_AGENT_STATE_DIR": str(root / "agent"),
            "DROP_AGENT_HOST": HOSTED_DOMAIN,
            "DROP_AGENT_NODE": "mail-test",
            "DROP_AGENT_SERVER": "http://127.0.0.1:1",
            "DROP_AGENT_ALLOWED_HOST_SUFFIX": "." + APP_DOMAIN,
            "DROP_AGENT_REPORTED": str(nib / "reported.json"),
            "DROP_AGENT_ARTIFACTS": str(artifacts),
        }
        proc = start(
            [args.drop, "agent", "host-services", "--socket", sock],
            root / "host.log",
            agent_env,
        )
        stack.callback(stop, proc)

        def serving():
            if proc.poll() is not None:
                raise RuntimeError(
                    "host services exited: " + (root / "host.log").read_text()[-800:]
                )
            return sock.exists()

        wait_for("host services", serving)
    proc = start([args.runner], root / "runner.log", runner_env)
    stack.callback(stop, proc)

    def ready():
        if proc.poll() is not None:
            raise RuntimeError(
                "runner exited: " + (root / "runner.log").read_text()[-1200:]
            )
        try:
            return api(443, hostname, "GET", "/api/bundle/auth-mode").get("codeOnly")
        except (OSError, RuntimeError):
            return False

    wait_for("Crew guest", ready, timeout=90)
    token = login(443, hostname, DEV, mail)
    assert (
        api(443, hostname, "POST", "/api/collections/users/auth-refresh", token=token)[
            "record"
        ]["email"]
        == DEV
    )
    print("PASS Crew", digest, flush=True)
    instance["desiredState"] = "stopped"
    instance["activation"] = {"sleepWhen": {"kind": "never"}}
    doc["revision"] = "fixture-cleanup"
    updated = desired.with_suffix(".next")
    updated.write_text(json.dumps(doc))
    updated.replace(desired)
    wait_for(
        "fixture VM to stop",
        lambda: any(
            row["appId"] == app_id and row["state"] == "stopped"
            for row in (read_json(nib / "reported.json") or {}).get("instances", [])
        ),
        timeout=60,
    )


if __name__ == "__main__":
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("drop", "crew", "runner", "guest", "versions"):
        p.add_argument(
            "--" + name, type=lambda v: Path(v).resolve(strict=True), required=True
        )
    p.add_argument("--agent-mail", action="store_true")
    args = p.parse_args()
    if (
        os.geteuid()
        or os.getpid() != 1
        or [
            x["ifname"]
            for x in json.loads(subprocess.check_output(["ip", "-j", "link"]))
        ]
        != ["lo"]
    ):
        raise SystemExit(
            "requires sudo unshare --mount --net --pid --fork --mount-proc"
        )
    os.umask(0o077)
    subprocess.run(["ip", "link", "set", "lo", "up"], check=True)
    subprocess.run(["ip", "link", "add", "fixture0", "type", "dummy"], check=True)
    subprocess.run(["ip", "addr", "add", "192.0.2.1/24", "dev", "fixture0"], check=True)
    subprocess.run(["ip", "link", "set", "fixture0", "up"], check=True)
    subprocess.run(["ip", "route", "add", "default", "dev", "fixture0"], check=True)
    with (
        tempfile.TemporaryDirectory(prefix="ml-", dir="/var/tmp") as directory,
        contextlib.ExitStack() as stack,
    ):
        root = Path(directory)
        (root / "home").mkdir()
        env = isolated_env(root / "home")
        mail = capture(stack, root, env)
        os.environ["SSL_CERT_FILE"] = str(mail / "mail.crt")
        check_drop(stack, args, root, mail, env)
        check_guest(stack, args, root, mail, env)
