import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api, pb } from "./api";
import App from "./App";
import { dropApi, OpError, type App as DropApp, type Me } from "./dropApi";

const token = `synthetic.${btoa(JSON.stringify({ exp: 4102444800 }))}.signature`;
const user = { id: "user00000000001", collectionId: "_pb_users_auth_", collectionName: "users", email: "owner@example.test" };
const member: Me = { id: user.id, email: user.email, role: "user", admin: false, owner: false, app_quota: 3 };
const id = "app000000000001";
const app: DropApp = {
  id, name: "Hello app", slug: "hello-app", owner: user.id, owner_email: user.email, nib_app_id: `d-${id}`,
  volume_id: `d-${id}-vol`, hostname: "hello-app.apps.example.test", desired_state: "on-request", idle_timeout_ms: 300000,
  vcpu: 1, memory_mib: 512, disk_mib: 1024, http_port: 8080, executable_name: "server", args: [], env: { WELCOME_TEXT: "hi" },
  health_kind: "tcp", health_path: "", deployment_seq: 1, deployment_id: `d-${id}-dep-1`, archived: false, archived_at: "",
  created: "2026-11-19 10:00:00.000Z", updated: "2026-11-19 10:00:00.000Z",
};

beforeEach(() => {
  pb.authStore.save(token, user);
  vi.spyOn(dropApi, "me").mockResolvedValue(member);
  vi.spyOn(api, "refreshSession").mockResolvedValue();
  vi.spyOn(api, "watchApps").mockResolvedValue(() => {});
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
  fireEvent.change(screen.getByLabelText("HTTP port"), { target: { value: "9000" } });
  fireEvent.click(screen.getByRole("button", { name: "Create app" }));
  await vi.waitFor(() => expect(location.hash).toBe(`#/apps/${id}`));
  expect(create).toHaveBeenCalledWith(expect.objectContaining({ name: "Hello app", slug: "hello-app", http_port: 9000, desired_state: "on-request", health_kind: "tcp" }));
});

it("saves only the changed settings and reports a new deployment", async () => {
  history.replaceState(null, "", `/#/apps/${id}/settings`);
  vi.spyOn(dropApi, "getApp").mockResolvedValue({ app, secrets: [] });
  const set = vi.spyOn(dropApi, "setApp").mockResolvedValue({ app: { ...app, memory_mib: 1024, deployment_id: `d-${id}-dep-2` }, deployment_bumped: true });
  render(<App />);
  fireEvent.change(await screen.findByLabelText("Memory (MiB)"), { target: { value: "1024" } });
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  expect(await screen.findByText(`Saved. New deployment d-${id}-dep-2.`)).toBeInTheDocument();
  expect(set).toHaveBeenCalledWith(id, { memory_mib: 1024 });
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
