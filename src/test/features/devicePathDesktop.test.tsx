/** @vitest-environment jsdom */
// Device connection path on the desktop: the 连接方式 radio submenu on a device workspace's tabs, its connection tab
// and its asset tree row; the one tooltip line kept live by fleet://device-path; no toast on a path change; and the
// 连接诊断 card of the device detail page.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, flushUntil, mount, mountWithClient, type MountedView } from "./reactTestUtils";
import type { DeviceP2PDiagnostics, DevicePathStatus } from "../../ipc/fleetPathApi";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    getMode: vi.fn(),
    setMode: vi.fn(),
    status: vi.fn(),
    diagnostics: vi.fn(),
    onPathChange: vi.fn(),
    pathHandlers: [] as ((status: DevicePathStatus) => void)[],
    devices: vi.fn(),
  };
});

vi.mock("../../ipc/fleetPathApi", () => ({
  DEVICE_PATH_EVENT: "fleet://device-path",
  fleetPathApi: {
    getMode: mocks.getMode,
    setMode: mocks.setMode,
    status: mocks.status,
    diagnostics: mocks.diagnostics,
    onPathChange: mocks.onPathChange,
  },
}));
vi.mock("../../features/fleet/useFleetAccount", () => ({
  useFleetAccount: () => ({ userId: "u-1", loading: false, isAdmin: false }),
}));
vi.mock("../../features/fleet/clients", () => ({
  fleetClient: () => ({ devices: mocks.devices }),
}));

import { PaneGroup } from "../../app/workspace/PaneGroup";
import { ProjectWorkspaceStrip } from "../../app/ProjectWorkspaceStrip";
import { UNGROUPED_PROJECT } from "../../app/projectFocus";
import { useUi, type SessionRuntimeInfo, type Workspace } from "../../app/store";
import { DeviceTreeSection } from "../../features/fleet/DeviceTreeSection";
import { DeviceDiagnosticsSection } from "../../features/fleet/DeviceDiagnosticsSection";
import { devicePathTooltip, resetDevicePathForTest } from "../../features/fleet/devicePath";

const NOW = Date.now();

function workspace(id: string, assetId: string, assetKind: string): Workspace {
  const tab = { id: `${id}-tab`, kind: "terminal" as const, title: "终端 1", closable: true, sessionId: `session-${id}` };
  return {
    id,
    kind: "session",
    title: id,
    sessionId: `session-${id}`,
    assetId,
    assetKind,
    panes: [{ id: `${id}-pane`, tabs: [tab], activeTabId: tab.id }],
    activePaneId: `${id}-pane`,
    closable: true,
  };
}

const DEVICE_WS = workspace("ws-dev", "device/d-1", "device");
const SSH_WS = workspace("ws-ssh", "asset-ssh", "ssh");

function session(ws: Workspace, status: SessionRuntimeInfo["status"] = "connected"): SessionRuntimeInfo {
  return { id: ws.sessionId as string, assetId: ws.assetId ?? null, name: ws.title, kind: "ssh", status, tabs: [], createdAt: 1 };
}

function pathStatus(overrides: Partial<DevicePathStatus> = {}): DevicePathStatus {
  return { deviceId: "d-1", mode: "auto", path: "relay", reason: "", since: NOW - 1000, ...overrides };
}

let mounted: MountedView | undefined;

function renderPane(ws: Workspace) {
  mounted = mount(createElement(PaneGroup, {
    pane: ws.panes[0],
    idPrefix: "p",
    active: true,
    stripRef: () => undefined,
    split: false,
    singlePane: true,
    paneNumber: 1,
    paneCount: 1,
    splitAllowed: true,
    moveTargets: [],
    onSplit: vi.fn(),
    onClosePane: vi.fn(),
    onCyclePane: vi.fn(),
    onActivate: vi.fn(),
    onNewTerminal: vi.fn(),
    workspaceId: ws.id,
    leftOpen: true,
    onToggleLeft: vi.fn(),
  }));
}

function tabElement(ws: Workspace): HTMLElement {
  const tab = document.querySelector<HTMLElement>(`[data-tab-id="${ws.panes[0].tabs[0].id}"]`);
  if (!tab) throw new Error("tab not found");
  return tab;
}

