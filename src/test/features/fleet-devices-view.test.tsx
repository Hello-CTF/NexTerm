/** @vitest-environment jsdom */
// FLEET149 interaction tests: role differences (super admin sees owner / edits enrollment URLs, regular users read-only),
// one-time enrollment code issuance and install command, revoke confirmation, desired/actual autostart (offline semantics), metrics display,
// device://status online/offline events flip the online state in real time.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, deferred, flush, flushUntil as flushUntilRaw, mountWithClient, setInputValue, setSelectValue, waitForRendered, type MountedView } from "./reactTestUtils";

async function flushUntil(predicate: () => boolean): Promise<void> {
  if (predicate()) {
    await flush();
    return;
  }
  await flushUntilRaw(predicate);
}

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    fetch: vi.fn(),
    ask: vi.fn(),
    listenEvent: vi.fn(),
    deviceStatusHandlers: [] as ((payload: unknown) => void)[],
  };
});

vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));
vi.mock("../../ipc/events", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/events")>()),
  listenEvent: mocks.listenEvent,
}));
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: Math.min(count, 8) }, (_, index) => ({
        index,
        start: index * 164,
        size: 152,
        key: index,
      })),
    getTotalSize: () => count * 164,
    measureElement: () => {},
  }),
}));

import { DevicesView } from "../../features/fleet/DevicesView";
import { MetricsPanel } from "../../features/fleet/MetricsPanel";
import { metricsGapThreshold } from "../../features/fleet/MetricsTrend";
import { formatBytes, formatTime } from "../../features/fleet/format";
import { useAuth } from "../../features/auth/store";
import { useUi } from "../../app/store";

const NOW = Date.now();

function buttonByText(root: ParentNode | null | undefined, text: string): HTMLButtonElement | undefined {
  return [...(root?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === text);
}

function expandDetails(card: Element | null | undefined): void {
  const toggle = buttonByText(card, "详情");
  if (!toggle) throw new Error("details toggle not found");
  if (toggle.getAttribute("aria-expanded") !== "true") click(toggle);
}

function pickMenuItem(card: Element | null | undefined, label: string): void {
  const more = card?.querySelector<HTMLButtonElement>('button[aria-label="更多操作"]');
  if (!more) throw new Error("more button not found");
  click(more);
  const item = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((b) => b.textContent?.includes(label));
  if (!item) throw new Error(`menu item ${label} not found`);
  click(item);
}

const SUPERADMIN = {
  id: "u-1",
  username: "root",
  display_name: "Root",
  role: "superadmin" as const,
  state: "active" as const,
  must_change_password: false,
  mfa_enabled: false,
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

interface MockResponse {
  ok: boolean;
  status: number;
  text: () => Promise<string>;
}

function jsonResponse(payload: unknown): MockResponse {
  return { ok: true, status: 200, text: async () => JSON.stringify(payload) };
}

function metricAt(ts: number, cpuPct: number) {
  return { ts, cpu_pct: cpuPct, mem_used: 100, mem_total: 200, disk_used: 300, disk_total: 400, uptime_s: 5000 };
}

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
      return {
        payload: overrides?.metrics ?? {
          samples: METRICS_SAMPLES,
          latest: METRICS_SAMPLES[METRICS_SAMPLES.length - 1],
          windowStart: METRICS_SAMPLES[0].ts,
          windowEnd: NOW,
          sampleCount: METRICS_SAMPLES.length,
        },
      };
    }
    if (url === "/device/enroll-codes" && method === "POST") {
      return { payload: overrides?.enrollCode ?? { code: "fleet-code-1", expires_at: NOW + 900_000 } };
    }
    if (/\/revoke$/.test(url) && method === "POST") return { payload: { ok: true } };
    if (/\/autostart$/.test(url) && method === "POST") {
      const desired = Boolean((body as { desired?: boolean } | undefined)?.desired);
      return { payload: { ok: true, desired_autostart: desired } };
    }
    if (/\/terminal-access$/.test(url) && method === "POST") {
      const enabled = Boolean((body as { enabled?: boolean } | undefined)?.enabled);
      return { payload: { ok: true, terminal_enabled: enabled } };
    }
    if (url === "/fleet/base-urls" && method === "PUT") {
      return { payload: { base_urls: (overrides?.baseUrls as { base_urls: unknown[] } | undefined)?.base_urls ?? BASE_URLS } };
    }
    return { status: 404, payload: { error: { code: "not_found", message: url } } };
  };
}

let mounted: MountedView | undefined;

function mountMetrics(deviceId = "d-1", online = true): HTMLDivElement {
  mounted = mountWithClient(createElement(MetricsPanel, { deviceId, online }));
  return mounted.container;
}

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
  mocks.deviceStatusHandlers.length = 0;
  mocks.listenEvent.mockImplementation((event: string, handler: (payload: unknown) => void) => {
    if (event === "device://status") mocks.deviceStatusHandlers.push(handler);
    return Promise.resolve(() => {});
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

describe("Device management view - role differences", () => {
  it("does not label the tool's UI ownership as global", async () => {
    route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);
    expect(document.querySelector(".nx-toolbar")?.textContent).toContain("设备管理");
    expect(
      document.querySelector('.nx-toolbar span[title="全局面板：内容不随工作区切换"]'),
    ).toBeNull();
  });

  it("shows the owner badge and base URL editor to superadmins", async () => {
    route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    expect(document.body.textContent).toContain("alice");
    expect(document.body.textContent).toContain("接入地址");
    const expand = [...document.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "展开编辑",
    );
    if (!expand) throw new Error("展开编辑按钮未找到");
    click(expand);
    await flushUntil(() => document.querySelector('input[aria-label="新接入地址"]') !== null);
    expect(document.body.textContent).toContain("共 4 台");
  });

  it("hides the owner badge and editor from regular users and keeps base URLs read-only", async () => {
    route(fleetHandler());
    seedUser(PLAIN_USER);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const expand = [...document.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "展开",
    );
    if (!expand) throw new Error("展开按钮未找到");
    click(expand);
    await flushUntil(() => document.body.textContent?.includes("仅超级管理员可修改") ?? false);
    expect(document.querySelector('input[aria-label="新接入地址"]')).toBeNull();
    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("web-01"));
    expect(card?.textContent).not.toContain("alice");
  });

  it("shows only the guide with zero buttons and no fleet requests when logged out, leaving login solely to the account card", async () => {
    const calls = route(fleetHandler());
    useAuth.setState({ user: null, gate: "ready", status: { initialized: true, registration_open: false, auth: "on" } });
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("登录后方可查看") ?? false);

    expect(document.body.textContent).toContain("登录入口位于「设置 → 账号同步」的「账号」卡片");
    expect(mounted.container.querySelectorAll("button").length).toBe(0);
    expect(calls.filter((c) => c.url.startsWith("/fleet") || c.url.startsWith("/device"))).toHaveLength(0);
  });

  it("accurately reports disabled account features with no login link or fleet requests when auth=off", async () => {
    const calls = route(fleetHandler());
    useAuth.setState({ user: null, gate: "ready", status: { initialized: false, registration_open: false, auth: "off" } });
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("已关闭账号功能") ?? false);

    expect(document.body.textContent).toContain("该服务器已关闭账号功能，设备管理不可用。");
    expect(document.body.textContent).not.toContain("登录入口位于");
    expect(mounted.container.querySelectorAll("button").length).toBe(0);
    expect(calls.filter((c) => c.url.startsWith("/fleet") || c.url.startsWith("/device"))).toHaveLength(0);
  });
});

