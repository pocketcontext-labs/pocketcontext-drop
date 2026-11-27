import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import RuntimeState from "./RuntimeState";
import { dropApi, type AppStatus } from "./dropApi";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
it.each([
  ["sleeping", "suspend", "Suspended"],
  ["sleeping", "terminate", "Terminated"],
  ["running", "terminate", "Running"],
  ["unknown", "terminate", "Status unavailable"],
])("shows %s with last hook %s as %s without probing the guest", async (phase, event, text) => {
  const read = vi.spyOn(dropApi, "appStatus").mockResolvedValue({ phase, last_hook: { event } } as AppStatus);
  render(<RuntimeState id="crew" />);
  expect(await screen.findByText(text)).toBeVisible();
  expect(read).toHaveBeenCalledWith("crew");
});
