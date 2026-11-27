import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { marked } from "marked";

// Compile only the reviewed, versioned documents from the release source.
const legalDocuments = Object.fromEntries(
  ["2026-11-26", "2026-11-26-waitlist"].flatMap((version) =>
    ["terms", "privacy"].map((name) => {
      const path = new URL(
        `../docs/legal/${version}/${name}.md`,
        import.meta.url,
      );
      return [
        `#/legal/${version}/${name}`,
        marked.parse(readFileSync(path, "utf8"), { async: false }),
      ];
    }),
  ),
);
export default defineConfig({
  define: { __DROP_LEGAL_DOCUMENTS__: JSON.stringify(legalDocuments) },
  plugins: [
    react(),
    {
      name: "drop-guest-launcher",
      resolveId(id) {
        if (id === "virtual:drop-guest") return "\0" + id;
      },
      load(id) {
        if (id !== "\0virtual:drop-guest") return;
        const dir = mkdtempSync(join(tmpdir(), "drop-guest-build-"));
        const binary = join(dir, "launcher");
        try {
          execFileSync(
            "go",
            [
              "build",
              "-buildvcs=false",
              "-trimpath",
              "-ldflags=-s -w -buildid=",
              "-o",
              binary,
              "./cmd/drop-guest",
            ],
            {
              cwd: fileURLToPath(new URL("..", import.meta.url)),
              env: {
                ...process.env,
                GOOS: "linux",
                GOARCH: "amd64",
                CGO_ENABLED: "0",
              },
            },
          );
          return (
            "export default " +
            JSON.stringify(readFileSync(binary).toString("base64"))
          );
        } finally {
          rmSync(dir, { recursive: true, force: true });
        }
      },
    },
  ],
  server: {
    host: "127.0.0.1",
    port: 5173,
    strictPort: true,
    cors: false,
    fs: { strict: true, allow: [fileURLToPath(new URL(".", import.meta.url))] },
    // The browser reaches Vite only through the Drop origin, so the
    // hot-reload WebSocket uses that port (also through a same-port forward).
    ws: {
      path: "/__ui_hmr",
      clientPort: Number(process.env.DROP_DEV_PORT || 8100),
    },
  },
  test: {
    include: ["src/**/*.test.{ts,tsx}"],
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
  },
});
