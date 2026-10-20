import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ipcEventTopics } from "../ipc/generated/events";
import { createWailsMock, type WailsMock } from "./wailsMock";
import type {
  SessionStatusEvent as SessionStatusEventFromEvents,
  TerminalExitEvent as TerminalExitEventFromEvents,
  TerminalControlEvent,
  TerminalThrottledEvent,
} from "../ipc/events";
import type {
  SessionStatusEvent as SessionStatusEventFromTypes,
  TerminalExitEvent as TerminalExitEventFromTypes,
} from "../ipc/types";

type EventHandler = (event: { data: unknown }) => void;

let runtime: WailsMock;

beforeEach(() => {
  vi.resetModules();
  vi.stubGlobal("window", undefined);
  runtime = createWailsMock();
  runtime.Events.On.mockImplementation(() => vi.fn());
  vi.doMock("@wailsio/runtime", () => runtime);
});

afterEach(() => {
  vi.doUnmock("@wailsio/runtime");
});

function handlerAt(index = 0): EventHandler {
  return runtime.Events.On.mock.calls[index][1] as EventHandler;
}

async function ready(channel: unknown): Promise<void> {
  await (channel as unknown as { ready: Promise<void> }).ready;
}

describe("Wails events and channels", () => {
  it("matches the generated event topics as a set", async () => {
    const { EVENTS } = await import("../ipc/events");
    expect(new Set(Object.values(EVENTS))).toEqual(new Set(Object.values(ipcEventTopics)));
  });

  it("subscribes before commands and preserves early bytes in order", async () => {
    const off = vi.fn();
    let push: EventHandler | undefined;
    runtime.Events.On.mockImplementation((_topic, handler) => {
      push = handler as EventHandler;
      return off;
    });
    runtime.Call.ByName.mockImplementation(async () => {
      push?.({ data: [1, 2] });
      push?.({ data: [3] });
      return { ok: true, data: { tabId: "tab-1", shell: "unknown" } };
    });
    const { createBinaryChannel, channelIdOf } = await import("../ipc/events");
    const { terminalApi } = await import("../ipc/commands");
    const frames: number[][] = [];
    const channel = createBinaryChannel((bytes) => frames.push(Array.from(bytes)));

    await expect(terminalApi.attach("session-1", 80, 24, channel)).resolves.toEqual({
      tabId: "tab-1",
      shell: "unknown",
    });
    expect(frames).toEqual([[1, 2], [3]]);
    expect(runtime.Events.On.mock.calls[0][0]).toBe(`channel://${channelIdOf(channel)}`);
    expect(runtime.Events.On.mock.invocationCallOrder[0]).toBeLessThan(
      runtime.Call.ByName.mock.invocationCallOrder[0],
    );
  });

  it("decodes ArrayBuffer/Uint8Array/number[]/string while preserving order", async () => {
    const { createBinaryChannel } = await import("../ipc/events");
    const frames: number[][] = [];
    const channel = createBinaryChannel((bytes) => frames.push(Array.from(bytes)));
    await ready(channel);
    const push = handlerAt();
    const buffer = new Uint8Array([4, 5]).buffer;

    push({ data: buffer });
    push({ data: new Uint8Array([6]) });
    push({ data: [7, 255] });
    push({ data: "A" });

    expect(frames).toEqual([[4, 5], [6], [7, 255], [65]]);
  });

  it("routes stream anchor frames to onControl and decodes other messages as bytes", async () => {
    const { createBinaryChannel, STREAM_ANCHOR_TYPE } = await import("../ipc/events");
    const frames: number[][] = [];
    const anchors: unknown[] = [];
    const channel = createBinaryChannel(
      (bytes) => frames.push(Array.from(bytes)),
      (control) => anchors.push(control),
    );
    await ready(channel);
    const push = handlerAt();

    push({ data: { type: STREAM_ANCHOR_TYPE, epoch: "e1", streamSeq: 5 } });
    push({ data: [1, 2] });
    push({ data: { type: "other-json" } });

    expect(anchors).toEqual([{ type: STREAM_ANCHOR_TYPE, epoch: "e1", streamSeq: 5 }]);
    expect(frames).toEqual([[1, 2]]);
  });

  it("silently ignores anchor frames when onControl is not registered", async () => {
    const { createBinaryChannel, STREAM_ANCHOR_TYPE } = await import("../ipc/events");
    const frames: number[] = [];
    const channel = createBinaryChannel((bytes) => frames.push(...bytes));
    await ready(channel);
    const push = handlerAt();

    push({ data: { type: STREAM_ANCHOR_TYPE, epoch: "e1", streamSeq: 5 } });
    push({ data: [3] });

    expect(frames).toEqual([3]);
  });

  it("dispose unsubscribes idempotently and ignores late callbacks", async () => {
    const off = vi.fn();
    runtime.Events.On.mockReturnValue(off);
    const { createBinaryChannel, disposeChannel } = await import("../ipc/events");
    const frames: number[] = [];
    const channel = createBinaryChannel((bytes) => frames.push(...bytes));
    await ready(channel);
    const push = handlerAt();

    disposeChannel(channel);
    disposeChannel(channel);
    push({ data: [1, 2, 3] });

    expect(off).toHaveBeenCalledTimes(1);
    expect(frames).toEqual([]);
  });

  it("delivers only object frames on the AI channel", async () => {
    const { createAiChannel } = await import("../ipc/events");
    const events: Record<string, unknown>[] = [];
    const channel = createAiChannel((event) => events.push(event));
    await ready(channel);
    const push = handlerAt();

    push({ data: { type: "done" } });
    push({ data: '{"type":"confirmRequired","jobId":"job-1"}' });
    push({ data: "Thinking" });
    push({ data: 42 });

    expect(events).toEqual([{ type: "done" }]);
  });

  it("unwraps named-event data and returns an asynchronous unsubscribe function", async () => {
    const off = vi.fn();
    runtime.Events.On.mockReturnValue(off);
    const { listenEvent, EVENTS } = await import("../ipc/events");
    const payloads: { sessionId: string }[] = [];

    const unlisten = await listenEvent<{ sessionId: string }>(EVENTS.sessionStatus, (payload) =>
      payloads.push(payload),
    );
    handlerAt()({ data: { sessionId: "session-1" } });
    unlisten();

    expect(payloads).toEqual([{ sessionId: "session-1" }]);
    expect(off).toHaveBeenCalledTimes(1);
  });

  it("routes desktop stream close notifications to reopen subscribers without emitting bytes", async () => {
    const { createBinaryChannel, onChannelReopen, DESKTOP_STREAM_CLOSED_TYPE } = await import("../ipc/events");
    const received = vi.fn();
    const channel = createBinaryChannel(received);
    await ready(channel);
    const reopened = vi.fn();
    const off = onChannelReopen(channel, reopened);
    const push = handlerAt();

    push({ data: { type: DESKTOP_STREAM_CLOSED_TYPE } });
    expect(reopened).toHaveBeenCalledTimes(1);
    expect(received).not.toHaveBeenCalled();

    off();
    push({ data: { type: DESKTOP_STREAM_CLOSED_TYPE } });
    expect(reopened).toHaveBeenCalledTimes(1);
  });

  it("does not reopen a disposed desktop channel on a late close notification", async () => {
    const { createBinaryChannel, disposeChannel, onChannelReopen, DESKTOP_STREAM_CLOSED_TYPE } = await import("../ipc/events");
    const channel = createBinaryChannel(() => {});
    await ready(channel);
    const reopened = vi.fn();
    onChannelReopen(channel, reopened);
    const push = handlerAt();

    disposeChannel(channel);
    push({ data: { type: DESKTOP_STREAM_CLOSED_TYPE } });
    expect(reopened).not.toHaveBeenCalled();
  });
});

