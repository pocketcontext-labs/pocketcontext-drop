#!/usr/bin/env python3
"""A fake Drop agent: the agent API contract (docs/agent-api.md) in Python.

It talks to a real hosted `drop serve` over plain HTTP on loopback, sending
the hosted domain as the Host header, exactly as `drop agent` does:

  1. sign in as the node (POST /api/collections/nodes/auth-with-password),
     refresh the token (POST /api/collections/nodes/auth-refresh);
  2. open the realtime stream (GET /api/realtime), take the clientId from
     PB_CONNECT and subscribe to nodes/<node id> (POST /api/realtime);
  3. pull the drop.desired/v1 document (GET /api/drop/agent/desired) and
     check it field by field: no secret value, parameters at
     <prefix>/apps/<appId without d->/<NAME>, releases at releases/<sha256>;
  4. with --fake-aws, resolve each secret version and find each release in
     the directory the server writes (as the real agent reads SSM and S3);
  5. send a drop.report/v1 (POST /api/drop/agent/report).

tests/e2e.py imports it for its hosted scenario; run alone, it performs one
contract check against a running server and prints a JSON summary. Standard
library only. Usage:

  printf '%s' "$PASSWORD" | python3 tests/fake_agent.py --port 8100 \\
      --domain drop.example.test --node walter-aws --host-id HOST --fake-aws DIR
"""
import argparse
import hashlib
import http.client
import json
from pathlib import Path
import queue
import re
import socket
import sys
import threading
import time

DESIRED_SCHEMA = 'drop.desired/v1'
REPORT_SCHEMA = 'drop.report/v1'
APP_ID = re.compile(r'^d-[a-z0-9]{15}$')
SHA256 = re.compile(r'^[0-9a-f]{64}$')
DESIRED_KEYS = {'schema', 'node', 'nodeId', 'hostId', 'state', 'desiredSeq', 'contentHash', 'apps', 'rejected'}
APP_KEYS = {'appId', 'deploymentId', 'desiredState', 'idleTimeoutMs', 'volume', 'release', 'executableName', 'httpPort',
            'args', 'resources', 'healthCheck', 'hostnames', 'env', 'secrets'}
PHASES = {'pending', 'staging', 'staging_failed', 'secret_failed', 'deploying', 'starting', 'running', 'sleeping',
          'unhealthy', 'stopped', 'failed', 'unknown'}


class ContractError(AssertionError):
    pass


def need(condition, message):
    if not condition:
        raise ContractError(message)


