import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TerminalGridCoordinator, type TerminalGrid } from "../../features/terminal/terminalGrid";

const cells = { widthPx: 10, heightPx: 20 };
const desktop = { widthPx: 1000, heightPx: 600 };
const desktopGrid: TerminalGrid = { cols: 100, rows: 30 };
const phoneGrid: TerminalGrid = { cols: 40, rows: 20 };

async function microtasks(): Promise<void> {
  for (let i = 0; i < 6; i += 1) await Promise.resolve();
}

function setup(canResize: boolean) {
  const calls: TerminalGrid[] = [];
  const resize = vi.fn((_tabId: string, grid: TerminalGrid) => {
    calls.push(grid);
    return Promise.resolve();
  });
  const coordinator = new TerminalGridCoordinator({ resize }, () => undefined, { visible: true, canResize });
  coordinator.attach("tab", null, false);
  return { calls, coordinator };
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("terminal input grid consistency check", () => {
  it("syncs the local grid on input when the PTY grid differs, once per observed PTY state", async () => {
    const { calls, coordinator } = setup(true);
    coordinator.update(desktop, cells);
    vi.advanceTimersByTime(1000);
    await microtasks();
    expect(calls).toEqual([desktopGrid]);

    // Something else left the PTY at the phone's size without this view hearing about a resize.
    coordinator.observe(5, phoneGrid);
    coordinator.noteInput();
    await microtasks();
    expect(calls).toEqual([desktopGrid, desktopGrid]);

    // More keystrokes while the PTY state is unchanged send nothing.
    coordinator.noteInput();
    coordinator.noteInput();
    await microtasks();
    expect(calls).toHaveLength(2);

    // The echo of our own resize agrees with the local grid: still nothing to send.
    coordinator.observe(6, desktopGrid);
    coordinator.noteInput();
    await microtasks();
    expect(calls).toHaveLength(2);

    // A later divergence is corrected again.
    coordinator.observe(7, phoneGrid);
    coordinator.noteInput();
    await microtasks();
    expect(calls).toHaveLength(3);
  });

  it("sends nothing on input when the PTY already has the local grid", async () => {
    const { calls, coordinator } = setup(true);
    coordinator.update(desktop, cells);
    vi.advanceTimersByTime(1000);
    await microtasks();
    coordinator.observe(3, desktopGrid);
    calls.length = 0;
    coordinator.noteInput();
    await microtasks();
    expect(calls).toEqual([]);
  });

  it("does not add a request while a resize is already on its way", async () => {
    const { calls, coordinator } = setup(true);
    coordinator.observe(2, phoneGrid);
    coordinator.update(desktop, cells);
    coordinator.noteInput();
    await microtasks();
    expect(calls).toEqual([]);
    vi.advanceTimersByTime(1000);
    await microtasks();
    expect(calls).toEqual([desktopGrid]);
  });

  it("leaves a follower alone on input and sends its grid at once when it takes the size", async () => {
    const { calls, coordinator } = setup(false);
    coordinator.update(desktop, cells);
    coordinator.observe(4, phoneGrid);
    coordinator.noteInput();
    await microtasks();
    expect(calls).toEqual([]);

    coordinator.setCanResize(true);
    await microtasks();
    expect(calls).toEqual([desktopGrid]);
  });

  it("still synchronizes on input after the PTY grid was seen while following", async () => {
    const { calls, coordinator } = setup(false);
    coordinator.update(desktop, cells);
    coordinator.observe(4, phoneGrid);
    coordinator.setCanResize(true);
    await microtasks();
    expect(calls).toEqual([desktopGrid]);
    // The backend echoes the new PTY size; a later phone resize that this view missed is fixed on input.
    coordinator.observe(5, desktopGrid);
    coordinator.observe(6, phoneGrid);
    coordinator.noteInput();
    await microtasks();
    expect(calls).toEqual([desktopGrid, desktopGrid]);
  });
});
