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
  resumeDeviceTerminal,
  unixNanoFromRFC3339,
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
  mfa_enabled: false,
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
  static deferCreate = false;
  // deferAttach 延迟 attach 响应, 让用例在 attach 在途时介入 (取消/账号切换)。
  static deferAttach = false;
  // rejectNew 模拟账号切换后的鉴权边界: 新连接在 WS 握手期被 401/403 拒绝
  // (error + close 1006, 不会进入 supervisor hello)。
  static rejectNew = false;
  // helloHang 模拟对端不响应 (creator 挂起): hello 帧收到但永不回复。
  static helloHang = false;
  // helloHangFrom 只对第 N 条及以后的连接挂起 hello (如第二条 attach 连接)。
  static helloHangFrom: number | undefined;
  readonly url: string;
  readonly sent: Uint8Array[] = [];
  binaryType = "";
  readyState = 1;
  behavior: { helloError?: string; digest?: string } = {};
  private createPending = false;
  private parser = new SupervisorFrameParser();
  private listeners = new Map<string, ((event: unknown) => void)[]>();

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
    if (FakeWebSocket.rejectNew) setTimeout(() => this.close(1006, ""), 0);
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

  // serverCloseClean 模拟生产 pipeBridge 的正常关闭 (1000, "bridge closed")。
  serverCloseClean(): void {
    this.close(1000, "bridge closed");
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
        // rejectNew 模拟握手期 401/403: 不应答 hello, 由构造时的 close(1006) 结束
        if (FakeWebSocket.rejectNew) return;
        if (FakeWebSocket.helloHang) return;
        const index = FakeWebSocket.instances.indexOf(this) + 1;
        if (FakeWebSocket.helloHangFrom !== undefined && index >= FakeWebSocket.helloHangFrom) return;
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
      case SupervisorFrameKind.Create: {
        const msg = JSON.parse(new TextDecoder().decode(frame.payload)) as { id: string };
        const info = { ...INFO, id: msg.id };
        if (FakeWebSocket.deferCreate) {
          // 服务端按序处理: Created 未发出前, 后续帧的响应都排在它之后;
          // 延迟期间不响应后续帧, Created 延迟发出 (客户端可能已关闭)
          this.createPending = true;
          setTimeout(() => {
            this.createPending = false;
            this.serverJSON(SupervisorFrameKind.Created, info);
          }, 50);
          return;
        }
        this.serverJSON(SupervisorFrameKind.Created, info);
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
      case SupervisorFrameKind.Detach:
        this.serverJSON(SupervisorFrameKind.OK, {});
        return;
      case SupervisorFrameKind.KillSession:
        // create 响应在途: killSession 的 OK 按序排在 Created 之后, 客户端已关闭读不到
        if (this.createPending) return;
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
  FakeWebSocket.deferCreate = false;
  FakeWebSocket.deferAttach = false;
  FakeWebSocket.rejectNew = false;
  FakeWebSocket.helloHang = false;
  FakeWebSocket.helloHangFrom = undefined;
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
    const createMsg = creator.sentJSON(SupervisorFrameKind.Create) as { id: string; attempt: string };
    expect(createMsg.id).toMatch(/^[0-9A-Za-z]{26}$/);
    expect(createMsg.attempt).toMatch(/^[0-9A-Za-z]{26}$/);
    expect(attacher.sentJSON(SupervisorFrameKind.Attach)).toMatchObject({
      id: createMsg.id,
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
    const createMsg = FakeWebSocket.instances[0].sentJSON(SupervisorFrameKind.Create) as { id: string };
    expect(resumer.sentJSON(SupervisorFrameKind.Attach)).toMatchObject({
      id: createMsg.id,
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

  it("生产 clean 1000 拆桥同样进入可重连断开态 (pipeBridge 正常关闭)", async () => {
    mounted = await mountTerminal();
    await flushUntil(() => bodyText().includes("已连接"));
    FakeWebSocket.instances[1].serverCloseClean();
    await flushUntil(() => bodyText().includes("连接已断开"));
    const resumeButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("重新连接"));
    expect(resumeButton).toBeTruthy();
  });

  it("resume hello 挂起时卸载: 替换 WS 被关闭, 既有会话仍可再次 resume", async () => {
    mounted = await mountTerminal();
    await flushUntil(() => bodyText().includes("已连接"));
    const createMsg = FakeWebSocket.instances[0].sentJSON(SupervisorFrameKind.Create) as { id: string };
    FakeWebSocket.instances[1].serverCloseAbnormal();
    await flushUntil(() => bodyText().includes("连接已断开"));
    // 第三条连接 (resume) 在 hello 期挂起
    FakeWebSocket.helloHangFrom = 3;
    const resumeButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("重新连接"));
    expect(resumeButton).toBeTruthy();
    resumeButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await flushUntil(() => FakeWebSocket.instances.length >= 3);
    mounted.unmount();
    mounted = null;
    // 卸载取消在途 resume: 替换 WS 被关闭, 不留旧账号活 WS
    await flushUntil(() => FakeWebSocket.instances[2].readyState === 3);
    // 既有会话仍可再次 resume (resume 不创建会话, 取消只断开替换连接)
    FakeWebSocket.helloHangFrom = undefined;
    const { bridge } = await resumeDeviceTerminal({
      deviceId: "d-1",
      stateDigest: "digest-1",
      sessionId: createMsg.id,
      identity: { createdAtUnixNano: unixNanoFromRFC3339(INFO.created_at), incarnation: INFO.incarnation },
      factory: (url) => new FakeWebSocket(url) as unknown as WebSocket,
    });
    expect(bridge.isAttached).toBe(true);
    bridge.detach();
  });

  it("exit 帧后的 clean 关闭停留在已结束, 不误报断开", async () => {
    mounted = await mountTerminal();
    await flushUntil(() => bodyText().includes("已连接"));
    const attacher = FakeWebSocket.instances[1];
    attacher.serverJSON(SupervisorFrameKind.Exit, { code: 0 });
    attacher.serverCloseClean();
    await flushUntil(() => bodyText().includes("已结束"));
    expect(bodyText()).not.toContain("连接已断开");
  });

  it("断开后设备被吊销: 重连显示吊销态, 不再发起桥接", async () => {
    mounted = await mountTerminal();
    await flushUntil(() => bodyText().includes("已连接"));
    FakeWebSocket.instances[1].serverCloseAbnormal();
    await flushUntil(() => bodyText().includes("连接已断开"));
    routeDevices([{ ...DEVICE, revoked_at: NOW - 1000 }]);
    const bridgesBefore = FakeWebSocket.instances.length;
    const resumeButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("重新连接"));
    resumeButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await flushUntil(() => bodyText().includes("设备已吊销"));
    expect(FakeWebSocket.instances).toHaveLength(bridgesBefore);
  });

  it("断开后设备关闭终端访问: 重连显示策略态, 不再发起桥接", async () => {
    mounted = await mountTerminal();
    await flushUntil(() => bodyText().includes("已连接"));
    FakeWebSocket.instances[1].serverCloseAbnormal();
    await flushUntil(() => bodyText().includes("连接已断开"));
    routeDevices([{ ...DEVICE, agent: { ...DEVICE.agent, terminal_enabled: false } }]);
    const bridgesBefore = FakeWebSocket.instances.length;
    const resumeButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("重新连接"));
    resumeButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await flushUntil(() => bodyText().includes("设备已关闭终端访问"));
    expect(FakeWebSocket.instances).toHaveLength(bridgesBefore);
  });
});

