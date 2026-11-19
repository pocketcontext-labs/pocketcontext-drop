import { afterEach, describe, expect, it } from "vitest";
import {
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
    expect(viewFromHash("#/admin/users")).toBe("apps");
    expect(viewFromHash("")).toBe("apps");
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
