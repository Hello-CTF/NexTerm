/** @vitest-environment jsdom */

import { describe, expect, it, vi } from "vitest";
import { act } from "react";
import { harness } from "./lib/terminalViewHarness";
import { attachOk, enc, feedBytes, feedControl, installTerminalViewHooks, runFrames, show, term } from "./lib/terminalViewDriver";

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

describe("XtermView live clipboard on the last input device", () => {
  const owner = (active: boolean) =>
    act(() => {
      harness.channel?.onInputOwner?.({ type: "nexterm.input-owner", active });
    });

  it("writes live OSC 52 only while this view is the input owner, judged where the bytes arrived", async () => {
    const onClipboard = vi.fn();
    await show({ onClipboard });
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0 });
    // Bytes that arrived before the announcement still write, even though
    // xterm parses them after it.
    feedBytes("\x1b]52;c;YQ==\x07");
    owner(false);
    feedBytes("\x1b]52;c;Yg==\x07");
    act(() => t.drainWrites());
    expect(onClipboard.mock.calls).toEqual([["c;YQ=="]]);

    owner(true);
    feedBytes("\x1b]52;c;Yw==\x07");
    act(() => t.drainWrites());
    expect(onClipboard.mock.calls).toEqual([["c;YQ=="], ["c;Yw=="]]);
  });

  it("takes the state from every anchor: redelivered bytes of a passive view write nothing", async () => {
    const onClipboard = vi.fn();
    await show({ onClipboard });
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, inputPassive: true });
    const first = "\x1b]52;c;YQ==\x07";
    feedBytes(first);
    // A recovery anchor redelivers the same bytes: still passive.
    feedControl({ type: "nexterm.stream-anchor", epoch: "recovery-kernel-1", streamSeq: 0, inputPassive: true });
    feedBytes(first + "\x1b]52;c;Yg==\x07");
    act(() => t.drainWrites());
    expect(onClipboard).not.toHaveBeenCalled();

    // An anchor without the flag (this view typed last, or an older server)
    // is active.
    const seq = enc.encode(first + "\x1b]52;c;Yg==\x07").length;
    feedControl({ type: "nexterm.stream-anchor", epoch: "recovery-kernel-1", streamSeq: seq });
    feedBytes("\x1b]52;c;Yw==\x07");
    act(() => t.drainWrites());
    expect(onClipboard.mock.calls).toEqual([["c;Yw=="]]);
  });
});

