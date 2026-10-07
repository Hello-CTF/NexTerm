/** @vitest-environment jsdom */
// FLEET156 设备远程终端交互测试: 打开流程 (hello/create/attach 两条桥接)、输出
// 渲染、断开与重连续传 (expect incarnation + backlog 重放)、结束终端的显式确认、
// 吊销/策略/离线摘要缺失状态、DevicesView 设备行的打开入口与账号切换清理。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { flush, flushUntil, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    fetch: vi.fn(),
    ask: vi.fn(),
  };
});

vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { DevicesView } from "../../features/fleet/DevicesView";
import { DeviceTerminalView } from "../../features/fleet/DeviceTerminalView";
import { useAuth } from "../../features/auth/store";
import { useUi } from "../../app/store";
import {
  SupervisorFrameKind,
  SupervisorFrameParser,
  encodeSupervisorFrame,
  encodeSupervisorJSONFrame,
  encodeSupervisorOutputPayload,
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
  static forcedHelloError: string | undefined;
  readonly url: string;
  readonly sent: Uint8Array[] = [];
  binaryType = "";
  readyState = 1;
  behavior: { helloError?: string; digest?: string } = {};
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

  close(code = 1000, reason = ""): void {
    if (this.readyState === 3) return;
    this.readyState = 3;
    this.emit("close", { code, reason, wasClean: code === 1000 });
  }

  serverSend(frame: Uint8Array): void {
    this.emit("message", { data: frame.buffer.slice(frame.byteOffset, frame.byteOffset + frame.byteLength) });
  }

  serverJSON(kind: number, message: unknown): void {
    this.serverSend(encodeSupervisorJSONFrame(kind, message));
  }

  serverOutput(seq: bigint, text: string): void {
    this.serverSend(
      encodeSupervisorFrame(
        SupervisorFrameKind.Output,
        encodeSupervisorOutputPayload(seq, new TextEncoder().encode(text)),
      ),
    );
  }

  serverCloseAbnormal(): void {
    this.close(1006, "");
  }

  sentFrames(): SupervisorFrame[] {
    const parser = new SupervisorFrameParser();
    const frames: SupervisorFrame[] = [];
    for (const bytes of this.sent) frames.push(...parser.push(bytes));
    return frames;
  }

  sentJSON(kind: number): unknown {
    const frame = [...this.sentFrames()].reverse().find((f) => f.kind === kind);
    if (!frame) throw new Error(`frame ${kind} not sent`);
    return JSON.parse(new TextDecoder().decode(frame.payload));
  }

  private respond(frame: SupervisorFrame): void {
    switch (frame.kind) {
      case SupervisorFrameKind.Hello: {
        const hello = JSON.parse(new TextDecoder().decode(frame.payload)) as { state_digest?: string };
        const helloError = this.behavior.helloError ?? FakeWebSocket.forcedHelloError;
        if (helloError) {
          this.serverJSON(SupervisorFrameKind.Error, { code: helloError, message: "err" });
          return;
        }
        if (hello.state_digest !== (this.behavior.digest ?? "digest-1")) {
          this.serverJSON(SupervisorFrameKind.Error, { code: "state_mismatch", message: "state" });
          return;
        }
        this.serverJSON(SupervisorFrameKind.HelloAck, { version: 2 });
        return;
      }
      case SupervisorFrameKind.Create:
        this.serverJSON(SupervisorFrameKind.Created, INFO);
        return;
      case SupervisorFrameKind.Attach:
        this.serverJSON(SupervisorFrameKind.Attached, INFO);
        return;
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
      return {
        ok: true,
        status: 200,
        text: async () => JSON.stringify({ devices }),
      };
    }
    if (u.includes("/fleet/base-urls")) {
      return { ok: true, status: 200, text: async () => JSON.stringify({ base_urls: [] }) };
    }
    return { ok: true, status: 200, text: async () => JSON.stringify({}) };
  });
}

async function mountTerminal(deviceId = "d-1"): Promise<MountedView> {
  const view = mount(createElement(DeviceTerminalView, { deviceId, visible: true }));
  await flush();
  return view;
}

function bodyText(): string {
  return document.body.textContent ?? "";
}

let mounted: MountedView | null = null;

function seedUser(user: typeof USER): void {
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth: "on" },
    user,
    dek: null,
    gate: "ready",
    pendingRecoveryKey: null,
    error: null,
  });
}

beforeEach(() => {
  FakeWebSocket.instances = [];
  FakeWebSocket.forcedHelloError = undefined;
  vi.stubGlobal("WebSocket", FakeWebSocket);
  vi.stubGlobal("fetch", mocks.fetch);
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    disconnect() {}
  });
  mocks.ask.mockReset();
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