class FakeAgent:
    def __init__(self, port, domain, node, password, host_id, fake_aws=None, ssm_prefix='/drop', version='0.0.0-fake'):
        self.port, self.domain, self.node, self.password = port, domain, node, password
        self.host_id, self.ssm_prefix, self.version = host_id, ssm_prefix, version
        self.fake_aws = Path(fake_aws) if fake_aws else None
        self.token = ''
        self.node_id = ''
        self.events = queue.Queue()
        self._stream = None
        self.last_error = None

    # HTTP -------------------------------------------------------------------

    def request(self, method, path, body=None, token=None, raw=None):
        """One request; returns (status, decoded JSON or None)."""
        connection = http.client.HTTPConnection('127.0.0.1', self.port, timeout=30)
        headers = {'Host': self.domain, 'Accept': 'application/json'}
        data = raw
        if body is not None:
            data = json.dumps(body).encode()
            headers['Content-Type'] = 'application/json'
        auth = self.token if token is None else token
        if auth:
            headers['Authorization'] = auth
        try:
            connection.request(method, path, body=data, headers=headers)
            response = connection.getresponse()
            payload = response.read()
            try:
                return response.status, json.loads(payload) if payload else None
            except ValueError:
                return response.status, None
        finally:
            connection.close()

    # Sign-in ----------------------------------------------------------------

    def login(self, password=None):
        # Sign-in is rate-limited per client address (Drop's and PocketBase's
        # own auth limits): back off and retry on 429, as the agent does.
        for attempt in range(4):
            status, body = self.request('POST', '/api/collections/nodes/auth-with-password',
                                        {'identity': self.node, 'password': password or self.password}, token='')
            if status != 429:
                break
            self.last_error = body
            time.sleep(3 * (attempt + 1))
        if status != 200:
            self.last_error = body
            return status
        need(body.get('token') and body.get('record', {}).get('collectionName') == 'nodes', f'node sign-in answer: {body}')
        need(body['record'].get('name') == self.node, 'the record is the node itself')
        need('password' not in body['record'] and 'tokenKey' not in body['record'], 'no credential in the node record')
        self.token, self.node_id = body['token'], body['record']['id']
        return status

    def refresh(self):
        status, body = self.request('POST', '/api/collections/nodes/auth-refresh')
        if status == 200:
            self.token = body['token']
        return status

    # Realtime ---------------------------------------------------------------

    def subscribe(self, timeout=15):
        """Open the realtime stream and subscribe to nodes/<self>; events
        (decoded record dicts) then arrive on self.events."""
        connection = http.client.HTTPConnection('127.0.0.1', self.port, timeout=None)
        connection.request('GET', '/api/realtime', headers={'Host': self.domain, 'Accept': 'text/event-stream'})
        response = connection.getresponse()
        need(response.status == 200, f'realtime stream: {response.status}')
        self._sock = connection.sock
        connected = queue.Queue()

        def read():
            event, data = '', []
            try:
                while True:
                    line = response.readline()
                    if not line:
                        return
                    line = line.decode('utf-8').rstrip('\r\n')
                    if line.startswith('event:'):
                        event = line[6:].strip()
                    elif line.startswith('data:'):
                        data.append(line[5:].strip())
                    elif line == '':
                        if event == 'PB_CONNECT':
                            connected.put(json.loads(''.join(data))['clientId'])
                        elif event:
                            self.events.put((event, json.loads(''.join(data))))
                        event, data = '', []
            except Exception:  # the stream closed (close(), a server restart): stop reading
                return

        thread = threading.Thread(target=read, daemon=True)
        thread.start()
        client_id = connected.get(timeout=timeout)
        status, body = self.request('POST', '/api/realtime', {'clientId': client_id, 'subscriptions': [f'nodes/{self.node_id}']})
        need(status == 204, f'subscribe nodes/<self>: {status} {body}')
        self._stream = connection
        return client_id

    def subscribe_other(self, client_id, topic):
        """Try another subscription on the same stream (isolation checks)."""
        return self.request('POST', '/api/realtime', {'clientId': client_id, 'subscriptions': [topic]})

    def next_seq(self, above, timeout=15):
        """Wait for a realtime event on nodes/<self> whose desired_seq is above
        `above` and return it."""
        while True:
            topic, message = self.events.get(timeout=timeout)
            need(topic == f'nodes/{self.node_id}', f'only nodes/<self> events arrive, got {topic}')
            seq = message.get('record', {}).get('desired_seq', 0)
            if seq > above:
                return seq

    def close(self):
        if self._stream is not None:
            # Shut the socket down first: closing the buffered reader while the
            # reader thread blocks in it would wait for that read forever.
            try:
                self._sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            self._stream = None

    # Desired ----------------------------------------------------------------

    def pull(self):
        status, desired = self.request('GET', '/api/drop/agent/desired')
        need(status == 200, f'desired: {status} {desired}')
        self.check_desired(desired)
        return desired

    def check_desired(self, d):
        need(set(d) == DESIRED_KEYS, f'desired keys: {sorted(d)}')
        need(d['schema'] == DESIRED_SCHEMA, 'schema')
        need(d['hostId'] == self.host_id and d['node'] == self.node and d['nodeId'] == self.node_id, 'node and host')
        need(d['state'] in ('active', 'draining'), 'state')
        need(isinstance(d['desiredSeq'], int) and d['desiredSeq'] >= 1, 'desiredSeq')
        need(SHA256.match(d['contentHash']), 'contentHash')
        ids = [a['appId'] for a in d['apps']]
        need(ids == sorted(ids) and len(set(ids)) == len(ids), 'apps sorted by appId, once each')
        for r in d['rejected']:
            need(APP_ID.match(r['appId']) and r['code'] and r['message'] and r['appId'] not in ids, f'rejected entry {r}')
        for a in d['apps']:
            extra = set(a) - APP_KEYS
            need(extra <= {'crewOwnerEmail', 'signInEmailEnabled', 'managedCrew', 'crewFreshEnrollment', 'crewReplicaBucket', 'awsRegion', 'hookPort', 'replicaSyncSeconds'} and set(a) >= APP_KEYS - {'idleTimeoutMs'}, f'app keys {sorted(a)}')
            if 'hookPort' in a:
                need(type(a['hookPort']) is int and 1 <= a['hookPort'] <= 65535 and a['hookPort'] != a['httpPort'], 'private hooks port')
            if 'replicaSyncSeconds' in a:
                need(type(a['replicaSyncSeconds']) is int and 1 <= a['replicaSyncSeconds'] <= 300 and a.get('managedCrew') is True, 'Crew replica sync interval')
            if extra - {'hookPort', 'replicaSyncSeconds'}:
                need(isinstance(a.get('crewOwnerEmail'), str) and '@' in a['crewOwnerEmail'], 'Crew owner email')
                need('signInEmailEnabled' not in a or a['signInEmailEnabled'] is True, 'enabled Crew binding')
            if a.get('managedCrew'):
                need(a.get('crewReplicaBucket') and a.get('awsRegion'), 'managed Crew storage')
            app_id = a['appId']
            need(APP_ID.match(app_id), 'appId')
            need(re.match('^' + re.escape(app_id) + r'-dep-[0-9]{1,9}$', a['deploymentId']), 'deploymentId')
            need(a['desiredState'] in ('running', 'on-request', 'stopped'), 'desiredState')
            need(a['volume'] == {'volumeId': app_id + '-vol', 'sizeBytes': a['volume']['sizeBytes']}
                 and a['volume']['sizeBytes'] % (1 << 20) == 0, 'volume')
            rel = a['release']
            need(SHA256.match(rel['sha256']) and rel['s3Key'] == 'releases/' + rel['sha256'] and rel['sizeBytes'] > 0, 'release')
            need(re.match(r'^[a-z][a-z0-9-]{0,31}$', a['executableName']), 'executableName')
            need(type(a['httpPort']) is int and 1 <= a['httpPort'] <= 65535, 'httpPort')
            need(isinstance(a['args'], list) and isinstance(a['env'], dict), 'args and env')
            for s in a['secrets']:
                need(set(s) == {'name', 'parameter', 'version'}, f'a secret has no value field: {sorted(s)}')
                need(s['parameter'] == f"{self.ssm_prefix}/apps/{app_id[2:]}/{s['name']}" and s['version'] >= 1, 'secret parameter')
            for h in a['hostnames']:
                need(h['kind'] in ('platform', 'custom'), 'hostname kind')

    def resolve(self, app):
        """Secret values and the release bytes of an app, from the fake AWS
        directory (as the real agent reads SSM name:version and S3)."""
        need(self.fake_aws is not None, 'resolve needs --fake-aws')
        values = {}
        for s in app['secrets']:
            directory = self.fake_aws / 'secrets' / hashlib.sha256(s['parameter'].encode()).hexdigest()
            need((directory / 'name').read_text(encoding='utf-8').strip() == s['parameter'], 'secret parameter in the store')
            values[s['name']] = (directory / str(s['version'])).read_text(encoding='utf-8')
        blob = (self.fake_aws / 'blobs' / app['release']['s3Key']).read_bytes()
        need(len(blob) == app['release']['sizeBytes'] and hashlib.sha256(blob).hexdigest() == app['release']['sha256'],
             'the release in the store matches size and sha256')
        return values

    # Report -----------------------------------------------------------------

    def report_body(self, desired, phase='running', extra_apps=()):
        apps = [{'appId': a['appId'], 'deploymentId': a['deploymentId'], 'phase': phase, 'message': '',
                 'restartCount': 0, 'lastExitCode': None, 'convergedAt': ''} for a in desired['apps']]
        apps += list(extra_apps)
        return {
            'schema': REPORT_SCHEMA, 'hostId': self.host_id, 'agentVersion': self.version,
            'desiredSeq': desired['desiredSeq'], 'acceptedRevision': f"drop-{desired['desiredSeq']}-{desired['contentHash'][:12]}",
            'nibrunner': {
                'state': 'ready', 'message': '', 'reportedAt': '2026-11-20T10:00:00Z',
                'capacity': {'vcpuCount': 4, 'memoryMib': 8192, 'cacheBytes': 1 << 30},
                'allocatable': {'vcpuCount': 4, 'memoryMib': 8192, 'cacheBytes': 1 << 30},
                'versions': {'agent': 'fake', 'firecracker': 'fake', 'guestImage': 'fake', 'zerofs': 'fake'},
            },
            'apps': apps,
        }

    def report(self, body):
        status, result = self.request('POST', '/api/drop/agent/report', body)
        if status == 200:
            need(set(result) == {'desiredSeq', 'state', 'accepted', 'ignored'}, f'report answer {result}')
        return status, result