describe("XtermView replayed output", () => {
  // Replayed: DA1 and DSR 6 queries, an OSC 52 clipboard write, BEL, an OSC 9
  // notification and an OSC 0 title.
  const REPLAYED = "old\x1b[c\x1b[6n\x1b]52;c;b2xk\x07\x07\x1b]9;old note\x07\x1b]0;Replayed title\x07";
  const LIVE = "new\x1b[c\x1b]52;c;bmV3\x07\x1b]9;new note\x07";

  function upstream(): string[] {
    return harness.write.mock.calls.map(([, bytes]) => new TextDecoder().decode(bytes as Uint8Array));
  }

  it("renders a session replay without replies, clipboard writes or notifications; live output still acts", async () => {
    harness.emitReplies = true;
    const onClipboard = vi.fn();
    const onTitle = vi.fn();
    await show({ onClipboard, onTitle });
    await attachOk();
    const t = term();
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "e1",
      streamSeq: 0,
      expired: true,
      classes: [{ from: 0, to: enc.encode(REPLAYED).length, kind: "replay" }],
    });
    // One chunk carries the end of the replay and the first live bytes.
    feedBytes(REPLAYED + LIVE);
    act(() => t.drainWrites());
    act(() => runFrames());
    expect(t.writeLog.join("")).toContain("old");
    expect(t.writeLog.join("")).toContain("new");
    // Only the live DA1 query is answered upstream, through the limiter.
    expect(upstream()).toEqual(["\x1b[?1;2c"]);
    expect(onClipboard).toHaveBeenCalledTimes(1);
    expect(onClipboard).toHaveBeenCalledWith("c;bmV3");
    // Titles are display state: the replayed title still reaches the tab,
    // where a custom title keeps precedence (applyRemoteTabTitle).
    expect(onTitle).toHaveBeenCalledWith("Replayed title");

    // The same bytes arriving live afterwards behave as before.
    feedBytes(REPLAYED);
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["\x1b[?1;2c", "\x1b[?1;2c", "\x1b[1;1R"]);
    expect(onClipboard).toHaveBeenLastCalledWith("c;b2xk");
  });

  it("renders a recovery anchor's refused suffix with its effects: it was never shown", async () => {
    harness.emitReplies = true;
    const onClipboard = vi.fn();
    await show({ onClipboard });
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "recovery-kernel-1", streamSeq: 0 });
    feedBytes("\x1b[6n\x1b]52;c;cmVm\x07");
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["\x1b[1;1R"]);
    expect(onClipboard).toHaveBeenCalledWith("c;cmVm");
  });

  it("counts replayed bytes across frames", async () => {
    harness.emitReplies = true;
    const onClipboard = vi.fn();
    await show({ onClipboard });
    await attachOk();
    const t = term();
    const suffix = "\x1b[6n\x1b]52;c;cmVw\x07";
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "e1",
      streamSeq: 0,
      classes: [{ from: 0, to: enc.encode(suffix).length, kind: "replay" }],
    });
    // The replay arrives split across frames: the count spans them.
    feedBytes(suffix.slice(0, 6));
    feedBytes(suffix.slice(6));
    act(() => t.drainWrites());
    expect(upstream()).toEqual([]);
    expect(onClipboard).not.toHaveBeenCalled();
    feedBytes("\x1b[6n");
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["\x1b[1;1R"]);
  });

  it("closes the replay once its last byte is parsed, with no live output after it", async () => {
    harness.emitReplies = true;
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, expired: true, classes: [{ from: 0, to: 4, kind: "replay" }] });
    feedBytes("\x1b[6n");
    act(() => t.drainWrites());
    expect(upstream()).toEqual([]);
    // Idle shell: nothing live follows. A focus report and a modified F3
    // (which looks like a cursor report) still reach the shell.
    act(() => t.handlers.data?.("\x1b[I"));
    act(() => t.handlers.data?.("\x1b[1;5R"));
    expect(upstream()).toEqual(["\x1b[I", "\x1b[1;5R"]);
  });

  it("passes reply-shaped user input while a replay is still being parsed", async () => {
    harness.emitReplies = true;
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, classes: [{ from: 0, to: 4, kind: "replay" }] });
    feedBytes("\x1b[6n");
    // Parse the open marker only: xterm is inside the replayed bytes.
    act(() => t.drainWrites(1));
    const textarea = document.createElement("textarea");
    t.host?.appendChild(textarea);
    // xterm's own key listener on its textarea raises onData.
    textarea.addEventListener("keydown", () => t.handlers.data?.("\x1b[1;5R"));
    act(() => {
      textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "F3", ctrlKey: true, bubbles: true }));
    });
    // Once the event is over, the same bytes are a reply to replay.
    act(() => t.handlers.data?.("\x1b[1;1R"));
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["\x1b[1;5R"]);
  });

  it("classifies bytes by stream sequence across class frames: replay, answered and live", async () => {
    harness.emitReplies = true;
    const onClipboard = vi.fn();
    await show({ onClipboard });
    await attachOk();
    const t = term();
    // A resumed stream at sequence 100: the first 4 bytes were answered by
    // the server while nobody was attached.
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "e1",
      streamSeq: 100,
      classes: [{ from: 100, to: 104, kind: "answered" }],
    });
    feedBytes("\x1b[6n");
    // A recovered tail continues on the live fanout: its class frame comes
    // ahead of the bytes.
    const tail = "\x1b[c\x1b]52;c;b2xk\x07\x1b]9;x\x07";
    act(() => {
      harness.channel?.onClass?.({ type: "nexterm.stream-class", from: 104, to: 104 + enc.encode(tail).length, kind: "replay" });
    });
    feedBytes(tail + "\x1b[6n\x1b]52;c;bGl2ZQ==\x07");
    act(() => t.drainWrites());
    // Answered and replayed queries get no reply; the live one does.
    expect(upstream()).toEqual(["\x1b[1;1R"]);
    expect(onClipboard).toHaveBeenCalledTimes(1);
    expect(onClipboard).toHaveBeenCalledWith("c;bGl2ZQ==");
  });

  it("classifies held bytes by the recovery anchor's spans", async () => {
    harness.emitReplies = true;
    const onClipboard = vi.fn();
    await show({ onClipboard });
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0 });
    feedBytes("abcd");
    // A replayed tail is announced while the server holds its bytes.
    const tail = "\x1b[6n\x1b]52;c;b2xk\x07";
    const tailSpan = { from: 4, to: 4 + enc.encode(tail).length, kind: "replay" };
    act(() => {
      harness.channel?.onClass?.({ type: "nexterm.stream-class", ...tailSpan });
    });
    // A recovery anchor redelivers from sequence 2 and covers everything fed
    // up to now, including the held bytes.
    feedControl({ type: "nexterm.stream-anchor", epoch: "recovery-kernel-1", streamSeq: 2, classes: [tailSpan] });
    feedBytes("cd" + tail);
    act(() => t.drainWrites());
    expect(upstream()).toEqual([]);
    expect(onClipboard).not.toHaveBeenCalled();
  });

  it("does not carry spans into a new stream that is not marked expired", async () => {
    harness.emitReplies = true;
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, classes: [{ from: 0, to: 64, kind: "replay" }] });
    feedBytes("old");
    // A fresh attach (clear-screen control, empty ring) starts another
    // sequence space at 0; the old replay span must not cover it.
    feedControl({ type: "nexterm.stream-anchor", epoch: "e2", streamSeq: 0, controlBytes: 3 });
    feedBytes("\x1b[H\x1b[6n");
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["\x1b[1;1R"]);
  });

  it("passes a key reply raised by a handler that stops propagation, and nothing after it", async () => {
    harness.emitReplies = true;
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, classes: [{ from: 0, to: 4, kind: "replay" }] });
    feedBytes("\x1b[6n");
    // Parse the open marker only: xterm is inside the replayed bytes.
    act(() => t.drainWrites(1));
    const textarea = document.createElement("textarea");
    t.host?.appendChild(textarea);
    // Like xterm's _keyDown: stop propagation and raise onData on the spot.
    textarea.addEventListener("keydown", (event) => {
      event.stopPropagation();
      t.handlers.data?.("\x1b[1;5R");
    });
    act(() => {
      textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "F3", ctrlKey: true, bubbles: true }));
    });
    // The mark ended with the event, before any timer: a reply-shaped report
    // parsed now is a reply to replay.
    act(() => t.handlers.data?.("\x1b[1;1R"));
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["\x1b[1;5R"]);
  });

  it("keeps keystrokes typed while a replay is parsed", async () => {
    harness.emitReplies = true;
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, expired: true, classes: [{ from: 0, to: 4, kind: "replay" }] });
    feedBytes("\x1b[6n");
    // Parse the start marker only: xterm is now inside the replayed bytes.
    act(() => t.drainWrites(1));
    act(() => t.handlers.data?.("ls\r"));
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["ls\r"]);
  });

  it("renders a docker exec backlog announced as replay without replies", async () => {
    harness.emitReplies = true;
    await show({ containerId: "container-1" });
    await attachOk();
    const t = term();
    const backlog = "\x1b[c\x1b]9;backlog\x07";
    act(() => {
      harness.channel?.onReplay?.({ type: "nexterm.stream-replay", bytes: enc.encode(backlog).length });
    });
    feedBytes(backlog + "\x1b[c");
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["\x1b[?1;2c"]);
  });
});
