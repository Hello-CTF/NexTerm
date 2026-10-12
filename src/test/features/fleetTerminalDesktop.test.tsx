/** @vitest-environment jsdom */
// 桌面传输的设备远程终端 (FLEET 第 2 期): deviceTerminalApi 按 TRANSPORT 分流到
// wails IPC 桥 (device_terminal_open/begin/input/resize/close + 三个事件主题),
// DeviceTerminalView 的桌面登录门 (sync_status) 与「登录服务端账号后可用」降级卡,
// 以及登录后经 remote_fleet_devices + IPC 桥打开、断线重连 (resume) 的完整链路。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, flush, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return { call: vi.fn(), listen: vi.fn() };
});

vi.mock("../../ipc/wails", async (importOriginal) => {
  const original = await importOriginal<typeof import("../../ipc/wails")>();
  return { ...original, callDesktop: mocks.call, listenWailsEvent: mocks.listen };
});

import { DeviceTerminalView } from "../../features/fleet/DeviceTerminalView";
import { useUi } from "../../app/store";
import {
  DeviceTerminalError,
  killDeviceTerminal,
  openDeviceTerminal,
  resumeDeviceTerminal,
  type SupervisorExit,
} from "../../ipc/deviceTerminalApi";

const INFO = {
  id: "s-1",
  created_at: "2026-10-07T05:34:56.123456789+08:00",
  incarnation: "inc-1",
  cols: 80,
  rows: 24,
  dead: false,
};

const IDENTITY_DTO = { createdAtUnixNano: "1759904096123456789", incarnation: "inc-1" };

const DEVICE = {
  id: "d-1",
  name: "web-01",
  kind: "agent",
  created_at: 1,
  last_seen_at: 2,
  revoked_at: 0,
  agent: {
    platform: "linux",
    app_version: "1.0.0",
    desired_autostart: true,
    terminal_enabled: true,
    current_url: "https://nx.example.com",
    service_state: { installed: true, enabled: true, active: true },
    last_seen_at: 2,
    state_digest: "digest-1",
  },
};

type IpcHandler = (args: Record<string, unknown>) => unknown;

const ipcHandlers = new Map<string, IpcHandler>();
const ipcListeners = new Map<string, Set<(payload: unknown) => void>>();
const callLog: string[] = [];

function emitIpc(topic: string, payload: unknown): void {
  for (const handler of [...(ipcListeners.get(topic) ?? [])]) handler(payload);
}

function listenerCount(topic: string): number {
  return ipcListeners.get(topic)?.size ?? 0;
}

