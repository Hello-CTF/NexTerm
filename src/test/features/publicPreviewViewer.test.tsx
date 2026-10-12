/** @vitest-environment jsdom */

import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mount, flush, clickButton, type MountedView } from "./reactTestUtils";
import { PublicPreviewApp } from "../../features/sharing/PublicPreviewApp";

const harness = vi.hoisted(() => ({
  terminals: [] as Array<{
    writes: unknown[];
    resets: number;
    dataCallbacks: Array<(data: string) => void>;
    selectionCallbacks: Array<() => void>;
    selection: string;
    disposed: boolean;
  }>,
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    options: Record<string, unknown>;
    private readonly state: {
      writes: unknown[];
      resets: number;
      dataCallbacks: Array<(data: string) => void>;
      selectionCallbacks: Array<() => void>;
      selection: string;
      disposed: boolean;
    };

    constructor(options: Record<string, unknown>) {
      this.options = options;
      this.state = { writes: [], resets: 0, dataCallbacks: [], selectionCallbacks: [], selection: "", disposed: false };
      harness.terminals.push(this.state);
    }

    open(element: HTMLElement) {
      const viewport = document.createElement("div");
      viewport.className = "xterm-viewport";
      element.append(viewport);
    }

    loadAddon() {}
    write(data: unknown) {
      this.state.writes.push(data);
    }
    writeln(data: unknown) {
      this.state.writes.push(data);
    }
    reset() {
      this.state.resets += 1;
    }
    onData(callback: (data: string) => void) {
      this.state.dataCallbacks.push(callback);
      return { dispose: () => {} };
    }
    onSelectionChange(callback: () => void) {
      this.state.selectionCallbacks.push(callback);
      return { dispose: () => {} };
    }
    getSelection() {
      return this.state.selection;
    }
    focus() {}
    dispose() {
      this.state.disposed = true;
    }
  },
}));

type Listener = (event: { data: unknown } | { code: number; reason: string; wasClean: boolean }) => void;

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  static readonly OPEN = 1;

  readonly url: string;
  binaryType = "blob";
  readyState = FakeWebSocket.OPEN;
  sent: unknown[] = [];
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

  close(): void {
    this.readyState = 3;
  }

  emitText(frame: unknown): void {
    for (const listener of this.listeners.get("message") ?? []) {
      listener({ data: JSON.stringify(frame) });
    }
  }

  emitBinary(bytes: Uint8Array): void {
    for (const listener of this.listeners.get("message") ?? []) {
      listener({ data: bytes.buffer.slice(0) });
    }
  }

  emitClose(code: number, reason: string, wasClean: boolean): void {
    this.readyState = 3;
    for (const listener of this.listeners.get("close") ?? []) {
      listener({ code, reason, wasClean });
    }
  }
}

const TOKEN = "test-preview-token";

function readyFrame(permission: "read" | "read_write" = "read") {
  return { type: "ready", session_id: "tab-1", permission, expires_at: 1790000000000 };
}

function snapshotFrame() {
  return {
    type: "snapshot",
    lines: ["alpha-line", "prompt$", "", ""],
    cursor_row: 1,
    cursor_col: 7,
    cols: 80,
    rows: 24,
  };
}

async function mountViewer(): Promise<MountedView> {
  const view = mount(createElement(PublicPreviewApp, { token: TOKEN }));
  await flush();
  return view;
}

function lastSocket(): FakeWebSocket {
  const socket = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
  if (!socket) throw new Error("no WebSocket instance");
  return socket;
}

function lastTerminal() {
  const terminal = harness.terminals[harness.terminals.length - 1];
  if (!terminal) throw new Error("no Terminal instance");
  return terminal;
}

async function goLive(permission: "read" | "read_write" = "read"): Promise<FakeWebSocket> {
  const socket = lastSocket();
  socket.emitText(readyFrame(permission));
  socket.emitText(snapshotFrame());
  await flush();
  return socket;
}

function badgeText(view: MountedView): string | null | undefined {
  return view.container.querySelector("header .rounded-full")?.textContent;
}

function decodeWrite(write: unknown): string {
  return new TextDecoder().decode(write as Uint8Array);
}

