/** @vitest-environment jsdom */
// 桌面端设备管理 · 已登录: 数据全部走 remote* 代理 (remote_fleet_* / remote_healthz),
// 列表/签发/吊销/审批/自启/指标/接入地址与 WEB 同视图; 桌面无 /ws/events 通道,
// 在线状态只靠 30 秒轮询刷新, 不订阅 device://status。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flushUntil, mountWithClient, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    status: vi.fn(),
    devices: vi.fn(),
    issueEnrollCode: vi.fn(),
    revoke: vi.fn(),
    approve: vi.fn(),
    reject: vi.fn(),
    setAutostart: vi.fn(),
    metrics: vi.fn(),
    baseUrls: vi.fn(),
    putBaseUrls: vi.fn(),
    serverVersion: vi.fn(),
    adminUsers: vi.fn(),
    ask: vi.fn(),
    listenEvent: vi.fn(),
    fetch: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    syncApi: { ...actual.syncApi, status: mocks.status },
  };
});
vi.mock("../../ipc/remoteApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/remoteApi")>();
  return {
    ...actual,
    remoteFleetApi: {
      devices: mocks.devices,
      issueEnrollCode: mocks.issueEnrollCode,
      revoke: mocks.revoke,
      approve: mocks.approve,
      reject: mocks.reject,
      setAutostart: mocks.setAutostart,
      metrics: mocks.metrics,
      baseUrls: mocks.baseUrls,
      putBaseUrls: mocks.putBaseUrls,
    },
    remoteHealthApi: { serverVersion: mocks.serverVersion },
    remoteAccountApi: { ...actual.remoteAccountApi, adminUsers: mocks.adminUsers },
  };
});
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));
vi.mock("../../ipc/events", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/events")>()),
  listenEvent: mocks.listenEvent,
}));

import { DevicesView } from "../../features/fleet/DevicesView";

const NOW = Date.now();

const SUPERADMIN_USER = {
  id: "u-1",
  username: "root",
  display_name: "Root",
  role: "superadmin",
  state: "active",
  must_change_password: false,
  mfa_enabled: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};

