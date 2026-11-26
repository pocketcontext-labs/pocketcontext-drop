// Hosted reader flow using synthetic server responses. Run with pnpm test:beta-browser.
import assert from "node:assert/strict";
import { checkAdminNodeLayout } from "./admin-nodes-browser.mjs";
import { checkWaitlist } from "./waitlist-browser.mjs";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { build, preview } from "vite";
import { mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

const ui = fileURLToPath(new URL("../", import.meta.url));
const outDir = await mkdtemp(path.join(os.tmpdir(), "drop-beta-reader-"));
await build({
  root: ui,
  build: { outDir },
  logLevel: "silent",
});
const server = await preview({
  root: ui,
  build: { outDir },
  preview: { host: "127.0.0.1", port: 0 },
});
const origin = "http://127.0.0.1:" + server.httpServer.address().port;
const bytes = Buffer.alloc(64);
Buffer.from([0x7f, 69, 76, 70, 2, 1, 1]).copy(bytes);
bytes.writeUInt16LE(2, 16);
bytes.writeUInt16LE(62, 18);
const digest = createHash("sha256").update(bytes).digest("hex");
const genericBytes = Buffer.from(bytes);
genericBytes[61] = 2;
const genericDigest = createHash("sha256").update(genericBytes).digest("hex");
const binary = {
  name: "crew",
  mimeType: "application/octet-stream",
  buffer: bytes,
};
const app = {
  id: "app000000000001",
  beta_managed: true,
  name: "PocketContext Crew",
  hostname: "crew.apps.example.test",
  current_release: "rel000000000001",
};
const record = {
  id: "user00000000001",
  collectionName: "users",
  email: "owner@example.test",
};
const auth = {
  token:
    "fixture." +
    Buffer.from(JSON.stringify({ exp: 4102444800 })).toString("base64url") +
    ".fixture",
  record,
};
let browser;
try {
  browser = await chromium.launch();
  await checkAdminNodeLayout(browser, origin);
  for (const viewport of [
    { width: 1200, height: 800 },
    { width: 375, height: 812 },
  ]) {
    await checkWaitlist(browser, origin, viewport);
    const context = await browser.newContext({ viewport, permissions: ["clipboard-read", "clipboard-write"] });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    let allowed = false;
    let inspectedGeneric = false;
    let funded = false;
    const waitlistRequests = [];
    let firstDeployment = true;
    let deployed = false;
    let runtimeFailure = false;
    let deploymentID = "d-1";
    let oldFailure = false;
    let httpsReady = false;
    const operations = [];
    await page.route("**/api/**", async (route) => {
      const request = route.request();
      const path = new URL(request.url()).pathname;
      const send = (json, status = 200) => route.fulfill({ json, status });
      if (path === "/api/bundle/auth-mode") return route.fulfill({
        json: { mode: "email" },
        headers: {
          "X-Drop-Terms-Version": "test-1",
          "X-Drop-Terms-URL": "https://terms.example.test/beta",
          "X-Drop-Privacy-URL": "https://privacy.example.test/beta",
        },
      });
      if (path === "/api/drop/waitlist/request") {
        if (request.postDataJSON().check_access) return send({ admitted: request.postDataJSON().email === record.email });
        assert.equal(request.postDataJSON().terms_version, "2026-11-26-waitlist");
        waitlistRequests.push(request.postDataJSON().email);
        return send({ ok: true }, 202);
      }
      if (path === "/api/drop/waitlist/verify") {
        const { email, code } = request.postDataJSON();
        return email === "uninvited@example.test" && code === "654321"
          ? send({ ok: true })
          : send({ code: "invalid_input", data: { field: "code" } }, 400);
      }
      if (path.endsWith("/request-otp")) {
        allowed = request.postDataJSON().email === record.email;
        return send({ otpId: "testotp" });
      }
      if (path.endsWith("/auth-with-otp-code")) {
        if (allowed && request.headers()["x-drop-accept-terms"] !== "test-1")
          return send({ code: "terms_required", message: "Terms required" }, 428);
        return allowed
          ? send(auth)
          : send({ message: "Invalid sign-in code" }, 400);
      }
      if (path.endsWith("/auth-refresh")) return send(auth);
      if (path === "/api/realtime") return route.abort();
      if (path === "/api/drop/upload/inspection") {
        assert.equal(request.method(), "PUT");
        assert.deepEqual(request.postDataBuffer(), genericBytes);
        inspectedGeneric = true;
        return send({inspected:true});
      }
      if (path.startsWith("/api/drop/upload/")) {
        assert.equal(request.method(), "PUT");
        assert.equal(
          createHash("sha256").update(request.postDataBuffer()).digest("hex"),
          digest,
        );
        return send({
          release: { id: app.current_release },
          created: true,
          deployed: false,
        });
      }
      assert.equal(path, "/api/drop/op");
      assert.ok(allowed && request.headers().authorization);
      const { op, input, idempotency_key } = request.postDataJSON();
      operations.push(op);
      switch (op) {
        case "me":
          return send({
            result: { ...record, role: "user", admin: false, hosted: true },
          });
        case "credits.get":
          return send({result:{account:record.id, state:{balance:funded ? 500000000 : 0,paid:funded,trial_seconds:0},
            settings:{pack:500,euro_micros_per_credit:10000},entries:[],apps:[]}});
        case "credits.buy":
          assert.ok(idempotency_key);
          funded = true;
          return send({result:{}});
        case "beta.probe_upload":
          assert.equal(input.sha256, genericDigest);
          return send({result:{path:"/api/drop/upload/inspection"}});
        case "beta.inspect":
          if (input.sha256 === genericDigest)
            return inspectedGeneric ? send({ result: {generic: true, name:"Generic Linux app",version:"",multipliers:[1,2],suggested_multiplier:1} }) : send({code:"probe_pending"},409);
          return input.sha256 === digest
            ? send({
                result: {
                  name: app.name,
                  needs_storage_credits: !funded,
                  version: "fixture",
                  multipliers: [1, 4, 8],
                  suggested_multiplier: 4,
                },
              })
            : send({ code: "unsupported_binary" }, 409);
        case "beta.prepare":
          assert.equal(input.sha256, digest);
          assert.equal(input.multiplier, 8);
          assert.ok(idempotency_key);
          return send({ result: { app } });
        case "release.upload_link":
          assert.equal(input.activate, false);
          return send({ result: { path: "/api/drop/upload/fixture" } });
        case "beta.deploy":
          if (firstDeployment) {
            firstDeployment = false;
            return send({ code: "beta_no_capacity" }, 409);
          }
          deployed = true;
          if (runtimeFailure) {
            deploymentID = "d-2";
            oldFailure = true;
          }
          runtimeFailure = false;
          return send({ result: { deployment_id: deploymentID } });
        case "app.get":
          return send({
            result: {
              app: {
                ...app,
                owner: record.id,
                memory_mib: 2048,
                beta_sha256: digest,
                current_release: deployed ? app.current_release : "",
              },
              secrets: [],
            },
          });
        case "release.list":
          return send({
            result: { releases: [{ id: app.current_release, sha256: digest }] },
          });
        case "status":
          assert.equal(input.verify_url, true);
          if (oldFailure) {
            oldFailure = false;
            return send({ result: {
              phase: "failed", deployment_id: deploymentID, reported_deployment_id: "d-1",
              hostname: "crew.apps.example.test", url_ready: false,
            } });
          }

          return send({
            result: {
              phase: runtimeFailure ? "failed" : "running",
              deployment_id: deploymentID,
              reported_deployment_id: deploymentID,
              hostname: "crew.apps.example.test",
              url_ready: httpsReady,
            },
          });
        default:
          throw new Error("Unexpected operation " + op);
      }
    });
    for (const [document, heading] of [
      ["terms", "Drop Beta Terms of Service"],
      ["privacy", "Drop Beta Privacy Policy"],
    ]) {
      await page.goto(origin + "/#/legal/2026-11-26/" + document);
      await page.getByRole("heading", { name: heading, exact: true }).waitFor();
      assert.equal(
        await page
          .getByRole("link", { name: "alberto.miorin@pocketcontext.com" })
          .getAttribute("href"),
        "mailto:alberto.miorin@pocketcontext.com",
      );
      assert.equal(
        await page
          .locator("body")
          .evaluate((body) => body.scrollWidth > window.innerWidth),
        false,
      );
    }
    await page.goto(origin);
    await page.getByText("Beta", { exact: true }).waitFor();
    await page.getByRole("button", { name: "Choose an app file" }).focus();
    assert.equal(
      await page
        .getByRole("button", { name: "Choose an app file" })
        .evaluate((node) => node === document.activeElement),
      true,
    );
    assert.equal(
      await page
        .getByRole("link", { name: "Terms of Service" })
        .getAttribute("href"),
      "https://terms.example.test/beta",
    );
    assert.equal(
      await page
        .getByRole("link", { name: "Privacy Policy" })
        .getAttribute("href"),
      "https://privacy.example.test/beta",
    );
    await page
      .getByLabel("App file", { exact: true })
      .setInputFiles({ ...binary, name: "crew.zip" });
    await page
      .getByRole("alert")
      .filter({ hasText: "Extract the archive" })
      .waitFor();
    await page.locator(".beta-picker").evaluate((node) => {
      const dt = new DataTransfer();
      dt.items.add(new File(["one"], "one"));
      dt.items.add(new File(["two"], "two"));
      node.dispatchEvent(
        new DragEvent("drop", { bubbles: true, dataTransfer: dt }),
      );
    });
    await page.getByRole("alert").filter({ hasText: "Choose one app file at a time" }).waitFor();
    const modified = Buffer.from(bytes);
    modified[60] = 1;
    await page
      .getByLabel("App file", { exact: true })
      .setInputFiles({ ...binary, buffer: modified });
    await page
      .getByLabel("Email", { exact: true })
      .fill("uninvited@example.test");
    await page.getByRole("button", { name: "Check access" }).click();
    await page.getByRole("heading", { name: "Private beta, join the waitlist?" }).waitFor();
    await page.getByRole("button", { name: "No, use another email" }).click();
    assert.deepEqual(waitlistRequests, []);
    await page.getByRole("button", { name: "Check access" }).click();
    await page.getByRole("button", { name: "Join the waitlist", exact: true }).click();
    const consent = page.getByRole("checkbox", { name: /Waitlist Terms/ });
    await consent.check();
    await page.getByRole("button", { name: /verification code/i }).click();
    await page.getByLabel("Verification code").fill("654321");
    await page.getByRole("button", { name: "Verify and join the waitlist" }).click();
    await page.getByRole("heading", { name: "You’re on the waitlist" }).waitFor();
    assert.deepEqual(waitlistRequests, ["uninvited@example.test"]);
    await page.getByRole("button", { name: "Use a different email" }).click();
    await page.getByLabel("Email", { exact: true }).fill(record.email);
    await page.getByRole("button", { name: "Check access" }).click();
    await page.getByRole("checkbox", { name: /Beta Terms of Service/ }).check();
    await page.getByRole("button", { name: "Email me a sign-in code" }).click();
    await page.getByLabel("Code from the email").fill("123456");
    await page
      .getByRole("alert")
      .filter({ hasText: "not supported" })
      .waitFor();
    await page.getByRole("button", { name: "Choose another app file" }).click();
    await page.getByLabel("App file", { exact: true }).setInputFiles({...binary,buffer:genericBytes});
    await page.getByText(/no guarantee it will start/).waitFor();
    await page.getByLabel("HTTP port").fill("65536");
    assert.ok(await page.getByRole("button",{name:"Deploy app"}).isDisabled());
    await page.getByLabel("HTTP port").fill("4000");
    assert.ok(await page.getByRole("button",{name:"Deploy app"}).isEnabled());
    await page.getByRole("button", {name:"Choose another app file"}).click();
    await page.getByLabel("App file", { exact: true }).setInputFiles(binary);
    await page.getByRole("heading", {name: "PocketContext Crew fixture"}).waitFor();
    assert.ok(await page.getByRole("button",{name:"Deploy app"}).isDisabled());
    await page.getByRole("button",{name:/Buy 500 credits/}).click();
    await page.getByRole("button",{name:/Buy 500 credits/}).waitFor({state:"hidden"});
    await page.getByRole("heading", {name: "PocketContext Crew fixture"}).waitFor();
    await page.getByLabel("App size").waitFor();
    assert.equal(await page.getByLabel("App size").inputValue(), "4");
    await page.getByLabel("App size").selectOption("1");
    await page.getByText("0.25 vCPU", { exact: true }).waitFor();
    await page.getByLabel("App size").selectOption("8");
    assert.deepEqual(await page.locator("select option").allTextContents(), [
      "1×",
      "4× (suggested)",
      "8×",
    ]);
    await page.getByText("2 vCPU", { exact: true }).waitFor();
    await page.getByText("2048 MB", { exact: true }).waitFor();
    await page.getByText("16 GB", { exact: true }).waitFor();
    await page.getByRole("button", { name: "Deploy app" }).click();
    await page.getByRole("alert").filter({ hasText: "no space" }).waitFor();
    if (viewport.width === 375) {
      // Reopen the prepared app after the tab loses all local upload state.
      await page.goto(origin + "/#/apps/" + app.id);
      await page.reload();
      await page.getByRole("heading", { name: "Resume deployment" }).waitFor();
      await page.getByRole("button", { name: "Deploy app" }).click();
    } else {
      await page.getByRole("button", { name: "Retry deployment" }).click();
    }
    await page.getByText("Checking the app address").waitFor();
    assert.equal(await page.getByRole("link", { name: "Open app" }).count(), 0);
    const address = "https://crew.apps.example.test";
    assert.equal(await page.getByRole("link", {name: address, exact: true}).getAttribute("href"), address);
    await page.getByRole("button", {name: "Copy app URL"}).click();
    await page.getByRole("button", {name: "Copied"}).waitFor();
    assert.equal(await page.evaluate(() => navigator.clipboard.readText()), address);
    httpsReady = true;
    const link = page.getByRole("link", { name: "Open app" });
    await link.waitFor();
    assert.equal(
      await link.getAttribute("href"),
      "https://crew.apps.example.test",
    );
    assert.equal(operations.filter((op) => op === "beta.prepare").length, 1);
    assert.equal(
      operations.filter((op) => op === "release.upload_link").length,
      1,
    );
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
      true,
    );
    runtimeFailure = true;
    await page.reload();
    await page.getByText("Your app could not start.").waitFor();
    await page.getByRole("button", { name: "Retry deployment" }).click();
    await page.getByRole("link", { name: "Open app" }).waitFor();
    assert.deepEqual(errors, []);
    await context.close();
    console.log("Beta browser flow passed at " + viewport.width + "px");
  }
} finally {
  await browser?.close();
  await server.close();
  await rm(outDir, { recursive: true, force: true });
}
