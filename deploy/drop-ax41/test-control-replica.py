"""Exercise the deployed Litestream configuration against loopback Moto S3.

Run with moto[server] installed and Litestream 0.5.18 on PATH.
"""
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import time

import boto3
from moto.server import ThreadedMotoServer


def exercise(root, endpoint):
    project = root / "empty-project"
    project.mkdir()
    live = root / "cache" / "live" / "data.db"
    live.parent.mkdir(parents=True)
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
    with sqlite3.connect(live) as db:
        db.executescript("""
          PRAGMA journal_mode=WAL;
          CREATE TABLE users(id TEXT PRIMARY KEY);
          CREATE TABLE balances(owner TEXT PRIMARY KEY, credits INTEGER);
          INSERT INTO users VALUES ('synthetic');
          INSERT INTO balances VALUES ('synthetic', 0);
        """)
        process = subprocess.Popen(["litestream", "replicate", "-config", config], env=env)
        try:
            for _ in range(100):
                if process.poll() is not None:
                    raise RuntimeError("replication exited before opening its socket")
                if socket.exists():
                    break
                time.sleep(.1)
            db.execute("UPDATE balances SET credits=500 WHERE owner='synthetic'")
            db.commit()
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
        assert db.execute("SELECT id, credits FROM users JOIN balances ON owner=id").fetchall() == [
            ("synthetic", 500)
        ]


if __name__ == "__main__":
    server = ThreadedMotoServer(ip_address="127.0.0.1", port=0, verbose=False)
    server.start()
    try:
        with tempfile.TemporaryDirectory() as directory:
            host, port = server.get_host_and_port()
            exercise(Path(directory), f"http://{host}:{port}")
    finally:
        server.stop()
