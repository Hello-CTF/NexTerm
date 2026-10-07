/** @vitest-environment jsdom */
// FLEET156 设备终端账号边界测试: 清理必须挂在常驻 App 根部的 boundary 上,
// 与 DevicesView 是否打开无关 — 覆盖 logout/401 会话过期/账号 A->B, 断言旧
// 账号的终端标签关闭且桥接 detach (旧 shell 不可继续输入)。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { flush, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    fetch: vi.fn(),
    ask: vi.fn(),
  };
});

vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { DeviceTerminalBoundary } from "../../features/fleet/DeviceTerminalBoundary";
import { DeviceTerminalView } from "../../features/fleet/DeviceTerminalView";
import { useAuth } from "../../features/auth/store";
import { useUi } from "../../app/store";
import {
  SupervisorFrameKind,
  SupervisorFrameParser,
  encodeSupervisorJSONFrame,
  type SupervisorFrame,
} from "../../ipc/deviceTerminalApi";

const NOW = Date.now();

const USER = {
  id: "u-1",
  username: "alice",
  display_name: "Alice",
  role: "user" as const,
  state: "active" as const,
  must_change_password: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};

const OTHER_USER = { ...USER, id: "u-2", username: "bob" };

const DEVICE = {
  id: "d-1",
  name: "web-01",
  kind: "agent",
  created_at: NOW - 86400_000,
  last_seen_at: NOW - 20_000,
  revoked_at: 0,
  agent: {
    platform: "linux",
    app_version: "0.2.2",
    desired_autostart: true,
    terminal_enabled: true,
    current_url: "https://nexterm.example.com",
    service_state: { installed: true, enabled: true, active: true, last_reconcile_at: NOW - 60_000 },
    last_seen_at: NOW - 20_000,
    state_digest: "digest-1",
  },
};

const INFO = {
  id: "s-1",
  created_at: "2026-10-07T05:34:56.123456789+08:00",
  incarnation: "inc-1",
  cols: 80,
  rows: 24,
  dead: false,
};

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  // deferAttach 延迟 attach 响应, 让用例在 attach 在途时介入 (取消/账号切换)。
  static deferAttach = false;
  readonly url: string;
  readonly sent: Uint8Array[] = [];
  binaryType = "";
  readyState = 1;
  private parser = new SupervisorFrameParser();
  private listeners = new Map<string, ((event: unknown) => void)[]>();

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }

  addEventListener(type: string, listener: (event: unknown) => void): void {
    const list = this.listeners.get(type) ?? [];
    list.push(listener);
    this.listeners.set(type, list);
  }

  send(data: ArrayBuffer | Uint8Array): void {
    const bytes = data instanceof Uint8Array ? data.slice() : new Uint8Array(data.slice(0));
    this.sent.push(bytes);
    for (const frame of this.parser.push(bytes)) this.respond(frame);
  }

  close(): void {
    if (this.readyState === 3) return;
    this.readyState = 3;
    this.emit("close", { code: 1000, reason: "", wasClean: true });
  }

  serverSend(frame: Uint8Array): void {
    this.emit("message", { data: frame.buffer.slice(frame.byteOffset, frame.byteOffset + frame.byteLength) });
  }

  serverJSON(kind: number, message: unknown): void {
    this.serverSend(encodeSupervisorJSONFrame(kind, message));
  }

  sentFrames(): SupervisorFrame[] {
    const parser = new SupervisorFrameParser();
    const frames: SupervisorFrame[] = [];
    for (const bytes of this.sent) frames.push(...parser.push(bytes));
    return frames;
  }

  private respond(frame: SupervisorFrame): void {
    switch (frame.kind) {
      case SupervisorFrameKind.Hello:
        this.serverJSON(SupervisorFrameKind.HelloAck, { version: 2 });
        return;
      case SupervisorFrameKind.Create: {
        const msg = JSON.parse(new TextDecoder().decode(frame.payload)) as { id: string };
        this.serverJSON(SupervisorFrameKind.Created, { ...INFO, id: msg.id });
        return;
      }
      case SupervisorFrameKind.Attach: {
        const msg = JSON.parse(new TextDecoder().decode(frame.payload)) as { id: string };
        const info = { ...INFO, id: msg.id };
        if (FakeWebSocket.deferAttach) {
          setTimeout(() => this.serverJSON(SupervisorFrameKind.Attached, info), 50);
          return;
        }
        this.serverJSON(SupervisorFrameKind.Attached, info);
        return;
      }
      case SupervisorFrameKind.Input:
        this.serverJSON(SupervisorFrameKind.InputAck, { written: frame.payload.length });
        return;
      case SupervisorFrameKind.Resize:
      case SupervisorFrameKind.Kill:
      case SupervisorFrameKind.KillSession:
      case SupervisorFrameKind.Detach:
        this.serverJSON(SupervisorFrameKind.OK, {});
        return;
      default:
        this.serverJSON(SupervisorFrameKind.Error, { code: "protocol", message: `unexpected ${frame.kind}` });
    }
  }

  private emit(type: string, event: unknown): void {
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }
}

function routeDevices(devices: unknown[]): void {
  mocks.fetch.mockImplementation(async (url: string) => {
    const u = String(url);
    if (u.includes("/fleet/devices")) {
      return { ok: true, status: 200, text: async () => JSON.stringify({ devices }) };
    }
    return { ok: true, status: 200, text: async () => JSON.stringify({}) };
  });
}

function seedUser(user: typeof USER | null): void {
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth: "on" },
    user,
    dek: null,
    gate: "ready",
    pendingRecoveryKey: null,
    error: null,
  });
}

