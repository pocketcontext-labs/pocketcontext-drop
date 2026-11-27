import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api, pb } from "./api";
import App from "./App";
import { dropApi, type DropNode, type Me } from "./dropApi";

const token = `synthetic.${btoa(JSON.stringify({ exp: 4102444800 }))}.signature`;
const user = { id: "user00000000001", collectionId: "_pb_users_auth_", collectionName: "users", email: "owner@example.test" };
const member: Me = { id: user.id, email: user.email, role: "user", admin: false, owner: false, app_quota: 3 };
const node: DropNode = {
  id: "node00000000001", name: "walter-aws", host_id: "host-0001", ec2_instance_id: "", private_ip: "", state: "active",
  desired_seq: 3, online: true, last_heartbeat: "2026-11-20T10:00:00Z", last_report_at: "", host_state: "ready",
  agent_version: "0.1.0", accepted_revision: "drop-3-000000000000", capacity: {},
  allocatable: { vcpuCount: 6, memoryMib: 12288 }, versions: {}, apps: 1, created: "2026-11-19 10:00:00.000Z",
};

beforeEach(() => {
  vi.spyOn(api, "loginMode").mockResolvedValue({ mode: "email" });
  history.replaceState(null, "", "/#/admin/nodes");
  pb.authStore.save(token, user);
  vi.spyOn(api, "refreshSession").mockResolvedValue();
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  pb.authStore.clear();
  history.replaceState(null, "", "/");
});

it("shows everyone but admins Not found at #/admin/nodes", async () => {
  vi.spyOn(dropApi, "me").mockResolvedValue(member);
  render(<App />);
  expect(await screen.findByRole("heading", { name: "Not found" })).toBeInTheDocument();
  expect(screen.queryByRole("link", { name: "Nodes" })).not.toBeInTheDocument();
});

it("lists nodes, adds one showing its password once, and places an app", async () => {
  vi.spyOn(dropApi, "me").mockResolvedValue({ ...member, role: "admin", admin: true, owner: true });
  vi.spyOn(dropApi, "listNodes").mockResolvedValue([node]);
  const create = vi.spyOn(dropApi, "createNode").mockResolvedValue({
    node: { ...node, id: "node00000000002", name: "second", apps: 0 }, password: "p".repeat(40), identity: "second", collection: "nodes",
  });
  const place = vi.spyOn(dropApi, "placeApp").mockResolvedValue({ moved: false });
  render(<App />);
  expect(await screen.findByRole("heading", { name: "Nodes", level: 2 })).toBeInTheDocument();
  expect(await screen.findByText("Online")).toBeInTheDocument();
  expect(screen.getByText("1 app · 6 vCPU · 12,288 MiB allocatable")).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: "second" } });
  fireEvent.change(screen.getByLabelText("Host id"), { target: { value: "host-0002" } });
  fireEvent.click(screen.getByRole("button", { name: "Add node" }));
  expect(await screen.findByText("p".repeat(40))).toBeInTheDocument();
  expect(create).toHaveBeenCalledWith({ name: "second", host_id: "host-0002" });
  fireEvent.change(screen.getByLabelText("App (id or slug)"), { target: { value: "hello-app" } });
  fireEvent.change(screen.getByLabelText("Node"), { target: { value: "walter-aws" } });
  fireEvent.click(screen.getByRole("button", { name: "Place" }));
  expect(await screen.findByText("hello-app is placed on walter-aws.")).toBeInTheDocument();
  expect(place).toHaveBeenCalledWith("hello-app", "walter-aws");
});
