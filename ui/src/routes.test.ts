import { afterEach, describe, expect, it } from "vitest";
import {
  appHash,
  appRouteFromHash,
  inviteEmailFromHash,
  loginReturnHash,
  oauthTargetFromHash,
  readEmailLoginLink,
  rememberLoginReturn,
  safeOAuthTarget,
  takeLoginReturn,
  viewFromHash,
} from "./routes";

afterEach(() => localStorage.clear());

describe("routes", () => {
  it("knows its pages and defaults to the apps", () => {
    expect(viewFromHash("#/connect")).toBe("connect");
    expect(viewFromHash("#/apps")).toBe("apps");
    expect(viewFromHash("#/admin/users")).toBe("admin-users");
    expect(viewFromHash("#/admin/nodes")).toBe("apps");
    expect(viewFromHash("")).toBe("apps");
    expect(viewFromHash("#/apps/new")).toBe("app-new");
    expect(viewFromHash("#/apps/app000000000001")).toBe("app");
    expect(viewFromHash("#/apps/app000000000001/env")).toBe("app");
    expect(viewFromHash("#/apps/not-an-id")).toBe("apps");
  });
  it("reads an app's page and tab", () => {
    expect(appRouteFromHash("#/apps/app000000000001")).toEqual({ id: "app000000000001", tab: "overview" });
    expect(appRouteFromHash("#/apps/app000000000001/settings")).toEqual({ id: "app000000000001", tab: "settings" });
    expect(appRouteFromHash("#/apps/app000000000001/releases")).toEqual({ id: "app000000000001", tab: "releases" });
    expect(appRouteFromHash("#/apps/app000000000001/other")).toBeNull();
    expect(appRouteFromHash("#/apps/new")).toBeNull();
    expect(appHash("app000000000001", "env")).toBe("#/apps/app000000000001/env");
    expect(loginReturnHash("#/apps/app000000000001/env")).toBe("#/apps/app000000000001");
  });
  it("reads the address of an invite's sign-in URL", () => {
    expect(inviteEmailFromHash("#/invite?email=friend%40example.test")).toBe("friend@example.test");
    expect(inviteEmailFromHash("#/invite?email=a%40b&email=c%40d")).toBe("");
    expect(inviteEmailFromHash("#/invite?email=not-an-address")).toBe("");
    expect(inviteEmailFromHash("#/apps?email=friend%40example.test")).toBe("");
  });
  it("reads only well-formed email links", () => {
    expect(readEmailLoginLink("#/login?otpId=abc123&otp=12345678")).toEqual({ otpId: "abc123", password: "12345678" });
    expect(readEmailLoginLink("#/login?otpId=abc&otpId=def&otp=12345678")).toEqual({ otpId: "", password: "" });
    expect(readEmailLoginLink("#/login?otpId=a b&otp=1234")).toEqual({ otpId: "", password: "" });
    expect(readEmailLoginLink("#/apps")).toBeNull();
  });
  it("follows only the consent page of this origin", () => {
    expect(safeOAuthTarget("/oauth/authorize?client_id=x")).toBe("/oauth/authorize?client_id=x");
    for (const bad of ["https://evil.example.test/oauth/authorize?x", "//evil.example.test/oauth/authorize?x", "/oauth/token?x", "/oauth/authorize", "/oauth/authorize?x#y", "/oauth/authorize?\u0000"])
      expect(safeOAuthTarget(bad)).toBeNull();
    expect(oauthTargetFromHash("#/signin?next=" + encodeURIComponent("/oauth/authorize?a=1"))).toBe("/oauth/authorize?a=1");
    expect(oauthTargetFromHash("#/signin?next=a&next=b")).toBeNull();
  });
  it("remembers a return place once, only in the forms it writes", () => {
    const signin = "#/signin?next=" + encodeURIComponent("/oauth/authorize?a=1");
    expect(loginReturnHash(signin)).toBe(signin);
    expect(loginReturnHash("#/connect")).toBe("#/connect");
    expect(loginReturnHash("#/apps")).toBeNull();
    rememberLoginReturn(signin);
    expect(takeLoginReturn()).toBe(signin);
    expect(takeLoginReturn()).toBeNull();
    localStorage.setItem("drop.reader.login_return", JSON.stringify({ hash: "#/elsewhere", expires: Date.now() + 1000 }));
    expect(takeLoginReturn()).toBeNull();
  });
});