beforeEach(() => {
  FakeWebSocket.instances = [];
  harness.terminals = [];
  vi.stubGlobal("WebSocket", FakeWebSocket);
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    disconnect() {}
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("PublicPreviewApp", () => {
  it("shows connecting copy before the ready frame", async () => {
    const view = await mountViewer();
    expect(view.container.textContent).toContain("正在连接预览终端");
    expect(lastSocket().url).toContain("/share/preview/test-preview-token");
  });

  it("shows the read-only badge and expiry once ready", async () => {
    const view = await mountViewer();
    await goLive();
    expect(badgeText(view)).toBe("只读");
    expect(view.container.textContent).toContain("有效期至");
  });

  it("renders the snapshot into a fresh terminal and places the cursor", async () => {
    await mountViewer();
    await goLive();
    const terminal = lastTerminal();
    expect(terminal.resets).toBe(1);
    expect(terminal.writes).toHaveLength(1);
    expect(decodeWrite(terminal.writes[0])).toBe("alpha-line\r\nprompt$\r\n\r\n\x1b[2;8H");
  });

  it("writes binary output into the terminal after the snapshot", async () => {
    await mountViewer();
    const socket = await goLive();
    const terminal = lastTerminal();
    socket.emitBinary(new TextEncoder().encode("live-output"));
    await flush();
    expect(terminal.writes).toHaveLength(2);
    expect(decodeWrite(terminal.writes[1])).toBe("live-output");
  });

  it("read permission: keystrokes never reach the socket", async () => {
    await mountViewer();
    await goLive();
    const terminal = lastTerminal();
    terminal.dataCallbacks.forEach((callback) => callback("ls\r"));
    await flush();
    expect(lastSocket().sent).toEqual([]);
  });

  it("read_write shows the controllable badge and sends keystrokes as binary frames", async () => {
    const view = await mountViewer();
    const socket = await goLive("read_write");
    expect(badgeText(view)).toBe("可控制");
    lastTerminal().dataCallbacks.forEach((callback) => callback("ls\r"));
    await flush();
    expect(socket.sent).toHaveLength(1);
    expect(typeof socket.sent[0]).not.toBe("string");
    expect(new TextDecoder().decode(socket.sent[0] as Uint8Array)).toBe("ls\r");
  });

  it("shows a persistent hint while the sharer controls but still forwards input", async () => {
    const view = await mountViewer();
    const socket = await goLive("read_write");
    socket.emitText({ type: "control", state: "owner", lease_until: 1790000060000 });
    await flush();
    expect(badgeText(view)).toBe("分享者控制中");
    expect(view.container.textContent).toContain("分享者正在控制终端，控制权归还前你只能观看");
    lastTerminal().dataCallbacks.forEach((callback) => callback("ls\r"));
    await flush();
    expect(socket.sent).toHaveLength(1);
  });

  it("shows a persistent hint while another guest controls but still forwards input", async () => {
    const view = await mountViewer();
    const socket = await goLive("read_write");
    socket.emitText({ type: "control", state: "busy" });
    await flush();
    expect(badgeText(view)).toBe("另一访客控制中");
    expect(view.container.textContent).toContain("另一位访客正在控制终端，控制权归还前你只能观看");
    lastTerminal().dataCallbacks.forEach((callback) => callback("ls\r"));
    await flush();
    expect(socket.sent).toHaveLength(1);
  });

  it("keeps forwarding input after busy and restores the controllable badge on guest control", async () => {
    const view = await mountViewer();
    const socket = await goLive("read_write");
    socket.emitText({ type: "control", state: "busy" });
    await flush();
    expect(badgeText(view)).toBe("另一访客控制中");
    lastTerminal().dataCallbacks.forEach((callback) => callback("a"));
    await flush();
    expect(socket.sent).toHaveLength(1);
    socket.emitText({ type: "control", state: "guest" });
    await flush();
    expect(badgeText(view)).toBe("可控制");
    expect(view.container.textContent).not.toContain("控制权归还前");
    lastTerminal().dataCallbacks.forEach((callback) => callback("b"));
    await flush();
    expect(socket.sent).toHaveLength(2);
  });

  it("shows a one-shot warning for read_only errors without leaving live", async () => {
    const view = await mountViewer();
    const socket = await goLive("read_write");
    socket.emitText({ type: "error", code: "read_only", message: "当前没有控制权" });
    await flush();
    expect(view.container.textContent).toContain("当前没有控制权");
    expect(badgeText(view)).toBe("可控制");
    const terminal = lastTerminal();
    socket.emitBinary(new TextEncoder().encode("still-live"));
    await flush();
    expect(decodeWrite(terminal.writes[terminal.writes.length - 1])).toBe("still-live");
    socket.emitText({ type: "error", code: "read_only", message: "再来一次" });
    await flush();
    expect(view.container.textContent).not.toContain("再来一次");
    socket.emitText({ type: "control", state: "guest" });
    await flush();
    socket.emitText({ type: "error", code: "read_only", message: "再次没有控制权" });
    await flush();
    expect(view.container.textContent).toContain("再次没有控制权");
  });

  it("maps an expired error frame to plain copy without retry", async () => {
    const view = await mountViewer();
    lastSocket().emitText({ type: "error", code: "expired", message: "分享链接已过期" });
    await flush();
    expect(view.container.textContent).toContain("预览链接已过期");
    expect(view.container.querySelector("button")).toBeNull();
  });

  it("maps a revoked error frame to plain copy", async () => {
    const view = await mountViewer();
    lastSocket().emitText({ type: "error", code: "revoked", message: "分享链接已吊销" });
    await flush();
    expect(view.container.textContent).toContain("预览链接已吊销");
  });

  it("maps an ended error frame to ended copy", async () => {
    const view = await mountViewer();
    await goLive();
    lastSocket().emitText({ type: "error", code: "ended", message: "分享已结束" });
    await flush();
    expect(view.container.textContent).toContain("预览已结束");
  });

  it("treats a close before ready as a failed connect with retry", async () => {
    const view = await mountViewer();
    lastSocket().emitClose(1006, "", false);
    await flush();
    expect(view.container.textContent).toContain("无法连接预览终端");
    clickButton(view.container, "重新连接");
    await flush();
    expect(FakeWebSocket.instances).toHaveLength(2);
  });

  it("shows disconnected copy with retry after an abnormal close", async () => {
    const view = await mountViewer();
    const socket = await goLive();
    socket.emitClose(1006, "", false);
    await flush();
    expect(view.container.textContent).toContain("连接已断开");
    clickButton(view.container, "重新连接");
    await flush();
    expect(FakeWebSocket.instances).toHaveLength(2);
  });

  it("never renders or persists the token", async () => {
    const view = await mountViewer();
    await goLive();
    expect(view.container.textContent).not.toContain(TOKEN);
    expect(window.localStorage.length).toBe(0);
    expect(window.sessionStorage.length).toBe(0);
  });

  it("closes the socket on unmount", async () => {
    const view = await mountViewer();
    const socket = lastSocket();
    view.unmount();
    await flush();
    expect(socket.readyState).toBe(3);
    expect(lastTerminal().disposed).toBe(true);
  });
});
