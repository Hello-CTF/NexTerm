/** @vitest-environment jsdom */
// FLEET149 交互测试: 角色差异(超管看 owner/编辑接入地址, 普通用户只读)、
// 一次性接入码签发与安装指令、吊销确认、期望/实际自启动(离线语义)、指标展示。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flushUntil, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    fetch: vi.fn(),
    ask: vi.fn(),
  };
});

vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { DevicesView } from "../../features/fleet/DevicesView";
import { useAuth } from "../../features/auth/store";

const NOW = Date.now();

const SUPERADMIN = {
  id: "u-1",
  username: "root",
  display_name: "Root",
  role: "superadmin" as const,
  state: "active" as const,
  must_change_password: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};

const PLAIN_USER = { ...SUPERADMIN, id: "u-2", username: "alice", role: "user" as const };

const AGENT_DEVICE = {
  id: "d-1",
  name: "web-01",
  kind: "agent",
  created_at: NOW - 86400_000,
  last_seen_at: NOW - 30_000,
  revoked_at: 0,
  owner: { id: "u-1", username: "alice" },
  agent: {
    platform: "linux",
    app_version: "0.2.2",
    desired_autostart: true,
    terminal_enabled: true,
    current_url: "https://nexterm.example.com",
    service_state: { installed: true, enabled: true, active: true, last_reconcile_at: NOW - 60_000 },
    last_seen_at: NOW - 30_000,
  },
};

const OFFLINE_DEVICE = {
  id: "d-2",
  name: "db-02",
  kind: "agent",
  created_at: NOW - 172800_000,
  last_seen_at: NOW - 3600_000,
  revoked_at: 0,
  owner: { id: "u-1", username: "alice" },
  agent: {
    platform: "windows",
    app_version: "0.2.2",
    desired_autostart: false,
    terminal_enabled: false,
    current_url: "",
    service_state: { installed: true, enabled: false, active: false, last_reconcile_at: NOW - 3600_000 },
    last_seen_at: NOW - 3600_000,
  },
};

const BROWSER_DEVICE = {
  id: "d-3",
  name: "browser-01",
  kind: "browser",
  created_at: NOW - 3600_000,
  last_seen_at: NOW - 60_000,
  revoked_at: 0,
};

const REVOKED_DEVICE = {
  ...AGENT_DEVICE,
  id: "d-4",
  name: "old-laptop",
  revoked_at: NOW - 3600_000,
};

const BASE_URLS = [{ url: "https://nexterm.example.com" }, { url: "http://10.0.0.8:8080", insecure: true }];

const METRICS_SAMPLES = [
  {
    ts: NOW - 60_000,
    cpu_pct: 12.5,
    mem_used: 8589934592,
    mem_total: 17179869184,
    disk_used: 107374182400,
    disk_total: 214748364800,
    uptime_s: 93784,
  },
];

interface RecordedCall {
  url: string;
  method: string;
  body?: unknown;
}

function route(
  handler: (url: string, method: string, body?: unknown) => { status?: number; payload: unknown },
): RecordedCall[] {
  const calls: RecordedCall[] = [];
  mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
    const u = String(url);
    const method = init.method ?? "GET";
    const body = init.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ url: u, method, ...(body === undefined ? {} : { body }) });
    const result = handler(u, method, body);
    const status = result.status ?? 200;
    return {
      ok: status < 400,
      status,
      text: async () => JSON.stringify(result.payload),
    };
  });
  vi.stubGlobal("fetch", mocks.fetch);
  return calls;
}

