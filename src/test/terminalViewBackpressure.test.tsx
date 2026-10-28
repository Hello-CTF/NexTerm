/** @vitest-environment jsdom */

import { describe, expect, it, vi } from "vitest";
import { Profiler, act, createElement } from "react";
import { deferred, flushMacrotask } from "./features/reactTestUtils";
import { harness } from "./lib/terminalViewHarness";
import { CountingXtermView, attachOk, ctx, enc, feedBytes, feedControl, installTerminalViewHooks, show, term } from "./lib/terminalViewDriver";

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

describe("XtermView stream wiring", () => {
  it("attaches through the channel and routes automatic responses and input upstream", async () => {
    await show();
    await attachOk();
    expect(harness.attach).toHaveBeenCalledWith("session-1", 80, 30, expect.anything());
    const t = term();
    act(() => t.handlers.data?.("\x1b[?1;2c"));
    expect(harness.write).toHaveBeenCalledWith("kernel-1", enc.encode("\x1b[?1;2c"));
    act(() => t.handlers.data?.("ls\r"));
    expect(harness.write).toHaveBeenCalledWith("kernel-1", enc.encode("ls\r"));
    expect(harness.feedInput).toHaveBeenCalledWith("ls\r");
  });

  it("rate-limits an automatic reply flood without any toast and logs it for diagnostics", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    await show();
    await attachOk();
    const t = term();
    // Freeze the limiter's clock so the bucket cannot refill mid-flood: the
    // outcome must not depend on how fast this machine runs the loop.
    vi.useFakeTimers({ toFake: ["Date"] });
    act(() => {
      for (let i = 0; i < 5000; i += 1) t.handlers.data?.("\x1b[?1;2c");
    });
    const upstreamBytes = harness.write.mock.calls.reduce((n, [, bytes]) => n + (bytes as Uint8Array).length, 0);
    expect(upstreamBytes).toBeGreaterThan(0);
    // Only the burst goes out: far below the 35 KB the flood asked for.
    expect(upstreamBytes).toBeLessThanOrEqual(20 * 1024);
    // Keystrokes still go straight through while replies are held back.
    act(() => t.handlers.data?.("q"));
    expect(harness.write).toHaveBeenLastCalledWith("kernel-1", enc.encode("q"));
    ctx.mounted?.unmount();
    ctx.mounted = undefined;
    expect(harness.pushToast).not.toHaveBeenCalled();
    const reports = warn.mock.calls.filter(([message]) => String(message).includes("automatic query replies"));
    expect(reports).toHaveLength(1);
    expect(reports[0][1]).toMatchObject({ coalesced: expect.any(Number), dropped: 1 });
    vi.useRealTimers();
    warn.mockRestore();
  });

  it("suppresses pre-expiry OSC events whose write callbacks fire after the expired control frame", async () => {
    const onClipboard = vi.fn();
    await show({ onClipboard });
    await attachOk();
    const t = term();
    feedBytes("before\x1b]52;c;stale\x07");
    expect(t.writeLog).toEqual(["before"]);
    feedControl({ type: "nexterm.stream-anchor", epoch: "e2", streamSeq: 0, controlBytes: 0, expired: true });
    expect(harness.blocksClear).toHaveBeenCalledTimes(1);
    expect(t.writeLog.some((entry) => entry.includes("输出历史已过期"))).toBe(true);
    feedBytes("after\x1b]52;c;fresh\x07");
    act(() => t.drainWrites());
    expect(onClipboard).toHaveBeenCalledTimes(1);
    expect(onClipboard).toHaveBeenCalledWith("c;fresh");
    expect(t.writeLog[0]).toBe("before");
    expect(t.writeLog[t.writeLog.length - 1]).toBe("after");
  });

  it("keeps pre-expiry events when the reattach control frame is not expired", async () => {
    const onClipboard = vi.fn();
    await show({ onClipboard });
    await attachOk();
    const t = term();
    feedBytes("before\x1b]52;c;valid\x07");
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 7, controlBytes: 0 });
    feedBytes("after");
    act(() => t.drainWrites());
    expect(onClipboard).toHaveBeenCalledWith("c;valid");
  });

  it("pauses upstream writes past the high watermark and resumes with visibility", async () => {
    const onStreamBacklog = vi.fn();
    await show({ onStreamBacklog });
    await attachOk();
    const t = term();
    const flood = new Uint8Array((4 << 20) + 4096).fill(0x61);
    act(() => {
      harness.channel?.onBytes?.(flood);
    });
    expect(onStreamBacklog).toHaveBeenCalledTimes(1);
    const backlog = onStreamBacklog.mock.calls[0][0];
    expect(backlog.paused).toBe(true);
    expect(backlog.pendingBytes).toBe(flood.length);
    act(() => {
      harness.channel?.onBytes?.(new Uint8Array(4096).fill(0x62));
    });
    expect(t.writeLog.length).toBe(1);
    act(() => t.drainWrites());
    expect(onStreamBacklog).toHaveBeenLastCalledWith(null);
    expect(t.writeLog.length).toBe(2);
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(flood.length + 4096);
  });

  it("bounds the FIFO under a sustained flood: refuses overflow, detaches, and replays from the accepted offset", async () => {
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    // Four 4 MiB chunks reach the 16 MiB hard cap; the writer is paused at
    // the 4 MiB high watermark after the first one.
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    expect(harness.detach).not.toHaveBeenCalled();
    expect(harness.pushToast).not.toHaveBeenCalled();
    // The high watermark asked the server to pause the feed.
    expect(harness.call).toHaveBeenCalledWith("terminal_pause", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      acceptedBytes: expect.any(Number),
    });
    // The fifth chunk is refused: it never enters the FIFO or the accounting.
    act(() => harness.channel?.onBytes?.(chunk));
    expect(harness.detach).toHaveBeenCalledTimes(1);
    expect(harness.detach).toHaveBeenCalledWith("kernel-1", "channel-1");
    expect(harness.pushToast).toHaveBeenCalledWith("info", expect.stringContaining("重新同步"));
    // Refusal is sticky: even after the FIFO drains, new chunks are refused
    // until the attachment boundary, so the accepted prefix stays contiguous.
    act(() => t.drainWrites());
    expect(t.writeLog.length).toBe(4);
    act(() => harness.channel?.onBytes?.(new Uint8Array(1024).fill(0x62)));
    expect(t.writeLog.length).toBe(4);
    expect(harness.detach).toHaveBeenCalledTimes(1);
    // Draining reattaches the detached subscription without a transport
    // reopen. A resume RPC alone cannot recreate that subscription.
    await act(async () => {
      await flushMacrotask();
    });
    expect(harness.attachTab).toHaveBeenCalledWith(
      "kernel-1",
      expect.anything(),
      -1,
      { seq: 4 * (4 << 20), epoch: "e1" },
      "session-1",
    );
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 4 * (4 << 20), controlBytes: 0, attachmentId: 12 });
    // The replay redelivers the refused chunk: accepted and rendered in order.
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    expect(t.writeLog.length).toBe(5);
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(5 * (4 << 20));
  });

  it("accepts replay that arrives before the attach response (attachment boundary, not response timing)", async () => {
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => harness.channel?.onBytes?.(chunk));
    expect(harness.detach).toHaveBeenCalledTimes(1);
    act(() => t.drainWrites());
    // The reattach RPC stays in flight while the anchor and replay arrive.
    let finish: ((value: unknown) => void) | undefined;
    harness.attachTab.mockImplementation(
      () => new Promise((resolve) => { finish = resolve; }),
    );
    act(() => harness.reopenCb?.());
    // The anchor frame marks the attachment stream boundary: the episode
    // ends here even though the attach response has not been processed.
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 4 * (4 << 20), controlBytes: 0 });
    const replay = new Uint8Array(4 << 20).fill(0x62);
    act(() => harness.channel?.onBytes?.(replay));
    act(() => t.drainWrites());
    finish?.({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      retainable: true,
      ephemeral: false,
      attachmentId: 12,
    });
    await act(async () => {
      await flushMacrotask();
    });
    // The replay was accepted before the response and rendered in order:
    // four original chunks plus the replayed chunk.
    expect(t.writeLog.length).toBe(5);
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(5 * (4 << 20));
  });

  it("keeps a replay overflow refused after the attach response and resumes before the gap", async () => {
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const attach = deferred<Record<string, unknown>>();
    const recovery = deferred<Record<string, unknown>>();
    harness.attachTab.mockReturnValueOnce(attach.promise).mockReturnValueOnce(recovery.promise);
    act(() => harness.reopenCb?.());
    expect(harness.attachTab).toHaveBeenCalledTimes(1);
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0, attachmentId: 12 });

    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let index = 0; index < 5; index++) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    expect(harness.detach).toHaveBeenCalledTimes(1);
    // Free enough capacity for the late small chunk while keeping the
    // writer paused, so a replacement cannot end the refusal episode yet.
    act(() => t.drainWrites(1));
    await act(async () => {
      attach.resolve({ tabId: "kernel-1", cols: 80, rows: 30, sizeOwner: "me", subscribers: 1, viewers: 1, exited: false, retainable: true, ephemeral: false, attachmentId: 12 });
      await flushMacrotask();
    });
    expect(harness.attachTab).toHaveBeenCalledTimes(1);
    const later = new Uint8Array(1024).fill(0x62);
    act(() => harness.channel?.onBytes?.(later));
    act(() => t.drainWrites());
    await act(async () => { await flushMacrotask(); });
    expect(t.writeLog.reduce((total, entry) => total + entry.length, 0)).toBe(4 * chunk.length);
    expect(harness.attachTab).toHaveBeenNthCalledWith(
      2, "kernel-1", expect.anything(), -1,
      { seq: 4 * chunk.length, epoch: "e1" }, "session-1",
    );

    // The next ordered anchor restores the refused suffix first; the late
    // bytes then follow it instead of being rendered across the missing gap.
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 4 * chunk.length, controlBytes: 0, attachmentId: 13 });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => harness.channel?.onBytes?.(later));
    act(() => t.drainWrites());
    await act(async () => {
      recovery.resolve({ tabId: "kernel-1", cols: 80, rows: 30, sizeOwner: "me", subscribers: 1, viewers: 1, exited: false, retainable: true, ephemeral: false, attachmentId: 13 });
      await flushMacrotask();
    });
    expect(t.writeLog.reduce((total, entry) => total + entry.length, 0)).toBe(5 * chunk.length + later.length);
    expect(t.writeLog.at(-1)).toBe("b".repeat(later.length));
  });

  it("ephemeral tabs: a working pause keeps the full sequence lossless with no refusal", async () => {
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    // The first chunk reaches the high watermark and the client pauses the
    // feed instead of letting the FIFO hit the cap.
    act(() => harness.channel?.onBytes?.(chunk));
    expect(harness.call).toHaveBeenCalledWith("terminal_pause", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      acceptedBytes: expect.any(Number),
    });
    // The feed is paused (no more bytes arrive); the FIFO drains below the
    // low watermark and the client resumes the feed.
    act(() => t.drainWrites());
    // The pause response carrying the episode token lands, then the resume
    // fires with it.
    await act(async () => {
      await flushMacrotask();
    });
    expect(harness.call).toHaveBeenCalledWith("terminal_resume", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      episodeToken: 31,
    });
    // The feed resumes and the full sequence arrives in order: nothing was
    // refused, so there is no hole.
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
      act(() => t.drainWrites());
    }
    expect(harness.pushToast).not.toHaveBeenCalledWith("error", expect.anything());
    expect(t.writeLog.length).toBe(5);
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(5 * (4 << 20));
  });

  it("ephemeral tabs: a failed pause retries and then surfaces an accurate overflow state", async () => {
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    harness.call.mockImplementation((command: string) =>
      command === "terminal_pause"
        ? Promise.reject(new Error("no such command"))
        : Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 31 }),
    );
    vi.useFakeTimers();
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => harness.channel?.onBytes?.(chunk));
    // The pause RPC failed every retry: the client redelivers the refused
    // suffix from the tab's recovery ring, never reaps the PTY, and the full
    // sequence arrives contiguously.
    expect(harness.detach).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(300);
    });
    expect(harness.call).toHaveBeenCalledWith("terminal_recover", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      acceptedBytes: 4 * (4 << 20),
    });
    // The replay arrives with a synthetic anchor that ends the episode. The
    // writer drains to make room for it, and the refused chunk is restored
    // in order.
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    // Caught up: the client resumes the feed.
    expect(harness.call).toHaveBeenCalledWith("terminal_resume", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      episodeToken: 31,
    });
    expect(t.writeLog.length).toBe(5);
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(5 * (4 << 20));
    vi.useRealTimers();
  });

  it("winrm line tabs: a failed pause recovers the refused suffix instead of detaching", async () => {
    // Production-shaped WinRM overflow: the first attach goes through
    // session_open_line_tab, whose tab is ephemeral. A cap refusal must take
    // the recovery path — an ordinary detach here would reap the line tab's
    // process — and the full sequence stays contiguous.
    harness.call.mockImplementation((command: string) =>
      command === "terminal_pause"
        ? Promise.reject(new Error("no such command"))
        : Promise.resolve(undefined),
    );
    vi.useFakeTimers();
    await show({ winrm: true });
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => harness.channel?.onBytes?.(chunk));
    expect(harness.openLineTab).toHaveBeenCalledWith("session-1", 80, 30, expect.anything());
    expect(harness.call).toHaveBeenCalledWith("terminal_pause", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 7,
      acceptedBytes: expect.any(Number),
    });
    // The pause RPC failed every retry: the client redelivers the refused
    // suffix from the tab's recovery ring. An ordinary detach would reap the
    // line tab's process, so it must never be called.
    expect(harness.detach).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(300);
    });
    expect(harness.call).toHaveBeenCalledWith("terminal_recover", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 7,
      acceptedBytes: 4 * (4 << 20),
    });
    // The synthetic anchor ends the episode; the refused chunk is restored
    // behind it in order, still with no detach (no reap).
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    expect(harness.detach).not.toHaveBeenCalled();
    // The pause-failure toast may fire, but the recovery itself must not
    // report unrecoverable output: the replay restored the full sequence.
    expect(harness.pushToast).not.toHaveBeenCalledWith(
      "error",
      "终端输出积压过长，早期输出无法恢复，画面可能不完整",
    );
    expect(t.writeLog.length).toBe(5);
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(5 * (4 << 20));
    vi.useRealTimers();
  });

  it("ephemeral tabs: the refusal episode persists through the low-water drain until the recovery anchor", async () => {
    // A refused frame must stay refused across the low-water drain: ending
    // the episode there accepts a later old frame and breaks the contiguous
    // prefix, which the scalar skipLeading cannot represent.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    harness.call.mockImplementation((command: string) =>
      command === "terminal_pause"
        ? Promise.reject(new Error("no such command"))
        : Promise.resolve({ flushedOffset: 22 << 20, episodeToken: 31 }),
    );
    vi.useFakeTimers();
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const a = new Uint8Array(4 << 20).fill(0x61);
    const b = new Uint8Array(4 << 20).fill(0x62);
    const c = new Uint8Array(1 << 20).fill(0x63);
    const d = new Uint8Array(1 << 20).fill(0x64);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(a));
    }
    act(() => harness.channel?.onBytes?.(b));
    act(() => harness.channel?.onBytes?.(c));
    // The writer drains to low water: the episode must NOT end here — only a
    // real recovery anchor ends it.
    act(() => t.drainWrites());
    act(() => harness.channel?.onBytes?.(d));
    // The recovery anchor ends the episode; the replay restores the refused
    // suffix (b+c+d) in order, so the full sequence stays contiguous.
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
    });
    const replay = new Uint8Array(6 << 20);
    replay.set(b, 0);
    replay.set(c, 4 << 20);
    replay.set(d, 5 << 20);
    act(() => harness.channel?.onBytes?.(replay));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    const rendered = t.writeLog.join("");
    expect(rendered.length).toBe(22 << 20);
    // Contiguous original sequence: a(16 MiB) → b(4 MiB) → c(1 MiB) → d(1 MiB),
    // checked at the segment boundaries.
    expect(rendered.slice((16 << 20) - 4, (16 << 20) + 4)).toBe("aaaabbbb");
    expect(rendered.slice((20 << 20) - 4, (20 << 20) + 4)).toBe("bbbbcccc");
    expect(rendered.slice((21 << 20) - 4, (21 << 20) + 4)).toBe("ccccdddd");
    // Caught up: the client resumes the feed.
    expect(harness.call).toHaveBeenCalledWith("terminal_resume", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      episodeToken: 31,
    });
    vi.useRealTimers();
  });

  it("ephemeral tabs: a refusal during an in-flight recover schedules a serial re-recovery", async () => {
    // A replay larger than the writer cap is refused again while the first
    // recover RPC is still in flight. The follow-up refusal must be recorded
    // and serially recovered with the latest accepted offset — never dropped.
    let resolveRecover: ((value: unknown) => void) | undefined;
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    harness.call.mockImplementation((command: string) => {
      if (command === "terminal_pause") return Promise.reject(new Error("no such command"));
      if (command === "terminal_recover") {
        return new Promise((resolve) => {
          resolveRecover = resolve;
        });
      }
      return Promise.resolve(undefined);
    });
    vi.useFakeTimers();
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => harness.channel?.onBytes?.(chunk));
    // The first recover is in flight (deferred). The anchor ends the episode.
    expect(harness.call.mock.calls.filter((c) => c[0] === "terminal_recover")).toHaveLength(1);
    expect(harness.call).toHaveBeenCalledWith("terminal_recover", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      acceptedBytes: 4 * (4 << 20),
    });
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
    });
    // A 20 MiB replay arrives: the writer accepts 16 MiB of it, then refuses
    // the last 4 MiB while the first RPC is still in flight.
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => harness.channel?.onBytes?.(chunk));
    // Complete the first RPC: the pending refusal must now serially fire a
    // second recover with the latest accepted offset (32 MiB).
    resolveRecover?.({ flushedOffset: 36 << 20, episodeToken: 31 });
    await act(async () => {
      await flushMacrotask();
    });
    // The pending refusal serially fired a second recover with the latest
    // accepted offset (32 MiB).
    expect(harness.call).toHaveBeenLastCalledWith("terminal_recover", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      acceptedBytes: 32 << 20,
    });
    // Complete the second RPC as well; the second replay restores the last
    // 4 MiB and the client resumes.
    resolveRecover?.({ flushedOffset: 36 << 20, episodeToken: 31 });
    await act(async () => {
      await flushMacrotask();
    });
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 32 << 20,
      controlBytes: 0,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(harness.call).toHaveBeenCalledWith("terminal_resume", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      episodeToken: 31,
    });
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(9 * (4 << 20));
    vi.useRealTimers();
  });

  it("unmount stops the pause retry: no new pause RPCs after unmount", async () => {
    // The pause retry must not keep calling a detached channel after the
    // component unmounts: the timer is cleared and the retry entry checks
    // disposed, so no new terminal_pause calls fire post-unmount.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    harness.call.mockImplementation((command: string) =>
      command === "terminal_pause"
        ? Promise.reject(new Error("no such command"))
        : Promise.resolve(undefined),
    );
    vi.useFakeTimers();
    await show();
    await attachOk();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    // Reach the high watermark so the pause retry loop starts (and keeps
    // failing, so it would retry forever if not torn down).
    act(() => harness.channel?.onBytes?.(new Uint8Array(4 << 20).fill(0x61)));
    expect(harness.call).toHaveBeenCalledWith("terminal_pause", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      acceptedBytes: expect.any(Number),
    });
    const callsBeforeUnmount = harness.call.mock.calls.length;
    ctx.mounted!.unmount();
    ctx.mounted = undefined;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(350);
    });
    expect(harness.call.mock.calls.length).toBe(callsBeforeUnmount);
    vi.useRealTimers();
  });

  it("visibility and controller-only changes do not detach or recreate the terminal", async () => {
    await show();
    await attachOk();
    expect(harness.terminals.length).toBe(1);
    expect(harness.attach).toHaveBeenCalledTimes(1);
    const rerender = async (props: Record<string, unknown>) => {
      await act(async () => {
        ctx.mounted!.root.render(
          createElement(
            Profiler,
            { id: "xterm-view", onRender: () => { ctx.commits += 1; } },
            createElement(CountingXtermView, { sessionId: "session-1", tabId: "tab-1", ...props }),
          ),
        );
        await flushMacrotask();
      });
    };
    // Only `visible` changed: no remount, no detach, no new xterm instance.
    await rerender({ visible: false });
    expect(harness.detach).not.toHaveBeenCalled();
    expect(harness.terminals.length).toBe(1);
    expect(harness.attach).toHaveBeenCalledTimes(1);
    // Only `canResize` changed: same expectations.
    await rerender({ canResize: false });
    expect(harness.detach).not.toHaveBeenCalled();
    expect(harness.terminals.length).toBe(1);
    expect(harness.attach).toHaveBeenCalledTimes(1);
  });

  it("docker exec accepts replay that arrives before the attach response", async () => {
    await show({ containerId: "container-1" });
    await attachOk();
    const t = term();
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => harness.channel?.onBytes?.(chunk));
    expect(harness.detach).toHaveBeenCalledWith("kernel-1", "channel-1");
    // The resume RPC stays in flight while the replay arrives: the episode
    // must already be closed so the replay is accepted, not discarded.
    let finish: ((value: unknown) => void) | undefined;
    harness.attachTab.mockImplementation(
      () => new Promise((resolve) => { finish = resolve; }),
    );
    act(() => t.drainWrites());
    await act(async () => { await flushMacrotask(); });
    expect(harness.attachTab).toHaveBeenCalled();
    const replay = new Uint8Array(4 << 20).fill(0x62);
    act(() => harness.channel?.onBytes?.(replay));
    act(() => t.drainWrites());
    finish?.({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      retainable: false,
      ephemeral: true,
      attachmentId: 12,
    });
    await act(async () => {
      await flushMacrotask();
    });
    expect(t.writeLog.length).toBe(5);
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(5 * (4 << 20));
  });

  it("docker exec pauses via detach and resumes through attachTab/ResumeExec", async () => {
    await show({ containerId: "container-1" });
    await attachOk();
    const t = term();
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    // The high watermark pauses the exec stream through DetachStream.
    expect(harness.detach).toHaveBeenCalledWith("kernel-1", "channel-1");
    act(() => harness.channel?.onBytes?.(chunk));
    expect(harness.pushToast).toHaveBeenCalledWith("info", expect.stringContaining("重新同步"));
    act(() => t.drainWrites());
    // The drain resumes the exec through attachTab/ResumeExec, which
    // replays the bounded backlog without reopening the transport.
    await act(async () => {
      await flushMacrotask();
      await flushMacrotask();
    });
    expect(harness.attachTab).toHaveBeenCalledWith("kernel-1", expect.anything(), -1, undefined, undefined);
    const replay = new Uint8Array(4 << 20).fill(0x62);
    act(() => harness.channel?.onBytes?.(replay));
    act(() => t.drainWrites());
    expect(t.writeLog.length).toBe(5);
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(5 * (4 << 20));
  });

  it("reattaches Docker after the high watermark drains without overflowing or reopening transport", async () => {
    await show({ containerId: "container-1" });
    await attachOk();
    act(() => harness.channel?.onBytes?.(new Uint8Array(4 << 20).fill(0x61)));
    expect(harness.detach).toHaveBeenCalledWith("kernel-1", "channel-1");
    expect(harness.attachTab).not.toHaveBeenCalled();
    act(() => term().drainWrites());
    await act(async () => { await flushMacrotask(); });
    expect(harness.attachTab).toHaveBeenCalledWith("kernel-1", expect.anything(), -1, undefined, undefined);
  });

  it.each([false, true])("waits for a pending detach before drain/reopen can replace the subscription (Docker=%s)", async (docker) => {
    const detach = deferred<void>();
    harness.detach.mockReturnValueOnce(detach.promise);
    await show(docker ? { containerId: "container-1" } : {});
    await attachOk();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let index = 0; index < (docker ? 1 : 5); index++) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => term().drainWrites());
    await act(async () => {
      harness.reopenCb?.();
      await flushMacrotask();
    });
    expect(harness.attachTab).not.toHaveBeenCalled();
    await act(async () => {
      detach.resolve();
      await flushMacrotask();
    });
    expect(harness.attachTab).toHaveBeenCalledTimes(1);
    expect(harness.attachTab).toHaveBeenCalledWith(
      "kernel-1", expect.anything(), -1,
      docker ? undefined : { seq: 4 * chunk.length, epoch: "e1" },
      docker ? undefined : "session-1",
    );
  });

  it("does not reattach a closed view after its backpressure detach completes", async () => {
    const detach = deferred<void>();
    harness.detach.mockReturnValueOnce(detach.promise);
    await show({ containerId: "container-1" });
    await attachOk();
    act(() => harness.channel?.onBytes?.(new Uint8Array(4 << 20).fill(0x61)));
    act(() => term().drainWrites());
    ctx.mounted?.unmount();
    ctx.mounted = undefined;
    await act(async () => { detach.resolve(); await flushMacrotask(); });
    expect(harness.attachTab).not.toHaveBeenCalled();
  });

  it("recovers a detached subscription even when the detach response is lost", async () => {
    harness.detach.mockRejectedValueOnce(new Error("response lost"));
    await show({ containerId: "container-1" });
    await attachOk();
    act(() => harness.channel?.onBytes?.(new Uint8Array(4 << 20).fill(0x61)));
    act(() => term().drainWrites());
    await act(async () => { await flushMacrotask(); });
    expect(harness.attachTab).toHaveBeenCalledTimes(1);
  });

  it("preserves another drain while a backpressure reattach is still replaying", async () => {
    const attach = deferred<Record<string, unknown>>();
    harness.attachTab.mockReturnValueOnce(attach.promise);
    await show({ containerId: "container-1" });
    await attachOk();
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => term().drainWrites());
    await act(async () => { await flushMacrotask(); });
    expect(harness.attachTab).toHaveBeenCalledTimes(1);
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => term().drainWrites());
    await act(async () => { await flushMacrotask(); });
    expect(harness.attachTab).toHaveBeenCalledTimes(1);
    await act(async () => {
      attach.resolve({ tabId: "kernel-1", cols: 80, rows: 30, sizeOwner: "me", subscribers: 1, viewers: 1, exited: false, attachmentId: 12 });
      await flushMacrotask();
    });
    expect(harness.attachTab).toHaveBeenCalledTimes(2);
  });
});