function rightClick(element: Element): void {
  act(() => {
    element.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, clientX: 10, clientY: 10 }));
  });
}

function menuItem(label: string): HTMLButtonElement | undefined {
  return [...document.querySelectorAll<HTMLButtonElement>(".nx-menu [role='menuitem']")].find(
    (button) => button.querySelector(".nx-menu-label")?.textContent === label,
  );
}

function openModeSubmenu(): HTMLElement {
  const item = menuItem("连接方式");
  if (!item) throw new Error("连接方式 not in the menu");
  click(item);
  const submenu = document.querySelector<HTMLElement>(".nx-submenu[aria-label='连接方式']");
  if (!submenu) throw new Error("连接方式 submenu not open");
  return submenu;
}

function radios(submenu: HTMLElement): { label: string; checked: boolean; button: HTMLButtonElement }[] {
  return [...submenu.querySelectorAll<HTMLButtonElement>("[role='menuitemradio']")].map((button) => ({
    label: button.querySelector(".nx-menu-label")?.textContent ?? "",
    checked: button.getAttribute("aria-checked") === "true",
    button,
  }));
}

function checkedLabel(submenu: HTMLElement): string | undefined {
  return radios(submenu).find((radio) => radio.checked)?.label;
}

function closeMenu(): void {
  act(() => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
  });
}

beforeEach(() => {
  document.body.replaceChildren();
  resetDevicePathForTest();
  mocks.pathHandlers.length = 0;
  mocks.onPathChange.mockImplementation((handler: (status: DevicePathStatus) => void) => {
    mocks.pathHandlers.push(handler);
    return Promise.resolve(() => {
      const index = mocks.pathHandlers.indexOf(handler);
      if (index >= 0) mocks.pathHandlers.splice(index, 1);
    });
  });
  mocks.status.mockResolvedValue(pathStatus());
  mocks.getMode.mockResolvedValue({ mode: "auto" });
  useUi.setState({ workspaces: [DEVICE_WS, SSH_WS], sessions: [session(DEVICE_WS), session(SSH_WS)], toasts: [] });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.clearAllMocks();
});

describe("devicePathTooltip", () => {
  it.each<[Partial<DevicePathStatus>, string | null]>([
    [{ path: "direct" }, "直连"],
    [{ path: "relay" }, "经中转"],
    [{ path: "connecting_direct" }, "经中转"],
    [{ mode: "relay", reason: "mode_relay" }, "经中转"],
    [{ reason: "nat_blocked" }, "经中转"],
    [{ reason: "connect_failed" }, "经中转"],
    [{ reason: "unstable" }, "经中转"],
    [{ reason: "background" }, "经中转"],
    [{ reason: "server_disabled" }, "经中转（管理员已禁用直连）"],
    [{ reason: "device_unsupported" }, "经中转（设备代理版本不支持直连）"],
    [{ reason: "key_pending" }, "经中转（设备密钥已变化，待确认）"],
    [{ reason: "browser_or_share" }, "经中转（浏览器和分享连接只经中转）"],
    [{ reason: "proxy" }, "经中转（经代理连接服务器）"],
    [{ reason: "insecure" }, "经中转（与服务器的连接未验证证书）"],
    [{ reason: "offline" }, null],
    [{ mode: "direct", path: "direct" }, "直连"],
    [{ mode: "direct", reason: "nat_blocked" }, "直连不可用，当前经中转（当前网络无法直连）"],
    [{ mode: "direct", reason: "connect_failed" }, "直连不可用，当前经中转（直连建立失败）"],
    [{ mode: "direct", reason: "unstable" }, "直连不可用，当前经中转（直连不稳定）"],
    [{ mode: "direct", reason: "background" }, "直连不可用，当前经中转（应用在后台）"],
    [{ mode: "direct", reason: "server_disabled" }, "直连不可用，当前经中转（管理员已禁用直连）"],
    [{ mode: "direct", reason: "" }, "直连不可用，当前经中转"],
    [{ mode: "direct", reason: "some_future_code" }, "直连不可用，当前经中转"],
    [{ mode: "direct", path: "connecting_direct" }, "正在建立直连，当前经中转"],
    [{ mode: "direct", reason: "offline" }, null],
  ])("%o reads %s", (overrides, expected) => {
    expect(devicePathTooltip(pathStatus(overrides))).toBe(expected);
  });
});

