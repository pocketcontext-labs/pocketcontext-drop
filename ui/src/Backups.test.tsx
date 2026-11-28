import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import Backups, { AppBackupSetting } from "./Backups";
import * as operations from "./dropApi";
import { pb } from "./api";

const workspace = {
  id: "workspace000001",
  name: "My Crew",
  slug: "crew-source",
};
const ready = {
  id: "backup000000001",
  app: workspace.id,
  name: workspace.name,
  state: "ready",
  created: 1795773600,
  bytes: 256 * 1024,
  microcredits_per_gib_month: 0,
};
const snapshot = () => ({
  eligible_apps: [workspace],
  backups: [ready],
  policies: [] as { app: string; next_at: number }[],
  jobs: [] as {
    id: string;
    backup: string;
    app: string;
    kind: string;
    state: string;
  }[],
  pricing: {
    microcredits_per_gib_month: 1_500_000,
    available_microcredits: 4_000_000,
  },
});
beforeEach(() => {
  HTMLDialogElement.prototype.showModal = function () {
    this.setAttribute("open", "");
  };
  HTMLDialogElement.prototype.close = function () {
    this.removeAttribute("open");
    this.dispatchEvent(new Event("close"));
  };
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  pb.authStore.clear();
});

it("enables daily backups for the selected workspace before queueing a manual capture", async () => {
  const data = snapshot();
  const other = {
    id: "workspace000002",
    name: "Experiment",
    slug: "crew-other",
  };
  data.eligible_apps.push(other);
  const op = vi
    .spyOn(operations, "op")
    .mockImplementation(async (name, input) => {
      if (name === "backup.configure")
        data.policies = [{ app: other.id, next_at: 1795852800 }];
      if (name === "backup.create") expect(input).toEqual({ app: other.id });
      return name === "backup.list" ? structuredClone(data) : {};
    });
  render(<Backups initialApp={other.id} />);
  expect(await screen.findByLabelText("App")).toHaveValue(other.id);
  expect(
    screen.getByText(/Estimated storage through expiry · 0 credits/),
  ).toBeVisible();
  expect(screen.getByRole("button", { name: "Back up now" })).toBeDisabled();
  expect(screen.getByText(/1.5 credits per GiB-month/)).toBeVisible();
  fireEvent.click(screen.getByLabelText("Daily backups"));
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Back up now" })).toBeEnabled(),
  );
  expect(op).toHaveBeenCalledWith("backup.configure", {
    app: other.id,
    enabled: true,
  });
  fireEvent.click(screen.getByRole("button", { name: "Back up now" }));
  expect(await screen.findByText("Backup queued.")).toBeVisible();
  expect(op).toHaveBeenCalledWith(
    "backup.create",
    { app: other.id },
    expect.any(String),
  );
});

it("requires the exact original slug before replacement", async () => {
  const op = vi.spyOn(operations, "op").mockResolvedValue(snapshot());
  render(<Backups />);
  fireEvent.click(await screen.findByRole("button", { name: "Restore" }));
  const dialog = within(screen.getByRole("dialog"));
  fireEvent.change(dialog.getByLabelText("Destination"), {
    target: { value: "same" },
  });
  const confirm = dialog.getByRole("button", { name: "Replace original app" });
  expect(confirm).toBeDisabled();
  fireEvent.change(dialog.getByLabelText(/Enter the app slug/), {
    target: { value: workspace.slug },
  });
  fireEvent.click(confirm);
  await waitFor(() =>
    expect(op).toHaveBeenCalledWith(
      "backup.restore",
      { backup: ready.id, same_app: workspace.slug },
      expect.any(String),
    ),
  );
});

it("restores a retained backup into a new app when the original was deleted", async () => {
  const data = snapshot();
  data.eligible_apps = [];
  const op = vi.spyOn(operations, "op").mockResolvedValue(data);
  render(<Backups />);
  fireEvent.click(await screen.findByRole("button", { name: "Restore" }));
  expect(screen.queryByRole("option", { name: "Original app" })).toBeNull();
  fireEvent.change(screen.getByLabelText("New app name"), {
    target: { value: "Recovered Crew" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Restore into new app" }));
  await waitFor(() =>
    expect(op).toHaveBeenCalledWith(
      "backup.restore",
      { backup: ready.id, new_name: "Recovered Crew" },
      expect.any(String),
    ),
  );
});

it("keeps download available with exhausted credits and uses the current owner token", async () => {
  const data = snapshot();
  data.pricing.available_microcredits = 0;
  vi.spyOn(operations, "op").mockResolvedValue(data);
  pb.authStore.save("fixture-token", {
    id: "owner001",
    collectionId: "_pb_users_auth_",
    collectionName: "users",
  });
  const fetcher = vi
    .fn()
    .mockResolvedValue(new Response(new Uint8Array([80, 75])));
  vi.stubGlobal("fetch", fetcher);
  vi.stubGlobal(
    "URL",
    class extends URL {
      static createObjectURL = vi.fn(() => "blob:fixture");
      static revokeObjectURL = vi.fn();
    },
  );
  const click = vi
    .spyOn(HTMLAnchorElement.prototype, "click")
    .mockImplementation(() => {});
  render(<Backups />);
  expect(await screen.findByLabelText("Daily backups")).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "Download ZIP" }));
  await waitFor(() => expect(click).toHaveBeenCalledOnce());
  expect(fetcher).toHaveBeenCalledWith(
    `/api/drop/backups/${ready.id}/download`,
    expect.objectContaining({
      headers: { Authorization: "Bearer fixture-token" },
    }),
  );
});

it("links from settings to the selected app and hides the toggle for unsupported releases", async () => {
  vi.spyOn(operations, "op").mockResolvedValue(snapshot());
  const { rerender } = render(<AppBackupSetting app={workspace.id} />);
  expect(await screen.findByLabelText("Daily backups")).toBeVisible();
  expect(screen.getByRole("link", { name: "Manage backups" })).toHaveAttribute(
    "href",
    `#/backups?app=${workspace.id}`,
  );
  rerender(<AppBackupSetting app="other" />);
  expect(screen.queryByLabelText("Daily backups")).toBeNull();
});