describe("Device management view - device list and metrics", () => {
  it("shows heartbeat and current base URL for online devices and renders refreshable CPU, memory, disk, and uptime metrics when expanded", async () => {
    const calls = route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("web-01"));
    expect(card?.textContent).toContain("在线");
    expect(card?.textContent).toContain("上报");
    expect(card?.textContent).not.toContain("https://nexterm.example.com");
    expandDetails(card);
    expect(card?.textContent).toContain("https://nexterm.example.com");

    const metricsButton = [...(card?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === "指标");
    expect(metricsButton).not.toBeNull();
    click(metricsButton as HTMLButtonElement);
    await flushUntil(() => card?.textContent?.includes("12.5%") ?? false);
    expect(card?.textContent).toContain("8.0 GiB / 16.0 GiB");
    expect(card?.textContent).toContain("100 GiB / 200 GiB");
    expect(card?.textContent).toContain("1 天 2 小时");

    const refresh = card?.querySelector<HTMLButtonElement>('button[aria-label="刷新指标"]');
    expect(refresh).not.toBeNull();
    click(refresh as HTMLButtonElement);
    await flushUntil(() => calls.filter((c) => c.url === "/fleet/devices/d-1/metrics").length === 2);
  });

  it("uses the fallback freshness threshold for missing and invalid intervals", () => {
    expect(metricsGapThreshold(undefined)).toBe(180_000);
    expect(metricsGapThreshold(0)).toBe(180_000);
    expect(metricsGapThreshold(-1)).toBe(180_000);
    expect(metricsGapThreshold(Number.NaN)).toBe(180_000);
    expect(metricsGapThreshold(Number.POSITIVE_INFINITY)).toBe(180_000);
  });

  it("breaks a four-minute gap and expires latest when the interval is unknown", async () => {
    const start = NOW - 600_000;
    const samples = [
      metricAt(start, 1),
      metricAt(start + 60_000, 2),
      metricAt(start + 300_000, 3),
      metricAt(start + 360_000, 4),
    ];
    route(() => ({
      payload: {
        samples,
        latest: samples[samples.length - 1],
        windowStart: start,
        windowEnd: NOW,
        sampleCount: samples.length,
      },
    }));
    const container = mountMetrics();
    await flushUntil(() => container.querySelector("polyline") !== null);

    expect(container.querySelectorAll("polyline")).toHaveLength(6); // one trend line per metric chart, each broken into two segments
    expect(container.textContent).toContain("数据已");
    expect(container.textContent).toContain("未更新");
  });

  it("ages latest with the local clock and keeps late responses from reverting freshness", async () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(NOW);
      route(() => ({
        payload: {
          samples: [metricAt(NOW - 170_000, 11)],
          latest: metricAt(NOW - 170_000, 11),
          windowStart: NOW - 2 * 3600_000,
          windowEnd: NOW - 3600_000,
          sampleCount: 1,
          sampleIntervalMs: 60_000,
        },
      }));
      const container = mountMetrics();
      await flushUntil(() => container.textContent?.includes("11.0%") ?? false);
      expect(container.textContent).not.toContain("未更新");

      await act(async () => {
        await vi.advanceTimersByTimeAsync(11_000);
      });
      expect(container.textContent).toContain("未更新");

      const oldRequest = deferred<MockResponse>();
      mocks.fetch.mockImplementation(() => oldRequest.promise);
      click(container.querySelector('button[aria-label="刷新指标"]') as HTMLButtonElement);
      const nextRequest = deferred<MockResponse>();
      mocks.fetch.mockImplementation(() => nextRequest.promise);
      act(() => {
        mounted?.root.render(createElement(MetricsPanel, { deviceId: "d-2", online: true }));
      });
      nextRequest.resolve(jsonResponse({
        samples: [metricAt(NOW + 11_000 - 30_000, 22)],
        latest: metricAt(NOW + 11_000 - 30_000, 22),
        windowStart: NOW - 2 * 3600_000,
        windowEnd: NOW - 3600_000,
        sampleCount: 1,
        sampleIntervalMs: 60_000,
      }));
      await flushUntil(() => container.textContent?.includes("22.0%") ?? false);
      expect(container.textContent).not.toContain("未更新");

      oldRequest.resolve(jsonResponse({
        samples: [metricAt(NOW - 600_000, 33)],
        latest: metricAt(NOW - 600_000, 33),
        windowStart: NOW - 2 * 3600_000,
        windowEnd: NOW - 3600_000,
        sampleCount: 1,
        sampleIntervalMs: 60_000,
      }));
      await flush();
      expect(container.textContent).toContain("22.0%");
      expect(container.textContent).not.toContain("33.0%");
      expect(container.textContent).not.toContain("未更新");
    } finally {
      vi.useRealTimers();
    }
  });

  it("uses independent latest and reports the actual window, raw count, and stale state", async () => {
    const start = NOW - 24 * 3600_000;
    const latest = metricAt(NOW - 600_000, 99);
    route(() => ({
      payload: {
        samples: [metricAt(start, 1), metricAt(start + 60_000, 2)],
        latest,
        windowStart: start,
        windowEnd: NOW,
        sampleCount: 17280,
        downsampled: true,
        sampleIntervalMs: 60_000,
      },
    }));
    const container = mountMetrics();
    await flushUntil(() => container.textContent?.includes("99.0%") ?? false);

    expect(container.textContent).toContain("99.0%");
    expect(container.textContent).toContain("数据已 10 分钟未更新");
    expect(container.textContent).not.toContain("趋势窗口");
    expect(container.textContent).not.toContain("采样点");
    expect(container.textContent).not.toContain("过期阈值");
    expect(container.textContent).toContain(formatTime(latest.ts));
  });

  it("labels the y and x axes of every metric chart", async () => {
    const start = NOW - 24 * 3600_000;
    route(() => ({
      payload: {
        samples: [metricAt(NOW - 120_000, 1), metricAt(NOW - 60_000, 2)],
        latest: metricAt(NOW - 60_000, 2),
        windowStart: start,
        windowEnd: NOW,
        sampleIntervalMs: 60_000,
      },
    }));
    const container = mountMetrics();
    await flushUntil(() => container.querySelector("polyline") !== null);

    const charts = container.querySelectorAll('svg[role="img"]');
    expect(charts).toHaveLength(3);
    const yLabels = (svg: Element) => [...svg.querySelectorAll('text[data-axis="y"]')].map((t) => t.textContent);
    expect(yLabels(charts[0])).toEqual(["0%", "50%", "100%"]);
    // Memory and disk show the GiB scale of the latest capacity.
    expect(yLabels(charts[1])).toEqual(["0", formatBytes(metricAt(NOW, 0).mem_total / 2), formatBytes(metricAt(NOW, 0).mem_total)]);
    expect(yLabels(charts[2])[2]).toBe(formatBytes(metricAt(NOW, 0).disk_total));
    for (const chart of charts) {
      const xLabels = [...chart.querySelectorAll('text[data-axis="x"]')].map((t) => t.textContent ?? "");
      expect(xLabels.length).toBeGreaterThanOrEqual(3);
      for (const label of xLabels) expect(label).toMatch(/^\d{2}:\d{2}$/);
    }
  });

  it("switches the time range by requesting since_ms and shows the matching data", async () => {
    const urls: string[] = [];
    route((url) => {
      urls.push(url);
      const hourly = url.includes("since_ms=");
      const ts = NOW - 60_000;
      return {
        payload: {
          samples: [metricAt(ts, hourly ? 55 : 11)],
          latest: metricAt(ts, hourly ? 55 : 11),
          windowStart: hourly ? NOW - 3600_000 : NOW - 24 * 3600_000,
          windowEnd: NOW,
          sampleIntervalMs: 60_000,
        },
      };
    });
    const container = mountMetrics();
    await flushUntil(() => container.textContent?.includes("11.0%") ?? false);
    expect(urls[0]).toBe("/fleet/devices/d-1/metrics");
    const labels = [...container.querySelectorAll('[aria-label="时间范围"] button')].map((b) => b.textContent);
    expect(labels).toEqual(["1 小时", "6 小时", "24 小时", "48 小时"]);
    expect(container.querySelector('[aria-label="时间范围"] button[aria-pressed="true"]')?.textContent).toBe("24 小时");

    click(buttonByText(container, "1 小时") as HTMLButtonElement);
    await flushUntil(() => container.textContent?.includes("55.0%") ?? false);
    const since = Number(new URL(urls[urls.length - 1], "http://x").searchParams.get("since_ms"));
    expect(Math.abs(Date.now() - 3600_000 - since)).toBeLessThan(60_000);
    expect(container.querySelector('[aria-label="时间范围"] button[aria-pressed="true"]')?.textContent).toBe("1 小时");
  });

  it("shows a hover readout with time and value", async () => {
    const t1 = NOW - 120_000;
    route(() => ({
      payload: {
        samples: [metricAt(t1 - 60_000, 10), metricAt(t1, 42.5)],
        latest: metricAt(t1, 42.5),
        windowStart: NOW - 3600_000,
        windowEnd: NOW,
        sampleIntervalMs: 60_000,
      },
    }));
    const container = mountMetrics();
    await flushUntil(() => container.querySelector("polyline") !== null);
    const svg = container.querySelector('svg[role="img"]') as SVGSVGElement;
    svg.getBoundingClientRect = () => ({ left: 0, top: 0, width: 240, height: 100, right: 240, bottom: 100, x: 0, y: 0, toJSON: () => ({}) });
    expect(svg.querySelector('[data-testid="metric-tooltip"]')).toBeNull();
    const hit = svg.querySelector("rect") as SVGRectElement;
    act(() => {
      hit.dispatchEvent(new MouseEvent("mousemove", { bubbles: true, clientX: 225 }));
    });
    expect(svg.querySelector('[data-testid="metric-tooltip"]')?.textContent).toContain("CPU 42.5%");
  });

  it("reports a valid empty window with an independent latest outside it as zero samples", async () => {
    const latest = metricAt(NOW + 60_000, 66);
    route(() => ({
      payload: {
        samples: [],
        latest,
        windowStart: NOW - 3600_000,
        windowEnd: NOW,
      },
    }));
    const container = mountMetrics();
    await flushUntil(() => container.textContent?.includes(formatTime(latest.ts)) ?? false);

    expect(container.textContent).toContain("66.0%");
    expect(container.textContent).toContain("所选时间范围内暂无数据");
    expect(container.textContent).not.toContain("采样点数");
  });

  it("keeps independent latest visible when the trend fails", async () => {
    route(() => ({
      payload: {
        samples: [],
        latest: metricAt(NOW - 60_000, 66),
        windowStart: NOW - 3600_000,
        windowEnd: NOW,
        seriesError: "context deadline exceeded",
      },
    }));
    const container = mountMetrics("d-1", false);
    await flushUntil(() => container.textContent?.includes("设备离线") || container.textContent?.includes("未能加载趋势") || false);

    expect(container.textContent).toContain("66.0%");
    expect(container.textContent).toContain("未能加载趋势");
    expect(container.textContent).toContain("context deadline exceeded");
    expect(container.textContent).not.toContain("设备离线，暂无近期指标");
  });

  it("does not confuse a trend failure without latest with an empty device", async () => {
    route(() => ({
      payload: {
        samples: [],
        windowStart: NOW - 3600_000,
        windowEnd: NOW,
        seriesError: "database unavailable",
      },
    }));
    const container = mountMetrics("d-1", false);
    await flushUntil(() => container.textContent?.includes("未能加载趋势") ?? false);

    expect(container.textContent).toContain("database unavailable");
    expect(container.textContent).not.toContain("设备离线，暂无近期指标");
  });

  it("uses the requested window for a single sample time axis", async () => {
    route(() => ({
      payload: {
        samples: [metricAt(NOW - 3600_000, 12.5)],
        latest: metricAt(NOW - 3600_000, 12.5),
        windowStart: NOW - 2 * 3600_000,
        windowEnd: NOW,
        sampleCount: 1,
      },
    }));
    const container = mountMetrics();
    await flushUntil(() => container.querySelector("circle") !== null);

    expect(container.querySelector("circle")?.getAttribute("cx")).toBe("136");
  });

  it("breaks the trend line across missing intervals", async () => {
    const start = NOW - 1000_000;
    const samples = [
      metricAt(start, 1),
      metricAt(start + 60_000, 2),
      metricAt(start + 660_000, 3),
      metricAt(start + 720_000, 4),
    ];
    route(() => ({
      payload: {
        samples,
        latest: samples[samples.length - 1],
        windowStart: start,
        windowEnd: samples[samples.length - 1].ts,
        sampleCount: samples.length,
        sampleIntervalMs: 60_000,
      },
    }));
    const container = mountMetrics();
    await flushUntil(() => container.querySelector("polyline") !== null);

    expect(container.querySelectorAll("polyline")).toHaveLength(6);
  });

  it("does not show a previous device sample while switching or resolving out of order", async () => {
    const currentResponse = (ts: number, cpu: number) => {
      const latest = metricAt(ts, cpu);
      return {
        samples: [latest],
        latest,
        windowStart: NOW - 3600_000,
        windowEnd: NOW,
        sampleCount: 1,
      };
    };
    route(() => ({ payload: currentResponse(NOW - 60_000, 11) }));
    const container = mountMetrics();
    await flushUntil(() => container.textContent?.includes("11.0%") ?? false);

    const oldRequest = deferred<MockResponse>();
    mocks.fetch.mockImplementation(() => oldRequest.promise);
    click(container.querySelector('button[aria-label="刷新指标"]') as HTMLButtonElement);
    const nextRequest = deferred<MockResponse>();
    mocks.fetch.mockImplementation(() => nextRequest.promise);
    act(() => {
      mounted?.root.render(createElement(MetricsPanel, { deviceId: "d-2", online: true }));
    });

    expect(container.textContent).not.toContain("11.0%");
    nextRequest.resolve(jsonResponse(currentResponse(NOW - 30_000, 22)));
    await flushUntil(() => container.textContent?.includes("22.0%") ?? false);
    oldRequest.resolve(jsonResponse(currentResponse(NOW - 10_000, 33)));
    await flush();
    expect(container.textContent).toContain("22.0%");
    expect(container.textContent).not.toContain("33.0%");
  });

  it("does not infer latest samples or window metadata from a samples-only response", async () => {
    route(() => ({ payload: { samples: [metricAt(NOW - 60_000, 12.5)] } }));
    const container = mountMetrics();
    await flushUntil(() => container.textContent?.includes("设备尚未上报指标") ?? false);

    expect(container.textContent).not.toContain("12.5%");
    expect(container.textContent).not.toContain("趋势窗口");
    expect(container.textContent).not.toContain("窗口完整性未知");
  });

  it("keeps the offline empty state distinct from a trend error", async () => {
    route(() => ({ payload: { samples: [], windowStart: NOW - 3600_000, windowEnd: NOW } }));
    const container = mountMetrics("d-1", false);
    await flushUntil(() => container.textContent?.includes("设备离线，暂无近期指标") ?? false);

    expect(container.textContent).not.toContain("未能加载趋势");
  });

  it("filters the current list locally by search and all/online/offline/revoked status without new requests", async () => {
    const calls = route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const cardOf = (name: string) => [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes(name));
    const requestCount = calls.length;
    const search = document.querySelector<HTMLInputElement>('input[aria-label="搜索设备"]');
    const filter = document.querySelector<HTMLSelectElement>('select[aria-label="设备状态过滤"]');
    expect(search).not.toBeNull();
    expect(filter).not.toBeNull();

    setInputValue(search as HTMLInputElement, "DB-02");
    expect(cardOf("db-02")).toBeTruthy();
    expect(cardOf("web-01")).toBeFalsy();
    expect(cardOf("browser-01")).toBeFalsy();
    expect(cardOf("old-laptop")).toBeFalsy();

    setInputValue(search as HTMLInputElement, "");
    setSelectValue(filter as HTMLSelectElement, "online");
    expect(cardOf("web-01")).toBeTruthy();
    expect(cardOf("db-02")).toBeFalsy();
    expect(cardOf("browser-01")).toBeFalsy();
    expect(cardOf("old-laptop")).toBeFalsy();

    setSelectValue(filter as HTMLSelectElement, "offline");
    expect(cardOf("db-02")).toBeTruthy();
    expect(cardOf("browser-01")).toBeTruthy();
    expect(cardOf("web-01")).toBeFalsy();
    expect(cardOf("old-laptop")).toBeFalsy();

    setSelectValue(filter as HTMLSelectElement, "revoked");
    expect(cardOf("old-laptop")).toBeTruthy();
    expect(cardOf("web-01")).toBeFalsy();
    expect(cardOf("db-02")).toBeFalsy();
    expect(cardOf("browser-01")).toBeFalsy();

    setSelectValue(filter as HTMLSelectElement, "all");
    expect(cardOf("web-01")).toBeTruthy();
    expect(cardOf("db-02")).toBeTruthy();
    expect(cardOf("browser-01")).toBeTruthy();
    expect(cardOf("old-laptop")).toBeFalsy();
    expect(document.body.textContent).toContain("已隐藏 1 台已撤销接入的设备");
    expect(calls).toHaveLength(requestCount);
  });

  it("windows large lists without narrowing local search or removing owner and card actions", async () => {
    const devices = Array.from({ length: 60 }, (_, index) => ({
      ...AGENT_DEVICE,
      id: `d-${index}`,
      name: index === 59 ? "needle-device" : `device-${index}`,
      owner: { id: `owner-${index}`, username: `owner-${index}` },
      agent: {
        ...AGENT_DEVICE.agent,
        service_state: { ...AGENT_DEVICE.agent.service_state },
      },
    }));
    route(fleetHandler({ devices: { devices } }));
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("device-0") ?? false);
    const cardOf = (name: string) => [...document.querySelectorAll("section.nx-card")].find((card) => card.textContent?.includes(name));
    expect(document.querySelectorAll("section.nx-card").length).toBeLessThan(devices.length);

    const search = document.querySelector<HTMLInputElement>('input[aria-label="搜索设备"]');
    if (!search) throw new Error("Device search input not found");
    act(() => search.focus());
    setInputValue(search, "needle-device");
    await flushUntil(() => cardOf("needle-device") !== undefined);

    const target = cardOf("needle-device");
    expect(target?.textContent).toContain("owner-59");
    expect([...(target?.querySelectorAll("button") ?? [])].map((button) => button.textContent?.trim())).toEqual(
      expect.arrayContaining(["打开工作区", "详情", "指标"]),
    );
    expect(target?.querySelector('button[aria-label="更多操作"]')).not.toBeNull();
    expect(document.activeElement).toBe(search);

    setInputValue(search, "");
    expect(document.activeElement).toBe(search);
    expect(document.querySelectorAll("section.nx-card").length).toBeLessThan(devices.length);
  });

  it.each([40, 41])("keeps direct cards at 40 and windows at 41: %s devices", async (deviceCount) => {
    const devices = Array.from({ length: deviceCount }, (_, index) => ({
      ...AGENT_DEVICE,
      id: `boundary-${index}`,
      name: `boundary-${index}`,
      agent: {
        ...AGENT_DEVICE.agent,
        service_state: { ...AGENT_DEVICE.agent.service_state },
      },
    }));
    route(fleetHandler({ devices: { devices } }));
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("boundary-0") ?? false);

    const scroller = document.querySelector(".nx-pane > div.overflow-auto");
    const firstCard = [...document.querySelectorAll("section.nx-card")].find((card) => card.textContent?.includes("boundary-0"));
    expect(firstCard).not.toBeUndefined();
    if (deviceCount === 40) {
      expect(firstCard?.parentElement).toBe(scroller);
      expect(scroller?.querySelector("[data-index]")).toBeNull();
    } else {
      expect(firstCard?.parentElement).not.toBe(scroller);
      expect(scroller?.querySelector("[data-index]")).not.toBeNull();
      expect(document.querySelectorAll("section.nx-card").length).toBeLessThan(deviceCount);
    }
  });

  it("measures the virtualized list offset when the async list arrives and re-measures on layout change", async () => {
    const devices = Array.from({ length: 41 }, (_, index) => ({
      ...AGENT_DEVICE,
      id: `offset-${index}`,
      name: `offset-${index}`,
      agent: {
        ...AGENT_DEVICE.agent,
        service_state: { ...AGENT_DEVICE.agent.service_state },
      },
    }));
    route(fleetHandler({ devices: { devices } }));
    seedUser(SUPERADMIN);

    // Deterministic geometry: scroller top 100, list top 140 → 40px scroll margin. The list
    // element only mounts once the asynchronous device response arrives, so a mount-only
    // effect never sees it and the margin stays 0.
    let listTop = 140;
    const rectSpy = vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
      this: Element,
    ) {
      const top = this.classList.contains("overflow-auto")
        ? 100
        : this.classList.contains("shrink-0")
          ? listTop
          : 0;
      return {
        top,
        bottom: top + 100,
        left: 0,
        right: 100,
        width: 100,
        height: 100,
        x: 0,
        y: top,
        toJSON: () => ({}),
      } as DOMRect;
    });
    try {
      mounted = mountWithClient(createElement(DevicesView));
      await flushUntil(() => document.body.textContent?.includes("offset-0") ?? false);

      const firstItem = document.querySelector<HTMLElement>('[data-index="0"]');
      expect(firstItem).not.toBeNull();
      expect(firstItem?.style.transform).toBe("translateY(-40px)");

      // Layout change: the list moves down and a resize re-measures the margin.
      act(() => {
        listTop = 180;
        window.dispatchEvent(new Event("resize"));
      });
      await flush();
      expect(firstItem?.style.transform).toBe("translateY(-80px)");
    } finally {
      rectSpy.mockRestore();
    }
  });

  it("re-measures the virtual offset when the enrollment panel opens and closes without a resize", async () => {
    const devices = Array.from({ length: 41 }, (_, index) => ({
      ...AGENT_DEVICE,
      id: `enroll-${index}`,
      name: `enroll-${index}`,
      agent: {
        ...AGENT_DEVICE.agent,
        service_state: { ...AGENT_DEVICE.agent.service_state },
      },
    }));
    route(fleetHandler({ devices: { devices } }));
    seedUser(SUPERADMIN);

    // Deterministic geometry: scroller top 100; the list sits at top 140, or 260 once the
    // enrollment panel renders above it. No window resize is ever dispatched — the panel
    // toggle alone must re-measure the offset.
    let enrollPanelShown = false;
    const rectSpy = vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
      this: Element,
    ) {
      const top = this.classList.contains("overflow-auto")
        ? 100
        : this.classList.contains("shrink-0")
          ? enrollPanelShown
            ? 260
            : 140
          : 0;
      return {
        top,
        bottom: top + 100,
        left: 0,
        right: 100,
        width: 100,
        height: 100,
        x: 0,
        y: top,
        toJSON: () => ({}),
      } as DOMRect;
    });
    try {
      mounted = mountWithClient(createElement(DevicesView));
      await flushUntil(() => document.body.textContent?.includes("enroll-0") ?? false);

      const firstItem = document.querySelector<HTMLElement>('[data-index="0"]');
      expect(firstItem).not.toBeNull();
      expect(firstItem?.style.transform).toBe("translateY(-40px)");

      const enrollButton = [...document.querySelectorAll("button")].find(
        (button) =>
          button.getAttribute("aria-expanded") !== null &&
          button.textContent?.includes("接入新设备"),
      );
      if (!enrollButton) throw new Error("Enrollment toggle not found");

      enrollPanelShown = true;
      click(enrollButton);
      expect(document.body.textContent).toContain("生成接入码");
      expect(firstItem?.style.transform).toBe("translateY(-160px)");

      enrollPanelShown = false;
      click(enrollButton);
      await flush();
      expect(document.body.textContent).not.toContain("生成接入码");
      expect(firstItem?.style.transform).toBe("translateY(-40px)");
    } finally {
      rectSpy.mockRestore();
    }
  });

  it("re-measures the virtual offset when the issued panel's native details toggles without a render or resize", async () => {
    const devices = Array.from({ length: 41 }, (_, index) => ({
      ...AGENT_DEVICE,
      id: `details-${index}`,
      name: `details-${index}`,
      agent: {
        ...AGENT_DEVICE.agent,
        service_state: { ...AGENT_DEVICE.agent.service_state },
      },
    }));
    route(fleetHandler({ devices: { devices } }));
    seedUser(SUPERADMIN);

    // Deterministic geometry: scroller top 100; the list sits at 140, +120 once the
    // enrollment panel opens, +80 once the issued-code view replaces the form, and +160
    // once the native advanced details expands. No window resize is ever dispatched.
    let enrollPanelShown = false;
    let issuedShown = false;
    let detailsOpen = false;
    const rectSpy = vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
      this: Element,
    ) {
      const top = this.classList.contains("overflow-auto")
        ? 100
        : this.classList.contains("shrink-0")
          ? 140 + (enrollPanelShown ? 120 : 0) + (issuedShown ? 80 : 0) + (detailsOpen ? 160 : 0)
          : 0;
      return {
        top,
        bottom: top + 100,
        left: 0,
        right: 100,
        width: 100,
        height: 100,
        x: 0,
        y: top,
        toJSON: () => ({}),
      } as DOMRect;
    });
    try {
      mounted = mountWithClient(createElement(DevicesView));
      await flushUntil(() => document.body.textContent?.includes("details-0") ?? false);

      const firstItem = document.querySelector<HTMLElement>('[data-index="0"]');
      expect(firstItem).not.toBeNull();
      expect(firstItem?.style.transform).toBe("translateY(-40px)");

      const enrollButton = [...document.querySelectorAll("button")].find(
        (button) =>
          button.getAttribute("aria-expanded") !== null &&
          button.textContent?.includes("接入新设备"),
      );
      if (!enrollButton) throw new Error("Enrollment toggle not found");
      enrollPanelShown = true;
      click(enrollButton);
      expect(firstItem?.style.transform).toBe("translateY(-160px)");

      const issueButton = [...document.querySelectorAll("button")].find(
        (button) => button.textContent?.trim() === "生成接入码",
      );
      if (!issueButton) throw new Error("Issue-code button not found");
      issuedShown = true;
      click(issueButton);
      await flushUntil(() => document.body.textContent?.includes("fleet-code-1") ?? false);
      expect(firstItem?.style.transform).toBe("translateY(-240px)");

      const details = document.querySelector("details");
      const summary = details?.querySelector("summary");
      if (!details || !summary) throw new Error("Advanced details not found");

      // Expanding the native details fires no React render and no resize; only the
      // toggle-driven measurement can pick up the new offset.
      detailsOpen = true;
      click(summary);
      await flushUntil(() => firstItem?.style.transform !== "translateY(-240px)");
      expect(details.open).toBe(true);
      expect(firstItem?.style.transform).toBe("translateY(-400px)");

      detailsOpen = false;
      click(summary);
      await flushUntil(() => firstItem?.style.transform !== "translateY(-400px)");
      expect(details.open).toBe(false);
      expect(firstItem?.style.transform).toBe("translateY(-240px)");
    } finally {
      rectSpy.mockRestore();
    }
  });

  it("shows offline badges and offline autostart hints for offline devices", async () => {
    route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("db-02") ?? false);

    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("db-02"));
    expect(card?.textContent).toContain("离线");
    expect(buttonByText(card, "打开工作区")).toBeUndefined();
    expect(buttonByText(card, "离线原因")).toBeDefined();
    expandDetails(card);
    expect(card?.textContent).toContain("设备离线：设置已保存，设备上线后生效");
    expect(card?.textContent).toContain("未运行");
  });

  it("reports missing runtime status for non-agent devices and provides no actions for revoked devices", async () => {
    route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("browser-01") ?? false);

    const browserCard = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("browser-01"));
    expect(browserCard?.textContent).toContain("无运行状态和指标");
    expect([...(browserCard?.querySelectorAll("button") ?? [])].some((b) => b.textContent?.trim() === "指标")).toBe(false);

    expect([...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("old-laptop"))).toBeUndefined();
    const filter = document.querySelector<HTMLSelectElement>('select[aria-label="设备状态过滤"]') as HTMLSelectElement;
    setSelectValue(filter, "revoked");
    const revokedCard = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("old-laptop"));
    expect(revokedCard?.textContent).toContain("已撤销接入");
    expect(revokedCard?.querySelectorAll("button")).toHaveLength(0);
  });

  it("shows enrollment guidance for an empty list and an error with retry for a failed list request", async () => {
    route(fleetHandler({ devices: { devices: [] } }));
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("还没有接入设备") ?? false);
    mounted.unmount();

    route(() => ({ status: 500, payload: { error: { code: "internal", message: "数据库错误" } } }));
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("未能加载设备列表") ?? false);
    expect(document.body.textContent).toContain("数据库错误");
  });
});

