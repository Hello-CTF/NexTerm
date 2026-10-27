/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  listLive: vi.fn(),
  listDurable: vi.fn(),
  listSessions: vi.fn(),
  closeTab: vi.fn(),
  resyncSessions: vi.fn(),
  helperStatus: vi.fn(),
  connectDevice: vi.fn(),
  connectLocal: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, askChoiceRemember: vi.fn() }));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    sessionApi: { list: mocks.listSessions, connectDevice: mocks.connectDevice, connectLocal: mocks.connectLocal },
    terminalApi: { listLive: mocks.listLive, listDurable: mocks.listDurable, closeTab: mocks.closeTab },
    localHelperApi: { status: mocks.helperStatus },
  });
});

import { BackgroundSessions } from "../../features/terminal/BackgroundSessions";
import { aggregateTerminatePath, groupBackgroundTabs, unknownHostText, type BackgroundTab } from "../../features/terminal/backgroundSessionsAccess";
import {
  applyAggregateList,
  applyAggregateUpdate,
  aggregateRows,
  forgetAggregateSession,
  resetSessionAggregateForTest,
  useSessionAggregate,
} from "../../app/sessionAggregate";
import { useAuth } from "../../features/auth/store";
import { setLocalHelperStatusForTest } from "../../features/terminal/localHelperStatus";
import { useUi } from "../../app/store";
import { flush, mount, waitFor, type MountedView } from "./reactTestUtils";
import type { SessionInfo } from "../../ipc/commands";
import type { AggregateHostStatus, AggregateSession } from "../../ipc/generated/types";

const ssh: SessionInfo = { id: "s1", assetId: "asset-one", name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 0 };
// A device session this client holds without a workspace: the cross-host listing names it by device.
const deviceSession: SessionInfo = { id: "s-dev", assetId: "device/near", name: "near box", kind: "device", status: "connected", tabs: [], createdAt: 0 };

function host(key: string, patch: Partial<AggregateHostStatus>): AggregateHostStatus {
  return { key, kind: "device", name: key, status: "ok", fetchedAt: 1, ...patch };
}

function item(hostKey: string, tabId: string, state = "needs_me"): AggregateSession {
  return {
    hostKey, tabId, title: `任务-${tabId}`, cols: 80, rows: 24,
    attention: { tabId, state, confidence: "signal", source: "helper", since: "2026-10-27T10:00:00Z", version: 1 },
  };
}

function seed() {
  useUi.setState({
    workspaces: [{ id: "tools", kind: "tools", title: "工具", panes: [{ id: "p", tabs: [], activeTabId: null }], activePaneId: "p", closable: true }],
    activeWorkspaceId: "tools",
    sessions: [ssh, deviceSession],
    toasts: [],
    resyncSessions: mocks.resyncSessions,
  });
  applyAggregateList({
    hosts: [
      host("device:near", { name: "near box", deviceId: "near" }),
      host("device:far", { name: "far box", deviceId: "far" }),
      host("asset:gone", { kind: "ssh", name: "db-02", sessionId: "s-gone" }),
      host("device:off", { name: "offline box", deviceId: "off", status: "offline", fetchedAt: undefined }),
    ],
    sessions: [item("device:near", "near-tab"), item("device:far", "far-tab", "running"), item("asset:gone", "gone-tab", "just_finished")],
    complete: true,
  });
}

function row(view: MountedView, tabId: string): HTMLTableRowElement {
  const found = view.container.querySelector<HTMLTableRowElement>(`tbody tr[data-background-row="${tabId}"]`);
  if (!found) throw new Error(`row ${tabId} not found`);
  return found;
}

function button(target: ParentNode, label: string): HTMLButtonElement {
  const found = [...target.querySelectorAll<HTMLButtonElement>("button")].find((candidate) => candidate.textContent?.trim() === label);
  if (!found) throw new Error(`${label} button not found`);
  return found;
}

async function mountAllHosts(): Promise<MountedView> {
  const view = mount(createElement(BackgroundSessions, {}));
  await flush();
  await waitFor(() => expect(view.container.querySelectorAll("tbody tr[data-background-row]")).toHaveLength(3));
  return view;
}

