#!/usr/bin/env node
import { spawn as spawnProcess } from "node:child_process";
import { access as accessPath, readFile, stat } from "node:fs/promises";
import { createServer } from "node:net";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { setTimeout as delay } from "node:timers/promises";

const root = fileURLToPath(new URL("../", import.meta.url));
export const DEFAULT_PORT = 8100;
const usage =
  "Usage: node scripts/dev.mjs --db PATH [--binary PATH] [--vite-port 5173]";
const valued = ["--db", "--binary", "--vite-port"];

export function parseOptions(args) {
  const options = {
    binary: join(root, "bin", "drop"),
    vitePort: 5173,
  };
  for (let i = 0; i < args.length; i++) {
    const name = args[i];
    if (
      !valued.includes(name) ||
      !args[i + 1] ||
      args[i + 1].startsWith("--")
    ) {
      throw new Error(usage);
    }
    const value = args[++i];
    if (name === "--vite-port") options.vitePort = validPort(Number(value));
    else options[name.slice(2)] = resolve(value);
  }
  if (!options.db)
    throw new Error(`An explicit development database is required. ${usage}`);
  return options;
}

function validPort(value) {
  if (!Number.isInteger(value) || value < 1024 || value > 65535)
    throw new Error("Port must be 1024-65535");
  return value;
}

export async function projectOptions(options) {
  const info = await stat(options.db).catch((error) => {
    if (error.code === "ENOENT")
      throw new Error(
        "Initialize an explicit development database with drop init --db PATH first.",
      );
    throw error;
  });
  const db = info.isDirectory()
    ? join(options.db, "drop.db")
    : options.db;
  await accessPath(db);
  let config = {};
  try {
    config = JSON.parse(
      await readFile(join(dirname(db), "drop.json"), "utf8"),
    );
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
  }
  const port = validPort(
    config.port === undefined || config.port === 0 ? DEFAULT_PORT : config.port,
  );
  if (port === options.vitePort)
    throw new Error("Drop and Vite must use different ports");
  return { ...options, db, port };
}

export async function assertPortAvailable(port) {
  await new Promise((resolvePromise, reject) => {
    const server = createServer();
    server.once("error", () =>
      reject(
        new Error(
          `Port ${port} is already in use. Stop its server explicitly or choose another port.`,
        ),
      ),
    );
    server.listen(port, "127.0.0.1", () => server.close(resolvePromise));
  });
}

function starter(spawn) {
  return function start(command, args, options = {}) {
    const child = spawn(command, args, { stdio: "inherit", ...options });
    const state = { child, done: false };
    state.exited = new Promise((resolvePromise) => {
      child.once("error", (error) => {
        state.done = true;
        resolvePromise(error.message);
      });
      child.once("exit", (code, signal) => {
        state.done = true;
        resolvePromise(signal || `exit ${code}`);
      });
    });
    return state;
  };
}

async function waitReady(fetch, url, state, signal, check) {
  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    signal.throwIfAborted();
    if (state.done)
      throw new Error(
        `Process stopped before readiness: ${await state.exited}`,
      );
    try {
      const response = await fetch(url, {
        signal: AbortSignal.any([signal, AbortSignal.timeout(1000)]),
      });
      const ready = response.ok && (await check(response));
      if (!response.bodyUsed) await response.body?.cancel();
      if (ready && !state.done) return;
    } catch (error) {
      if (signal.aborted) throw error;
    }
    await delay(150, undefined, { signal });
  }
  throw new Error(`Server did not become ready within 60 seconds: ${url}`);
}

async function terminate(state, timeout = 5000) {
  if (!state || state.done) return;
  state.child.kill("SIGTERM");
  await Promise.race([state.exited, delay(timeout, undefined, { ref: false })]);
  if (!state.done) {
    state.child.kill("SIGKILL");
    await state.exited;
  }
}

