import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { PassThrough } from "node:stream";
import { test } from "node:test";
import {
  assertPortAvailable,
  parseOptions,
  projectOptions,
  run,
} from "../scripts/dev.mjs";

test("launcher requires an explicit database and valid port", () => {
  assert.throws(() => parseOptions([]), /explicit development database/);
  assert.throws(
    () => parseOptions(["--db", "--binary", "bin/drop"]),
    /Usage/,
  );
  assert.throws(
    () => parseOptions(["--db", "example.db", "--vite-port", "0"]),
    /Port/,
  );
  assert.throws(
    () => parseOptions(["--db", "example.db", "--take-over"]),
    /Usage/,
  );
});

test("launcher has no tunnel or account option", () => {
  assert.throws(() => parseOptions(["--db", "example.db", "--tunnel"]), /Usage/);
  assert.throws(() => parseOptions(["--db", "example.db", "--email", "you@example.localhost"]), /Usage/);
});

test("launcher reads only explicit project settings and refuses missing databases", async () => {
  const folder = await mkdtemp(join(tmpdir(), "drop-launcher-"));
  try {
    const db = join(folder, "drop.db");
    await assert.rejects(
      projectOptions(parseOptions(["--db", db])),
      /Initialize/,
    );
    await writeFile(db, "synthetic");
    assert.equal((await projectOptions(parseOptions(["--db", folder]))).db, db);
    assert.equal((await projectOptions(parseOptions(["--db", db]))).port, 8100);
    await writeFile(
      join(folder, "drop.json"),
      JSON.stringify({ port: 18123 }),
    );
    const options = await projectOptions(parseOptions(["--db", db]));
    assert.equal(options.port, 18123);
    await assert.rejects(
      projectOptions(parseOptions(["--db", db, "--vite-port", "18123"])),
      /different ports/,
    );
    await writeFile(
      join(folder, "drop.json"),
      JSON.stringify({ port: "18123" }),
    );
    await assert.rejects(projectOptions(parseOptions(["--db", db])), /Port/);
    await writeFile(join(folder, "drop.json"), "broken JSON");
    await assert.rejects(
      projectOptions(parseOptions(["--db", db])),
      SyntaxError,
    );
  } finally {
    await rm(folder, { recursive: true, force: true });
  }
});

test("launcher refuses an occupied port without touching its listener", async () => {
  const server = createServer();
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address();
  try {
    await assert.rejects(assertPortAvailable(port), /already in use/);
    assert.equal(server.listening, true);
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
  await assertPortAvailable(port);
});

/** A project folder and fake child processes; nothing real is started. */
async function harness() {
  const folder = await mkdtemp(join(tmpdir(), "drop-launcher-run-"));
  const db = join(folder, "drop.db");
  await writeFile(db, "synthetic");
  await writeFile(join(folder, "drop.json"), JSON.stringify({ port: 18123, instance: "instance-1" }));
  const events = [];
  const children = [];
  let pid = 1000;
  let backend;
  function fakeChild(argv) {
    const child = new EventEmitter();
    child.pid = ++pid;
    child.argv = argv;
    child.stdout = new PassThrough();
    child.killed = [];
    child.exit = (code, signal = null) => {
      if (child.ended) return;
      child.ended = true;
      child.stdout.end();
      setImmediate(() => child.emit("exit", code, signal));
    };
    child.kill = (signal) => {
      child.killed.push(signal);
      events.push(`kill ${argv[0]} ${signal}`);
      child.exit(null, signal);
    };
    children.push(child);
    return child;
  }
  const spawn = (command, argv, options) => {
    const isVite = command === process.execPath;
    const child = fakeChild(isVite ? ["vite", ...argv.slice(1)] : argv);
    child.options = options;
    const name = isVite ? "vite" : argv[0];
    events.push(`spawn ${name}`);
    setImmediate(() => {
      if (name === "serve") backend = child;
      if (name === "stop") {
        child.exit(0);
        backend?.exit(0);
      }
    });
    return child;
  };
  const fetch = async (url) => {
    if (String(url).endsWith("/api/bundle/instance")) {
      if (!backend) throw new Error("not listening");
      return new Response(JSON.stringify({ app: "drop", instance: "instance-1", pid: backend.pid, uiDev: "http://127.0.0.1:5173" }));
    }
    return new Response("vite");
  };
  const output = [];
  const signals = new EventEmitter();
  const deps = {
    spawn, fetch, signals,
    access: async () => {},
    assertPortAvailable: async () => {},
    log: (text) => output.push(text),
  };
  return {
    db, events, children, output, signals, deps,
    text: () => output.join("\n"),
    cleanup: () => rm(folder, { recursive: true, force: true }),
  };
}
async function waitFor(check, what) {
  const deadline = Date.now() + 5000;
  while (!check()) {
    if (Date.now() > deadline) throw new Error(`Timed out: ${what}`);
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
}

test("the launcher runs Vite and the server, and Ctrl-C stops both", async () => {
  const h = await harness();
  try {
    const running = run(["--db", h.db], h.deps);
    await waitFor(() => h.text().includes("Edit ui/src"), "ready");
    h.signals.emit("SIGTERM");
    await running;
    assert.deepEqual(h.events.filter((event) => event.startsWith("spawn")), ["spawn vite", "spawn serve", "spawn stop"]);
    const vite = h.children.find((child) => child.argv[0] === "vite");
    assert.equal(vite.options.env.DROP_DEV_PORT, "18123");
    assert(h.children.every((child) => child.ended), "no child process survives Ctrl-C");
    assert.equal(h.signals.listenerCount("SIGTERM"), 0);
  } finally {
    await h.cleanup();
  }
});
