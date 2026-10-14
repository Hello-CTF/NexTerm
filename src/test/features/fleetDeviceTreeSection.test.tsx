/** @vitest-environment jsdom */
// Asset tree device rows tests: row position (after the built-in local terminal, before other assets/groups), rows-only contract
// (no header/count/refresh button, no loading/error/empty/all-offline hints - the component renders null in those states), no rows
// and zero requests when logged out, click/Enter opens the device terminal (no separate "Terminal" button), only online devices are
// listed (offline/agent-less rows hidden, terminal-closed rows stay but disabled), revoked not shown, revoke double-confirm and
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
const GROUP = { id: "g1", parentId: null, name: "生产", sort: 0, createdAt: 1, updatedAt: 1 };

interface RecordedCall {
  url: string;
  method: string;
}

function route(devices: unknown, metrics: unknown = { samples: METRICS_SAMPLES }): RecordedCall[] {
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
        splitRatio: 0.5,
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

describe("资产树设备行 · 位置与登录门控", () => {
  it("设备行渲染在「本机终端」之后、其他资产与分组之前", async () => {
    route({ devices: [ONLINE_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const children = [...tree().children];
    const localIdx = children.findIndex((el) => el.textContent?.includes("本机终端"));
    const deviceIdx = children.findIndex((el) => el.textContent?.includes("web-01"));
    const webIdx = children.findIndex((el) => el.textContent?.includes("web-asset"));
    const groupIdx = children.findIndex((el) => el.textContent?.includes("生产"));
    expect(localIdx).toBeGreaterThanOrEqual(0);
    expect(deviceIdx).toBeGreaterThan(localIdx);
    expect(webIdx).toBeGreaterThan(deviceIdx);
    expect(groupIdx).toBeGreaterThan(deviceIdx);
  });

  it("未登录不渲染设备行, 不发任何 fleet 请求, 也不订阅事件", async () => {
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

describe("资产树设备行 · 仅渲染设备行", () => {
  it("有在线设备时只渲染设备行: 无分区标题/计数/刷新按钮, 也无任何提示行", async () => {
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

  it("设备列表加载中: 不渲染任何设备内容, 也无加载提示", async () => {
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

  it("设备列表加载失败: 不渲染错误行或重试按钮", async () => {
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

  it("没有接入设备: 不渲染空态提示", async () => {
    const calls = route({ devices: [] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-asset") ?? false);
    await flushUntil(() => calls.some((c) => c.url === "/fleet/devices" && c.method === "GET"));
    await flush();

    expect(tree().textContent ?? "").not.toContain("还没有接入设备");
    expect(document.querySelector('[role="group"][aria-label="设备"]')).toBeNull();
  });

  it("设备都不在线: 不渲染全离线提示, 也不显示离线设备行", async () => {
    const calls = route({ devices: [OFFLINE_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-asset") ?? false);
    await flushUntil(() => calls.some((c) => c.url === "/fleet/devices" && c.method === "GET"));
    await flush();

    expect(tree().textContent ?? "").not.toContain("db-02");
    expect(tree().textContent ?? "").not.toContain("设备都不在线");
    expect(document.querySelector('[role="group"][aria-label="设备"]')).toBeNull();
  });
});

describe("资产树设备行 · 行行为", () => {
  it("单击与 Enter 都打开设备终端工作区, 且没有独立「终端」按钮", async () => {
    route({ devices: [ONLINE_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const row = deviceRow("web-01");
    expect(row.getAttribute("role")).toBe("treeitem");
    expect([...row.querySelectorAll("button")].some((b) => b.textContent?.trim() === "终端")).toBe(false);

    click(row);
    await flushUntil(() => deviceWorkspace() !== undefined);
    expect(mocks.sessionConnectDevice).toHaveBeenCalledTimes(1);
    expect(mocks.sessionConnectDevice).toHaveBeenCalledWith("d-1", "web-01");
    expect(deviceWorkspace()?.assetKind).toBe("device");
    expect(deviceWorkspace()?.sessionId).toBe("s-dev-1");
    await flushUntil(() => deviceTerminalTabs().length === 1);

    // Enter on the same device again focuses the existing workspace, no new session is created.
    keydown(row, "Enter");
    await flush();
    expect(mocks.sessionConnectDevice).toHaveBeenCalledTimes(1);
    expect(deviceTerminalTabs()).toHaveLength(1);
  });

  it("离线设备不在资产树显示; 终端关闭的在线设备禁用并给出提示, 点击不打开终端", async () => {
    route({ devices: [OFFLINE_DEVICE, TERMINAL_DISABLED_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("noterm-01") ?? false);

    expect(tree().textContent).not.toContain("db-02");
    expect(tree().textContent).not.toContain("离线");

    const noTerm = deviceRow("noterm-01");
    expect(noTerm.getAttribute("aria-disabled")).toBe("true");
    expect(noTerm.getAttribute("title")).toContain("该设备已关闭远程终端");

    click(noTerm);
    keydown(noTerm, "Enter");
    await flush();
    expect(mocks.sessionConnectDevice).not.toHaveBeenCalled();
    expect(deviceWorkspace()).toBeUndefined();
  });

  it("已吊销设备不在资产树显示 (不是灰显)", async () => {
    route({ devices: [ONLINE_DEVICE, REVOKED_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    expect(tree().textContent).not.toContain("old-laptop");
  });

  it("上下文菜单的吊销项为 danger 样式, 行内吊销按钮为 is-danger", async () => {
    route({ devices: [ONLINE_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const inlineRevoke = tree().querySelector<HTMLButtonElement>('button[aria-label="吊销 web-01"]');
    expect(inlineRevoke?.classList.contains("is-danger")).toBe(true);

    act(() => {
      deviceRow("web-01").dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, clientX: 40, clientY: 40 }));
    });
    await flushUntil(() => document.querySelector(".nx-menu-item") !== null);
    const revokeItem = [...document.querySelectorAll<HTMLButtonElement>(".nx-menu-item")].find((b) =>
      b.textContent?.includes("吊销"),
    );
    expect(revokeItem?.classList.contains("is-danger")).toBe(true);
  });

  it("方向键从设备行移动焦点到相邻树行", async () => {
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

describe("资产树设备行 · 自动收敛", () => {
  it("WEB 每 60 秒轮询重新拉取设备列表, 他处吊销随刷新消失", async () => {
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
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const deviceFetches = () => calls.filter((c) => c.url === "/fleet/devices" && c.method === "GET").length;
    const before = deviceFetches();

    // After a revoke elsewhere, the next 60s poll removes the device row.
    devices = [{ ...ONLINE_DEVICE, revoked_at: NOW - 60_000 }];
    await act(async () => {
      await vi.advanceTimersByTimeAsync(61_000);
    });
    await flushUntil(() => !(tree().textContent ?? "").includes("web-01"));
    expect(deviceFetches()).toBeGreaterThan(before);
  });

  it("device://status 事件实时使设备行上下线, 不重拉列表; 未知设备 ID 忽略", async () => {
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
    expect(tree().textContent ?? "").not.toContain("web-01");
    expect(calls.length).toBe(fetchesBefore);

    act(() => handler({ deviceId: "d-1", online: true }));
    await flush();
    expect(tree().textContent).toContain("web-01");
    expect(calls.length).toBe(fetchesBefore);
  });

  it("事件 WS 重连 resync 时立即重取设备列表, 不等下一轮轮询", async () => {
    vi.useFakeTimers();
    const calls = route({ devices: [ONLINE_DEVICE] });
    seedUser(SUPERADMIN);
    mountTree();
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

describe("资产树设备行 · 吊销两次确认", () => {
  it("两个 warning 都确认后才吊销, 成功后设备立即从资产树移除", async () => {
    const calls = route({ devices: [ONLINE_DEVICE, SECOND_ONLINE] });
    mocks.ask.mockResolvedValue(true);
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click(tree().querySelector('button[aria-label="吊销 web-01"]') as HTMLButtonElement);
    await flushUntil(() => calls.some((c) => c.url === "/fleet/devices/d-1/revoke" && c.method === "POST"));
    await flushUntil(() => !(tree().textContent ?? "").includes("web-01"));

    expect(mocks.ask).toHaveBeenCalledTimes(2);
    expect(String(mocks.ask.mock.calls[0]?.[1]?.kind)).toBe("warning");
    expect(String(mocks.ask.mock.calls[1]?.[1]?.kind)).toBe("warning");
    expect(String(mocks.ask.mock.calls[1]?.[0])).toContain("凭证立即失效");
    expect(String(mocks.ask.mock.calls[1]?.[0])).toContain("从资产中移除且不可恢复");
    expect(tree().textContent).toContain("nas-01");
  });

  it("第一个确认取消: 不再弹第二个, 不发请求", async () => {
    const calls = route({ devices: [ONLINE_DEVICE] });
    mocks.ask.mockResolvedValue(false);
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click(tree().querySelector('button[aria-label="吊销 web-01"]') as HTMLButtonElement);
    await flushUntil(() => mocks.ask.mock.calls.length === 1);
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(calls.some((c) => c.url.includes("/revoke"))).toBe(false);
    expect(tree().textContent).toContain("web-01");
  });

  it("第二个确认取消: 不发请求, 设备保留", async () => {
    const calls = route({ devices: [ONLINE_DEVICE] });
    mocks.ask.mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    seedUser(SUPERADMIN);
    mountTree();
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click(tree().querySelector('button[aria-label="吊销 web-01"]') as HTMLButtonElement);
    await flushUntil(() => mocks.ask.mock.calls.length === 2);
    await flush();
    expect(calls.some((c) => c.url.includes("/revoke"))).toBe(false);
    expect(tree().textContent).toContain("web-01");
  });
});

describe("设备管理视图复用 MetricsPanel 回归", () => {
  it("DevicesView 展开指标同样渲染趋势 SVG 与采样统计", async () => {
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
    expect(card?.textContent).toContain("近 24 小时共 2 个采样点");
  });
});
