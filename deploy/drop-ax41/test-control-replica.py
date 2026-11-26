"""Exercise the deployed Litestream configuration against loopback Moto S3.

Run after make build with moto[server], aiosmtpd and Litestream 0.5.18 on PATH.
"""
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import time

import boto3
from moto.server import ThreadedMotoServer


REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO / "tests"))
from drop_e2e import Server
from e2e import InvitedUser, hosted_mailbox, hosted_request, isolated_env


def accounts(database):
    with sqlite3.connect(f"file:{database}?mode=ro", uri=True) as db:
        return db.execute("""SELECT u.id, u.email, a.balance FROM users u
            JOIN drop_credit_accounts a ON a.owner=u.id ORDER BY u.email""").fetchall()


def exercise(root, endpoint):
    home = root / "home"
    home.mkdir()
    project = root / "source"
    project.mkdir()
    env = isolated_env(home)
    env.update(DROP_OWNER_EMAIL="owner@example.test", DROP_SMTP_FROM="login@example.test")
    mailbox = hosted_mailbox(project, env)
    drop = Server(REPO / "bin/drop", project, env)
    try:
        drop.start(init=True)
        drop.owner("invite", "create", "invitee@example.test", "--role", "user")
        user = InvitedUser(drop.port, project)
        live, = (home / "cache").glob("pocketcontext-drop/*/live/data.db")
        replicate(root, endpoint, live, user)
    finally:
        drop.stop()
        mailbox.stop()


def replicate(root, endpoint, live, user):
    project = root / "empty-project"
    project.mkdir()
    socket = root / "control.sock"
    env = dict(os.environ, DROP_CONTROL_LIVE_DB=str(live),
               DROP_CONTROL_REPLICA_URL="s3://control-replica/control",
               DROP_REPLICA_SOCKET=str(socket), AWS_REGION="us-east-1",
               AWS_ACCESS_KEY_ID="test", AWS_SECRET_ACCESS_KEY="test",
               AWS_SESSION_TOKEN="", AWS_EC2_METADATA_DISABLED="true")
    client = boto3.client("s3", endpoint_url=endpoint, region_name="us-east-1",
                          aws_access_key_id="test", aws_secret_access_key="test")
    client.create_bucket(Bucket="control-replica")
    source = Path(__file__).with_name("control-litestream.yml").read_text()
    config = root / "test.yml"
    config.write_text(source + f"      endpoint: {endpoint}\n      force-path-style: true\n")
    process = subprocess.Popen(["litestream", "replicate", "-config", config], env=env)
    try:
        for _ in range(100):
            if process.poll() is not None:
                raise RuntimeError("replication exited before opening its socket")
            if socket.exists():
                break
            time.sleep(.1)
        status, _ = hosted_request(user.port, "POST", "/api/drop/op",
            {"op": "credits.buy", "input": {}, "idempotency_key": "replica-test-pack"},
            headers={"Authorization": user.token})
        assert status == 200, status
        expected = accounts(live)
        assert len(expected) == 2 and any(row[2] == 500_000_000 for row in expected)
        subprocess.run(["litestream", "sync", "-wait", "-socket", socket, live],
                       env=env, check=True, timeout=40)
    finally:
        process.terminate()
        process.wait(timeout=30)
    restored = project / "drop.db"
    subprocess.run(["litestream", "restore", "-config", config, "-o", restored, live],
                   env=env, check=True, timeout=60)
    with sqlite3.connect(f"file:{restored}?mode=ro", uri=True) as db:
        assert db.execute("PRAGMA integrity_check").fetchall() == [("ok",)]
    assert accounts(restored) == expected


if __name__ == "__main__":
    server = ThreadedMotoServer(ip_address="127.0.0.1", port=0, verbose=False)
    server.start()
    try:
        with tempfile.TemporaryDirectory() as directory:
            host, port = server.get_host_and_port()
            exercise(Path(directory), f"http://{host}:{port}")
    finally:
        server.stop()
