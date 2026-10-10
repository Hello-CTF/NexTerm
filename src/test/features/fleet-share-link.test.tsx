/** @vitest-environment jsdom */
// FLEET168 设备终端公开链接创建交互测试: 真实 POST /share/links 合同 (蛇形
// 字段 + CSRF)、默认只读、显式 read_write、边界内 TTL、一次性 URL 只在创建
// 响应展示 (token 不进 web storage)、复制/吊销指引、失败/断网错误态、
// 卸载 (账号边界关闭标签路径) 取消在途创建、会话结束后面板与 URL 清除。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, deferred, flush, flushUntil, mount, setSelectValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    fetch: vi.fn(),
    ask: vi.fn(),
  };
});

vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { DeviceTerminalView } from "../../features/fleet/DeviceTerminalView";
import { sharePublicUrl } from "../../features/fleet/shareLinkCreate";
import { useAuth } from "../../features/auth/store";
import { useUi } from "../../app/store";
import {
  SupervisorFrameKind,
  SupervisorFrameParser,
  encodeSupervisorJSONFrame,
} from "../../ipc/deviceTerminalApi";
import { setCsrfToken } from "../../ipc/authApi";

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

const LINK_TOKEN = "fake-one-time-token-168";

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
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

  sentFrames() {
    const parser = new SupervisorFrameParser();
    const frames = [];
    for (const bytes of this.sent) frames.push(...parser.push(bytes));
    return frames;
  }

  sentJSON(kind: number): unknown {
    const frame = [...this.sentFrames()].reverse().find((f) => f.kind === kind);
    if (!frame) throw new Error(`frame ${kind} not sent`);
    return JSON.parse(new TextDecoder().decode(frame.payload));
  }

  private respond(frame: { kind: number; payload: Uint8Array }): void {
    switch (frame.kind) {
      case SupervisorFrameKind.Hello: {
        const hello = JSON.parse(new TextDecoder().decode(frame.payload)) as { state_digest?: string };
        if (hello.state_digest !== "digest-1") {
          this.serverJSON(SupervisorFrameKind.Error, { code: "state_mismatch", message: "state" });
          return;
        }
        this.serverJSON(SupervisorFrameKind.HelloAck, { version: 2 });
        return;
      }
      case SupervisorFrameKind.Create: {
        const msg = JSON.parse(new TextDecoder().decode(frame.payload)) as { id: string };
        this.serverJSON(SupervisorFrameKind.Created, { ...INFO, id: msg.id });
        return;
      }
      case SupervisorFrameKind.Attach: {
        const msg = JSON.parse(new TextDecoder().decode(frame.payload)) as { id: string };
        this.serverJSON(SupervisorFrameKind.Attached, { ...INFO, id: msg.id });
        return;
      }
      case SupervisorFrameKind.Input:
        this.serverJSON(SupervisorFrameKind.InputAck, { written: frame.payload.length });
        return;
      case SupervisorFrameKind.Resize:
      case SupervisorFrameKind.Kill:
      case SupervisorFrameKind.Detach:
      case SupervisorFrameKind.KillSession:
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

interface RecordedCall {
  url: string;
  method: string;
  headers: Record<string, string>;
  body?: unknown;
}

// shareBehavior 控制 POST /share/links 的 fake 行为: 默认立即成功,
// deferred 时挂起在途创建 (账号切换/卸载取消路径), failStatus 模拟服务端失败。
const shareBehavior: { deferred?: { promise: Promise<unknown> }; failStatus?: number } = {};

function route(): RecordedCall[] {
  const calls: RecordedCall[] = [];
  mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
    const u = String(url);
    const method = init.method ?? "GET";
    const headers = (init.headers ?? {}) as Record<string, string>;
    const body = init.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ url: u, method, headers, ...(body === undefined ? {} : { body }) });
    if (u.includes("/fleet/devices")) {
      return { ok: true, status: 200, text: async () => JSON.stringify({ devices: [DEVICE] }) };
    }
    if (u.includes("/fleet/base-urls")) {
      return { ok: true, status: 200, text: async () => JSON.stringify({ base_urls: [] }) };
    }
    if (u === "/share/links" && method === "POST") {
      if (shareBehavior.deferred) {
        await shareBehavior.deferred.promise;
      }
      if (shareBehavior.failStatus) {
        return {
          ok: false,
          status: shareBehavior.failStatus,
          text: async () => JSON.stringify({ error: { code: "internal", message: "数据库错误" } }),
        };
      }
      return {
        ok: true,
        status: 200,
        text: async () =>
          JSON.stringify({
            id: "link-1",
            owner_id: "u-1",
            device_id: "d-1",
            session_id: (body as { session_id: string }).session_id,
            permission: (body as { write: boolean }).write ? "read_write" : "read",
            created_at: NOW,
            expires_at: NOW + (body as { ttl_ms: number }).ttl_ms,
            token: LINK_TOKEN,
          }),
      };
    }
    return { ok: true, status: 200, text: async () => JSON.stringify({}) };
  });
  return calls;
}