describe("Device management view - enrollment code issuance", () => {
  it("shows expiration and single-use guidance with the real agent enrollment commands after issuance", async () => {
    const calls = route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const openButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("接入新设备"));
    click(openButton as HTMLButtonElement);
    const issueButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "生成接入码");
    click(issueButton as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("fleet-code-1") ?? false);

    const enrollCall = calls.find((c) => c.url === "/device/enroll-codes");
    expect(enrollCall?.method).toBe("POST");
    expect(enrollCall?.body).toEqual({ ttl_ms: 900_000 });

    const text = document.body.textContent ?? "";
    expect(text).toContain("只能使用一次");
    expect(text).toContain("完成后不再显示");
    expect(text).toContain("设备凭证只保存在设备上，服务器只保存校验信息");
    expect(text).toContain("高级说明：多地址、手动接入与服务管理");
    expect(text).toContain("执行完成后刷新列表");
    expect(text).toContain("随用户登录自动启动");
    expect(text).toContain("设备会出现在这里");
    expect(text).not.toContain("第 1 步");
    const dataDir = '"${NEXTERM_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/NexTerm}"';
    expect(text).toContain(
      `nexterm-agent enroll --server 'https://nexterm.example.com' --code 'fleet-code-1' --data-dir ${dataDir}`,
    );
    expect(text).toContain(
      `nexterm-agent enroll --server 'http://10.0.0.8:8080' --code 'fleet-code-1' --insecure --data-dir ${dataDir}`,
    );
    expect(text).toContain(`nexterm-agent install --data-dir ${dataDir}`);
    expect(text).toContain("不会执行安装");
    expect(text).not.toContain("/var/lib");

    // enroll and install use the same data directory; the directory is a double-quoted single argument, expansion priority
    // NEXTERM_DATA_DIR > XDG_DATA_HOME > $HOME (matches the override promise in the copy).
    const commandElements = [...document.querySelectorAll("code")];
    const commands = commandElements.map((c) => c.textContent ?? "");
    const details = document.querySelector<HTMLDetailsElement>("details");
    expect(details).not.toBeNull();
    expect(details?.open).toBe(false);
    const enrollCommandElements = commandElements.filter((c) => c.textContent?.includes("agent enroll"));
    expect(enrollCommandElements).toHaveLength(2);
    expect(enrollCommandElements[0].closest("details")).toBeNull();
    expect(enrollCommandElements[1].closest("details")).toBe(details);
    const enrollDirs = enrollCommandElements.map((c) => (c.textContent ?? "").split("--data-dir ")[1]);
    const installDirs = commands
      .filter((c) => c.includes("agent install"))
      .map((c) => c.split("--data-dir ")[1]);
    expect(enrollDirs.length).toBeGreaterThan(0);
    expect(installDirs).toEqual([dataDir]);
    for (const dir of enrollDirs) {
      expect(dir).toBe(dataDir);
      expect(dir?.startsWith('"')).toBe(true);
      expect(dir?.endsWith('"')).toBe(true);
      expect(dir).not.toContain(" ");
      const envOrder = [dir?.indexOf("NEXTERM_DATA_DIR"), dir?.indexOf("XDG_DATA_HOME"), dir?.indexOf("$HOME")];
      expect(envOrder[0]).toBeGreaterThanOrEqual(0);
      expect(envOrder[0]).toBeLessThan(envOrder[1] ?? Infinity);
      expect(envOrder[1]).toBeLessThan(envOrder[2] ?? Infinity);
    }

    const doneButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "完成");
    click(doneButton as HTMLButtonElement);
    await waitForRendered(() => {
      expect(document.body.textContent).not.toContain("fleet-code-1");
    });
  });

  it("clears the enrollment code after Done across refresh, panel reopen, and view remount", async () => {
    const calls = route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const enrollToggle = () =>
      [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("接入新设备")) as HTMLButtonElement;
    click(enrollToggle());
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "生成接入码") as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("fleet-code-1") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "完成") as HTMLButtonElement);
    await waitForRendered(() => {
      expect(document.body.textContent).not.toContain("fleet-code-1");
    });

    const deviceRequests = calls.filter((c) => c.url === "/fleet/devices" && c.method === "GET").length;
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "刷新") as HTMLButtonElement);
    await flush();
    expect(calls.filter((c) => c.url === "/fleet/devices" && c.method === "GET").length).toBeGreaterThan(deviceRequests);
    expect(document.body.textContent).not.toContain("fleet-code-1");

    click(enrollToggle());
    expect(enrollToggle().getAttribute("aria-expanded")).toBe("false");
    click(enrollToggle());
    expect(enrollToggle().getAttribute("aria-expanded")).toBe("true");
    expect([...document.querySelectorAll("button")].some((b) => b.textContent?.trim() === "生成接入码")).toBe(true);
    expect(document.body.textContent).not.toContain("fleet-code-1");
    await flush();

    mounted.unmount();
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);
    click(enrollToggle());
    expect(enrollToggle().getAttribute("aria-expanded")).toBe("true");
    expect([...document.querySelectorAll("button")].some((b) => b.textContent?.trim() === "生成接入码")).toBe(true);
    expect(document.body.textContent).not.toContain("fleet-code-1");
    await flush();
  });

  it("reports clear errors when copying the enrollment code or command while the clipboard is unavailable", async () => {
    const toast = vi.fn();
    useUi.setState({ pushToast: toast });
    route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("接入新设备")) as HTMLButtonElement);
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "生成接入码") as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("fleet-code-1") ?? false);

    vi.stubGlobal("navigator", { clipboard: undefined });
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "复制接入码") as HTMLButtonElement);
    const command = [...document.querySelectorAll("code")].find((c) => c.textContent?.includes("agent enroll"));
    const copyCommand = command?.parentElement?.querySelector("button");
    expect(copyCommand).toBeTruthy();
    click(copyCommand as HTMLButtonElement);
    await flush();

    expect(toast).toHaveBeenCalledTimes(2);
    expect(toast).toHaveBeenNthCalledWith(1, "error", "复制失败，请手动复制");
    expect(toast).toHaveBeenNthCalledWith(2, "error", "复制失败，请手动复制");
  });

  it("gates issuance and overwrite saving on base URL loading, error, and ready states and allows retry after failure", async () => {
    const firstBaseUrls = deferred<{ ok: boolean; status: number; text: () => Promise<string> }>();
    let baseUrlCalls = 0;
    let enrollCalls = 0;
    mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
      const u = String(url);
      const method = init.method ?? "GET";
      if (u === "/fleet/devices" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ devices: [AGENT_DEVICE] }) };
      }
      if (u === "/fleet/base-urls" && method === "GET") {
        baseUrlCalls += 1;
        if (baseUrlCalls === 1) return firstBaseUrls.promise;
        return { ok: true, status: 200, text: async () => JSON.stringify({ base_urls: BASE_URLS }) };
      }
      if (u === "/healthz" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ ok: true, version: "0.2.2" }) };
      }
      if (u === "/device/enroll-codes" && method === "POST") {
        enrollCalls += 1;
        return { ok: true, status: 200, text: async () => JSON.stringify({ code: "ready-code", expires_at: NOW + 900_000 }) };
      }
      return { ok: false, status: 404, text: async () => JSON.stringify({ error: { code: "not_found", message: u } }) };
    });
    vi.stubGlobal("fetch", mocks.fetch);
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("接入新设备")) as HTMLButtonElement);
    const issueButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "生成接入码") as HTMLButtonElement;
    expect(issueButton.disabled).toBe(true);
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "展开编辑") as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("正在读取接入地址") ?? false);
    expect(document.querySelector('input[aria-label="新接入地址"]')).toBeNull();
    expect([...document.querySelectorAll("button")].some((b) => b.textContent?.includes("保存修改"))).toBe(false);

    await act(async () => {
      firstBaseUrls.reject(new Error("地址服务不可用"));
      await firstBaseUrls.promise.catch(() => {});
    });
    await flushUntil(() => document.body.textContent?.includes("无法读取接入地址") ?? false);
    expect(document.body.textContent).toContain("地址服务不可用");
    expect(issueButton.disabled).toBe(true);
    expect(document.querySelector('input[aria-label="新接入地址"]')).toBeNull();

    const baseCard = document.querySelector("#fleet-base-urls");
    const retry = [...(baseCard?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === "重试");
    expect(retry).toBeTruthy();
    click(retry as HTMLButtonElement);
    await flushUntil(() => baseUrlCalls === 2 && document.querySelector('input[aria-label="新接入地址"]') !== null);
    expect(issueButton.disabled).toBe(false);
    const save = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("保存修改"));
    expect(save).toBeTruthy();

    click(issueButton);
    await flushUntil(() => enrollCalls === 1);
  });
});

