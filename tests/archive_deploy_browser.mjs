// Test driver for archive_deploy_e2e.py. Every HTTP request goes to the real
// temporary Drop server, including the public GET /deploy and private beta operations.
import assert from "node:assert/strict";
import http from "node:http";
import https from "node:https";
import { once } from "node:events";
import { readFile, writeFile, access } from "node:fs/promises";
import { createRequire } from "node:module";
import { setTimeout as delay } from "node:timers/promises";
import path from "node:path";
const { chromium } = createRequire(
  new URL("../ui/package.json", import.meta.url),
)("playwright");
const [directory, port, mode, selected, appID, assignedHost, guestProxy] =
  process.argv.slice(2);
const domain = "drop.example.test";
const proxy = https.createServer(
  {
    cert: await readFile(path.join(directory, "cert.pem")),
    key: await readFile(path.join(directory, "key.pem")),
  },
  (incoming, outgoing) => {
    const headers = { ...incoming.headers, host: domain };
    if (headers.origin === origin) headers.origin = "https://" + domain;
    const req = http.request(
      {
        hostname: "127.0.0.1",
        port,
        method: incoming.method,
        path: incoming.url,
        headers,
      },
      (resp) => {
        outgoing.writeHead(resp.statusCode, resp.headers);
        resp.pipe(outgoing);
      },
    );
    req.on("error", () => outgoing.destroy());
    outgoing.on("close", () => req.destroy());
    incoming.pipe(req);
  },
);
proxy.listen(0, "127.0.0.1");
await once(proxy, "listening");
const origin = "https://" + domain + ":" + proxy.address().port;
const flags = ["--host-resolver-rules=MAP " + domain + " 127.0.0.1"];
if (guestProxy)
  flags.push("--proxy-server=" + guestProxy, "--proxy-bypass-list=" + domain);
