/** @vitest-environment jsdom */
// Host detail page: one host's sessions, containers, databases and metrics; missing, offline and no-permission states.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, clickButton, flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    fetch: vi.fn(),
    assetList: vi.fn(),
    dockerPs: vi.fn(),
    dbSchemas: vi.fn(),
    healthProbe: vi.fn(),
    connectAsset: vi.fn(),
    connectDevice: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: { ...actual.assetApi, list: mocks.assetList },
    dockerApi: { ...actual.dockerApi, ps: mocks.dockerPs },
    dbApi: { ...actual.dbApi, schemas: mocks.dbSchemas },
  };
});

vi.mock("../../ipc/execBatchApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/execBatchApi")>();
  return { ...actual, execBatchApi: { ...actual.execBatchApi, healthProbe: mocks.healthProbe, healthCancel: vi.fn(async () => undefined) } };
});

vi.mock("../../app/store", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../app/store")>();
  return { ...actual, connectAsset: mocks.connectAsset, connectDevice: mocks.connectDevice };
});

import { HostDetailView } from "../../features/hosts/HostDetailView";
import { deviceIdOfHost, hostAddress, hostDetailLabel, relatedDatabases } from "../../features/hosts/hostDetail";
import { useAuth } from "../../features/auth/store";
import { useUi, type AppTab, type Workspace } from "../../app/store";
import type { Asset, SessionInfo } from "../../ipc/commands";

const NOW = Date.now();

function asset(extra: Partial<Asset> & { id: string; name: string }): Asset {
  return {
    groupId: null,
    kind: "ssh",
    host: "10.0.0.8",
    port: 22,
    username: "root",
    authKind: "password",
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: 1,
    updatedAt: 1,
    deletedAt: null,
    builtin: false,
    ...extra,
  };
}

const WEB = asset({ id: "a-web", name: "web-01" });
const LOCAL = asset({ id: "a-local", name: "本机", kind: "local", host: null, port: null, username: null, builtin: true });
const TUNNELLED_DB = asset({ id: "db-1", name: "orders", kind: "postgres", host: "127.0.0.1", port: 5432, options: { viaAssetId: "a-web" } });
const SAME_HOST_DB = asset({ id: "db-2", name: "cache", kind: "redis", host: "10.0.0.8", port: 6379 });
const LOCAL_DB = asset({ id: "db-3", name: "dev-mysql", kind: "mysql", host: "localhost", port: 3306 });
const OTHER_DB = asset({ id: "db-4", name: "elsewhere", kind: "mysql", host: "10.0.0.9", port: 3306 });

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

const AGENT = {
  platform: "linux",
  app_version: "1.3.0",
  desired_autostart: true,
  terminal_enabled: true,
  current_url: "",
  service_state: { installed: true, enabled: true, active: true, last_reconcile_at: NOW },
  last_seen_at: NOW - 30_000,
};
const ONLINE_DEVICE = { id: "d-1", name: "nas-01", kind: "agent", created_at: 1, last_seen_at: NOW - 30_000, revoked_at: 0, agent: AGENT };
const OFFLINE_DEVICE = {
  ...ONLINE_DEVICE,
  id: "d-2",
  name: "lab-02",
  last_seen_at: NOW - 3600_000,
  agent: { ...AGENT, last_seen_at: NOW - 3600_000 },
};

let mounted: MountedView | undefined;
let fetched: string[] = [];

