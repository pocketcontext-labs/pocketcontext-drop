#!/usr/bin/env python3
"""End-to-end test of the drop binary with synthetic apps.

Runs on Linux with the standard library only. Every run uses
a temporary project folder, home and cache, and a free loopback port.
"""
import argparse
from contextlib import closing
import hashlib
import json
import os
from pathlib import Path
import socket
import shutil
import signal
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request

import fake_agent
import leak_scan

PASSWORD = 'synthetic test password'
# The sign-in identity of the project's one account (bundle.AccountEmail).
ACCOUNT_EMAIL = 'account@drop.invalid'
# The retired product name, assembled from parts so this file never contains it.
RETIRED = 'wi' + 'ki'
# Inherited variables of this program, of the application it was derived from and
# of that one's predecessor must not leak into the test.
SCRUBBED_PREFIXES = ('DROP_', 'TEMPLATE' + 'CONTEXT_', 'POCKET' + 'CONTEXT_', RETIRED.upper() + 'CONTEXT_')
# The per-machine cache folder (Identity.CacheRoot).
CACHE_ROOT = 'pocketcontext-drop'


def isolated_env(home):
    """The inherited environment with a private home and cache and no product variables."""
    env = {key: value for key, value in os.environ.items() if not key.upper().startswith(SCRUBBED_PREFIXES)}
    env.update(HOME=str(home), USERPROFILE=str(home), XDG_CACHE_HOME=str(home / 'cache'),
               LOCALAPPDATA=str(home / 'AppData' / 'Local'))
    return env


def user_cache(home):
    """os.UserCacheDir() for the isolated environment."""
    return home / 'cache'


def retired_names(root):
    """Every file or directory below root (project, homes, caches, skills) named after the retired product."""
    return [str(path) for path in root.rglob('*') if RETIRED in path.name.lower()]


def free_port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


class Project:
    def __init__(self, binary, root):
        self.binary, self.root = binary, root
        self.project = root / 'Cloud Drive' / 'Drop'
        self.project.mkdir(parents=True)
        self.db = self.project / 'drop.db'
        self.home = root / 'home'
        self.home.mkdir()
        self.env = isolated_env(self.home)
        self.port = free_port()
        self.url = f'http://127.0.0.1:{self.port}'

    def run(self, *args, stdin=None, expect=0):
        result = subprocess.run([str(self.binary), '--db', str(self.db), *args], input=stdin, capture_output=True,
                                text=True, encoding='utf-8', env=self.env, timeout=120)
        if result.returncode != expect:
            raise AssertionError(f'{args}: exit {result.returncode}, expected {expect}\n{result.stdout}\n{result.stderr}')
        return result

    def json(self, *args, **kwargs):
        return json.loads(self.run(*args, **kwargs).stdout)

    def http(self, method, path, body=None, headers=None):
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(self.url + path, data=data, method=method,
                                         headers={'Content-Type': 'application/json', **(headers or {})})
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as error:
            return error.code, error.read()


class Remote:
    """Another computer: its own home and cache, an empty working folder and no project."""

    def __init__(self, binary, root, url):
        self.binary, self.root, self.url = binary, root, url
        self.home = root / 'home'
        self.work = root / 'work'
        self.home.mkdir(parents=True)
        self.work.mkdir()
        self.env = isolated_env(self.home)

    def run(self, *args, stdin=None, expect=0, url=None, env=None):
        flags = [] if url is False else ['--url', url or self.url]
        result = subprocess.run([str(self.binary), *args, *flags], input='' if stdin is None else stdin,
                                capture_output=True, text=True, encoding='utf-8', env={**self.env, **(env or {})},
                                cwd=self.work, timeout=120)
        if expect is not None and result.returncode != expect:
            raise AssertionError(f'remote {args}: exit {result.returncode}, expected {expect}\n{result.stdout}\n{result.stderr}')
        return result

    def json(self, *args, **kwargs):
        return json.loads(self.run(*args, **kwargs).stdout)

    def session(self):
        sessions = [p for p in self.root.rglob('session.json') if p.parent.name.startswith('remote-')
                    and p.parent.parent == user_cache(self.home) / CACHE_ROOT]
        if len(sessions) != 1:
            raise AssertionError(f'expected one remote session, found {sessions}')
        return sessions[0]


class Forward:
    """A loopback TCP forward from a different local port, like a tunnel on the wrong port."""

    def __init__(self, target):
        self.target = target
        self.listener = socket.socket()
        self.listener.bind(('127.0.0.1', 0))
        self.listener.listen(16)
        self.listener.settimeout(0.2)
        self.port = self.listener.getsockname()[1]
        self.stopped = threading.Event()
        self.thread = threading.Thread(target=self.serve, daemon=True)
        self.thread.start()

    def serve(self):
        with self.listener:
            while not self.stopped.is_set():
                try:
                    client, _ = self.listener.accept()
                except socket.timeout:
                    continue
                except OSError:
                    return
                self.connect(client)

    def connect(self, client):
        client.settimeout(None)
        try:
            upstream = socket.create_connection(('127.0.0.1', self.target), timeout=10)
            upstream.settimeout(None)
        except OSError:
            client.close()
            return
        for src, dst in ((client, upstream), (upstream, client)):
            threading.Thread(target=self.pipe, args=(src, dst), daemon=True).start()

    @staticmethod
    def pipe(src, dst):
        try:
            while True:
                data = src.recv(65536)
                if not data:
                    break
                dst.sendall(data)
        except OSError:
            pass
        finally:
            for sock in (src, dst):
                try:
                    sock.close()
                except OSError:
                    pass

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        self.stopped.set()
        self.thread.join(timeout=5)


def http_status(url):
    try:
        with urllib.request.urlopen(url, timeout=10) as response:
            return response.status
    except urllib.error.HTTPError as error:
        return error.code


def listening(port):
    try:
        with socket.create_connection(('127.0.0.1', port), timeout=2):
            return True
    except OSError:
        return False


def check(condition, message):
    if not condition:
        raise AssertionError(message)
    print('ok -', message)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True, type=Path)
    binary = parser.parse_args().binary.resolve()
    with tempfile.TemporaryDirectory(prefix='drop-e2e-') as temp:
        pc = Project(binary, Path(temp))
        try:
            scenario(pc)
            single_user_scenario(pc)
            sync_scenario(pc)
            earlier_release_scenario(pc)
            remote = Remote(binary, Path(temp) / 'other-computer', pc.url)
            remote_scenario(pc, remote)
            hosted_scenario(pc, remote)
            codes_scenario(pc, remote)
            pc.json('stop')
            leftovers = retired_names(pc.root)
            check(not leftovers, f'no file or directory name contains the retired product name: {leftovers}')
        finally:
            subprocess.run([str(binary), '--db', str(pc.db), 'stop'], env=pc.env, capture_output=True, timeout=60)
    print('all end-to-end checks passed')


def app_names(client):
    """The names of the client's apps, newest first."""
    return [row['name'] for row in client.json('app', 'list')['apps']]


def slug(name):
    """A valid, unique slug for a synthetic app name."""
    return 'app-' + hashlib.sha256(name.encode()).hexdigest()[:12]


def add(client, name, **kwargs):
    """Create an app named name with its synthetic slug."""
    return client.json('app', 'create', name, '--slug', slug(name), **kwargs)['app']


def token(pc, password):
    status, body = pc.http('POST', '/api/collections/users/auth-with-password', {'identity': ACCOUNT_EMAIL, 'password': password})
    if status != 200:
        raise AssertionError(f'password sign-in: HTTP {status} {body!r}')
    return json.loads(body)['token']


def local_key(pc):
    """The private local key the server wrote to this computer's cache."""
    keys = list((user_cache(pc.home) / CACHE_ROOT).glob('*/local.key'))
    if len(keys) != 1:
        raise AssertionError(f'expected one local key, found {keys}')
    return keys[0].read_text(encoding='utf-8').strip()


