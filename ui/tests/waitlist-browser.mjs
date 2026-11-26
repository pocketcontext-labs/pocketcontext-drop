import assert from "node:assert/strict";
import { checkAccountRows } from "./account-rows.mjs";

export async function checkWaitlist(browser, origin, viewport) {
  const redacted = "anonymous@example.test";
  const email = "alex@example.test";
  const entry = {
    id: "verified123",
    email,
    verified_at: "2026-11-26T10:00:00Z",
    state: "waiting",
    mail_state: "",
    account_id: "",
    suspended: false,
  };
  const publicContext = await browser.newContext({ viewport });
  try {
    const page = await publicContext.newPage();
    const publicWrites = [];
    await page.route("**/api/**", async (route) => {
      const request = route.request();
      const path = new URL(request.url()).pathname;
      if (path === "/api/bundle/auth-mode")
        return route.fulfill({ json: { mode: "email" } });
      assert.ok(
        !request.headers().authorization,
        "public waitlist request uses no account credential",
      );
      const input = request.postDataJSON();
      publicWrites.push(path);
      if (path.endsWith("/request")) {
        assert.deepEqual(input, {
          email,
          terms_version: "2026-11-26-waitlist",
        });
        return route.fulfill({ status: 202, json: { ok: true } });
      }
      if (path.endsWith("/verify")) {
        assert.equal(input.email, email);
        return route.fulfill({
          status: input.code === "123456" ? 200 : 400,
          json: { ok: input.code === "123456" },
        });
      }
      if (path.endsWith("/leave")) {
        assert.equal(input.token, "synthetic-leave-token");
        return route.fulfill({ json: { ok: true } });
      }
      throw new Error("Unexpected public endpoint " + path);
    });
    await page.goto(origin);
    await page.getByRole("link", { name: "Join waitlist" }).click();
    await page.getByRole("heading", { name: "Join the waitlist" }).waitFor();
    const signIn = page.getByRole("link", { name: "Sign in to Drop" });
    await signIn.waitFor();
    const terms = page.getByRole("link", { name: "Waitlist Terms" });
    const privacy = page.getByRole("link", { name: "Waitlist Privacy Notice" });
    assert.equal(
      await terms.getAttribute("href"),
      "#/legal/2026-11-26-waitlist/terms",
    );
    assert.equal(
      await privacy.getAttribute("href"),
      "#/legal/2026-11-26-waitlist/privacy",
    );
    await page.getByLabel("Email", { exact: true }).fill(email);
    assert.equal(await page.getByRole("checkbox").isChecked(), false);
    assert.equal(
      await page
        .getByRole("button", { name: "Send verification code" })
        .isDisabled(),
      true,
    );
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: "Send verification code" }).click();
    await signIn.click();
    await page.getByRole("heading", { name: "Log in to Drop" }).waitFor();
    await page.getByRole("button", { name: "Email me a sign-in code" }).waitFor();
    await page.goBack();
    await page.getByLabel("Email", { exact: true }).fill(email);
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: "Send verification code" }).click();
    await page.getByLabel("Verification code").fill("101010");
    await page.getByRole("button", { name: "Verify and join the waitlist" }).click();
    await page.getByRole("alert").filter({ hasText: "expired" }).waitFor();
    await page.getByRole("button", { name: "Resend code" }).click();
    await page.getByRole("status").filter({ hasText: "latest code" }).waitFor();
    await page.getByLabel("Verification code").fill("123456");
    await page.getByRole("button", { name: "Verify and join the waitlist" }).click();
    await page
      .getByRole("heading", { name: "You’re on the waitlist" })
      .waitFor();
    await page.goto(origin + "/#/waitlist/leave?token=synthetic-leave-token");
    await page.getByRole("button", { name: "Confirm and leave" }).waitFor();
    assert.ok(!new URL(page.url()).hash.includes("token"));
    assert.ok(!publicWrites.includes("/api/drop/waitlist/leave"));
    await page.getByRole("button", { name: "Confirm and leave" }).click();
    await page
      .getByRole("heading", { name: "You left the waitlist" })
      .waitFor();
  } finally {
    await publicContext.close();
  }

  const record = {
    id: "admin0000000001",
    collectionName: "users",
    email: "owner@example.test",
  };
  const token =
    "fixture." +
    Buffer.from(JSON.stringify({ exp: 4102444800 })).toString("base64url") +
    ".fixture";
  const context = await browser.newContext({
    viewport,
    storageState: {
      cookies: [],
      origins: [
        {
          origin,
          localStorage: [
            {
              name: "drop.reader.auth",
              value: JSON.stringify({ record, token }),
            },
          ],
        },
      ],
    },
  });
  try {
    const page = await context.newPage();
    const writes = [];
    const entries = [
      { ...entry },
      {
        ...entry,
        id: "joined123",
        email: "joined@example.test",
        state: "used",
        account_id: "joined000000001",
        mail_state: "sent",
      },
    ];
    let rejectResend = true;
    await page.route("**/api/**", async (route) => {
      const request = route.request();
      const path = new URL(request.url()).pathname;
      if (path === "/api/bundle/auth-mode")
        return route.fulfill({ json: { mode: "email" } });
      if (path.endsWith("/auth-refresh"))
        return route.fulfill({ json: { record, token } });
      assert.equal(path, "/api/drop/op");
      const { op, input } = request.postDataJSON();
      const reply = (result) => route.fulfill({ json: { result } });
      if (op === "me")
        return reply({ ...record, hosted: true, admin: true, role: "admin" });
      if (op === "waitlist.list") {
        assert.equal(input.limit, 50);
        return reply({
          entries: entries.filter((e) => e.email.includes(input.search)),
          has_more: false,
        });
      }
      writes.push(op);
      if (op === "user.set") {
        assert.equal(input.user, entries[1].account_id);
        entries[1].suspended = input.suspended;
        return reply({ user: { id: entries[1].account_id } });
      }
      assert.equal(input.id, entry.id);
      if (op === "waitlist.invite") {
        entries[0].state = "open";
        entries[0].mail_state = "failed";
      } else if (op === "waitlist.resend") {
        if (rejectResend) {
          rejectResend = false;
          return route.fulfill({
            status: 429,
            json: { code: "rate_limited", message: "Try again in a minute." },
          });
        }
        entries[0].mail_state = "sent";
      } else if (op === "waitlist.revoke") {
        entries[0].state = "revoked";
        entries[0].mail_state = "";
      } else throw new Error("Unexpected admin operation " + op);
      return reply({ ok: true });
    });
    await page.goto(origin);
    await page
      .getByRole("heading", { name: "Waitlist", exact: true })
      .waitFor();
    assert.equal(
      await page.getByRole("link", { name: /^(Apps|Deploy|Connect)$/ }).count(),
      0,
    );
    const first = page.getByRole("listitem").filter({ hasText: email });
    const active = page
      .getByRole("listitem")
      .filter({ hasText: "joined@example.test" });
    await first.getByText("Waiting", { exact: true }).waitFor();
    await first
      .getByRole("button", { name: "Invite and send welcome email" })
      .click();
    await first.getByText("Invited", { exact: true }).waitFor();
    await first.getByText(/Welcome email failed/).waitFor();
    await first.getByRole("button", { name: "Resend welcome email" }).click();
    await page.getByRole("alert").filter({ hasText: "Try again" }).waitFor();
    await first.getByRole("button", { name: "Resend welcome email" }).click();
    await first.getByText(/Welcome email sent/).waitFor();
    await first.getByRole("button", { name: "Revoke invitation" }).click();
    await first.getByRole("button", { name: "Confirm revoke" }).click();
    await first.getByText(/Invitation revoked/).waitFor();
    await active.getByRole("button", { name: "Suspend access" }).click();
    await active.getByText(/apps will keep serving/).waitFor();
    await active.getByRole("button", { name: "Cancel" }).click();
    assert.ok(!writes.includes("user.set"));
    await active.getByRole("button", { name: "Suspend access" }).click();
    await active.getByRole("button", { name: "Confirm suspend" }).click();
    await active.getByText("Suspended", { exact: true }).waitFor();
    await active.getByRole("button", { name: "Restore access" }).click();
    await active.getByRole("button", { name: "Confirm restore" }).click();
    await active.getByText("Active", { exact: true }).waitFor();
    await page.getByLabel("Search by email").fill(redacted);
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await page.getByText("No verified addresses match this search.").waitFor();
    await page.getByRole("button", { name: "Clear" }).click();
    await active.getByText("Active", { exact: true }).waitFor();
    assert.ok(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    );
    await checkAccountRows(page, viewport, record);
    await page.goto(origin + "/#/deploy");
    await page.getByRole("heading", { name: "Not found" }).waitFor();
    assert.equal(
      await page.getByRole("button", { name: "Deploy app" }).count(),
      0,
    );
  } finally {
    await context.close();
  }
}