function fleetHandler(overrides?: {
  devices?: unknown;
  baseUrls?: unknown;
  metrics?: unknown;
  enrollCode?: unknown;
}) {
  return (url: string, method: string, body?: unknown): { status?: number; payload: unknown } => {
    if (url === "/fleet/devices" && method === "GET") {
      return { payload: overrides?.devices ?? { devices: [AGENT_DEVICE, OFFLINE_DEVICE, BROWSER_DEVICE, REVOKED_DEVICE] } };
    }
    if (url === "/fleet/base-urls" && method === "GET") {
      return { payload: overrides?.baseUrls ?? { base_urls: BASE_URLS } };
    }
    if (url === "/fleet/devices/d-1/metrics" && method === "GET") {
      return { payload: overrides?.metrics ?? { samples: METRICS_SAMPLES } };
    }
    if (url === "/device/enroll-codes" && method === "POST") {
      return { payload: overrides?.enrollCode ?? { code: "fleet-code-1", expires_at: NOW + 900_000 } };
    }
    if (/\/revoke$/.test(url) && method === "POST") return { payload: { ok: true } };
    if (/\/autostart$/.test(url) && method === "POST") {
      const desired = Boolean((body as { desired?: boolean } | undefined)?.desired);
      return { payload: { ok: true, desired_autostart: desired } };
    }
    if (url === "/fleet/base-urls" && method === "PUT") {
      return { payload: { base_urls: (overrides?.baseUrls as { base_urls: unknown[] } | undefined)?.base_urls ?? BASE_URLS } };
    }
    return { status: 404, payload: { error: { code: "not_found", message: url } } };
  };
}

let mounted: MountedView | undefined;

function seedUser(user: typeof SUPERADMIN | typeof PLAIN_USER) {
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
  vi.clearAllMocks();
  document.body.replaceChildren();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

describe("设备管理视图 · 角色差异", () => {
  it("超管看到 owner 徽章与接入地址编辑器", async () => {
    route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    expect(document.body.textContent).toContain("alice");
    expect(document.body.textContent).toContain("接入地址");
    expect(document.querySelector('input[aria-label="新接入地址"]')).not.toBeNull();
    expect(document.body.textContent).toContain("共 4 台");
  });

  it("普通用户看不到 owner 徽章与编辑器, 接入地址只读", async () => {
    route(fleetHandler());
    seedUser(PLAIN_USER);
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    expect(document.querySelector('input[aria-label="新接入地址"]')).toBeNull();
    expect(document.body.textContent).toContain("仅超级管理员可修改");
    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("web-01"));
    expect(card?.textContent).not.toContain("alice");
  });

  it("未登录时提示先登录, 不发任何 fleet 请求", async () => {
    const calls = route(fleetHandler());
    useAuth.setState({ user: null, gate: "ready", status: { initialized: true, registration_open: false, auth: "on" } });
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("登录后才能查看") ?? false);

    expect(calls.filter((c) => c.url.startsWith("/fleet") || c.url.startsWith("/device"))).toHaveLength(0);
  });
});

describe("设备管理视图 · 设备列表与指标", () => {
  it("在线设备显示心跳与当前接入地址, 指标展开展示 CPU/内存/磁盘/运行时长", async () => {
    route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("web-01"));
    expect(card?.textContent).toContain("在线");
    expect(card?.textContent).toContain("https://nexterm.example.com");

    const metricsButton = [...(card?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === "指标");
    expect(metricsButton).not.toBeNull();
    click(metricsButton as HTMLButtonElement);
    await flushUntil(() => card?.textContent?.includes("12.5%") ?? false);
    expect(card?.textContent).toContain("8.0 GiB / 16.0 GiB");
    expect(card?.textContent).toContain("100 GiB / 200 GiB");
    expect(card?.textContent).toContain("1 天 2 小时");
  });

  it("离线设备显示离线徽章与离线自启动提示", async () => {
    route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("db-02") ?? false);

    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("db-02"));
    expect(card?.textContent).toContain("离线");
    expect(card?.textContent).toContain("设备离线: 修改只会保存为期望状态, 上线后生效");
    expect(card?.textContent).toContain("未运行");
  });

  it("非 agent 设备提示没有运行状态, 已吊销设备不再给操作按钮", async () => {
    route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("browser-01") ?? false);

    const browserCard = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("browser-01"));
    expect(browserCard?.textContent).toContain("没有运行状态与指标");
    expect([...(browserCard?.querySelectorAll("button") ?? [])].some((b) => b.textContent?.trim() === "指标")).toBe(false);

    const revokedCard = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("old-laptop"));
    expect(revokedCard?.textContent).toContain("已吊销");
    expect([...(revokedCard?.querySelectorAll("button") ?? [])].some((b) => b.textContent?.includes("吊销"))).toBe(false);
  });

  it("空列表给接入引导, 列表失败给错误与重试", async () => {
    route(fleetHandler({ devices: { devices: [] } }));
    seedUser(SUPERADMIN);
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("还没有设备接入") ?? false);
    mounted.unmount();

    route(() => ({ status: 500, payload: { error: { code: "internal", message: "数据库错误" } } }));
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("设备列表加载失败") ?? false);
    expect(document.body.textContent).toContain("数据库错误");
  });
});

