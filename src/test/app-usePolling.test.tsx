/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { startPolling, usePolling } from "../app/usePolling";
import { mount } from "./features/reactTestUtils";

let hidden = false;
let focused = true;

function setHidden(value: boolean): void {
  hidden = value;
  document.dispatchEvent(new Event("visibilitychange"));
}

beforeEach(() => {
  vi.useFakeTimers();
  hidden = false;
  focused = true;
  Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
  vi.spyOn(document, "hasFocus").mockImplementation(() => focused);
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("startPolling", () => {
  it("runs immediately and then once per interval", async () => {
    const run = vi.fn();
    const stop = startPolling(run, { intervalMs: 1000 });
    expect(run).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(3000);
    expect(run).toHaveBeenCalledTimes(4);
    stop();
  });

  it("skips the immediate run when asked and still honours the interval", async () => {
    const run = vi.fn();
    const stop = startPolling(run, { intervalMs: 1000, immediate: false });
    expect(run).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1000);
    expect(run).toHaveBeenCalledTimes(1);
    stop();
  });

  it("does not poll while hidden and refreshes immediately when visible again", async () => {
    const run = vi.fn();
    const stop = startPolling(run, { intervalMs: 1000 });
    setHidden(true);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(run).toHaveBeenCalledTimes(1);

    setHidden(false);
    await vi.advanceTimersByTimeAsync(0);
    expect(run).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1000);
    expect(run).toHaveBeenCalledTimes(3);
    stop();
  });

  it("slows down while the window is unfocused and refreshes on focus", async () => {
    const run = vi.fn();
    const stop = startPolling(run, { intervalMs: 1000, blurredIntervalMs: 4000, immediate: false });
    focused = false;
    window.dispatchEvent(new Event("blur"));
    await vi.advanceTimersByTimeAsync(3999);
    expect(run).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(run).toHaveBeenCalledTimes(1);

    focused = true;
    await vi.advanceTimersByTimeAsync(2000);
    window.dispatchEvent(new Event("focus"));
    await vi.advanceTimersByTimeAsync(0);
    expect(run).toHaveBeenCalledTimes(2);
    stop();
  });

  it("never overlaps runs and waits an interval after a slow run finishes", async () => {
    let finish!: () => void;
    const run = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
    const stop = startPolling(run, { intervalMs: 1000 });
    await vi.advanceTimersByTimeAsync(10_000);
    expect(run).toHaveBeenCalledTimes(1);
    finish();
    await vi.advanceTimersByTimeAsync(999);
    expect(run).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(run).toHaveBeenCalledTimes(2);
    stop();
  });

  it("keeps polling after a run throws", async () => {
    const run = vi.fn(() => { throw new Error("boom"); });
    const stop = startPolling(run, { intervalMs: 1000 });
    await vi.advanceTimersByTimeAsync(2000);
    expect(run).toHaveBeenCalledTimes(3);
    stop();
  });

  it("stops completely after the returned stop function", async () => {
    const run = vi.fn();
    const stop = startPolling(run, { intervalMs: 1000 });
    stop();
    setHidden(true);
    setHidden(false);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(run).toHaveBeenCalledTimes(1);
  });
});

describe("usePolling", () => {
  function Probe({ run, enabled }: { run: () => void; enabled: boolean }) {
    usePolling(run, { intervalMs: 1000, enabled });
    return null;
  }

  it("follows enabled, calls the latest callback and cleans up on unmount", async () => {
    const first = vi.fn();
    const second = vi.fn();
    const view = mount(createElement(Probe, { run: first, enabled: false }));
    await vi.advanceTimersByTimeAsync(5000);
    expect(first).not.toHaveBeenCalled();

    act(() => view.root.render(createElement(Probe, { run: first, enabled: true })));
    expect(first).toHaveBeenCalledTimes(1);
    act(() => view.root.render(createElement(Probe, { run: second, enabled: true })));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(first).toHaveBeenCalledTimes(1);
    expect(second).toHaveBeenCalledTimes(1);

    view.unmount();
    await vi.advanceTimersByTimeAsync(5000);
    expect(second).toHaveBeenCalledTimes(1);
  });
});