describe("Device management view - revocation and autostart", () => {
  it("requires confirmation to revoke and sends POST before refreshing the list", async () => {
    const calls = route(fleetHandler());
    mocks.ask.mockResolvedValue(true);
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("web-01"));
    pickMenuItem(card, "撤销接入");
    await flushUntil(() => calls.some((c) => c.url === "/fleet/devices/d-1/revoke"));

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("无法恢复");
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("重新生成接入码");
    expect(calls.filter((c) => c.url === "/fleet/devices" && c.method === "GET").length).toBeGreaterThanOrEqual(2);
  });

  it("sends no request when revoke confirmation is canceled", async () => {
    const calls = route(fleetHandler());
    mocks.ask.mockResolvedValue(false);
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("web-01"));
    pickMenuItem(card, "撤销接入");
    await flushUntil(() => mocks.ask.mock.calls.length === 1);

    expect(calls.some((c) => c.url.includes("/revoke"))).toBe(false);
  });

  it("applies autostart changes to online devices directly without confirmation", async () => {
    const calls = route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("web-01"));
    expandDetails(card);
    const checkbox = card?.querySelector<HTMLInputElement>('input[aria-label="随登录自动启动"]');
    expect(checkbox?.checked).toBe(true);
    click(checkbox as HTMLInputElement);
    await flushUntil(() => calls.some((c) => c.url === "/fleet/devices/d-1/autostart"));

    expect(mocks.ask).not.toHaveBeenCalled();
    const autostartCall = calls.find((c) => c.url === "/fleet/devices/d-1/autostart");
    expect(autostartCall?.body).toEqual({ desired: false });
  });

  it("offers no autostart for a desktop exposing its local terminals", async () => {
    const desk = {
      ...AGENT_DEVICE, id: "d-desk", name: "desk-01", kind: "local-helper",
      agent: { ...AGENT_DEVICE.agent, desired_autostart: false, service_state: { installed: false, enabled: false, active: false, last_reconcile_at: NOW - 60_000 } },
    };
    route(fleetHandler({ devices: { devices: [desk] } }));
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("desk-01") ?? false);

    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("desk-01"));
    expandDetails(card);
    expect(card?.textContent).toContain("后台服务");
    expect(card?.querySelector('input[aria-label="随登录自动启动"]')).toBeNull();
  });

  it("confirms that offline autostart changes only save desired state", async () => {
    const calls = route(fleetHandler());
    mocks.ask.mockResolvedValue(true);
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("db-02") ?? false);

    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("db-02"));
    expandDetails(card);
    const checkbox = card?.querySelector<HTMLInputElement>('input[aria-label="随登录自动启动"]');
    expect(checkbox?.checked).toBe(false);
    click(checkbox as HTMLInputElement);
    await flushUntil(() => calls.some((c) => c.url === "/fleet/devices/d-2/autostart"));

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("离线");
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("设置已保存");
    const autostartCall = calls.find((c) => c.url === "/fleet/devices/d-2/autostart");
    expect(autostartCall?.body).toEqual({ desired: true });
  });
});