function textToBase64(text: string): string {
  const bytes = new TextEncoder().encode(text);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function base64ToText(value: string): string {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return new TextDecoder().decode(bytes);
}

interface FakeChannel {
  channelId: string;
  inputs: string[];
  began: boolean;
  closed: boolean;
  killed: boolean;
}

function installDeviceTerminal() {
  const state = {
    seq: 0,
    channels: [] as FakeChannel[],
    resumeArgs: null as Record<string, unknown> | null,
    killSessionArgs: null as Record<string, unknown> | null,
  };
  const findChannel = (args: Record<string, unknown>): FakeChannel => {
    const channel = state.channels.find((c) => c.channelId === args.channelId);
    if (!channel || channel.closed) throw { code: "not_found", message: "设备终端通道不存在或已关闭" };
    return channel;
  };
  ipcHandlers.set("device_terminal_open", () => {
    state.seq += 1;
    const channel: FakeChannel = { channelId: `ch-${state.seq}`, inputs: [], began: false, closed: false, killed: false };
    state.channels.push(channel);
    return { channelId: channel.channelId, info: INFO, identity: IDENTITY_DTO };
  });
  ipcHandlers.set("device_terminal_resume", (args) => {
    state.resumeArgs = args;
    state.seq += 1;
    const channel: FakeChannel = { channelId: `ch-${state.seq}`, inputs: [], began: false, closed: false, killed: false };
    state.channels.push(channel);
    return { channelId: channel.channelId, info: INFO, identity: IDENTITY_DTO };
  });
  ipcHandlers.set("device_terminal_begin", (args) => {
    findChannel(args).began = true;
    return {};
  });
  ipcHandlers.set("device_terminal_input", (args) => {
    const channel = findChannel(args);
    const text = base64ToText(String(args.data));
    channel.inputs.push(text);
    return { written: text.length };
  });
  ipcHandlers.set("device_terminal_resize", () => ({}));
  ipcHandlers.set("device_terminal_close", (args) => {
    findChannel(args).closed = true;
    return {};
  });
  ipcHandlers.set("device_terminal_kill", (args) => {
    const channel = findChannel(args);
    channel.killed = true;
    channel.closed = true;
    return {};
  });
  ipcHandlers.set("device_terminal_kill_session", (args) => {
    state.killSessionArgs = args;
    return {};
  });
  return state;
}

function installLink(loggedIn: boolean) {
  ipcHandlers.set("sync_status", () => ({ configured: true, loggedIn, seq: 0 }));
  ipcHandlers.set("sync_link_get", () => ({
    url: "https://nx.example.com",
    username: "alice",
    insecure: false,
    hasPassword: true,
    verifiedAt: 1,
    lastError: "",
  }));
}

let mounted: MountedView | null = null;

beforeEach(() => {
  ipcHandlers.clear();
  ipcListeners.clear();
  callLog.length = 0;
  mocks.call.mockImplementation((cmd: string, request?: { args?: Record<string, unknown> }) => {
    callLog.push(cmd);
    const handler = ipcHandlers.get(cmd);
    if (!handler) return Promise.reject({ code: "internal", message: `unknown command ${cmd}` });
    try {
      return Promise.resolve(handler(request?.args ?? {}));
    } catch (error) {
      return Promise.reject(error);
    }
  });
  mocks.listen.mockImplementation((topic: string, handler: (payload: unknown) => void) => {
    let set = ipcListeners.get(topic);
    if (!set) {
      set = new Set();
      ipcListeners.set(topic, set);
    }
    set.add(handler);
    return Promise.resolve(() => {
      set.delete(handler);
    });
  });
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    disconnect() {}
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  useUi.setState({ workspaces: [], activeWorkspaceId: null } as never);
  vi.unstubAllGlobals();
});

describe("桌面 IPC 桥 (deviceTerminalApi 分流)", () => {
  it("open 经 IPC 建通道并开闸, output/close 事件驱动回调, input 走 base64", async () => {
    const state = installDeviceTerminal();
    const output: string[] = [];
    const closedWith: (DeviceTerminalError | null)[] = [];
    const { info, identity, bridge } = await openDeviceTerminal({
      deviceId: "d-1",
      stateDigest: "digest-1",
      cols: 80,
      rows: 24,
      wire: (b) => {
        b.onOutput = (data) => output.push(new TextDecoder().decode(data));
        b.onClose = (error) => closedWith.push(error);
      },
    });
    expect(info.id).toBe("s-1");
    expect(identity.incarnation).toBe("inc-1");
    expect(identity.createdAtUnixNano).toBe(1759904096123456789n);
    expect(callLog).toEqual(["device_terminal_open", "device_terminal_begin"]);
    expect(state.channels[0].began).toBe(true);

    emitIpc("device-terminal://output", { channelId: "ch-1", data: textToBase64("hello\r\n") });
    expect(output).toEqual(["hello\r\n"]);
    emitIpc("device-terminal://output", { channelId: "other-channel", data: textToBase64("noise") });
    expect(output).toEqual(["hello\r\n"]);

    await bridge.input(new TextEncoder().encode("ls\n"));
    expect(state.channels[0].inputs).toEqual(["ls\n"]);
    await bridge.resize(100, 40);

    bridge.detach();
    await vi.waitFor(() => expect(callLog).toContain("device_terminal_close"));
    expect(closedWith).toEqual([null]);
    expect(bridge.isClosed).toBe(true);
    await expect(bridge.input(new TextEncoder().encode("x"))).rejects.toBeInstanceOf(DeviceTerminalError);
  });

  it("wire 在 begin 之前挂上: 开闸首批 output 不丢", async () => {
    installDeviceTerminal();
    ipcHandlers.set("device_terminal_begin", (args) => {
      emitIpc("device-terminal://output", { channelId: args.channelId, data: textToBase64("first-batch") });
      return {};
    });
    const output: string[] = [];
    await openDeviceTerminal({
      deviceId: "d-1",
      stateDigest: "digest-1",
      cols: 80,
      rows: 24,
      wire: (b) => {
        b.onOutput = (data) => output.push(new TextDecoder().decode(data));
      },
    });
    expect(output).toEqual(["first-batch"]);
  });

  it("exit 事件先交付退出码, 干净 closed 不带错误", async () => {
    installDeviceTerminal();
    const exits: SupervisorExit[] = [];
    const closedWith: (DeviceTerminalError | null)[] = [];
    const { bridge } = await openDeviceTerminal({
      deviceId: "d-1",
      stateDigest: "digest-1",
      cols: 80,
      rows: 24,
      wire: (b) => {
        b.onExit = (exit) => exits.push(exit);
        b.onClose = (error) => closedWith.push(error);
      },
    });
    emitIpc("device-terminal://exit", { channelId: "ch-1", code: 42, signal: "" });
    expect(exits).toEqual([{ code: 42, signal: "" }]);
    emitIpc("device-terminal://closed", { channelId: "ch-1" });
    expect(closedWith).toEqual([null]);
    expect(bridge.isClosed).toBe(true);
    expect(listenerCount("device-terminal://output")).toBe(0);
  });

  it("带码 closed 映射成 DeviceTerminalError 并拒绝后续 input", async () => {
    installDeviceTerminal();
    const closedWith: (DeviceTerminalError | null)[] = [];
    const { bridge } = await openDeviceTerminal({
      deviceId: "d-1",
      stateDigest: "digest-1",
      cols: 80,
      rows: 24,
      wire: (b) => {
        b.onClose = (error) => closedWith.push(error);
      },
    });
    emitIpc("device-terminal://closed", { channelId: "ch-1", code: "disconnected", message: "桥接连接断开" });
    expect(closedWith).toHaveLength(1);
    expect(closedWith[0]).toBeInstanceOf(DeviceTerminalError);
    expect(closedWith[0]?.code).toBe("disconnected");
    await expect(bridge.input(new TextEncoder().encode("x"))).rejects.toMatchObject({ code: "disconnected" });
  });

  it("resume 带字符串化身份, killDeviceTerminal 走 kill_session 命令", async () => {
    const state = installDeviceTerminal();
    const { bridge, info } = await resumeDeviceTerminal({
      deviceId: "d-1",
      stateDigest: "digest-1",
      sessionId: "s-1",
      identity: { createdAtUnixNano: 1759904096123456789n, incarnation: "inc-1" },
    });
    expect(info.id).toBe("s-1");
    expect(state.resumeArgs).toMatchObject({
      deviceId: "d-1",
      stateDigest: "digest-1",
      sessionId: "s-1",
      identity: { createdAtUnixNano: "1759904096123456789", incarnation: "inc-1" },
    });
    expect(callLog).toEqual(["device_terminal_resume", "device_terminal_begin"]);

    await killDeviceTerminal({
      deviceId: "d-1",
      stateDigest: "digest-1",
      sessionId: "s-1",
      identity: { createdAtUnixNano: 1759904096123456789n, incarnation: "inc-1" },
    });
    expect(state.killSessionArgs).toMatchObject({ sessionId: "s-1", identity: IDENTITY_DTO });
    bridge.detach();
  });
});

describe("DeviceTerminalView 桌面适配", () => {
  it("未登录服务端账号时给降级卡, 不发起任何 device_terminal 调用", async () => {
    installLink(false);
    installDeviceTerminal();
    ipcHandlers.set("remote_fleet_devices", () => ({ devices: [DEVICE] }));
    mounted = mount(createElement(DeviceTerminalView, { deviceId: "d-1" }));
    await flushUntil(() => mounted!.container.textContent!.includes("登录服务端账号后可用"));
    expect(mounted.container.textContent).toContain("未登录");
    expect(callLog).toContain("sync_status");
    expect(callLog).not.toContain("device_terminal_open");
    expect(callLog).not.toContain("remote_fleet_devices");
    clickButton(mounted.container, "去设置 → 账号同步登录");
    expect(useUi.getState().workspaces.some((w) => w.panes.some((p) => p.tabs.some((t) => t.id === "settings")))).toBe(true);
  });

  it("登录后经 IPC 桥打开终端, 断线事件驱动重连续传", async () => {
    installLink(true);
    const state = installDeviceTerminal();
    ipcHandlers.set("remote_fleet_devices", () => ({ devices: [DEVICE] }));
    mounted = mount(createElement(DeviceTerminalView, { deviceId: "d-1" }));
    await flushUntil(() => mounted!.container.textContent!.includes("已连接"));
    expect(mounted.container.textContent).toContain("web-01");
    expect(callLog).toEqual([
      "sync_status",
      "sync_link_get",
      "remote_fleet_devices",
      "device_terminal_open",
      "device_terminal_begin",
    ]);

    emitIpc("device-terminal://closed", { channelId: "ch-1", code: "disconnected", message: "桥接连接断开" });
    await flushUntil(() => mounted!.container.textContent!.includes("重新连接"));
    clickButton(mounted.container, "重新连接");
    await flushUntil(() => callLog.includes("device_terminal_resume"));
    await flushUntil(() => mounted!.container.textContent!.includes("已连接"));
    expect(state.resumeArgs).toMatchObject({ sessionId: "s-1", identity: IDENTITY_DTO });
    await flush();
  });
});
