/** @vitest-environment jsdom */

import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mount, flush, clickButton, type MountedView } from "./reactTestUtils";
import { PublicShareApp } from "../../features/sharing/PublicShareApp";

const harness = vi.hoisted(() => ({
  terminals: [] as Array<{
    writes: unknown[];
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
      dataCallbacks: Array<(data: string) => void>;
      selectionCallbacks: Array<() => void>;
      selection: string;
      disposed: boolean;
    };

    constructor(options: Record<string, unknown>) {
      this.options = options;
      this.state = { writes: [], dataCallbacks: [], selectionCallbacks: [], selection: "", disposed: false };
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

const TOKEN = "test-share-token";

function readyFrame(permission: "read" | "read_write" = "read") {
  return { type: "ready", session_id: "s-1", permission, expires_at: 1790000000000 };
}

async function mountViewer(): Promise<MountedView> {
  const view = mount(createElement(PublicShareApp, { token: TOKEN }));
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
  await flush();
  return socket;
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

describe("PublicShareApp", () => {
  it("shows connecting copy before the ready frame", async () => {
    const view = await mountViewer();
    expect(view.container.textContent).toContain("正在连接分享终端");
    expect(lastSocket().url).toContain("/share/public/test-share-token");
  });

  it("shows the read-only badge and expiry once ready", async () => {
    const view = await mountViewer();
    await goLive("read");
    expect(view.container.textContent).toContain("只读");
    expect(view.container.textContent).toContain("有效期至");
  });

  it("shows the read_write badge for writable grants", async () => {
    const view = await mountViewer();
    await goLive("read_write");
    expect(view.container.textContent).toContain("可读写");
  });

  it("writes binary output into the terminal", async () => {
    await mountViewer();
    const socket = await goLive("read");
    const terminal = lastTerminal();
    socket.emitBinary(new TextEncoder().encode("hello share"));
    await flush();
    expect(terminal.writes).toHaveLength(1);
    expect(new TextDecoder().decode(terminal.writes[0] as Uint8Array)).toBe("hello share");
  });

  it("blocks terminal input in read-only mode", async () => {
    await mountViewer();
    await goLive("read");
    const terminal = lastTerminal();
    terminal.dataCallbacks.forEach((callback) => callback("ls\r"));
    await flush();
    expect(lastSocket().sent).toEqual([]);
  });

  it("sends terminal input as binary frames in read_write mode", async () => {
    await mountViewer();
    const socket = await goLive("read_write");
    const terminal = lastTerminal();
    terminal.dataCallbacks.forEach((callback) => callback("ls\r"));
    await flush();
    expect(socket.sent).toHaveLength(1);
    expect(new TextDecoder().decode(socket.sent[0] as Uint8Array)).toBe("ls\r");
  });

  it("maps an expired error frame to plain copy without retry", async () => {
    const view = await mountViewer();
    lastSocket().emitText({ type: "error", code: "expired", message: "分享授权已过期" });
    await flush();
    expect(view.container.textContent).toContain("分享链接已过期");
    expect(view.container.textContent).toContain("请让分享者重新生成链接");
    expect(view.container.querySelector("button")).toBeNull();
  });

  it("maps a not_found error frame to plain copy", async () => {
    const view = await mountViewer();
    lastSocket().emitText({ type: "error", code: "not_found", message: "分享会话不存在" });
    await flush();
    expect(view.container.textContent).toContain("分享链接无效或已吊销");
  });

  it("maps a disconnected error frame and reconnects on retry", async () => {
    const view = await mountViewer();
    await goLive("read");
    lastSocket().emitText({ type: "error", code: "disconnected", message: "设备代理离线" });
    await flush();
    expect(view.container.textContent).toContain("设备当前离线");
    clickButton(view.container, "重新连接");
    await flush();
    expect(FakeWebSocket.instances).toHaveLength(2);
    lastSocket().emitText(readyFrame("read"));
    await flush();
    expect(view.container.textContent).toContain("只读");
  });

  it("shows ended copy after a normal close", async () => {
    const view = await mountViewer();
    const socket = await goLive("read");
    socket.emitClose(1000, "share ended", true);
    await flush();
    expect(view.container.textContent).toContain("分享已结束");
    expect(view.container.querySelector("button")).toBeNull();
  });

  it("shows disconnected copy with retry after an abnormal close", async () => {
    const view = await mountViewer();
    const socket = await goLive("read");
    socket.emitClose(1006, "", false);
    await flush();
    expect(view.container.textContent).toContain("连接已断开");
    clickButton(view.container, "重新连接");
    await flush();
    expect(FakeWebSocket.instances).toHaveLength(2);
  });

  it("treats a close before ready as a failed connect with retry", async () => {
    const view = await mountViewer();
    lastSocket().emitClose(1006, "", false);
    await flush();
    expect(view.container.textContent).toContain("无法连接分享终端");
    expect(view.container.textContent).toContain("链接可能无效、已过期或已吊销");
    clickButton(view.container, "重新连接");
    await flush();
    expect(FakeWebSocket.instances).toHaveLength(2);
  });

  it("copies the user selection to the local clipboard and shows a hint", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(window.navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    const view = await mountViewer();
    await goLive("read");
    const terminal = lastTerminal();
    terminal.selection = "selected text";
    terminal.selectionCallbacks.forEach((callback) => callback());
    await flush();
    expect(writeText).toHaveBeenCalledWith("selected text");
    expect(view.container.textContent).toContain("已复制 13 个字符");
  });

  it("shows a manual-copy hint when clipboard access fails", async () => {
    const writeText = vi.fn().mockRejectedValue(new Error("denied"));
    Object.defineProperty(window.navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    const view = await mountViewer();
    await goLive("read");
    const terminal = lastTerminal();
    terminal.selection = "text";
    terminal.selectionCallbacks.forEach((callback) => callback());
    await flush();
    expect(view.container.textContent).toContain("复制失败");
  });

  it("never renders or persists the token", async () => {
    const view = await mountViewer();
    await goLive("read_write");
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
