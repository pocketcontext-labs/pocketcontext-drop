import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api, pb } from "./api";
import App from "./App";
import { dropApi, type Me } from "./dropApi";
import { browserNavigation } from "./routes";

const token = `synthetic.${btoa(JSON.stringify({ exp: 4102444800 }))}.signature`;
const user = { id: "user00000000001", collectionId: "_pb_users_auth_", collectionName: "users", email: "owner@example.test" };
const noApps = { apps: [], quota: { used: 0, limit: 3, unlimited: false }, has_more: false };
const member: Me = { id: user.id, email: user.email, role: "user", admin: false, owner: false, app_quota: 3 };
beforeEach(() => {
  history.replaceState(null, "", "/");
  vi.spyOn(api, "loginMode").mockResolvedValue({ mode: "email" });
  vi.spyOn(dropApi, "me").mockResolvedValue(member);
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  pb.authStore.clear();
  localStorage.clear();
});

it("shows the password sign-in locally until a users session exists, then the reader, and signs out", async () => {
  vi.spyOn(api, "loginMode").mockResolvedValue({ mode: "password" });
  vi.spyOn(dropApi, "listApps").mockResolvedValue(noApps);
  vi.spyOn(api, "watchApps").mockResolvedValue(() => {});
  const refresh = vi.spyOn(api, "refreshSession").mockResolvedValue();
  render(<App />);
  expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  act(() => pb.authStore.save(token, user));
  expect(await screen.findByRole("heading", { name: "Your apps" })).toBeInTheDocument();
  expect(screen.getByText("owner@example.test")).toBeInTheDocument();
  expect(refresh).toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
  expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  expect(pb.authStore.token).toBe("");
});

it("signs in by email on the hosted origin with the code from the email", async () => {
  vi.spyOn(api, "loginMode").mockResolvedValue({ mode: "email" });
  const request = vi.spyOn(api, "requestEmailLogin").mockResolvedValue("otp000000000001");
  const redeem = vi.spyOn(api, "loginEmailCode").mockImplementation(async () => pb.authStore.save(token, user));
  vi.spyOn(dropApi, "listApps").mockResolvedValue(noApps);
  vi.spyOn(api, "watchApps").mockResolvedValue(() => {});
  vi.spyOn(api, "refreshSession").mockResolvedValue();
  render(<App />);
  fireEvent.click(await screen.findByRole("button", { name: "Sign in" }));
  fireEvent.change(await screen.findByLabelText("Email"), { target: { value: "owner@example.test" } });
  fireEvent.click(screen.getByRole("button", { name: "Email me a sign-in code" }));
  expect(await screen.findByRole("heading", { name: "Check your inbox" })).toBeInTheDocument();
  expect(request).toHaveBeenCalledWith("owner@example.test");
  fireEvent.change(screen.getByLabelText("Code from the email"), { target: { value: "123 456" } });
  expect(await screen.findByRole("heading", { name: "Your apps" })).toBeInTheDocument();
  expect(redeem).toHaveBeenCalledWith("otp000000000001", "123456");
});

it("confirms an emailed link before redeeming it and clears it from the address", async () => {
  history.replaceState(null, "", "/#/login?otpId=otp000000000001&otp=12345678901234567890123456789012");
  vi.spyOn(api, "loginMode").mockResolvedValue({ mode: "email", legal: {
    termsURL: "https://docs.example.test/terms", privacyURL: "https://docs.example.test/privacy", termsVersion: "new-terms",
  } });
  const redeem = vi.spyOn(api, "loginEmail").mockImplementation(async () => pb.authStore.save(token, user));
  vi.spyOn(dropApi, "listApps").mockResolvedValue(noApps);
  vi.spyOn(api, "watchApps").mockResolvedValue(() => {});
  vi.spyOn(api, "refreshSession").mockResolvedValue();
  render(<App />);
  expect(location.hash).toBe("#/login");
  expect(redeem).not.toHaveBeenCalled();
  fireEvent.click(await screen.findByRole("checkbox"));
  fireEvent.click(await screen.findByRole("button", { name: "Continue to Drop" }));
  expect(await screen.findByRole("heading", { name: "Your apps" })).toBeInTheDocument();
  expect(redeem).toHaveBeenCalledWith("otp000000000001", "12345678901234567890123456789012", "new-terms");
});

it("returns a signed-in browser to the OAuth consent request after checking the session", async () => {
  const target = "/oauth/authorize?client_id=abc&state=x";
  history.replaceState(null, "", "/#/signin?next=" + encodeURIComponent(target));
  pb.authStore.save(token, user);
  const refresh = vi.spyOn(api, "refreshSession").mockResolvedValue();
  const replace = vi.spyOn(browserNavigation, "replace").mockImplementation(() => {});
  render(<App />);
  await vi.waitFor(() => expect(replace).toHaveBeenCalledWith(target));
  expect(refresh).toHaveBeenCalledWith(true);
});

it("ignores a sign-in return target that is not the consent page", async () => {
  history.replaceState(null, "", "/#/signin?next=" + encodeURIComponent("https://evil.example.test/"));
  pb.authStore.save(token, user);
  vi.spyOn(dropApi, "listApps").mockResolvedValue(noApps);
  vi.spyOn(api, "watchApps").mockResolvedValue(() => {});
  vi.spyOn(api, "refreshSession").mockResolvedValue();
  const replace = vi.spyOn(browserNavigation, "replace").mockImplementation(() => {});
  render(<App />);
  expect(await screen.findByRole("heading", { name: "Your apps" })).toBeInTheDocument();
  expect(replace).not.toHaveBeenCalled();
  expect(location.hash).toBe("#/");
});

