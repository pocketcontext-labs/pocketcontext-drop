import assert from "node:assert/strict";
import {mkdir} from "node:fs/promises";
import path from "node:path";

export async function checkAdminNodeLayout(browser, origin) {
  const record = { id: "admin0000000001", collectionName: "users", email: "owner@example.test" };
  const token = "fixture." + Buffer.from(JSON.stringify({ exp: 4102444800 })).toString("base64url") + ".fixture";
  const flag = process.argv.indexOf("--screenshots");
  const screenshots = flag >= 0 ? process.argv[flag + 1] : null;
  if (screenshots) await mkdir(screenshots, {recursive: true});
  for (const width of [1600, 375]) {
    const context = await browser.newContext({
      viewport: { width, height: 900 },
      storageState: { cookies: [], origins: [{ origin, localStorage: [
        { name: "drop.reader.auth", value: JSON.stringify({ record, token }) },
      ] }] },
    });
    try {
      const page = await context.newPage();
      const first = { id:"node00000000001", name:"node-01", online:true, state:"active", apps:1, desired_seq:3,
        last_heartbeat:"2026-11-26 12:35:00.000Z", allocatable:{vcpuCount:2, memoryMib:4096}, agent_version:"0.6.0" };
      const second = {...first, id:"node00000000002", name:"stockholm-compute-02", apps:29, desired_seq:103456,
        allocatable:{vcpuCount:96, memoryMib:1058304}, agent_version:"build-" + "e".repeat(120)};
      const nodes = [first, second];
      const writes = [];
      await page.route("**/api/**", async route => {
        const request = route.request();
        const pathname = new URL(request.url()).pathname;
        const json = value => route.fulfill({json:value});
        if (pathname === "/api/bundle/auth-mode") return json({mode:"email"});
        if (pathname.endsWith("/auth-refresh")) return json({record, token});
        assert.equal(pathname, "/api/drop/op");
        const {op,input} = request.postDataJSON();
        let result;
        switch (op) {
          case "private.shared": result={apps:[]}; break;
          case "me": result={...record, role:"admin", admin:true, hosted:true}; break;
          case "waitlist.list": result={entries:[],has_more:false}; break;
          case "user.list": result={users:[]}; break;
          case "invite.list": result={invites:[]}; break;
          case "node.list": result={nodes}; break;
          case "node.create": {
            writes.push(op);
            assert.equal(input.name,"node-03");
            assert.equal(input.host_id,"host-03");
            const node = {...first,id:"node00000000003",name:input.name};
            nodes.push(node);
            result={node,identity:node.name,password:"synthetic-initial-password"};
            break;
          }
          case "node.set_state":
            writes.push(op);
            assert.equal(input.node,first.id);
            first.state=input.state;
            result={node:first}; break;
          case "node.rotate_secret":
            writes.push(op);
            assert.equal(input.node,first.id);
            result={node:first,identity:first.name,password:"synthetic-rotated-password"}; break;
          case "placement.set":
            writes.push(op);
            assert.deepEqual(input,{app:"example-app",node:second.name});
            result={moved:false}; break;
          case "placement.clear":
            writes.push(op);
            assert.equal(input.app,"example-app");
            result={cleared_node:second.name}; break;
          default: throw new Error("Unexpected operation " + op);
        }
        return json({result});
      });

      async function checkPanel() {
        const panel = page.locator("main.content > :is(.panel, .nodes-page)");
        const box=await panel.boundingBox();
        assert.ok(Math.abs(box.x - ((await page.locator("html").boundingBox()).width - box.x - box.width)) < 2, "admin panel gutters are balanced");
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no page overflow");
      }
      for (const [route, title] of [["waitlist","Waitlist"],["users","Users"],["nodes","Nodes"]]) {
        await page.goto(origin + "/#/admin/" + route);
        await page.getByRole("heading", {name:title,level:2,exact:true}).waitFor();
        await checkPanel();
      }
      const list=page.getByRole("list",{name:"Nodes by name"});
      await list.getByText(second.agent_version,{exact:false}).waitFor();
      const states=list.getByLabel("State");
      const rotations=list.getByRole("button",{name:"Rotate password"});
      if (width >= 1000) {
        for (const controls of [states,rotations]) {
          const boxes=await Promise.all([controls.nth(0).boundingBox(),controls.nth(1).boundingBox()]);
          assert.ok(Math.abs(boxes[0].x-boxes[1].x)<1, "desktop action columns align across unequal node metadata");
        }
      }
      const hint=page.locator("#node-host-hint");
      await hint.waitFor();
      assert.ok((await hint.innerText()).includes("reported.json"));
      assert.ok(await hint.evaluate(el => el.scrollWidth <= el.clientWidth), "host ID guidance fits");
      assert.equal(await page.getByLabel("Host id",{exact:true}).getAttribute("aria-describedby"),"node-host-hint");
      if (screenshots) {
        await page.evaluate(() => document.fonts.ready);
        await page.screenshot({path:path.join(screenshots,`drop-admin-nodes-${width}.png`),fullPage:true});
      }
      await page.getByLabel("Name",{exact:true}).fill("node-03");
      await page.getByLabel("Host id",{exact:true}).fill("host-03");
      await page.getByRole("button",{name:"Add node"}).click();
      await list.getByText("node-03",{exact:true}).waitFor();
      await states.first().selectOption("draining");
      await page.waitForFunction(() => document.querySelector('option:checked[value="draining"]') !== null);
      page.once("dialog",dialog=>dialog.accept());
      await rotations.first().click();
      await page.getByText("synthetic-rotated-password",{exact:true}).waitFor();
      await page.getByLabel("App (id or slug)").fill("example-app");
      await page.getByRole("region", {name:"Place an app"}).getByRole("combobox").selectOption(second.name);
      await page.getByRole("button",{name:"Place",exact:true}).click();
      await page.getByText("example-app is placed on " + second.name + ".").waitFor();
      await page.getByRole("button",{name:"Clear placement"}).click();
      await page.getByText("example-app is no longer placed.").waitFor();
      assert.deepEqual(writes, ["node.create","node.set_state","node.rotate_secret","placement.set","placement.clear"]);
      await checkPanel();
      console.log("Admin Nodes layout and controls passed at " + width + "px");
    } finally { await context.close(); }
  }
}
