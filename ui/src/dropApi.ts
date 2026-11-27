import { pb } from "./api";

// Drop's operations (POST /api/drop/op, internal/dropapi). Every write goes
// through this one route; the server checks the account, its role and the
// input. OPS lists every operation name in the server's registry, in its
// order: internal/dropapi's TestReaderKnowsEveryOperation compares them.
export const OPS = [
  "private.grant",
  "private.revoke",
  "private.access",
  "private.shared",
  "catalog.list",
  "catalog.upload",
  "catalog.rehearse",
  "catalog.archive",
  "catalog.submissions",
  "catalog.accept",
  "developer.submit",
  "catalog.publish",
  "catalog.activate",
  "workspace.create",
  "workspace.resize",
  "workspace.upgrade",
  "user.directory",

  "credits.accounts",
  "credits.get",
  "developer.build_upload",
  "developer.build_import",
  "developer.build_create",
  "developer.create",
  "developer.upload_link",
  "developer.deploy",
  "credits.buy",
  "credits.adjust",
  "credits.configure",
  "me",
  "event.list",
  "user.list",
  "user.set",
  "waitlist.list",
  "waitlist.invite",
  "waitlist.resend",
  "waitlist.revoke",
  "invite.create",
  "invite.list",
  "invite.revoke",
  "app.list",
  "app.get",
  "app.create",
  "app.set",
  "app.start",
  "app.stop",
  "app.deletions",
  "app.delete",
  "app.archive",
  "app.restore",
  "secret.list",
  "secret.set",
  "secret.delete",
  "release.list",
  "release.upload_link",
  "release.deploy",
  "status",
  "node.list",
  "node.create",
  "node.rotate_secret",
  "node.set_state",
  "placement.set",
  "placement.clear",
  "usage.get",
  "usage.set_limits",
  "usage.collect",
  "beta.approve",
  "beta.revoke",
  "beta.claim",
  "beta.complete",
  "beta.probe_upload",
  "beta.inspect",
  "beta.import_url",
  "beta.prepare",
  "beta.deploy",
] as const;
export type Op = (typeof OPS)[number];

