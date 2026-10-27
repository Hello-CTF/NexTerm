/** @vitest-environment jsdom */

import { describe, expect, it, vi } from "vitest";
import { Profiler, act, createElement } from "react";
import { deferred, flushMacrotask, mount } from "./features/reactTestUtils";
import { harness, type FakeTerminal } from "./lib/terminalViewHarness";
import { CountingXtermView, attachOk, buttonOccurrences, buttonText, ctx, enc, feedBytes, feedControl, installTerminalViewHooks, runFrames, show, stubFrames, term, xtermViewRenders } from "./lib/terminalViewDriver";
import { getTerminalLinesBelow, useTerminalViewStore } from "../app/terminalViewStore";
import { XtermView } from "../features/terminal/XtermView";

vi.mock("@xterm/xterm", async () => (await import("./lib/terminalViewHarness")).xtermModule());
vi.mock("@xterm/addon-webgl", async () => (await import("./lib/terminalViewHarness")).webglModule());
vi.mock("@xterm/addon-search", async () => (await import("./lib/terminalViewHarness")).searchModule());
vi.mock("@xterm/addon-web-links", async () => (await import("./lib/terminalViewHarness")).webLinksModule());
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

installTerminalViewHooks();

describe("XtermView render coalescing (T-09 component portion)", () => {
  it("coalesces scroll-driven linesBelow updates into one render per frame", async () => {
    await show();
    await attachOk();
    const t = term();
    t.buffer.active.baseY = 100;
    const before = ctx.commits;
    const rendersBefore = xtermViewRenders.get("tab-1") ?? 0;
    for (const y of [10, 20, 30, 40, 50]) {
      t.buffer.active.viewportY = y;
      act(() => t.handlers.scroll?.());
    }
    expect(ctx.commits).toBe(before);
    act(() => runFrames());
    expect(ctx.commits).toBe(before + 1);
    expect(buttonText()).toContain("50 行");
    // The coalesced value lands in the root view store, and the burst re-renders
    // only the badge's precise selector — never the XtermView element above it.
    expect(getTerminalLinesBelow("tab-1")).toBe(50);
    expect(xtermViewRenders.get("tab-1")).toBe(rendersBefore);
  });

  it("writes nothing and re-renders nothing for frames where linesBelow is unchanged", async () => {
    await show();
    await attachOk();
    const t = term();
    t.buffer.active.baseY = 100;
    t.buffer.active.viewportY = 10;
    act(() => t.handlers.scroll?.());
    act(() => runFrames());
    expect(getTerminalLinesBelow("tab-1")).toBe(90);
    const before = ctx.commits;
    const rendersBefore = xtermViewRenders.get("tab-1") ?? 0;
    act(() => t.handlers.scroll?.());
    act(() => runFrames());
    act(() => t.handlers.scroll?.());
    act(() => runFrames());
    expect(ctx.commits).toBe(before);
    expect(getTerminalLinesBelow("tab-1")).toBe(90);
    expect(xtermViewRenders.get("tab-1")).toBe(rendersBefore);
  });

  it("reports the top of the normal screen's scrollback, never of the alternate screen", async () => {
    await show();
    await attachOk();
    const t = term();
    const active = t.buffer.active as { baseY: number; viewportY: number; type?: string; length?: number };
    const history = () => useTerminalViewStore.getState().history["tab-1"];
    t.options.scrollback = 1000;
    active.type = "normal";
    active.baseY = 100;
    active.viewportY = 0;
    active.length = 124;
    act(() => t.handlers.scroll?.());
    act(() => runFrames());
    expect(history()).toMatchObject({ atTop: true, trimmed: false });
    active.viewportY = 10;
    act(() => t.handlers.scroll?.());
    act(() => runFrames());
    expect(history()?.atTop).toBe(false);
    active.type = "alternate";
    active.baseY = 0;
    active.viewportY = 0;
    act(() => t.handlers.scroll?.());
    act(() => runFrames());
    expect(history()?.atTop).toBe(false);
  });

  it("reports a full scrollback as trimmed", async () => {
    await show();
    await attachOk();
    const t = term();
    t.options.scrollback = 1000;
    const active = t.buffer.active as { baseY: number; viewportY: number; type?: string; length?: number };
    active.type = "normal";
    active.baseY = 1000;
    active.viewportY = 1000;
    active.length = 1000 + t.rows;
    act(() => t.handlers.scroll?.());
    act(() => runFrames());
    expect(useTerminalViewStore.getState().history["tab-1"]?.trimmed).toBe(true);
  });

  it("keeps the view's durable history start from full-replay anchors only", async () => {
    await show();
    await attachOk();
    const from = () => useTerminalViewStore.getState().history["tab-1"]?.from ?? null;
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0, historyFrom: 4096 });
    expect(from()).toBe(4096);
    // A resumed anchor keeps the view's content and its start.
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 10, controlBytes: 0 });
    expect(from()).toBe(4096);
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0, expired: true, historyFrom: 9000 });
    expect(from()).toBe(9000);
    // An expired anchor without a start (no durable history) clears it.
    feedControl({ type: "nexterm.stream-anchor", epoch: "e2", streamSeq: 0, controlBytes: 0, expired: true });
    expect(from()).toBeNull();
    // A full replay (clear prelude) without a start clears it as well.
    feedControl({ type: "nexterm.stream-anchor", epoch: "e2", streamSeq: 0, controlBytes: 0, expired: true, historyFrom: 7000 });
    expect(from()).toBe(7000);
    feedControl({ type: "nexterm.stream-anchor", epoch: "e2", streamSeq: 0, controlBytes: 4 });
    expect(from()).toBeNull();
  });

  it("coalesces title changes to the latest one per frame and skips duplicates", async () => {
    const onTitle = vi.fn();
    await show({ onTitle });
    await attachOk();
    const t = term();
    act(() => {
      t.handlers.title?.("t1");
      t.handlers.title?.("t2");
      t.handlers.title?.("t3");
    });
    expect(onTitle).not.toHaveBeenCalled();
    act(() => runFrames());
    expect(onTitle.mock.calls).toEqual([["t3"]]);
    act(() => {
      t.handlers.title?.("t3");
    });
    act(() => runFrames());
    expect(onTitle).toHaveBeenCalledTimes(1);
    act(() => {
      t.handlers.title?.("t4");
    });
    act(() => runFrames());
    expect(onTitle.mock.calls).toEqual([["t3"], ["t4"]]);
  });

  it("coalesces write-completion updates into one render per frame", async () => {
    await show();
    await attachOk();
    const t = term();
    t.buffer.active.baseY = 42;
    t.buffer.active.viewportY = 0;
    const before = ctx.commits;
    feedBytes("one\r\n");
    act(() => t.drainWrites());
    feedBytes("two\r\n");
    act(() => t.drainWrites());
    feedBytes("three\r\n");
    act(() => t.drainWrites());
    expect(ctx.commits).toBe(before);
    act(() => runFrames());
    expect(ctx.commits).toBe(before + 1);
    expect(buttonText()).toContain("42 行");
    expect(getTerminalLinesBelow("tab-1")).toBe(42);
  });

  it("unmount clears the view-store entry and late scroll/title signals never write stale values", async () => {
    const onTitle = vi.fn();
    await show({ onTitle });
    await attachOk();
    const t = term();
    t.buffer.active.baseY = 100;
    t.buffer.active.viewportY = 10;
    act(() => t.handlers.scroll?.());
    act(() => runFrames());
    expect(getTerminalLinesBelow("tab-1")).toBe(90);
    ctx.mounted?.unmount();
    ctx.mounted = undefined;
    expect(getTerminalLinesBelow("tab-1")).toBe(0);
    // The fake terminal's disposables are no-ops, so its handlers still fire after
    // unmount: without the disposal guard these would schedule a flush and write
    // the stale value (and a stale title) back into the store for a dead tab.
    act(() => {
      t.handlers.scroll?.();
      t.handlers.title?.("late title");
    });
    act(() => runFrames());
    expect(getTerminalLinesBelow("tab-1")).toBe(0);
    expect(onTitle).not.toHaveBeenCalled();
  });

  it("a scroll burst on one tab re-renders only that tab's badge", async () => {
    stubFrames();
    let commitsA = 0;
    let commitsB = 0;
    ctx.mounted = mount(
      createElement(
        "div",
        null,
        createElement(
          Profiler,
          { id: "view-a", onRender: () => { commitsA += 1; } },
          createElement(CountingXtermView, { sessionId: "session-1", tabId: "tab-1" }),
        ),
        createElement(
          Profiler,
          { id: "view-b", onRender: () => { commitsB += 1; } },
          createElement(CountingXtermView, { sessionId: "session-2", tabId: "tab-2" }),
        ),
      ),
    );
    await act(async () => {
      await flushMacrotask();
      await flushMacrotask();
    });
    expect(harness.terminals).toHaveLength(2);
    const t = harness.terminals[1] as FakeTerminal;
    t.buffer.active.baseY = 100;
    const beforeA = commitsA;
    const beforeB = commitsB;
    const rendersBefore = xtermViewRenders.get("tab-1") ?? 0;
    for (const y of [10, 20, 30]) {
      t.buffer.active.viewportY = y;
      act(() => t.handlers.scroll?.());
    }
    act(() => runFrames());
    expect(commitsB).toBe(beforeB + 1);
    expect(commitsA).toBe(beforeA);
    expect(getTerminalLinesBelow("tab-2")).toBe(70);
    expect(getTerminalLinesBelow("tab-1")).toBe(0);
    expect(xtermViewRenders.get("tab-1")).toBe(rendersBefore);
  });

  it("concurrent attaching tabs: pre-response replay/scroll stays per-tab and survives the attach transition", async () => {
    // Both panes hang in the shared "pending" placeholder phase (the attach RPC has
    // not returned), exactly as TerminalPane renders them before kernelTabId arrives.
    const attachA = deferred<Record<string, unknown>>();
    const attachB = deferred<Record<string, unknown>>();
    harness.attach
      .mockImplementationOnce(() => attachA.promise)
      .mockImplementationOnce(() => attachB.promise);
    stubFrames();
    let commitsA = 0;
    let commitsB = 0;
    const onAttachA = vi.fn();
    const onAttachB = vi.fn();
    const renderViews = (tabIdA: string, tabIdB: string) =>
      createElement(
        "div",
        null,
        createElement(
          Profiler,
          { id: "view-a", onRender: () => { commitsA += 1; } },
          createElement(XtermView, { sessionId: "session-1", tabId: tabIdA, viewTabId: "tab-a", onAttach: onAttachA }),
        ),
        createElement(
          Profiler,
          { id: "view-b", onRender: () => { commitsB += 1; } },
          createElement(XtermView, { sessionId: "session-2", tabId: tabIdB, viewTabId: "tab-b", onAttach: onAttachB }),
        ),
      );
    ctx.mounted = mount(renderViews("pending", "pending"));
    // The attach timers fire but both RPCs stay in flight.
    await act(async () => {
      await flushMacrotask();
    });
    expect(harness.attach).toHaveBeenCalledTimes(2);
    expect(harness.channels).toHaveLength(2);

    // Pre-response replay on A: an anchor frame plus replayed output, then a scroll
    // leaving the viewport 90 lines above the bottom — all before any attach
    // response. B receives nothing.
    const tA = harness.terminals[0] as FakeTerminal;
    act(() => {
      harness.channels[0]?.onControl?.({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0, attachmentId: 11 });
    });
    act(() => {
      harness.channels[0]?.onBytes?.(enc.encode("replayed output\r\n"));
    });
    tA.buffer.active.baseY = 100;
    tA.buffer.active.viewportY = 10;
    const commitsABefore = commitsA;
    const commitsBBefore = commitsB;
    act(() => tA.handlers.scroll?.());
    act(() => runFrames());

    // Isolation: only A's per-pane key was written; B's key, badge, and render
    // count are untouched even though both panes share the "pending" placeholder.
    expect(getTerminalLinesBelow("tab-a")).toBe(90);
    expect(getTerminalLinesBelow("tab-b")).toBe(0);
    expect(commitsA).toBe(commitsABefore + 1);
    expect(commitsB).toBe(commitsBBefore);
    expect(buttonOccurrences("90 行")).toBe(1);

    // A's attach response lands and the pane switches tabId to the real kernel id:
    // the current value must carry over with no reset and no new signal.
    await act(async () => {
      attachA.resolve({ tabId: "kernel-1", cols: 80, rows: 30, sizeOwner: "me", subscribers: 1, viewers: 1, exited: false, shell: "unknown", retainable: true, ephemeral: false, attachmentId: 11 });
      await flushMacrotask();
    });
    expect(onAttachA).toHaveBeenCalledWith("kernel-1");
    await act(async () => {
      ctx.mounted!.root.render(renderViews("kernel-1", "pending"));
      await flushMacrotask();
    });
    expect(getTerminalLinesBelow("tab-a")).toBe(90);
    expect(buttonOccurrences("90 行")).toBe(1);

    // B attaches afterwards and still shows nothing: A's pre-response state never
    // leaked into B's key at any point of the lifecycle.
    await act(async () => {
      attachB.resolve({ tabId: "kernel-2", cols: 80, rows: 30, sizeOwner: "me", subscribers: 1, viewers: 1, exited: false, shell: "unknown", retainable: true, ephemeral: false, attachmentId: 12 });
      await flushMacrotask();
    });
    await act(async () => {
      ctx.mounted!.root.render(renderViews("kernel-1", "kernel-2"));
      await flushMacrotask();
    });
    expect(onAttachB).toHaveBeenCalledWith("kernel-2");
    expect(getTerminalLinesBelow("tab-b")).toBe(0);
    expect(buttonOccurrences("90 行")).toBe(1);
  });

  it("realistic burst: 600 scroll events, cycling OSC titles, and streaming output coalesce to one commit per frame", async () => {
    const onTitle = vi.fn();
    await show({ onTitle });
    await attachOk();
    const t = term();
    // A 100k-line scrollback with the viewport scrolled far up while output
    // keeps arriving — the exact shape the per-tab view store exists for.
    t.buffer.active.baseY = 100_000;
    const before = ctx.commits;
    const rendersBefore = xtermViewRenders.get("tab-1") ?? 0;
    for (let frame = 0; frame < 60; frame += 1) {
      for (let e = 0; e < 10; e += 1) {
        t.buffer.active.viewportY = 50_000 - frame * 100 - e;
        act(() => t.handlers.scroll?.());
      }
      // The cwd title holds steady for 10-frame runs: repeated identical OSC
      // titles must be deduped, not delivered once per frame.
      const title = `user@host: ~/projects/app${Math.floor(frame / 10)}`;
      act(() => {
        t.handlers.title?.(title);
        t.handlers.title?.(title);
      });
      feedBytes(`output line ${frame}\r\n`);
      act(() => t.drainWrites());
      act(() => runFrames());
    }
    // Every frame carried a real value change, so each produced exactly one
    // badge commit — never one per scroll event, title, or write completion.
    expect(ctx.commits).toBe(before + 60);
    expect(buttonText()).toContain("55909 行");
    expect(getTerminalLinesBelow("tab-1")).toBe(55_909);
    expect(xtermViewRenders.get("tab-1")).toBe(rendersBefore);
    // Only the 6 distinct cwd titles were delivered; the repeated ones were
    // deduped on top of the per-frame coalescing.
    expect(onTitle).toHaveBeenCalledTimes(6);
    expect(onTitle).toHaveBeenLastCalledWith("user@host: ~/projects/app5");
  });
});
