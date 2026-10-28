/** @vitest-environment jsdom */
// Asset tree device rows tests: row position (after the built-in local terminal, before other assets/groups), rows-only contract
// (no header/count/refresh button, no loading/error/empty hints), no rows
// and zero requests when logged out, click/Enter/play opens the device workspace, offline/agent-less rows stay visible but disabled,
// terminal-closed and revoked rows stay visible but disabled, revoke double-confirm and
// cancel, context-menu revoke danger style, DevicesView reusing MetricsPanel regression.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    fetch: vi.fn(),
    ask: vi.fn(),
    assetList: vi.fn(),
    groupList: vi.fn(),
    snippetList: vi.fn(),
    sessionConnectDevice: vi.fn(),
    listenEvent: vi.fn(),
    deviceStatusHandlers: [] as ((payload: unknown) => void)[],
    onEventsResync: vi.fn(),
    resyncHandlers: [] as (() => void)[],
  };
});

vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));
vi.mock("../../ipc/events", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/events")>()),
  listenEvent: mocks.listenEvent,
}));
vi.mock("../../ipc/webTransport", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/webTransport")>()),
  onEventsResync: mocks.onEventsResync,
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      list: mocks.assetList,
      search: vi.fn(),
      update: vi.fn(),
      delete: vi.fn(),
      groupList: mocks.groupList,
      groupCreate: vi.fn(),
      groupUpdate: vi.fn(),
      groupDelete: vi.fn(),
      snippetList: mocks.snippetList,
      snippetCreate: vi.fn(),
      snippetUpdate: vi.fn(),
      snippetDelete: vi.fn(),
      readKeyFile: vi.fn(),
    },
    vaultApi: { listCredentials: vi.fn().mockResolvedValue([]), setCredential: vi.fn() },
    sessionApi: {
      connect: vi.fn(),
      probe: vi.fn(),
      list: vi.fn().mockResolvedValue([]),
      connectDevice: mocks.sessionConnectDevice,
    },
    terminalApi: { write: vi.fn() },
    dbApi: {},
  };
});

import { AssetTree } from "../../features/explorer/AssetTree";
import { DevicesView } from "../../features/fleet/DevicesView";
import { useAuth } from "../../features/auth/store";
import { useUi } from "../../app/store";

const NOW = Date.now();

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

const ONLINE_DEVICE = {
  id: "d-1",
  name: "web-01",
  kind: "agent",
  created_at: NOW - 86400_000,
  last_seen_at: NOW - 30_000,
  revoked_at: 0,
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
  agent: {
    platform: "windows",
    app_version: "0.2.2",
    desired_autostart: false,
    terminal_enabled: true,
    current_url: "",
    service_state: { installed: true, enabled: false, active: false, last_reconcile_at: NOW - 3600_000 },
    last_seen_at: NOW - 3600_000,
  },
};

const TERMINAL_DISABLED_DEVICE = {
  ...OFFLINE_DEVICE,
  id: "d-6",
  name: "noterm-01",
  last_seen_at: NOW - 30_000,
  agent: { ...OFFLINE_DEVICE.agent, terminal_enabled: false, last_seen_at: NOW - 30_000 },
};

const NO_AGENT_DEVICE = { ...ONLINE_DEVICE, id: "d-7", name: "bare-01", agent: undefined };

const REVOKED_DEVICE = { ...ONLINE_DEVICE, id: "d-4", name: "old-laptop", revoked_at: NOW - 3600_000 };

const SECOND_ONLINE = { ...ONLINE_DEVICE, id: "d-9", name: "nas-01" };

const METRICS_SAMPLES = [
  {
    ts: NOW - 120_000,
    cpu_pct: 5,
    mem_used: 4294967296,
    mem_total: 17179869184,
    disk_used: 107374182400,
    disk_total: 214748364800,
    uptime_s: 93784,
  },
  {
    ts: NOW - 60_000,
    cpu_pct: 12.5,
    mem_used: 8589934592,
    mem_total: 17179869184,
    disk_used: 107374182400,
    disk_total: 214748364800,
    uptime_s: 93844,
  },
];