describe("tab menu and tooltip of a device workspace", () => {
  it("offers 连接方式 on device tabs only, with the stored mode checked", async () => {
    mocks.status.mockResolvedValue(pathStatus({ mode: "relay", reason: "mode_relay" }));
    renderPane(DEVICE_WS);
    await flush();
    expect(mocks.status).toHaveBeenCalledWith("d-1");

    rightClick(tabElement(DEVICE_WS));
    const submenu = openModeSubmenu();
    expect(radios(submenu).map((radio) => radio.label)).toEqual(["自动", "中转", "直连"]);
    expect(checkedLabel(submenu)).toBe("中转");
    closeMenu();
    mounted?.unmount();

    renderPane(SSH_WS);
    rightClick(tabElement(SSH_WS));
    expect(menuItem("关闭标签页")).toBeDefined();
    expect(menuItem("连接方式")).toBeUndefined();
    expect(mocks.status).toHaveBeenCalledTimes(1);
  });

  it("sets the mode and shows the new selection", async () => {
    mocks.setMode.mockResolvedValue({ mode: "direct" });
    renderPane(DEVICE_WS);
    await flush();

    rightClick(tabElement(DEVICE_WS));
    let submenu = openModeSubmenu();
    expect(checkedLabel(submenu)).toBe("自动");
    const direct = radios(submenu).find((radio) => radio.label === "直连");
    if (!direct) throw new Error("直连 radio missing");
    click(direct.button);
    await flush();
    expect(mocks.setMode).toHaveBeenCalledWith("d-1", "direct");
    expect(document.querySelector(".nx-menu")).toBeNull();

    rightClick(tabElement(DEVICE_WS));
    submenu = openModeSubmenu();
    expect(checkedLabel(submenu)).toBe("直连");
    expect(useUi.getState().toasts).toEqual([]);
  });

  it("keeps the old selection and shows an error toast when setting fails", async () => {
    mocks.setMode.mockRejectedValue({ code: "internal", message: "资产选项写入失败" });
    renderPane(DEVICE_WS);
    await flush();

    rightClick(tabElement(DEVICE_WS));
    const relay = radios(openModeSubmenu()).find((radio) => radio.label === "中转");
    if (!relay) throw new Error("中转 radio missing");
    click(relay.button);
    await flush();

    const toasts = useUi.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0]?.kind).toBe("error");
    expect(toasts[0]?.text).toContain("设置连接方式失败");
    expect(toasts[0]?.text).toContain("资产选项写入失败");

    rightClick(tabElement(DEVICE_WS));
    expect(checkedLabel(openModeSubmenu())).toBe("自动");
  });

  it("keeps the tooltip line live from fleet://device-path without any toast", async () => {
    renderPane(DEVICE_WS);
    await flushUntil(() => tabElement(DEVICE_WS).title.includes("经中转"));
    expect(tabElement(DEVICE_WS).title).toBe("终端 1（经中转）");

    act(() => {
      for (const handler of mocks.pathHandlers) handler(pathStatus({ path: "direct", since: NOW }));
    });
    expect(tabElement(DEVICE_WS).title).toBe("终端 1（直连）");

    act(() => {
      for (const handler of mocks.pathHandlers) handler(pathStatus({ mode: "direct", path: "relay", reason: "unstable", since: NOW + 1 }));
    });
    expect(tabElement(DEVICE_WS).title).toBe("终端 1（直连不可用，当前经中转（直连不稳定））");
    // A late event about an older change does not roll the line back.
    act(() => {
      for (const handler of mocks.pathHandlers) handler(pathStatus({ path: "direct", since: NOW - 5000 }));
    });
    expect(tabElement(DEVICE_WS).title).toContain("直连不可用");
    expect(useUi.getState().toasts).toEqual([]);
  });

  it("shows no path line for another workspace's device or a disconnected device workspace", async () => {
    useUi.setState({ sessions: [session(DEVICE_WS, "disconnected"), session(SSH_WS)] });
    renderPane(DEVICE_WS);
    await flush();
    expect(mocks.status).not.toHaveBeenCalled();
    expect(tabElement(DEVICE_WS).title).toBe("终端 1");

    act(() => {
      for (const handler of mocks.pathHandlers) handler(pathStatus({ path: "direct", since: NOW }));
    });
    expect(tabElement(DEVICE_WS).title).toBe("终端 1");
  });

  it("drops the listener once no device workspace shows a path", async () => {
    renderPane(DEVICE_WS);
    await flush();
    expect(mocks.pathHandlers).toHaveLength(1);
    mounted?.unmount();
    mounted = undefined;
    expect(mocks.pathHandlers).toHaveLength(0);
  });
});