def scenario(pc):
    usage = pc.run('help')
    text = usage.stdout + usage.stderr
    check('drop' in text and RETIRED not in text.lower(), 'help names drop and not the retired product')
    app_help = pc.run('app', '--help').stdout
    check(app_help.startswith('usage: drop app') and '"apps"' in app_help and '"quota"' in app_help, 'app --help documents the output shape')
    check('unknown command' in pc.run('add', 'x', '--slug', 'xyz', expect=2).stderr, 'apps are created with app create, not add')
    check(pc.run('help', 'search').stdout == pc.run('search', '-h').stdout and 'generation' in pc.run('help', 'search').stdout,
          'help COMMAND and COMMAND -h print the same help')
    check('unknown command' in pc.run('help', 'no-such-command', expect=2).stderr, 'help refuses an unknown command')
    check('unknown command' in pc.run('ingest', 'x', expect=2).stderr, 'knowledge-base commands are not part of Drop')

    pc.run('init', '--email', 'tester@example.localhost', '--port', str(pc.port), '--password-stdin', stdin=PASSWORD + '\n', expect=2)
    check(not pc.db.exists(), 'init has no --email: the project has one account without a chosen identity')
    init = pc.json('init', '--port', str(pc.port), '--password-stdin', stdin=PASSWORD + '\n')
    check(init['accountCreated'] and init['url'] == pc.url and init['newProject'] and 'email' not in init,
          'init creates the account and starts the server')
    check(pc.db.exists(), 'init writes the database to the project folder')
    config = json.loads((pc.project / 'drop.json').read_text(encoding='utf-8'))
    check(sorted(config) == ['instance', 'port'], f'drop.json holds only the port and project ID: {config}')
    me = pc.json('whoami')
    check(sorted(me) == ['db', 'id', 'url'] and me['db'] == str(pc.db), 'whoami reports the account ID, URL and database')
    again = pc.json('init', '--port', str(pc.port))
    check(not again['accountCreated'] and not again['newProject'] and pc.json('whoami')['id'] == me['id'],
          'init on an existing project keeps its account and password')
    listed = pc.json('app', 'list')
    check(listed['apps'] == [] and listed['quota']['unlimited'], 'a new project has no apps, and its admin account no quota')
    schema = pc.json('schema')
    tables = {t['name']: sorted(c['name'] for c in t['columns']) for t in schema['tables']}
    check(tables == {'apps': sorted(['id', 'owner', 'name', 'slug', 'nib_app_id', 'hostname', 'desired_state', 'idle_timeout_ms', 'vcpu',
                                     'memory_mib', 'disk_mib', 'http_port', 'executable_name', 'health_kind', 'health_path',
                                     'deployment_seq', 'current_release', 'archived', 'archived_at', 'created', 'updated']),
                     'releases': sorted(['id', 'app', 'sha256', 'size_bytes', 's3_key', 'note', 'created_by', 'created']),
                     'app_secret_names': sorted(['id', 'app', 'name', 'ssm_version', 'length', 'set_via', 'updated_by', 'created', 'updated']),
                     'events': ['action', 'actor', 'actor_node', 'app', 'created', 'detail', 'id', 'subject_user'],
                     'placements': sorted(['id', 'app', 'node', 'generation', 'created_by', 'created', 'updated']),
                     'node_reports': sorted(['id', 'node', 'app', 'nib_app_id', 'deployment_id', 'phase', 'message', 'restart_count',
                                             'last_exit_code', 'has_exit_code', 'converged_at', 'observed_at'])},
          f'the SQL schema exposes apps (no env or args), secret names, releases, events, placements and node reports (never nodes): {tables}')
    check(schema['permissionModel'] == 'shared' and schema['search']['indexes'] == ['apps'] and 'isolation' not in schema['search']
          and 'snapshotLimits' not in schema, 'reads use the live database and a shared apps index')

    accounts_scenario(pc, me['id'])

    first = add(pc, 'Hello from the espresso machine ✓')
    second = pc.json('app', 'create', '-', '--slug', 'cafe-app', '--port', '9000', '--mode', 'running', stdin='Café app for everyone 😀\n')['app']
    check(first['name'] == 'Hello from the espresso machine ✓' and first['slug'] == slug(first['name'])
          and first['nib_app_id'] == 'd-' + first['id'] and first['deployment_id'] == first['nib_app_id'] + '-dep-1'
          and first['desired_state'] == 'on-request' and first['http_port'] == 8080 and first['hostname'] == '', 'app create creates an app with defaults')
    check(second['name'] == 'Café app for everyone 😀' and second['slug'] == 'cafe-app' and second['http_port'] == 9000
          and second['desired_state'] == 'running', 'app create - reads the name from standard input and takes settings')
    check(app_names(pc) == [second['name'], first['name']], 'app list shows the apps newest first')
    check([a['name'] for a in pc.json('app', 'list', '--limit', '1')['apps']] == [second['name']], 'app list --limit bounds the result')
    pc.run('app', 'list', '--limit', '501', expect=1)
    pc.run('app', 'create', '   ', '--slug', 'blank-name', expect=1)
    check('1-80' in pc.run('app', 'create', 'x' * 81, '--slug', 'long-name', expect=1).stderr, 'app create refuses empty and over-long names')
    pc.run('app', 'create', 'No slug', expect=2)
    for bad in ('ab', '1abc', 'abc-', 'Abc', 'a_bc', 'a' * 33, 'www'):
        pc.run('app', 'create', 'Bad slug', '--slug', bad, expect=1)
    check(True, 'app create refuses a missing, invalid or reserved slug')
    taken = pc.run('app', 'create', 'Another café', '--slug', 'cafe-app', expect=4)
    check('slug_taken' in taken.stderr and len(app_names(pc)) == 2, 'app create refuses a slug already in use (exit 4)')
    status, _ = pc.http('POST', '/api/collections/apps/records', {'name': 'Through the records API', 'slug': 'records-api'},
                        headers={'Authorization': token(pc, PASSWORD)})
    check(status == 403 and len(app_names(pc)) == 2, 'the records API writes no app: apps change only through operations')
    changed = pc.json('app', 'set', 'cafe-app', '--memory', '1024', '--name', 'Café app')
    check(changed['deployment_bumped'] and changed['app']['deployment_id'].endswith('-dep-2') and changed['app']['name'] == 'Café app',
          'app set changes settings and bumps the deployment')
    renamed = pc.json('app', 'set', 'cafe-app', '--name', 'Café app for everyone 😀')
    check(not renamed['deployment_bumped'], 'a new name keeps the deployment')
    env = pc.json('app', 'env', 'cafe-app', 'WELCOME_TEXT=hello', 'URL=https://${NIBRUN_HOSTNAME}/')['app']
    check(env['env'] == {'WELCOME_TEXT': 'hello', 'URL': 'https://${NIBRUN_HOSTNAME}/'} and env['deployment_id'].endswith('-dep-3'), 'app env sets variables')
    check(pc.json('app', 'env', 'cafe-app', '-WELCOME_TEXT')['app']['env'] == {'URL': 'https://${NIBRUN_HOSTNAME}/'}, 'app env -KEY removes one')
    pc.run('app', 'env', 'cafe-app', 'HOME=/root', expect=1)
    check(pc.json('app', 'stop', 'cafe-app')['app']['desired_state'] == 'stopped'
          and pc.json('app', 'start', 'cafe-app')['app']['desired_state'] == 'running', 'app stop and start keep the mode')
    shown = pc.json('app', 'show', 'cafe-app')
    check(shown['app']['id'] == second['id'] and shown['secrets'] == [], 'app show reads one app by slug')
    unavailable = pc.run('secret', 'set', 'cafe-app', 'API_KEY', stdin='synthetic\n', expect=1)
    check('secrets_unavailable' in unavailable.stderr, 'the local server has no secret store')
    pc.run('secret', 'set', 'cafe-app', 'API_KEY', 'value-as-argument', expect=2)
    check(True, 'secret set never takes the value as an argument')

    found = pc.json('search', 'espresso')
    check([g['id'] for g in found['apps']] == [first['id']] and found['generation'] and not found['hasMore']
          and 'espresso' in found['apps'][0]['excerpt'], 'full-text search finds the app')
    check(pc.json('search', 'café')['apps'][0]['id'] == second['id'], 'search matches Unicode words')
    check(pc.json('search', 'cafe-app')['apps'][0]['id'] == second['id'], 'search matches the slug')
    check(pc.json('search', 'nonexistentword')['apps'] == [], 'search without matches is empty')
    for i in range(3):
        add(pc, f'Paged hello number {i}')
    page = pc.json('search', 'paged', '--limit', '2')
    check(len(page['apps']) == 2 and page['hasMore'] and page['nextOffset'] == 2, 'search pages its results')
    rest = pc.json('search', 'paged', '--limit', '2', '--offset', '2', '--generation', page['generation'])
    check(len(rest['apps']) == 1 and not rest['hasMore'], 'a continuation with the generation returns the rest')
    pc.run('search', 'paged', '--offset', '2', expect=2)
    add(pc, 'Paged hello number 3')
    stale = pc.run('search', 'paged', '--limit', '2', '--offset', '2', '--generation', page['generation'], expect=4)
    check('restart at offset 0' in stale.stderr, 'a changed search generation fails with exit 4')

    rows = pc.json('sql', 'SELECT count(*) FROM apps')['rows']
    check(rows == [[6]], 'sql counts the apps')
    check(pc.json('sql', "SELECT deployment_seq FROM apps WHERE slug = 'cafe-app'")['rows'] == [[4]], 'sql reads the deployment number')
    pc.run('sql', 'SELECT env FROM apps', expect=1)
    check(True, 'sql never reads the app environment')
    newest = pc.json('query', 'SELECT name FROM apps ORDER BY created DESC, id DESC LIMIT 1')['rows']
    check(newest == [['Paged hello number 3']], 'query is an alias of sql')
    for denied in ('SELECT * FROM users', 'SELECT email FROM users', 'SELECT * FROM user_directory', 'SELECT * FROM sqlite_schema', 'SELECT * FROM dbstat', 'SELECT * FROM pragma_table_info(\'apps\')',
                   "DELETE FROM apps", "INSERT INTO apps(name, slug) VALUES ('x', 'xyz')"):
        pc.run('sql', denied, expect=1)
    check(pc.json('sql', 'SELECT count(*) FROM apps')['rows'] == [[6]], 'the SQL reader refuses credentials, metadata and writes')

    status, _ = pc.http('GET', '/')
    check(status == 200, 'the reader is served')
    status, body = pc.http('GET', '/api/bundle/instance')
    check(status == 200 and json.loads(body).get('app') == 'drop', 'the instance endpoint reports app drop')
    cache = user_cache(pc.home) / CACHE_ROOT
    working = [p for p in cache.iterdir() if p.is_dir() and p.name != 'app' and not p.name.startswith('remote-')] if cache.is_dir() else []
    check(len(working) == 1 and any(working[0].iterdir()), f'the working copy lives in the per-machine drop cache: {working}')
    status, _ = pc.http('GET', '/api/bundle/instance', headers={'Host': f'attacker.example:{pc.port}'})
    check(status == 403, 'foreign Host headers are refused')
    status, _ = pc.http('POST', '/api/context/query', {'sql': 'SELECT 1'})
    check(status == 401, 'SQL requires authentication')
    status, _ = pc.http('POST', '/api/context/search', {'index': 'apps', 'query': 'hello'})
    check(status == 401, 'search requires authentication')
    status, _ = pc.http('GET', '/api/collections/apps/records')
    check(status in (200, 401, 403) and b'espresso' not in pc.http('GET', '/api/collections/apps/records')[1],
          'anonymous record listing returns no apps')
    status, _ = pc.http('POST', '/api/bundle/token')
    check(status == 403, 'local tokens require the private key')
    status, _ = pc.http('POST', '/api/bundle/login-code')
    check(status == 403, 'sign-in codes require the private key')
    link = pc.json('open', '--no-browser')['signInLink']
    check(link.startswith(pc.url + '/bundle/login#'), 'open prints a single-use sign-in link')
    status, page_html = pc.http('GET', '/bundle/login')
    check(status == 200 and b'Drop sign-in' in page_html, 'the sign-in page is served')
    code = link.split('#', 1)[1]
    status, body = pc.http('POST', '/api/bundle/redeem', {'code': code})
    check(status == 200 and json.loads(body)['record']['id'] == me['id'], 'the sign-in link signs in the account')
    status, _ = pc.http('POST', '/api/bundle/redeem', {'code': code})
    check(status == 401, 'the sign-in link is single-use')
    token(pc, PASSWORD)
    check(True, 'the reader password works')

    pc.json('stop')
    names = sorted(p.name for p in pc.project.iterdir())
    check(names in (['drop.db', 'drop.json'], ['storage', 'drop.db', 'drop.json']),
          f'only the project files remain after stop: {names}')
    with closing(sqlite3.connect(f'file:{pc.db.as_posix()}?mode=ro', uri=True)) as db:
        stored = db.execute('SELECT count(*) FROM apps').fetchone()[0]
    check(stored == 6, 'stop writes the apps back to the synced database')
    check(len(app_names(pc)) == 6, 'apps survive a restart')

    # Another computer is using the project.
    pc.json('stop')
    pc.db.with_name('drop.db.lease').write_text(json.dumps({'host': 'other-laptop', 'pid': 1, 'heartbeat': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}))
    blocked = pc.run('ensure-running', expect=1)
    check('other-laptop' in blocked.stderr, 'a fresh lease from another computer blocks start')
    pc.json('ensure-running', '--take-over')
    check(pc.json('status')['running'], '--take-over starts the server')

    skills = pc.root / 'skill'
    installed = pc.json('skill', '--dir', str(skills))
    text = (skills / 'SKILL.md').read_text(encoding='utf-8')
    check(str(pc.binary) in text and str(pc.db) in text and '{{' not in text, 'the skill names the absolute binary and database')
    leftovers = [str(p) for p in skills.rglob('*.md') if '{{' in p.read_text(encoding='utf-8')]
    check(not leftovers and (skills / 'references' / 'commands.md').is_file() and not (skills / 'SKILL.remote.md').exists(),
          f'the installed skill has its references and no placeholders or remote template: {leftovers}')
    check(installed['binary'] == str(pc.binary), 'skill install reports the binary')
    check((skills / '.drop').is_file(), 'the skill directory carries the .drop marker')
    check('name: drop' in text, 'the skill is named drop')
    mentions = [str(p) for p in skills.rglob('*') if p.is_file() and RETIRED in p.read_bytes().decode('utf-8', 'replace').lower()]
    check(not mentions, f'no installed skill file mentions the retired product: {mentions}')
    (pc.home / '.claude').mkdir()
    agent = pc.json('skill', '--agent', 'claude-code')
    target = pc.home / '.claude' / 'skills' / 'drop'
    check(agent['installed'].get('claude-code') == str(target) and (target / 'SKILL.md').is_file()
          and (target / '.drop').is_file(), 'the Claude Code skill is installed as drop')
    every = pc.json('skill')
    check(every['installed']['claude-code'] == str(target) and every['installed']['codex'].startswith('skipped')
          and every['installed']['antigravity'].startswith('skipped'), 'skill skips agents whose configuration folder is missing')


