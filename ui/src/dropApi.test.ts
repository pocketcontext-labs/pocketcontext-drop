import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { pb } from "./api";
import { dropApi, OpError } from "./dropApi";

const user = { id: "user00000000001", collectionId: "_pb_users_auth_", collectionName: "users" };
const token = `synthetic.${btoa(JSON.stringify({ exp: 4102444800 }))}.signature`;
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

beforeEach(() => pb.authStore.save(token, user));
afterEach(() => {
  vi.restoreAllMocks();
  pb.authStore.clear();
});

it("calls one operation route with {op, input} and returns the result", async () => {
  const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ result: { invites: [] } }));
  expect(await dropApi.listInvites()).toEqual([]);
  const [url, init] = fetch.mock.calls[0];
  expect(new URL(String(url)).pathname).toBe("/api/drop/op");
  expect(JSON.parse(String((init as RequestInit).body))).toEqual({ op: "invite.list", input: { limit: 200 } });
});

it("keeps the session when an operation is refused, and ends it when the token is rejected", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
    json({ status: 403, code: "forbidden", message: "You cannot change your own role.", data: {} }, 403),
  );
  const refused = dropApi.setUser("user00000000001", { role: "user" });
  await expect(refused).rejects.toBeInstanceOf(OpError);
  await expect(refused).rejects.toMatchObject({ status: 403, code: "forbidden", message: "You cannot change your own role." });
  expect(pb.authStore.token).toBe(token);
  vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(json({ status: 401, code: "unauthenticated", message: "Sign in first.", data: {} }, 401));
  await expect(dropApi.me()).rejects.toMatchObject({ status: 401 });
  expect(pb.authStore.token).toBe("");
});