describe("BackgroundSessions across hosts", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    window.localStorage.clear();
    useAuth.setState({ user: null });
    resetSessionAggregateForTest();
    mocks.listLive.mockResolvedValue([]);
    mocks.listDurable.mockResolvedValue([]);
    mocks.listSessions.mockResolvedValue([ssh, deviceSession]);
    mocks.closeTab.mockResolvedValue(undefined);
    mocks.resyncSessions.mockResolvedValue(undefined);
    mocks.helperStatus.mockResolvedValue({ state: "disabled" });
    setLocalHelperStatusForTest(null);
  });

  afterEach(() => {
    resetSessionAggregateForTest();
    setLocalHelperStatusForTest(null);
  });

  it("groups sessions by host and shows an offline host as unknown", async () => {
    seed();
    const view = await mountAllHosts();
    try {
      const groups = [...view.container.querySelectorAll<HTMLTableRowElement>("tr[data-background-group]")];
      expect(groups.map((tr) => [tr.dataset.backgroundGroup, tr.dataset.hostStatus])).toEqual([
        ["device:near", "ok"], ["device:far", "ok"], ["asset:gone", "ok"], ["device:off", "offline"],
      ]);
      expect(groups[3].textContent).toContain("会话状态未知");
      expect(row(view, "near-tab").querySelector("[data-attention]")?.getAttribute("data-attention")).toBe("needs_me");
      expect(row(view, "far-tab").querySelector("[data-attention]")?.getAttribute("data-attention")).toBe("running");
    } finally {
      view.unmount();
    }
  });

  it("ends a session through the host connection this client holds, after a confirmation that the process ends", async () => {
    seed();
    mocks.ask.mockResolvedValue(true);
    const view = await mountAllHosts();
    try {
      await act(async () => {
        button(row(view, "near-tab"), "结束进程").click();
        await flush();
      });
      expect(mocks.ask).toHaveBeenCalledTimes(1);
      const [message, options] = mocks.ask.mock.calls[0] as [string, unknown];
      expect(message).toContain("程序会被终止");
      expect(message).toContain("无法恢复");
      expect(options).toEqual({ title: "结束进程", kind: "warning" });
      expect(mocks.closeTab).toHaveBeenCalledWith("near-tab", "kill", "s-dev");
      expect(aggregateRows(useSessionAggregate.getState()).some((entry) => entry.session.tabId === "near-tab")).toBe(false);
    } finally {
      view.unmount();
    }
  });

  it("connects a device the user agreed to connect, then ends the session through it without moving the focus", async () => {
    seed();
    mocks.ask.mockResolvedValue(true);
    const far: SessionInfo = { id: "s-far", assetId: "device/far", name: "far box", kind: "device", status: "connected", tabs: [], createdAt: 0 };
    mocks.connectDevice.mockResolvedValue(far);
    const view = await mountAllHosts();
    try {
      const before = useUi.getState().activeWorkspaceId;
      await act(async () => {
        button(row(view, "far-tab"), "结束进程").click();
        await flush();
      });
      await waitFor(() => expect(mocks.closeTab).toHaveBeenCalledWith("far-tab", "kill", "s-far"));
      expect(mocks.connectDevice).toHaveBeenCalledWith("far", "far box");
      expect(useUi.getState().activeWorkspaceId).toBe(before);
      expect(useUi.getState().toasts.map((toast) => toast.text)).toContain("已结束进程");
    } finally {
      view.unmount();
    }
  });

  it("ends a local-helper session through this device's local session", async () => {
    seed();
    applyAggregateList({
      hosts: [host("local", { kind: "local", name: "本机" })],
      sessions: [item("local", "local-tab", "running")],
      complete: true,
    });
    mocks.ask.mockResolvedValue(true);
    mocks.connectLocal.mockResolvedValue({ id: "s-local", assetId: null, name: "本机", kind: "local", status: "connected", tabs: [], createdAt: 0 });
    const view = mount(createElement(BackgroundSessions, {}));
    try {
      await flush();
      await waitFor(() => expect(view.container.querySelector('tr[data-background-row="local-tab"]')).not.toBeNull());
      await act(async () => {
        button(row(view, "local-tab"), "结束进程").click();
        await flush();
      });
      await waitFor(() => expect(mocks.closeTab).toHaveBeenCalledWith("local-tab", "kill", "s-local"));
      expect(mocks.connectLocal).toHaveBeenCalledTimes(1);
    } finally {
      view.unmount();
    }
  });

  it("keeps the row and reports the error when ending fails", async () => {
    seed();
    mocks.ask.mockResolvedValue(true);
    mocks.closeTab.mockRejectedValue(new Error("helper gone"));
    const view = await mountAllHosts();
    try {
      await act(async () => {
        button(row(view, "near-tab"), "结束进程").click();
        await flush();
      });
      await waitFor(() => expect(useUi.getState().toasts.some((toast) => toast.text.startsWith("结束进程失败"))).toBe(true));
      expect(row(view, "near-tab")).toBeTruthy();
      expect(view.container.querySelectorAll('tr[data-background-row="near-tab"]')).toHaveLength(1);
      expect(aggregateRows(useSessionAggregate.getState()).some((entry) => entry.session.tabId === "near-tab")).toBe(true);
    } finally {
      view.unmount();
    }
  });

  it("does not open a device when the confirmed connection was lost before ending", async () => {
    seed();
    let confirm: (value: boolean) => void = () => {};
    mocks.ask.mockReturnValue(new Promise<boolean>((resolve) => { confirm = resolve; }));
    const view = await mountAllHosts();
    try {
      await act(async () => {
        button(row(view, "near-tab"), "结束进程").click();
        await flush();
      });
      // The device connection goes away while the confirmation is open.
      act(() => useUi.setState({ sessions: [ssh] }));
      await act(async () => {
        confirm(true);
        await flush();
      });
      expect(mocks.connectDevice).not.toHaveBeenCalled();
      expect(mocks.closeTab).not.toHaveBeenCalled();
      expect(useUi.getState().toasts.some((toast) => toast.text.startsWith("结束进程失败"))).toBe(true);
      expect(row(view, "near-tab")).toBeTruthy();
    } finally {
      view.unmount();
    }
  });

  it("does nothing when the confirmation is declined", async () => {
    seed();
    mocks.ask.mockResolvedValue(false);
    const view = await mountAllHosts();
    try {
      await act(async () => {
        button(row(view, "far-tab"), "结束进程").click();
        await flush();
      });
      // The device has no connection here; the confirmation says it is connected first.
      expect(mocks.ask.mock.calls[0][0]).toContain("需要先连接设备「far box」");
      expect(mocks.closeTab).not.toHaveBeenCalled();
      expect(row(view, "far-tab")).toBeTruthy();
    } finally {
      view.unmount();
    }
  });

  it("never dials an SSH host to end a session; the user connects it first", async () => {
    seed();
    const view = await mountAllHosts();
    try {
      const end = button(row(view, "gone-tab"), "结束进程");
      expect(end.disabled).toBe(true);
      expect(end.title).toContain("请先从资产树连接这台主机");
      expect(mocks.ask).not.toHaveBeenCalled();
    } finally {
      view.unmount();
    }
  });
});