it("opens the Connect page from its hash", async () => {
  history.replaceState(null, "", "/#/connect");
  pb.authStore.save(token, user);
  vi.spyOn(api, "refreshSession").mockResolvedValue();
  vi.spyOn(api, "connection").mockResolvedValue({ kind: "local" });
  render(<App />);
  expect(await screen.findByRole("heading", { name: "Connect an assistant" })).toBeInTheDocument();
  expect(await screen.findByText(/hosted Drop only/)).toBeInTheDocument();
});

it("does not treat another collection's token as a reader session", async () => {
  vi.spyOn(api, "loginMode").mockResolvedValue({ mode: "password" });
  render(<App />);
  act(() => pb.authStore.save(token, { ...user, collectionName: "_superusers" }));
  expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
});

it("shows admins the Users page and everyone else Not found there", async () => {
  history.replaceState(null, "", "/#/admin/users");
  pb.authStore.save(token, user);
  vi.spyOn(api, "refreshSession").mockResolvedValue();
  render(<App />);
  expect(await screen.findByRole("heading", { name: "Not found" })).toBeInTheDocument();
  expect(screen.queryByRole("link", { name: "Users" })).not.toBeInTheDocument();
  cleanup();
  vi.spyOn(dropApi, "me").mockResolvedValue({ ...member, role: "admin", admin: true, owner: true });
  vi.spyOn(dropApi, "listUsers").mockResolvedValue([
    { id: user.id, email: user.email, role: "admin", app_quota: 3, suspended: false, owner: true, signed_in: true, invited_by: "", created: "2026-11-19 10:00:00.000Z" },
  ]);
  vi.spyOn(dropApi, "listInvites").mockResolvedValue([]);
  const create = vi.spyOn(dropApi, "createInvite").mockResolvedValue({
    invite: { id: "inv000000000001", email: "friend@example.test", role: "user", app_quota: 3, state: "open", invited_by: user.id, expires_at: "2026-11-26 10:00:00.000Z", used_at: "", used_by: "", revoked_at: "", created: "2026-11-19 10:00:00.000Z" },
    sign_in_url: "https://drop.example.test/#/invite?email=friend%40example.test",
  });
  render(<App />);
  expect(await screen.findByRole("heading", { name: "Users" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Users" })).toBeInTheDocument();
  expect(await screen.findByText("No invites yet.")).toBeInTheDocument();
  // The owner's own role and suspension cannot be changed.
  expect(screen.getByRole("button", { name: "Suspend" })).toBeDisabled();
  fireEvent.change(screen.getByLabelText("Email"), { target: { value: "friend@example.test" } });
  fireEvent.click(screen.getByRole("button", { name: "Invite" }));
  expect(await screen.findByText("https://drop.example.test/#/invite?email=friend%40example.test")).toBeInTheDocument();
  expect(create).toHaveBeenCalledWith({ email: "friend@example.test", role: "user" });
});

it("fills in the address of an invite's sign-in URL", async () => {
  history.replaceState(null, "", "/#/invite?email=friend%40example.test");
  vi.spyOn(api, "loginMode").mockResolvedValue({ mode: "email" });
  render(<App />);
  expect(await screen.findByLabelText("Email")).toHaveValue("friend@example.test");
});

it("keeps hosted admins on administration pages and never reads tenant apps for them", async () => {
  pb.authStore.save(token, user);
  vi.spyOn(dropApi, "me").mockResolvedValue({...member, hosted:true, admin:true, role:"admin"});
  vi.spyOn(api, "refreshSession").mockResolvedValue();
  const appReads = vi.spyOn(dropApi, "listApps");
  const list = vi.spyOn(dropApi, "listWaitlist").mockResolvedValue({entries:[],has_more:false});
  render(<App />);
  expect(await screen.findByText("No verified waitlist entries.")).toBeInTheDocument();
  expect(list).toHaveBeenCalledWith("", 0, 50);
  expect(screen.queryByRole("link", {name:"Apps"})).toBeNull();
  expect(appReads).not.toHaveBeenCalled();
  act(() => { location.hash = "#/apps/new"; window.dispatchEvent(new HashChangeEvent("hashchange")); });
  expect(await screen.findByRole("heading", {name:"Not found"})).toBeInTheDocument();
  expect(screen.queryByLabelText("App name")).toBeNull();
});

it("hides the admin waitlist from a hosted member including direct navigation", async () => {
  pb.authStore.save(token, user);
  vi.spyOn(dropApi,"me").mockResolvedValue({...member, hosted:true});
  vi.spyOn(api,"refreshSession").mockResolvedValue();
  const list = vi.spyOn(dropApi,"listWaitlist");
  history.replaceState(null,"","/#/admin/waitlist");
  render(<App />);
  expect(await screen.findByRole("heading",{name:"Not found"})).toBeInTheDocument();
  expect(screen.queryByRole("link",{name:"Waitlist"})).toBeNull();
  expect(list).not.toHaveBeenCalled();
});