describe("设备管理视图 · 接入码签发", () => {
  it("签发后只显示一次, 带有效期/单次使用说明与真实 agent 接入命令", async () => {
    const calls = route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const openButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("接入新设备"));
    click(openButton as HTMLButtonElement);
    const issueButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "签发接入码");
    click(issueButton as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("fleet-code-1") ?? false);

    const enrollCall = calls.find((c) => c.url === "/device/enroll-codes");
    expect(enrollCall?.method).toBe("POST");
    expect(enrollCall?.body).toEqual({ ttl_ms: 900_000 });

    const text = document.body.textContent ?? "";
    expect(text).toContain("只显示这一次");
    expect(text).toContain("单次使用");
    expect(text).toContain("nexterm-desktop agent enroll --server https://nexterm.example.com --code fleet-code-1");
    expect(text).toContain("nexterm-desktop agent enroll --server http://10.0.0.8:8080 --code fleet-code-1 --insecure");
    expect(text).toContain("nexterm-desktop agent install");
    expect(text).toContain("不会替你安装");

    const doneButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "完成");
    click(doneButton as HTMLButtonElement);
    await waitFor(() => {
      expect(document.body.textContent).not.toContain("fleet-code-1");
    });
  });
});

describe("设备管理视图 · 吊销与自启动", () => {
  it("吊销需确认, 确认后 POST 并刷新列表", async () => {
    const calls = route(fleetHandler());
    mocks.ask.mockResolvedValue(true);
    seedUser(SUPERADMIN);
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("web-01"));
    const revokeButton = [...(card?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.includes("吊销"));
    click(revokeButton as HTMLButtonElement);
    await flushUntil(() => calls.some((c) => c.url === "/fleet/devices/d-1/revoke"));

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("不可恢复");
    expect(calls.filter((c) => c.url === "/fleet/devices" && c.method === "GET").length).toBeGreaterThanOrEqual(2);
  });

  it("吊销确认被取消时不发请求", async () => {
    const calls = route(fleetHandler());
    mocks.ask.mockResolvedValue(false);
    seedUser(SUPERADMIN);
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("web-01"));
    const revokeButton = [...(card?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.includes("吊销"));
    click(revokeButton as HTMLButtonElement);
    await flushUntil(() => mocks.ask.mock.calls.length === 1);

    expect(calls.some((c) => c.url.includes("/revoke"))).toBe(false);
  });

  it("在线设备切换自启动直接生效, 不弹确认", async () => {
    const calls = route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("web-01"));
    const checkbox = card?.querySelector<HTMLInputElement>('input[type="checkbox"]');
    expect(checkbox?.checked).toBe(true);
    click(checkbox as HTMLInputElement);
    await flushUntil(() => calls.some((c) => c.url === "/fleet/devices/d-1/autostart"));

    expect(mocks.ask).not.toHaveBeenCalled();
    const autostartCall = calls.find((c) => c.url === "/fleet/devices/d-1/autostart");
    expect(autostartCall?.body).toEqual({ desired: false });
  });

  it("离线设备切换自启动先确认'只存期望状态'", async () => {
    const calls = route(fleetHandler());
    mocks.ask.mockResolvedValue(true);
    seedUser(SUPERADMIN);
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("db-02") ?? false);

    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("db-02"));
    const checkbox = card?.querySelector<HTMLInputElement>('input[type="checkbox"]');
    expect(checkbox?.checked).toBe(false);
    click(checkbox as HTMLInputElement);
    await flushUntil(() => calls.some((c) => c.url === "/fleet/devices/d-2/autostart"));

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("离线");
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("期望状态");
    const autostartCall = calls.find((c) => c.url === "/fleet/devices/d-2/autostart");
    expect(autostartCall?.body).toEqual({ desired: true });
  });
});