describe("shared event DTOs and version high-water marks", () => {
  it("shares SessionStatusEvent/TerminalExitEvent between events.ts and types.ts, including version", () => {
    const status: SessionStatusEventFromEvents = {
      sessionId: "s-1",
      status: "connected",
      error: null,
      version: 3,
    };
    const sameStatus: SessionStatusEventFromTypes = status;
    expect(sameStatus.version).toBe(3);

    const exit: TerminalExitEventFromEvents = { tabId: "t-1", exitCode: 0, version: 5 };
    const sameExit: TerminalExitEventFromTypes = exit;
    expect(sameExit.version).toBe(5);
  });

  it("carries the authoritative grid and event version in TerminalControlEvent (matching Go ControlEvent)", () => {
    const control: TerminalControlEvent = {
      tabId: "t-1",
      controller: null,
      subscribers: 2,
      viewers: 1,
      exited: false,
      cols: 120,
      rows: 40,
      gridRevision: 7,
      version: 11,
    };
    expect(control.gridRevision).toBe(7);
    expect(control.version).toBe(11);
  });

  it("requires version/channelId in TerminalThrottledEvent (matching Go ThrottleEvent)", () => {
    const throttled: TerminalThrottledEvent = {
      tabId: "t-1",
      channelId: "c-1",
      inflightBytes: 4096,
      version: 2,
    };
    expect(throttled.channelId).toBe("c-1");
    expect(throttled.version).toBe(2);

    const recovered: TerminalThrottledEvent = { ...throttled, recovered: true };
    expect(recovered.recovered).toBe(true);
  });

  it("EventVersionGate accepts increasing versions and rejects replays, out-of-order versions, and invalid versions", async () => {
    const { EventVersionGate } = await import("../ipc/events");
    const gate = new EventVersionGate();

    expect(gate.accept("tab-1", 1)).toBe(true);
    expect(gate.accept("tab-1", 2)).toBe(true);
    expect(gate.accept("tab-1", 2)).toBe(false);
    expect(gate.accept("tab-1", 1)).toBe(false);
    expect(gate.accept("tab-1", 0)).toBe(false);
    expect(gate.accept("tab-1", Number.NaN)).toBe(false);
    expect(gate.accept("", 3)).toBe(false);
    expect(gate.accept("tab-2", 1)).toBe(true);
    expect(gate.accept("tab-1", 3)).toBe(true);
  });
});
