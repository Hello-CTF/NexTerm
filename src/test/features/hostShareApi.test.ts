/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  createHostShareConnection,
  hostShareIdentityScope,
  hostShareWsUrl,
  type HostShareCloseInfo,
  type HostShareFailure,
  type HostShareReady,
} from "../../ipc/hostShareApi";

type Listener = (event: { data: unknown } | { code: number; reason: string; wasClean: boolean }) => void;

class FakeWebSocket {
  static readonly OPEN = 1;
  static instances: FakeWebSocket[] = [];
  readonly url: string;
  binaryType = "blob";
  readyState = FakeWebSocket.OPEN;
  sent: unknown[] = [];
  closeCalls = 0;
  private readonly listeners = new Map<string, Set<Listener>>();

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

  close(): void {
    this.closeCalls += 1;
    this.readyState = 3;
  }

  emit(data: unknown): void {
    for (const listener of this.listeners.get("message") ?? []) listener({ data });
  }

  emitClose(code: number, reason = "", wasClean = false): void {
    this.readyState = 3;
    for (const listener of this.listeners.get("close") ?? []) listener({ code, reason, wasClean });
  }
}

function connect(deviceId = "device-1") {
  const ready: HostShareReady[] = [];
  const output: Uint8Array[] = [];
  const errors: HostShareFailure[] = [];
  const closes: HostShareCloseInfo[] = [];
  const connection = createHostShareConnection(deviceId, {
    onReady: (value) => ready.push(value),
    onOutput: (value) => output.push(value),
    onError: (value) => errors.push(value),
    onClose: (value) => closes.push(value),
  });
  const socket = FakeWebSocket.instances.at(-1)!;
  return { connection, socket, ready, output, errors, closes };
}

beforeEach(() => {
  FakeWebSocket.instances = [];
  vi.stubGlobal("WebSocket", FakeWebSocket);
});

afterEach(() => vi.unstubAllGlobals());

describe("browser host share terminal connection", () => {
  it("uses only the dedicated recipient route and preserves binary output", () => {
    expect(hostShareWsUrl("a/b ?")).toContain("/share/devices/a%2Fb%20%3F/terminal");
    const { socket, output } = connect("a/b ?");
    expect(socket.url).toContain("/share/devices/a%2Fb%20%3F/terminal");
    expect(socket.url).not.toContain("/fleet/devices/");
    expect(socket.binaryType).toBe("arraybuffer");
    socket.emit(new Uint8Array([0, 0xff, 65]).buffer);
    expect(Array.from(output[0])).toEqual([0, 0xff, 65]);
  });

  it("blocks input before ready and for read-only grants", () => {
    const { connection, socket, ready } = connect();
    connection.sendInput("before-ready");
    socket.emit(JSON.stringify({ type: "ready", session_id: "s-1", permission: "read", expires_at: 10 }));
    connection.sendInput("read-only");
    expect(socket.sent).toEqual([]);
    expect(ready).toEqual([{ sessionId: "s-1", permission: "read", expiresAt: 10 }]);
  });

  it("sends UTF-8 input only for read_write and closes the socket", () => {
    const { connection, socket } = connect();
    socket.emit(JSON.stringify({ type: "ready", session_id: "s-1", permission: "read_write", expires_at: 10 }));
    connection.sendInput("echo 好\n");
    expect(new TextDecoder().decode(socket.sent[0] as Uint8Array)).toBe("echo 好\n");
    connection.close();
    connection.close();
    expect(socket.closeCalls).toBe(1);
  });

  it("forwards revocation errors and terminal close frames", () => {
    const { socket, errors, closes } = connect();
    socket.emit(JSON.stringify({ type: "error", code: "forbidden", message: "主机分享已吊销" }));
    socket.emitClose(1008, "share terminated", true);
    expect(errors).toEqual([{ code: "forbidden", message: "主机分享已吊销" }]);
    expect(closes).toEqual([{ code: 1008, reason: "share terminated", wasClean: true }]);
  });

  it("applies a repeated ready frame that shrinks read_write to read", () => {
    const { connection, socket, ready } = connect();
    socket.emit(JSON.stringify({ type: "ready", session_id: "s-1", permission: "read_write", expires_at: 10 }));
    connection.sendInput("before-shrink");
    socket.emit(JSON.stringify({ type: "ready", session_id: "s-1", permission: "read", expires_at: 10 }));
    connection.sendInput("after-shrink");
    expect(socket.sent).toHaveLength(1);
    expect(ready.at(-1)?.permission).toBe("read");
  });

  it("normalizes server URL and includes user and generation in identity scope", () => {
    const scope = hostShareIdentityScope(" HTTPS://A.Example.com/ ", "user-a", "session-a");
    expect(scope).toBe(hostShareIdentityScope("https://a.example.com", "user-a", "session-a"));
    expect(scope).not.toBe(hostShareIdentityScope("https://b.example.com", "user-a", "session-a"));
    expect(scope).not.toBe(hostShareIdentityScope("https://a.example.com", "user-a", "session-b"));
  });

  it("reports a failed upgrade, including a 401 handshake, through close", () => {
    const { socket, closes } = connect();
    socket.emitClose(1006, "unauthorized", false);
    expect(closes[0]).toEqual({ code: 1006, reason: "unauthorized", wasClean: false });
  });
});
