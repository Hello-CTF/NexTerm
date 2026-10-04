import { describe, expect, it, vi } from "vitest";
import {
  TerminalGridCoordinator,
  gridForViewport,
  type TerminalGrid,
  type TerminalGridRuntimeAdapter,
} from "../../features/terminal/terminalGrid";

const cells = { widthPx: 10, heightPx: 20 };

function deferred() {
  let resolve!: () => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<void>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

async function microtasks(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

describe("terminal grid measurement", () => {
  it.each([
    { width: 360, cols: 36 },
    { width: 560, cols: 56 },
    { width: 820, cols: 82 },
    { width: 1440, cols: 144 },
  ])("uses measured cells at $width px", ({ width, cols }) => {
    expect(gridForViewport({ widthPx: width, heightPx: 400 }, cells)).toEqual({
      cols,
      rows: 20,
    });
  });

  it("suppresses invalid geometry and follows the visible sub-cell contract", () => {
    expect(gridForViewport({ widthPx: 0, heightPx: 400 }, cells)).toBeNull();
    expect(gridForViewport({ widthPx: 360, heightPx: Number.NaN }, cells)).toBeNull();
    expect(gridForViewport({ widthPx: 360, heightPx: 400 }, { widthPx: 0, heightPx: 20 })).toBeNull();
    expect(gridForViewport({ widthPx: 5, heightPx: 10 }, cells)).toEqual({ cols: 1, rows: 1 });
  });
});

describe("terminal grid runtime lifecycle", () => {
  it("serializes intermediate intents and publishes only the latest pending grid", async () => {
    const calls: TerminalGrid[] = [];
    const completions: Array<() => void> = [];
    const runtime: TerminalGridRuntimeAdapter = {
      resize: (_tabId, grid) => {
        calls.push(grid);
        const pending = deferred();
        completions.push(pending.resolve);
        return pending.promise;
      },
    };
    const coordinator = new TerminalGridCoordinator(runtime, () => undefined, {
      visible: true,
      canResize: true,
    });
    coordinator.attach("tab", null, false);
    coordinator.update({ widthPx: 600, heightPx: 300 }, cells);
    coordinator.update({ widthPx: 700, heightPx: 320 }, cells);
    coordinator.update({ widthPx: 1000, heightPx: 400 }, cells);

    expect(calls).toEqual([{ cols: 60, rows: 15 }]);
    expect(coordinator.snapshot().pending).toBe(true);
    completions[0]();
    await microtasks();
    expect(calls).toEqual([
      { cols: 60, rows: 15 },
      { cols: 100, rows: 20 },
    ]);
    completions[1]();
    await microtasks();
    expect(coordinator.snapshot().committed).toEqual({ cols: 100, rows: 20 });
  });

  it("forces an accurate final resize and adapter flush even when dimensions are unchanged", async () => {
    const resize = vi.fn().mockResolvedValue(undefined);
    const flush = vi.fn().mockResolvedValue(undefined);
    const coordinator = new TerminalGridCoordinator({ resize, flush }, () => undefined, {
      visible: true,
      canResize: true,
    });
    coordinator.attach("tab", { cols: 80, rows: 20 }, false);
    coordinator.update({ widthPx: 800, heightPx: 400 }, cells);
    await microtasks();
    expect(resize).not.toHaveBeenCalled();

    coordinator.update({ widthPx: 800, heightPx: 400 }, cells);
    coordinator.flush();
    await microtasks();
    expect(resize).toHaveBeenCalledOnce();
    expect(resize).toHaveBeenCalledWith("tab", { cols: 80, rows: 20 });
    expect(flush).toHaveBeenCalledWith("tab");
  });

  it("suppresses hidden and sub-cell hidden submissions, then synchronizes on resume", async () => {
    const resize = vi.fn().mockResolvedValue(undefined);
    const coordinator = new TerminalGridCoordinator({ resize }, () => undefined, {
      visible: true,
      canResize: true,
    });
    coordinator.attach("tab", null, false);
    coordinator.update({ widthPx: 800, heightPx: 400 }, cells);
    await microtasks();
    expect(resize).toHaveBeenCalledTimes(1);

    coordinator.setVisible(false);
    coordinator.update({ widthPx: 5, heightPx: 10 }, cells);
    coordinator.flush();
    expect(resize).toHaveBeenCalledTimes(1);
    expect(coordinator.desiredGrid()).toEqual({ cols: 80, rows: 20 });

    coordinator.setVisible(true);
    coordinator.update({ widthPx: 800, heightPx: 400 }, cells);
    await microtasks();
    expect(resize).toHaveBeenCalledTimes(2);
    expect(resize).toHaveBeenLastCalledWith("tab", { cols: 80, rows: 20 });
  });

  it("recomputes on metric changes and fences a reconnect from the old completion", async () => {
    const completions = new Map<string, () => void>();
    const calls: string[] = [];
    const runtime: TerminalGridRuntimeAdapter = {
      resize: (tabId) => {
        calls.push(tabId);
        const pending = deferred();
        completions.set(tabId, pending.resolve);
        return pending.promise;
      },
    };
    const coordinator = new TerminalGridCoordinator(runtime, () => undefined, {
      visible: true,
      canResize: true,
    });
    coordinator.attach("old", null, false);
    coordinator.update({ widthPx: 800, heightPx: 400 }, cells);
    coordinator.attach("new", null, true);
    completions.get("old")?.();
    await microtasks();
    expect(calls).toEqual(["old", "new"]);
    expect(coordinator.snapshot().committed).toBeNull();

    completions.get("new")?.();
    await microtasks();
    expect(coordinator.snapshot().committed).toEqual({ cols: 80, rows: 20 });

    coordinator.update({ widthPx: 800, heightPx: 400 }, { widthPx: 20, heightPx: 20 });
    await microtasks();
    expect(coordinator.desiredGrid()).toEqual({ cols: 40, rows: 20 });
  });

  it("applies observer revisions without transport work and ignores stale grids", () => {
    const resize = vi.fn().mockResolvedValue(undefined);
    const local: TerminalGrid[] = [];
    const coordinator = new TerminalGridCoordinator(
      { resize },
      (grid) => local.push(grid),
      { visible: true, canResize: false },
    );
    coordinator.attach("tab", null, false);
    coordinator.update({ widthPx: 900, heightPx: 400 }, cells);
    expect(resize).not.toHaveBeenCalled();
    expect(local).toEqual([]);

    expect(coordinator.observe(2, { cols: 100, rows: 30 })).toBe(true);
    expect(coordinator.observe(1, { cols: 50, rows: 10 })).toBe(false);
    expect(coordinator.observe(2, { cols: 60, rows: 20 })).toBe(false);
    expect(coordinator.observe(2, { cols: 100, rows: 30 })).toBe(false);
    expect(local).toEqual([{ cols: 100, rows: 30 }]);

    coordinator.attach("replacement", null, false);
    expect(coordinator.observe(2, { cols: 100, rows: 30 })).toBe(true);
    coordinator.setCanResize(true);
    expect(local).toContainEqual({ cols: 90, rows: 20 });
    expect(resize).toHaveBeenCalledWith("replacement", { cols: 90, rows: 20 });
  });

  it("denies observer grids for the controller so its own resize echo never applies", () => {
    const resize = vi.fn().mockResolvedValue(undefined);
    const local: TerminalGrid[] = [];
    const coordinator = new TerminalGridCoordinator(
      { resize },
      (grid) => local.push(grid),
      { visible: true, canResize: true },
    );
    coordinator.attach("tab", null, false);
    coordinator.update({ widthPx: 800, heightPx: 400 }, cells);

    expect(coordinator.observe(3, { cols: 100, rows: 30 })).toBe(false);
    expect(coordinator.observe(1, { cols: 50, rows: 10 })).toBe(false);
    expect(local).toEqual([{ cols: 80, rows: 20 }]);
    expect(resize).toHaveBeenCalledTimes(1);
    expect(resize).toHaveBeenCalledWith("tab", { cols: 80, rows: 20 });
    expect(coordinator.snapshot().observedRevision).toBe(0);
  });

  it("does not commit a disconnected resize and finishes the final intent on reattach", async () => {
    const first = deferred();
    const second = deferred();
    const resize = vi.fn()
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise);
    const flush = vi.fn().mockResolvedValue(undefined);
    const coordinator = new TerminalGridCoordinator({ resize, flush }, () => undefined, {
      visible: true,
      canResize: true,
    });
    coordinator.attach("tab", { cols: 80, rows: 20 }, false);
    coordinator.update({ widthPx: 900, heightPx: 400 }, cells);
    coordinator.flush();

    first.reject({ code: "disconnected" });
    await microtasks();
    expect(coordinator.snapshot().committed).toEqual({ cols: 80, rows: 20 });
    expect(coordinator.desiredGrid()).toEqual({ cols: 90, rows: 20 });
    expect(flush).not.toHaveBeenCalled();

    second.resolve();
    await microtasks();
    expect(resize).toHaveBeenCalledTimes(2);
    expect(flush).toHaveBeenCalledWith("tab");
    expect(coordinator.snapshot().committed).toEqual({ cols: 90, rows: 20 });
  });

  it("drops pending work on unmount and ignores the late completion", async () => {
    const pending = deferred();
    const resize = vi.fn(() => pending.promise);
    const coordinator = new TerminalGridCoordinator({ resize }, () => undefined, {
      visible: true,
      canResize: true,
    });
    coordinator.attach("tab", null, false);
    coordinator.update({ widthPx: 800, heightPx: 400 }, cells);
    coordinator.close();
    coordinator.flush();
    pending.resolve();
    await microtasks();

    expect(resize).toHaveBeenCalledTimes(1);
    expect(coordinator.snapshot().closed).toBe(true);
    expect(coordinator.snapshot().committed).toBeNull();
    expect(coordinator.snapshot().pending).toBe(false);
  });
});
