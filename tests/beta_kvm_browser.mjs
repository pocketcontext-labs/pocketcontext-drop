// The browser talks to the real disposable Drop server. Only the public hostname
// and TLS termination are provided by a temporary loopback reverse proxy.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { setTimeout as delay } from 'node:timers/promises';
import path from 'node:path';
import http from 'node:http';
import https from 'node:https';
import {once} from 'node:events';
const { chromium } = createRequire(new URL('../ui/package.json', import.meta.url))('playwright');
const [port, crew, directory, phase] = process.argv.slice(2);
assert.ok(['join', 'deploy'].includes(phase));
const domain = 'drop.example.test';
const proxy = https.createServer({
  key: await readFile(path.join(directory, 'key.pem')),
  cert: await readFile(path.join(directory, 'cert.pem')),
}, (incoming, outgoing) => {
  const headers = {...incoming.headers, host: domain};
  if (headers.origin === origin) headers.origin = 'https://' + domain;
  const upstream = http.request({
    hostname: '127.0.0.1', port, path: incoming.url, method: incoming.method, headers,
  }, reply => {
    outgoing.writeHead(reply.statusCode, reply.headers);
    reply.pipe(outgoing);
  });
  upstream.on('error', () => outgoing.destroy());
  outgoing.on('close', () => upstream.destroy());
  incoming.pipe(upstream);
});
proxy.listen(0, '127.0.0.1');
await once(proxy, 'listening');
const origin = 'https://' + domain + ':' + proxy.address().port;

async function readCode(mailbox) {
  for (let i = 0; i < 100; i++) {
    try {
      const code = await readFile(mailbox, 'utf8');
      if (/^\d{6}$/.test(code)) return code;
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
    }
    await delay(100);
  }
  throw new Error('The loopback SMTP fixture did not receive the invited account’s code.');
}

const browser = await chromium.launch({args:['--no-proxy-server', '--host-resolver-rules=MAP drop.example.test 127.0.0.1']});
try {
  for (const [name, width, height] of [['desktop', 1280, 800], ['mobile', 390, 844]]) {
    const context = await browser.newContext({ignoreHTTPSErrors: true, viewport: {width, height}});
    try {
      const page = await context.newPage();
      const uploads = [];
      page.on('response', response => {
        if (response.request().method() === 'PUT' && new URL(response.url()).pathname.startsWith('/api/drop/upload/')) {
          uploads.push(response.status());
        }
      });
      await page.goto(origin);
      await page.getByText('Beta', {exact:true}).waitFor();
      const legal = page.getByRole('link', {name:'Terms of Service', exact:true});
      assert.equal(await legal.getAttribute('href'), 'https://' + domain + '/terms');
      const email = name + '@example.test';
      if (phase === 'join') {
        await page.getByRole('button', {name:'Sign in', exact:true}).click();
        await page.getByLabel('Email', {exact:true}).fill(email);
        if (name === 'desktop') {
          await page.getByRole('button', {name:'Email me a sign-in code'}).click();
          await page.getByRole('button', {name:'Join the waitlist with this email'}).click();
          assert.equal(await page.getByLabel('Email', {exact:true}).inputValue(), email);
        }
        await page.getByRole('checkbox', {name:/Waitlist Terms/i}).check();
        await page.getByRole('button', {name:'Email me a verification code'}).click();
        const code = await readCode(path.join(directory, email + '.waitlist'));
        const submit = page.getByRole('button', {name:'Verify and join the waitlist'});
        const field = page.getByLabel('Code from the email');
        await field.fill(code === '999999' ? '999998' : '999999');
        await submit.click();
        await page.getByRole('alert').waitFor();
        await field.fill(code);
        await submit.click();
        await page.getByRole('heading', {name:/on the waitlist/i}).waitFor();
        assert.equal(uploads.length, 0);
        console.log(name + ': unapproved address verified from the sign-in flow');
        continue;
      }
      await page.locator('input[type=file]').setInputFiles(crew);
      await page.getByLabel('Email', {exact:true}).fill(email);
      await page.getByRole('button', {name:'Check access'}).click();
      await page.getByText('This email has access.', {exact:false}).waitFor();
      await page.getByRole('checkbox', {name:/Beta Terms of Service/}).check();
      await page.getByRole('button', {name:'Email me a sign-in code'}).click();
      await page.getByLabel('Code from the email').fill(await readCode(path.join(directory, email)));
      await page.getByText('This app requires platform S3.', {exact:false}).waitFor();
      await page.getByRole('button',{name:/Buy 500 credits/}).click();
      await page.getByRole('button',{name:/Buy 500 credits/}).waitFor({state:'hidden'});
      const sizePicker = page.getByLabel('App size');
      await sizePicker.waitFor();
      assert.equal(await sizePicker.inputValue(), '4');
      await sizePicker.selectOption('1');
      await page.getByText('256 MB', {exact:true}).waitFor();
      await page.getByText('0.25 vCPU', {exact:true}).waitFor();
      await page.getByRole('button', {name:'Deploy app'}).click();
      // No production node, workload role or SMTP delivery service is attached to this Drop.
      await page.getByRole('alert').filter({hasText:'no space'}).waitFor({timeout: 45000});
      assert.deepEqual(uploads, [200]);
      await page.getByRole('button', {name:'Retry deployment'}).click();
      await page.getByRole('alert').filter({hasText:'no space'}).waitFor();
      await page.getByRole('link', {name:'Apps', exact:true}).click();
      const apps = page.getByRole('list', {name:'Apps, newest first'});
      await apps.getByRole('listitem').first().waitFor();
      assert.equal(await apps.getByRole('listitem').count(), 1);
      const address = apps.getByRole('link', {name:/^https:\/\//});
      assert.equal(await address.innerText(), await address.getAttribute('href'));
      await apps.getByRole('button', {name:'Copy app URL'}).waitFor();
      await apps.locator('a[href*="#/apps/"]').click();
      await page.getByRole('heading', {name:'Resume deployment'}).waitFor();
      await page.getByRole('button', {name:'Deploy app'}).click();
      await page.getByRole('alert').filter({hasText:'no space'}).waitFor();
      assert.deepEqual(uploads, [200]);
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
      console.log(name + ': invited account signed in and uploaded released Crew, resumed after capacity rejection, retained one quota slot');
    } finally {
      await context.close();
    }
  }
} finally {
  await browser.close();
  proxy.closeAllConnections();
  proxy.close();
}