def accounts_scenario(pc, account_id):
    """Operations as the local account, which is an admin: invites, accounts, op and idempotency keys."""
    me = pc.json('op', 'me')
    check(me['id'] == account_id and me['role'] == 'admin' and me['admin'] and me['owner'], f'the local account is the admin owner: {me}')
    created = pc.json('invite', 'create', 'Friend@Example.test', '--quota', '2')
    invite = created['invite']
    check(invite['email'] == 'friend@example.test' and invite['role'] == 'user' and invite['app_quota'] == 2 and invite['state'] == 'open',
          f'invite create invites a lower-cased address: {invite}')
    check(created['sign_in_url'] == pc.url + '/#/invite?email=friend%40example.test', f"the sign-in URL opens the reader: {created['sign_in_url']}")
    again = pc.run('invite', 'create', 'friend@example.test', expect=4)
    check('invite_exists' in again.stderr, 'a second open invite for the address is refused with exit 4')
    pc.run('invite', 'create', 'not-an-address', expect=1)
    listed = pc.json('invite', 'list', '--state', 'open')
    check([i['id'] for i in listed['invites']] == [invite['id']] and not listed['has_more'], 'invite list shows the open invite')
    check(pc.json('invite', 'revoke', invite['id'])['invite']['state'] == 'revoked', 'invite revoke closes it')
    users = pc.json('user', 'list')['users']
    check(len(users) == 1 and users[0]['id'] == account_id and users[0]['owner'], 'user list shows the one local account')
    pc.run('user', 'set', account_id, '--role', 'user', expect=1)
    pc.run('user', 'set', account_id, expect=2)
    key_input = '{"email":"retry@example.test"}'
    first = pc.json('op', 'invite.create', key_input, '--idempotency-key', 'e2e-1')
    second = pc.json('op', 'invite.create', '-', '--idempotency-key', 'e2e-1', stdin=key_input)
    check(first['invite']['id'] == second['invite']['id'], 'a retried operation with its idempotency key replays the first result')
    pc.run('op', 'invite.create', '{"email":"other@example.test"}', '--idempotency-key', 'e2e-1', expect=4)
    pc.run('op', 'no.such', expect=2)
    rows = pc.json('sql', "SELECT action, actor FROM events ORDER BY created")['rows']
    check([r[0] for r in rows] == ['invite.create', 'invite.revoke', 'invite.create'] and all(r[1] == account_id for r in rows),
          f'each change wrote one event: {rows}')


