#!/usr/bin/env python3
"""End-to-end test of the node agent: the real drop binary on both sides.

A hosted `drop serve --fake-aws DIR` on loopback is the control plane; the
real `drop agent run --fake-aws DIR` is the node, writing nibrunner's files
in a temporary folder (never /var/lib/nibrunner). The test plays nibrunner
by writing reported.json. It checks:

  1. `drop agent check` passes and writes nothing;
  2. the agent stages the release by sha256, resolves the secret version and
     writes desired.json (0600) with desired.json.drop-prev beside it;
  3. a fake reported.json makes the app's status `running`;
  4. a change on the server reaches desired.json through realtime;
  5. with the server stopped the agent keeps retrying and never writes;
     after a restart it converges again;
  6. a desired.json holding an entry not starting with d- makes `run` and
     `check` exit 3 naming it, without writing;
  7. no secret value or node password in state.json or the agent's output.

Standard library only. Usage: python3 tests/drop_e2e.py --binary bin/drop
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import stat
import subprocess
import sys
import tempfile
import time

import leak_scan

from e2e import (APP_DOMAIN, HOSTED_DOMAIN, build_hello, check, free_port, hosted_request, isolated_env,
                 listening)

HOST_ID = 'host-agent-e2e'
NODE = 'node-agent'
SENTINEL = 'synthetic-agent-sentinel-' + hashlib.sha256(b'drop agent e2e').hexdigest()[:16]
EXIT_REFUSED = 3


def wait_for(what, condition, timeout=60, interval=0.2):
    deadline = time.time() + timeout
    while True:
        value = condition()
        if value:
            return value
        if time.time() > deadline:
            raise AssertionError(f'timed out waiting for {what}')
        time.sleep(interval)


def read_json(path):
    try:
        return json.loads(Path(path).read_text(encoding='utf-8'))
    except (OSError, ValueError):
        return None


class Server:
    """The hosted control plane with the fake AWS directory."""

    def __init__(self, binary, root, env):
        self.binary, self.root, self.env = binary, root, env
        self.db = root / 'drop.db'
        self.fake_aws = root / 'fake-aws'
        self.port = free_port()
        self.process = None
        self.log = None

    def start(self, init):
        command = [str(self.binary), '--db', str(self.db), 'serve', '--hosted', '--headless', '--mcp',
                   '--domain', HOSTED_DOMAIN, '--hosted-http-address', f'127.0.0.1:{self.port}', '--fake-aws', str(self.fake_aws)]
        if init:
            command.append('--hosted-init')
        self.log = open(self.root / 'serve.log', 'a', encoding='utf-8')
        self.process = subprocess.Popen(command, stdout=self.log, stderr=subprocess.STDOUT, env=self.env)
        deadline = time.time() + 60
        while not listening(self.port):
            if self.process.poll() is not None or time.time() > deadline:
                raise AssertionError(f'the hosted server did not start: {(self.root / "serve.log").read_text()[-2000:]}')
            time.sleep(0.2)

    def stop(self):
        if self.process is None:
            return
        self.process.send_signal(signal.SIGTERM)
        try:
            self.process.wait(timeout=60)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait()
        self.log.close()
        self.process = None

    def owner(self, *args, stdin=None, expect=0):
        """A command as the owner through the private socket."""
        result = subprocess.run([str(self.binary), '--db', str(self.db), *args], input=stdin, capture_output=True, text=True,
                                encoding='utf-8', env=self.env, timeout=120)
        if result.returncode != expect:
            raise AssertionError(f'drop {" ".join(args)}: exit {result.returncode}\n{result.stdout}\n{result.stderr}')
        return json.loads(result.stdout) if expect == 0 else result


class Node:
    """The agent's host: nibrunner's files in a temporary folder."""

    def __init__(self, binary, root, server, password, env):
        self.binary, self.root, self.server = binary, root, server
        self.nib = root / 'nibrunner'
        self.nib.mkdir(parents=True)
        self.desired = self.nib / 'desired.json'
        self.reported = self.nib / 'reported.json'
        self.artifacts = self.nib / 'artifact-store'
        self.state_dir = root / 'drop-agent'
        self.secret = root / 'node-secret'
        self.secret.write_text(password + '\n', encoding='utf-8')
        self.secret.chmod(0o600)
        self.work = root / 'work'  # the agent's working directory: it must create nothing here
        self.work.mkdir()
        self.env = {**env,
                    'DROP_AGENT_SERVER': f'http://127.0.0.1:{server.port}', 'DROP_AGENT_HOST': HOSTED_DOMAIN,
                    'DROP_AGENT_NODE': NODE, 'DROP_AGENT_SECRET_FILE': str(self.secret),
                    'DROP_AGENT_DESIRED': str(self.desired), 'DROP_AGENT_REPORTED': str(self.reported),
                    'DROP_AGENT_ARTIFACTS': str(self.artifacts), 'DROP_AGENT_STATE_DIR': str(self.state_dir),
                    'DROP_AGENT_ALLOWED_HOST_SUFFIX': '.' + APP_DOMAIN, 'DROP_SSM_PREFIX': '/drop',
                    'DROP_AGENT_PULL_INTERVAL': '5', 'DROP_AGENT_REPORT_INTERVAL': '5',
                    # Remote mode must not apply to the agent.
                    'DROP_URL': 'http://127.0.0.1:1'}
        self.process = None
        self.log_path = root / 'agent.log'

    def write_empty(self, revision='empty-1'):
        self.desired.write_text(json.dumps({'hostId': HOST_ID, 'revision': revision, 'instances': [], 'volumes': [],
                                            'checkpoints': [], 'exports': []}) + '\n', encoding='utf-8')
        self.desired.chmod(0o600)

    def write_reported(self, instances=(), accepted=None):
        doc = {'hostId': HOST_ID, 'state': 'ready', 'reportedAt': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
               'acceptedRevision': accepted, 'acceptedDigest': None,
               'capacity': {'vcpuCount': 2, 'memoryMib': 7136, 'cacheBytes': 1 << 30},
               'allocatable': {'vcpuCount': 2, 'memoryMib': 7136, 'cacheBytes': 1 << 30},
               'versions': {'agent': 'fake', 'firecracker': 'fake', 'guestImage': 'fake', 'zerofs': 'fake'},
               'checkpoints': [], 'exports': [], 'volumes': [], 'instances': list(instances),
               # Tenant environment that must never reach a report.
               'cron': [{'appId': 'd-' + 'c' * 15, 'deploymentId': 'd-' + 'c' * 15 + '-dep-1', 'enabled': True, 'timeZone': 'UTC',
                         'jobs': [{'jobId': 'j', 'command': 'x', 'schedule': '* * * * *', 'nextRunAt': None,
                                   'environment': {'API_KEY': SENTINEL}}]}]}
        self.reported.write_text(json.dumps(doc), encoding='utf-8')

    def agent(self, *args, expect=0, timeout=120):
        result = subprocess.run([str(self.binary), 'agent', *args, '--fake-aws', str(self.server.fake_aws)], capture_output=True,
                                text=True, encoding='utf-8', env=self.env, cwd=self.work, timeout=timeout)
        if expect is not None and result.returncode != expect:
            raise AssertionError(f'drop agent {" ".join(args)}: exit {result.returncode}, expected {expect}\n{result.stdout}\n{result.stderr}')
        return result

    def start(self):
        self.log = open(self.log_path, 'a', encoding='utf-8')
        self.process = subprocess.Popen([str(self.binary), 'agent', 'run', '--fake-aws', str(self.server.fake_aws)],
                                        stdout=self.log, stderr=subprocess.STDOUT, env=self.env, cwd=self.work)

    def stop(self):
        if self.process is None:
            return None
        self.process.send_signal(signal.SIGTERM)
        try:
            code = self.process.wait(timeout=30)
        except subprocess.TimeoutExpired:
            self.process.kill()
            code = self.process.wait()
        self.log.close()
        self.process = None
        return code

    def logs(self):
        return self.log_path.read_text(encoding='utf-8', errors='replace')

    def revision(self):
        doc = read_json(self.desired)
        return doc.get('revision', '') if doc else ''


def scenario(binary, root):
    env = isolated_env(root / 'home')
    (root / 'home').mkdir()
    env.update({'DROP_OWNER_EMAIL': 'owner@example.test', 'DROP_SMTP_FROM': 'login@example.test', 'DROP_APP_DOMAIN': APP_DOMAIN,
                'DROP_SMTP_HOST': '127.0.0.1', 'DROP_SMTP_PORT': '9', 'DROP_SMTP_USERNAME': 'synthetic', 'DROP_SMTP_PASSWORD': 'synthetic',
                'DROP_USAGE_LOG_BUCKET': 'logs-example', 'DROP_USAGE_LOG_PREFIX': 'access-logs/111122223333/eu-north-1/replicas-example/'})
    server_root = root / 'server'
    server_root.mkdir()
    server = Server(binary, server_root, env)
    server.start(init=True)
    node = None
    try:
        check(hosted_request(server.port, 'GET', '/up', host='127.0.0.1')[0] == 200, 'the hosted server answers /up')
        app = server.owner('app', 'create', 'Agent app', '--slug', 'agent-app', '--port', '80')['app']
        server.owner('secret', 'set', 'agent-app', 'API_KEY', stdin=SENTINEL + '\n')
        hello = build_hello(server_root)
        hello_bytes = hello.read_bytes()
        hello_sha = hashlib.sha256(hello_bytes).hexdigest()
        uploaded = server.owner('release', 'upload', 'agent-app', str(hello), '--activate')
        check(uploaded['release']['sha256'] == hello_sha, 'the release is uploaded and deployed')
        created = server.owner('node', 'create', NODE, '--host-id', HOST_ID)
        server.owner('place', 'agent-app', NODE)
        app_id = app['nib_app_id']

        node = Node(binary, root / 'node', server, created['password'], env)
        node.write_empty()
        node.write_reported()
        empty = node.desired.read_bytes()

        # 1. check passes and writes nothing.
        result = node.agent('check')
        report = json.loads(result.stdout)
        check(report['ok'] and not report['refused'] and report['hostId'] == HOST_ID
              and {c['name'] for c in report['checks']} >= {'desired.json', 'reported.json', 'sign-in', 'desired document', 'releases', 'secrets'},
              f'drop agent check passes: {[c["name"] + ":" + str(c["ok"]) for c in report["checks"]]}')
        check(SENTINEL not in result.stdout + result.stderr and created['password'] not in result.stdout + result.stderr,
              'check prints no secret value or password')
        check(node.desired.read_bytes() == empty and not node.state_dir.exists() and not node.artifacts.exists()
              and not Path(str(node.desired) + '.drop-prev').exists(), 'check writes nothing')
        check(list(node.work.iterdir()) == [], 'the agent creates nothing in its working directory (no project, no database)')

        # 2. run: stage, resolve, write.
        node.start()
        wait_for('the first desired.json write', lambda: node.revision().startswith('drop-'))
        doc = read_json(node.desired)
        mode = stat.S_IMODE(node.desired.stat().st_mode)
        check(mode == 0o600, f'desired.json is 0600 ({oct(mode)})')
        check(doc['hostId'] == HOST_ID and [i['appId'] for i in doc['instances']] == [app_id]
              and [v['volumeId'] for v in doc['volumes']] == [app_id + '-vol'], 'desired.json holds the placed app and its volume')
        instance = doc['instances'][0]
        check(instance['config']['httpPort'] == 80, 'the agent renders the configured guest HTTP port 80')
        check(instance['deploymentId'] == uploaded['deployment_id'] and instance['layers'][0]['objectKey'] == hello_sha
              and instance['layers'][0]['digest'] == hello_sha, 'the instance runs the deployed release by sha256')
        check(instance['config']['command']['environment'].get('API_KEY') == SENTINEL, 'the secret version is resolved from the store')
        check(instance['hostnames'] == [{'hostname': 'agent-app.' + APP_DOMAIN, 'kind': 'platform'}], 'the hostname is the platform one')
        staged = node.artifacts / hello_sha
        check(staged.is_file() and staged.read_bytes() == hello_bytes and stat.S_IMODE(staged.stat().st_mode) == 0o600,
              'the release is staged under its sha256 (0600)')
        check(Path(str(node.desired) + '.drop-prev').read_bytes() == empty, 'desired.json.drop-prev holds the previous file')
        state = wait_for('the first state.json write', lambda: read_json(node.state_dir / 'state.json'))
        check(state and state['schema'] == 'drop.agent-state/v1' and state['hostId'] == HOST_ID and state['lastRevision'] == node.revision()
              and state['artifacts'].get(hello_sha, {}).get('size') == len(hello_bytes), f'state.json records the applied document: {state}')

        # 3. nibrunner reports the app running.
        revision = node.revision()
        node.write_reported([{'appId': app_id, 'deploymentId': instance['deploymentId'], 'state': 'running', 'restartCount': 0,
                              'compute': None, 'convergedAt': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}], accepted=revision)
        status = wait_for('the app to be running', lambda: (lambda s: s if s['phase'] == 'running' else None)(server.owner('app', 'status', 'agent-app')))
        check(status['node'] == NODE and status['reported_deployment_id'] == instance['deploymentId'],
              f'drop app status shows running from the agent\'s report: {status["phase"]}')
        nodes = server.owner('node', 'list')['nodes']
        check(nodes[0]['online'] and nodes[0]['agent_version'], 'the node is online with its agent version')

        # 4. A change on the server reaches desired.json through realtime.
        changed = server.owner('app', 'set', 'agent-app', '--memory', '256')
        check(changed['deployment_bumped'], 'app set starts a new deployment')
        started = time.time()
        wait_for('the new deployment in desired.json', lambda: read_json(node.desired)['instances'][0]['deploymentId'] == changed['app']['nib_app_id'] + '-dep-' + str(changed['app']['deployment_seq']), timeout=30)
        check(time.time() - started < 4.5, f'the change arrived through realtime before the 5 s ticker ({time.time() - started:.1f} s)')
        check(read_json(node.desired)['instances'][0]['config']['resources']['memoryMib'] == 256, 'the new memory size is written')

        # 4b. Usage: S3 access logs in the fake log bucket are metered per app;
        # past the hard limit the app stops and desired.json follows.
        server.owner('usage', 'limits', 'agent-app', '--requests-b', '2')
        now = time.gmtime()
        stamp = time.strftime('%d/%b/%Y:%H:%M:%S +0000', now)
        line = (f'0000 replicas-example [{stamp}] 192.0.2.10 arn:aws:sts::111122223333:assumed-role/workloads/{app_id} SYNTHREQ '
                f'REST.GET.OBJECT {app_id}/db "GET /{app_id}/db HTTP/1.1" 200 - 100 100 10 9 "-" "synthetic" - SYNTHHOST= '
                'SigV4 TLS_AES_128_GCM_SHA256 AuthHeader replicas-example.s3.eu-north-1.amazonaws.com TLSv1.3 - -')
        log_dir = server.fake_aws / 'usage' / 'logs' / 'access-logs/111122223333/eu-north-1/replicas-example' / time.strftime('%Y/%m/%d', now)
        log_dir.mkdir(parents=True)
        (log_dir / (time.strftime('%Y-%m-%d', now) + '-00-00-00-SYNTHETIC0001')).write_text((line + '\n') * 25 + 'malformed\n', encoding='utf-8')
        collected = server.owner('usage', 'collect')
        check(collected['objects_processed'] == 1 and collected['malformed'] == 1 and collected['stopped'] == [app['id']],
              f'drop usage collect meters the log and stops the app past its hard limit: {collected}')
        again = server.owner('usage', 'collect')
        check(again['objects_processed'] == 0 and again['stopped'] == [], 'a second run counts nothing twice')
        shown = server.owner('usage', 'show', 'agent-app')
        check(shown['month_to_date']['class_b'] == 25 and sorted(e['level'] for e in shown['exceeded']) == ['hard', 'soft'],
              f'drop usage show reports the month and the exceeded limits: {shown["month_to_date"]}')
        wait_for('the usage stop in desired.json', lambda: read_json(node.desired)['instances'][0]['desiredState'] == 'stopped', timeout=30)
        check(True, 'the hard-limit stop reaches desired.json')
        server.owner('app', 'start', 'agent-app')
        wait_for('the restart in desired.json', lambda: read_json(node.desired)['instances'][0]['desiredState'] != 'stopped', timeout=30)

        # 5. Server down: no write, the agent keeps retrying.
        node.write_reported([], accepted=node.revision())
        before = node.desired.read_bytes()
        before_mtime = node.desired.stat().st_mtime_ns
        server.stop()
        time.sleep(12)
        check(node.process.poll() is None, 'the agent keeps running while the server is down')
        check(node.desired.read_bytes() == before and node.desired.stat().st_mtime_ns == before_mtime,
              'desired.json is untouched while the server is down')
        check(node.logs().count('not reachable') >= 2, 'the agent keeps retrying (pulls and reconnects logged)')
        server.start(init=False)
        server.owner('app', 'stop', 'agent-app')
        wait_for('the stopped state after the restart', lambda: read_json(node.desired)['instances'][0]['desiredState'] == 'stopped', timeout=45)
        check(True, 'after the server restarts the agent converges again (app stop reaches desired.json)')

        code = node.stop()
        check(code == 0, f'the agent stops cleanly on SIGTERM (exit {code})')

        # 6. Foreign entries: run and check refuse, write nothing.
        foreign = json.loads(node.desired.read_text(encoding='utf-8'))
        foreign['instances'].append(dict(foreign['instances'][0], appId='canary', volumeId='canary-vol', deploymentId='canary-dep-1'))
        foreign['volumes'].append({'appId': 'canary', 'volumeId': 'canary-vol', 'sizeBytes': 1 << 28, 'desiredState': 'present'})
        node.desired.write_text(json.dumps(foreign), encoding='utf-8')
        foreign_bytes = node.desired.read_bytes()
        prev_bytes = Path(str(node.desired) + '.drop-prev').read_bytes()
        state_bytes = (node.state_dir / 'state.json').read_bytes()
        refused = node.agent('check', expect=EXIT_REFUSED)
        check('canary' in refused.stdout and json.loads(refused.stdout)['refused'], 'drop agent check exits 3 and names the foreign entry')
        refused = node.agent('run', expect=EXIT_REFUSED, timeout=60)
        check('canary' in refused.stderr, f'drop agent run refuses to start and names the foreign entry: {refused.stderr.strip()[-200:]}')
        check(node.desired.read_bytes() == foreign_bytes and Path(str(node.desired) + '.drop-prev').read_bytes() == prev_bytes
              and (node.state_dir / 'state.json').read_bytes() == state_bytes, 'a refused run or check writes nothing')

        # 7. No secret value or password outside desired.json.
        state_text = (node.state_dir / 'state.json').read_text(encoding='utf-8')
        logs = node.logs()
        password = created['password']
        check(SENTINEL not in state_text and password not in state_text and 'token' not in state_text.lower(),
              'state.json holds no secret value, password or token')
        check(SENTINEL not in logs and password not in logs, 'the agent output holds no secret value or password')
        check(SENTINEL not in (server_root / 'serve.log').read_text(encoding='utf-8', errors='replace'),
              'the server output holds no secret value (not even from the report)')
        # The shared leak scan (tests/leak_scan.py) over everything but nibrunner's
        # input files, which must hold the value, and the fake secret store.
        leak_scan.require_clean({'secret value': SENTINEL, 'node password': password},
                                paths=[node.state_dir, server_root, node.artifacts],
                                texts={'agent output': logs},
                                exclude=[server.fake_aws / 'secrets'])
        check(True, 'the leak scan finds no secret value or node password in state.json, artifacts, the server database, log or release store, or the agent output')
        check(list(node.work.iterdir()) == [], 'the agent never created a project in its working directory')
    finally:
        if node is not None:
            node.stop()
        server.stop()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True, type=Path)
    binary = parser.parse_args().binary.resolve()
    with tempfile.TemporaryDirectory(prefix='drop-agent-e2e-') as temp:
        scenario(binary, Path(temp))
    print('all agent end-to-end checks passed')


if __name__ == '__main__':
    try:
        main()
    except AssertionError as error:
        print('FAIL -', error)
        sys.exit(1)
