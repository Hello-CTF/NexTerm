/** @vitest-environment jsdom */

import { describe, expect, it, vi } from "vitest";
import { act } from "react";
import { harness } from "./lib/terminalViewHarness";
import { attachOk, feedBytes, feedControl, installTerminalViewHooks, show, term } from "./lib/terminalViewDriver";
import type { TerminalHandle } from "../features/terminal/XtermView";

vi.mock("@xterm/xterm", async () => (await import("./lib/terminalViewHarness")).xtermModule());
vi.mock("@xterm/addon-webgl", async () => (await import("./lib/terminalViewHarness")).webglModule());
vi.mock("@xterm/addon-search", async () => (await import("./lib/terminalViewHarness")).searchModule());
vi.mock("../features/terminal/commandBlocks", async () => (await import("./lib/terminalViewHarness")).commandBlocksModule());
vi.mock("../features/terminal/terminalGeometry", async () => (await import("./lib/terminalViewHarness")).terminalGeometryModule());
vi.mock("../features/terminal/gridRuntimeAdapter", async () => (await import("./lib/terminalViewHarness")).gridRuntimeModule());
vi.mock("../ipc/commands", async () => (await import("./lib/terminalViewHarness")).commandsModule());
vi.mock("../ipc/events", async () => (await import("./lib/terminalViewHarness")).eventsModule());
vi.mock("../ipc/env", async () => (await import("./lib/terminalViewHarness")).envModule());
vi.mock("../ipc/wails", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ipc/wails")>()),
  openExternalURL: vi.fn(),
}));
vi.mock("../app/keybindings", async () => (await import("./lib/terminalViewHarness")).keybindingsModule());
vi.mock("../app/store", async () => (await import("./lib/terminalViewHarness")).storeModule());
vi.mock("../app/preferences", async () => (await import("./lib/terminalViewHarness")).preferencesModule());
vi.mock("../ui/clipboard", async () => (await import("./lib/terminalViewHarness")).clipboardModule());

// The fake terminal has no buffer to move a cursor over: copy mode is a switch the test flips.
const copyState = vi.hoisted(() => ({ active: false }));
vi.mock("../features/terminal/copyMode", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../features/terminal/copyMode")>()),
  createCopyMode: () => ({
    state: () => ({ active: copyState.active }),
    subscribe: () => () => undefined,
    isActive: () => copyState.active,
    enter: () => undefined,
    exit: () => {
      copyState.active = false;
    },
    dispose: () => undefined,
  }),
}));
vi.mock("../features/terminal/CopyModeOverlay", () => ({ CopyModeOverlay: () => null }));

installTerminalViewHooks();

function upstream(): string[] {
  return harness.write.mock.calls.map(([, bytes]) => new TextDecoder().decode(bytes as Uint8Array));
}

describe("XtermView input while copy mode is active", () => {
  it("withholds typed input and pastes, but still answers terminal queries", async () => {
    let handle: TerminalHandle | undefined;
    harness.emitReplies = true;
    copyState.active = false;
    await show({ onHandle: (h: TerminalHandle) => (handle = h) });
    await attachOk();
    const t = term();
    copyState.active = true;
    act(() => t.handlers.data?.("ls\r"));
    act(() => handle?.paste("echo hi", true));
    expect(upstream()).toEqual([]);
    expect(handle?.copyModeActive?.()).toBe(true);
    // A DA1 query parsed outside any DOM event is answered even while copy mode is open.
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    feedBytes("\x1b[c");
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["\x1b[?1;2c"]);
    copyState.active = false;
    act(() => handle?.paste("echo hi", true));
    expect(upstream().length).toBe(2);
  });
});
