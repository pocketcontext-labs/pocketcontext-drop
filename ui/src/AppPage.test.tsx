import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api, pb } from "./api";
import App from "./App";
import { dropApi, OpError, releaseFileProblem, type App as DropApp, type Me, type Release } from "./dropApi";

const token = `synthetic.${btoa(JSON.stringify({ exp: 4102444800 }))}.signature`;
const user = { id: "user00000000001", collectionId: "_pb_users_auth_", collectionName: "users", email: "owner@example.test" };
const member: Me = { id: user.id, email: user.email, role: "user", admin: false, owner: false, app_quota: 3 };
const id = "app000000000001";
const app: DropApp = {
  id, name: "Hello app", slug: "hello-app", owner: user.id, owner_email: user.email, nib_app_id: `d-${id}`,
  volume_id: `d-${id}-vol`, hostname: "hello-app.apps.example.test", desired_state: "on-request", idle_timeout_ms: 300000,
  vcpu: 1, memory_mib: 512, disk_mib: 1024, http_port: 8080, executable_name: "server", args: [], env: { WELCOME_TEXT: "hi" },
  health_kind: "tcp", health_path: "", deployment_seq: 1, deployment_id: `d-${id}-dep-1`, current_release: "", disk_locked: false, archived: false, archived_at: "",
  created: "2026-11-19 10:00:00.000Z", updated: "2026-11-19 10:00:00.000Z",
};

beforeEach(() => {
  pb.authStore.save(token, user);
  vi.spyOn(dropApi, "me").mockResolvedValue(member);
  vi.spyOn(api, "refreshSession").mockResolvedValue();
  vi.spyOn(api, "watchApps").mockResolvedValue(() => {});
  vi.spyOn(dropApi, "appStatus").mockResolvedValue({
    app: id, phase: "unplaced", message: "Waiting for an admin to place the app on a node.", desired_state: "on-request",
    deployment_id: `d-${id}-dep-1`, reported_deployment_id: "", hostname: "", placed: false, restart_count: 0,
    last_exit_code: null, converged_at: "", observed_at: "", last_seen: "",
  });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  pb.authStore.clear();
  history.replaceState(null, "", "/");
});

it("lists the apps with the quota and links each to its page", async () => {
  history.replaceState(null, "", "/#/apps");
  vi.spyOn(dropApi, "listApps").mockResolvedValue({ apps: [app], quota: { used: 1, limit: 3, unlimited: false }, has_more: false });
  render(<App />);
  expect(await screen.findByRole("link", { name: "Hello app" })).toHaveAttribute("href", `#/apps/${id}`);
  expect(screen.getByText("1 of 3 apps")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "New app" })).toHaveAttribute("href", "#/apps/new");
});

it("hides New app when the quota is reached", async () => {
  vi.spyOn(dropApi, "listApps").mockResolvedValue({ apps: [app, app, app].map((a, i) => ({ ...a, id: `app00000000000${i}` })), quota: { used: 3, limit: 3, unlimited: false }, has_more: false });
  render(<App />);
  expect(await screen.findByText("Quota reached")).toBeInTheDocument();
  expect(screen.queryByRole("link", { name: "New app" })).not.toBeInTheDocument();
});

it("creates an app from #/apps/new and opens its page", async () => {
  history.replaceState(null, "", "/#/apps/new");
  const create = vi.spyOn(dropApi, "createApp").mockResolvedValue(app);
  render(<App />);
  fireEvent.change(await screen.findByLabelText("App name"), { target: { value: " Hello app " } });
  fireEvent.change(screen.getByLabelText("Slug"), { target: { value: "hello-app" } });
  const port = screen.getByLabelText("HTTP port");
  expect(port).toHaveValue(8080);
  for (const value of ["0", "65536"]) {
    fireEvent.change(port, { target: { value } });
    expect(port).toBeInvalid();
  }
  fireEvent.change(port, { target: { value: "65535" } });
  expect(port).toBeValid();
  fireEvent.change(port, { target: { value: "1" } });
  fireEvent.click(screen.getByRole("button", { name: "Create app" }));
  await vi.waitFor(() => expect(location.hash).toBe(`#/apps/${id}`));
  expect(create).toHaveBeenCalledWith(expect.objectContaining({ name: "Hello app", slug: "hello-app", http_port: 1, desired_state: "on-request", health_kind: "tcp" }));
});

