// Run by TestTenantReader with fresh accounts and signed test credentials on stdin.
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { json } from 'node:stream/consumers';

const { chromium } = createRequire(new URL('../ui/package.json', import.meta.url))('playwright');
const origin = process.argv[2];
const tenants = await json(process.stdin);
const browser = await chromium.launch();
try {
  for (const tenant of tenants) {
    const other = tenants.find(t => t.id !== tenant.id);
    const session = { token: tenant.token, record: { id: tenant.id, collectionName: 'users' } };
    const context = await browser.newContext({
      storageState: { cookies: [], origins: [{ origin, localStorage: [{ name: 'drop.reader.auth', value: JSON.stringify(session) }] }] },
    });
    try {
      const page = await context.newPage();
      await page.goto(origin + '/#/apps/' + tenant.app);
      await page.getByRole('heading', { name: tenant.name, exact: true }).waitFor();
      const response = page.waitForResponse(r =>
        r.url() === origin + '/api/drop/op' && r.request().method() === 'POST' &&
        r.request().postDataJSON()?.op === 'app.get' && r.request().postDataJSON()?.input.app === other.app);
      await page.goto(origin + '/#/apps/' + other.app);
      assert.equal((await response).status(), 404);
      await page.getByRole('heading', { name: 'Not found' }).waitFor();
      assert.equal(await page.getByText(other.name, { exact: true }).count(), 0);
      for (const route of ['users', 'nodes']) {
        await page.goto(origin + '/#/admin/' + route);
        await page.getByRole('heading', { name: 'Not found' }).waitFor();
      }
      assert.equal(await page.getByRole('link', { name: 'Users', exact: true }).count(), 0);
    } finally {
      await context.close();
    }
  }
} finally {
  await browser.close();
}
