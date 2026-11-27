import { afterEach, expect, it, vi } from "vitest";
import { openPrivateApp } from "./PrivateApps";
import { op } from "./dropApi";

vi.mock("./dropApi", () => ({ op: vi.fn() }));

const hostname = "testing.example.com";
const origin = "https://" + hostname;
afterEach(() => { vi.restoreAllMocks(); vi.mocked(op).mockReset(); });

function setup() {
  const popup = { close: vi.fn(), postMessage: vi.fn(), location: { replace: vi.fn() } };
  vi.spyOn(window, "open").mockReturnValue(popup as unknown as Window);
  return popup;
}

it("sends the credential only to the expected app window after its gateway is ready", async () => {
  const popup = setup();
  vi.mocked(op).mockResolvedValue({ url: origin, token: "runtime-ticket" });
  const opened = openPrivateApp("private-app", hostname);
  await vi.waitFor(() => expect(popup.location.replace).toHaveBeenCalledWith(origin + "/.well-known/drop/app-session"));
  const ready = (source: unknown, from: string) =>
    window.dispatchEvent(Object.assign(new Event("message"), { source, origin: from, data: "drop-private-ready" }));
  ready(window, origin);
  ready(popup, "https://different.example.com");
  expect(popup.postMessage).not.toHaveBeenCalled();
  ready(popup, origin);
  await opened;
  expect(op).toHaveBeenCalledWith("private.access", { app: "private-app" });
  expect(popup.postMessage).toHaveBeenCalledExactlyOnceWith({ type: "drop-private-access", token: "runtime-ticket" }, origin);
});

it("closes the new tab if the credential has a different destination", async () => {
  const popup = setup();
  vi.mocked(op).mockResolvedValue({ url: origin + ":444", token: "runtime-ticket" });
  await expect(openPrivateApp("private-app", hostname)).rejects.toThrow("unexpected");
  expect(popup.close).toHaveBeenCalled();
  expect(popup.location.replace).not.toHaveBeenCalled();
});

it("keeps denied launches on the control plane", async () => {
  const popup = setup();
  vi.mocked(op).mockRejectedValue(new Error("Access denied"));
  await expect(openPrivateApp("private-app", hostname)).rejects.toThrow("Access denied");
  expect(popup.close).toHaveBeenCalled();
  expect(popup.postMessage).not.toHaveBeenCalled();
});