describe("connection tab of a device workspace", () => {
  it("adds the path to the workspace tab tooltip and offers 连接方式", async () => {
    mocks.status.mockResolvedValue(pathStatus({ reason: "server_disabled" }));
    mounted = mount(createElement(ProjectWorkspaceStrip, {
      workspaces: [DEVICE_WS],
      activeWorkspaceId: DEVICE_WS.id,
      activeProject: UNGROUPED_PROJECT,
      projectInventory: { groups: [], assets: [] },
      groups: [],
      onSelect: vi.fn(),
    }));
    const tab = () => document.querySelector<HTMLElement>(`[data-project-workspace-id="${DEVICE_WS.id}"]`);
    await flushUntil(() => Boolean(tab()?.title.includes("经中转")));
    expect(tab()?.title).toContain("经中转（管理员已禁用直连）");

    rightClick(tab() as HTMLElement);
    expect(checkedLabel(openModeSubmenu())).toBe("自动");
  });
});

describe("asset tree device row", () => {
  const DEVICE = {
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
      current_url: "",
      service_state: { installed: true, enabled: true, active: true },
      last_seen_at: NOW - 30_000,
    },
  };

  function row(): HTMLElement | null {
    return document.querySelector<HTMLElement>("[role='treeitem']");
  }

  it("reads the stored mode for its menu and shows the path only while the device workspace is open", async () => {
    mocks.devices.mockResolvedValue({ devices: [DEVICE] });
    mocks.getMode.mockResolvedValue({ mode: "direct" });
    useUi.setState({ workspaces: [], sessions: [] });
    mounted = mountWithClient(createElement(DeviceTreeSection));
    await flushUntil(() => row() !== null);
    expect(row()?.title).not.toContain("中转");
    expect(mocks.status).not.toHaveBeenCalled();

    rightClick(row() as HTMLElement);
    await flush();
    expect(mocks.getMode).toHaveBeenCalledWith("d-1");
    expect(checkedLabel(openModeSubmenu())).toBe("直连");
    closeMenu();

    act(() => useUi.setState({ workspaces: [DEVICE_WS], sessions: [session(DEVICE_WS)] }));
    await flushUntil(() => Boolean(row()?.title.includes("中转")));
    expect(row()?.title).toContain("web-01 · linux · 经中转");
  });
});