describe("Device management view - remote terminal switch and version note", () => {
  function cardWith(name: string) {
    return [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes(name));
  }

  it("states the remote terminal state on the card in plain Chinese", async () => {
    route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    expect(cardWith("web-01")?.textContent).toContain("远程终端已开启");
    expect(cardWith("db-02")?.textContent).toContain("远程终端已关闭");
  });

  it("turns the remote terminal on with one click, even for an offline device after its info dialog", async () => {
    const calls = route(fleetHandler());
    mocks.ask.mockResolvedValue(true);
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("db-02") ?? false);

    const card = cardWith("db-02");
    expandDetails(card);
    const checkbox = card?.querySelector<HTMLInputElement>('input[aria-label="远程终端"]');
    expect(checkbox?.checked).toBe(false);
    click(checkbox as HTMLInputElement);
    await flushUntil(() => calls.some((c) => c.url === "/fleet/devices/d-2/terminal-access"));

    expect(calls.find((c) => c.url === "/fleet/devices/d-2/terminal-access")?.body).toEqual({ enabled: true });
    await flushUntil(() => cardWith("db-02")?.textContent?.includes("远程终端已开启") ?? false);
  });

  it("enables an online device without any confirmation", async () => {
    const calls = route(
      fleetHandler({ devices: { devices: [{ ...AGENT_DEVICE, agent: { ...AGENT_DEVICE.agent, terminal_enabled: false } }] } }),
    );
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const card = cardWith("web-01");
    expandDetails(card);
    click(card?.querySelector<HTMLInputElement>('input[aria-label="远程终端"]') as HTMLInputElement);
    await flushUntil(() => calls.some((c) => c.url === "/fleet/devices/d-1/terminal-access"));

    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("asks before turning it off, explains existing connections stay open, and sends nothing when cancelled", async () => {
    const calls = route(fleetHandler());
    mocks.ask.mockResolvedValue(false);
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const card = cardWith("web-01");
    expandDetails(card);
    const checkbox = card?.querySelector<HTMLInputElement>('input[aria-label="远程终端"]') as HTMLInputElement;
    click(checkbox);
    await flushUntil(() => mocks.ask.mock.calls.length === 1);
    const message = String(mocks.ask.mock.calls[0]?.[0]);
    expect(message).toContain("拒绝新的远程终端");
    expect(message).toContain("不会被强制断开");
    await flush();
    expect(calls.some((c) => c.url.endsWith("/terminal-access"))).toBe(false);

    mocks.ask.mockResolvedValue(true);
    click(checkbox);
    await flushUntil(() => calls.some((c) => c.url === "/fleet/devices/d-1/terminal-access"));
    expect(calls.find((c) => c.url === "/fleet/devices/d-1/terminal-access")?.body).toEqual({ enabled: false });
    await flushUntil(() => cardWith("web-01")?.textContent?.includes("远程终端已关闭") ?? false);
  });

  it("hints an outdated agent only when it is older than the server, both are release versions and the device is online", async () => {
    route(
      fleetHandler({
        devices: {
          server_version: "0.3.0",
          devices: [
            { ...AGENT_DEVICE, id: "d-old", name: "old-01", agent: { ...AGENT_DEVICE.agent, app_version: "0.2.2" } },
            { ...AGENT_DEVICE, id: "d-new", name: "new-01", agent: { ...AGENT_DEVICE.agent, app_version: "0.10.0" } },
            { ...AGENT_DEVICE, id: "d-same", name: "same-01", agent: { ...AGENT_DEVICE.agent, app_version: "v0.3.0" } },
            { ...AGENT_DEVICE, id: "d-dev", name: "dev-01", agent: { ...AGENT_DEVICE.agent, app_version: "dev" } },
            { ...AGENT_DEVICE, id: "d-zero", name: "zero-01", agent: { ...AGENT_DEVICE.agent, app_version: "0.0.0" } },
            OFFLINE_DEVICE,
          ],
        },
      }),
    );
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("old-01") ?? false);

    expect(cardWith("old-01")?.textContent).toContain("设备代理版本过旧（设备 0.2.2，服务端 0.3.0）");
    expect(cardWith("old-01")?.textContent).toContain("nexterm-agent update");
    for (const name of ["new-01", "same-01", "dev-01", "zero-01", "db-02"]) {
      expect(cardWith(name)?.textContent).not.toContain("服务端 0.3.0");
    }
  });

  it("shows no version note when the server does not report its version", async () => {
    route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    expect(cardWith("web-01")?.textContent).not.toContain("服务端");
  });
});

