#!/usr/bin/env node
// Synthetic real-browser check of the built (embedded) reader. Run after
// make ui && make build, and (cd ui && pnpm exec playwright install chromium).
// No real project is opened. Options: --binary PATH, --browser PATH,
// --screenshots DIR (keep screenshots there).
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdir, mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
function option(name, fallback) {
  const index = process.argv.indexOf(name);
  if (index < 0) return fallback;
  assert(process.argv[index + 1], `${name} requires a value`);
  return process.argv[index + 1];
}
const binary = path.resolve(option('--binary', path.join(root, 'bin', process.platform === 'win32' ? 'drop.exe' : 'drop')));
const { chromium } = createRequire(path.join(root, 'ui/package.json'))('playwright');
const temporary = await mkdtemp(path.join(os.tmpdir(), 'drop-reader-test-'));
const screenshotOption = option('--screenshots');
const screenshots = screenshotOption ? path.resolve(screenshotOption) : path.join(temporary, 'screenshots');
const home = path.join(temporary, 'home');
const db = path.join(temporary, 'project', 'drop.db');
await Promise.all([mkdir(home), mkdir(path.dirname(db)), mkdir(screenshots, { recursive: true })]);
// Inherited product variables would redirect the binary; scrub them as tests/e2e.py does.
const scrubbed = ['DROP_', 'POCKET' + 'CONTEXT_'];
const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !scrubbed.some(prefix => key.startsWith(prefix))));
Object.assign(env, { HOME: home, USERPROFILE: home, XDG_CACHE_HOME: path.join(home, 'cache'), LOCALAPPDATA: path.join(home, 'AppData', 'Local') });
const children = [];
let browser;

async function freePort() {
  const server = net.createServer();
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const port = server.address().port;
  await new Promise(resolve => server.close(resolve));
  return port;
}
function start(command, argv, options = {}) {
  const child = spawn(command, argv, { stdio: ['pipe', 'pipe', 'pipe'], ...options });
  child.output = '';
  child.stdout.on('data', chunk => { child.output += chunk; });
  child.stderr.on('data', () => {}); // Never print authentication URLs or request logs.
  children.push(child);
  return child;
}
async function cli(argv, input) {
  const child = start(binary, ['--db', db, ...argv], { env, cwd: temporary });
  const ended = once(child, 'exit');
  child.stdin.end(input);
  const timeout = setTimeout(() => child.kill('SIGKILL'), 120_000);
  try {
    const [code] = await ended;
    assert.equal(code, 0, `CLI ${argv[0]} failed`);
    return child.output.trim() ? JSON.parse(child.output) : null;
  } finally { clearTimeout(timeout); }
}
function ok(message) { console.log(`ok - ${message}`); }
const key = 'drop.reader.auth';

