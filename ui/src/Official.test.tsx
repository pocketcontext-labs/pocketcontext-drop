import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { OfficialCatalog, OfficialReleases } from "./Official";
import UserDirectory from "./UserDirectory";
import DeleteSlider from "./DeleteSlider";
import * as api from "./dropApi";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  history.replaceState(null, "", "/");
});
it("offers the same resource units for Atlas and creates the named workspace", async () => {
  const ops = vi
    .spyOn(api, "op")
    .mockResolvedValueOnce({
      enabled: true,
      releases: [
        { product: "atlas", version: "1.2", sha256: "abc", published: 1 },
      ],
    })
    .mockResolvedValueOnce({ app: { id: "workspace000001" } });
  render(<OfficialCatalog />);
  fireEvent.click(await screen.findByRole("button", { name: "Choose Atlas" }));
  const size = screen.getByLabelText("Resources");
  expect(size).toHaveValue("4");
  expect(screen.getAllByRole("option")).toHaveLength(8);
  fireEvent.change(size, { target: { value: "8" } });
  fireEvent.change(screen.getByLabelText("Workspace name"), {
    target: { value: "Research" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
  await waitFor(() =>
    expect(ops).toHaveBeenCalledWith(
      "workspace.create",
      { product: "atlas", name: "Research", multiplier: 8 },
      expect.any(String),
    ),
  );
});
it("requires baseline rehearsal evidence before showing a usable publish action", async () => {
  vi.spyOn(api, "op").mockResolvedValue({
    enabled: true,
    releases: [
      {
        product: "crew",
        version: "1.2",
        sha256: "abc",
        published: 0,
        size_bytes: 100,
      },
    ],
  });
  render(<OfficialReleases />);
  expect(
    await screen.findByRole("button", { name: "Publish release" }),
  ).toBeDisabled();
  expect(screen.getByRole("heading", { name: "Releases" })).toBeVisible();
});
it("keeps the combined directory paginated and sends selected filters to the server", async () => {
  const ops = vi
    .spyOn(api, "op")
    .mockResolvedValue({
      users: [
        {
          account: "a",
          entry: "",
          invite: "",
          email: "a@example.test",
          role: "user",
          status: "active",
          workspaces: 2,
          microcredits: 52000000,
          joined: "2026-11-26 10:00:00.000Z",
        },
      ],
      total: 5000,
    });
  render(
    <UserDirectory
      me={{
        hosted: true,
        id: "owner",
        email: "owner@example.test",
        role: "admin",
        admin: true,
        owner: true,
        app_quota: 3,
      }}
    />,
  );
  expect(
    await screen.findByRole("columnheader", { name: "Workspaces" }),
  ).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "Next" }));
  await waitFor(() =>
    expect(ops).toHaveBeenCalledWith(
      "user.directory",
      expect.objectContaining({ offset: 25, limit: 25 }),
    ),
  );
  fireEvent.change(screen.getByLabelText("Status"), {
    target: { value: "waiting" },
  });
  await waitFor(() =>
    expect(ops).toHaveBeenCalledWith(
      "user.directory",
      expect.objectContaining({ status: "waiting", offset: 0 }),
    ),
  );
});
it("requires completion before keyboard confirmation and ignores track clicks", () => {
  const confirm = vi.fn();
  render(<DeleteSlider disabled={false} onConfirm={confirm} />);
  const slider = screen.getByRole("slider");
  fireEvent.click(slider);
  fireEvent.keyDown(slider, { key: "ArrowRight" });
  fireEvent.keyDown(slider, { key: "Enter" });
  expect(confirm).not.toHaveBeenCalled();
  for (let i = 1; i < 10; i++) fireEvent.keyDown(slider, { key: "ArrowRight" });
  fireEvent.keyDown(slider, { key: "Enter" });
  expect(confirm).toHaveBeenCalledTimes(1);
});