function seedTabs(): void {
  useUi.setState({
    workspaces: [
      {
        id: "ws-1",
        kind: "tools",
        title: "工具",
        panes: [
          {
            id: "pane-1",
            tabs: [
              { id: "t-1", kind: "deviceTerminal", title: "终端 · web-01", deviceId: "d-1", closable: true },
              { id: "t-2", kind: "settings", title: "设置", closable: true },
            ],
            activeTabId: "t-1",
          },
        ],
        activePaneId: "pane-1",
        splitRatio: 0.5,
        closable: true,
      },
    ],
    activeWorkspaceId: "ws-1",
  } as never);
}

function storeTabs(): { kind: string }[] {
  return useUi.getState().workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs));
}

// TabHarness 模拟 App 的标签渲染: store 里的 deviceTerminal 标签决定视图的挂载,
// 标签被 boundary 关闭时视图随之卸载 (与 App 的 PaneForTab 同一链路)。
function TabHarness() {
  const workspaces = useUi((s) => s.workspaces);
  const tabs = workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs));
  return (
    <>
      {tabs
        .filter((t) => t.kind === "deviceTerminal")
        .map((t) => (
          <DeviceTerminalView key={t.id} deviceId={(t as { deviceId?: string }).deviceId ?? "d-1"} visible />
        ))}
    </>
  );
}

let mounted: MountedView | null = null;

beforeEach(() => {
  FakeWebSocket.instances = [];
  FakeWebSocket.deferAttach = false;
  vi.stubGlobal("WebSocket", FakeWebSocket);
  vi.stubGlobal("fetch", mocks.fetch);
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    disconnect() {}
  });
  mocks.fetch.mockReset();
  document.body.replaceChildren();
  routeDevices([DEVICE]);
  seedUser(USER);
});

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  vi.unstubAllGlobals();
  useUi.setState({ workspaces: [], activeWorkspaceId: null } as never);
});

describe("DeviceTerminalBoundary 账号隔离", () => {
  it("mount 时不误清 (previous user id 守卫)", async () => {
    seedTabs();
    mounted = mount(createElement(DeviceTerminalBoundary));
    await flush();
    expect(storeTabs().some((t) => t.kind === "deviceTerminal")).toBe(true);
  });

  it("账号 A->B 切换: 关闭全部设备终端标签, 与 DevicesView 是否挂载无关", async () => {
    seedTabs();
    mounted = mount(createElement(DeviceTerminalBoundary));
    await flush();
    seedUser(OTHER_USER);
    await flush();
    const tabs = storeTabs();
    expect(tabs.some((t) => t.kind === "deviceTerminal")).toBe(false);
    expect(tabs.some((t) => t.kind === "settings")).toBe(true);
  });

  it("logout (user -> null) 同样关闭全部设备终端标签", async () => {
    seedTabs();
    mounted = mount(createElement(DeviceTerminalBoundary));
    await flush();
    seedUser(null);
    await flush();
    expect(storeTabs().some((t) => t.kind === "deviceTerminal")).toBe(false);
  });

  it("401 会话过期 (SESSION_EXPIRED_EVENT) 经账号 store 清空 user 后关闭标签", async () => {
    seedTabs();
    mounted = mount(createElement(DeviceTerminalBoundary));
    await flush();
    window.dispatchEvent(new CustomEvent("nexterm:session-expired"));
    await flush();
    expect(useAuth.getState().user).toBeNull();
    expect(storeTabs().some((t) => t.kind === "deviceTerminal")).toBe(false);
  });

  it("账号切换后旧桥接被 detach: 旧账号的 shell 不可继续输入", async () => {
    seedTabs();
    mounted = mount(
      <>
        {createElement(TabHarness)}
        {createElement(DeviceTerminalBoundary)}
      </>,
    );
    // 视图完成 open (creator + attach 两条桥接)
    await flushUntil(() => FakeWebSocket.instances.length >= 2);
    const attacher = FakeWebSocket.instances[1];
    expect(attacher.sentFrames().some((f) => f.kind === SupervisorFrameKind.Attach)).toBe(true);

    seedUser(OTHER_USER);
    await flushUntil(() => storeTabs().every((t) => t.kind !== "deviceTerminal"));
    // 视图卸载 → cleanup detach: 旧桥收到 detach 帧并关闭
    await flushUntil(() => attacher.readyState === 3);
    expect(attacher.sentFrames().some((f) => f.kind === SupervisorFrameKind.Detach)).toBe(true);
    // 桥接已关闭, 输入路径不复存在 (再发帧只会落到关闭的连接上)
    expect(attacher.readyState).toBe(3);
  });

  it("在途 open 遇账号切换: boundary 关标签后经 creator 旧连接回收 killSession, 全部旧 WS 关闭", async () => {
    FakeWebSocket.deferAttach = true;
    seedTabs();
    mounted = mount(
      <>
        {createElement(TabHarness)}
        {createElement(DeviceTerminalBoundary)}
      </>,
    );
    // create 已成功且 attach 已发出 (响应延迟): instances 0=creator, 1=attach
    await flushUntil(() =>
      FakeWebSocket.instances[1]?.sentFrames().some((f) => f.kind === SupervisorFrameKind.Attach),
    );
    const creator = FakeWebSocket.instances[0];
    seedUser(OTHER_USER);
    // boundary 关闭标签 → 视图卸载 → 取消在途 open → creator 旧连接回收
    await flushUntil(() => storeTabs().every((t) => t.kind !== "deviceTerminal"));
    await flushUntil(() => creator.sentFrames().some((f) => f.kind === SupervisorFrameKind.KillSession));
    await flush();
    expect(creator.sentFrames().some((f) => f.kind === SupervisorFrameKind.KillSession)).toBe(true);
    expect(FakeWebSocket.instances.every((ws) => ws.readyState === 3)).toBe(true);
  });
});