def single_user_scenario(pc):
    """One hidden account reads and writes every app; nobody else gets in, and no second account exists."""
    mine = token(pc, PASSWORD)
    me = pc.json('whoami')['id']
    key = local_key(pc)

    # The account is the only one: with the local key, the account route only changes its password
    # (pocketcontext-core; TestRealHooksRefuseSecondAccount proves the hook refuses a second record).
    status, body = pc.http('POST', '/api/bundle/account', {'password': 'synthetic second password'},
                           headers={'X-Drop-Local-Key': key})
    check(status == 200 and json.loads(body).get('created') is False and json.loads(body).get('id') == me,
          f'the account route keeps the one account: {status} {body!r}')
    status, body = pc.http('POST', '/api/bundle/account', {'password': PASSWORD}, headers={'X-Drop-Local-Key': key})
    check(status == 200 and json.loads(body).get('created') is False, 'the password is restored')
    # A password change revokes earlier tokens, including mine.
    mine = token(pc, PASSWORD)
    check(mine and pc.json('whoami')['id'] == me, 'there is still exactly one account')
    for headers in ({}, {'Authorization': mine}):
        status, _ = pc.http('POST', '/api/collections/users/records', {'email': 'second@example.localhost', 'password': 'synthetic second password',
                                                                      'passwordConfirm': 'synthetic second password'}, headers=headers)
        check(status in (400, 403), f'the records API creates no account ({"signed in" if headers else "anonymous"})')
    status, body = pc.http('GET', '/api/collections/users/records', headers={'Authorization': mine})
    check(status == 403 or (status == 200 and me not in body.decode()), 'accounts cannot be listed')
    status, _ = pc.http('DELETE', f'/api/collections/users/records/{me}', headers={'Authorization': mine})
    check(status in (403, 404), 'the account cannot be deleted')
    status, _ = pc.http('POST', '/api/bundle/account', {'password': 'synthetic second password'})
    check(status == 403, 'creating the account requires the local key')
    check(pc.http('POST', '/api/bundle/password', {'password': 'synthetic new password'})[0] == 404,
          'there is no separate password route; /api/bundle/account covers it and requires the local key')
    pc.json('flush')
    with closing(sqlite3.connect(f'file:{pc.db.as_posix()}?mode=ro', uri=True)) as db:
        accounts = db.execute('SELECT id, email FROM users').fetchall()
    check(accounts == [(me, ACCOUNT_EMAIL)], f'the database holds exactly one account: {accounts}')

    # Apps change only through operations; the records API reads the account's apps and writes none.
    status, _ = pc.http('POST', '/api/collections/apps/records', {'name': 'Through the records API', 'slug': 'records-api', 'owner': 'aaaaaaaaaaaaaaa'}, headers={'Authorization': mine})
    check(status == 403, 'the records API creates no app')
    status, _ = pc.http('POST', '/api/drop/op', {'op': 'app.create', 'input': {'name': 'Crew', 'slug': 'local-crew', 'kind': 'crew'}}, headers={'Authorization': mine})
    check(status == 403, 'a local password account cannot provision managed email')
    created = pc.json('app', 'create', 'Records reader app', '--slug', 'records-app')['app']
    check(created['owner'] == me, 'app create makes an app owned by the caller')
    status, body = pc.http('GET', '/api/collections/apps/records?perPage=100', headers={'Authorization': mine})
    listed = {item['id'] for item in json.loads(body)['items']}
    check(status == 200 and created['id'] in listed and len(listed) == pc.json('sql', 'SELECT count(*) FROM apps')['rows'][0][0],
          'the account lists every app through the records API')
    check(created['id'] in json.dumps(pc.json('app', 'list')) and [g['id'] for g in pc.json('search', 'records')['apps']] == [created['id']]
          and pc.json('sql', f"SELECT name FROM apps WHERE id = '{created['id']}'")['rows'] == [['Records reader app']],
          'app list, search and SQL read the app')
    for method, body in (('PATCH', {'name': 'Edited app'}), ('DELETE', None)):
        status, _ = pc.http(method, f"/api/collections/apps/records/{created['id']}", body, headers={'Authorization': mine})
        check(status == 403, f'the records API refuses {method} on an app')
    check(pc.json('app', 'set', created['id'], '--name', 'Edited app')['app']['name'] == 'Edited app', 'the account edits an app with app set')

    # Authentication stays on for every read and write.
    for headers, who in (({}, 'anonymous'), ({'Authorization': 'not-a-valid-token'}, 'an invalid token')):
        status, _ = pc.http('POST', '/api/context/query', {'sql': 'SELECT count(*) FROM apps'}, headers=headers)
        check(status == 401, f'SQL refuses {who}')
        status, _ = pc.http('GET', '/api/context/schema', headers=headers)
        check(status == 401, f'the schema refuses {who}')
        status, _ = pc.http('POST', '/api/context/search', {'index': 'apps', 'query': 'edited'}, headers=headers)
        check(status == 401, f'search refuses {who}')
        status, body = pc.http('GET', '/api/collections/apps/records', headers=headers)
        check(status in (200, 401, 403) and b'Edited app' not in body, f'record listing shows {who} no apps')
        status, _ = pc.http('GET', f"/api/collections/apps/records/{created['id']}", headers=headers)
        check(status in (401, 403, 404), f'reading an app refuses {who}')
        status, _ = pc.http('POST', '/api/collections/apps/records', {'name': 'Intruder', 'slug': 'intruder'}, headers=headers)
        check(status in (400, 401, 403), f'adding an app refuses {who}')
        status, _ = pc.http('PATCH', f"/api/collections/apps/records/{created['id']}", {'name': 'Hijacked'}, headers=headers)
        check(status in (401, 403, 404), f'editing an app refuses {who}')
        status, _ = pc.http('DELETE', f"/api/collections/apps/records/{created['id']}", headers=headers)
        check(status in (401, 403, 404), f'deleting an app refuses {who}')
    check(pc.json('sql', f"SELECT name FROM apps WHERE id = '{created['id']}'")['rows'] == [['Edited app']],
          'refused requests change nothing')
    archived = pc.json('app', 'archive', created['id'])['app']
    check(archived['archived'] and created['id'] not in json.dumps(pc.json('app', 'list'))
          and created['id'] in json.dumps(pc.json('app', 'list', '--archived')), 'the account archives an app instead of deleting it')

    # password changes the reader password and, like any PocketBase password change, revokes earlier tokens.
    new_password = 'synthetic changed password'
    check(pc.json('password', '--password-stdin', stdin=new_password + '\n') == {'password': 'changed'}, 'password changes the reader password')
    status, _ = pc.http('POST', '/api/collections/users/auth-with-password', {'identity': ACCOUNT_EMAIL, 'password': PASSWORD})
    check(status == 400, 'the old password no longer signs in')
    status, _ = pc.http('POST', '/api/context/query', {'sql': 'SELECT 1'}, headers={'Authorization': mine})
    check(status == 401, 'a password change revokes earlier sessions')
    token(pc, new_password)
    pc.json('password', '--password-stdin', stdin=PASSWORD + '\n')
    token(pc, PASSWORD)
    check(True, 'the password can be changed back')


def earlier_release_scenario(pc):
    """A database with a schema migration this release does not ship (another release or product) is refused before any migration, unchanged."""
    pc.json('flush')
    folder = pc.root / 'Earlier'
    folder.mkdir()
    old = folder / 'drop.db'
    shutil.copyfile(pc.db, old)
    with closing(sqlite3.connect(old)) as db:
        db.execute("INSERT INTO _migrations(file, applied) VALUES ('1800000000_users.js', 1)")
        db.commit()
    port = free_port()
    (folder / 'drop.json').write_text(json.dumps({'port': port}), encoding='utf-8')
    before = hashlib.sha256(old.read_bytes()).hexdigest()
    result = subprocess.run([str(pc.binary), '--db', str(old), 'ensure-running'], capture_output=True, text=True, encoding='utf-8',
                            env=pc.env, timeout=120)
    check(result.returncode == 1 and 'different Drop release' in result.stderr and 'leaves the file unchanged' in result.stderr,
          f'a database of another release is refused with a clear message: {result.stderr.strip()[-300:]}')
    check(hashlib.sha256(old.read_bytes()).hexdigest() == before and not listening(port), 'the refused database is left unchanged and nothing serves it')
    check(sorted(p.name for p in folder.iterdir()) == ['drop.db', 'drop.json'], 'the refusal writes nothing beside the database')