function routeFleet(devices: unknown[]): void {
  fetched = [];
  mocks.fetch.mockImplementation(async (url: string) => {
    const u = String(url);
    fetched.push(u);
    const json = (payload: unknown, status = 200) => ({ ok: status < 400, status, text: async () => JSON.stringify(payload) });
    if (u === "/fleet/devices") return json({ devices });
    if (u.startsWith("/fleet/devices/") && u.endsWith("/metrics")) return json({ samples: [] });
    return json({ error: { code: "not_found", message: u } }, 404);
  });
  vi.stubGlobal("fetch", mocks.fetch);
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

function hostWorkspace(tabs: AppTab[], extra: Partial<Workspace> = {}): Workspace {
  return {
    id: "ws-web",
    kind: "session",
    title: "web-01",
    sessionId: "s-web",
    assetId: "a-web",
    assetKind: "ssh",
    panes: [{ id: "p-web", tabs, activeTabId: tabs[0]?.id ?? null }],
    activePaneId: "p-web",
    closable: true,
    ...extra,
  };
}

function session(status: SessionInfo["status"]): SessionInfo {
  return { id: "s-web", assetId: "a-web", name: "web-01", kind: "ssh", status, tabs: [], createdAt: 0 };
}

function mountView(assetId: string): MountedView {
  mounted = mountWithClient(createElement(HostDetailView, { assetId, visible: true }));
  return mounted;
}

function text(): string {
  return mounted?.container.textContent ?? "";
}

function section(testId: string): HTMLElement {
  const el = mounted?.container.querySelector<HTMLElement>(`[data-testid="${testId}"]`);
  if (!el) throw new Error(`section ${testId} not found`);
  return el;
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.assetList.mockResolvedValue([WEB, LOCAL, TUNNELLED_DB, SAME_HOST_DB, LOCAL_DB, OTHER_DB]);
  mocks.dockerPs.mockResolvedValue([]);
  mocks.dbSchemas.mockResolvedValue([]);
  useUi.setState({ workspaces: [], activeWorkspaceId: null, sessions: [], toasts: [] });
  seedUser(USER);
  routeFleet([]);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

describe("host detail helpers", () => {
  it("relates databases tunnelled through the host or at its address", () => {
    const all = [WEB, LOCAL, TUNNELLED_DB, SAME_HOST_DB, LOCAL_DB, OTHER_DB];
    expect(relatedDatabases(WEB, all).map((a) => a.id)).toEqual(["db-1", "db-2"]);
    // A database tunnelled through another host is not on this host even at a loopback address.
    expect(relatedDatabases(LOCAL, all).map((a) => a.id)).toEqual(["db-3"]);
    expect(relatedDatabases({ ...WEB, host: "" }, all).map((a) => a.id)).toEqual(["db-1"]);
    expect(relatedDatabases(WEB, [{ ...SAME_HOST_DB, deletedAt: 5 }])).toEqual([]);
  });

  it("names device hosts and formats addresses", () => {
    expect(deviceIdOfHost("device/d-1")).toBe("d-1");
    expect(deviceIdOfHost("device/")).toBeNull();
    expect(deviceIdOfHost("a-web")).toBeNull();
    expect(hostAddress(WEB)).toBe("root@10.0.0.8:22");
    expect(hostAddress({ host: "fe80::1", port: 22, username: null })).toBe("[fe80::1]:22");
    expect(hostAddress({ host: null, port: null, username: "root" })).toBe("");
    expect(hostDetailLabel("ssh")).toBe("主机详情");
    expect(hostDetailLabel("postgres")).toBe("数据库详情");
    expect(hostDetailLabel("kubernetes")).toBeNull();
  });
});

describe("host detail page for an asset", () => {
  it("shows the connected host's sessions, containers, databases and schemas on one page", async () => {
    const terminal: AppTab = { id: "t1", kind: "terminal", title: "bash", sessionId: "s-web", tabId: "k1", closable: true };
    const files: AppTab = { id: "f1", kind: "files", title: "文件", sessionId: "s-web", closable: true };
    useUi.setState({
      workspaces: [
        hostWorkspace([terminal, files]),
        {
          id: "ws-db",
          kind: "db",
          title: "orders",
          connId: "c-1",
          dbKind: "postgres",
          assetId: "db-1",
          panes: [{ id: "p-db", tabs: [], activeTabId: null }],
          activePaneId: "p-db",
          closable: true,
        },
      ],
      activeWorkspaceId: "ws-db",
      sessions: [session("connected")],
    });
    mocks.dockerPs.mockResolvedValue([
      { id: "c1", name: "nginx", image: "nginx:1.27", state: "running", status: "Up", ports: "", composeProject: null },
    ]);
    mocks.dbSchemas.mockResolvedValue(["public", "audit"]);
    mountView("a-web");
    await flushUntil(() => text().includes("nginx") && text().includes("audit"));

    expect(mounted!.container.querySelector('[data-testid="host-detail-name"]')?.textContent).toBe("web-01");
    expect(text()).toContain("root@10.0.0.8:22");
    expect(section("host-session-status").textContent).toBe("已连接");
    expect(mocks.dockerPs).toHaveBeenCalledWith("s-web");
    expect(mocks.dbSchemas).toHaveBeenCalledWith("c-1");
    const databases = section("host-databases").textContent ?? "";
    expect(databases).toContain("orders");
    expect(databases).toContain("cache");
    expect(databases).not.toContain("elsewhere");

    // Selecting a tab in the session list switches to it in the host workspace.
    clickButton(section("host-sessions"), "bash终端");
    const state = useUi.getState();
    expect(state.activeWorkspaceId).toBe("ws-web");
    expect(state.workspaces.find((w) => w.id === "ws-web")?.panes[0].activeTabId).toBe("t1");
  });

  it("waits for a connection before reading containers and opens the workspace only on request", async () => {
    mountView("a-web");
    await flushUntil(() => text().includes("web-01"));

    expect(section("host-sessions").textContent).toContain("尚未打开这台主机的工作区");
    expect(section("host-containers").textContent).toContain("打开并连接工作区后可查看容器");
    expect(mocks.dockerPs).not.toHaveBeenCalled();
    expect(mocks.connectAsset).not.toHaveBeenCalled();
    expect(mocks.healthProbe).not.toHaveBeenCalled();

    clickButton(mounted!.container, "打开工作区");
    expect(mocks.connectAsset).toHaveBeenCalledWith(expect.objectContaining({ id: "a-web" }));
  });

  it("probes an SSH host's health on request", async () => {
    mocks.healthProbe.mockResolvedValue({
      batchId: "b",
      results: [{ assetId: "a-web", reachable: true, latencyMs: 4, status: "success", load1: 0.5, load5: 0.25, load15: 0.1, memoryUsedPercent: 41.25, diskUsedPercent: 70, uptimeSeconds: 3600 }],
    });
    mountView("a-web");
    await flushUntil(() => text().includes("web-01"));
    clickButton(section("host-metrics"), "检查");
    await flushUntil(() => text().includes("41.3%"));
    expect(mocks.healthProbe).toHaveBeenCalledWith(expect.objectContaining({ targets: [{ assetId: "a-web" }] }));
    expect(section("host-metrics").textContent).toContain("0.50 / 0.25 / 0.10");
  });

  it("explains a missing asset", async () => {
    mountView("gone");
    await flushUntil(() => text().includes("资产不存在或已删除"));
    expect(mounted!.container.querySelector('[data-testid="host-sessions"]')).toBeNull();
  });

  it("links a tunnelled database to its host", async () => {
    mountView("db-1");
    await flushUntil(() => text().includes("orders"));
    expect(text()).toContain("经由");
    click([...mounted!.container.querySelectorAll("button")].find((b) => b.textContent === "web-01")!);
    const hostTab = useUi.getState().workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs)).find((t) => t.kind === "host");
    expect(hostTab?.assetId).toBe("a-web");
  });
});

