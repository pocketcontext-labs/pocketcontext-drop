import assert from "node:assert/strict";

// The signed-in admin fixture is shared with the waitlist browser checks.
export async function checkAccountRows(page, viewport, record) {
  const users = [
    {
      id: record.id,
      email: "owner.with.a.long.email.address@administration.example.test",
      role: "admin",
      owner: true,
      signed_in: true,
      app_quota: 3,
    },
    {
      id: "member000000001",
      email:
        "member.with.an.even.longer.email.address@engineering.department.example.test",
      role: "user",
      owner: false,
      signed_in: false,
      app_quota: 2,
    },
  ];
  const changes = [];
  await page.route("**/api/drop/op", async (route) => {
    const { op, input } = route.request().postDataJSON();
    const reply = (result) => route.fulfill({ json: { result } });
    if (op === "user.list") return reply({ users });
    if (op === "invite.list") return reply({ invites: [] });
    if (op !== "user.set") return route.fallback();
    const { user, ...update } = input;
    assert.equal(user, users[1].id);
    changes.push(update);
    Object.assign(users[1], update);
    return reply({ user: users[1] });
  });
  await page.getByRole("link", { name: "Users", exact: true }).click();
  const accounts = page.getByRole("list", { name: "Accounts, newest first" });
  const owner = accounts
    .getByRole("listitem")
    .filter({ hasText: users[0].email });
  const member = accounts
    .getByRole("listitem")
    .filter({ hasText: users[1].email });
  await owner.waitFor();
  assert.ok(await owner.getByLabel("Role").isDisabled());
  assert.ok(await owner.getByRole("button", { name: "Suspend" }).isDisabled());
  assert.ok(await owner.getByLabel("Quota").isEnabled());
  for (const selector of ["select", "input", "button"]) {
    const first = await owner.locator(selector).boundingBox();
    const second = await member.locator(selector).boundingBox();
    assert.ok(Math.abs(first.x - second.x) < 1, selector + " columns align");
  }
  for (const row of [owner, member]) {
    assert.ok(await row.evaluate((li) => li.scrollWidth <= li.clientWidth));
    if (viewport.width < 640) {
      const email = await row.locator("p").boundingBox();
      const role = await row.getByLabel("Role").boundingBox();
      assert.ok(email.y + email.height <= role.y);
    }
  }
  await member.getByLabel("Role").selectOption("admin");
  await member.getByLabel("Quota").fill("7");
  await member.getByLabel("Quota").press("Tab");
  page.once("dialog", (dialog) => dialog.accept());
  await member.getByRole("button", { name: "Suspend" }).click();
  page.once("dialog", (dialog) => dialog.accept());
  await member.getByRole("button", { name: "Restore access" }).click();
  await member.getByRole("button", { name: "Suspend" }).waitFor();
  assert.deepEqual(changes, [
    { role: "admin" },
    { app_quota: 7 },
    { suspended: true },
    { suspended: false },
  ]);
}