const defaults = {
  spawn: spawnProcess,
  fetch: (...args) => globalThis.fetch(...args),
  access: accessPath,
  assertPortAvailable,
  log: (text) => console.log(text),
  signals: process,
};

export async function run(args, overrides = {}) {
  const deps = { ...defaults, ...overrides };
  const start = starter(deps.spawn);
  const options = await projectOptions(parseOptions(args));
  const vite = join(root, "ui", "node_modules", "vite", "bin", "vite.js");
  await deps.access(options.binary).catch(() => {
    throw new Error(
      `Build Drop once with make build; executable missing: ${options.binary}`,
    );
  });
  await deps.access(vite).catch(() => {
    throw new Error(
      "Install frontend dependencies first: (cd ui && pnpm install --frozen-lockfile)",
    );
  });
  await deps.assertPortAvailable(options.port);
  await deps.assertPortAvailable(options.vitePort);
  const controller = new AbortController();
  let finish;
  const interrupted = new Promise((resolvePromise) => {
    finish = resolvePromise;
  });
  const onSignal = () => {
    controller.abort();
    finish();
  };
  deps.signals.on("SIGINT", onSignal);
  deps.signals.on("SIGTERM", onSignal);
  let frontend;
  let backend;
  let backendReady = false;
  try {
    frontend = start(
      process.execPath,
      [vite, "--port", String(options.vitePort)],
      {
        cwd: join(root, "ui"),
        env: { ...process.env, DROP_DEV_PORT: String(options.port) },
      },
    );
    const upstream = `http://127.0.0.1:${options.vitePort}`;
    await waitReady(
      deps.fetch,
      upstream,
      frontend,
      controller.signal,
      async () => true,
    );
    backend = start(
      options.binary,
      ["serve", "--db", options.db, "--ui-dev", upstream],
      { cwd: root },
    );
    const origin = `http://127.0.0.1:${options.port}`;
    await waitReady(
      deps.fetch,
      `${origin}/api/bundle/instance`,
      backend,
      controller.signal,
      async (response) => {
        const instance = await response.json();
        const config = JSON.parse(
          await readFile(
            join(dirname(options.db), "drop.json"),
            "utf8",
          ),
        );
        return (
          instance.app === "drop" &&
          instance.instance === config.instance &&
          instance.pid === backend.child.pid &&
          instance.uiDev === upstream
        );
      },
    );
    backendReady = true;
    deps.log(
      `\nReader ready at ${origin}. Sign in there, or run drop open --db PATH in another terminal.`,
    );
    const stopped = [
      interrupted,
      frontend.exited.then((value) => `Vite stopped (${value})`),
      backend.exited.then((value) => `Drop stopped (${value})`),
    ];
    deps.log(
      "Edit ui/src for hot updates. Press Ctrl-C to stop all development processes.\n",
    );
    const result = await Promise.race(stopped);
    if (result) throw new Error(result);
  } catch (error) {
    if (!controller.signal.aborted) throw error;
  } finally {
    if (backendReady && !backend.done) {
      // The authenticated stop endpoint flushes the database snapshot.
      const stop = start(options.binary, ["stop", "--db", options.db], {
        stdio: "ignore",
      });
      await Promise.race([
        stop.exited,
        delay(35_000, undefined, { ref: false }),
      ]);
      await terminate(stop);
      await Promise.race([
        backend.exited,
        delay(5000, undefined, { ref: false }),
      ]);
    }
    await terminate(backend, 35_000);
    await terminate(frontend);
    deps.signals.off("SIGINT", onSignal);
    deps.signals.off("SIGTERM", onSignal);
  }
}

if (
  process.argv[1] &&
  pathToFileURL(resolve(process.argv[1])).href === import.meta.url
) {
  if (process.argv.slice(2).includes("--help")) console.log(usage);
  else
    run(process.argv.slice(2)).catch((error) => {
      console.error(error.message);
      process.exitCode = 1;
    });
}
