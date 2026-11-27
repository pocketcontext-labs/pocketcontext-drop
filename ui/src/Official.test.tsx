import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { CreateWorkspace, OfficialCatalog, OfficialReleases } from "./Official";
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
  expect(screen.getByRole("option", { name: "1× · 0.25 vCPU · 256 MiB RAM · 2 GiB disk" })).toBeInTheDocument();
  expect(screen.getByRole("option", { name: "8× · 2 vCPU · 2048 MiB RAM · 16 GiB disk" })).toBeInTheDocument();
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
  fireEvent.change(screen.getByLabelText("Role"), { target: { value: "developer" } });
  await waitFor(() => expect(ops).toHaveBeenCalledWith(
    "user.directory", expect.objectContaining({ role: "developer", offset: 0 }),
  ));
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
  const resources = screen.getByLabelText("Resources");
  expect(resources.querySelectorAll("option")).toHaveLength(8);
  fireEvent.change(resources, {target: {value: "1"}});
  fireEvent.click(screen.getByRole("button", { name: "Create app" }));
  await waitFor(() => expect(ops).toHaveBeenCalledWith("workspace.create",
    { product: "crew", name: "My Crew", multiplier: 1, sha256: "older", allow_archived: true }, expect.any(String)));
});

it("offers both active Crew versions and pins the older selection", async () => {
  const ops = vi.spyOn(api, "op").mockResolvedValueOnce({ enabled: true, releases: [
    { product: "crew", version: "0.45.4-drop.5", sha256: "older", published: 1 },
    { product: "crew", version: "0.45.4-drop.6", sha256: "latest", published: 2 },
  ] }).mockResolvedValueOnce({ app: { id: "workspace000001" } });
  render(<OfficialCatalog />);
  fireEvent.click(await screen.findByRole("button", { name: "Choose Crew" }));
  const release = screen.getByLabelText("Release");
  expect(release).toHaveValue("latest");
  expect(release.querySelectorAll("option")).toHaveLength(2);
  fireEvent.change(release, { target: { value: "older" } });
  fireEvent.click(screen.getByRole("button", { name: "Create app" }));
  await waitFor(() => expect(ops).toHaveBeenCalledWith("workspace.create",
    expect.objectContaining({ sha256: "older" }), expect.any(String)));
});

it("stages the exact submitted digest before catalog publication", async () => {
  const candidate = { app: "private1", release: "release1", product: "crew", version: "1.2", sha256: "a".repeat(64), submitted_by: "account1", size_bytes: 1024 };
  let accepted = false;
  const ops = vi.spyOn(api, "op").mockImplementation(async name => {
    if (name === "catalog.submissions") return { submissions: accepted ? [] : [candidate] };
    if (name === "catalog.accept") {
      accepted = true;
      return candidate;
    }
    if (name === "catalog.list") return { enabled: true, releases: accepted ? [{ ...candidate, published: 0 }] : [] };
    throw new Error("Unexpected operation " + name);
  });
  render(<OfficialReleases />);
  expect(await screen.findByText("sha256: " + candidate.sha256)).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "Accept for rehearsal" }));
  await waitFor(() => expect(ops).toHaveBeenCalledWith("catalog.accept", {
    app: candidate.app, release: candidate.release, sha256: candidate.sha256,
  }));
  expect(await screen.findByRole("button", { name: "Publish release" })).toBeDisabled();
  expect(screen.queryByText("Submitted for review")).toBeNull();
});

