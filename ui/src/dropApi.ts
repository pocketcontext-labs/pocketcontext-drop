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

export const dropApi = {
  me,
  listUsers,
  setUser,
  createInvite,
  listInvites,
  revokeInvite,
};
