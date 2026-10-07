/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  PublicShareConnection,
  publicShareWsUrl,
  type PublicShareCloseInfo,
  type PublicShareFailure,
  type PublicShareReady,
} from "../../ipc/publicShareApi";

type Listener = (event: { data: unknown } | { code: number; reason: string; wasClean: boolean }) => void;

class FakeWebSocket {
  static readonly OPEN = 1;
  static instances: FakeWebSocket[] = [];

  readonly url: string;
  binaryType = "blob";
  readyState = FakeWebSocket.OPEN;
  sent: unknown[] = [];
  closeCalls: Array<{ code?: number; reason?: string }> = [];
  private listeners = new Map<string, Set<Listener>>();

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }

  addEventListener(type: string, listener: Listener): void {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set());
    this.listeners.get(type)!.add(listener);
  }

  send(data: unknown): void {
    this.sent.push(data);
  }

  close(code?: number, reason?: string): void {
    this.closeCalls.push({ code, reason });
    this.readyState = 3;
  }

  emitMessage(data: unknown): void {
    for (const listener of this.listeners.get("message") ?? []) listener({ data });
  }

  emitClose(code: number, reason: string, wasClean: boolean): void {
    this.readyState = 3;
    for (const listener of this.listeners.get("close") ?? []) listener({ code, reason, wasClean });
  }
}

interface Recorded {
  ready: PublicShareReady[];
  output: Uint8Array[];
  errors: PublicShareFailure[];
  closes: PublicShareCloseInfo[];
}

function connect(token: string): { socket: FakeWebSocket; recorded: Recorded; connection: PublicShareConnection } {
  const recorded: Recorded = { ready: [], output: [], errors: [], closes: [] };
  const connection = new PublicShareConnection(token, {
    onReady: (ready) => recorded.ready.push(ready),
    onOutput: (bytes) => recorded.output.push(bytes),
    onError: (failure) => recorded.errors.push(failure),
    onClose: (info) => recorded.closes.push(info),
  });
  const socket = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
  return { socket, recorded, connection };
}

beforeEach(() => {
  FakeWebSocket.instances = [];
  vi.stubGlobal("WebSocket", FakeWebSocket);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("publicShareWsUrl", () => {
  it("builds a same-origin ws URL with the token percent-encoded", () => {
    const url = publicShareWsUrl("abc/123 ?=x");
    expect(url).toBe(`${window.location.protocol === "https:" ? "wss" : "ws"}://${window.location.host}/share/public/abc%2F123%20%3F%3Dx`);
  });

  it("honors the api base override", () => {
    window.history.replaceState({}, "", "/?api=http%3A%2F%2F10.0.0.8%3A8080");
    expect(publicShareWsUrl("tok")).toBe("ws://10.0.0.8:8080/share/public/tok");
    window.history.replaceState({}, "", "/");
  });
});

describe("PublicShareConnection contract", () => {
  it("opens a binary socket at the share endpoint", () => {
    const { socket } = connect("tok-1");
    expect(socket.url).toContain("/share/public/tok-1");
    expect(socket.binaryType).toBe("arraybuffer");
  });

  it("parses the ready text frame", () => {
    const { socket, recorded } = connect("tok");
    socket.emitMessage(JSON.stringify({ type: "ready", session_id: "s-42", permission: "read_write", expires_at: 1790000000000 }));
    expect(recorded.ready).toEqual([{ sessionId: "s-42", permission: "read_write", expiresAt: 1790000000000 }]);
  });

  it("defaults anything but explicit read_write to read", () => {
    const { socket, recorded } = connect("tok");
    socket.emitMessage(JSON.stringify({ type: "ready", session_id: "s", permission: "admin", expires_at: 1 }));
    expect(recorded.ready[0].permission).toBe("read");
  });

  it("delivers binary frames as terminal output bytes", () => {
    const { socket, recorded } = connect("tok");
    const bytes = new Uint8Array([0x1b, 0x5b, 0x33, 0x31, 0x6d]);
    socket.emitMessage(bytes.buffer.slice(0));
    expect(recorded.output).toHaveLength(1);
    expect(Array.from(recorded.output[0])).toEqual([0x1b, 0x5b, 0x33, 0x31, 0x6d]);
  });

  it("parses error text frames", () => {
    const { socket, recorded } = connect("tok");
    socket.emitMessage(JSON.stringify({ type: "error", code: "expired", message: "分享授权已过期" }));
    expect(recorded.errors).toEqual([{ code: "expired", message: "分享授权已过期" }]);
  });

  it("ignores unknown and malformed text frames without throwing", () => {
    const { socket, recorded } = connect("tok");
    socket.emitMessage("not json {");
    socket.emitMessage(JSON.stringify({ type: "mystery" }));
    socket.emitMessage(JSON.stringify("ready"));
    expect(recorded.ready).toEqual([]);
    expect(recorded.errors).toEqual([]);
  });

  it("sends input as binary frames with UTF-8 bytes", () => {
    const { socket, connection } = connect("tok");
    connection.sendInput("ls -l\n");
    expect(socket.sent).toHaveLength(1);
    const payload = socket.sent[0] as Uint8Array;
    expect(Array.from(payload)).toEqual(Array.from(new TextEncoder().encode("ls -l\n")));
    expect(new TextDecoder().decode(payload)).toBe("ls -l\n");
  });

  it("passes through raw byte input unchanged", () => {
    const { socket, connection } = connect("tok");
    const bytes = new Uint8Array([0x00, 0xff, 0x10]);
    connection.sendInput(bytes);
    expect(socket.sent[0]).toBe(bytes);
  });

  it("drops input while the socket is not open", () => {
    const { socket, connection } = connect("tok");
    socket.readyState = 2;
    connection.sendInput("x");
    expect(socket.sent).toEqual([]);
  });

  it("reports close code, reason and cleanliness", () => {
    const { socket, recorded } = connect("tok");
    socket.emitClose(1000, "share ended", true);
    expect(recorded.closes).toEqual([{ code: 1000, reason: "share ended", wasClean: true }]);
  });

  it("close() closes the underlying socket", () => {
    const { socket, connection } = connect("tok");
    connection.close();
    expect(socket.closeCalls).toHaveLength(1);
  });

  it("never writes the token to web storage", () => {
    connect("secret-token-value");
    expect(window.localStorage.length).toBe(0);
    expect(window.sessionStorage.length).toBe(0);
  });
});
