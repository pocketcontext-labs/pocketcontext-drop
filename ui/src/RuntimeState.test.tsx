import { act, cleanup, render, screen } from "@testing-library/react";
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

it("refreshes a deployment quickly and returns to normal polling once running", async () => {
  vi.useFakeTimers();
  try {
    const read = vi.spyOn(dropApi, "appStatus")
      .mockResolvedValueOnce({ phase: "deploying" } as AppStatus)
      .mockResolvedValue({ phase: "running" } as AppStatus);
    await act(async () => { render(<RuntimeState id="crew" />); });
    expect(screen.getByText("Deploying")).toBeVisible();
    await act(() => vi.advanceTimersByTimeAsync(3000));
    expect(screen.getByText("Running")).toBeVisible();
    await act(() => vi.advanceTimersByTimeAsync(29999));
    expect(read).toHaveBeenCalledTimes(2);
    await act(() => vi.advanceTimersByTimeAsync(1));
    expect(read).toHaveBeenCalledTimes(3);
  } finally {
    vi.useRealTimers();
  }
});