def server_pid(pc):
    return json.loads(pc.db.with_name('drop.db.lease').read_text(encoding='utf-8'))['pid']


def crash(pc):
    """Kill the server without a write-back, as a power loss would."""
    pid = server_pid(pc)
    os.kill(pid, signal.SIGKILL if hasattr(signal, 'SIGKILL') else signal.SIGTERM)
    for _ in range(100):
        if not listening(pc.port):
            break
        time.sleep(0.1)
    time.sleep(0.5)  # let the OS release the server's lock


def replace_elsewhere(pc, name):
    """Another computer's sync client replaces the synced database with a version holding an app named name."""
    work = pc.root / 'elsewhere'
    work.mkdir(exist_ok=True)
    copy = work / 'copy.db'
    shutil.copyfile(pc.db, copy)
    with closing(sqlite3.connect(copy)) as db:
        record = hashlib.sha256(name.encode()).hexdigest()[:15]
        owner = db.execute('SELECT id FROM users').fetchone()[0]
        db.execute("INSERT INTO apps(id, owner, name, slug, nib_app_id, volume_id, deployment_seq, created, updated) VALUES (?, ?, ?, ?, ?, ?, 1, '2026-01-01 00:00:00.000Z', '2026-01-01 00:00:00.000Z')",
                   (record, owner, name, slug(name), 'd-' + record, 'd-' + record + '-vol'))
        db.commit()
        db.execute('PRAGMA journal_mode=DELETE')
    os.replace(copy, pc.db)


def stored_names(path):
    with closing(sqlite3.connect(f'file:{path.as_posix()}?mode=ro', uri=True)) as db:
        return {row[0] for row in db.execute('SELECT name FROM apps')}


def sync_scenario(pc):
    """Write-back after a crash, a synced file replaced elsewhere, and both recover choices."""
    pc.json('ensure-running')
    # A crash with unsaved changes: the next start writes them back.
    add(pc, 'Survives a crash')
    crash(pc)
    pc.json('ensure-running')
    check('Survives a crash' in app_names(pc), 'unsaved changes survive a crash')
    pc.json('flush')
    check('Survives a crash' in stored_names(pc.db), 'the recovered changes reach the synced database')

    # The synced file changes elsewhere while this computer has unsaved changes.
    add(pc, 'Local change while running')
    replace_elsewhere(pc, 'Remote change while running')
    conflicts = []
    for _ in range(60):
        conflicts = pc.json('status')['conflicts']
        if conflicts:
            break
        time.sleep(0.5)
    check(len(conflicts) == 1 and Path(conflicts[0]).name.startswith('drop.db.conflict-'), f'a write-back conflict keeps a copy: {conflicts}')
    check(stored_names(pc.db) >= {'Remote change while running'} and 'Local change while running' not in stored_names(pc.db),
          'the file written elsewhere is never overwritten')
    check('Local change while running' in stored_names(Path(conflicts[0])), "the conflict copy holds this computer's change")
    pc.json('stop')
    blocked = pc.run('ensure-running', expect=1)
    check('recover' in blocked.stderr, 'start after a conflict asks for recover')
    pc.run('recover', '--keep', 'both', expect=2)
    pc.json('recover', '--keep', 'synced')
    current = app_names(pc)
    check('Remote change while running' in current and 'Local change while running' not in current, 'recover --keep synced uses the synced file')
    check('Local change while running' in stored_names(Path(conflicts[0])), "this computer's work stays in the conflict copy")
    pc.run('recover', '--keep', 'local', expect=1)
    check(True, 'recover refuses while the server runs')

    # A crash with unsaved changes while the synced file also changed: keep local.
    add(pc, 'Local change before crash')
    crash(pc)
    replace_elsewhere(pc, 'Remote change after crash')
    blocked = pc.run('ensure-running', expect=1)
    check('recover' in blocked.stderr, 'an interrupted run over a changed synced file needs recover')
    pc.json('recover', '--keep', 'local')
    replaced = list(pc.project.glob('drop.db.replaced-*.db'))
    check(len(replaced) == 1 and 'Remote change after crash' in stored_names(replaced[0]), 'recover --keep local saves the other file')
    current = app_names(pc)
    check('Local change before crash' in current and 'Remote change after crash' not in current, "recover --keep local keeps this computer's work")
    check('Local change before crash' in stored_names(pc.db), 'the kept work is written back')
    pc.json('stop')
    check(not pc.run('recover', '--keep', 'local', expect=1).stdout, 'recover without unsaved changes has nothing to do')
    for path in [*replaced, *map(Path, conflicts)]:
        path.unlink()
    pc.json('ensure-running')


def remote_scenario(pc, r):
    instance = json.loads((pc.project / 'drop.json').read_text(encoding='utf-8'))['instance']
    me = pc.json('whoami')['id']

    missing = r.run('whoami', expect=2)
    check('drop login' in missing.stderr, 'remote data commands require login')
    status = r.json('status')
    check(status['url'] == pc.url and status['reachable'] and not status['loggedIn'], 'remote status reports reachable and logged out')
    r.run('login', '--password-stdin', stdin='wrong password\n', expect=1)
    r.run('login', '--email', ACCOUNT_EMAIL, '--password-stdin', stdin=PASSWORD + '\n', expect=2)
    check(not r.json('status')['loggedIn'], 'remote login refuses a wrong password and has no --email')
    login = r.json('login', '--password-stdin', stdin=PASSWORD + '\n')
    check(login == {'url': pc.url, 'instance': instance}, 'remote login with the password reports the instance')
    session = r.session()
    check(session.parent.name == 'remote-' + hashlib.sha256(pc.url.encode()).hexdigest()[:16], 'the session is keyed by the URL')
    if os.name != 'nt':
        check(session.stat().st_mode & 0o777 == 0o600, 'the remote session is private')
    check(PASSWORD not in session.read_text(encoding='utf-8') and ACCOUNT_EMAIL not in session.read_text(encoding='utf-8'),
          'the session file holds no password or account email')
    status = r.json('status')
    check(status['loggedIn'] and status['instance'] == instance and 'email' not in status, 'remote status reports the session')
    env_status = r.json('status', url=False, env={'DROP_URL': pc.url})
    check(env_status['url'] == pc.url and env_status['loggedIn'], 'DROP_URL selects remote mode')

    remote_me = r.json('whoami')
    check(remote_me == {'id': me, 'url': pc.url}, 'remote whoami reports the same account without a database')
    check(app_names(r) == app_names(pc), 'the remote client sees the same apps')
    check(r.json('op', 'me')['id'] == remote_me['id'], 'operations work with --url')
    added = add(r, 'Rooibosquartz app from the other computer')
    check(app_names(r)[0] == 'Rooibosquartz app from the other computer', 'remote list shows the remote app')
    check([g['id'] for g in r.json('search', 'rooibosquartz')['apps']] == [added['id']], 'remote search finds the remote app')
    check(r.json('sql', 'SELECT count(*) FROM apps')['rows'] == pc.json('sql', 'SELECT count(*) FROM apps')['rows'],
          'remote SQL reads the same apps')
    check([g['id'] for g in pc.json('search', 'rooibosquartz')['apps']] == [added['id']], 'the local client reads the remote app')

    for args in (['stop'], ['ensure-running'], ['start'], ['serve'], ['flush'], ['recover', '--keep', 'local'], ['init'],
                 ['password', '--password-stdin'], ['login-code']):
        refused = r.run(*args, expect=2)
        check('not available with --url' in refused.stderr, f'{args[0]} is refused in remote mode')
    check('unknown command' in r.run('account', expect=2).stderr, 'there is no account command')
    check(pc.json('status')['running'], 'the server keeps running after refused remote commands')
    check(r.run('whoami', '--db', str(pc.db), expect=None).returncode == 2, '--url with --db is refused')
    for url in ('http://example.com:8100', 'http://10.0.0.1:8100', 'https://127.0.0.1:8100'):
        check(r.run('status', url=url, expect=None).returncode == 2, f'non-loopback URL {url} is refused')

    with Forward(pc.port) as forward:
        via = f'http://127.0.0.1:{forward.port}'
        check(http_status(via + '/api/bundle/instance') == 403, 'a forward on a different port fails the Host check')
        check(not r.json('status', url=via)['reachable'], 'remote status through a wrong-port forward is not reachable')
        check(r.run('whoami', url=via, expect=None).returncode == 1, 'remote whoami through a wrong-port forward fails')
        login = r.run('login', '--password-stdin', stdin=PASSWORD + '\n', url=via, expect=None)
        check(login.returncode == 1, 'remote login through a wrong-port forward fails')

    closed = free_port()
    unreachable = r.run('whoami', url=f'http://127.0.0.1:{closed}', expect=1)
    check('not reachable' in unreachable.stderr, 'an unreachable server is reported')
    check(not listening(closed), 'remote mode never starts a server')

    original = session.read_text(encoding='utf-8')
    forged = json.loads(original)
    forged['instance'] = 'f' * len(instance)
    session.write_text(json.dumps(forged), encoding='utf-8')
    other = r.run('whoami', expect=1)
    check('different' in other.stderr and 'sign in again' in other.stderr, 'a session for another instance is refused')
    session.write_text(original, encoding='utf-8')
    check(r.json('whoami')['id'] == me, 'the restored session works again')

    skills = r.root / 'skill'
    r.json('skill', '--dir', str(skills))
    text = (skills / 'SKILL.md').read_text(encoding='utf-8')
    check(pc.url in text and str(r.binary) in text and '--url' in text and '--db' not in text and 'ensure-running' not in text
          and '{{' not in text and '# Drop (remote)' in text and not (skills / 'SKILL.remote.md').exists(),
          'the remote skill names the binary and URL and no local lifecycle')
    opened = r.json('open', '--no-browser')
    check(opened['url'] == pc.url and opened.get('note'), 'remote open prints the server URL')

    check(r.json('logout') == {'url': pc.url, 'loggedOut': True}, 'remote logout ends the session')
    r.run('whoami', expect=2)
    check(not r.json('status')['loggedIn'], 'after logout the remote is logged out')

    leftovers = [p for p in r.root.rglob('*') if p.name in ('drop.db', 'drop.json', 'local.key') or p.suffix in ('.db', '.lease')]
    check(not leftovers, f'remote mode creates no database, project or key: {leftovers}')
    check(sorted(p.name for p in r.work.iterdir()) == [], 'remote mode writes nothing to the working folder')


