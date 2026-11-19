import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  api,
  connection,
  logout,
  pb,
  refreshSession,
  validName,
  validSlug,
  watchApps,
} from "./api";

const user = { id: "user00000000001", collectionId: "_pb_users_auth_", collectionName: "users" };
const token = `synthetic.${btoa(JSON.stringify({ exp: 4102444800 }))}.signature`;
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

beforeEach(() => pb.authStore.save(token, user));
afterEach(() => {
  vi.restoreAllMocks();
  pb.authStore.clear();
});

describe("requests", () => {
  it("clears the session when the server rejects its token", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ message: "Unauthorized" }, 401));
    await expect(pb.send("/api/drop/op", { method: "POST", body: { op: "app.list" } })).rejects.toMatchObject({ status: 401 });
    expect(pb.authStore.token).toBe("");
  });
  it("keeps the session when an operation is refused for the role", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ status: 403, code: "forbidden", message: "Admins only.", data: {} }, 403));
    await expect(pb.send("/api/drop/op", { method: "POST", body: { op: "user.list" } })).rejects.toMatchObject({ status: 403 });
    expect(pb.authStore.token).toBe(token);
  });
  it("discards a response that arrives after the account changed", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () => {
      pb.authStore.save(token, { ...user, id: "user00000000002" });
      return json({ result: { apps: [] } });
    });
    await expect(pb.send("/api/drop/op", { method: "POST", body: { op: "app.list" } })).rejects.toThrow(/Session changed/);
  });
});

describe("connection", () => {
  beforeEach(() => vi.spyOn(api, "loginMode").mockResolvedValue("email"));
  it("reads the MCP endpoint from the protected resource document", async () => {
    const resource = `https://${location.host}/mcp`;
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(json({ resource }));
    expect(await connection()).toEqual({ kind: "mcp", url: resource });
  });
  it("is local on the password-only local server, without asking for MCP", async () => {
    vi.spyOn(api, "loginMode").mockResolvedValue("password");
    const fetch = vi.spyOn(globalThis, "fetch");
    expect(await connection()).toEqual({ kind: "local" });
    expect(fetch).not.toHaveBeenCalled();
  });
  it("refuses a hosted origin without MCP", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(new Response("", { status: 404 }));
    await expect(connection()).rejects.toThrow(/unavailable/);
  });
  it("refuses another origin or path", async () => {
    for (const resource of ["https://evil.example.test/mcp", `http://${location.host}/mcp`, `https://${location.host}/other`, 42]) {
      vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(json({ resource }));
      await expect(connection()).rejects.toThrow(/unavailable/);
    }
  });
});

describe("writes", () => {
  it("validates the name and slug before sending", () => {
    expect(() => validName("   ")).toThrow(/Name/);
    expect(() => validName("x".repeat(81))).toThrow(/80/);
    expect(validName("x".repeat(80))).toHaveLength(80);
    for (const bad of ["ab", "1abc", "abc-", "Abc", "a_bc", "a".repeat(33)])
      expect(() => validSlug(bad)).toThrow(/slug/);
    expect(validSlug("hello-app")).toBe("hello-app");
    expect(validSlug("a".repeat(32))).toHaveLength(32);
  });
  it("subscribes to app changes and ignores events after sign-out", async () => {
    let handler: () => void = () => {};
    const unsubscribe = vi.fn().mockResolvedValue(undefined);
    const subscribe = vi.fn(async (_topic: string, callback: () => void) => { handler = callback; return unsubscribe; });
    vi.spyOn(pb, "collection").mockReturnValue({ subscribe } as never);
    const notify = vi.fn();
    const stop = await watchApps(notify);
    expect(subscribe).toHaveBeenCalledWith("*", expect.any(Function));
    handler();
    expect(notify).toHaveBeenCalledTimes(1);
    logout();
    handler();
    expect(notify).toHaveBeenCalledTimes(1);
    stop();
    expect(unsubscribe).toHaveBeenCalled();
  });
});

describe("session", () => {
  it("refreshes explicitly and ignores a refresh that completes after sign-out", async () => {
    const send = vi.spyOn(pb, "send").mockImplementation(async () => {
      logout();
      return { token: "renewed", record: user };
    });
    await refreshSession();
    expect(send).toHaveBeenCalledWith("/api/collections/users/auth-refresh", { method: "POST", headers: { Authorization: token } });
    expect(pb.authStore.token).toBe("");
  });
});