describe("Device management view - autostart drift from installed/enabled", () => {
  function agentWith(serviceState: { installed: boolean; enabled: boolean; active: boolean }, desired: boolean) {
    return {
      devices: [
        {
          ...AGENT_DEVICE,
          id: "d-drift",
          name: "drift-01",
          agent: { ...AGENT_DEVICE.agent, desired_autostart: desired, service_state: { ...serviceState, last_reconcile_at: NOW - 60_000 } },
        },
      ],
    };
  }

  it("reports no drift when enabled but not running and lists not running separately", async () => {
    route(fleetHandler({ devices: agentWith({ installed: true, enabled: true, active: false }, true) }));
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("drift-01") ?? false);

    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("drift-01"));
    expect(card?.textContent).not.toContain("没有生效");
    expect(card?.querySelector('[role="status"]')).toBeNull();
    expandDetails(card);
    expect(card?.textContent).toContain("当前已生效");
    expect(card?.textContent).toContain("后台服务");
    expect(card?.textContent).toContain("未运行");
  });

  it("reports ineffective drift when disabled but the process is running", async () => {
    route(fleetHandler({ devices: agentWith({ installed: true, enabled: false, active: true }, true) }));
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("drift-01") ?? false);

    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("drift-01"));
    expect(card?.querySelector('[role="status"]')?.textContent).toContain("已设置随用户登录自动启动，但当前未生效");
    expandDetails(card);
    expect(card?.textContent).toContain("当前未生效");
    expect(card?.textContent).toContain("运行中");
  });
});

