import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import AppAddress from "./AppAddress";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("keeps the address available for manual copying when the clipboard is unavailable", async () => {
  vi.stubGlobal("navigator", { clipboard: undefined });
  render(<AppAddress hostname="crew.apps.example.test" />);
  fireEvent.click(screen.getByRole("button", {name: "Copy app URL"}));
  expect(await screen.findByRole("status")).toHaveTextContent("Select and copy");
  expect(screen.getByRole("link")).toHaveTextContent("https://crew.apps.example.test");
});

it("omits links and copy actions for missing or malformed hostnames", () => {
  const { rerender } = render(<AppAddress />);
  for (const hostname of ["", "example.test:8000", "https://example.test/path", "evil@other.test"]) {
    rerender(<AppAddress hostname={hostname} />);
    expect(screen.queryByRole("link")).toBeNull();
    expect(screen.queryByRole("button")).toBeNull();
  }
});