export type Role = "admin" | "developer" | "user";
export interface Me {
  app_domain?: string;
  official_catalog?: boolean;
  release_selection?: boolean;
  trial_remaining_seconds?: number;
  hosted?: boolean;
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

export type AppKind = "ordinary" | "crew";
export type DesiredState = "running" | "on-request" | "stopped";
export type HealthKind = "tcp" | "http";
/** An app (internal/dropapi App). Env holds plain values only. */
export interface App {
  developer_private?: boolean;
  minimum_multiplier?:number;
  installed_version?:string;
  official_product?: string;
  kind: AppKind;
  beta_managed?: boolean;
  beta_sha256?: string;
  generic_beta?: boolean;
  crew_owner_email?: string;
  sign_in_email_enabled: boolean;
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
  disk_resize_supported?: boolean;
  disk_resize?: { multiplier: number; outcome: "pending" | "refused" };
  http_port: number;
  hook_port: number;
  executable_name: string;
  args: string[];
  env: Record<string, string>;
  health_kind: HealthKind;
  health_path: string;
  deployment_seq: number;
  deployment_id: string;
  /** The id of the release the app runs; empty before its first deploy. */
  current_release: string;
  /** True once the app has been placed on a node: the disk size is fixed. */
  disk_locked: boolean;
  archived: boolean;
  archived_at: string;
  created: string;
  updated: string;
  /** A Crew app's Litestream sync interval, such as "10s". */
  replica_sync_interval?: string;
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
  hook_port?: number;
  executable_name?: string;
  args?: string[];
  health_kind?: HealthKind;
  health_path?: string;
}

/** An uploaded executable of an app (internal/dropapi Release). */
export interface Release {
  archive_sha256?: string;
  executable_sha256?: string;
  executable_path?: string;
  id: string;
  app: string;
  sha256: string;
  size_bytes: number;
  s3_key: string;
  note: string;
  created_by: string;
  created: string;
  current: boolean;
}
/** A one-time upload link: one PUT of the file to `path` before `expires_at`. */
export interface UploadLink {
  url: string;
  path: string;
  method: "PUT";
  max_bytes: number;
  expires_at: string;
  activate: boolean;
  example: string;
}
/** The answer of a successful upload. */
export interface UploadResult {
  release: Release;
  created: boolean;
  deployed: boolean;
  deployment_id: string;
}
export type NodeState = "active" | "draining" | "disabled";
/** A node (host) as admins see it (internal/dropapi Node). */
export interface DropNode {
  memory_history?: { at: number; used_mib: number; running_vms: number }[];
  id: string;
  name: string;
  host_id: string;
  ec2_instance_id: string;
  private_ip: string;
  state: NodeState;
  desired_seq: number;
  online: boolean;
  last_heartbeat: string;
  last_report_at: string;
  host_state: string;
  agent_version: string;
  accepted_revision: string;
  capacity: Record<string, unknown>;
  allocatable: Record<string, unknown>;
  versions: Record<string, unknown>;
  apps: number;
  created: string;
}
/** A node's agent password, shown once (node.create, node.rotate_secret). */
export interface NodeSecret {
  node: DropNode;
  password: string;
  identity: string;
  collection: string;
}
/** An app's runtime status (internal/dropapi Status). */
export interface AppStatus {
  first_run?: { kind: "pocketbase_installer" | "zeroclaw_pairing"; value: string; expires_at: string };
  log_tail?: string;
  url_ready?: boolean;
  app: string;
  phase: string;
  message: string;
  desired_state: DesiredState;
  deployment_id: string;
  reported_deployment_id: string;
  hostname: string;
  placed: boolean;
  /** The node's name, for admins only. */
  node?: string;
  restart_count: number;
  last_exit_code: number | null;
  last_hook?: { event: string; outcome: string; time: string; durationMs: number };
  converged_at: string;
  observed_at: string;
  last_seen: string;
}

/** The largest release (90 MiB, dropapi.MaxReleaseBytes). */
export const MAX_RELEASE_BYTES = 90 * 1024 * 1024;

/** The first bytes of a file (FileReader works in every browser and in tests). */
function readStart(file: Blob, bytes: number): Promise<Uint8Array> {
  if (typeof file.arrayBuffer === "function") {
    return file.slice(0, bytes).arrayBuffer().then(buffer => new Uint8Array(buffer));
  }
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(new Uint8Array(reader.result as ArrayBuffer));
    reader.onerror = () => reject(new Error("The app file could not be read."));
    reader.readAsArrayBuffer(file.slice(0, bytes));
  });
}

/**
 * Why a file cannot be a release, or "" (dropapi.ReleaseHeaderProblem): a
 * 64-bit little-endian x86-64 ELF executable of at most `max` bytes. The
 * server checks again; this only fails fast.
 */