describe("Device management view - card layout", () => {
  it("offers the workspace as the primary action when online and explains the offline reason otherwise", async () => {
    route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("db-02") ?? false);
    const cardOf = (name: string) => [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes(name));

    expect(buttonByText(cardOf("web-01"), "打开工作区")).toBeDefined();
    expect(buttonByText(cardOf("web-01"), "离线原因")).toBeUndefined();

    const offline = cardOf("db-02");
    expect(offline?.textContent).not.toContain("常见原因");
    click(buttonByText(offline, "离线原因") as HTMLButtonElement);
    expect(offline?.textContent).toContain("常见原因");
    expect(offline?.textContent).toContain("接入新设备");
  });

  it("hides internal jargon and shows relative time with the absolute time on hover", async () => {
    route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);
    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("web-01"));

    expect(card?.textContent).not.toContain("吊销");
    expect(card?.textContent).not.toContain("期望自启动");
    expect(card?.querySelector("time")?.getAttribute("title")).toBe(formatTime(AGENT_DEVICE.agent.last_seen_at));
    expect(card?.textContent).toContain("Linux");
  });

  it("shows the terminal switch meaning and a copyable address in the details, without a drift warning when consistent", async () => {
    route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);
    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("web-01"));

    expect(card?.querySelector('[role="status"]')).toBeNull();
    expandDetails(card);
    expect(card?.textContent).toContain("允许新建终端、文件和端口转发连接");
    expect(card?.querySelector('button[aria-label="复制接入地址"]')).not.toBeNull();
    expect(card?.querySelector("code")?.textContent).toBe("https://nexterm.example.com");
  });

  it("warns when autostart is switched off but the service still starts on login", async () => {
    route(
      fleetHandler({
        devices: {
          devices: [
            {
              ...AGENT_DEVICE,
              id: "d-rev",
              name: "rev-drift",
              agent: { ...AGENT_DEVICE.agent, desired_autostart: false, service_state: { installed: true, enabled: true, active: true, last_reconcile_at: NOW - 60_000 } },
            },
          ],
        },
      }),
    );
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("rev-drift") ?? false);
    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("rev-drift"));
    expect(card?.querySelector('[role="status"]')?.textContent).toContain("已设置不自动启动");
  });
});

