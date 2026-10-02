import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import layoutSource from "../app/layout.ts?raw";
import { createWailsMock, type WailsMock } from "./wailsMock";

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
  it("保留八个共享事件和 layout 第九事件", async () => {
    const { EVENTS } = await import("../ipc/events");
    expect(Object.values(EVENTS)).toEqual([
      "session://status",
      "terminal://exit",
      "terminal://throttled",
      "terminal://control",
      "fs://progress",
      "docker://stats",
      "ai://event",
      "app://error",
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

  it("demo 仍使用内存通道，不加载 Wails listener", async () => {
    vi.resetModules();
    vi.stubGlobal("window", { location: { search: "?demo=1" } });
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const { createBinaryChannel } = await import("../ipc/events");
    const frames: number[][] = [];
    const channel = createBinaryChannel((bytes) => frames.push(Array.from(bytes)));

    channel.onmessage([9, 8]);
    expect(channel.toJSON()).toBeNull();
    expect(frames).toEqual([[9, 8]]);
    expect(runtime.Events.On).not.toHaveBeenCalled();
  });
});