function bodyText(): string {
  return document.body.textContent ?? "";
}

function webStorageText(): string {
  return JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } });
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
  shareBehavior.deferred = undefined;
  shareBehavior.failStatus = undefined;
  vi.stubGlobal("WebSocket", FakeWebSocket);
  vi.stubGlobal("fetch", mocks.fetch);
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    disconnect() {}
  });
  mocks.ask.mockReset();
  mocks.fetch.mockReset();
  document.body.replaceChildren();
  setCsrfToken("csrf-168");
  seedUser(USER);
});

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  vi.unstubAllGlobals();
  setCsrfToken(null);
  useUi.setState({ workspaces: [], activeWorkspaceId: null } as never);
});

async function mountReadyTerminal(): Promise<void> {
  mounted = mount(createElement(DeviceTerminalView, { deviceId: "d-1", visible: true }));
  await flushUntil(() => bodyText().includes("已连接"));
}

function openSharePanel(): void {
  const button = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "分享");
  expect(button).toBeTruthy();
  click(button as HTMLButtonElement);
}

function createButton(): HTMLButtonElement {
  const button = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "创建公开链接");
  expect(button).toBeTruthy();
  return button as HTMLButtonElement;
}

describe("DeviceTerminalView 公开链接创建", () => {
  it("分享按钮只在就绪后出现; 面板默认只读 + 1 小时, 创建走真实 POST /share/links", async () => {
    const calls = route();
    await mountReadyTerminal();
    openSharePanel();

    expect(document.querySelector('select[aria-label="链接有效期"]')).not.toBeNull();
    expect(bodyText()).toContain("默认只读");
    // 创建前即说明访问边界: 任何拿到链接的人无需账号可开, 只发给信任的人
    expect(bodyText()).toContain("无需 NexTerm 账号");
    expect(bodyText()).toContain("请仅分享给可信对象");
    expect(bodyText()).toContain("链接仅在创建成功后显示一次，本页面不会保存");
    expect(bodyText()).not.toContain("链接只能创建一次");
    click(createButton());

    await flushUntil(() => calls.some((c) => c.url === "/share/links"));
    const create = calls.find((c) => c.url === "/share/links");
    const createMsg = FakeWebSocket.instances[0].sentJSON(SupervisorFrameKind.Create) as { id: string };
    expect(create?.method).toBe("POST");
    expect(create?.body).toEqual({ device_id: "d-1", session_id: createMsg.id, write: false, ttl_ms: 3_600_000 });
    expect(create?.headers["X-NexTerm-CSRF"]).toBe("csrf-168");

    // 一次性 URL 只在创建响应展示, 带复制与吊销指引
    await flushUntil(() => bodyText().includes(LINK_TOKEN));
    expect(bodyText()).toContain(`${window.location.origin}/share/public/${LINK_TOKEN}`);
    expect(bodyText()).toContain("仅显示一次");
    expect(bodyText()).toContain("无需账号");
    expect(bodyText()).toContain("设置 → 分享");
    expect(bodyText()).toContain("吊销");
    expect(webStorageText()).not.toContain(LINK_TOKEN);
  });

  it("显式勾选读写并选择 TTL 后按 read_write + 所选 ttl 创建", async () => {
    const calls = route();
    await mountReadyTerminal();
    openSharePanel();

    const ttl = document.querySelector<HTMLSelectElement>('select[aria-label="链接有效期"]');
    expect(ttl).not.toBeNull();
    setSelectValue(ttl as HTMLSelectElement, String(24 * 60 * 60_000));
    const write = [...document.querySelectorAll('input[type="checkbox"]')].find((c) => c.closest("label")?.textContent?.includes("允许读写"));
    expect(write).toBeTruthy();
    click(write as HTMLInputElement);
    click(createButton());

    await flushUntil(() => calls.some((c) => c.url === "/share/links"));
    const create = calls.find((c) => c.url === "/share/links");
    expect(create?.body).toMatchObject({ write: true, ttl_ms: 86_400_000 });
    await flushUntil(() => bodyText().includes("读写"));
  });

  it("在途创建时关闭面板: 晚到成功响应失效, 重开面板不显示旧 token", async () => {
    const late = deferred<void>();
    shareBehavior.deferred = { promise: late.promise };
    route();
    await mountReadyTerminal();
    openSharePanel();
    click(createButton());
    await flushUntil(() => bodyText().includes("创建中…"));

    // 创建在途时点「分享」关闭面板 (不是卸载): 代次递增 + creating 复位
    const shareToggle = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "分享");
    expect(shareToggle).toBeTruthy();
    click(shareToggle as HTMLButtonElement);
    await flush();
    expect(bodyText()).not.toContain("创建中…");

    // 放行晚到成功响应: 不得写入一次性 URL
    late.resolve();
    await flush();
    await flush();
    expect(bodyText()).not.toContain(LINK_TOKEN);
    expect(webStorageText()).not.toContain(LINK_TOKEN);

    // 重新打开是全新表单: 不显示旧 token, 可直接再次创建
    openSharePanel();
    await flush();
    expect(bodyText()).not.toContain(LINK_TOKEN);
    expect(bodyText()).toContain("创建公开链接");
    shareBehavior.deferred = undefined;
  });

  it("成功后再关闭面板同样清除一次性 URL", async () => {
    route();
    await mountReadyTerminal();
    openSharePanel();
    click(createButton());
    await flushUntil(() => bodyText().includes(LINK_TOKEN));

    const done = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "完成");
    expect(done).toBeTruthy();
    click(done as HTMLButtonElement);
    await flush();
    expect(bodyText()).not.toContain(LINK_TOKEN);
    expect(webStorageText()).not.toContain(LINK_TOKEN);
  });

  it("clipboard 不可用时复制公开链接给出明确错误", async () => {
    route();
    await mountReadyTerminal();
    openSharePanel();
    click(createButton());
    await flushUntil(() => bodyText().includes(LINK_TOKEN));

    useUi.setState({ toasts: [] });
    vi.stubGlobal("navigator", { clipboard: undefined });
    const copy = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "复制链接");
    expect(copy).toBeTruthy();
    click(copy as HTMLButtonElement);

    expect(useUi.getState().toasts).toEqual([
      expect.objectContaining({ kind: "error", text: "复制失败：剪贴板不可用，请手动复制" }),
    ]);
  });

  it("创建失败给显式错误, 不展示任何 URL", async () => {
    shareBehavior.failStatus = 500;
    route();
    await mountReadyTerminal();
    openSharePanel();
    click(createButton());

    await flushUntil(() => bodyText().includes("创建公开链接失败"));
    expect(bodyText()).toContain("数据库错误");
    expect(bodyText()).not.toContain("/share/public/");
  });

  it("断网 (fetch 拒绝) 给断线错误态, 可重试创建", async () => {
    mocks.fetch.mockImplementation(async (url: string) => {
      const u = String(url);
      if (u.includes("/fleet/devices")) {
        return { ok: true, status: 200, text: async () => JSON.stringify({ devices: [DEVICE] }) };
      }
      if (u.includes("/fleet/base-urls")) {
        return { ok: true, status: 200, text: async () => JSON.stringify({ base_urls: [] }) };
      }
      throw new Error("network down");
    });
    await mountReadyTerminal();
    openSharePanel();
    click(createButton());

    await flushUntil(() => bodyText().includes("创建公开链接失败"));
    expect(bodyText()).toContain("无法连接服务器");
    expect(createButton().disabled).toBe(false);
  });

  it("在途创建时卸载 (账号边界关闭标签路径): 晚到响应不写入, token 永不出现", async () => {
    const late = deferred<void>();
    shareBehavior.deferred = { promise: late.promise };
    route();
    await mountReadyTerminal();
    openSharePanel();
    click(createButton());
    await flushUntil(() => bodyText().includes("创建中…"));

    mounted?.unmount();
    mounted = null;
    late.resolve();
    await flush();
    await flush();

    expect(webStorageText()).not.toContain(LINK_TOKEN);
    expect(document.body.textContent ?? "").not.toContain(LINK_TOKEN);
  });

  it("会话自然结束后分享面板关闭, 已创建的一次性 URL 立即清除", async () => {
    route();
    await mountReadyTerminal();
    openSharePanel();
    click(createButton());
    await flushUntil(() => bodyText().includes(LINK_TOKEN));

    FakeWebSocket.instances[1].serverJSON(SupervisorFrameKind.Exit, { code: 0 });
    await flushUntil(() => bodyText().includes("已结束"));
    expect(bodyText()).not.toContain(LINK_TOKEN);
    expect(bodyText()).not.toContain("分享");
  });

  it("sharePublicUrl 无 ?api= 时回退当前页面源", () => {
    expect(sharePublicUrl("tok-1")).toBe(`${window.location.origin}/share/public/tok-1`);
  });
});
