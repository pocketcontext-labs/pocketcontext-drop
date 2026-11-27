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
  fireEvent.change(screen.getByRole("searchbox"), { target: { value: "sample@" } });
  await waitFor(() => expect(op).toHaveBeenCalledWith("credits.accounts", { offset: 0, limit: 25, query: "sample@" }));
});