HOSTED_DOMAIN = 'drop.example.test'
APP_DOMAIN = 'apps.example.test'
# A synthetic secret value that must reach only the fake secret store.
SECRET_SENTINEL = 'synthetic-secret-sentinel-' + hashlib.sha256(b'drop e2e').hexdigest()[:16]


def build_hello(directory):
    """Build the test release tests/fixtures/hello: a static linux/amd64 executable, reproducibly."""
    go = shutil.which('go')
    if go is None:
        raise AssertionError('the Go toolchain is needed to build tests/fixtures/hello')
    out = Path(directory) / 'hello'
    env = {**os.environ, 'CGO_ENABLED': '0', 'GOOS': 'linux', 'GOARCH': 'amd64'}
    subprocess.run([go, 'build', '-trimpath', '-ldflags', '-s -w -buildid=', '-o', str(out), './tests/fixtures/hello'],
                   cwd=Path(__file__).resolve().parents[1], env=env, check=True, timeout=600)
    return out


def hosted_request(port, method, path, body=None, host=HOSTED_DOMAIN, raw=None):
    import http.client
    connection = http.client.HTTPConnection('127.0.0.1', port, timeout=30)
    data = raw if raw is not None else None if body is None else json.dumps(body).encode()
    headers = {'Host': host, 'Accept': 'application/json'}
    if raw is not None:
        headers['Content-Type'] = 'application/octet-stream'
    elif data is not None:
        headers['Content-Type'] = 'application/json'
    try:
        connection.request(method, path, body=data, headers=headers)
        response = connection.getresponse()
        return response.status, response.read()
    finally:
        connection.close()