def contract_check(agent):
    """One pass of the contract; returns a summary."""
    need(agent.login() == 200, 'node sign-in')
    need(agent.refresh() == 200, 'token refresh')
    client_id = agent.subscribe()
    desired = agent.pull()
    resolved = {a['appId']: sorted(agent.resolve(a)) for a in desired['apps']} if agent.fake_aws else {}
    status, result = agent.report(agent.report_body(desired))
    need(status == 200 and result['accepted'] == len(desired['apps']) and result['desiredSeq'] >= desired['desiredSeq'], f'report: {result}')
    return {'node': agent.node, 'clientId': bool(client_id), 'desiredSeq': desired['desiredSeq'],
            'apps': [a['appId'] for a in desired['apps']], 'secrets': resolved, 'report': result}


def main():
    parser = argparse.ArgumentParser(description=__doc__.split('\n')[0])
    parser.add_argument('--port', type=int, required=True)
    parser.add_argument('--domain', required=True)
    parser.add_argument('--node', required=True)
    parser.add_argument('--host-id', required=True)
    parser.add_argument('--fake-aws')
    parser.add_argument('--ssm-prefix', default='/drop')
    args = parser.parse_args()
    password = sys.stdin.readline().strip()
    agent = FakeAgent(args.port, args.domain, args.node, password, args.host_id, args.fake_aws, args.ssm_prefix)
    try:
        print(json.dumps(contract_check(agent), indent=2))
    finally:
        agent.close()


if __name__ == '__main__':
    try:
        main()
    except ContractError as error:
        print('FAIL -', error)
        sys.exit(1)