else flags.push("--no-proxy-server");
const browser = await chromium.launch({ args: flags });
try {
  const session = JSON.parse(
    await readFile(path.join(directory, "browser.json")),
  );
  const context = await browser.newContext({
    ignoreHTTPSErrors: true,
    storageState: {
      cookies: [],
      origins: [
        {
          origin,
          localStorage: [
            { name: "drop.reader.auth", value: JSON.stringify(session) },
          ],
        },
      ],
    },
  });
  const page = await context.newPage();
  page.setDefaultTimeout(60000);
  const runs = JSON.parse(await readFile(path.join(directory, "recipes.json")));
  for (const recipe of runs.filter((r) => !selected || r.name === selected)) {
    const ops = [];
    // Prime the fixture's isolated probe cache. The binary's normal production
    // probe defaults to a host-global socket, which this test must never access.
    await page.route("**/api/drop/op", async (route) => {
      const { op, input } = route.request().postDataJSON();
      ops.push(op);
      if (op === "beta.import_url") {
        const source = path.join(directory, recipe.source);
        if (process.env.DROP_ARCHIVE_FIXTURES) {
          const bytes = await readFile(
            path.join(process.env.DROP_ARCHIVE_FIXTURES, recipe.source),
          );
          await writeFile(source, bytes);
          await route.fulfill({
            json: {
              result: {
                sha256: recipe.sha256,
                content_base64: bytes.toString("base64"),
              },
            },
          });
        } else {
          // Read from the actual server as it responds. Chromium evicts large
          // downloads from its inspector cache before response.json() can read them.
          const response = await route
            .fetch({
              url: "https://127.0.0.1:" + proxy.address().port + "/api/drop/op",
              timeout: 120000,
            })
            .catch(() => {
              throw new Error("Fixture upstream import request failed");
            });
          assert(response.ok());
          const result = (await response.json()).result;
          assert.equal(result.sha256, recipe.sha256);
          await writeFile(source, Buffer.from(result.content_base64, "base64"));
          await route.fulfill({ response });
          await response.dispose();
        }
        return;
      }
      if (op === "beta.inspect") {
        assert.match(input.sha256, /^[a-f0-9]{64}$/);
        await writeFile(path.join(directory, "probe-" + input.sha256), "");
        const until = Date.now() + 30000;
        for (;;) {
          try {
            await access(path.join(directory, "approved-" + input.sha256));
            break;
          } catch {}
          if (Date.now() > until)
            throw new Error("Fixture probe cache timed out");
          await delay(100);
        }
      }
      if (op === "beta.probe_upload")
        throw new Error("Unexpected host-global manifest inspection");
      await route.continue();
    });
    const params = new URLSearchParams({
      name: recipe.name,
      binary: recipe.url,
      sha256: recipe.sha256,
      arg: JSON.stringify(recipe.args),
      port: String(recipe.port),
      minimal: "true",
    });
    if (mode === "setup") {
      await page.goto(origin + "/#/apps/" + appID);
      await page.getByText("Your app is live.").waitFor();
      await page.reload();
      if (recipe.name === "pocketbase") {
        const link = page.getByRole("link", {
          name: "Open PocketBase installer",
        });
        const href = new URL(await link.getAttribute("href"));
        assert.equal(href.hostname, assignedHost);
        assert.equal(href.protocol, "https:");
        assert.match(href.hash, /^#\/pbinstall\/.+/);
        const opened = context.waitForEvent("page");
        await link.click();
        const installer = await opened;
        await installer.waitForLoadState("domcontentloaded");
        assert.equal(new URL(installer.url()).hostname, assignedHost);
      } else {
        await page.getByRole("button", { name: "Copy pairing code" }).waitFor();
        const link = page.getByRole("link", { name: "Open app", exact: true });
        assert.equal(
          new URL(await link.getAttribute("href")).hostname,
          assignedHost,
        );
      }
      console.log(
        "Owner setup UI and refreshed status verified: " + recipe.name,
      );
      break;
    }
    if (mode === "update") {
      await page.goto(origin + "/#/apps/" + appID);
      await page.getByRole("button", { name: "Deploy a new release" }).click();
      await page
        .getByLabel("App file", { exact: true })
        .setInputFiles(path.join(directory, recipe.updated));
      await page.getByRole("button", { name: "Inspect release" }).click();
      await page.getByRole("button", { name: "Use this release" }).waitFor();
      const region = page.getByRole("region", { name: "Release import" });
      const executable = region.getByRole("combobox");
      if (recipe.name === "zeroclaw") {
        assert.equal(await executable.inputValue(), "");
        assert.deepEqual(
          (await executable.locator("option").allTextContents())
            .slice(1)
            .sort(),
          ["zeroclaw", "zerocode", "zerorelay"],
        );
        await executable.selectOption("zeroclaw");
      }
      await page
        .getByLabel("Launch arguments (JSON array)")
        .fill(JSON.stringify(recipe.args));
      await page
        .getByLabel("Non-secret environment settings (JSON object)")
        .fill(JSON.stringify(recipe.env));
      await page
        .getByLabel("Packaged asset paths (JSON array)")
        .fill(JSON.stringify(recipe.assets));
      await page.getByRole("button", { name: "Use this release" }).click();
      const accepted = page.waitForResponse(
        (r) =>
          r.request().method() === "POST" &&
          r.url().endsWith("/api/drop/op") &&
          r.request().postDataJSON()?.op === "beta.deploy",
      );
      await page
        .getByRole("button", { name: "Deploy app", exact: true })
        .click();
      assert.equal((await accepted).status(), 200);
      assert(
        !ops.includes("beta.prepare"),
        "update keeps the existing app and volume",
      );
      console.log(
        "Updated the original app through the reader: " + recipe.name,
      );
      break;
    }
    console.error("fixture opening", recipe.name);
    const opened = await page.goto(origin + "/deploy?" + params);
    assert(opened.ok());
    await page.getByRole("button", { name: "Inspect release" }).click();
    await page
      .getByRole("button", { name: "Use this release" })
      .waitFor()
      .catch(async (e) => {
        console.error(await page.locator("[role=alert]").allTextContents());
        throw e;
      });
    assert.equal(
      await page
        .getByRole("region", { name: "Release import" })
        .getByRole("combobox")
        .inputValue(),
      recipe.executable,
    );
    await page.getByRole("button", { name: "Use this release" }).click();
    const deploy = page.getByRole("button", {
      name: "Deploy app",
      exact: true,
    });
    await deploy.waitFor({ timeout: 180000 }).catch(async (e) => {
      console.error(
        "reader import diagnostic",
        await page.locator("[role=alert]").allTextContents(),
      );
      throw e;
    });
    const deployed = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        r.url().endsWith("/api/drop/op") &&
        r.request().postDataJSON()?.op === "beta.deploy",
    );
    await deploy.click();
    const response = await deployed;
    assert.equal(response.status(), 200, "deploy operation");
    assert.equal(
      ops.filter((x) => x === "beta.import_url").length,
      1,
      "one import",
    );
    assert(ops.includes("release.upload_link"));
    const app = new URL(page.url()).hash.match(/^#\/apps\/([a-z0-9]{15})/)[1];
    console.log(JSON.stringify({ name: recipe.name, app }));
    await page.unroute("**/api/drop/op");
  }
} finally {
  await browser.close();
  proxy.close();
}