describe("Device management view - enrollment command shell quoting", () => {
  it("single-quotes semicolons and command substitutions in URL paths so the shell cannot interpret them", async () => {
    route(fleetHandler({ baseUrls: { base_urls: [{ url: "https://nexterm.example.com/mirror;v2/$(id)" }] } }));
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const openButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("接入新设备"));
    click(openButton as HTMLButtonElement);
    const issueButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "生成接入码");
    click(issueButton as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("fleet-code-1") ?? false);

    expect(document.body.textContent).toContain(
      "--server 'https://nexterm.example.com/mirror;v2/$(id)' --code 'fleet-code-1'",
    );
  });
});

describe("Device management view - account switching isolation", () => {
  it("clears old devices and the one-time enrollment code immediately when user.id changes and ignores the old account's late response", async () => {
    const lateAdminDevices = deferred<{ ok: boolean; status: number; text: () => Promise<string> }>();
    const userDevices = {
      devices: [{ ...AGENT_DEVICE, id: "d-9", name: "user-device", owner: undefined }],
    };
    let switched = false;
    mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
      const u = String(url);
      const method = init.method ?? "GET";
      if (u === "/fleet/devices" && method === "GET") {
        if (!switched) return lateAdminDevices.promise;
        return { ok: true, status: 200, text: async () => JSON.stringify(userDevices) };
      }
      if (u === "/fleet/base-urls" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ base_urls: BASE_URLS }) };
      }
      if (u === "/device/enroll-codes" && method === "POST") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ code: "old-admin-code", expires_at: NOW + 900_000 }) };
      }
      return { ok: false, status: 404, text: async () => JSON.stringify({ error: { code: "not_found", message: u } }) };
    });
    vi.stubGlobal("fetch", mocks.fetch);
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));

    await flushUntil(() => document.body.textContent?.includes("2 个地址") ?? false);
    const openButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("接入新设备"));
    click(openButton as HTMLButtonElement);
    const issueButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "生成接入码");
    click(issueButton as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("old-admin-code") ?? false);

    // Simulate the AuthGate scenario: the super-admin session expires and a regular user logs in, the pane is not remounted.
    switched = true;
    act(() => {
      useAuth.setState({ user: PLAIN_USER });
    });
    await flushUntil(() => document.body.textContent?.includes("user-device") ?? false);

    expect(document.body.textContent).not.toContain("old-admin-code");
    expect(document.body.textContent).not.toContain("web-01");

    // The old account's late response arrives only now: it must not overwrite the new account's list.
    await act(async () => {
      lateAdminDevices.resolve({
        ok: true,
        status: 200,
        text: async () => JSON.stringify({ devices: [AGENT_DEVICE] }),
      });
      await lateAdminDevices.promise;
    });
    await flush();
    await flush();
    expect(document.body.textContent).not.toContain("web-01");
    expect(document.body.textContent).toContain("user-device");
  });
});

describe("Device management view - account isolation during issuance", () => {
  it("writes neither the old account's issuance response nor its finally state after switching accounts, so the old code never appears", async () => {
    const lateIssue = deferred<{ ok: boolean; status: number; text: () => Promise<string> }>();
    let issueCalls = 0;
    mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
      const u = String(url);
      const method = init.method ?? "GET";
      if (u === "/fleet/devices" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ devices: [AGENT_DEVICE] }) };
      }
      if (u === "/fleet/base-urls" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ base_urls: BASE_URLS }) };
      }
      if (u === "/device/enroll-codes" && method === "POST") {
        issueCalls += 1;
        if (issueCalls === 1) return lateIssue.promise;
        return {
          ok: true,
          status: 200,
          text: async () => JSON.stringify({ code: "new-user-code", expires_at: NOW + 900_000 }),
        };
      }
      return { ok: false, status: 404, text: async () => JSON.stringify({ error: { code: "not_found", message: u } }) };
    });
    vi.stubGlobal("fetch", mocks.fetch);
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("接入新设备")) as HTMLButtonElement);
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "生成接入码") as HTMLButtonElement);
    await flushUntil(() => issueCalls === 1);

    // Switching to a regular user while issuance is in flight: the render-time reset has already cleared the enrollment panel and issuing.
    act(() => {
      useAuth.setState({ user: PLAIN_USER });
    });
    await flush();

    // The old account's issuance response arrives only now: it must not be written into the new account's view.
    await act(async () => {
      lateIssue.resolve({
        ok: true,
        status: 200,
        text: async () => JSON.stringify({ code: "old-admin-code", expires_at: NOW + 900_000 }),
      });
      await lateIssue.promise;
    });
    await flush();
    await flush();
    expect(document.body.textContent).not.toContain("old-admin-code");
    expect(document.body.textContent).not.toContain("正在生成");

    // The new account can reopen the enrollment panel and issue normally; issuing is not stuck by the old finally.
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("接入新设备")) as HTMLButtonElement);
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "生成接入码") as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("new-user-code") ?? false);
    expect(document.body.textContent).not.toContain("old-admin-code");
  });
});

describe("Device management view - real-time online/offline events", () => {
  it("flips online state in real time with device://status without refetching and ignores unknown device IDs", async () => {
    const calls = route(fleetHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const cardOf = (name: string) =>
      [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes(name));
    expect(cardOf("db-02")?.textContent).toContain("离线");
    expect(cardOf("web-01")?.textContent).toContain("在线");

    const handler = mocks.deviceStatusHandlers.at(-1);
    if (!handler) throw new Error("device://status 未订阅");
    const fetchesBefore = calls.length;

    // Unknown device ID (someone else's device): ignored, no card is polluted.
    act(() => handler({ deviceId: "d-x", online: true }));
    await flush();
    expect(cardOf("db-02")?.textContent).toContain("离线");

    // db-02 comes online: the status flips immediately, the offline action disappears, no list request is fired.
    act(() => handler({ deviceId: "d-2", online: true }));
    await flush();
    expect(cardOf("db-02")?.textContent).toContain("在线");
    expect(buttonByText(cardOf("db-02"), "打开工作区")).toBeUndefined();
    expect(buttonByText(cardOf("db-02"), "离线原因")).toBeUndefined();
    expect(calls.length).toBe(fetchesBefore);

    // web-01 goes offline: the status turns offline, the workspace button is replaced by the offline-reason action, and again no list request is fired.
    act(() => handler({ deviceId: "d-1", online: false }));
    await flush();
    const webCard = cardOf("web-01");
    expect(webCard?.textContent).toContain("离线");
    expect(buttonByText(webCard, "打开工作区")).toBeUndefined();
    expect(buttonByText(webCard, "离线原因")).toBeDefined();
    expect(calls.length).toBe(fetchesBefore);
  });
});