try {
  const port = await freePort();
  const origin = `http://127.0.0.1:${port}`;
  const password = 'synthetic reader password';
  // init starts the server in the background with the embedded reader.
  await cli(['init', '--port', String(port), '--password-stdin'], `${password}\n`);
  await cli(['app', 'create', 'An app from the CLI', '--slug', 'cli-app']);

  const served = await fetch(origin);
  assert.equal(served.status, 200);
  const html = await served.text();
  assert(!html.includes('/@vite/client'), 'the embedded reader is served, not the development UI');
  const built = await readFile(path.join(root, 'ui/dist/index.html'), 'utf8').catch(() => '');
  assert.equal(html, built, 'the binary must embed the current ui/dist; rebuild with make ui && make build');
  assert.match(served.headers.get('content-security-policy'), /script-src 'self';/);
  ok('the binary serves the current built reader under the production policy');
  const assetDir = path.join(root, 'ui/dist/assets');
  const launchers = (await readdir(assetDir)).filter(name => name.includes('drop-guest') && name.endsWith('.js'));
  assert.equal(launchers.length, 1, 'the build includes the split archive launcher');
  const launcher = await fetch(origin + '/assets/' + launchers[0]);
  assert.equal(launcher.status, 200, 'the compiled binary serves the lazy archive launcher');
  assert((await readFile(path.join(assetDir, launchers[0]))).equals(Buffer.from(await launcher.arrayBuffer())),
    'the response contains the built launcher bytes');
  ok('the binary serves the generated archive launcher chunk');

  browser = await chromium.launch({ headless: true, executablePath: option('--browser') });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: 'light' });
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
  await page.goto(origin);
  await page.getByRole('heading', { name: 'Sign in' }).waitFor();
  assert.equal(await page.getByLabel('Email').count(), 0, 'the single account signs in with its password alone');
  await page.getByLabel('Password').fill('not the password');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.getByRole('alert').waitFor();
  assert.equal(await page.evaluate(k => localStorage.getItem(k), key), null);
  ok('a wrong password shows an alert and stores nothing');
  await page.screenshot({ path: path.join(screenshots, 'sign-in-light.png') });

  const link = await cli(['open', '--no-browser']);
  assert(link.signInLink.startsWith(`${origin}/bundle/login#`));
  await page.goto(link.signInLink);
  await page.getByRole('button', { name: 'Account menu' }).waitFor();
  assert.equal(new URL(page.url()).pathname, '/');
  assert.equal(new URL(page.url()).hash, '', 'the code never stays in the address bar');
  await page.getByText('An app from the CLI', { exact: true }).waitFor();
  ok('the single-use open link signs in and shows the apps');
  const reused = await (await browser.newContext()).newPage();
  await reused.goto(link.signInLink);
  await reused.getByText(/used or expired/).waitFor();
  await reused.context().close();
  ok('the open link works only once');

  const messages = ['First synthetic hello', 'Second synthetic hello <b>bold</b>', 'Third app about telescopes'];
  const slugs = ['first-app', 'second-app', 'third-app'];
  const appList = page.getByRole('list', { name: 'Apps, newest first' });
  for (const [n, message] of messages.entries()) {
    await page.getByRole('link', { name: 'New app' }).click();
    await page.getByRole('heading', { name: 'New app' }).waitFor();
    await page.getByLabel('App name').fill(message);
    await page.getByLabel('Slug').fill(slugs[n]);
    await page.getByRole('button', { name: 'Create app' }).click();
    await page.getByRole('heading', { name: message, exact: true }).waitFor();
    assert.match(new URL(page.url()).hash, /^#\/apps\/[a-z0-9]{15}$/, 'a new app opens its page');
    await page.getByRole('link', { name: '← Your apps' }).click();
    await appList.getByText(message, { exact: true }).waitFor();
    await page.waitForTimeout(20); // Distinct creation times keep the order deterministic.
  }
  const listed = await appList.locator('li > p > a').allTextContents();
  assert.deepEqual(listed, [...messages].reverse().concat('An app from the CLI'), 'apps are listed newest first');
  assert.equal(await page.locator('.apps b').count(), 0, 'names render as text');
  await page.getByText(/GiB retained$/).waitFor();
  await page.getByRole('link', { name: 'New app' }).click();
  await page.getByLabel('App name').fill('Duplicate slug');
  await page.getByLabel('Slug').fill('first-app');
  await page.getByRole('button', { name: 'Create app' }).click();
  await page.getByRole('alert').getByText(/taken/).waitFor();
  await page.getByRole('link', { name: '← Your apps' }).click();
  const cliList = await cli(['app', 'list']);
  assert.equal(cliList.apps.length, 4, 'the CLI sees the apps created in the reader, and no duplicate slug');
  ok('apps created in the reader appear newest first as plain text; a duplicate slug is refused');

  // The app page: Settings starts a new deployment; Env & secrets sets a variable.
  await appList.getByRole('link', { name: 'Third app about telescopes' }).click();
  await page.getByRole('link', { name: 'Settings' }).click();
  assert.match(new URL(page.url()).hash, /^#\/apps\/[a-z0-9]{15}\/settings$/);
  await page.getByLabel('Memory (MiB)').fill('1024');
  await page.getByRole('button', { name: 'Save settings' }).click();
  await page.getByText(/^Saved\. New deployment d-[a-z0-9]{15}-dep-2\.$/).waitFor();
  await page.getByRole('link', { name: 'Env & secrets' }).click();
  await page.getByLabel('Variable name').fill('WELCOME_TEXT');
  await page.getByLabel('Variable value').fill('hello');
  await page.getByRole('button', { name: 'Set variable' }).click();
  await page.getByRole('list', { name: 'Environment variables' }).getByText('WELCOME_TEXT').waitFor();
  assert.equal(await page.getByLabel('Secret value').getAttribute('type'), 'password', 'secret values are typed into a password field');
  await page.screenshot({ path: path.join(screenshots, 'app-env-light.png'), fullPage: true });
  // Releases: the local server has no release store, so only the list and the local file check.
  await page.getByRole('navigation', { name: 'App' }).getByRole('link', { name: 'Releases' }).click();
  assert.match(new URL(page.url()).hash, /^#\/apps\/[a-z0-9]{15}\/releases$/);
  await page.getByText('No releases yet.').waitFor();
  await page.getByLabel('App file').setInputFiles({ name: 'script.sh', mimeType: 'text/plain', buffer: Buffer.from('#!/bin/sh\necho hello\n') });
  await page.getByRole('alert').getByText(/not an ELF executable/).waitFor();
  assert(await page.getByRole('button', { name: 'Upload app file' }).isDisabled(), 'a file that is not a linux/amd64 executable cannot be uploaded');
  await page.screenshot({ path: path.join(screenshots, 'app-releases-light.png'), fullPage: true });
  await page.getByRole('link', { name: 'Overview' }).click();
  await page.getByText(/-dep-3$/).waitFor();
  await page.getByText('No release', { exact: true }).waitFor();
  await page.getByRole('link', { name: '← Your apps' }).click();
  ok('the app page saves settings and environment variables, each a new deployment, and checks a release file before upload');

  await page.getByRole('link', { name: 'Connect' }).click();
  await page.getByRole('heading', { name: 'Connect an assistant' }).waitFor();
  await page.getByText(/hosted Drop only/).waitFor();
  assert.equal(new URL(page.url()).hash, '#/connect');
  assert.equal(await page.getByText('account@drop.invalid').count(), 0, 'the local account address is never shown');
  await page.getByRole('link', { name: 'Apps' }).click();
  await page.getByRole('heading', { name: 'Your apps' }).waitFor();
  ok('the Connect page says remote MCP is hosted only, and the Apps link returns');

  // The local account is an admin: it has the Users page.
  await page.getByRole('link', { name: 'Users' }).click();
  await page.getByRole('heading', { name: 'Users', exact: true }).waitFor();
  assert.equal(new URL(page.url()).hash, '#/admin/users');
  await page.getByLabel('Email').fill('friend@example.test');
  await page.getByRole('button', { name: 'Invite' }).click();
  await page.getByText(`${origin}/#/invite?email=friend%40example.test`, { exact: true }).waitFor();
  const invites = page.getByRole('list', { name: 'Invites, newest first' });
  await invites.getByText('friend@example.test', { exact: true }).waitFor();
  page.once('dialog', dialog => void dialog.accept());
  await invites.getByRole('button', { name: 'Revoke' }).click();
  await invites.getByText(/Revoked/).waitFor();
  const cliInvites = await cli(['invite', 'list']);
  assert.equal(cliInvites.invites[0].state, 'revoked', 'the CLI sees the invite revoked in the reader');
  await page.getByRole('button', { name: 'Suspend' }).first().isDisabled().then(disabled => assert(disabled, 'the owner cannot be suspended'));
  await page.screenshot({ path: path.join(screenshots, 'admin-users-light.png'), fullPage: true });
  await page.getByRole('link', { name: 'Apps' }).click();
  await page.getByRole('heading', { name: 'Your apps' }).waitFor();
  ok('the admin invites from the Users page, gets the sign-in link and revokes the invite');

  // Nodes: add one (its agent password is shown once) and place an app.
  await page.getByRole('link', { name: 'Nodes' }).click();
  await page.getByRole('heading', { name: 'Nodes', level: 2 }).waitFor();
  assert.equal(new URL(page.url()).hash, '#/admin/nodes');
  await page.getByText('No nodes yet.').waitFor();
  await page.getByLabel('Name', { exact: true }).fill('node-one');
  await page.getByLabel('Host id').fill('host-browser-1');
  await page.getByRole('button', { name: 'Add node' }).click();
  await page.getByText(/Agent password for node-one/).waitFor();
  const nodePassword = await page.locator('.invite-link code').textContent();
  assert.match(nodePassword, /^[A-Za-z0-9]{40}$/, 'a 40-character agent password');
  await page.getByRole('list', { name: 'Nodes by name' }).getByText('node-one', { exact: true }).waitFor();
  await page.getByLabel('App (id or slug)').fill('third-app');
  await page.getByRole('combobox', { name: /^Node/ }).selectOption('node-one');
  await page.getByRole('button', { name: 'Place', exact: true }).click();
  await page.getByText('third-app is placed on node-one.').waitFor();
  await page.screenshot({ path: path.join(screenshots, 'admin-nodes-light.png'), fullPage: true });
  const cliNodes = await cli(['node', 'list']);
  assert.equal(cliNodes.nodes[0].apps, 1, 'the CLI sees the placement made in the reader');
  await page.getByRole('link', { name: 'Apps' }).click();
  await page.getByRole('heading', { name: 'Your apps' }).waitFor();
  ok('the admin adds a node from the Nodes page, sees its password once and places an app');

  await page.screenshot({ path: path.join(screenshots, 'reader-light.png'), fullPage: true });

  const lightBackground = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  await page.emulateMedia({ colorScheme: 'dark' });
  const darkBackground = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  assert.notEqual(lightBackground, darkBackground, 'dark mode changes the palette');
  await page.screenshot({ path: path.join(screenshots, 'reader-dark.png'), fullPage: true });
  await page.setViewportSize({ width: 375, height: 800 });
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'no horizontal scroll at phone width');
  await page.screenshot({ path: path.join(screenshots, 'reader-mobile-dark.png'), fullPage: true });
  await page.emulateMedia({ colorScheme: 'light' });
  await page.setViewportSize({ width: 1280, height: 900 });
  ok('light, dark and phone layouts render');

  await page.getByRole('button', { name: 'Account menu' }).click();
  await page.getByRole('button', { name: 'Sign out' }).click();
  await page.getByRole('heading', { name: 'Sign in' }).waitFor();
  assert.equal(await page.evaluate(k => localStorage.getItem(k), key), null);
  await page.reload();
  await page.getByRole('heading', { name: 'Sign in' }).waitFor();
  ok('sign out clears the stored session and survives a reload');

  const code = await cli(['login-code']);
  await page.getByRole('button', { name: 'Use a sign-in code instead' }).click();
  await page.getByLabel('Sign-in code').fill(code.code);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.getByText('Third app about telescopes', { exact: true }).waitFor();
  ok('a login code signs in the same account, which sees the same apps');
  await page.getByRole('button', { name: 'Account menu' }).click();
  await page.getByRole('button', { name: 'Sign out' }).click();
  await page.getByLabel('Password').fill(password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.getByText('Third app about telescopes', { exact: true }).waitFor();
  ok('password sign-in works');

  await page.getByRole('link', { name: messages[2], exact: true }).click();
  const deleteButton = page.getByRole('button', { name: 'Delete app permanently' });
  await deleteButton.focus();
  await page.keyboard.press('Enter');
  const modal = page.getByRole('dialog');
  await modal.waitFor();
  await page.keyboard.press('Escape');
  await modal.waitFor({state: 'hidden'});
  assert(await deleteButton.evaluate(el => el === document.activeElement), 'dismissal returns keyboard focus');
  await page.keyboard.press('Enter');
  const confirm = modal.getByRole('button', {name: 'Delete ' + messages[2], exact: true});
  assert(await confirm.isDisabled());
  await modal.getByLabel('Type third-app to confirm').fill('third-app');
  await confirm.click({trial: true}); // Wait for the inventory read to enable confirmation.
  await confirm.focus();
  await page.keyboard.press('Enter');
  await page.getByRole('heading', {name: 'Your apps'}).waitFor();
  await page.reload();
  await page.getByRole('list', {name: 'Apps, newest first'}).waitFor();
  assert.equal(await page.getByRole('link', {name: messages[2], exact: true}).count(), 0);
  ok('confirmed deletion removes the app from the list across reloads');

  // Refused requests (a wrong password, a taken slug) log their status.
  const unexpected = errors.filter(text => !/Failed to load resource: the server responded with a status of (400|409)/.test(text));
  assert.deepEqual(unexpected, [], 'no runtime or security-policy errors');
  console.log('all reader browser checks passed');
} finally {
  await browser?.close();
  await cli(['stop']).catch(() => {});
  for (const child of children) if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
  await Promise.all(children.map(child => child.exitCode === null && child.signalCode === null ? once(child, 'exit').catch(() => {}) : undefined));
  await rm(temporary, { recursive: true, force: true });
}
