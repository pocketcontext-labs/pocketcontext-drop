import { pb } from "./api";

// Drop's operations (POST /api/drop/op, internal/dropapi). Every write goes
// through this one route; the server checks the account, its role and the
// input. OPS lists every operation name in the server's registry, in its
// order: internal/dropapi's TestReaderKnowsEveryOperation compares them.
export const OPS = [
  "me",
  "user.list",
  "user.set",
  "invite.create",
  "invite.list",
  "invite.revoke",
  "app.list",
  "app.get",
  "app.create",
  "app.set",
  "app.start",
  "app.stop",
  "app.archive",
  "app.restore",
  "secret.list",
  "secret.set",
  "secret.delete",
] as const;
export type Op = (typeof OPS)[number];

export type Role = "admin" | "user";
export interface Me {
  id: string;
  email: string;
  role: Role;
  admin: boolean;
  owner: boolean;
  app_quota: number;
}
export interface User {
  id: string;
  email: string;
  role: Role;
  app_quota: number;
  suspended: boolean;
  owner: boolean;
  signed_in: boolean;
  invited_by: string;
  created: string;
}
export type InviteState = "open" | "used" | "revoked" | "expired";
export interface Invite {
  id: string;
  email: string;
  role: Role;
  app_quota: number;
  state: InviteState;
  invited_by: string;
  expires_at: string;
  used_at: string;
  used_by: string;
  revoked_at: string;
  created: string;
}

export type DesiredState = "running" | "on-request" | "stopped";
export type HealthKind = "tcp" | "http";
/** An app (internal/dropapi App). Env holds plain values only. */
export interface App {
  id: string;
  name: string;
  slug: string;
  owner: string;
  owner_email: string;
  nib_app_id: string;
  volume_id: string;
  hostname: string;
  desired_state: DesiredState;
  idle_timeout_ms: number;
  vcpu: number;
  memory_mib: number;
  disk_mib: number;
  http_port: number;
  executable_name: string;
  args: string[];
  env: Record<string, string>;
  health_kind: HealthKind;
  health_path: string;
  deployment_seq: number;
  deployment_id: string;
  archived: boolean;
  archived_at: string;
  created: string;
  updated: string;
}
export interface Quota {
  used: number;
  limit: number;
  unlimited: boolean;
}
/** A secret's name and metadata; the value is never returned. */
export interface Secret {
  name: string;
  version: number;
  length: number;
  set_via: "ui" | "mcp";
  updated_by: string;
  updated: string;
}
/** The settings app.create and app.set take (app.set: env as {set, unset}). */
export interface AppSettings {
  desired_state?: DesiredState;
  idle_timeout_ms?: number;
  vcpu?: number;
  memory_mib?: number;
  disk_mib?: number;
  http_port?: number;
  executable_name?: string;
  args?: string[];
  health_kind?: HealthKind;
  health_path?: string;
}

/** The error body of a failed operation: a stable code and a message. */
export class OpError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

/** Call one operation and return its result. */
export async function op<T>(name: Op, input: object = {}): Promise<T> {
  try {
    const body = await pb.send<{ result: T }>("/api/drop/op", {
      method: "POST",
      body: { op: name, input },
    });
    return body.result;
  } catch (error) {
    const e = error as {
      status?: number;
      response?: { code?: unknown; message?: unknown };
    };
    const message =
      typeof e.response?.message === "string" && e.response.message
        ? e.response.message
        : "Something went wrong. Please retry.";
    throw new OpError(
      e.status ?? 0,
      typeof e.response?.code === "string" ? e.response.code : "",
      message,
    );
  }
}

/** The signed-in account. */
async function me(): Promise<Me> {
  return op<Me>("me");
}
async function listUsers(): Promise<User[]> {
  return (await op<{ users: User[] }>("user.list", { limit: 500 })).users;
}
async function setUser(
  user: string,
  change: { role?: Role; app_quota?: number; suspended?: boolean },
): Promise<User> {
  return (await op<{ user: User }>("user.set", { user, ...change })).user;
}
async function createInvite(input: {
  email: string;
  role: Role;
  app_quota: number;
  expires_days?: number;
}): Promise<{ invite: Invite; sign_in_url: string }> {
  return op("invite.create", input);
}
async function listInvites(): Promise<Invite[]> {
  return (await op<{ invites: Invite[] }>("invite.list", { limit: 200 }))
    .invites;
}
async function revokeInvite(invite: string): Promise<Invite> {
  return (await op<{ invite: Invite }>("invite.revoke", { invite })).invite;
}

async function listApps(
  archived = false,
): Promise<{ apps: App[]; quota: Quota; has_more: boolean }> {
  return op("app.list", { archived, limit: 500 });
}
async function getApp(app: string): Promise<{ app: App; secrets: Secret[] }> {
  return op("app.get", { app });
}
async function createApp(
  input: { name: string; slug: string; env?: Record<string, string> } & AppSettings,
): Promise<App> {
  return (await op<{ app: App }>("app.create", input)).app;
}
async function setApp(
  app: string,
  change: AppSettings & {
    name?: string;
    env?: { set?: Record<string, string>; unset?: string[] };
  },
): Promise<{ app: App; deployment_bumped: boolean }> {
  return op("app.set", { app, ...change });
}
type AppAction = "app.start" | "app.stop" | "app.archive" | "app.restore";
async function appAction(action: AppAction, app: string): Promise<App> {
  return (await op<{ app: App }>(action, { app })).app;
}
async function setSecret(
  app: string,
  name: string,
  value: string,
): Promise<{ secret: Secret; deployment_id: string }> {
  return op("secret.set", { app, name, value });
}
async function deleteSecret(
  app: string,
  name: string,
): Promise<{ deleted: string; deployment_id: string }> {
  return op("secret.delete", { app, name });
}

export const dropApi = {
  me,
  listUsers,
  setUser,
  createInvite,
  listInvites,
  revokeInvite,
  listApps,
  getApp,
  createApp,
  setApp,
  appAction,
  setSecret,
  deleteSecret,
};
