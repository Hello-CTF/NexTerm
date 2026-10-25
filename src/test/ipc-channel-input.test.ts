import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];

  readyState = 0;
  binaryType = "";
  protocol = "";
  bufferedAmount = 0;
  sent: Uint8Array[] = [];
  onopen: ((event: unknown) => void) | null = null;
  onmessage: ((event: { data: unknown }) => void) | null = null;
  onclose: ((event: unknown) => void) | null = null;
  onerror: ((event: unknown) => void) | null = null;

  constructor(
    readonly url: string,
    readonly protocols: string[],
  ) {
    FakeWebSocket.instances.push(this);
  }

  addEventListener() {}

  send(data: Uint8Array) {
    this.sent.push(data);
  }

  close() {
    this.readyState = 3;
    this.onclose?.({});
  }

  open(protocol: string) {
    this.readyState = 1;
    this.protocol = protocol;
    this.onopen?.({});
  }
}

beforeEach(() => {
  vi.resetModules();
  FakeWebSocket.instances = [];
  vi.stubGlobal("window", {
    __NEXTERM_TRANSPORT__: "web",
    location: { search: "", protocol: "http:", host: "127.0.0.1:9" },
    localStorage: { getItem: () => null, setItem: () => {} },
    setTimeout: (...args: Parameters<typeof setTimeout>) => globalThis.setTimeout(...args),
    addEventListener: () => {},
  });
  vi.stubGlobal("WebSocket", FakeWebSocket);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const bytes = (...v: number[]) => new Uint8Array(v);

describe("channel socket terminal input", () => {
  it("merges same-tick writes into one frame and decodes the tab header", async () => {
    const { newBinaryChannel, sendChannelInput } = await import("../ipc/webTransport");
    const ch = newBinaryChannel();
    const ws = FakeWebSocket.instances[0];
    expect(ws.protocols).toEqual(["nexterm.input", "nexterm"]);
    expect(ws.url).toContain("?client=");
    ws.open("nexterm.input");
    const http = vi.fn().mockResolvedValue(undefined);
    sendChannelInput(ch.id, "tab", bytes(1), http);
    sendChannelInput(ch.id, "tab", bytes(2, 3), http);
    expect(ws.sent).toHaveLength(0);
    await Promise.resolve();
    expect(http).not.toHaveBeenCalled();
    expect(ws.sent).toHaveLength(1);
    expect(Array.from(ws.sent[0])).toEqual([1, 3, ...new TextEncoder().encode("tab"), 1, 2, 3]);
  });

  it("falls back to HTTP when the server did not negotiate input", async () => {
    const { newBinaryChannel, sendChannelInput } = await import("../ipc/webTransport");
    const ch = newBinaryChannel();
    const ws = FakeWebSocket.instances[0];
    ws.open("nexterm");
    const http = vi.fn().mockResolvedValue(undefined);
    sendChannelInput(ch.id, "tab", bytes(7), http);
    await Promise.resolve();
    expect(ws.sent).toHaveLength(0);
    expect(http).toHaveBeenCalledWith(bytes(7));
  });

  it("keeps order when the socket opens while an HTTP write is in flight", async () => {
    const { newBinaryChannel, sendChannelInput } = await import("../ipc/webTransport");
    const ch = newBinaryChannel();
    const ws = FakeWebSocket.instances[0];
    let release!: () => void;
    const http = vi.fn().mockImplementation(
      () => new Promise<void>((resolve) => (release = resolve)),
    );
    sendChannelInput(ch.id, "tab", bytes(1), http);
    await Promise.resolve();
    expect(http).toHaveBeenCalledTimes(1);
    ws.open("nexterm.input");
    sendChannelInput(ch.id, "tab", bytes(2), http);
    await Promise.resolve();
    expect(ws.sent).toHaveLength(0);
    release();
    await vi.waitFor(() => expect(ws.sent).toHaveLength(1));
    expect(http).toHaveBeenCalledTimes(1);
    expect(Array.from(ws.sent[0]).slice(-1)).toEqual([2]);
  });

  it("splits oversized writes into ordered frames", async () => {
    const { newBinaryChannel, sendChannelInput } = await import("../ipc/webTransport");
    const ch = newBinaryChannel();
    const ws = FakeWebSocket.instances[0];
    ws.open("nexterm.input");
    sendChannelInput(ch.id, "t", new Uint8Array(40 * 1024).fill(9), vi.fn());
    await Promise.resolve();
    expect(ws.sent.map((f) => f.length - 3)).toEqual([16384, 16384, 8192]);
  });

  it("never re-addresses queued input when the tab changes during an in-flight POST", async () => {
    const { newBinaryChannel, sendChannelInput } = await import("../ipc/webTransport");
    const ch = newBinaryChannel();
    const ws = FakeWebSocket.instances[0];
    const releases: Array<() => void> = [];
    const httpA = vi.fn().mockImplementation(() => new Promise<void>((r) => releases.push(r)));
    const httpB = vi.fn().mockResolvedValue(undefined);
    sendChannelInput(ch.id, "A", bytes(1), httpA);
    await Promise.resolve();
    sendChannelInput(ch.id, "A", bytes(2), httpA);
    sendChannelInput(ch.id, "B", bytes(3), httpB);
    await Promise.resolve();
    expect(httpA).toHaveBeenCalledTimes(1);
    expect(httpB).not.toHaveBeenCalled();
    releases[0]();
    await vi.waitFor(() => expect(httpA).toHaveBeenCalledTimes(2));
    expect(httpA).toHaveBeenLastCalledWith(bytes(2));
    releases[1]();
    await vi.waitFor(() => expect(httpB).toHaveBeenCalledWith(bytes(3)));
    expect(ws.sent).toHaveLength(0);
  });

  it("waits for the socket buffer to drain instead of falling back to HTTP", async () => {
    vi.useFakeTimers();
    try {
      const { newBinaryChannel, sendChannelInput } = await import("../ipc/webTransport");
      const ch = newBinaryChannel();
      const ws = FakeWebSocket.instances[0];
      ws.open("nexterm.input");
      const http = vi.fn().mockResolvedValue(undefined);
      ws.bufferedAmount = 300 * 1024;
      sendChannelInput(ch.id, "t", bytes(1, 2), http);
      await Promise.resolve();
      sendChannelInput(ch.id, "t", bytes(3), http);
      await vi.advanceTimersByTimeAsync(50);
      expect(ws.sent).toHaveLength(0);
      expect(http).not.toHaveBeenCalled();
      ws.bufferedAmount = 0;
      await vi.advanceTimersByTimeAsync(20);
      expect(http).not.toHaveBeenCalled();
      expect(ws.sent.map((f) => Array.from(f.subarray(3)))).toEqual([[1, 2, 3]]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops pausing mid-paste when the buffer fills and resumes in order", async () => {
    vi.useFakeTimers();
    try {
      const { newBinaryChannel, sendChannelInput } = await import("../ipc/webTransport");
      const ch = newBinaryChannel();
      const ws = FakeWebSocket.instances[0];
      ws.open("nexterm.input");
      const http = vi.fn().mockResolvedValue(undefined);
      const send = ws.send.bind(ws);
      ws.send = (data: Uint8Array) => {
        send(data);
        ws.bufferedAmount = 300 * 1024;
      };
      const big = new Uint8Array(40 * 1024);
      big.forEach((_, i) => (big[i] = i % 251));
      sendChannelInput(ch.id, "t", big, http);
      await Promise.resolve();
      expect(ws.sent).toHaveLength(1);
      ws.bufferedAmount = 0;
      await vi.advanceTimersByTimeAsync(10);
      expect(ws.sent).toHaveLength(2);
      ws.bufferedAmount = 0;
      await vi.advanceTimersByTimeAsync(10);
      const joined = ws.sent.flatMap((f) => Array.from(f.subarray(3)));
      expect(joined).toEqual(Array.from(big));
      expect(http).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it("drops input beyond the pending cap with a warning", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const { newBinaryChannel, sendChannelInput } = await import("../ipc/webTransport");
    const ch = newBinaryChannel();
    const http = vi.fn().mockImplementation(() => new Promise(() => {}));
    sendChannelInput(ch.id, "t", bytes(1), http);
    await Promise.resolve();
    sendChannelInput(ch.id, "t", new Uint8Array(5 * 1024 * 1024), http);
    expect(warn).toHaveBeenCalled();
    warn.mockRestore();
  });
});
