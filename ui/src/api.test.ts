import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  addApp,
  listApps,
  logout,
  pb,
  query,
  refreshSession,
  searchApps,
  SearchChangedError,
  SearchUnavailableError,
  TruncatedQueryError,
  validName,
  validSlug,
  watchApps,
} from "./api";

const user = { id: "user00000000001", collectionId: "_pb_users_auth_", collectionName: "users" };
const token = `synthetic.${btoa(JSON.stringify({ exp: 4102444800 }))}.signature`;
const generation = "a".repeat(64);
function response(rows: Record<string, unknown>[], truncated = false) {
  const columns = ["id", "name", "slug", "created"];
  return { columns, rows: rows.map(row => columns.map(column => row[column])), truncated };
}
const row = (id: string, name = `Hello ${id}`) => ({ id, name, slug: `app-${id}`, created: "2026-10-25 10:00:00.000Z" });

beforeEach(() => pb.authStore.save(token, user));
afterEach(() => {
  vi.restoreAllMocks();
  pb.authStore.clear();
});

describe("SQL reads", () => {
  it("maps columns to objects and refuses truncated results", async () => {
    vi.spyOn(pb, "send").mockResolvedValueOnce(response([row("app000000000001")])).mockResolvedValueOnce(response([], true));
    expect(await query("SELECT 1")).toEqual([row("app000000000001")]);
    await expect(query("SELECT 1")).rejects.toBeInstanceOf(TruncatedQueryError);
  });
  it("lists the apps newest first through the SQL endpoint", async () => {
    const send = vi.spyOn(pb, "send").mockResolvedValue(response([row("app000000000002"), row("app000000000001")]));
    expect((await listApps()).map(g => g.id)).toEqual(["app000000000002", "app000000000001"]);
    expect(send).toHaveBeenCalledWith("/api/context/query", {
      method: "POST",
      body: { sql: "SELECT id, name, slug, created FROM apps ORDER BY created DESC, id DESC LIMIT 100" },
    });
  });
  it("refuses malformed rows and anonymous reads", async () => {
    vi.spyOn(pb, "send").mockResolvedValue(response([{ id: "x' OR 1", name: "m", slug: "s", created: "c" }]));
    await expect(listApps()).rejects.toThrow(/Invalid app/);
    pb.authStore.clear();
    await expect(listApps()).rejects.toThrow(/sign in/);
  });
  it("clears the session when the server rejects its token", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ message: "Unauthorized" }), { status: 401, headers: { "Content-Type": "application/json" } }));
    await expect(query("SELECT 1")).rejects.toMatchObject({ status: 401 });
    expect(String(fetch.mock.calls[0][0])).toMatch(/\/api\/context\/query$/);
    expect(pb.authStore.token).toBe("");
  });
  it("discards a response that arrives after the account changed", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () => {
      pb.authStore.save(token, { ...user, id: "user00000000002" });
      return new Response(JSON.stringify(response([row("app000000000001")])), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    await expect(query("SELECT 1")).rejects.toThrow(/Session changed/);
  });
});

describe("writes", () => {
  it("creates a trimmed app through the collection API", async () => {
    const create = vi.fn().mockResolvedValue(row("app000000000003", "Hi"));
    vi.spyOn(pb, "collection").mockReturnValue({ create } as never);
    expect(await addApp("  Hi  ", " app-app000000000003 ")).toEqual(row("app000000000003", "Hi"));
    expect(pb.collection).toHaveBeenCalledWith("apps");
    expect(create).toHaveBeenCalledWith({ name: "Hi", slug: "app-app000000000003" });
  });
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

describe("full-text search", () => {
  const searchResult = (hits: { id: string; score: number; excerpt: string }[], hasMore = false) =>
    ({ index: "apps", generation, hits, hasMore, truncated: hasMore });
  it("posts the index alias without a scope and hydrates hits in ranking order", async () => {
    const send = vi.spyOn(pb, "send").mockImplementation(async path => {
      if (path === "/api/context/search")
        return searchResult([{ id: "app000000000002", score: -2, excerpt: "hello there" }, { id: "app000000000001", score: -1, excerpt: "hello" }], true);
      return response([row("app000000000001"), row("app000000000002")]);
    });
    const page = await searchApps(" hello ");
    expect(send.mock.calls[0]).toEqual(["/api/context/search", { method: "POST", body: { index: "apps", query: "hello", offset: 0, limit: 20 } }]);
    expect(send.mock.calls[1][1]?.body.sql).toBe(
      "SELECT id, name, slug, created FROM apps WHERE id IN ('app000000000002','app000000000001') ORDER BY id LIMIT 20",
    );
    expect(page).toMatchObject({ generation, hasMore: true });
    expect(page.hits.map(hit => [hit.id, hit.excerpt])).toEqual([["app000000000002", "hello there"], ["app000000000001", "hello"]]);
  });
  it("pins the generation when paging", async () => {
    const send = vi.spyOn(pb, "send").mockResolvedValue(searchResult([]));
    await searchApps("hello", 20, 20, generation);
    expect(send.mock.calls[0][1]?.body).toEqual({ index: "apps", query: "hello", offset: 20, limit: 20, expectedGeneration: generation });
    await expect(searchApps("hello", 20)).rejects.toThrow(/page/);
  });
  it("refuses invalid queries before sending", async () => {
    const send = vi.spyOn(pb, "send");
    await expect(searchApps(" ")).rejects.toThrow(/1 to 500/);
    await expect(searchApps(Array(17).fill("w").join(" "))).rejects.toThrow(/16 words/);
    expect(send).not.toHaveBeenCalled();
  });
  it.each([
    [{ index: "other" }],
    [{ generation: "short" }],
    [{ scope: "space0000000001" }],
  ])("treats a mismatched response as a changed search (%o)", async patch => {
    vi.spyOn(pb, "send").mockResolvedValue({ ...searchResult([]), ...patch });
    await expect(searchApps("hello")).rejects.toBeInstanceOf(SearchChangedError);
  });
  it("refuses out-of-order or duplicate hits", async () => {
    vi.spyOn(pb, "send").mockResolvedValueOnce(searchResult([{ id: "app000000000001", score: -1, excerpt: "" }, { id: "app000000000002", score: -2, excerpt: "" }]));
    await expect(searchApps("hello")).rejects.toThrow(/ranking/);
    vi.spyOn(pb, "send").mockResolvedValueOnce(searchResult([{ id: "app000000000001", score: -1, excerpt: "" }, { id: "app000000000001", score: -1, excerpt: "" }]));
    await expect(searchApps("hello")).rejects.toThrow(/hit/);
  });
  it("reports an app that vanished between search and hydration as changed", async () => {
    vi.spyOn(pb, "send").mockImplementation(async path =>
      path === "/api/context/search" ? searchResult([{ id: "app000000000001", score: -1, excerpt: "" }]) : response([]));
    await expect(searchApps("hello")).rejects.toBeInstanceOf(SearchChangedError);
  });
  it.each([[409, SearchChangedError], [413, SearchUnavailableError], [503, SearchUnavailableError]])("maps status %i", async (status, kind) => {
    vi.spyOn(pb, "send").mockRejectedValue(Object.assign(new Error("failed"), { status }));
    await expect(searchApps("hello")).rejects.toBeInstanceOf(kind);
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
