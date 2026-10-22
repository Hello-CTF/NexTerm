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

describe("webTransport reconnection timing", () => {
  it("reconnects with the same channel ID after a drop and notifies reopen only for reconnections", async () => {
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

  it("increases reconnect backoff exponentially from 300ms and resets it after open", async () => {
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

  it("does not reconnect or notify after dispose", async () => {
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

  it("continues decoding and delivering binary and JSON text frames in order after reconnecting", async () => {
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

  it("one reopen subscriber throwing does not affect other subscribers", async () => {
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

  it("routes WEB channel reconnect notifications from events.ts to onChannelReopen (acceptance path)", async () => {
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

  it("does not apply desktop base64 decoding to WebSocket messages", async () => {
    const events = await import("../ipc/events");
    const frames: number[][] = [];
    const channel = events.createBinaryChannel((bytes) => frames.push(Array.from(bytes)));
    const socket = FakeWebSocket.instances[0];
    socket.serverOpen();

    socket.serverMessage(JSON.stringify({ type: "nexterm.desktop-binary", data: btoa("nx") }));
    socket.serverMessage("bnh4");
    socket.serverMessage(new Uint8Array([0, 255]).buffer);

    expect(frames).toEqual([
      [98, 110, 104, 52],
      [0, 255],
    ]);
    events.disposeChannel(channel);
  });
});

describe("events reconnection recovery cursor", () => {
  it("advances the cursor from event IDs and resumes with since after reconnecting", async () => {
    const { subscribeEvent } = await import("../ipc/webTransport");
    const seen: unknown[] = [];
    subscribeEvent("session://status", (payload) => seen.push(payload));
    const first = FakeWebSocket.instances[0];
    expect(first.url).toContain("/ws/events");
    expect(first.url).not.toContain("since=");
    first.serverOpen();
    first.serverMessage(JSON.stringify({ id: 1, event: "session://status", payload: { a: 1 } }));
    first.serverMessage(JSON.stringify({ id: 2, event: "session://status", payload: { a: 2 } }));
    expect(seen).toEqual([{ a: 1 }, { a: 2 }]);

    first.serverDrop();
    vi.advanceTimersByTime(300);
    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(FakeWebSocket.instances[1].url).toContain("/ws/events?since=2");
  });

  it("updates the cursor and notifies subscribers on resync, then continues dispatching events", async () => {
    const { subscribeEvent, onEventsResync } = await import("../ipc/webTransport");
    const seen: unknown[] = [];
    const resynced = vi.fn();
    subscribeEvent("session://status", (payload) => seen.push(payload));
    onEventsResync(resynced);
    const first = FakeWebSocket.instances[0];
    first.serverOpen();
    first.serverMessage(JSON.stringify({ resync: true, id: 7 }));
    expect(resynced).toHaveBeenCalledTimes(1);
    expect(seen).toEqual([]);

    first.serverDrop();
    vi.advanceTimersByTime(300);
    expect(FakeWebSocket.instances[1].url).toContain("/ws/events?since=7");

    FakeWebSocket.instances[1].serverOpen();
    FakeWebSocket.instances[1].serverMessage(JSON.stringify({ id: 8, event: "session://status", payload: { b: 1 } }));
    expect(seen).toEqual([{ b: 1 }]);
  });

  it("a resync subscriber throwing does not affect later event dispatch", async () => {
    const { subscribeEvent, onEventsResync } = await import("../ipc/webTransport");
    const seen: unknown[] = [];
    subscribeEvent("session://status", (payload) => seen.push(payload));
    const off = onEventsResync(() => {
      throw new Error("boom");
    });
    const first = FakeWebSocket.instances[0];
    first.serverOpen();
    first.serverMessage(JSON.stringify({ resync: true, id: 3 }));
    first.serverMessage(JSON.stringify({ id: 4, event: "session://status", payload: { c: 1 } }));
    expect(seen).toEqual([{ c: 1 }]);
    off();
  });
});
