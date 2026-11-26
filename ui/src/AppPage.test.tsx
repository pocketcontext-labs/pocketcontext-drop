import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api, pb } from "./api";
import App from "./App";
import { dropApi, OpError, releaseFileProblem, type App as DropApp, type Me, type Release } from "./dropApi";

const token = `synthetic.${btoa(JSON.stringify({ exp: 4102444800 }))}.signature`;
const user = { id: "user00000000001", collectionId: "_pb_users_auth_", collectionName: "users", email: "owner@example.test" };
const member: Me = { id: user.id, email: user.email, role: "user", hosted: true, admin: false, owner: false, app_quota: 3 };
const id = "app000000000001";
const app: DropApp = {
  id, kind: "ordinary", sign_in_email_enabled: false, name: "Hello app", slug: "hello-app", owner: user.id, owner_email: user.email, nib_app_id: `d-${id}`,
  volume_id: `d-${id}-vol`, hostname: "hello-app.apps.example.test", desired_state: "on-request", idle_timeout_ms: 300000,
  vcpu: 1, memory_mib: 512, disk_mib: 1024, http_port: 8080, hook_port: 0, executable_name: "server", args: [], env: { WELCOME_TEXT: "hi" },
  health_kind: "tcp", health_path: "", deployment_seq: 1, deployment_id: `d-${id}-dep-1`, current_release: "", disk_locked: false, archived: false, archived_at: "",
  created: "2026-11-19 10:00:00.000Z", updated: "2026-11-19 10:00:00.000Z",
};

beforeEach(() => {
  vi.spyOn(api, "loginMode").mockResolvedValue({ mode: "email" });
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
  expect(screen.getByRole("link", { name: "Deploy an app" })).toHaveAttribute("href", "#/deploy");
});

it("hides New app when the quota is reached", async () => {
  vi.spyOn(dropApi, "listApps").mockResolvedValue({ apps: [app, app, app].map((a, i) => ({ ...a, id: `app00000000000${i}` })), quota: { used: 3, limit: 3, unlimited: false }, has_more: false });
  render(<App />);
  expect(await screen.findByText("Quota reached")).toBeInTheDocument();
  expect(screen.queryByRole("link", { name: "Deploy an app" })).not.toBeInTheDocument();
});

it("lets an admin create an app from #/apps/new and opens its page", async () => {
  vi.spyOn(dropApi, "me").mockResolvedValue({ ...member, admin: true, role: "admin" });
  history.replaceState(null, "", "/#/apps/new");
  const create = vi.spyOn(dropApi, "createApp").mockResolvedValue(app);
  render(<App />);
  fireEvent.change(await screen.findByLabelText("App name"), { target: { value: " Hello app " } });
  fireEvent.change(screen.getByLabelText("Slug"), { target: { value: "hello-app" } });
  fireEvent.change(await screen.findByLabelText("App type"), { target: { value: "crew" } });
  expect(screen.getByRole("spinbutton", {name: /Hooks port/})).toHaveValue(8081);
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
  expect(create).toHaveBeenCalledWith(expect.objectContaining({ name: "Hello app", slug: "hello-app", kind: "crew", http_port: 1, hook_port: 8081, executable_name: "crew", desired_state: "on-request", health_kind: "tcp" }));
});

it("only offers ordinary apps on a local server", async () => {
  vi.spyOn(dropApi, "me").mockResolvedValue({ ...member, hosted: false });
  history.replaceState(null, "", "/#/apps/new");
  render(<App />);
  expect(await screen.findByLabelText("App name")).toBeInTheDocument();
  expect(screen.queryByLabelText("App type")).not.toBeInTheDocument();
});

