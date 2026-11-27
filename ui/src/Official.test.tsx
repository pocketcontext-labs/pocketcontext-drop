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
it("offers the same resource units for Atlas and creates the named app", async () => {
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
  expect(size.querySelectorAll("option")).toHaveLength(8);
  fireEvent.change(size, { target: { value: "8" } });
  fireEvent.change(screen.getByLabelText("App name"), {
    target: { value: "Research" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Create app" }));
  await waitFor(() =>
    expect(ops).toHaveBeenCalledWith(
      "workspace.create",
      { product: "atlas", name: "Research", multiplier: 8, sha256: "abc" },
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
    await screen.findByRole("columnheader", { name: "Apps" }),
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

it("lets administrators copy the full digest and archive a published release", async () => {
  const digest = "12345678" + "a".repeat(49) + "abcdef0";
  const ops = vi.spyOn(api, "op").mockResolvedValue({ enabled: true, releases: [
    {product: "atlas", version: "1.0", sha256: digest, published: 1, size_bytes: 1048576},
  ] });
  const write = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", Object.assign(Object.create(navigator), { clipboard: {writeText:write} }));
  vi.spyOn(window, "confirm").mockReturnValue(true);
  try {
    render(<OfficialReleases />);
    expect(await screen.findByText("sha256: 12345678…abcdef0")).toHaveAttribute("title", digest);
    fireEvent.click(screen.getByRole("button", {name: "Copy SHA-256 for Atlas 1.0"}));
    expect(write).toHaveBeenCalledWith(digest);
    fireEvent.click(screen.getByRole("button", {name:"Archive release"}));
    await waitFor(() => expect(ops).toHaveBeenCalledWith("catalog.archive", {sha256:digest}));
  } finally {
    vi.unstubAllGlobals();
  }
});

it("deploys an archived version only after the user enables and selects it", async () => {
  const ops = vi.spyOn(api, "op").mockResolvedValueOnce({ enabled: true, releases: [
    { product: "crew", version: "2", sha256: "current", published: 2 },
    { product: "crew", version: "1", sha256: "older", published: 1, archived: true, minimum_multiplier: 6 },
  ] }).mockResolvedValueOnce({ app: { id: "workspace000001" } });
  render(<OfficialCatalog />);
  fireEvent.click(await screen.findByRole("button", { name: "Choose Crew" }));
  const release = screen.getByLabelText("Release");
  expect(release).toHaveValue("current");
  expect(screen.queryByRole("option", { name: "1 (archived)" })).toBeNull();
  fireEvent.click(screen.getByLabelText("Include archived releases"));
  fireEvent.change(release, { target: { value: "older" } });
  expect(screen.getByLabelText("Resources")).toHaveValue("6");
  fireEvent.click(screen.getByRole("button", { name: "Create app" }));
  await waitFor(() => expect(ops).toHaveBeenCalledWith("workspace.create",
    { product: "crew", name: "My Crew", multiplier: 6, sha256: "older", allow_archived: true }, expect.any(String)));
});