describe("cross-host grouping and terminate paths", () => {
  it("orders groups as the listing does and keeps unnamed hosts after them", () => {
    const hosts = new Map([
      ["device:a", host("device:a", { name: "A" })],
      ["device:b", host("device:b", { name: "B", status: "pending", fetchedAt: undefined })],
    ]);
    const tab = (tabId: string, patch: Partial<BackgroundTab>): BackgroundTab => ({
      tabId, sessionId: "", sessionName: "", sessionKind: "device", cols: 80, rows: 24, sizeOwner: null, subscribers: 0, viewers: 0, exited: false, ...patch,
    });
    const groups = groupBackgroundTabs([
      tab("x", { sessionId: "s9", sessionName: "other", sessionKind: "ssh" }),
      tab("y", { aggregateHost: hosts.get("device:a") }),
    ], hosts);
    expect(groups.map((group) => [group.key, group.tabs.map((entry) => entry.tabId)])).toEqual([
      ["device:a", ["y"]], ["device:b", []], ["session:s9", ["x"]],
    ]);
  });

  it("keeps an ended session out of replies and events read before it ended", () => {
    resetSessionAggregateForTest();
    applyAggregateList({ hosts: [host("device:a", {})], sessions: [item("device:a", "gone"), item("device:a", "kept")], complete: true });
    forgetAggregateSession("gone");
    const tabs = () => aggregateRows(useSessionAggregate.getState()).map((entry) => entry.session.tabId);
    expect(tabs()).toEqual(["kept"]);
    applyAggregateUpdate({ host: host("device:a", { answeredAt: 5 }), sessions: [item("device:a", "gone"), item("device:a", "kept")] });
    expect(tabs()).toEqual(["kept"]);
    applyAggregateList({ hosts: [host("device:a", { answeredAt: 9 })], sessions: [item("device:a", "gone"), item("device:a", "kept")], complete: true });
    expect(tabs()).toEqual(["kept"]);
    resetSessionAggregateForTest();
  });

  it("explains a host whose helper is not running", () => {
    expect(unknownHostText({ status: "offline", errorCode: "helper_not_ready" })).toContain("在这台主机上打开一个终端");
    expect(unknownHostText({ status: "offline" })).toBe("未连接，会话状态未知");
    expect(unknownHostText({ status: "failed", error: "超时" })).toBe("无法读取，会话状态未知：超时");
  });

  it("ends through a held session, a device connection, or not at all", () => {
    const sessions = [ssh, deviceSession];
    expect(aggregateTerminatePath({ key: "asset:one", kind: "ssh", name: "web", sessionId: "s1" }, sessions)).toBe("direct");
    expect(aggregateTerminatePath({ key: "device:near", kind: "device", name: "n", deviceId: "near" }, sessions)).toBe("direct");
    expect(aggregateTerminatePath({ key: "device:far", kind: "device", name: "f", deviceId: "far" }, sessions)).toBe("connect_device");
    expect(aggregateTerminatePath({ key: "asset:gone", kind: "ssh", name: "g", sessionId: "s-gone" }, sessions)).toBeNull();
    // The local helper is reached through a local session, with no network connection.
    expect(aggregateTerminatePath({ key: "local", kind: "local", name: "本机" }, sessions)).toBe("direct");
  });
});