describe("DeviceTerminalView 打开与输出", () => {
  it("hello/create/attach 走两条桥接, 就绪后渲染设备名与输出", async () => {
    mounted = await mountTerminal();
    await flushUntil(() => bodyText().includes("已连接"));
    expect(FakeWebSocket.instances).toHaveLength(2);
    const [creator, attacher] = FakeWebSocket.instances;
    expect(creator.url).toContain("/fleet/devices/d-1/bridge");
    expect(creator.sentJSON(SupervisorFrameKind.Hello)).toEqual({ version: 2, state_digest: "digest-1" });
    expect(creator.sentJSON(SupervisorFrameKind.Create)).toMatchObject({ cols: 80, rows: 24 });
    expect(attacher.sentJSON(SupervisorFrameKind.Attach)).toMatchObject({
      id: "s-1",
      expect_incarnation: "inc-1",
    });
    expect(bodyText()).toContain("web-01");
    attacher.serverOutput(0n, "READY-42\r\n");
    await waitFor(() => {
      expect(document.querySelector(".xterm-rows")?.textContent).toContain("READY-42");
    });
  });

  it("设备关闭终端访问时给出策略态, 不发任何桥接", async () => {
    routeDevices([{ ...DEVICE, agent: { ...DEVICE.agent, terminal_enabled: false } }]);
    mounted = await mountTerminal();
    await flushUntil(() => bodyText().includes("设备已关闭终端访问"));
    expect(FakeWebSocket.instances).toHaveLength(0);
  });

  it("设备已吊销时给出吊销态", async () => {
    routeDevices([{ ...DEVICE, revoked_at: NOW - 1000 }]);
    mounted = await mountTerminal();
    await flushUntil(() => bodyText().includes("设备已吊销"));
    expect(FakeWebSocket.instances).toHaveLength(0);
  });

  it("设备代理未上报摘要 (离线) 时显式失败", async () => {
    routeDevices([{ ...DEVICE, agent: { ...DEVICE.agent, state_digest: undefined } }]);
    mounted = await mountTerminal();
    await flushUntil(() => bodyText().includes("未上报终端状态摘要"));
    expect(FakeWebSocket.instances).toHaveLength(0);
  });

  it("hello 被 state_mismatch 拒绝时展示可重试失败态", async () => {
    FakeWebSocket.forcedHelloError = "state_mismatch";
    mounted = await mountTerminal();
    await flushUntil(() => bodyText().includes("设备端终端状态已变化"));
    expect(bodyText()).toContain("新开终端");
  });
});

describe("DeviceTerminalView 断开与恢复", () => {
  it("桥接异常断开显示断开态, 重新连接按 expect 身份重attach 并重放 backlog", async () => {
    mounted = await mountTerminal();
    await flushUntil(() => bodyText().includes("已连接"));
    const attacher = FakeWebSocket.instances[1];
    attacher.serverOutput(0n, "before-drop\r\n");
    await waitFor(() => {
      expect(document.querySelector(".xterm-rows")?.textContent).toContain("before-drop");
    });
    attacher.serverCloseAbnormal();
    await flushUntil(() => bodyText().includes("连接已断开"));

    const resumeButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("重新连接"));
    expect(resumeButton).toBeTruthy();
    resumeButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await flushUntil(() => FakeWebSocket.instances.length >= 3);
    const resumer = FakeWebSocket.instances[2];
    expect(resumer.sentJSON(SupervisorFrameKind.Attach)).toMatchObject({
      id: "s-1",
      expect_incarnation: "inc-1",
    });
    resumer.serverOutput(0n, "before-drop\r\nlate-7\r\n");
    await flushUntil(() => bodyText().includes("已连接"));
    await waitFor(() => {
      const text = document.querySelector(".xterm-rows")?.textContent ?? "";
      expect(text).toContain("before-drop");
      expect(text).toContain("late-7");
    });
  });
});

describe("DeviceTerminalView 结束终端", () => {
  it("结束终端需显式确认, 确认后在控制桥接上 killSession", async () => {
    mounted = await mountTerminal();
    await flushUntil(() => bodyText().includes("已连接"));
    mocks.ask.mockResolvedValueOnce(true);
    const killButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("结束终端"));
    expect(killButton).toBeTruthy();
    killButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await flushUntil(() => FakeWebSocket.instances.length >= 3);
    const killer = FakeWebSocket.instances[2];
    expect(killer.sentJSON(SupervisorFrameKind.KillSession)).toMatchObject({
      id: "s-1",
      expect_incarnation: "inc-1",
    });
    await flushUntil(() => bodyText().includes("已结束"));
  });

  it("拒绝确认则不发送 killSession", async () => {
    mounted = await mountTerminal();
    await flushUntil(() => bodyText().includes("已连接"));
    mocks.ask.mockResolvedValueOnce(false);
    const killButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("结束终端"));
    killButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await flush();
    await flush();
    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(bodyText()).toContain("已连接");
  });
});

describe("DevicesView 打开入口", () => {
  it("在线且开启终端的设备行显示「终端」按钮, 点击打开 deviceTerminal 标签", async () => {
    routeDevices([DEVICE]);
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => bodyText().includes("web-01"));
    const button = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "终端");
    expect(button).toBeTruthy();
    button?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await flush();
    const tabs = useUi.getState().workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs));
    const tab = tabs.find((t) => t.kind === "deviceTerminal");
    expect(tab?.deviceId).toBe("d-1");
    expect(tab?.title).toContain("web-01");
  });

  it("离线设备的「终端」按钮禁用, 关闭终端访问的设备不显示按钮", async () => {
    routeDevices([
      { ...DEVICE, id: "d-2", name: "offline-01", last_seen_at: NOW - 3600_000, agent: { ...DEVICE.agent, last_seen_at: NOW - 3600_000 } },
      { ...DEVICE, id: "d-3", name: "no-term-01", agent: { ...DEVICE.agent, terminal_enabled: false } },
    ]);
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => bodyText().includes("offline-01"));
    const buttons = [...document.querySelectorAll("button")].filter((b) => b.textContent?.trim() === "终端");
    expect(buttons).toHaveLength(1);
    expect((buttons[0] as HTMLButtonElement).disabled).toBe(true);
  });

  it("账号切换时关闭全部设备终端标签", async () => {
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
    routeDevices([DEVICE]);
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => bodyText().includes("web-01"));
    seedUser({ ...USER, id: "u-2" });
    await flush();
    const tabs = useUi.getState().workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs));
    expect(tabs.some((t) => t.kind === "deviceTerminal")).toBe(false);
    expect(tabs.some((t) => t.kind === "settings")).toBe(true);
  });
});
