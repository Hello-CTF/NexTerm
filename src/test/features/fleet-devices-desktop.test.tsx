/** @vitest-environment jsdom */
// Desktop device management - logged in: all data goes through the remote* proxies (remote_fleet_* / remote_healthz),
// list/issue/revoke/autostart/metrics/enrollment URLs share the WEB view; desktop has no /ws/events channel,
// online state refreshes only via 30-second polling, no device://status subscription.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, flushUntil as flushUntilRaw, mountWithClient, setInputValue, type MountedView } from "./reactTestUtils";

async function flushUntil(predicate: () => boolean): Promise<void> {
  if (predicate()) {
    await flush();
    return;
  }
  await flushUntilRaw(predicate);
}

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    status: vi.fn(),
    devices: vi.fn(),
    issueEnrollCode: vi.fn(),
    revoke: vi.fn(),
    setAutostart: vi.fn(),
    metrics: vi.fn(),
    baseUrls: vi.fn(),
    putBaseUrls: vi.fn(),
    serverVersion: vi.fn(),
    adminUsers: vi.fn(),
    ask: vi.fn(),
    listenEvent: vi.fn(),
    fetch: vi.fn(),
    assetList: vi.fn(),
    assetSearch: vi.fn(),
    groupList: vi.fn(),
    snippetList: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      ...actual.assetApi,
      list: mocks.assetList,
      search: mocks.assetSearch,
      groupList: mocks.groupList,
      snippetList: mocks.snippetList,
    },
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

import { AssetTree } from "../../features/explorer/AssetTree";
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
  mocks.assetList.mockResolvedValue([]);
  mocks.assetSearch.mockResolvedValue([]);
  mocks.groupList.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([]);
  vi.stubGlobal("fetch", mocks.fetch);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("Device management - desktop logged in", () => {
  it("renders the device list through remote proxies, determines online state from heartbeats, and does not subscribe to device://status events", async () => {
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

  it("shows a newly online device in the asset tree immediately after a manual refresh through remote proxies", async () => {
    let devices: unknown[] = [];
    seedLoggedIn();
    seedFleet();
    mocks.devices.mockImplementation(() => Promise.resolve({ devices }));
    mounted = mountWithClient(
      createElement("div", null, createElement(AssetTree), createElement(DevicesView)),
    );
    await flushUntil(() => document.body.textContent?.includes("共 0 台") ?? false);
    await flushUntil(() => mocks.devices.mock.calls.length >= 2);
    await flushUntil(() => document.querySelector('[role="tree"]')?.textContent?.includes("还没有资产") ?? false);
    await flush();
    expect(document.querySelector('[role="tree"]')?.textContent ?? "").not.toContain("web-01");

    devices = [AGENT_DEVICE];
    const before = mocks.devices.mock.calls.length;
    const management = document.querySelector<HTMLElement>(".nx-pane");
    if (!management) throw new Error("DevicesView did not render");
    const refresh = [...management.querySelectorAll("button")].find((button) => button.textContent?.trim() === "刷新");
    if (!refresh) throw new Error("Refresh button not found");
    click(refresh);

    await flushUntil(() => management.textContent?.includes("共 1 台") ?? false);
    await flushUntil(() => document.querySelector('[role="tree"]')?.textContent?.includes("web-01") ?? false);
    expect(management.textContent).toContain("web-01");
    expect(mocks.devices.mock.calls.length).toBe(before + 1);
    expect(mocks.fetch).not.toHaveBeenCalled();
  });

  it("polls every 30 seconds to refresh the list (desktop has no event push, so polling is the only online signal)", async () => {
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

  it("loads independent latest and window metadata through remote metrics", async () => {
    seedLoggedIn();
    seedFleet();
    mocks.metrics.mockResolvedValue({
      samples: METRICS_SAMPLES,
      latest: { ...METRICS_SAMPLES[0], cpu_pct: 99 },
      windowStart: NOW - 86400_000,
      windowEnd: NOW,
      sampleCount: 17280,
      downsampled: true,
      sampleIntervalMs: 60_000,
    });
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const card = cardOf("web-01") as HTMLElement;
    click([...card.querySelectorAll("button")].find((b) => b.textContent?.trim() === "指标") as HTMLButtonElement);
    await flushUntil(() => card.textContent?.includes("99.0%") ?? false);
    expect(mocks.metrics).toHaveBeenCalledWith("d-1");
    expect(card.textContent).toContain("8.0 GiB / 16.0 GiB");
    expect(card.textContent).toContain("共 17280 个采样点");
    expect(card.textContent).toContain("显示 1 个原始点");

    click(card.querySelector<HTMLButtonElement>('button[aria-label="刷新指标"]') as HTMLButtonElement);
    await flushUntil(() => mocks.metrics.mock.calls.length === 2);
  });

  it("issues an enrollment code via remote enroll and uses the remote healthz version in one-click install commands", async () => {
    seedLoggedIn();
    seedFleet();
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("接入新设备")) as HTMLButtonElement);
    await flushUntil(() => mocks.serverVersion.mock.calls.length === 1);
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "生成接入码") as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("desk-code-1") ?? false);

    expect(mocks.issueEnrollCode).toHaveBeenCalledWith(15 * 60_000);
    const text = document.body.textContent ?? "";
    expect(text).toContain("完成后不再显示");
    expect(text).toContain("设备凭证原文仅保存在设备本地，服务端只保存散列");
    expect(text).toContain("install-device.sh");
    expect(text).toContain("--version '1.2.3'");
    expect(text).toContain("install-device.ps1");
  });

  it("requires confirmation to revoke a device, then uses remote revoke and refreshes the list", async () => {
    seedLoggedIn();
    seedFleet();
    mocks.ask.mockResolvedValue(true);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const card = cardOf("web-01") as HTMLElement;
    click(card.querySelector<HTMLButtonElement>('button[aria-label="更多操作"]') as HTMLButtonElement);
    click([...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((b) => b.textContent?.includes("撤销接入")) as HTMLButtonElement);
    await flushUntil(() => mocks.revoke.mock.calls.length === 1);

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.revoke).toHaveBeenCalledWith("d-1");
    expect(mocks.devices.mock.calls.length).toBeGreaterThanOrEqual(2);
  });

  it("toggles desired autostart via remote autostart", async () => {
    seedLoggedIn();
    seedFleet();
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click([...(cardOf("web-01")?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === "详情") as HTMLButtonElement);
    const checkbox = cardOf("web-01")?.querySelector<HTMLInputElement>('input[type="checkbox"]') as HTMLInputElement;
    expect(checkbox.checked).toBe(true);
    click(checkbox);
    await flushUntil(() => mocks.setAutostart.mock.calls.length === 1);
    expect(mocks.setAutostart).toHaveBeenCalledWith("d-1", false);
  });

  it("saves base URLs via remote putBaseUrls", async () => {
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

  it("hides base URL editing and owner badges for regular users when the adminUsers probe fails", async () => {
    seedLoggedIn(false);
    seedFleet();
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "展开") as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("仅超级管理员可修改") ?? false);
    expect(document.querySelector('input[aria-label="新接入地址"]')).toBeNull();

    expect(cardOf("web-01")?.textContent).not.toContain("所有者");
  });
});