def hosted_scenario(pc, r):
    """Hosted Multi serving with the real binary: refusals, startup, and the public boundary."""
    check(r.run('tunnel', 'serve', url=False, expect=2).returncode == 2, 'there is no tunnel command')
    check('tunnel' not in r.run('help', url=False).stdout, 'the overview has no tunnel')
    for args in (['serve', '--headless', '--domain', HOSTED_DOMAIN], ['serve', '--mcp'],
                 ['serve', '--hosted', '--headless', '--domain', HOSTED_DOMAIN],
                 ['serve', '--hosted-http-address', '127.0.0.1:1']):
        pc.run(*args, expect=2)
    check(True, 'native public mode is gone; hosted options require --hosted --headless --domain --mcp')
    hosted = r.root / 'hosted'
    hosted.mkdir()
    db = hosted / 'drop.db'
    port = free_port()
    command = [str(pc.binary), '--db', str(db), 'serve', '--hosted', '--hosted-init', '--headless', '--mcp',
               '--domain', HOSTED_DOMAIN, '--hosted-http-address', f'127.0.0.1:{port}',
               '--hosted-proxy-header', 'X-Forwarded-For', '--hosted-proxy-networks', '127.0.0.0/8']
    no_aws = subprocess.run(command, capture_output=True, text=True, encoding='utf-8', env=r.env, timeout=60)
    check(no_aws.returncode == 2 and 'DROP_' in no_aws.stderr and not db.exists(),
          'hosted serving without AWS settings or --fake-aws is refused and creates nothing')
    off_loopback = subprocess.run([*command[:-6], '--hosted-http-address', f'0.0.0.0:{port}', '--fake-aws', str(hosted / 'fake-aws')],
                                  capture_output=True, text=True, encoding='utf-8', env=r.env, timeout=60)
    check(off_loopback.returncode == 2 and '--fake-aws' in off_loopback.stderr and not db.exists(),
          '--fake-aws is refused with a non-loopback listen address')
    command += ['--fake-aws', str(hosted / 'fake-aws')]
    refused = subprocess.run(command, capture_output=True, text=True, encoding='utf-8', env=r.env, timeout=60)
    check(refused.returncode != 0 and 'OWNER_EMAIL' in refused.stderr and not db.exists(),
          'hosted serving refuses a missing owner and SMTP configuration and creates nothing')
    env = {**r.env, 'DROP_OWNER_EMAIL': 'owner@example.test', 'DROP_SMTP_FROM': 'login@example.test', 'DROP_APP_DOMAIN': APP_DOMAIN,
           'DROP_SMTP_HOST': '127.0.0.1', 'DROP_SMTP_PORT': '9', 'DROP_SMTP_USERNAME': 'synthetic',
           'DROP_SMTP_PASSWORD': 'synthetic'}
    output = open(hosted / 'serve.log', 'w+', encoding='utf-8')
    server = subprocess.Popen(command, stdout=output, stderr=subprocess.STDOUT, env=env)
    try:
        deadline = time.time() + 60
        while not listening(port):
            if server.poll() is not None or time.time() > deadline:
                output.seek(0)
                raise AssertionError(f'hosted server did not start: {output.read()[-2000:]}')
            time.sleep(0.2)
        check(hosted_request(port, 'GET', '/up', host='127.0.0.1')[0] == 200, 'the hosted server answers /up')
        check(hosted_request(port, 'GET', '/', host='other.example.test')[0] == 403, 'another Host is refused')
        status, body = hosted_request(port, 'GET', '/api/bundle/auth-mode')
        check(status == 200 and json.loads(body).get('mode') == 'email', 'hosted sign-in is by email')
        check(hosted_request(port, 'GET', '/')[0] == 200, 'the reader is served on the hosted origin')
        for method, path in (('POST', '/api/context/query'), ('POST', '/api/context/search'), ('GET', '/api/context/schema'),
                             ('POST', '/api/collections/users/auth-with-password'), ('POST', '/api/bundle/token'),
                             ('POST', '/api/collections/apps/records'), ('GET', '/_/')):
            check(hosted_request(port, method, path, {} if method == 'POST' else None)[0] == 404, f'{method} {path} is 404 publicly')
        status, body = hosted_request(port, 'GET', '/.well-known/oauth-protected-resource')
        check(status == 200 and json.loads(body).get('resource') == f'https://{HOSTED_DOMAIN}/mcp', 'remote MCP is announced')
        check(hosted_request(port, 'POST', '/mcp', {})[0] == 401, 'MCP requires OAuth')
        status, body = hosted_request(port, 'POST', '/api/collections/users/request-otp', {'email': 'stranger@example.test'})
        check(status == 200 and 'otpId' in json.loads(body), 'an unknown address gets the ordinary sign-in answer')
        check(hosted_request(port, 'POST', '/api/drop/op', {'op': 'me'})[0] == 401, 'operations require a session')
        invited = subprocess.run([str(pc.binary), '--db', str(db), 'invite', 'create', 'invitee@example.test', '--role', 'admin'],
                                 capture_output=True, text=True, encoding='utf-8', env=r.env, timeout=60)
        check(invited.returncode == 0, f'the owner invites through the private socket: {invited.stderr}')
        check(json.loads(invited.stdout)['sign_in_url'] == f'https://{HOSTED_DOMAIN}/#/invite?email=invitee%40example.test',
              'the sign-in URL is on the hosted domain')
        # The sign-in email cannot be sent here (no SMTP server), but the
        # invited address gets its account before the send.
        hosted_request(port, 'POST', '/api/collections/users/request-otp', {'email': 'Invitee@Example.test'})

        # Apps and secrets through the private socket, with the fake AWS directory.
        def owner(*args, stdin=None, expect=0):
            result = subprocess.run([str(pc.binary), '--db', str(db), *args], input=stdin, capture_output=True, text=True,
                                    encoding='utf-8', env=r.env, timeout=60)
            check(result.returncode == expect, f'drop {" ".join(args[:2])} exits {expect} {result.stderr.strip()[-300:]}'.rstrip())
            return json.loads(result.stdout) if expect == 0 else result
        app = owner('app', 'create', 'Hosted app', '--slug', 'hosted-app')['app']
        check(app['hostname'] == 'hosted-app.' + APP_DOMAIN, f"the hostname is the slug under DROP_APP_DOMAIN: {app['hostname']}")
        set_result = owner('secret', 'set', 'hosted-app', 'API_KEY', stdin=SECRET_SENTINEL + '\n')
        check(set_result['secret']['version'] == 1 and set_result['secret']['length'] == len(SECRET_SENTINEL)
              and set_result['deployment_id'] == app['nib_app_id'] + '-dep-2', 'secret set stores a version and bumps the deployment')
        check(SECRET_SENTINEL not in json.dumps(owner('secret', 'list', 'hosted-app')) + json.dumps(owner('app', 'show', 'hosted-app')),
              'secret list and app show never return the value')
        stored = [p for p in (hosted / 'fake-aws' / 'secrets').rglob('*') if p.is_file() and p.name == '1']
        check(len(stored) == 1 and stored[0].read_text(encoding='utf-8') == SECRET_SENTINEL
              and (stored[0].parent / 'name').read_text(encoding='utf-8').strip() == '/drop/apps/' + app['id'] + '/API_KEY',
              'the value is in the secret store at <prefix>/apps/<app id>/<NAME>')
        check(owner('secret', 'delete', 'hosted-app', 'API_KEY')['deployment_id'] == app['nib_app_id'] + '-dep-3', 'secret delete bumps the deployment')

        # Releases: the test executable, built from tests/fixtures/hello, through the CLI and a curl-style PUT.
        hello = build_hello(hosted)
        hello_bytes = hello.read_bytes()
        hello_sha = hashlib.sha256(hello_bytes).hexdigest()
        not_elf = hosted / 'script.sh'
        not_elf.write_text('#!/bin/sh\necho hello\n', encoding='utf-8')
        refused_file = owner('release', 'upload', 'hosted-app', str(not_elf), expect=2)
        check('linux/amd64 ELF' in refused_file.stderr, 'release upload refuses a file that is not a linux/amd64 executable before asking for a link')
        uploaded = owner('release', 'upload', 'hosted-app', str(hello), '--activate', '--note', 'e2e build')
        check(uploaded['created'] and uploaded['deployed'] and uploaded['release']['sha256'] == hello_sha
              and uploaded['release']['size_bytes'] == len(hello_bytes) and uploaded['release']['s3_key'] == 'releases/' + hello_sha
              and uploaded['deployment_id'] == app['nib_app_id'] + '-dep-4', f'release upload --activate stores and deploys the release: {uploaded}')
        blob = hosted / 'fake-aws' / 'blobs' / 'releases' / hello_sha
        check(blob.is_file() and blob.read_bytes() == hello_bytes, 'the release is in the fake store at releases/<sha256>')
        link = owner('op', 'release.upload_link', json.dumps({'app': 'hosted-app'}))
        check(link['url'] == f'https://{HOSTED_DOMAIN}' + link['path'] and link['method'] == 'PUT' and link['max_bytes'] == 90 << 20,
              f'release.upload_link returns a one-time URL on the hosted domain: {link["max_bytes"]}')
        status, body = hosted_request(port, 'PUT', link['path'], raw=b'not an executable')
        check(status == 400 and json.loads(body)['code'] == 'invalid_release', 'the public upload route refuses a non-ELF body')
        status, body = hosted_request(port, 'PUT', link['path'], raw=hello_bytes)
        answer = json.loads(body)
        check(status == 200 and not answer['created'] and answer['release']['id'] == uploaded['release']['id'] and not answer['deployed'],
              f'the same bytes through the public route reuse the release: {status} {answer}')
        # A refused link answers before the body is read (curl waits for it with Expect: 100-continue), so send a short body.
        status, body = hosted_request(port, 'PUT', link['path'], raw=hello_bytes[:4096])
        check(status == 404 and json.loads(body)['code'] == 'upload_link_invalid', 'an upload link works once')
        check(hosted_request(port, 'PUT', '/api/drop/upload/short', raw=hello_bytes[:4096])[0] == 404, 'a malformed upload token is not routed')
        releases = owner('release', 'list', 'hosted-app')
        check(len(releases['releases']) == 1 and releases['current_release'] == uploaded['release']['id']
              and releases['releases'][0]['note'] == 'e2e build', f'release list shows the one current release: {releases}')
        deployed = owner('release', 'deploy', 'hosted-app', uploaded['release']['id'])
        check(not deployed['deployment_bumped'] and deployed['deployment_id'] == app['nib_app_id'] + '-dep-4',
              'deploying the current release again starts no deployment')

        agent_secrets, agent_texts = nodes_scenario(pc, r, owner, port, hosted, app)
    finally:
        server.send_signal(signal.SIGTERM)
        try:
            server.wait(timeout=60)
        except subprocess.TimeoutExpired:
            server.kill()
            server.wait()
        output.close()
    check(server.returncode == 0, 'the hosted server stops cleanly')
    with closing(sqlite3.connect(db)) as conn:
        users = conn.execute('SELECT email, role FROM users ORDER BY created').fetchall()
        used = conn.execute("SELECT count(*) FROM invites WHERE used_by != ''").fetchone()[0]
    check(users == [('owner@example.test', 'admin'), ('invitee@example.test', 'admin')] and used == 1,
          f'only the owner (an admin) and the invited address (with the invite\'s role) got accounts: {users}')
    with closing(sqlite3.connect(db)) as conn:
        dump = '\n'.join(str(row) for table in [t[0] for t in conn.execute("SELECT name FROM sqlite_master WHERE type = 'table'")]
                          for row in conn.execute(f'SELECT * FROM `{table}`'))
    output_text = (hosted / 'serve.log').read_text(encoding='utf-8', errors='replace')
    check('API_KEY' in dump and SECRET_SENTINEL not in dump, 'the database holds the secret name and never its value')
    check(SECRET_SENTINEL not in output_text, 'the server output never holds the secret value')
    token = link['path'].rsplit('/', 1)[1]
    check(token not in dump and token not in output_text, 'the upload token is in neither the database nor the server output')
    # The leak scan (tests/leak_scan.py): the database row by row and as raw
    # bytes, the server output, the server's private cache (its working copy
    # and request log database) and the fake AWS directory except the secret
    # store, plus what the agent received (desired documents, reports).
    leak_scan.require_clean({'secret value': SECRET_SENTINEL, 'upload token': token, **agent_secrets},
                            paths=[db, hosted / 'serve.log', user_cache(r.home), hosted / 'fake-aws'],
                            texts=agent_texts, exclude=[hosted / 'fake-aws' / 'secrets'])
    check(True, 'no secret value, upload token or node password in the database, logs, cache, release store or agent answers')