export async function releaseFileProblem(
  file: Blob,
  max = MAX_RELEASE_BYTES,
): Promise<string> {
  if (file.size === 0) return "The app file is empty.";
  if (file.size > max)
    return `The app file is ${(file.size / 1048576).toFixed(1)} MiB. The limit is ${Math.floor(max / 1048576)} MiB.`;
  const h = await readStart(file, 64);
  const want =
    "Choose a Linux (amd64) ELF executable as the app file";
  if (h.length < 64 || h[0] !== 0x7f || h[1] !== 0x45 || h[2] !== 0x4c || h[3] !== 0x46)
    return `${want}. This file is not an ELF executable.`;
  if (h[4] !== 2 || h[5] !== 1 || h[6] !== 1)
    return `${want}. This ELF file is not 64-bit little-endian.`;
  if (h[7] !== 0 && h[7] !== 3)
    return `${want}. This ELF file is for another operating system.`;
  const type = h[16] | (h[17] << 8);
  if (type !== 2 && type !== 3)
    return `${want}. This ELF file is not an executable.`;
  const machine = h[18] | (h[19] << 8);
  if (machine !== 0x3e) {
    const arch =
      { 0xb7: "arm64", 0x03: "386", 0xf3: "riscv64" }[machine] ??
      "another architecture";
    return `${want}. This one is built for ${arch}.`;
  }
  return "";
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
export async function op<T>(name: Op, input: object = {}, idempotencyKey?: string): Promise<T> {
  try {
    const body = await pb.send<{ result: T }>("/api/drop/op", {
      method: "POST",
      body: { op: name, input, ...(idempotencyKey && { idempotency_key: idempotencyKey }) },
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
  app_quota?: number;
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

async function listApps(): Promise<{ apps: App[]; quota: Quota; has_more: boolean }> {
  return op("app.list", { include_archived: true, limit: 500 });
}
export interface DeletionReceipt { app: string; slug: string; completed: boolean; waiting_nodes: number }
interface AppDetail {
  app: App;
  secrets: Secret[];
  deletion_inventory?: { release_count: number; replica_mode: "enabled" | "local" | "none" };
}
async function getApp(app: string, inventory = false): Promise<AppDetail> {
  return op("app.get", inventory ? { app, deletion_inventory: true } : { app });
}
async function createApp(
  input: { name: string; slug: string; kind?: AppKind; env?: Record<string, string> } & AppSettings,
): Promise<App> {
  return (await op<{ app: App }>("app.create", input)).app;
}
async function setApp(
  app: string,
  change: AppSettings & {
    sign_in_email_enabled?: boolean;
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

async function listReleases(
  app: string,
): Promise<{ releases: Release[]; current_release: string; has_more: boolean }> {
  return op("release.list", { app, limit: 100 });
}
async function createUploadLink(
  app: string,
  input: { note?: string; activate?: boolean },
): Promise<UploadLink> {
  return op("release.upload_link", { app, ...input });
}
async function deployRelease(
  app: string,
  release: string,
): Promise<{ app: App; release: Release; deployment_bumped: boolean; deployment_id: string }> {
  return op("release.deploy", { app, release });
}
/**
 * PUT a file to an upload link's path, reporting progress. Resolves with the
 * upload's answer; rejects with an OpError carrying the server's code.
 */
function uploadRelease(
  path: string,
  file: Blob,
  onProgress: (loaded: number, total: number) => void,
): Promise<UploadResult> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("PUT", pb.buildURL(path));
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) onProgress(event.loaded, event.total);
    };
    xhr.onload = () => {
      let body: { code?: unknown; message?: unknown } & Partial<UploadResult> = {};
      try {
        body = JSON.parse(xhr.responseText) as typeof body;
      } catch {
        // Not JSON: a proxy's own error page.
      }
      if (xhr.status === 200 && body.release) {
        resolve(body as UploadResult);
        return;
      }
      reject(
        new OpError(
          xhr.status,
          typeof body.code === "string" ? body.code : "",
          typeof body.message === "string" && body.message
            ? body.message
            : xhr.status === 413
              ? "The file is larger than the server or its proxy accepts."
              : `The upload failed (HTTP ${xhr.status}). Please retry.`,
        ),
      );
    };
    xhr.onerror = () =>
      reject(new OpError(0, "", "The upload failed. Check your connection and retry."));
    xhr.send(file);
  });
}

async function appStatus(app: string, verifyUrl = false): Promise<AppStatus> {
  return op("status", { app, ...(verifyUrl && { verify_url: true }) });
}
async function listNodes(): Promise<DropNode[]> {
  return (await op<{ nodes: DropNode[] }>("node.list", { limit: 500 })).nodes;
}
async function createNode(input: {
  name: string;
  host_id: string;
  ec2_instance_id?: string;
  private_ip?: string;
}): Promise<NodeSecret> {
  return op("node.create", input);
}
async function rotateNodeSecret(node: string): Promise<NodeSecret> {
  return op("node.rotate_secret", { node });
}
async function setNodeState(node: string, state: NodeState): Promise<DropNode> {
  return (await op<{ node: DropNode }>("node.set_state", { node, state })).node;
}
async function placeApp(
  app: string,
  node: string,
): Promise<{ moved: boolean; previous_node?: string }> {
  return op("placement.set", { app, node });
}
/** One app's S3 usage counts (internal/usage Counts). */
export interface UsageCounts {
  class_a: number;
  class_b: number;
  deletes: number;
  bytes_in: number;
  bytes_out: number;
  status_4xx: number;
  status_5xx: number;
  denied: number;
}
export type UsageMetric = "requests_a" | "requests_b" | "bytes_out" | "stored";
export interface UsageLimit {
  metric: UsageMetric;
  label: string;
  value: number;
  soft: number;
  hard: number;
  level: "ok" | "soft" | "hard";
}
/** The answer of usage.get. */
export interface Usage {
  app: string;
  month: string;
  hourly: (UsageCounts & { hour: string })[];
  budget_refusals?: number;
  month_to_date: UsageCounts;
  stored: {
    day: string;
    current_bytes: number;
    noncurrent_bytes: number;
    objects: number;
    versions: number;
  } | null;
  limits: UsageLimit[];
  limits_override: Record<string, number>;
  estimate: {
    class_a_usd: number;
    class_b_usd: number;
    egress_usd: number;
    storage_usd: number;
    total_usd: number;
  };
  estimate_note: string;
  exceeded: { metric: string; level: string; stopped: boolean; message: string; created: string }[];
  metered: boolean;
}
async function getUsage(app: string): Promise<Usage> {
  return op("usage.get", { app });
}
async function setUsageLimits(
  app: string,
  limits: { requests_a?: number; requests_b?: number; bytes_out?: number; stored_bytes?: number; reset?: boolean },
): Promise<unknown> {
  return op("usage.set_limits", { app, ...limits });
}

async function clearPlacement(app: string): Promise<{ cleared_node: string }> {
  return op("placement.clear", { app });
}

export interface BetaRelease {
  generic?: boolean;
  needs_storage_credits?: boolean;
  requires?: string[];
  optional?: string[];
  http_port?: number;
  name: string;
  version: string;
  multipliers: number[];
  suggested_multiplier?: number;
}

export interface WaitlistEntry {
  id: string;
  email: string;
  verified_at: string;
  invited_at: string;
  expires_at: string;
  activated_at: string;
  state: "waiting" | "open" | "used" | "expired" | "revoked";
  account_id: string;
  suspended: boolean;
  mail_state: "" | "queued" | "sent" | "failed";
}

export const dropApi = {
  importURL: (url: string, sha256?: string) =>
    op<{ content_base64: string; sha256: string }>("beta.import_url", { url, sha256 }),
  listWaitlist: (search: string, offset: number, limit: number) =>
    op<{entries: WaitlistEntry[]; has_more: boolean}>("waitlist.list", {search, offset, limit}),
  changeWaitlist: (action: "invite" | "resend" | "revoke", id: string, role?: Role) =>
    op<{ok: boolean}>(`waitlist.${action}`, { id, role }),
  inspectBeta: (sha256: string) => op<BetaRelease>("beta.inspect", { sha256 }),
  probeUpload: (sha256:string) => op<{path:string}>("beta.probe_upload",{sha256}),
  prepareBeta: (sha256: string, multiplier: number, key: string, http_port?: number, name?: string) =>
    op<{ app: App }>("beta.prepare", { sha256, multiplier, http_port, name }, key),
  deployBeta: (app: string, release: string) =>
    op<{ deployment_id: string }>("beta.deploy", { app, release }),
  me,
  listUsers,
  setUser,
  createInvite,
  listInvites,
  revokeInvite,
  listApps,
  getApp,
  listDeletions: (app = "", offset = 0) =>
    op<{deletions: DeletionReceipt[]; has_more: boolean}>("app.deletions", { app, offset, limit: 25 }),
  deleteApp: (app: string, slug: string) => op("app.delete", { app, confirm_slug: slug }),
  createApp,
  setApp,
  appAction,
  setSecret,
  deleteSecret,
  listReleases,
  createUploadLink,
  deployRelease,
  uploadRelease,
  appStatus,
  listNodes,
  createNode,
  rotateNodeSecret,
  setNodeState,
  placeApp,
  clearPlacement,
  getUsage,
  setUsageLimits,
};
