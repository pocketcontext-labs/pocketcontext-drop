import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";
import { readFileSync } from "node:fs";
import { marked } from "marked";

// Compile only the reviewed, versioned documents from the release source.
const legalDocuments = Object.fromEntries(
  ["terms", "privacy"].map((name) => {
    const path = new URL(
      `../docs/legal/2026-11-26/${name}.md`,
      import.meta.url,
    );
    return [
      `#/legal/2026-11-26/${name}`,
      marked.parse(readFileSync(path, "utf8"), { async: false }),
    ];
  }),
);
export default defineConfig({
  define: { __DROP_LEGAL_DOCUMENTS__: JSON.stringify(legalDocuments) },
  plugins: [react()],
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