it("shows the runtime phase on the Overview", async () => {
  history.replaceState(null, "", `/#/apps/${id}`);
  vi.spyOn(dropApi, "getApp").mockResolvedValue({ app: { ...app, current_release: "rel000000000001" }, secrets: [] });
  const status = vi.spyOn(dropApi, "appStatus").mockResolvedValue({
    app: id, phase: "running", message: "", desired_state: "on-request", deployment_id: `d-${id}-dep-1`,
    reported_deployment_id: `d-${id}-dep-1`, hostname: app.hostname, placed: true, restart_count: 2,
    last_exit_code: 1, converged_at: "", observed_at: "", last_seen: "",
  });
  render(<App />);
  expect(await screen.findByText("Running")).toBeInTheDocument();
  expect(screen.getByText(/2 restarts, last exit code 1/)).toBeInTheDocument();
  expect(status).toHaveBeenCalledWith(id);
});

it("saves only the changed settings and reports a new deployment", async () => {
  history.replaceState(null, "", `/#/apps/${id}/settings`);
  vi.spyOn(dropApi, "getApp").mockResolvedValue({ app, secrets: [] });
  const set = vi.spyOn(dropApi, "setApp").mockResolvedValue({ app: { ...app, memory_mib: 1024, http_port: 80, deployment_id: `d-${id}-dep-2` }, deployment_bumped: true });
  render(<App />);
  fireEvent.change(await screen.findByLabelText("Memory (MiB)"), { target: { value: "1024" } });
  fireEvent.change(screen.getByLabelText("HTTP port"), { target: { value: "80" } });
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  expect(await screen.findByText(`Saved. New deployment d-${id}-dep-2.`)).toBeInTheDocument();
  expect(set).toHaveBeenCalledWith(id, { memory_mib: 1024, http_port: 80 });
});

it("sets a secret write-only and never keeps its value", async () => {
  history.replaceState(null, "", `/#/apps/${id}/env`);
  vi.spyOn(dropApi, "getApp").mockResolvedValue({
    app,
    secrets: [{ name: "FROM_MCP", version: 2, length: 8, set_via: "mcp", updated_by: user.id, updated: "2026-11-19 10:00:00.000Z" }],
  });
  const setSecret = vi.spyOn(dropApi, "setSecret").mockResolvedValue({
    secret: { name: "API_TOKEN", version: 1, length: 9, set_via: "ui", updated_by: user.id, updated: "2026-11-19 10:00:00.000Z" },
    deployment_id: `d-${id}-dep-2`,
  });
  render(<App />);
  expect(await screen.findByText("Set through MCP")).toBeInTheDocument();
  expect(screen.getByText("WELCOME_TEXT")).toBeInTheDocument();
  const value = screen.getByLabelText("Secret value");
  expect(value).toHaveAttribute("type", "password");
  expect(value).toHaveAttribute("autocomplete", "off");
  fireEvent.change(screen.getByLabelText("Secret name"), { target: { value: "API_TOKEN" } });
  fireEvent.change(value, { target: { value: "synthetic" } });
  fireEvent.click(screen.getByRole("button", { name: "Save secret" }));
  expect(await screen.findByText(/API_TOKEN saved \(version 1\)/)).toBeInTheDocument();
  expect(setSecret).toHaveBeenCalledWith(id, "API_TOKEN", "synthetic");
  expect(screen.getByLabelText("Secret value")).toHaveValue("");
  // A reserved name is refused before sending.
  fireEvent.change(screen.getByLabelText("Secret name"), { target: { value: "HOME" } });
  fireEvent.change(screen.getByLabelText("Secret value"), { target: { value: "x" } });
  fireEvent.click(screen.getByRole("button", { name: "Save secret" }));
  expect(await screen.findByText("HOME is reserved.")).toBeInTheDocument();
  expect(setSecret).toHaveBeenCalledTimes(1);
});

it("shows Not found for an app that is not the account's", async () => {
  history.replaceState(null, "", `/#/apps/${id}`);
  vi.spyOn(dropApi, "getApp").mockRejectedValue(new OpError(404, "not_found", "App not found."));
  render(<App />);
  expect(await screen.findByRole("heading", { name: "Not found" })).toBeInTheDocument();
});

