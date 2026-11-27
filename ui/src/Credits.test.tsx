import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import Credits from "./Credits";
import { op } from "./dropApi";

vi.mock("./dropApi", () => ({ op: vi.fn() }));
afterEach(() => { cleanup(); vi.clearAllMocks(); });

it("loads one account page and resets pagination for a new search", async () => {
  vi.mocked(op).mockImplementation(async (name, input) => {
    const page = input as { offset?: number; query?: string } | undefined;
    if (name === "credits.accounts") return {
      accounts: [{ id: "sample", email: "sample@example.test", purchases: 0, balance: 0 }],
      has_more: page?.offset === 0 && !page.query,
    };
    return { account: "self", state: { balance: 0, trial_seconds: 0 }, settings: {pack: 500, euro_micros_per_credit: 10000}, entries: [], apps: [] };
  });
  render(<Credits admin />);
  const next = screen.getByRole("button", {name: "Next"});
  await waitFor(() => expect(next).toBeEnabled());
  expect(vi.mocked(op).mock.calls.filter(([name]) => name === "credits.accounts")).toHaveLength(1);
  fireEvent.click(next);
  await waitFor(() => expect(op).toHaveBeenCalledWith("credits.accounts", { offset: 25, limit: 25, query: "" }));
  fireEvent.change(screen.getByRole("searchbox", { name: "Find an account" }), { target: { value: "sample@" } });
  await waitFor(() => expect(op).toHaveBeenCalledWith("credits.accounts", { offset: 0, limit: 25, query: "sample@" }));
});

it("pages through transaction history and restarts at the newest match after filtering", async () => {
  vi.mocked(op).mockImplementation(async (_name, input) => {
    const { before } = input as { before: number };
    return { account: "self", state: {balance: 0, trial_seconds: 0}, apps: [],
      settings: {pack: 500, euro_micros_per_credit: 10000}, has_more: !before,
      entries: [{id: before ? 8 : 9, kind: "compute", delta: -1000, app: "", created: 1000}] };
  });
  render(<Credits admin={false} />);
  fireEvent.click(await screen.findByText("Transaction history"));
  fireEvent.click(screen.getByRole("button", {name: "Older transactions"}));
  await waitFor(() => expect(op).toHaveBeenCalledWith("credits.get", {account: "", before: 9, limit: 25, query: "", kind: ""}));
  fireEvent.change(screen.getByLabelText("Entry type"), {target: {value: "compute"}});
  fireEvent.change(screen.getByLabelText("Find transactions"), {target: {value: "Atlas"}});
  await waitFor(() => expect(op).toHaveBeenCalledWith("credits.get", {account: "", before: 0, limit: 25, query: "Atlas", kind: "compute"}));
});