const AGENT_DEVICE = {
  id: "d-1",
  name: "web-01",
  kind: "agent",
  created_at: NOW - 86400_000,
  last_seen_at: NOW - 30_000,
  revoked_at: 0,
  owner: { id: "u-1", username: "root" },
  agent: {
    platform: "linux",
    app_version: "1.2.3",
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
  agent: {
    platform: "windows",
    app_version: "1.2.3",
    desired_autostart: false,
    terminal_enabled: false,
    current_url: "",
    service_state: { installed: true, enabled: false, active: false, last_reconcile_at: NOW - 3600_000 },
    last_seen_at: NOW - 3600_000,
  },
};

const PENDING_DEVICE = {
  id: "d-5",
  name: "gpu-03",
  kind: "agent",
  state: "pending" as const,
  created_at: NOW - 600_000,
  last_seen_at: 0,
  revoked_at: 0,
  owner: { id: "u-2", username: "bob" },
  agent: {
    platform: "linux",
    app_version: "1.2.3",
    desired_autostart: true,
    terminal_enabled: true,
    current_url: "",
    service_state: {},
    last_seen_at: 0,
  },
};

const BASE_URLS = [{ url: "https://nexterm.example.com" }];

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

let mounted: MountedView | undefined;

function seedLoggedIn(admin = true) {
  mocks.status.mockResolvedValue({
    configured: true,
    loggedIn: true,
    username: "root",
    userId: "u-1",
    seq: 0,
    verifiedAt: 1,
    lastError: "",
  });
  if (admin) {
    mocks.adminUsers.mockResolvedValue({ users: [SUPERADMIN_USER] });
  } else {
    mocks.adminUsers.mockRejectedValue(new Error("forbidden"));
  }
}

function seedFleet(devices: unknown[] = [AGENT_DEVICE, OFFLINE_DEVICE]) {
  mocks.devices.mockResolvedValue({ devices });
  mocks.baseUrls.mockResolvedValue({ base_urls: BASE_URLS });
  mocks.metrics.mockResolvedValue({ samples: METRICS_SAMPLES });
  mocks.serverVersion.mockResolvedValue("1.2.3");
  mocks.issueEnrollCode.mockResolvedValue({ code: "desk-code-1", expires_at: NOW + 900_000 });
  mocks.revoke.mockResolvedValue({ ok: true });
  mocks.approve.mockResolvedValue({ ok: true });
  mocks.reject.mockResolvedValue({ ok: true });
  mocks.setAutostart.mockImplementation((_id: string, desired: boolean) =>
    Promise.resolve({ ok: true, desired_autostart: desired }),
  );
  mocks.putBaseUrls.mockImplementation((entries: unknown) => Promise.resolve({ base_urls: entries }));
}

function cardOf(name: string): HTMLElement | undefined {
  return [...document.querySelectorAll<HTMLElement>(".nx-card")].find((c) => c.textContent?.includes(name));
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.listenEvent.mockResolvedValue(() => {});
  vi.stubGlobal("fetch", mocks.fetch);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("设备管理 · 桌面端已登录", () => {
  it("列表经 remote 代理渲染, 在线状态按心跳判定, 不订阅 device://status 事件", async () => {
    seedLoggedIn();
    seedFleet();
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    expect(mocks.devices).toHaveBeenCalled();
    expect(mocks.baseUrls).toHaveBeenCalled();
    expect(cardOf("web-01")?.textContent).toContain("在线");
    expect(cardOf("db-02")?.textContent).toContain("离线");
    expect(document.body.textContent).toContain("共 2 台");
    expect(mocks.listenEvent).not.toHaveBeenCalled();
    expect(mocks.fetch).not.toHaveBeenCalled();
  });

  it("30 秒轮询刷新列表 (桌面无事件推送, 轮询是唯一在线信号来源)", async () => {
    vi.useFakeTimers();
    seedLoggedIn();
    seedFleet();
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);
    const before = mocks.devices.mock.calls.length;

    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(mocks.devices.mock.calls.length).toBeGreaterThan(before);
  });

  it("指标展开走 remote metrics 并可刷新", async () => {
    seedLoggedIn();
    seedFleet();
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const card = cardOf("web-01") as HTMLElement;
    click([...card.querySelectorAll("button")].find((b) => b.textContent?.trim() === "指标") as HTMLButtonElement);
    await flushUntil(() => card.textContent?.includes("12.5%") ?? false);
    expect(mocks.metrics).toHaveBeenCalledWith("d-1");
    expect(card.textContent).toContain("8.0 GiB / 16.0 GiB");

    click(card.querySelector<HTMLButtonElement>('button[aria-label="刷新指标"]') as HTMLButtonElement);
    await flushUntil(() => mocks.metrics.mock.calls.length === 2);
  });

  it("签发接入码走 remote enroll, 一键安装命令版本来自 remote healthz", async () => {
    seedLoggedIn();
    seedFleet();
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("接入新设备")) as HTMLButtonElement);
    await flushUntil(() => mocks.serverVersion.mock.calls.length === 1);
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "签发接入码") as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("desk-code-1") ?? false);

    expect(mocks.issueEnrollCode).toHaveBeenCalledWith(15 * 60_000);
    const text = document.body.textContent ?? "";
    expect(text).toContain("仅显示一次");
    expect(text).toContain("install-device.sh");
    expect(text).toContain("--version '1.2.3'");
    expect(text).toContain("install-device.ps1");
  });

  it("吊销需确认, 确认后走 remote revoke 并刷新列表", async () => {
    seedLoggedIn();
    seedFleet();
    mocks.ask.mockResolvedValue(true);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const card = cardOf("web-01") as HTMLElement;
    click([...card.querySelectorAll("button")].find((b) => b.textContent?.includes("吊销")) as HTMLButtonElement);
    await flushUntil(() => mocks.revoke.mock.calls.length === 1);

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.revoke).toHaveBeenCalledWith("d-1");
    expect(mocks.devices.mock.calls.length).toBeGreaterThanOrEqual(2);
  });

  it("超管批准待审批设备走 remote approve, 徽章与横幅随状态消失", async () => {
    seedLoggedIn();
    seedFleet([AGENT_DEVICE, PENDING_DEVICE]);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("gpu-03") ?? false);
    expect(document.body.textContent).toContain("1 台设备待审批");

    click([...(cardOf("gpu-03")?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === "批准") as HTMLButtonElement);
    await flushUntil(() => mocks.approve.mock.calls.length === 1);
    expect(mocks.approve).toHaveBeenCalledWith("d-5");
    await flushUntil(() => !(cardOf("gpu-03")?.textContent ?? "").includes("待审批"));
    expect(document.body.textContent).not.toContain("台设备待审批");
  });

  it("超管拒绝待审批设备需确认, 确认后走 remote reject 且卡片移除", async () => {
    seedLoggedIn();
    seedFleet([AGENT_DEVICE, PENDING_DEVICE]);
    mocks.ask.mockResolvedValue(true);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("gpu-03") ?? false);

    click([...(cardOf("gpu-03")?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === "拒绝") as HTMLButtonElement);
    await flushUntil(() => mocks.reject.mock.calls.length === 1);
    expect(mocks.reject).toHaveBeenCalledWith("d-5");
    await flushUntil(() => cardOf("gpu-03") === undefined);
  });

  it("切换期望自启动走 remote autostart", async () => {
    seedLoggedIn();
    seedFleet();
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const checkbox = cardOf("web-01")?.querySelector<HTMLInputElement>('input[type="checkbox"]') as HTMLInputElement;
    expect(checkbox.checked).toBe(true);
    click(checkbox);
    await flushUntil(() => mocks.setAutostart.mock.calls.length === 1);
    expect(mocks.setAutostart).toHaveBeenCalledWith("d-1", false);
  });

  it("接入地址保存走 remote putBaseUrls", async () => {
    seedLoggedIn();
    seedFleet();
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "展开编辑") as HTMLButtonElement);
    await flushUntil(() => document.querySelector('input[aria-label="新接入地址"]') !== null);
    setInputValue(document.querySelector('input[aria-label="新接入地址"]') as HTMLInputElement, "https://b.example.com");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "添加") as HTMLButtonElement);
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("保存修改")) as HTMLButtonElement);
    await flushUntil(() => mocks.putBaseUrls.mock.calls.length === 1);
    expect(mocks.putBaseUrls).toHaveBeenCalledWith([{ url: "https://nexterm.example.com" }, { url: "https://b.example.com" }]);
  });

  it("普通用户 (adminUsers 探测失败) 无审批按钮与接入地址编辑, 不显示 owner 徽章", async () => {
    seedLoggedIn(false);
    seedFleet([AGENT_DEVICE, PENDING_DEVICE]);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("gpu-03") ?? false);

    const card = cardOf("gpu-03") as HTMLElement;
    expect(card.textContent).toContain("待审批");
    expect([...card.querySelectorAll("button")].some((b) => b.textContent?.trim() === "批准")).toBe(false);
    expect([...card.querySelectorAll("button")].some((b) => b.textContent?.trim() === "拒绝")).toBe(false);
    expect(document.body.textContent).not.toContain("台设备待审批：批准后才能上线");

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "展开") as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("仅超级管理员可修改") ?? false);
    expect(document.querySelector('input[aria-label="新接入地址"]')).toBeNull();

    expect(cardOf("web-01")?.textContent).not.toContain("所有者");
  });
});
