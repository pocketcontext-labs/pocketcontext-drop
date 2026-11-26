#!/usr/bin/env python3
"""Opt-in cross-product acceptance on a disposable, KVM-capable GitHub runner.

Starts a temporary hosted Drop with fake AWS and a loopback TLS SMTP mailbox.
Only the NibRunner manifest command needs root. Never point this test at a deployed host.
"""
import argparse
from email import policy
from email.parser import BytesParser
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import ssl
import subprocess
import sys
import tempfile

from aiosmtpd.controller import Controller
from aiosmtpd.smtp import AuthResult, LoginPassword

from drop_e2e import Server
from e2e import APP_DOMAIN, free_port, HOSTED_DOMAIN, isolated_env

REPO = Path(__file__).resolve().parents[1]
CREW_SHA = '90b0cdf5b94c37baf1d1e472f833e9b2d0df98e0361b4132bc81ecfe380a29db'


class Mailbox:
    def __init__(self, directory):
        self.directory = directory

    async def handle_DATA(self, server, session, envelope):
        msg = BytesParser(policy=policy.default).parsebytes(envelope.content)
        code = re.fullmatch(r'(\d{6}) is your Drop sign-in code', str(msg['Subject']))
        # No mail is sent off this loopback test host.
        if not code or len(envelope.rcpt_tos) != 1 or envelope.rcpt_tos[0] not in ('desktop@example.test', 'mobile@example.test'):
            return '550 Unrecognized acceptance message'
        mailbox = self.directory / envelope.rcpt_tos[0]
        temp = mailbox.with_suffix('.new')
        temp.write_text(code[1])
        temp.replace(mailbox)
        return '250 OK'


def test_smtp(root):
    cert = root / 'cert.pem'
    key = root / 'key.pem'
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                    '-subj', '/CN=localhost', '-addext', 'subjectAltName=IP:127.0.0.1',
                    '-keyout', key, '-out', cert], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain(cert, key)

    def auth(server, session, envelope, mechanism, credentials):
        valid = isinstance(credentials, LoginPassword) and credentials.login == b'test' and credentials.password == b'test'
        return AuthResult(success=valid, handled=False)

    return Controller(Mailbox(root), hostname='127.0.0.1', port=free_port(), tls_context=tls, require_starttls=True,
                      authenticator=auth, auth_required=True), cert


def scenario(root, drop, crew, nib):
    with crew.open('rb') as source:
        assert hashlib.file_digest(source, 'sha256').hexdigest() == CREW_SHA, 'unexpected Crew release digest'
    server_root = root / 'server'
    home = root / 'home'
    server_root.mkdir()
    home.mkdir()
    mailbox, cert = test_smtp(root)
    env = isolated_env(home)
    env.update(DROP_OWNER_EMAIL='owner@example.test', DROP_SMTP_FROM='login@example.test', DROP_APP_DOMAIN=APP_DOMAIN,
               DROP_SMTP_HOST='127.0.0.1', DROP_SMTP_PORT=str(mailbox.port), DROP_SMTP_USERNAME='test', DROP_SMTP_PASSWORD='test', DROP_SMTP_TLS='starttls',
               DROP_CREW_REPLICA_BUCKET='replicas-example', DROP_AWS_REGION='eu-north-1', DROP_TERMS_URL='https://drop.example.test/terms', DROP_PRIVACY_URL='https://drop.example.test/privacy',
               DROP_TERMS_VERSION='acceptance-1', SSL_CERT_FILE=str(cert))
    server = Server(drop, server_root, env)
    probe = root / 'run-probe'
    probe.write_text('#!/bin/sh\nexec sudo -n -- ' + shlex.quote(str(nib / 'nibrunnerd-linux-x64')) + ' "$@"\n')
    probe.chmod(0o700)
    config = root / 'nib.toml'
    config.write_text(f'''max_apps = 1
lifecycle_hook_timeout_seconds = 10
max_concurrent_vm_starts = 1
[cron]
max_jobs_per_app = 1
time_zone = "UTC"
[paths]
state_dir = "{root}/state"
runtime_dir = "{root}/runtime"
snapshot_dir = "{root}/snapshots"
guest_image_dir = "{nib}"
desired_state_file = "{root}/desired.json"
versions_file = "{root}/versions.json"
[artifacts]
store_url = "{root}/artifacts"
[volumes]
backend = "local-file"
storage_prefix = "volumes"
[exports]
store_url = "{root}/exports"
staging_dir = "{root}/staging"
[network]
denied_egress_addresses_v4 = []
denied_egress_addresses_v6 = []
''')

    def inspect(runner):
        result = subprocess.run([sys.executable, REPO / 'scripts/probe_release.py', crew, '--sha256', CREW_SHA,
                                 '--drop', drop, '--db', server.db, '--nibrunner', runner, '--config', config],
                                env=env, stdout=subprocess.PIPE, text=True, check=True, timeout=160)
        return result.stdout.strip()

    def owner_op(name, **payload):
        return server.owner('op', name, '-', stdin=json.dumps(payload))

    mailbox.start()
    try:
        server.start(init=True)
        owner_op('beta.approve', sha256=CREW_SHA)
        assert inspect(probe) == 'Manifest cached.'
        subprocess.run(['sudo', '-n', 'test', '!', '-e', '/run/nibrunner-manifest/work'], check=True)
        server.stop()
        server.start(init=False)
        assert inspect(root / 'absent-nibrunner') == 'Manifest already cached.'
        result = owner_op('beta.inspect', sha256=CREW_SHA)
        assert result['name'] == 'PocketContext Crew' and result['version'] == '0.45.4-drop.4', result
        assert 4 in result['multipliers'] and all(x >= 4 for x in result['multipliers']), result
        print('Released Crew manifest cached, inspector jail removed and cache reused after Drop restart.', flush=True)
        for name in ('desktop', 'mobile'):
            server.owner('invite', 'create', name + '@example.test', '--role', 'user', '--quota', '1')
        # Use the caller’s Playwright cache, separate from Drop’s isolated HOME.
        subprocess.run(['node', REPO / 'tests/beta_kvm_browser.mjs', str(server.port), str(crew), str(root)],
                       check=True, timeout=240)
        owner_op('beta.revoke', sha256=CREW_SHA)
        assert server.owner('op', 'beta.inspect', '-', stdin=json.dumps({'sha256': CREW_SHA}), expect=4).returncode == 4
        print('Revocation denied the previously cached release.')
    finally:
        server.stop()
        mailbox.stop()


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    for opt in ('drop', 'crew', 'nib-release'):
        p.add_argument('--' + opt, type=Path, required=True)
    args = p.parse_args()
    if os.environ.get('GITHUB_ACTIONS') != 'true' or os.environ.get('RUNNER_ENVIRONMENT') != 'github-hosted':
        sys.exit('Run the Drop Beta KVM acceptance workflow on a disposable GitHub-hosted runner.')
    with tempfile.TemporaryDirectory(prefix='drop-beta-') as tmp:
        scenario(Path(tmp), args.drop.resolve(), args.crew.resolve(), args.nib_release.resolve())