it("saves the Crew delivery toggle", async () => {
  history.replaceState(null, "", `/#/apps/${id}/settings`);
  const crew = { ...app, kind: "crew" as const, crew_owner_email: "owner@example.test", sign_in_email_enabled: true };
  vi.spyOn(dropApi, "getApp").mockResolvedValue({ app: crew, secrets: [] });
  const set = vi.spyOn(dropApi, "setApp").mockResolvedValue({ app: { ...crew, sign_in_email_enabled: false }, deployment_bumped: true });
  const label = "Deliver sign-in codes to owner@example.test";
  render(<App />);
  expect(await screen.findByLabelText(label)).toBeChecked();
  fireEvent.click(screen.getByLabelText(label));
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  await vi.waitFor(() => expect(set).toHaveBeenCalledWith(id, { sign_in_email_enabled: false }));
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
  fireEvent.change(screen.getByRole("spinbutton", {name:/Hooks port/}),{target:{value:"8000"}});
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  expect(await screen.findByText(`Saved. New deployment d-${id}-dep-2.`)).toBeInTheDocument();
  expect(set).toHaveBeenCalledWith(id, { memory_mib: 1024, http_port: 80, hook_port: 8000 });
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

const counts = { class_a: 1200, class_b: 30000, deletes: 40, bytes_in: 1048576, bytes_out: 3 * 2 ** 30, status_4xx: 2, status_5xx: 0, denied: 1 };
const usage = {
  app: id, month: "2026-11", metered: true,
  hourly: [{ hour: "2026-11-25T14:00:00Z", ...counts }],
  month_to_date: counts,
  stored: { day: "2026-11-25", current_bytes: 2 ** 20, noncurrent_bytes: 0, objects: 3, versions: 3 },
  limits: [
    { metric: "requests_a" as const, label: "Class A and DELETE requests", value: 1240, soft: 100000, hard: 1000000, level: "ok" as const },
    { metric: "requests_b" as const, label: "Class B requests", value: 30000, soft: 1000000, hard: 10000000, level: "ok" as const },
    { metric: "bytes_out" as const, label: "Bytes sent", value: 3 * 2 ** 30, soft: 2 * 2 ** 30, hard: 20 * 2 ** 30, level: "soft" as const },
    { metric: "stored" as const, label: "Stored bytes", value: 2 ** 20, soft: 2 ** 30, hard: 10 * 2 ** 30, level: "ok" as const },
  ],
  limits_override: {},
  estimate: { class_a_usd: 0.006, class_b_usd: 0.012, egress_usd: 0.27, storage_usd: 0, total_usd: 0.288 },
  estimate_note: "Estimated S3 cost. An estimate, not a bill.",
  exceeded: [{ metric: "bytes_out", level: "soft", stopped: false, message: "Bytes sent reached 3.00 GB in 2026-11, past the soft limit of 2.00 GB.", created: "2026-11-25 14:30:00.000Z" }],
};

it("shows an app's usage against its limits, with an estimate, and no limit form for users", async () => {
  history.replaceState(null, "", `/#/apps/${id}/usage`);
  vi.spyOn(dropApi, "getApp").mockResolvedValue({ app, secrets: [] });
  const get = vi.spyOn(dropApi, "getUsage").mockResolvedValue(usage);
  render(<App />);
  expect(await screen.findByRole("meter", { name: "Bytes sent" })).toBeInTheDocument();
  expect(get).toHaveBeenCalledWith(id);
  expect(screen.getByText("Past the soft limit")).toBeInTheDocument();
  expect(screen.getByText(/past the soft limit of 2.00 GB/)).toBeInTheDocument();
  expect(screen.getByText("$0.2880")).toBeInTheDocument();
  expect(screen.getByText("An estimate, not a bill.", { exact: false })).toBeInTheDocument();
  expect(screen.getByRole("table")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Save limits" })).not.toBeInTheDocument();
});

it("lets an admin override an app's soft limits", async () => {
  history.replaceState(null, "", `/#/apps/${id}/usage`);
  vi.spyOn(dropApi, "me").mockResolvedValue({ ...member, role: "admin", admin: true });
  vi.spyOn(dropApi, "getApp").mockResolvedValue({ app, secrets: [] });
  vi.spyOn(dropApi, "getUsage").mockResolvedValue(usage);
  const set = vi.spyOn(dropApi, "setUsageLimits").mockResolvedValue({});
  render(<App />);
  fireEvent.change(await screen.findByLabelText("Bytes sent (GB)"), { target: { value: "5" } });
  fireEvent.click(screen.getByRole("button", { name: "Save limits" }));
  expect(await screen.findByText("Limits saved.")).toBeInTheDocument();
  expect(set).toHaveBeenCalledWith(id, { requests_a: 100000, requests_b: 1000000, bytes_out: 5 * 2 ** 30, stored_bytes: 2 ** 30 });
});
