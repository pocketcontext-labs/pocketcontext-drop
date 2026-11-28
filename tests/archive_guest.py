"""Boot an agent-staged executable using a private Firecracker VM and real NibRunner kernel/rootfs.
The fixture supplies guest init, a private vsock HTTP bridge, and a per-app ext4 volume.
"""

import contextlib
import http.client
import json
import queue
import re
import shutil
import socket
import subprocess
import threading
import time
from pathlib import Path


def command(argv, **kwargs):
    subprocess.run(
        argv, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, **kwargs
    )


def vsock(path, port):
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.settimeout(5)
    try:
        s.connect(str(path))
        s.sendall(f"CONNECT {port}\n".encode())
        with s.makefile("rb", buffering=0) as f:
            if not f.readline(64).startswith(b"OK "):
                raise ConnectionError("Guest rejected vsock connection")
        return s
    except BaseException:
        s.close()
        raise


class Guest:
    def __init__(self, path):
        self.path = path

    def request(self, route, body=None, headers=None, method=None):
        data = None if body is None else json.dumps(body).encode()
        c = http.client.HTTPConnection("127.0.0.1", timeout=5)
        c.sock = vsock(self.path, 10000)
        try:
            c.request(
                method or ("GET" if data is None else "POST"),
                route,
                body=data,
                headers={
                    "Content-Type": "application/json",
                    "Connection": "close",
                    **(headers or {}),
                },
            )
            r = c.getresponse()
            return r.status, r.read()
        finally:
            c.close()

    def log(self):
        with vsock(self.path, 10001) as s, s.makefile("rb") as f:
            return f.read(65536).decode(errors="replace")


@contextlib.contextmanager
def boot(root, release, instance, firecracker, nib, relay, volume):
    root.mkdir()
    layer = root / "root.ext4"
    shutil.copyfile(nib / "rootfs.ext4", layer)
    with layer.open("ab") as f:
        f.truncate(512 << 20)
    command(["resize2fs", "-f", layer])
    if not volume.exists():
        with volume.open("wb") as f:
            f.truncate(128 << 20)
        command(["mke2fs", "-q", "-t", "ext4", "-F", volume])
    destination = instance["layers"][0]["destinationPath"]
    assert (
        destination.startswith("/app/")
        and destination.count("/") == 2
        and " " not in destination
    )
    (root / "instance.json").write_text(json.dumps(instance))
    init = root / "fixture-init"
    init.write_text("""#!/busybox sh
set -eu
test -b /dev/vda || /busybox mount -t devtmpfs devtmpfs /dev
/busybox mount -t proc proc /proc
/busybox mount -t sysfs sysfs /sys
/busybox mount -t tmpfs tmpfs /tmp
/busybox chmod 1777 /tmp
/busybox ip link set lo up
/busybox mount -t ext4 /dev/vdb /app/data
/busybox chown 65534:65534 /app /app/data
exec /drop-relay
""")
    init.chmod(0o755)
    commands = ["mkdir /app", "mkdir /app/data"]
    for source, target in [
        (release, destination),
        (Path("/bin/busybox"), "/busybox"),
        (init, "/drop-init"),
        (relay, "/drop-relay"),
        (root / "instance.json", "/drop-instance.json"),
    ]:
        commands.append(f"write {source} {target}")
        commands.append(f"set_inode_field {target} mode 0100755")
    command(
        ["debugfs", "-w", "-f", "/dev/stdin", layer],
        input=("\n".join(commands) + "\n").encode(),
    )
    path = root / "vsock"
    config = {
        "boot-source": {
            "kernel_image_path": str(nib / "vmlinux"),
            "boot_args": "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw init=/drop-init",
        },
        "drives": [
            {
                "drive_id": "root",
                "path_on_host": str(layer),
                "is_root_device": True,
                "is_read_only": False,
            },
            {
                "drive_id": "data",
                "path_on_host": str(volume),
                "is_root_device": False,
                "is_read_only": False,
            },
        ],
        "machine-config": {
            "vcpu_count": 1,
            "mem_size_mib": instance["config"]["resources"]["memoryMib"],
        },
        "vsock": {"guest_cid": 5, "uds_path": str(path)},
    }
    (root / "firecracker.json").write_text(json.dumps(config))
    with (root / "serial.log").open("w") as serial:
        p = subprocess.Popen(
            [firecracker, "--no-api", "--config-file", root / "firecracker.json"],
            stdout=serial,
            stderr=serial,
        )
        g = Guest(path)
        try:
            deadline = time.monotonic() + 45
            while True:
                if p.poll() is not None:
                    raise RuntimeError(
                        "Firecracker exited; see private test serial log"
                    )
                try:
                    status, _ = g.request("/")
                    if 200 <= status < 500:
                        break
                except (OSError, http.client.HTTPException):
                    pass
                if time.monotonic() > deadline:
                    raise TimeoutError("Guest HTTP startup")
                time.sleep(0.2)
            yield g
        finally:
            try:
                vsock(path, 10002).close()
            except OSError:
                pass
            try:
                p.wait(timeout=10)
            except subprocess.TimeoutExpired:
                p.kill()
                p.wait()


