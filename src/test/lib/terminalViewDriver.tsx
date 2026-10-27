import { afterEach, beforeEach, vi } from "vitest";
import { Profiler, act, createElement } from "react";
import { flushMacrotask, mount, type MountedView } from "../features/reactTestUtils";
import { XtermView, type XtermViewProps } from "../../features/terminal/XtermView";
import { resetTerminalView } from "../../app/terminalViewStore";
import { harness, type FakeTerminal } from "./terminalViewHarness";

export const enc = new TextEncoder();

export const ctx: { mounted: MountedView | undefined; commits: number } = { mounted: undefined, commits: 0 };
let frameId = 0;
let frames = new Map<number, () => void>();

export function runFrames() {
  const queued = [...frames.values()];
  frames.clear();
  for (const callback of queued) callback();
}

export function term(): FakeTerminal {
  const t = harness.terminals[0] as FakeTerminal | undefined;
  if (!t) throw new Error("no fake terminal");
  return t;
}

export function buttonText(): string {
  return [...(ctx.mounted?.container.querySelectorAll("button") ?? [])]
    .map((b) => b.textContent ?? "")
    .join(" ");
}

export function buttonOccurrences(text: string): number {
  return [...(ctx.mounted?.container.querySelectorAll("button") ?? [])]
    .map((b) => b.textContent ?? "")
    .filter((t) => t.includes(text)).length;
}

// Counts every render of the XtermView element driven from above: with the view state
// in the root store the component has no own subscriptions, so this stays flat across
// output bursts — only the badge's precise selector re-renders.
export const xtermViewRenders = new Map<string, number>();

export function CountingXtermView(props: XtermViewProps) {
  xtermViewRenders.set(props.tabId, (xtermViewRenders.get(props.tabId) ?? 0) + 1);
  return createElement(XtermView, props);
}

export function stubFrames() {
  frames = new Map();
  frameId = 0;
  vi.stubGlobal("requestAnimationFrame", (callback: () => void) => {
    const id = ++frameId;
    frames.set(id, callback);
    return id;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => frames.delete(id));
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    disconnect() {}
  });
}

export async function show(props: Record<string, unknown> = {}) {
  stubFrames();
  ctx.commits = 0;
  ctx.mounted = mount(
    createElement(
      Profiler,
      { id: "xterm-view", onRender: () => { ctx.commits += 1; } },
      createElement(CountingXtermView, { sessionId: "session-1", tabId: "tab-1", ...props }),
    ),
  );
  await act(async () => {
    await flushMacrotask();
    await flushMacrotask();
  });
}

export async function attachOk() {
  await act(async () => {
    await flushMacrotask();
  });
}

export function feedBytes(data: string) {
  act(() => {
    harness.channel?.onBytes?.(enc.encode(data));
  });
}

export function feedControl(control: Record<string, unknown>) {
  act(() => {
    harness.channel?.onControl?.(control);
  });
}

// Registers the shared per-test setup: reset the fake terminal harness and default IPC mock results.
export function installTerminalViewHooks(): void {

  beforeEach(() => {
    vi.clearAllMocks();
    xtermViewRenders.clear();
    act(() => resetTerminalView());
    harness.terminals.length = 0;
    harness.channel = null;
    harness.channels.length = 0;
    harness.reopenCb = null;
    harness.emitReplies = false;
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: true,
      ephemeral: false,
      attachmentId: 11,
    });
    harness.attachTab.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      retainable: true,
      ephemeral: false,
      attachmentId: 11,
    });
    harness.detach.mockResolvedValue(undefined);
    harness.setVisible.mockResolvedValue(undefined);
    harness.write.mockResolvedValue(undefined);
    harness.call.mockReset().mockImplementation((command: string) => {
      if (command === "terminal_pause") return Promise.resolve({ episodeToken: 31 });
      if (command === "terminal_recover") return Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 31 });
      return Promise.resolve(undefined);
    });
    harness.execAttach.mockReset().mockResolvedValue("kernel-1");
    harness.openLineTab.mockReset().mockResolvedValue({
      tabId: "kernel-1",
      sessionId: "session-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 7,
    });
  });

  afterEach(() => {
    ctx.mounted?.unmount();
    ctx.mounted = undefined;
    vi.unstubAllGlobals();
  });

}
