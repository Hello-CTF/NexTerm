import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import layoutSource from "../app/layout.ts?raw";
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

describe("Wails events 与 channels", () => {
  it("保留共享事件列表和 layout 事件", async () => {
    const { EVENTS } = await import("../ipc/events");
    expect(Object.values(EVENTS)).toEqual([
      "session://status",
      "terminal://exit",
      "terminal://throttled",
      "terminal://control",
      "fs://progress",
      "update://progress",
      "docker://stats",
      "ai://event",
      "app://error",
      "device://status",
    ]);
    expect(layoutSource).toContain('"layout://changed"');
  });

  it("监听在命令前就绪，命令返回前的 early bytes 不丢且有序", async () => {
    const off = vi.fn();
    let push: EventHandler | undefined;
    runtime.Events.On.mockImplementation((_topic, handler) => {
      push = handler as EventHandler;
      return off;
    });
    runtime.Call.ByName.mockImplementation(async () => {
      push?.({ data: [1, 2] });
      push?.({ data: [3] });
      return { ok: true, data: "tab-1" };
    });
    const { createBinaryChannel, channelIdOf } = await import("../ipc/events");
    const { terminalApi } = await import("../ipc/commands");
    const frames: number[][] = [];
    const channel = createBinaryChannel((bytes) => frames.push(Array.from(bytes)));

    await expect(terminalApi.attach("session-1", 80, 24, channel)).resolves.toBe("tab-1");
    expect(frames).toEqual([[1, 2], [3]]);
    expect(runtime.Events.On.mock.calls[0][0]).toBe(`channel://${channelIdOf(channel)}`);
    expect(runtime.Events.On.mock.invocationCallOrder[0]).toBeLessThan(
      runtime.Call.ByName.mock.invocationCallOrder[0],
    );
  });

  it("保持 ArrayBuffer/Uint8Array/number[]/string 解码和顺序", async () => {
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

  it("dispose 幂等退订，并忽略迟到的回调", async () => {
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

  it("AI channel 保留对象、JSON 字符串和 delta 文本", async () => {
    const { createAiChannel } = await import("../ipc/events");
    const events: Record<string, unknown>[] = [];
    const channel = createAiChannel((event) => events.push(event));
    await ready(channel);
    const push = handlerAt();

    push({ data: { type: "done" } });
    push({ data: '{"type":"confirmRequired","jobId":"job-1"}' });
    push({ data: "正在思考" });
    push({ data: 42 });

    expect(events).toEqual([
      { type: "done" },
      { type: "confirmRequired", jobId: "job-1" },
      { type: "delta", text: "正在思考" },
    ]);
  });

  it("命名事件解包 data，dispose 返回异步退订函数", async () => {
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

  it("desktop 没有 WS reopen，但仍返回可用退订", async () => {
    const { createBinaryChannel, onChannelReopen } = await import("../ipc/events");
    const channel = createBinaryChannel(() => {});
    const reopened = vi.fn();
    const off = onChannelReopen(channel, reopened);

    off();
    expect(reopened).not.toHaveBeenCalled();
  });
});

describe("共享事件 DTO 与版本高水位", () => {
  it("events.ts 的 SessionStatusEvent/TerminalExitEvent 与 types.ts 同源（含 version）", () => {
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

  it("TerminalControlEvent 携带权威网格与事件版本（对齐 Go ControlEvent）", () => {
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

  it("TerminalThrottledEvent 的 version/channelId 必填（对齐 Go ThrottleEvent）", () => {
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

  it("EventVersionGate 接受递增版本，丢弃重放、乱序与非法版本", async () => {
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
