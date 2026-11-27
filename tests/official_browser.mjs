// Invoked by TestOfficialBrowser with a disposable database and signed test sessions.
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { json } from "node:stream/consumers";
const { chromium } = createRequire(
  new URL("../ui/package.json", import.meta.url),
)("playwright");
const origin = process.argv[2];
const fixture = await json(process.stdin);
const browser = await chromium.launch();
async function session(role, mobile = false) {
  const auth = {
    token: fixture[role],
    record: { id: fixture[role + "Id"], collectionName: "users" },
  };
  const context = await browser.newContext({
    viewport: mobile
      ? { width: 390, height: 844 }
      : { width: 1440, height: 960 },
    isMobile: mobile,
    hasTouch: mobile,
    storageState: {
      cookies: [],
      origins: [
        {
          origin,
          localStorage: [
            { name: "drop.reader.auth", value: JSON.stringify(auth) },
          ],
        },
      ],
    },
  });
  const page = await context.newPage();
  page.setDefaultTimeout(10000);
  return page;
}
async function drag(page, mobile, start, end, y) {
  if (mobile) {
    const cdp = await page.context().newCDPSession(page);
    for (const [type, x] of [
      ["touchStart", start],
      ["touchMove", end],
      ["touchEnd", end],
    ]) {
      await cdp.send("Input.dispatchTouchEvent", {
        type,
        touchPoints: type === "touchEnd" ? [] : [{ x, y }],
      });
    }
    await cdp.detach();
  } else {
    await page.mouse.move(start, y);
    await page.mouse.down();
    await page.mouse.move(end, y, { steps: 8 });
    await page.mouse.up();
  }
}
try {
  const admin = await session("admin");
  await admin.goto(origin + "/#/admin/releases");
  await admin.getByRole("heading", { name: "Releases" }).waitFor();
  for (const product of ["crew", "atlas"]) {
    await admin.getByRole("combobox").first().selectOption(product);
    await admin.getByLabel("Version", { exact: true }).fill("1.0.0");
    await admin
      .getByLabel("Linux executable")
      .setInputFiles(fixture.files[product]);
    await admin.getByRole("button", { name: "Upload and check" }).click();
    const row = admin.getByRole("listitem").filter({
      has: admin.getByRole("heading", {
        name: new RegExp(product + " 1.0.0", "i"),
      }),
    });
    const publish = row.getByRole("button", { name: "Publish release" });
    await publish.waitFor();
    assert.equal(await publish.isDisabled(), true);
    await row
      .getByLabel("Data rehearsal receipt (JSON)")
      .setInputFiles(fixture.files[product] + ".json");
    await publish.click();
    await row.getByText(/Published/).waitFor();
  }
  await admin
    .getByRole("button", { name: "Enable official app catalog" })
    .click();
  await admin.getByText("The official app catalog is active.").waitFor();
  await admin.getByRole("link", { name: "Users", exact: true }).click();
  await admin
    .getByRole("combobox", { name: /^Status/ })
    .selectOption("waiting");
  await admin.getByRole("button", { name: "Next" }).click();
  await admin.getByLabel("Search users").fill("waiting-4999");
  const account = admin.getByRole("button", {
    name: "waiting-4999@example.test",
  });
  await account.click();
  await admin
    .getByRole("button", { name: "Approve and send invitation" })
    .click();
  await account.waitFor({ state: "hidden" });
  await admin
    .getByRole("combobox", { name: /^Status/ })
    .selectOption("invited");
  await account.waitFor();
  await admin.getByRole("button", { name: "Clear filters" }).click();
  await admin.getByRole("combobox", { name: /^Role/ }).selectOption("user");
  await admin.getByLabel("Has apps").selectOption("yes");
  await admin.getByLabel("Has credits").selectOption("no");
  await admin.getByRole("button", { name: "browser@example.test" }).waitFor();
  await admin.setViewportSize({ width: 390, height: 844 });
  const fits = await admin.evaluate(
    () => document.documentElement.scrollWidth <= innerWidth,
  );
  assert.equal(fits, true, "Users table must scroll inside its own container");
  for (const [mobile, product, size] of [
    [false, "Crew", 2],
    [true, "Atlas", 8],
  ]) {
    const page = await session("customer", mobile);
    await page.goto(origin + "/#/apps/" + fixture.legacy);
    await page
      .getByRole("heading", { name: "Retained workspace", exact: true })
      .waitFor();
    await page.goto(origin + "/#/deploy?name=obsolete");
    await page.getByRole("button", { name: "Choose " + product }).click();
    const resources = page.getByRole("combobox");
    assert.equal(await resources.inputValue(), "4");
    assert.equal(await resources.getByRole("option").count(), 8);
    await resources.selectOption(String(size));
    await page.getByLabel("App name").fill("Hosted " + product);
    const creation = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/drop/op") &&
        r.request().postDataJSON()?.op === "workspace.create",
    );
    await page.getByRole("button", { name: "Create app" }).click();
    const result = await (await creation).json();
    assert.equal(
      result.result?.app?.memory_mib,
      256 * size,
      JSON.stringify(result),
    );
    await page.getByRole("button", { name: "Delete app permanently" }).click();
    const slider = page.getByRole("slider");
    await page.locator("[role=slider][aria-disabled=false]").waitFor();
    await slider.scrollIntoViewIfNeeded();
    const box = await slider.boundingBox(),
      handle = await slider.locator("span").boundingBox();
    const x = handle.x + handle.width / 2,
      y = handle.y + handle.height / 2,
      end = box.x + box.width - handle.width / 2 + 2;
    await drag(page, mobile, x, (x + end) / 2, y);
    assert.equal(await slider.getAttribute("aria-valuenow"), "0");
    await drag(page, mobile, x, end, y);
    await page
      .getByRole("heading", { name: "Your apps" })
      .waitFor();
    const denied = await page.request.post(origin + "/api/drop/op", {
      headers: { Authorization: fixture.customer },
      data: { op: "app.create", input: { name: "Blocked", slug: "blocked" } },
    });
    assert.equal(denied.status(), 403);
    await page.context().close();
  }
} finally {
  await browser.close();
}