describe("连接诊断", () => {
  const FULL: DeviceP2PDiagnostics = {
    status: pathStatus({ path: "direct", mode: "auto" }),
    localNat: "independent",
    remoteNat: "dependent",
    stunReachable: true,
    selectedPair: { local: "192.0.2.10:50000", remote: "198.51.100.7:41000", type: "srflx" },
    directRttMs: 12.4,
    relayRttMs: 48,
    lossPct: 0.5,
    recentSwitches: [
      { at: NOW - 60_000, from: "relay", to: "direct", reason: "" },
      { at: NOW - 30_000, from: "direct", to: "relay", reason: "unstable" },
    ],
    policy: { server: true, device: true, mode: "auto" },
  };

  const MINIMAL: DeviceP2PDiagnostics = {
    status: pathStatus({ reason: "server_disabled" }),
    localNat: "unknown",
    remoteNat: "unknown",
    stunReachable: false,
    recentSwitches: [],
    policy: { server: false, device: true, mode: "auto" },
  };

  function card(): HTMLElement {
    const element = document.querySelector<HTMLElement>("[data-testid='host-path-diagnostics']");
    if (!element) throw new Error("diagnostics card missing");
    return element;
  }

  function value(label: string): string | undefined {
    const term = [...card().querySelectorAll("dt")].find((dt) => dt.textContent === label);
    return term?.nextElementSibling?.textContent ?? undefined;
  }

  it("renders every field of a full report", async () => {
    mocks.diagnostics.mockResolvedValue(FULL);
    mounted = mount(createElement(DeviceDiagnosticsSection, { deviceId: "d-1", visible: true }));
    await flushUntil(() => value("当前路径") !== undefined);
    expect(mocks.diagnostics).toHaveBeenCalledWith("d-1");
    expect(value("当前路径")).toBe("直连");
    expect(value("连接方式")).toBe("自动");
    expect(value("本机 NAT")).toBe("端点无关映射");
    expect(value("设备 NAT")).toBe("端点相关映射");
    expect(value("STUN")).toBe("可达");
    expect(value("直连 RTT")).toBe("12 ms");
    expect(value("中转 RTT")).toBe("48 ms");
    expect(value("丢包")).toBe("0.5%");
    expect(value("选中的候选对")).toBe("192.0.2.10:50000 ↔ 198.51.100.7:41000 · srflx");
    expect(value("策略")).toBe("服务器允许直连 · 设备允许直连 · 连接方式：自动");
    const switches = [...card().querySelectorAll("li")].map((item) => item.textContent ?? "");
    expect(switches).toHaveLength(2);
    expect(switches[0]).toContain("直连 → 中转");
    expect(switches[0]).toContain("直连不稳定");
    expect(switches[1]).toContain("中转 → 直连");
  });

  it("renders a minimal report with placeholders and refreshes on demand", async () => {
    mocks.diagnostics.mockResolvedValue(MINIMAL);
    mounted = mount(createElement(DeviceDiagnosticsSection, { deviceId: "d-1", visible: true }));
    await flushUntil(() => value("当前路径") !== undefined);
    expect(value("当前路径")).toBe("中转（管理员已禁用直连）");
    expect(value("本机 NAT")).toBe("未知");
    expect(value("STUN")).toBe("不可达");
    expect(value("直连 RTT")).toBe("—");
    expect(value("中转 RTT")).toBe("—");
    expect(value("丢包")).toBe("—");
    expect(value("选中的候选对")).toBe("—");
    expect(value("策略")).toContain("服务器已禁用直连");
    expect(card().textContent).toContain("暂无切换记录");

    mocks.diagnostics.mockResolvedValue(FULL);
    const refresh = [...card().querySelectorAll("button")].find((button) => button.textContent?.includes("刷新"));
    if (!refresh) throw new Error("refresh missing");
    click(refresh);
    await flushUntil(() => value("当前路径") === "直连");
    expect(mocks.diagnostics).toHaveBeenCalledTimes(2);
  });

  it("reads again when the device's path changes and shows a failed read inline", async () => {
    mocks.diagnostics.mockResolvedValue(MINIMAL);
    mounted = mount(createElement(DeviceDiagnosticsSection, { deviceId: "d-1", visible: true }));
    await flushUntil(() => value("当前路径") !== undefined && mocks.pathHandlers.length > 0);

    mocks.diagnostics.mockRejectedValue({ code: "internal", message: "设备未响应" });
    act(() => {
      for (const handler of mocks.pathHandlers) handler(pathStatus({ deviceId: "d-other", since: NOW }));
    });
    await flush();
    expect(mocks.diagnostics).toHaveBeenCalledTimes(1);

    act(() => {
      for (const handler of mocks.pathHandlers) handler(pathStatus({ path: "direct", since: NOW }));
    });
    await flushUntil(() => card().textContent?.includes("读取失败") ?? false);
    expect(card().textContent).toContain("设备未响应");
    expect(useUi.getState().toasts).toEqual([]);
  });

  it("does not read while hidden", async () => {
    mocks.diagnostics.mockResolvedValue(FULL);
    mounted = mount(createElement(DeviceDiagnosticsSection, { deviceId: "d-1", visible: false }));
    await flush();
    expect(mocks.diagnostics).not.toHaveBeenCalled();
  });
});