def nodes_scenario(pc, r, owner, port, hosted, app):
    """Nodes, placements and the agent API with the fake agent (tests/fake_agent.py) against the real binary."""
    host_id = 'host-e2e-0001'
    owner('secret', 'set', 'hosted-app', 'API_KEY', stdin=SECRET_SENTINEL + '\n')
    created = owner('node', 'create', 'node-one', '--host-id', host_id)
    password = created['password']
    check(len(password) == 40 and created['identity'] == 'node-one' and created['collection'] == 'nodes',
          'node create prints the agent password once with its sign-in identity')
    agent = fake_agent.FakeAgent(port, HOSTED_DOMAIN, 'node-one', password, host_id, hosted / 'fake-aws')
    try:
        check(agent.login('x' * 40) == 400 and agent.login() == 200 and agent.refresh() == 200,
              'the node signs in with its password on the hosted origin and refreshes its token')
        status, _ = agent.request('POST', '/api/drop/op', {'op': 'me'})
        check(status == 403, 'a node token cannot call operations')
        status, _ = agent.request('GET', '/api/collections/apps/records')
        status_users, _ = agent.request('GET', '/api/collections/users/records')
        check(status == 200 and status_users == 404, 'a node token reads no app record and no account')
        check(agent.request('GET', '/api/drop/agent/desired', token='')[0] == 401, 'the agent routes need a node token')
        agent.subscribe()
        empty = agent.pull()
        check(empty['apps'] == [] and empty['rejected'] == [], 'a new node has an empty desired document')
        placed = owner('place', 'hosted-app', 'node-one')
        check(placed['placement']['node_name'] == 'node-one' and not placed['moved'], 'drop place puts the app on the node')
        seq = agent.next_seq(empty['desiredSeq'])
        desired = agent.pull()
        check(desired['desiredSeq'] == seq and [a['appId'] for a in desired['apps']] == [app['nib_app_id']],
              'the placement wakes the agent through realtime nodes/<id>, and the pull carries the app')
        entry = desired['apps'][0]
        check(entry['deploymentId'] == app['nib_app_id'] + '-dep-5' and entry['hostnames'] == [{'hostname': 'hosted-app.' + APP_DOMAIN, 'kind': 'platform'}],
              f"the app's deployment and hostname: {entry['deploymentId']}")
        check(SECRET_SENTINEL not in json.dumps(desired) and agent.resolve(entry) == {'API_KEY': SECRET_SENTINEL},
              'the document names the secret version only; the agent resolves the value from the store')
        check(owner('app', 'status', 'hosted-app')['phase'] == 'pending', 'the app is pending until the node reports')
        status, result = agent.report(agent.report_body(desired, extra_apps=[
            {'appId': 'd-' + 'z' * 15, 'deploymentId': '', 'phase': 'running', 'message': '', 'restartCount': 0, 'lastExitCode': None, 'convergedAt': ''}]))
        check(status == 200 and result['accepted'] == 1 and result['ignored'] == ['d-' + 'z' * 15] and result['desiredSeq'] == seq,
              f'the report is taken for the placed app only: {result}')
        shown = owner('app', 'status', 'hosted-app')
        check(shown['phase'] == 'running' and shown['node'] == 'node-one', f'app status shows the reported phase: {shown}')
        wrong = agent.report_body(desired)
        wrong['hostId'] = 'another-host'
        check(agent.report(wrong)[0] == 409, 'a report for another host is refused')
        owner('app', 'stop', 'hosted-app')
        stopped_seq = agent.next_seq(seq)
        stopped = agent.pull()
        check(stopped['desiredSeq'] == stopped_seq and stopped['apps'][0]['desiredState'] == 'stopped',
              'app stop bumps desired_seq and the pull says stopped')
        owner('app', 'set', 'hosted-app', '--name', 'Renamed hosted app')
        check(agent.pull()['desiredSeq'] == stopped_seq, 'a rename does not change the desired document')
        listed = owner('node', 'list')['nodes']
        check(len(listed) == 1 and listed[0]['online'] and listed[0]['apps'] == 1 and listed[0]['agent_version'] == agent.version,
              f'node list shows the node online with its app: {listed}')
        rotated = owner('node', 'rotate-secret', 'node-one')['password']
        after = (agent.request('GET', '/api/drop/agent/desired')[0], agent.login())
        check(after == (401, 400), f'rotating the password ends the old token and password: {after} {agent.last_error}')
    finally:
        agent.close()
    # The standalone fake agent: one contract pass, as a separate process.
    run = subprocess.run([sys.executable, str(Path(__file__).with_name('fake_agent.py')), '--port', str(port), '--domain', HOSTED_DOMAIN,
                          '--node', 'node-one', '--host-id', host_id, '--fake-aws', str(hosted / 'fake-aws')],
                         input=rotated + '\n', capture_output=True, text=True, timeout=120)
    check(run.returncode == 0 and json.loads(run.stdout)['apps'] == [app['nib_app_id']],
          f'tests/fake_agent.py passes the contract with the new password: {run.stdout[-300:]} {run.stderr[-300:]}')
    owner('node', 'set-state', 'node-one', 'disabled')
    disabled = fake_agent.FakeAgent(port, HOSTED_DOMAIN, 'node-one', rotated, host_id)
    check(disabled.login() == 403, f'a disabled node cannot sign in (403): {disabled.last_error}')
    # For the leak scan: the node passwords, and what the agent received.
    return ({'node password': password, 'rotated node password': rotated},
            {'desired document': json.dumps(desired), 'stopped document': json.dumps(stopped), 'standalone agent output': run.stdout + run.stderr})


def codes_scenario(pc, r):
    """One-time sign-in codes for the project's account."""
    # Started first so the 30-second expiry overlaps the other checks.
    expiring = pc.json('login-code', '--ttl', '30')
    expiring_at = time.monotonic()
    check(sorted(expiring) == ['code', 'expires'] and expiring['code'] and expiring['expires'],
          'login-code accepts the minimum lifetime of 30 seconds')
    me = pc.json('whoami')['id']

    issued = pc.json('login-code')
    check(issued['code'] and issued['expires'], 'login-code issues a code')
    instance = json.loads((pc.project / 'drop.json').read_text(encoding='utf-8'))['instance']
    login = r.json('login', '--code-stdin', stdin=issued['code'] + '\n')
    check(login == {'url': pc.url, 'instance': instance}, 'remote login redeems a code')
    check(r.json('whoami')['id'] == me, 'the code signs in the project account')
    check([g['id'] for g in r.json('search', 'rooibosquartz')['apps']] != [], 'a code session reads the apps')
    r.json('logout')
    reused = r.run('login', '--code-stdin', stdin=issued['code'] + '\n', expect=1)
    check('used or expired' in reused.stderr, 'a redeemed code cannot be reused')
    unknown = r.run('login', '--code-stdin', stdin='synthetic-unknown-code\n', expect=1)
    check('used or expired' in unknown.stderr, 'an unknown code is refused')
    check(not r.json('status')['loggedIn'], 'a refused code leaves the remote logged out')
    check('no sign-in code' in r.run('login', '--code-stdin', stdin='\n', expect=2).stderr, 'an empty code is a usage error')

    log = Path(pc.json('status')['log'])
    if log.exists():
        check(issued['code'] not in log.read_text(encoding='utf-8', errors='replace'), 'the server log does not contain the code')

    pc.run('login-code', '--email', 'someone@example.localhost', expect=2)
    check(True, 'login-code has no --email')
    for ttl in ('10', '601'):
        pc.run('login-code', '--ttl', ttl, expect=2)
        check(True, f'login-code refuses --ttl {ttl}')

    pc.run('open', '--email', 'someone@example.localhost', '--no-browser', expect=2)
    check(True, 'open has no --email')
    opened = pc.json('open', '--no-browser')
    code = opened['signInLink'].split('#', 1)[1]
    status, body = pc.http('POST', '/api/bundle/redeem', {'code': code})
    check(status == 200 and json.loads(body)['record']['id'] == me, 'the open link signs in the project account')
    status, _ = pc.http('POST', '/api/bundle/redeem', {'code': code})
    check(status == 401, 'the open link is single-use')

    r.run('login', '--code-stdin', '--password-stdin', stdin=issued['code'] + '\n', expect=2)
    check(True, 'login refuses --code-stdin with --password-stdin')

    remaining = 31 - (time.monotonic() - expiring_at)
    if remaining > 0:
        time.sleep(remaining)
    expired = r.run('login', '--code-stdin', stdin=expiring['code'] + '\n', expect=1)
    check('used or expired' in expired.stderr, 'an expired code is refused')
    check(not r.json('status')['loggedIn'], 'an expired code leaves the remote logged out')


if __name__ == '__main__':
    try:
        main()
    except AssertionError as error:
        print('FAIL -', error)
        sys.exit(1)