/** A synthetic release: a linux/amd64 ELF header and filler; machine changes the architecture. */
function elf(size = 256, machine = 0x3e): File {
  const bytes = new Uint8Array(size);
  bytes.set([0x7f, 0x45, 0x4c, 0x46, 2, 1, 1]);
  bytes[16] = 2;
  bytes[18] = machine;
  return new File([bytes], "server", { type: "application/octet-stream" });
}

const release: Release = {
  id: "rel000000000001", app: id, sha256: "a".repeat(64), size_bytes: 256, s3_key: `releases/${"a".repeat(64)}`, note: "v1",
  created_by: user.id, created: "2026-11-19 10:00:00.000Z", current: true,
};
const older: Release = { ...release, id: "rel000000000002", sha256: "b".repeat(64), s3_key: `releases/${"b".repeat(64)}`, note: "", current: false };

it("checks a release file locally like the server", async () => {
  expect(await releaseFileProblem(elf())).toBe("");
  expect(await releaseFileProblem(elf(256, 0xb7))).toMatch(/built for arm64/);
  expect(await releaseFileProblem(new File(["#!/bin/sh\n"], "script.sh"))).toMatch(/not an ELF executable/);
  expect(await releaseFileProblem(new File([], "empty"))).toBe("The file is empty.");
  expect(await releaseFileProblem(elf(), 100)).toMatch(/at most 0 MiB/);
});

it("uploads a release through a one-time link with progress and deploys another", async () => {
  history.replaceState(null, "", `/#/apps/${id}/releases`);
  vi.spyOn(dropApi, "getApp").mockResolvedValue({ app: { ...app, current_release: release.id }, secrets: [] });
  vi.spyOn(dropApi, "listReleases").mockResolvedValue({ releases: [release, older], current_release: release.id, has_more: false });
  const link = vi.spyOn(dropApi, "createUploadLink").mockResolvedValue({
    url: "https://drop.example.test/api/drop/upload/" + "T".repeat(32), path: "/api/drop/upload/" + "T".repeat(32), method: "PUT",
    max_bytes: 90 * 1024 * 1024, expires_at: "2026-11-19T10:15:00Z", activate: true, example: "curl -fT FILE URL",
  });
  const put = vi.spyOn(dropApi, "uploadRelease").mockImplementation(async (_path, file, onProgress) => {
    onProgress(file.size, file.size);
    return { release: { ...release, sha256: "c".repeat(64) }, created: true, deployed: true, deployment_id: `d-${id}-dep-2` };
  });
  const deploy = vi.spyOn(dropApi, "deployRelease").mockResolvedValue({
    app, release: older, deployment_bumped: true, deployment_id: `d-${id}-dep-3`,
  });
  render(<App />);
  expect(await screen.findByText("Current")).toBeInTheDocument();
  const upload = screen.getByRole("button", { name: "Upload release" });
  expect(upload).toBeDisabled();
  // A file for another architecture is refused before anything is sent.
  fireEvent.change(screen.getByLabelText("Release file"), { target: { files: [elf(256, 0xb7)] } });
  expect(await screen.findByText(/built for arm64/)).toBeInTheDocument();
  expect(upload).toBeDisabled();
  fireEvent.change(screen.getByLabelText("Release file"), { target: { files: [elf()] } });
  await vi.waitFor(() => expect(upload).toBeEnabled());
  fireEvent.change(screen.getByLabelText("Note (optional)"), { target: { value: " v2 " } });
  fireEvent.click(upload);
  expect(await screen.findByText(`Uploaded cccccccccccc. Deployed: new deployment d-${id}-dep-2.`)).toBeInTheDocument();
  expect(link).toHaveBeenCalledWith(id, { note: "v2", activate: true });
  expect(put).toHaveBeenCalledWith("/api/drop/upload/" + "T".repeat(32), expect.any(File), expect.any(Function));
  expect(screen.getByLabelText("Upload progress")).toHaveAttribute("value", "256");
  fireEvent.click(screen.getByRole("button", { name: "Deploy" }));
  expect(await screen.findByText(`Deployed bbbbbbbbbbbb: new deployment d-${id}-dep-3.`)).toBeInTheDocument();
  expect(deploy).toHaveBeenCalledWith(id, older.id);
});