describe("DeviceTerminalView 生命周期", () => {
  it("create 结果不确定时卸载视图: 按 attempt 回收而非零回收, 不留活桥接", async () => {
    FakeWebSocket.deferCreate = true;
    const view = mount(createElement(DeviceTerminalView, { deviceId: "d-1", visible: true }));
    await flushUntil(() =>
      FakeWebSocket.instances[0]?.sentFrames().some((f) => f.kind === SupervisorFrameKind.Create),
    );
    view.unmount();
    await flush();
    await flush();
    // 服务端可能已完成 create 而 Created 未交付 (context.Background 完成,
    // 回包 best-effort): 取消必须按 attempt 回收, 不能因 info 未确认而零回收
    const creator = FakeWebSocket.instances[0];
    const createMsg = creator.sentJSON(SupervisorFrameKind.Create) as { id: string; attempt: string };
    const reap = creator.sentJSON(SupervisorFrameKind.KillSession) as { id: string; attempt: string };
    expect(reap).toEqual({ id: createMsg.id, attempt: createMsg.attempt });
    expect(creator.readyState).toBe(3);
    expect(creator.sentFrames().some((f) => f.kind === SupervisorFrameKind.Attach)).toBe(false);
    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("create 成功后 attach 连接挂起 (hello 无响应): 取消关闭 attach, creator 完成 killSession, 两条 WS 均关闭", async () => {
    FakeWebSocket.helloHangFrom = 2;
    mounted = await mountTerminal();
    // create 已成功, 第二条 attach 连接在 hello 期挂起
    await flushUntil(() => FakeWebSocket.instances.length >= 2);
    mounted.unmount();
    mounted = null;
    // attaching 取消关闭 attach 解锁 await; creator 保留并完成 killSession 后关闭
    await flushUntil(() =>
      FakeWebSocket.instances[0].sentFrames().some((f) => f.kind === SupervisorFrameKind.KillSession),
    );
    await flush();
    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(FakeWebSocket.instances.every((ws) => ws.readyState === 3)).toBe(true);
  });

  it("create 成功后卸载视图: 经 creator 旧连接 killSession 回收, 不留活桥接", async () => {
    FakeWebSocket.deferAttach = true;
    mounted = await mountTerminal();
    // create 已成功且 attach 已发出 (响应延迟): instances 0=creator, 1=attach
    await flushUntil(() =>
      FakeWebSocket.instances[1]?.sentFrames().some((f) => f.kind === SupervisorFrameKind.Attach),
    );
    mounted.unmount();
    mounted = null;
    // 取消发生在 create 成功后: creator 保留给回收; attach 桥被 cleanup
    // detach 而失败 (或 attach 完成后取消生效), killSession 走 creator 旧连接
    await flushUntil(() =>
      FakeWebSocket.instances[0].sentFrames().some((f) => f.kind === SupervisorFrameKind.KillSession),
    );
    await flush();
    expect(FakeWebSocket.instances.every((ws) => ws.readyState === 3)).toBe(true);
    expect(FakeWebSocket.instances).toHaveLength(2);
  });

  it("create 成功后账号切换 (logout/A->B): 新 attach 连接 401/403, 经 creator 旧连接完成 kill", async () => {
    FakeWebSocket.deferCreate = true;
    mounted = await mountTerminal();
    // create 已发出未响应; 此时账号切换, 新连接在握手期 401/403
    await flushUntil(() =>
      FakeWebSocket.instances[0]?.sentFrames().some((f) => f.kind === SupervisorFrameKind.Create),
    );
    seedUser({ ...USER, id: "u-2" });
    FakeWebSocket.rejectNew = true;
    // create 响应到达后流程继续: attach connect 被 401/403 拒绝,
    // 回收走切换前已鉴权的 creator 连接
    await flushUntil(() =>
      FakeWebSocket.instances[0].sentFrames().some((f) => f.kind === SupervisorFrameKind.KillSession),
    );
    await flush();
    expect(FakeWebSocket.instances[0].sentFrames().some((f) => f.kind === SupervisorFrameKind.KillSession)).toBe(true);
    expect(FakeWebSocket.instances.every((ws) => ws.readyState === 3)).toBe(true);
    // 失败态可见 (open 失败但没有孤儿 shell)
    await flushUntil(() => bodyText().includes("终端不可用") || bodyText().includes("已断开"));
  });

  it("creator 挂起 (hello 无响应) 时取消可关闭首连接, 不发 create", async () => {
    FakeWebSocket.helloHang = true;
    const view = mount(createElement(DeviceTerminalView, { deviceId: "d-1", visible: true }));
    await flushUntil(() => FakeWebSocket.instances.length >= 1);
    view.unmount();
    // connecting 阶段取消: 首连接被关闭, create 从未发出
    await flushUntil(() => FakeWebSocket.instances[0].readyState === 3);
    expect(FakeWebSocket.instances[0].sentFrames().some((f) => f.kind === SupervisorFrameKind.Create)).toBe(false);
    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("自然 exit 后新开终端: 旧桥接先 detach 再替换", async () => {
    mounted = await mountTerminal();
    await flushUntil(() => bodyText().includes("已连接"));
    const oldBridge = FakeWebSocket.instances[1];
    oldBridge.serverJSON(SupervisorFrameKind.Exit, { code: 0 });
    await flushUntil(() => bodyText().includes("已结束"));
    const restartButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新开终端"));
    expect(restartButton).toBeTruthy();
    restartButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await flushUntil(() => FakeWebSocket.instances.length >= 4);
    await flushUntil(() => bodyText().includes("已连接"));
    expect(oldBridge.sentFrames().some((f) => f.kind === SupervisorFrameKind.Detach)).toBe(true);
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
    const createMsg = FakeWebSocket.instances[0].sentJSON(SupervisorFrameKind.Create) as { id: string };
    expect(killer.sentJSON(SupervisorFrameKind.KillSession)).toMatchObject({
      id: createMsg.id,
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
});