@contextlib.contextmanager
def public_gateway(root):
    """Private TLS endpoint for the assigned hostnames. No DNS or host changes."""
    import http.server
    import ssl
    import threading

    guests = {}
    cert, key = root / "guest-cert.pem", root / "guest-key.pem"
    command(
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
            "/CN=*.apps.example.test",
            "-addext",
            "subjectAltName=DNS:*.apps.example.test",
            "-keyout",
            key,
            "-out",
            cert,
        ]
    )
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain(cert, key)

    class GuestHTTP(http.server.BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def forward(self):
            try:
                status, data = self.server.request(self.path, method=self.command)
                self.send_response(status)
                self.send_header("Content-Length", str(len(data)))
                # The test browser opens PocketBase's installation shell.
                if self.path.startswith("/_/"):
                    if ".js" in self.path:
                        self.send_header("Content-Type", "application/javascript")
                    elif ".css" in self.path:
                        self.send_header("Content-Type", "text/css")
                    else:
                        self.send_header("Content-Type", "text/html")
                self.end_headers()
                if self.command != "HEAD":
                    self.wfile.write(data)
            except (OSError, http.client.HTTPException):
                self.send_error(503)

        do_GET = do_HEAD = forward

    class Connect(http.server.BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_CONNECT(self):
            name, _, port = self.path.partition(":")
            guest = guests.get(name)
            if port != "443" or guest is None:
                self.send_error(502)
                return
            self.send_response_only(200, "Connection established")
            self.end_headers()
            try:
                with tls.wrap_socket(self.connection, server_side=True) as stream:
                    GuestHTTP(stream, self.client_address, guest)
            except (OSError, ssl.SSLError):
                pass

    proxy = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Connect)
    thread = threading.Thread(target=proxy.serve_forever, daemon=True)
    thread.start()
    try:
        yield guests, "http://127.0.0.1:" + str(proxy.server_port), cert
    finally:
        proxy.shutdown()
        proxy.server_close()
        thread.join()


def guest_request(
    guest,
    hostname,
    method,
    path,
    body=None,
    token="",
    content_type="application/json",
    expected=200,
):
    if isinstance(body, dict):
        body = json.dumps(body).encode()
    conn = http.client.HTTPConnection("127.0.0.1", timeout=15)
    conn.sock = vsock(guest.path, 10000)
    try:
        conn.request(
            method,
            path,
            body,
            {
                "Host": hostname,
                "Authorization": token,
                "Content-Type": content_type,
                "X-Forwarded-For": "192.0.2.25",
            },
        )
        response = conn.getresponse()
        data = response.read()
        assert response.status == expected, (
            method,
            path.split("?")[0],
            response.status,
            data[:150],
        )
        return data
    finally:
        conn.close()


def login(guest, hostname, email):
    # The release posts its six-digit code over the real guest-to-host vsock. Only the synthetic mailbox is replaced.
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as inbox:
        inbox.bind(str(guest.path) + "_51006")
        inbox.listen(1)
        codes = queue.Queue()

        def deliver():
            with inbox.accept()[0] as conn, conn.makefile("rb") as stream:
                code = json.loads(stream.readline(256))["code"]
                assert re.fullmatch(r"[0-9]{6}", code)
                conn.sendall(b'{"status":"accepted"}\n')
                codes.put(code)

        worker = threading.Thread(target=deliver, daemon=True)
        worker.start()
        otp = json.loads(
            guest_request(
                guest,
                hostname,
                "POST",
                "/api/collections/users/request-otp",
                {"email": email},
            )
        )
        token = json.loads(
            guest_request(
                guest,
                hostname,
                "POST",
                "/api/collections/users/auth-with-otp-code",
                {"otpId": otp["otpId"], "code": codes.get(timeout=15)},
            )
        )["token"]
        worker.join(2)
        return token
