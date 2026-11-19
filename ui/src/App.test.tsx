import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api, pb } from "./api";
import App from "./App";
import { browserNavigation } from "./routes";

const token = `synthetic.${btoa(JSON.stringify({ exp: 4102444800 }))}.signature`;
const user = { id: "user00000000001", collectionId: "_pb_users_auth_", collectionName: "users", email: "owner@example.test" };
beforeEach(() => history.replaceState(null, "", "/"));
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  pb.authStore.clear();
  localStorage.clear();
});

it("shows the password sign-in locally until a users session exists, then the reader, and signs out", async () => {
  vi.spyOn(api, "loginMode").mockResolvedValue("password");
  vi.spyOn(api, "listApps").mockResolvedValue([]);
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
  vi.spyOn(api, "loginMode").mockResolvedValue("email");
  const request = vi.spyOn(api, "requestEmailLogin").mockResolvedValue("otp000000000001");
  const redeem = vi.spyOn(api, "loginEmailCode").mockImplementation(async () => pb.authStore.save(token, user));
  vi.spyOn(api, "listApps").mockResolvedValue([]);
  vi.spyOn(api, "watchApps").mockResolvedValue(() => {});
  vi.spyOn(api, "refreshSession").mockResolvedValue();
  render(<App />);
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
  const redeem = vi.spyOn(api, "loginEmail").mockImplementation(async () => pb.authStore.save(token, user));
  vi.spyOn(api, "listApps").mockResolvedValue([]);
  vi.spyOn(api, "watchApps").mockResolvedValue(() => {});
  vi.spyOn(api, "refreshSession").mockResolvedValue();
  render(<App />);
  expect(location.hash).toBe("#/login");
  expect(redeem).not.toHaveBeenCalled();
  fireEvent.click(await screen.findByRole("button", { name: "Continue to Drop" }));
  expect(await screen.findByRole("heading", { name: "Your apps" })).toBeInTheDocument();
  expect(redeem).toHaveBeenCalledWith("otp000000000001", "12345678901234567890123456789012");
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
  vi.spyOn(api, "listApps").mockResolvedValue([]);
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
  vi.spyOn(api, "loginMode").mockResolvedValue("password");
  render(<App />);
  act(() => pb.authStore.save(token, { ...user, collectionName: "_superusers" }));
  expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
});