describe("host detail page for a device", () => {
  it("asks to sign in when no account is logged in and sends no request", async () => {
    seedUser(null);
    mountView("device/d-1");
    await flushUntil(() => text().includes("登录服务端账号后可查看设备"));
    expect(fetched).toEqual([]);
  });

  it("reports a device the account cannot see", async () => {
    routeFleet([ONLINE_DEVICE]);
    mountView("device/d-9");
    await flushUntil(() => text().includes("无法查看这台设备"));
    expect(text()).not.toContain("nas-01");
  });

  it("shows an offline device with its workspace action disabled", async () => {
    routeFleet([ONLINE_DEVICE, OFFLINE_DEVICE]);
    mountView("device/d-2");
    await flushUntil(() => text().includes("设备离线，没有近期指标"));
    expect(section("host-device-status").textContent).toBe("离线");
    const open = [...mounted!.container.querySelectorAll("button")].find((b) => b.textContent === "打开工作区");
    expect(open?.disabled).toBe(true);
    expect(open?.title).toBe("设备离线，无法打开工作区");
    expect(section("host-containers").textContent).toContain("打开并连接工作区后可查看容器");
    expect(fetched).toContain("/fleet/devices/d-2/metrics");
  });

  it("opens an online device's workspace on request", async () => {
    routeFleet([ONLINE_DEVICE]);
    mountView("device/d-1");
    await flushUntil(() => text().includes("nas-01"));
    expect(section("host-device-status").textContent).toBe("在线");
    clickButton(mounted!.container, "打开工作区");
    expect(mocks.connectDevice).toHaveBeenCalledWith(expect.objectContaining({ id: "d-1" }));
  });
});