const LOCAL_ASSET = {
  id: "a-local",
  groupId: null,
  kind: "local",
  name: "本机终端",
  host: "",
  port: 0,
  username: "",
  authKind: "none",
  keyPath: null,
  credId: null,
  options: {},
  tags: "",
  note: "",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
  deletedAt: null,
  builtin: true,
};

const WEB_ASSET = { ...LOCAL_ASSET, id: "a-web", kind: "ssh", name: "web-asset", host: "10.0.0.8", port: 22, builtin: false };
const GROUP = { id: "g1", parentId: null, name: "Production", sort: 0, createdAt: 1, updatedAt: 1 };

interface RecordedCall {
  url: string;
  method: string;
}

function route(
  devices: unknown,
  metrics: unknown = {
    samples: METRICS_SAMPLES,
    latest: METRICS_SAMPLES[METRICS_SAMPLES.length - 1],
    windowStart: NOW - 86400_000,
    windowEnd: NOW,
    sampleCount: METRICS_SAMPLES.length,
  },
): RecordedCall[] {
  const calls: RecordedCall[] = [];
  mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
    const u = String(url);
    const method = init.method ?? "GET";
    calls.push({ url: u, method });
    const json = (payload: unknown, status = 200) => ({ ok: status < 400, status, text: async () => JSON.stringify(payload) });
    if (u === "/fleet/devices" && method === "GET") return json(devices);
    if (u === "/fleet/base-urls" && method === "GET") return json({ base_urls: [] });
    if (u === "/fleet/devices/d-1/metrics" && method === "GET") return json(metrics);
    if (u.endsWith("/revoke") && method === "POST") return json({ ok: true });
    return json({ error: { code: "not_found", message: u } }, 404);
  });
  vi.stubGlobal("fetch", mocks.fetch);
  return calls;
}

function seedUser(user: typeof SUPERADMIN | null) {
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth: "on" },
    user,
    dek: null,
    gate: "ready",
    pendingRecoveryKey: null,
    error: null,
  });
}

function seedWorkspace() {
  useUi.setState({
    leftOpen: true,
    activeWorkspaceId: "ws1",
    workspaces: [
      {
        id: "ws1",
        kind: "session" as const,
        title: "ws",
        closable: true,
        panes: [{ id: "p1", activeTabId: null, tabs: [] }],
        activePaneId: "p1",
      },
    ],
  });
}

function stubFinePointer(): void {
  vi.stubGlobal(
    "matchMedia",
    (query: string) =>
      ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
      }) as MediaQueryList,
  );
}

let mounted: MountedView | undefined;

function mountTree() {
  mounted = mountWithClient(createElement(AssetTree));
}

function tree(): HTMLElement {
  const el = document.querySelector<HTMLElement>('[role="tree"]');
  if (!el) throw new Error("资产树未渲染");
  return el;
}

function deviceRow(name: string): HTMLElement {
  const row = [...tree().querySelectorAll<HTMLElement>('[role="treeitem"]')].find((r) => r.textContent?.includes(name));
  if (!row) throw new Error(`设备行未找到: ${name}`);
  return row;
}

function openDeviceMenu(name: string): void {
  act(() => {
    deviceRow(name).dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, clientX: 40, clientY: 40 }));
  });
}

function clickDeviceMenuItem(label: string): void {
  const item = [...document.querySelectorAll<HTMLButtonElement>(".nx-menu-item")].find((button) =>
    button.textContent?.includes(label),
  );
  if (!item) throw new Error(`设备菜单项未找到: ${label}`);
  click(item);
}

function deviceWorkspace() {
  return useUi.getState().workspaces.find((w) => w.assetId === "device/d-1");
}

function deviceTerminalTabs() {
  return (deviceWorkspace()?.panes ?? []).flatMap((p) =>
    p.tabs.filter((t) => t.kind === "terminal" && t.sessionId === "s-dev-1"),
  );
}

