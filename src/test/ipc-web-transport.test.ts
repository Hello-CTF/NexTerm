import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];

  readonly url: string;
  readyState = 0;
  binaryType = "";
  onopen: ((event: unknown) => void) | null = null;
  onmessage: ((event: { data: unknown }) => void) | null = null;
  onclose: ((event: unknown) => void) | null = null;
  onerror: ((event: unknown) => void) | null = null;
  private listeners = new Map<string, Set<(event: never) => void>>();

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }

  addEventListener(type: string, listener: (event: never) => void) {
    let set = this.listeners.get(type);
    if (!set) {
      set = new Set();
      this.listeners.set(type, set);
    }
    set.add(listener);
  }

  removeEventListener(type: string, listener: (event: never) => void) {
    this.listeners.get(type)?.delete(listener);
  }

  send() {}

  close() {
    if (this.readyState >= 2) return;
    this.readyState = 3;
    this.onclose?.({});
    for (const listener of this.listeners.get("close") ?? []) listener({} as never);
  }

  serverOpen() {
    this.readyState = 1;
    this.onopen?.({});
    for (const listener of this.listeners.get("open") ?? []) listener({} as never);
  }

  serverDrop() {
    if (this.readyState >= 2) return;
    this.readyState = 3;
    this.onerror?.({});
    this.onclose?.({});
    for (const listener of this.listeners.get("error") ?? []) listener({} as never);
    for (const listener of this.listeners.get("close") ?? []) listener({} as never);
  }

  serverMessage(data: unknown) {
    this.onmessage?.({ data });
    for (const listener of this.listeners.get("message") ?? []) listener({ data } as never);
  }
}

function installWebEnv() {
  const storage = new Map<string, string>();
  vi.stubGlobal("window", {
    __NEXTERM_TRANSPORT__: "web",
    location: { search: "", protocol: "http:", host: "127.0.0.1:9" },
    localStorage: {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => void storage.set(key, value),
    },
    setTimeout: (...args: Parameters<typeof setTimeout>) => globalThis.setTimeout(...args),
    addEventListener: () => {},
  });
  vi.stubGlobal("WebSocket", FakeWebSocket);
}

beforeEach(() => {
  vi.resetModules();
  FakeWebSocket.instances = [];
  installWebEnv();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("webTransport 重连时序", () => {
  it("掉线后用同一通道 id 重连，且只有重连才通知 reopen", async () => {
    const { newBinaryChannel, onChannelReopen } = await import("../ipc/webTransport");
    const channel = newBinaryChannel();
    const reopened = vi.fn();
    onChannelReopen(channel.id, reopened);

    expect(FakeWebSocket.instances).toHaveLength(1);
    const first = FakeWebSocket.instances[0];
    expect(first.url).toContain(`/ws/channel/${encodeURIComponent(channel.id)}`);

    first.serverOpen();
    expect(reopened).not.toHaveBeenCalled();

    first.serverDrop();
    vi.advanceTimersByTime(300);
    expect(FakeWebSocket.instances).toHaveLength(2);
    const second = FakeWebSocket.instances[1];
    expect(second.url).toBe(first.url);

    second.serverOpen();
    expect(reopened).toHaveBeenCalledTimes(1);
  });

  it("重连退避按 300ms 指数增长，open 后重置", async () => {
    const { newBinaryChannel } = await import("../ipc/webTransport");
    newBinaryChannel();
    const first = FakeWebSocket.instances[0];
    first.serverOpen();
    first.serverDrop();

    vi.advanceTimersByTime(299);
    expect(FakeWebSocket.instances).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(FakeWebSocket.instances).toHaveLength(2);

    FakeWebSocket.instances[1].serverDrop();
    vi.advanceTimersByTime(599);
    expect(FakeWebSocket.instances).toHaveLength(2);
    vi.advanceTimersByTime(1);
    expect(FakeWebSocket.instances).toHaveLength(3);

    FakeWebSocket.instances[2].serverOpen();
    FakeWebSocket.instances[2].serverDrop();
    vi.advanceTimersByTime(300);
    expect(FakeWebSocket.instances).toHaveLength(4);
  });

  it("dispose 后不再重连、不再通知", async () => {
    const { newBinaryChannel, onChannelReopen, disposeChannel } = await import("../ipc/webTransport");
    const channel = newBinaryChannel();
    const reopened = vi.fn();
    const off = onChannelReopen(channel.id, reopened);
    const first = FakeWebSocket.instances[0];
    first.serverOpen();

    disposeChannel(channel.id);
    first.serverDrop();
    vi.advanceTimersByTime(60_000);

    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(reopened).not.toHaveBeenCalled();
    off();
  });

  it("重连成功后二进制帧与 JSON 文本帧仍按序解码投递", async () => {
    const { newBinaryChannel } = await import("../ipc/webTransport");
    const channel = newBinaryChannel();
    const received: unknown[] = [];
    channel.onmessage = (message) => received.push(message);
    const first = FakeWebSocket.instances[0];
    first.serverOpen();
    first.serverDrop();
    vi.advanceTimersByTime(300);
    const second = FakeWebSocket.instances[1];
    second.serverOpen();

    second.serverMessage(new Uint8Array([1, 2, 3]).buffer);
    second.serverMessage(JSON.stringify({ type: "delta", text: "x" }));

    expect(received).toEqual([new Uint8Array([1, 2, 3]), { type: "delta", text: "x" }]);
  });

  it("单个 reopen 订阅者抛错不影响其他订阅者", async () => {
    const { newBinaryChannel, onChannelReopen } = await import("../ipc/webTransport");
    const channel = newBinaryChannel();
    const good = vi.fn();
    onChannelReopen(channel.id, () => {
      throw new Error("boom");
    });
    onChannelReopen(channel.id, good);

    const first = FakeWebSocket.instances[0];
    first.serverOpen();
    first.serverDrop();
    vi.advanceTimersByTime(300);
    FakeWebSocket.instances[1].serverOpen();

    expect(good).toHaveBeenCalledTimes(1);
  });

  it("events.ts 的 WEB 通道把重连通知接到 onChannelReopen（验收同一路径）", async () => {
    const events = await import("../ipc/events");
    const frames: number[][] = [];
    const channel = events.createBinaryChannel((bytes) => frames.push(Array.from(bytes)));
    const reopened = vi.fn();
    events.onChannelReopen(channel, reopened);

    const first = FakeWebSocket.instances[0];
    first.serverOpen();
    first.serverDrop();
    vi.advanceTimersByTime(300);
    expect(FakeWebSocket.instances).toHaveLength(2);
    FakeWebSocket.instances[1].serverOpen();
    expect(reopened).toHaveBeenCalledTimes(1);

    FakeWebSocket.instances[1].serverMessage(new Uint8Array([0x6e, 0x78]).buffer);
    expect(frames).toEqual([[0x6e, 0x78]]);

    events.disposeChannel(channel);
    FakeWebSocket.instances[1].serverDrop();
    vi.advanceTimersByTime(60_000);
    expect(FakeWebSocket.instances).toHaveLength(2);
  });
});
