/** @vitest-environment jsdom */
// Preview WS protocol contract: the ready frame carries the real permission and optional lease_ms, control frames
// deliver lease state, viewer input is a binary frame (sendInput), and an error frame with code:"read_only"
// is a non-terminal notice and the connection continues. The token is only used for URL assembly, never web storage.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  PreviewConnection,
  type PreviewCloseInfo,
  type PreviewControl,
  type PreviewFailure,
  type PreviewReady,
  type PreviewSnapshot,
} from "../../features/sharing/previewApi";

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

  emitText(frame: unknown): void {
    this.emitMessage(JSON.stringify(frame));
  }

  emitClose(code: number, reason: string, wasClean: boolean): void {
    this.readyState = 3;
    for (const listener of this.listeners.get("close") ?? []) listener({ code, reason, wasClean });
  }
}

interface Recorded {
  ready: PreviewReady[];
  snapshots: PreviewSnapshot[];
  output: Uint8Array[];
  errors: PreviewFailure[];
  closes: PreviewCloseInfo[];
  controls: PreviewControl[];
}

function connect(token: string, withControl = true): { socket: FakeWebSocket; recorded: Recorded; connection: PreviewConnection } {
  const recorded: Recorded = { ready: [], snapshots: [], output: [], errors: [], closes: [], controls: [] };
  const connection = new PreviewConnection(token, {
    onReady: (ready) => recorded.ready.push(ready),
    onSnapshot: (snapshot) => recorded.snapshots.push(snapshot),
    onOutput: (bytes) => recorded.output.push(bytes),
    onError: (failure) => recorded.errors.push(failure),
    onClose: (info) => recorded.closes.push(info),
    ...(withControl ? { onControl: (control: PreviewControl) => recorded.controls.push(control) } : {}),
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

describe("PreviewConnection contract", () => {
  it("opens a binary socket at the preview endpoint with the token percent-encoded", () => {
    const { socket } = connect("abc/123 ?=");
    expect(socket.url).toContain("/share/preview/abc%2F123%20%3F%3D");
    expect(socket.binaryType).toBe("arraybuffer");
  });

  it("parses the ready frame with the real permission and lease_ms", () => {
    const { socket, recorded } = connect("tok");
    socket.emitText({ type: "ready", session_id: "s-42", permission: "read_write", expires_at: 1790000000000, lease_ms: 30000 });
    expect(recorded.ready).toEqual([{ sessionId: "s-42", permission: "read_write", expiresAt: 1790000000000, leaseMs: 30000 }]);
  });

  it("omits leaseMs when the ready frame carries no lease_ms", () => {
    const { socket, recorded } = connect("tok");
    socket.emitText({ type: "ready", session_id: "s-1", permission: "read", expires_at: 1 });
    expect(recorded.ready[0]).toEqual({ sessionId: "s-1", permission: "read", expiresAt: 1 });
    expect(recorded.ready[0]?.leaseMs).toBeUndefined();
  });

  it("degrades an unknown ready permission to read", () => {
    const { socket, recorded } = connect("tok");
    socket.emitText({ type: "ready", session_id: "s", permission: "admin", expires_at: 1 });
    expect(recorded.ready[0]?.permission).toBe("read");
  });

  it("degrades a missing ready permission to read", () => {
    const { socket, recorded } = connect("tok");
    socket.emitText({ type: "ready", session_id: "s", expires_at: 1 });
    expect(recorded.ready[0]?.permission).toBe("read");
  });

  it("parses the snapshot frame", () => {
    const { socket, recorded } = connect("tok");
    socket.emitText({ type: "snapshot", lines: ["alpha", "prompt$"], cursor_row: 1, cursor_col: 7, cols: 80, rows: 24 });
    expect(recorded.snapshots).toEqual([{ lines: ["alpha", "prompt$"], cursorRow: 1, cursorCol: 7, cols: 80, rows: 24 }]);
  });

  it("delivers binary frames as terminal output bytes", () => {
    const { socket, recorded } = connect("tok");
    const bytes = new Uint8Array([0x1b, 0x5b, 0x33, 0x31, 0x6d]);
    socket.emitMessage(bytes.buffer.slice(0));
    expect(recorded.output).toHaveLength(1);
    expect(Array.from(recorded.output[0])).toEqual([0x1b, 0x5b, 0x33, 0x31, 0x6d]);
  });

  it("dispatches control frames with state and lease_until", () => {
    const { socket, recorded } = connect("tok");
    socket.emitText({ type: "control", state: "owner", lease_until: 1790000030000 });
    socket.emitText({ type: "control", state: "busy" });
    socket.emitText({ type: "control", state: "guest" });
    expect(recorded.controls).toEqual([
      { state: "owner", leaseUntil: 1790000030000 },
      { state: "busy" },
      { state: "guest" },
    ]);
    expect(recorded.controls[1]?.leaseUntil).toBeUndefined();
  });

  it("degrades an unknown control state to guest", () => {
    const { socket, recorded } = connect("tok");
    socket.emitText({ type: "control", state: "root" });
    expect(recorded.controls[0]?.state).toBe("guest");
  });

  it("tolerates control frames when no onControl handler is registered", () => {
    const { socket } = connect("tok", false);
    expect(() => socket.emitText({ type: "control", state: "owner", lease_until: 1 })).not.toThrow();
  });

  it("passes a non-terminal read_only error frame through without closing", () => {
    const { socket, recorded } = connect("tok");
    socket.emitText({ type: "error", code: "read_only", message: "preview is read-only" });
    expect(recorded.errors).toEqual([{ code: "read_only", message: "preview is read-only" }]);
    expect(recorded.closes).toEqual([]);
    socket.emitText({ type: "control", state: "owner" });
    expect(recorded.controls).toEqual([{ state: "owner" }]);
  });

  it("parses terminal error frames with the original code/message contract", () => {
    const { socket, recorded } = connect("tok");
    socket.emitText({ type: "error", code: "expired", message: "share link expired" });
    expect(recorded.errors).toEqual([{ code: "expired", message: "share link expired" }]);
  });

  it("ignores unknown and malformed text frames without throwing", () => {
    const { socket, recorded } = connect("tok");
    socket.emitMessage("not json {");
    socket.emitText({ type: "mystery" });
    socket.emitMessage(JSON.stringify("ready"));
    expect(recorded.ready).toEqual([]);
    expect(recorded.errors).toEqual([]);
    expect(recorded.controls).toEqual([]);
  });

  it("sends string input as a binary frame with UTF-8 bytes", () => {
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
    connection.sendInput(new Uint8Array([0x01]));
    expect(socket.sent).toEqual([]);
  });

  it("reports close code, reason and cleanliness", () => {
    const { socket, recorded } = connect("tok");
    socket.emitClose(1000, "preview ended", true);
    expect(recorded.closes).toEqual([{ code: 1000, reason: "preview ended", wasClean: true }]);
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
