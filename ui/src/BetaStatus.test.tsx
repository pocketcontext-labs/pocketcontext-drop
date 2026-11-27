import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { BetaStatus } from "./BetaDeploy";
import { dropApi, type AppStatus } from "./dropApi";

const deployment = "d-aaaaaaaaaaaaaaa-dep-1";
const hostname = "fixture.apps.example.test";
const running: AppStatus = {
  app: "aaaaaaaaaaaaaaa", phase: "running", desired_state: "running", message: "",
  deployment_id: deployment, reported_deployment_id: deployment, placed: true,
  hostname, url_ready: true, restart_count: 0, last_exit_code: null, observed_at: "", last_seen: "", converged_at: "",
};

function show() {
  return render(<BetaStatus id={running.app} name="Fixture" release="release" hostname={hostname} />);
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

it("opens the private installer on the verified deployment's own HTTPS origin", async () => {
  vi.spyOn(dropApi, "appStatus").mockResolvedValue({
    ...running, first_run: { kind: "pocketbase_installer", value: "header.payload.signature", expires_at: new Date(Date.now() + 60000).toISOString() },
  });
  show();
  expect(await screen.findByRole("link", { name: "Open PocketBase installer" })).toHaveAttribute("href", "https://" + hostname + "/_/#/pbinstall/header.payload.signature");
});

it("shows a current pairing code to the owner after public HTTP readiness", async () => {
  const value = "syntheticPairingCode";
  vi.spyOn(dropApi, "appStatus").mockResolvedValue({
    ...running, first_run: { kind: "zeroclaw_pairing", value, expires_at: new Date(Date.now() + 60000).toISOString() },
  });
  show();
  expect(await screen.findByText(value)).toBeVisible();
  expect(screen.getByRole("button", { name: "Copy pairing code" })).toBeVisible();
});

it("keeps a running process waiting until its public endpoint responds, and explains the failed check", async () => {
  vi.useFakeTimers();
  vi.spyOn(dropApi, "appStatus").mockResolvedValue({
    ...running, url_ready: false, log_tail: "listening on the wrong port",
    first_run: { kind: "pocketbase_installer", value: "sensitive", expires_at: new Date(Date.now() + 600000).toISOString() },
  });
  show();
  await act(async () => { await vi.advanceTimersByTimeAsync(96000); });
  expect(screen.getByText("Release verification: complete")).toBeVisible();
  expect(screen.getByText("Startup: complete")).toBeVisible();
  expect(screen.getByRole("alert")).toHaveTextContent("The public HTTP check has not succeeded");
  expect(screen.getByText("listening on the wrong port")).toBeVisible();
  expect(screen.queryByRole("link", { name: "Open PocketBase installer" })).toBeNull();
  expect(screen.queryByText("Your app is live.")).toBeNull();
});
