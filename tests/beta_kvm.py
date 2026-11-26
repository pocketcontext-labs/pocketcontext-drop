#!/usr/bin/env python3
"""Opt-in cross-product acceptance on a disposable, KVM-capable GitHub runner.

Starts a temporary hosted Drop with fake AWS and a loopback TLS SMTP mailbox.
Only the NibRunner manifest command needs root. Never point this test at a deployed host.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import time

from hosted_mail import read_mail, start_mailbox

from drop_e2e import Server
from e2e import APP_DOMAIN, free_port, HOSTED_DOMAIN, isolated_env

REPO = Path(__file__).resolve().parents[1]
CREW_RELEASES = {
    '90b0cdf5b94c37baf1d1e472f833e9b2d0df98e0361b4132bc81ecfe380a29db': '0.45.4-drop.4',
    'edeb0740870ba881c005e6a309f444206c0c133e821f69b190bcc13563923b6a': '0.45.4-drop.5',
}


def scenario(root, drop, crew, nib):
    with crew.open('rb') as source:
        crew_sha = hashlib.file_digest(source, 'sha256').hexdigest()
    assert crew_sha in CREW_RELEASES, 'unexpected Crew release digest'
    version = CREW_RELEASES[crew_sha]
    server_root = root / 'server'
    home = root / 'home'
    server_root.mkdir()
    home.mkdir()
    mailbox, cert = start_mailbox(root, free_port)
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
        result = subprocess.run([sys.executable, REPO / 'scripts/probe_release.py', crew, '--sha256', crew_sha,
                                 '--drop', drop, '--db', server.db, '--nibrunner', runner, '--config', config],
                                env=env, stdout=subprocess.PIPE, text=True, check=True, timeout=160)
        return result.stdout.strip()

    def generic_probe_cases():
        # Exercise the actual socket worker against the released KVM inspector,
        # using this test's temporary host config and no production socket.
        tools = root / 'probe-tools'
        tools.mkdir()
        wrapper = tools / 'nibrunnerd'
        wrapper.write_text('#!/bin/sh\n'
                           'test "$1" = probe-manifest && test "$2" = --config || exit 1\n'
                           'shift 3\nexec ' + shlex.quote(str(probe)) +
                           ' probe-manifest --config ' + shlex.quote(str(config)) + ' "$@"\n')
        wrapper.chmod(0o700)
        worker_env = {**env, 'PATH': str(tools) + os.pathsep + env['PATH']}
        for name, statement in [('invalid', 'puts("invalid manifest");'), ('hanging', 'for (;;) pause();')]:
            source = root / (name + '.c')
            binary = root / name
            source.write_text('#include <stdio.h>\n#include <unistd.h>\nint main(void) { ' + statement + ' return 0; }\n')
            subprocess.run(['cc', '-static', '-Os', '-o', binary, source], check=True)
            data = binary.read_bytes()
            digest = hashlib.sha256(data).hexdigest()
            header = f'{digest} {len(data)}\n'.encode()
            output = subprocess.run([drop, 'agent', 'manifest-probe'], input=header + data,
                                    stdout=subprocess.PIPE, env=worker_env, check=True, timeout=90)
            receipt = json.loads(output.stdout)
            assert receipt == {'outcome': 'invalid'}, (name, receipt)
            attempt = owner_op('beta.claim', sha256=digest)['attempt']
            owner_op('beta.complete', sha256=digest, attempt=attempt, outcome=receipt['outcome'])
            result = owner_op('beta.inspect', sha256=digest)
            assert result['generic'] and result['suggested_multiplier'] == 1, result
        print('Non-manifest and hanging executables both received a generic profile after a real isolated probe.', flush=True)

    def owner_op(name, **payload):
        return server.owner('op', name, '-', stdin=json.dumps(payload))

    try:
        server.start(init=True)
        generic_probe_cases()
        assert inspect(probe) == 'Manifest cached.'
        subprocess.run(['sudo', '-n', 'test', '!', '-e', '/run/nibrunner-manifest/work'], check=True)
        server.stop()
        server.start(init=False)
        assert inspect(root / 'absent-nibrunner') == 'Manifest already cached.'
        result = owner_op('beta.inspect', sha256=crew_sha)
        assert result['name'] == 'PocketContext Crew' and result['version'] == version, result
        assert result['multipliers'] == list(range(1, 9)) and result['suggested_multiplier'] == 4, result
        print('Released Crew manifest cached, inspector jail removed and cache reused after Drop restart.', flush=True)
        browser_command = ['node', REPO / 'tests/beta_kvm_browser.mjs', str(server.port), str(crew), str(root)]
        subprocess.run([*browser_command, 'join'], check=True, timeout=180)
        # The sign-in form requests an OTP before approval. Even unknown addresses
        # have a one-minute send cooldown so that eligibility stays private.
        next_signin = time.monotonic() + 60
        for name in ('desktop', 'mobile'):
            email = name + '@example.test'
            entries = owner_op('waitlist.list', search=email)['entries']
            assert len(entries) == 1 and entries[0]['email'] == email
            owner_op('waitlist.invite', id=entries[0]['id'])
            assert read_mail(root / (email + '.welcome')) == 'received'
        # Use the caller’s Playwright cache, separate from Drop’s isolated HOME.
        time.sleep(max(0, next_signin - time.monotonic()))
        storage = 'required' if version == '0.45.4-drop.4' else 'optional'
        assert 's3_replica' in result['requires' if storage == 'required' else 'optional'], result
        subprocess.run([*browser_command, 'deploy', storage], check=True, timeout=240)
        owner_op('beta.revoke', sha256=crew_sha)
        assert server.owner('op', 'beta.inspect', '-', stdin=json.dumps({'sha256': crew_sha}), expect=4).returncode == 4
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