function keydown(element: HTMLElement, key: string): void {
  act(() => {
    element.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ rightOpen: false });
  stubFinePointer();
  mocks.assetList.mockResolvedValue([LOCAL_ASSET, WEB_ASSET]);
  mocks.groupList.mockResolvedValue([GROUP]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.sessionConnectDevice.mockResolvedValue({
    id: "s-dev-1",
    assetId: "device/d-1",
    name: "web-01",
    kind: "device",
    status: "connected",
    tabs: [],
    createdAt: 1,
  });
  mocks.deviceStatusHandlers.length = 0;
  mocks.resyncHandlers.length = 0;
  mocks.listenEvent.mockImplementation((event: string, handler: (payload: unknown) => void) => {
    if (event === "device://status") mocks.deviceStatusHandlers.push(handler);
    return Promise.resolve(() => {});
  });
  mocks.onEventsResync.mockImplementation((cb: () => void) => {
    mocks.resyncHandlers.push(cb);
    return () => {};
  });
  seedWorkspace();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("Asset tree device rows - placement and login gating", () => {
  it("renders device rows after the local terminal and before other assets and groups", async () => {
    route({ devices: [ONLINE_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const children = [...tree().children];
    const localIdx = children.findIndex((el) => el.textContent?.includes("本机终端"));
    const deviceIdx = children.findIndex((el) => el.textContent?.includes("web-01"));
    const webIdx = children.findIndex((el) => el.textContent?.includes("web-asset"));
    const groupIdx = children.findIndex((el) => el.textContent?.includes("Production"));
    expect(localIdx).toBeGreaterThanOrEqual(0);
    expect(deviceIdx).toBeGreaterThan(localIdx);
    expect(webIdx).toBeGreaterThan(deviceIdx);
    expect(groupIdx).toBeGreaterThan(deviceIdx);
  });

  it("renders no device rows, sends no fleet requests, and subscribes to no events when logged out", async () => {
    const calls = route({ devices: [ONLINE_DEVICE] });
    seedUser(null);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-asset") ?? false);
    await flush();

    expect(tree().textContent ?? "").not.toContain("web-01");
    expect(document.querySelector('[role="group"][aria-label="设备"]')).toBeNull();
    expect(calls.filter((c) => c.url.startsWith("/fleet") || c.url.startsWith("/device"))).toHaveLength(0);
    expect(mocks.deviceStatusHandlers).toHaveLength(0);
    expect(mocks.resyncHandlers).toHaveLength(0);
  });
});

describe("Asset tree device rows - rows only", () => {
  it("renders only device rows for online devices with no section title, count, refresh button, or hint rows", async () => {
    route({ devices: [ONLINE_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    expect(deviceRow("web-01").getAttribute("role")).toBe("treeitem");
    expect(document.querySelector('[role="group"][aria-label="设备"]')).toBeNull();
    expect(document.querySelector('button[aria-label="刷新设备列表"]')).toBeNull();
    for (const hint of ["设备列表加载中", "设备列表加载失败", "还没有接入设备", "设备都不在线"]) {
      expect(tree().textContent ?? "").not.toContain(hint);
    }
  });

  it("renders no device content or loading hint while the device list is loading", async () => {
    mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
      const u = String(url);
      const method = init.method ?? "GET";
      const json = (payload: unknown, status = 200) => ({ ok: status < 400, status, text: async () => JSON.stringify(payload) });
      if (u === "/fleet/devices" && method === "GET") return new Promise(() => {});
      return json({ error: { code: "not_found", message: u } }, 404);
    });
    vi.stubGlobal("fetch", mocks.fetch);
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-asset") ?? false);
    await flushUntil(() => mocks.fetch.mock.calls.some((c) => String(c[0]) === "/fleet/devices"));
    await flush();

    expect(tree().textContent ?? "").not.toContain("设备列表加载中");
    expect(document.querySelector('[role="group"][aria-label="设备"]')).toBeNull();
  });

  it("renders no error row or retry button when device loading fails", async () => {
    mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
      const u = String(url);
      const method = init.method ?? "GET";
      const json = (payload: unknown, status = 200) => ({ ok: status < 400, status, text: async () => JSON.stringify(payload) });
      if (u === "/fleet/devices" && method === "GET") return json({ error: { code: "boom", message: "boom" } }, 500);
      return json({ error: { code: "not_found", message: u } }, 404);
    });
    vi.stubGlobal("fetch", mocks.fetch);
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-asset") ?? false);
    await flushUntil(() => mocks.fetch.mock.calls.some((c) => String(c[0]) === "/fleet/devices"));
    await flush();

    expect(tree().textContent ?? "").not.toContain("设备列表加载失败");
    expect(tree().textContent ?? "").not.toContain("重试");
    expect(document.querySelector('[role="group"][aria-label="设备"]')).toBeNull();
  });

  it("renders no empty-state hint when no devices are enrolled", async () => {
    const calls = route({ devices: [] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-asset") ?? false);
    await flushUntil(() => calls.some((c) => c.url === "/fleet/devices" && c.method === "GET"));
    await flush();

    expect(tree().textContent ?? "").not.toContain("还没有接入设备");
    expect(document.querySelector('[role="group"][aria-label="设备"]')).toBeNull();
  });

  it("keeps an all-offline device visible and disabled without an empty-state hint", async () => {
    const calls = route({ devices: [OFFLINE_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-asset") ?? false);
    await flushUntil(() => calls.some((c) => c.url === "/fleet/devices" && c.method === "GET"));
    await flush();

    expect(tree().textContent ?? "").toContain("db-02");
    expect(deviceRow("db-02").getAttribute("aria-disabled")).toBe("true");
    expect(deviceRow("db-02").textContent).toContain("离线");
    expect(tree().textContent ?? "").not.toContain("设备都不在线");
    expect(document.querySelector('[role="group"][aria-label="设备"]')).toBeNull();
  });
});

describe("Asset tree device rows - row behavior", () => {
  it("opens the device workspace from the play button and Enter", async () => {
    route({ devices: [ONLINE_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const row = deviceRow("web-01");
    expect(row.getAttribute("role")).toBe("treeitem");
    const actions = [...row.querySelectorAll<HTMLButtonElement>(".nx-row-actions button")];
    expect(actions.map((button) => button.getAttribute("aria-label"))).toEqual(["打开工作区 web-01"]);
    expect(row.querySelector(".nx-row-more")).toBeNull();

    click(actions[0]);
    await flushUntil(() => deviceWorkspace() !== undefined);
    expect(mocks.sessionConnectDevice).toHaveBeenCalledTimes(1);
    expect(mocks.sessionConnectDevice).toHaveBeenCalledWith("d-1", "web-01");
    expect(deviceWorkspace()?.assetKind).toBe("device");
    expect(deviceWorkspace()?.sessionId).toBe("s-dev-1");
    expect(useUi.getState().activeWorkspaceId).toBe(deviceWorkspace()?.id);
    expect(deviceTerminalTabs()).toHaveLength(0);

    // Enter on the same device again focuses the existing workspace, no new session is created.
    keydown(row, "Enter");
    await flush();
    expect(mocks.sessionConnectDevice).toHaveBeenCalledTimes(1);
    expect(deviceTerminalTabs()).toHaveLength(0);
  });

  it("disables offline, agent-less, and terminal-disabled devices without hiding them", async () => {
    route({ devices: [OFFLINE_DEVICE, NO_AGENT_DEVICE, TERMINAL_DISABLED_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("noterm-01") ?? false);

    const offline = deviceRow("db-02");
    const noAgent = deviceRow("bare-01");
    expect(offline.getAttribute("aria-disabled")).toBe("true");
    expect(offline.textContent).toContain("离线");
    expect(offline.querySelector<HTMLButtonElement>(".nx-row-actions button")?.disabled).toBe(true);
    expect(noAgent.getAttribute("aria-disabled")).toBe("true");
    expect(noAgent.textContent).toContain("未接入 Agent");
    expect(noAgent.querySelector<HTMLButtonElement>(".nx-row-actions button")?.disabled).toBe(true);

    const noTerm = deviceRow("noterm-01");
    expect(noTerm.getAttribute("aria-disabled")).toBe("true");
    expect(noTerm.getAttribute("title")).toContain("该设备已关闭远程终端");

    click(offline);
    keydown(offline, "Enter");
    click(noAgent);
    keydown(noAgent, "Enter");
    click(noTerm);
    keydown(noTerm, "Enter");
    openDeviceMenu("db-02");
    await flushUntil(() => document.querySelector(".nx-menu-item") !== null);
    const menuConnect = [...document.querySelectorAll<HTMLButtonElement>(".nx-menu-item")].find((button) =>
      button.textContent?.includes("打开工作区"),
    );
    expect(menuConnect?.disabled).toBe(true);
    await flush();
    expect(mocks.sessionConnectDevice).not.toHaveBeenCalled();
    expect(deviceWorkspace()).toBeUndefined();
  });

  it("does not list revoked devices in the tree", async () => {
    route({ devices: [ONLINE_DEVICE, REVOKED_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);
    expect(tree().textContent).not.toContain("old-laptop");
    expect(tree().textContent).not.toContain("已吊销");
  });

  it("renders no device rows when every device is revoked", async () => {
    const calls = route({ devices: [REVOKED_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => calls.some((c) => c.url === "/fleet/devices"));
    await flush();
    expect(tree().textContent).not.toContain("old-laptop");
    expect(tree().textContent).not.toContain("已吊销");
  });

  it("keeps device management actions in the context menu", async () => {
    route({ devices: [ONLINE_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    expect(deviceRow("web-01").querySelectorAll(".nx-row-actions button")).toHaveLength(1);
    openDeviceMenu("web-01");
    await flushUntil(() => document.querySelector(".nx-menu-item") !== null);
    expect(document.body.textContent).toContain("详情");
    const revokeItem = [...document.querySelectorAll<HTMLButtonElement>(".nx-menu-item")].find((b) =>
      b.textContent?.includes("吊销"),
    );
    expect(revokeItem?.classList.contains("is-danger")).toBe(true);
  });

  it("opens the management menu on touch long press without connecting", async () => {
    route({ devices: [ONLINE_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);
    vi.useFakeTimers();

    const row = deviceRow("web-01");
    const pointer = (type: string) => {
      const event = new Event(type, { bubbles: true });
      Object.defineProperties(event, {
        pointerType: { value: "touch" },
        clientX: { value: 40 },
        clientY: { value: 40 },
      });
      return event;
    };
    act(() => {
      row.dispatchEvent(pointer("pointerdown"));
      vi.advanceTimersByTime(500);
      row.dispatchEvent(pointer("pointerup"));
    });
    act(() => {
      vi.advanceTimersByTime(100);
      row.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(document.body.textContent).toContain("吊销");
    expect(mocks.sessionConnectDevice).not.toHaveBeenCalled();

    act(() => {
      row.dispatchEvent(pointer("pointerdown"));
      row.dispatchEvent(pointer("pointerup"));
      row.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    vi.useRealTimers();
    await flushUntil(() => deviceWorkspace() !== undefined);
    expect(mocks.sessionConnectDevice).toHaveBeenCalledTimes(1);
  });

  it("moves focus between device rows and adjacent tree rows with arrow keys", async () => {
    route({ devices: [ONLINE_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const rows = () => [...tree().querySelectorAll<HTMLElement>('[role="treeitem"]')];
    const row = deviceRow("web-01");
    act(() => {
      row.focus();
    });
    expect(document.activeElement).toBe(row);

    keydown(row, "ArrowDown");
    const next = rows()[rows().indexOf(row) + 1];
    expect(next).toBeDefined();
    expect(document.activeElement).toBe(next);

    act(() => {
      row.focus();
    });
    keydown(row, "ArrowUp");
    const prev = rows()[rows().indexOf(row) - 1];
    expect(prev).toBeDefined();
    expect(document.activeElement).toBe(prev);
  });
});

describe("Asset tree device rows - automatic convergence", () => {
  it("polls every 60 seconds on WEB and drops devices revoked elsewhere", async () => {
    vi.useFakeTimers();
    let devices: unknown[] = [ONLINE_DEVICE];
    const calls: RecordedCall[] = [];
    mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
      const u = String(url);
      const method = init.method ?? "GET";
      calls.push({ url: u, method });
      const json = (payload: unknown, status = 200) => ({ ok: status < 400, status, text: async () => JSON.stringify(payload) });
      if (u === "/fleet/devices" && method === "GET") return json({ devices });
      return json({ error: { code: "not_found", message: u } }, 404);
    });
    vi.stubGlobal("fetch", mocks.fetch);
    seedUser(SUPERADMIN);
    mountTree();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const deviceFetches = () => calls.filter((c) => c.url === "/fleet/devices" && c.method === "GET").length;
    const before = deviceFetches();

    // After a revoke elsewhere, the next 60s poll removes the row.
    devices = [{ ...ONLINE_DEVICE, revoked_at: NOW - 60_000 }];
    await act(async () => {
      await vi.advanceTimersByTimeAsync(61_000);
    });
    await flushUntil(() => !(document.body.textContent?.includes("web-01") ?? false));
    expect(deviceFetches()).toBeGreaterThan(before);
  });

  it("updates the visible device row's disabled state in real time without refetching", async () => {
    const calls = route({ devices: [ONLINE_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const handler = mocks.deviceStatusHandlers.at(-1);
    if (!handler) throw new Error("device://status 未订阅");
    const fetchesBefore = calls.length;

    act(() => handler({ deviceId: "d-x", online: false }));
    await flush();
    expect(tree().textContent).toContain("web-01");

    act(() => handler({ deviceId: "d-1", online: false }));
    await flush();
    expect(tree().textContent ?? "").toContain("web-01");
    expect(deviceRow("web-01").getAttribute("aria-disabled")).toBe("true");
    expect(deviceRow("web-01").textContent).toContain("离线");
    expect(calls.length).toBe(fetchesBefore);

    act(() => handler({ deviceId: "d-1", online: true }));
    await flush();
    expect(tree().textContent).toContain("web-01");
    expect(deviceRow("web-01").getAttribute("aria-disabled")).toBe("false");
    expect(calls.length).toBe(fetchesBefore);
  });

  it("refetches devices immediately on a WebSocket resync without waiting for the next poll", async () => {
    vi.useFakeTimers();
    const calls = route({ devices: [ONLINE_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const resync = mocks.resyncHandlers.at(-1);
    if (!resync) throw new Error("onEventsResync 未订阅");
    const deviceFetches = () => calls.filter((c) => c.url === "/fleet/devices" && c.method === "GET").length;
    const before = deviceFetches();

    await act(async () => {
      resync();
      await vi.advanceTimersByTimeAsync(0);
    });
    await flushUntil(() => deviceFetches() > before);
  });
});

describe("Asset tree device rows - two-step revoke confirmation", () => {
  it("revokes only after both warnings are confirmed and removes the row immediately", async () => {
    const calls = route({ devices: [ONLINE_DEVICE, SECOND_ONLINE] });
    mocks.ask.mockResolvedValue(true);
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    openDeviceMenu("web-01");
    await flushUntil(() => document.querySelector(".nx-menu-item") !== null);
    clickDeviceMenuItem("吊销");
    await flushUntil(() => calls.some((c) => c.url === "/fleet/devices/d-1/revoke" && c.method === "POST"));
    await flushUntil(() => !(tree().textContent ?? "").includes("web-01"));

    expect(mocks.ask).toHaveBeenCalledTimes(2);
    expect(String(mocks.ask.mock.calls[0]?.[1]?.kind)).toBe("warning");
    expect(String(mocks.ask.mock.calls[1]?.[1]?.kind)).toBe("warning");
    expect(String(mocks.ask.mock.calls[1]?.[0])).toContain("凭证立即失效");
    expect(String(mocks.ask.mock.calls[1]?.[0])).toContain("设备仍显示为已吊销但无法连接");
    expect(tree().textContent).not.toContain("web-01");
    expect(tree().textContent).toContain("nas-01");
  });

  it("does not show the second confirmation or send a request when the first is canceled", async () => {
    const calls = route({ devices: [ONLINE_DEVICE] });
    mocks.ask.mockResolvedValue(false);
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    openDeviceMenu("web-01");
    await flushUntil(() => document.querySelector(".nx-menu-item") !== null);
    clickDeviceMenuItem("吊销");
    await flushUntil(() => mocks.ask.mock.calls.length === 1);
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(calls.some((c) => c.url.includes("/revoke"))).toBe(false);
    expect(tree().textContent).toContain("web-01");
  });

  it("does not send a request and keeps the device when the second confirmation is canceled", async () => {
    const calls = route({ devices: [ONLINE_DEVICE] });
    mocks.ask.mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    openDeviceMenu("web-01");
    await flushUntil(() => document.querySelector(".nx-menu-item") !== null);
    clickDeviceMenuItem("吊销");
    await flushUntil(() => mocks.ask.mock.calls.length === 2);
    await flush();
    expect(calls.some((c) => c.url.includes("/revoke"))).toBe(false);
    expect(tree().textContent).toContain("web-01");
  });
});

describe("DevicesView and AssetTree cache synchronization", () => {
  it("shows a newly online device in both views immediately after a manual refresh", async () => {
    const response: { devices: unknown[] } = { devices: [] };
    const calls = route(response);
    const deviceFetches = () => calls.filter((c) => c.url === "/fleet/devices" && c.method === "GET").length;
    seedUser(SUPERADMIN);
    mounted = mountWithClient(
      createElement("div", null, createElement(AssetTree), createElement(DevicesView)),
    );
    await flushUntil(() => document.body.textContent?.includes("共 0 台") ?? false);
    await flushUntil(() => document.body.textContent?.includes("web-asset") ?? false);
    await flushUntil(() => deviceFetches() >= 2);
    await flush();
    expect(tree().textContent ?? "").not.toContain("web-01");

    response.devices = [ONLINE_DEVICE];
    const before = deviceFetches();
    const management = document.querySelector<HTMLElement>(".nx-pane");
    if (!management) throw new Error("DevicesView did not render");
    const refresh = [...management.querySelectorAll("button")].find((button) => button.textContent?.trim() === "刷新");
    if (!refresh) throw new Error("Refresh button not found");
    click(refresh);

    await flushUntil(() => management.textContent?.includes("共 1 台") ?? false);
    await flushUntil(() => tree().textContent?.includes("web-01") ?? false);
    expect(management.textContent).toContain("web-01");
    expect(deviceFetches()).toBe(before + 1);
  });
});

describe("DevicesView MetricsPanel reuse regression", () => {
  it("renders trend SVG and sample statistics when expanding metrics in DevicesView", async () => {
    route({ devices: [ONLINE_DEVICE] });
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("web-01"));
    const metricsButton = [...(card?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === "指标");
    click(metricsButton as HTMLButtonElement);
    await flushUntil(() => card?.querySelector('svg[role="img"]') !== null);

    expect(card?.querySelector("polyline")).not.toBeNull();
    expect(card?.textContent).toContain("12.5%");
    expect(card?.textContent).not.toContain("趋势窗口");
    expect(card?.textContent).not.toContain("采样点");
  });
});
