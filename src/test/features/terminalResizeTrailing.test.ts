import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  TERMINAL_RESIZE_MAX_WAIT_MS,
  TERMINAL_RESIZE_TRAILING_MS,
  TerminalGridCoordinator,
  type TerminalGrid,
} from "../../features/terminal/terminalGrid";

const cells = { widthPx: 10, heightPx: 20 };

async function microtasks(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

function setup() {
  const calls: TerminalGrid[] = [];
  const flushes: string[] = [];
  const resize = vi.fn((_tabId: string, grid: TerminalGrid) => {
    calls.push(grid);
    return Promise.resolve();
  });
  const flush = vi.fn((tabId: string) => {
    flushes.push(tabId);
    return Promise.resolve();
  });
  const local: TerminalGrid[] = [];
  const coordinator = new TerminalGridCoordinator(
    { resize, flush },
    (grid) => local.push(grid),
    { visible: true, canResize: true },
  );
  coordinator.attach("tab", null, false);
  return { calls, flushes, local, resize, flush, coordinator };
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("terminal grid trailing remote resize", () => {
  it("fits the local grid immediately while the remote resize trails", () => {
    const { calls, local, coordinator } = setup();

    coordinator.update({ widthPx: 600, heightPx: 300 }, cells);
    expect(local).toEqual([{ cols: 60, rows: 15 }]);
    expect(calls).toEqual([]);

    vi.advanceTimersByTime(TERMINAL_RESIZE_TRAILING_MS);
    expect(calls).toEqual([{ cols: 60, rows: 15 }]);
  });

  it("bounds a continuous drag storm by the max wait and still lands the exact final grid", async () => {
    const { calls, flushes, coordinator } = setup();

    for (let i = 0; i < 12; i += 1) {
      coordinator.update({ widthPx: 600 + i * 10, heightPx: 400 }, cells);
      vi.advanceTimersByTime(100);
      await microtasks();
    }
    expect(calls.length).toBeLessThanOrEqual(3);
    expect(calls.length).toBeGreaterThanOrEqual(1);
    expect(calls[calls.length - 1]).toEqual({ cols: 71, rows: 20 });

    coordinator.flush();
    await microtasks();
    expect(calls[calls.length - 1]).toEqual({ cols: 71, rows: 20 });
    expect(flushes).toEqual(["tab"]);
  });

  it("cancels the trailing timer when the final flush lands", async () => {
    const { calls, flushes, resize, flush, coordinator } = setup();

    coordinator.update({ widthPx: 600, heightPx: 300 }, cells);
    expect(resize).not.toHaveBeenCalled();

    coordinator.flush();
    await microtasks();
    expect(resize).toHaveBeenCalledExactlyOnceWith("tab", { cols: 60, rows: 15 });
    expect(flush).toHaveBeenCalledExactlyOnceWith("tab");

    vi.advanceTimersByTime(TERMINAL_RESIZE_MAX_WAIT_MS * 2);
    expect(calls).toEqual([{ cols: 60, rows: 15 }]);
    expect(flushes).toEqual(["tab"]);
  });

  it("lets forced measurements and attach synchronization bypass the trailing window", async () => {
    const { calls, coordinator } = setup();

    coordinator.update({ widthPx: 600, heightPx: 300 }, cells);
    coordinator.attach("tab-2", null, true);
    await microtasks();
    expect(calls).toEqual([{ cols: 60, rows: 15 }]);

    vi.advanceTimersByTime(TERMINAL_RESIZE_MAX_WAIT_MS * 2);
    expect(calls).toEqual([{ cols: 60, rows: 15 }]);

    coordinator.setVisible(false);
    coordinator.setVisible(true);
    coordinator.update({ widthPx: 700, heightPx: 300 }, cells);
    await microtasks();
    expect(calls).toEqual([
      { cols: 60, rows: 15 },
      { cols: 70, rows: 15 },
    ]);
  });

  it("drops an armed trailing timer on close without touching the runtime", () => {
    const { calls, coordinator } = setup();

    coordinator.update({ widthPx: 600, heightPx: 300 }, cells);
    coordinator.close();

    vi.advanceTimersByTime(TERMINAL_RESIZE_MAX_WAIT_MS * 2);
    expect(calls).toEqual([]);
    expect(coordinator.snapshot().debouncing).toBe(false);
  });
});
