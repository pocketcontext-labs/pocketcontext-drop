import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import AppCatalog from "./AppCatalog";
import { readDeployLink } from "./deployRecipes";
import { loginReturnHash, rememberLoginReturn, takeLoginReturn, viewFromHash } from "./routes";

afterEach(() => {
  cleanup();
  localStorage.clear();
});

it("hands each catalog release to the generic deploy route with its pinned settings", () => {
  render(<AppCatalog />);
  const target = (name: string) => "#" + screen.getByRole("link", { name: "Deploy " + name }).getAttribute("href");
  const pocketbase = target("PocketBase");
  expect(viewFromHash(pocketbase)).toBe("deploy");
  expect(readDeployLink(pocketbase).recipe).toMatchObject({
    name: "PocketBase", executable: "pocketbase", port: 8090, minimal: true,
    releaseUrl: "https://github.com/pocketbase/pocketbase/releases/download/v0.40.6/pocketbase_0.40.6_linux_amd64.zip",
    sha256: "0e1f609158ed945f809f8a02ae012aa0427129c66ebb63c1aee76f2c0309373f",
    args: ["serve", "--http=0.0.0.0:8090", "--dir=./data/pb_data", "--publicDir=./data/pb_public"],
  });
  expect(readDeployLink(target("ZeroClaw")).recipe).toMatchObject({
    executable: "zeroclaw", assets: ["web/dist"], port: 42617,
    sha256: "8b9bc6735946158a8effbaa3c130efd8d4c1c983504cf20f08bb745af3544fd0",
    args: ["daemon", "--host", "0.0.0.0", "--port", "42617"],
    env: { ZEROCLAW_CONFIG_DIR: "/app/data/zeroclaw", ZEROCLAW_DATA_DIR: "/app/data/zeroclaw" },
  });
  const saved = loginReturnHash(pocketbase);
  expect(saved).toBe(pocketbase);
  rememberLoginReturn(saved!);
  expect(takeLoginReturn()).toBe(pocketbase);
});

it("accepts external release links with literal argv and no catalog privileges", () => {
  const args = ["serve", "words with spaces", '\"quoted\"', "", "a+b&c"];
  const params = new URLSearchParams({ name: "zeroclaw", binary: "https://builds.example.test/release.zip", arg: JSON.stringify(args) });
  expect(readDeployLink("#/deploy?" + params).recipe).toMatchObject({
    name: "zeroclaw", args, executable: "", env: {}, assets: [], minimal: false,
  });
});

it.each([
  "binary=http://example.test/app",
  "binary=https://user:secret@example.test/app",
  "binary=https://example.test/app&port=0",
  "binary=https://example.test/app&port=65536",
  "binary=https://example.test/app&sha256=short",
  "binary=https://example.test/app&arg=serve",
  "binary=https://example.test/app&arg=%5B3%5D",
  "binary=https://example.test/app&arg=%5B%22%5Cu0000%22%5D",
  "binary=https://example.test/app&minimal=on",
  "binary=https://example.test/app&binary=https://example.test/app",
])("rejects malformed link %s before authentication or import", query => {
  const hash = "#/deploy?name=sample&" + query;
  expect(readDeployLink(hash).error).toBeTruthy();
  expect(loginReturnHash(hash)).toBeNull();
});