it("invites an account directly as a developer", async () => {
  vi.spyOn(api, "op").mockResolvedValue({ users: [], total: 0 });
  const invite = vi.spyOn(api.dropApi, "createInvite").mockResolvedValue({
    invite: { id: "inv000000000001", email: "dev@example.test", role: "developer", app_quota: 3, state: "open", invited_by: "owner", created: "", expires_at: "", used_by: "", used_at: "", revoked_at: "" },
    sign_in_url: "https://drop.example.test/#/invite",
  });
  render(<UserDirectory me={{ hosted: true, id: "owner", email: "admin@example.test", role: "admin", admin: true, owner: true, app_quota: 3 }} />);
  fireEvent.click(screen.getByRole("button", { name: "Invite someone" }));
  fireEvent.change(screen.getByLabelText("Email"), { target: { value: "dev@example.test" } });
  fireEvent.change(screen.getByLabelText("Invitation role"), { target: { value: "developer" } });
  fireEvent.click(screen.getByRole("button", { name: "Create invitation" }));
  await waitFor(() => expect(invite).toHaveBeenCalledWith({ email: "dev@example.test", role: "developer" }));
});


it("checks an uploaded executable before enabling private creation", async () => {
  const ops = vi.spyOn(api, "op").mockResolvedValue({ path: "/api/drop/upload/fixture" });
  const file = new File(["ELF fixture"], "tool");
  const put = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({
    product: "third-product", version: "0.1", sha256: "f".repeat(64), minimum_multiplier: 2,
  })));
  render(<CreateWorkspace privateBuild />);
  fireEvent.change(screen.getByLabelText("Linux executable"), {target: { files: [file] }});
  expect(await screen.findByText("Third Product · 0.1")).toBeVisible();
  expect(ops).toHaveBeenCalledWith("developer.build_upload", {});
  expect(put).toHaveBeenCalledWith("/api/drop/upload/fixture", {method: "PUT", body: file, credentials: "omit", redirect: "error"});
  expect(screen.getByRole("button", {name:"Create app"})).toBeEnabled();
  fireEvent.change(screen.getByLabelText("Source"), {target: {value: "url"}});
  expect(screen.getByRole("button", {name:"Create app"})).toBeDisabled();
});

it("lets the admin assign a new product's prefix at publication", async () => {
  const ops = vi.spyOn(api, "op").mockImplementation(async name => name === "catalog.list" ? {
    releases: [{product: "my-tool", version: "1", size_bytes: 100, sha256: "abc", rehearsed: true}],
  } : {submissions: []});
  render(<OfficialReleases />);
  const prefix = await screen.findByLabelText("Catalog prefix");
  const publish = screen.getByRole("button", {name:"Publish release"});
  fireEvent.change(prefix, {target: {value: "dev-labs"}});
  expect(publish).toBeDisabled();
  fireEvent.change(prefix, {target: {value: "tool"}});
  fireEvent.click(publish);
  await waitFor(() => expect(ops).toHaveBeenCalledWith("catalog.publish", {sha256: "abc", prefix: "tool"}));
});

it("shows published products beyond Crew and Atlas in the catalog", async () => {
  vi.spyOn(api, "op").mockResolvedValue({enabled: true, releases: [
    {product: "new-product", version: "2.0", sha256: "abc", published: 2, minimum_multiplier: 2},
  ]});
  render(<OfficialCatalog />);
  fireEvent.click(await screen.findByRole("button", {name: "Choose New Product"}));
  expect(screen.getByLabelText("App name")).toHaveValue("My New Product");
  expect(screen.getByLabelText("Release")).toHaveValue("abc");
  expect(screen.getByLabelText("Resources").querySelectorAll("option")).toHaveLength(7);
});

it("stages an administrator's HTTPS release URL for rehearsal", async () => {
  const ops = vi.spyOn(api, "op").mockImplementation(async name =>
    name === "catalog.list" ? { enabled: true, releases: [] } : { submissions: [] });
  render(<OfficialReleases />);
  const url = "https://releases.example.test/crew.tar.gz";
  fireEvent.change(screen.getByLabelText("Source"), { target: { value: "url" } });
  fireEvent.change(screen.getByLabelText("Release URL"), { target: { value: url } });
  fireEvent.change(screen.getByLabelText("Version"), { target: { value: "1.2" } });
  fireEvent.click(screen.getByRole("button", { name: "Download and check" }));
  expect(await screen.findByText(/Manifest checked/)).toBeVisible();
  expect(ops).toHaveBeenCalledWith("catalog.import_url", { product: "crew", version: "1.2", url });
});
