/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { deferred } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoice: vi.fn(),
  askChoiceRemember: vi.fn(),
  closeTab: vi.fn(),
  listLive: vi.fn(),
  sessionList: vi.fn(),
  sessionConnect: vi.fn(),
  sessionReconnect: vi.fn(),
  assetGet: vi.fn(),
  dbDisconnect: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({
  ask: mocks.ask,
  askChoice: mocks.askChoice,
  askChoiceRemember: mocks.askChoiceRemember,
  promptText: vi.fn(),
}));
vi.mock("../../ipc/commands", () => ({
  assetApi: { get: mocks.assetGet, list: vi.fn(async () => []) },
  dbApi: { disconnect: mocks.dbDisconnect },
  sessionApi: {
    list: mocks.sessionList,
    connect: mocks.sessionConnect,
    reconnect: mocks.sessionReconnect,
    disconnect: vi.fn(async () => undefined),
  },
  terminalApi: {
    closeTab: mocks.closeTab,
    listLive: mocks.listLive,
    listDurable: vi.fn(async () => []),
  },
  vaultApi: {},
  layoutApi: {},
}));

import {
  closedTabEntryFromTab,
  useClosedTabs,
  type ClosedTabEntry,
} from "../../app/closedTabs";
import { serializeLayout } from "../../app/layout";
import {
  reconnectRestoredTab,
  restoreClosedTab,
  useUi,
  type AppTab,
  type Pane,
  type Workspace,
} from "../../app/store";
import { setTerminalCloseAction } from "../../app/preferences";
import type { SessionInfo } from "../../ipc/commands";

const ACCOUNT_A = "account-a";
const ACCOUNT_B = "account-b";
const storageKey = (accountId: string) => `nexterm:closed-tabs-v1:${encodeURIComponent(accountId)}`;

function sessionInfo(overrides: Partial<SessionInfo> = {}): SessionInfo {
  return { id: "s1", assetId: "asset-1", name: "session", kind: "ssh", status: "connected", tabs: [], createdAt: 0, ...overrides };
}

function terminalTab(id: string, overrides: Partial<AppTab> = {}): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s1", assetId: "asset-1", tabId: `kernel-${id}`, closable: true, retainable: true, ...overrides };
}

function fileTab(id: string, overrides: Partial<AppTab> = {}): AppTab {
  return { id, kind: "files", title: id, sessionId: "s1", path: `/a/${id}.txt`, closable: true, ...overrides };
}

function workspace(id: string, tabs: AppTab[], overrides: Partial<Workspace> = {}): Workspace {
  const pane: Pane = { id: `${id}-pane`, tabs, activeTabId: tabs[tabs.length - 1]?.id ?? null };
  return {
    id,
    kind: "session",
    title: id,
    sessionId: "s1",
    assetId: "asset-1",
    panes: [pane],
    activePaneId: pane.id,
    splitRatio: 0.5,
    closable: true,
    ...overrides,
  };
}

function seedWorkspace(tabs: AppTab[], overrides: Partial<Workspace> = {}): Workspace {
  const ws = workspace("ws-1", tabs, overrides);
  useUi.setState({ workspaces: [ws], activeWorkspaceId: ws.id });
  return ws;
}

function stackEntries() {
  return useClosedTabs.getState().stack.entries;
}

function findTab(id: string): AppTab | undefined {
  return useUi
    .getState()
    .workspaces.flatMap((w) => w.panes)
    .flatMap((p) => p.tabs)
    .find((t) => t.id === id);
}

function captureEntry(tab: AppTab, options?: { workspaceId?: string; detached?: boolean }): ClosedTabEntry {
  const entry = closedTabEntryFromTab(tab, options);
  if (!entry) throw new Error("expected a restorable entry");
  return entry;
}

beforeEach(() => {
  vi.clearAllMocks();
  window.localStorage.clear();
  setTerminalCloseAction("detach");
  mocks.ask.mockResolvedValue(true);
  mocks.askChoiceRemember.mockResolvedValue({ key: "detach", remember: false });
  mocks.closeTab.mockResolvedValue(undefined);
  mocks.listLive.mockResolvedValue([]);
  mocks.sessionList.mockImplementation(async () => useUi.getState().sessions);
  mocks.sessionConnect.mockResolvedValue(sessionInfo({ id: "s2" }));
  mocks.assetGet.mockResolvedValue({ id: "asset-1", name: "主机", kind: "ssh", credId: null });
  useUi.setState({ workspaces: [], activeWorkspaceId: null, sessions: [sessionInfo()], toasts: [] });
  useClosedTabs.getState().resetForTest();
  useClosedTabs.getState().ensureAccount(ACCOUNT_A);
});

describe("closeTab captures restorable entries", () => {
  it("captures a detached remote terminal as a detached-terminal entry that can reattach later", async () => {
    seedWorkspace([terminalTab("t1")]);

    await expect(useUi.getState().closeTab("t1", "detach")).resolves.toBe(true);

    expect(findTab("t1")).toBeUndefined();
    expect(stackEntries()).toHaveLength(1);
    expect(stackEntries()[0]).toMatchObject({ kind: "detached-terminal", tabId: "kernel-t1", sessionId: "s1", workspaceId: "ws-1" });
  });

  it("defaults a running remote terminal close to detach and captures it as detached", async () => {
    seedWorkspace([terminalTab("t1")]);

    await useUi.getState().closeTab("t1");

    expect(stackEntries()[0]?.kind).toBe("detached-terminal");
  });

  it("captures a killed terminal as a plain terminal entry without the live tabId", async () => {
    seedWorkspace([terminalTab("t1")]);

    await useUi.getState().closeTab("t1", "kill");

    expect(stackEntries()[0]?.kind).toBe("terminal");
    expect(stackEntries()[0]?.tabId).toBeUndefined();
  });

  it("never captures a dead or exited terminal as detachable even when detach is requested", async () => {
    seedWorkspace([terminalTab("t1", { dead: true })]);
    await useUi.getState().closeTab("t1", "detach");
    expect(stackEntries()[0]?.kind).toBe("terminal");
    expect(stackEntries()[0]?.tabId).toBeUndefined();

    useClosedTabs.getState().resetForTest();
    useClosedTabs.getState().ensureAccount(ACCOUNT_A);
    seedWorkspace([terminalTab("t2", { exited: true })]);
    await useUi.getState().closeTab("t2", "detach");
    expect(stackEntries()[0]?.kind).toBe("terminal");
    expect(stackEntries()[0]?.tabId).toBeUndefined();
  });

  it("captures a file tab with its path and captures nothing for unsupported kinds", async () => {
    seedWorkspace([fileTab("f1"), { id: "cfg", kind: "settings", title: "设置", closable: true }]);

    await useUi.getState().closeTab("f1");
    await useUi.getState().closeTab("cfg");

    expect(stackEntries()).toHaveLength(1);
    expect(stackEntries()[0]).toMatchObject({ kind: "file", path: "/a/f1.txt", sessionId: "s1" });
  });

  it("does not capture when the terminal close fails and the tab stays open", async () => {
    seedWorkspace([terminalTab("t1")]);
    mocks.closeTab.mockRejectedValue({ code: "disconnected", message: "boom" });

    await expect(useUi.getState().closeTab("t1", "detach")).resolves.toBe(false);

    expect(findTab("t1")).toBeDefined();
    expect(stackEntries()).toEqual([]);
  });

  it("records a remote default close answered with not_found as a plain terminal, never a live detached entry", async () => {
    seedWorkspace([terminalTab("t1")]);
    mocks.closeTab.mockRejectedValue({ code: "not_found", message: "gone" });

    await expect(useUi.getState().closeTab("t1")).resolves.toBe(true);

    expect(findTab("t1")).toBeUndefined();
    expect(stackEntries()[0]?.kind).toBe("terminal");
    expect(stackEntries()[0]?.tabId).toBeUndefined();
  });

  it("records an explicit detach answered with not_found as a plain terminal, not a detached-terminal", async () => {
    seedWorkspace([terminalTab("t1")]);
    mocks.closeTab.mockRejectedValue({ code: "not_found", message: "gone" });

    await expect(useUi.getState().closeTab("t1", "detach")).resolves.toBe(true);

    expect(stackEntries()[0]?.kind).toBe("terminal");
    expect(stackEntries()[0]?.tabId).toBeUndefined();
  });

  it("dedupes a duplicate close of the same logical target and moves it to the top", async () => {
    seedWorkspace([terminalTab("t1"), fileTab("f1")]);
    await useUi.getState().closeTab("t1", "kill");
    await useUi.getState().closeTab("f1");
    expect(stackEntries().map((entry) => entry.kind)).toEqual(["file", "terminal"]);

    await restoreClosedTab();
    expect(findTab("f1")).toBeDefined();
    await useUi.getState().closeTab("f1");
    expect(stackEntries()).toHaveLength(2);
    expect(stackEntries()[0]?.kind).toBe("file");
    expect(stackEntries()[0]?.seq).toBeGreaterThan(stackEntries()[1]?.seq ?? 0);
  });

  it("evicts the oldest entry beyond the 20-entry bound", async () => {
    const tabs = Array.from({ length: 21 }, (_, index) => fileTab(`f${index}`));
    seedWorkspace(tabs);

    for (const tab of tabs) await useUi.getState().closeTab(tab.id);

    expect(stackEntries()).toHaveLength(20);
    expect(stackEntries().some((entry) => entry.id === "f0")).toBe(false);
    expect(stackEntries()[0]?.id).toBe("f20");
  });
});

describe("closeWorkspace captures the remaining restorable tabs", () => {
  it("captures detached terminals via closeTab and file/background tabs on workspace deletion", async () => {
    seedWorkspace([
      terminalTab("t1"),
      fileTab("f1"),
      { id: "background-ws-1", kind: "background", title: "后台会话", sessionId: "s1", closable: true },
    ]);

    await expect(useUi.getState().closeWorkspace("ws-1")).resolves.toBe(true);

    expect(useUi.getState().workspaces).toEqual([]);
    const detached = stackEntries().find((entry) => entry.kind === "detached-terminal" && entry.tabKind === "terminal");
    expect(detached?.tabId).toBe("kernel-t1");
    expect(stackEntries().some((entry) => entry.kind === "file" && entry.path === "/a/f1.txt")).toBe(true);
    const background = stackEntries().find((entry) => entry.tabKind === "background");
    expect(background?.kind).toBe("detached-terminal");
  });

  it("captures nothing when the workspace close is canceled", async () => {
    seedWorkspace([terminalTab("t1"), fileTab("f1")]);
    setTerminalCloseAction("ask");
    mocks.askChoiceRemember.mockResolvedValueOnce(null);

    await expect(useUi.getState().closeWorkspace("ws-1")).resolves.toBe(false);

    expect(useUi.getState().workspaces).toHaveLength(1);
    expect(stackEntries()).toEqual([]);
  });
});

describe("restoreClosedTab applies the reviewed restore modes", () => {
  it("reattaches a detached terminal to the still-running process without marking it pending", async () => {
    seedWorkspace([terminalTab("t1")]);
    await useUi.getState().closeTab("t1", "detach");

    await expect(restoreClosedTab()).resolves.toBe(true);

    const restored = findTab("t1");
    expect(restored?.kind).toBe("terminal");
    expect(restored?.tabId).toBe("kernel-t1");
    expect(restored?.restorePending).toBeUndefined();
    expect(stackEntries()).toEqual([]);
  });

  it("refuses to reattach when the owning session is not connected and keeps the entry", async () => {
    seedWorkspace([terminalTab("t1")]);
    await useUi.getState().closeTab("t1", "detach");
    useUi.setState({ sessions: [sessionInfo({ status: "disconnected" })] });

    await expect(restoreClosedTab()).resolves.toBe(false);

    expect(stackEntries()).toHaveLength(1);
    expect(useUi.getState().toasts.some((toast) => toast.text.includes("未连接"))).toBe(true);
  });

  it("restores a killed terminal as a pending tab that never carries a live tabId", async () => {
    seedWorkspace([terminalTab("t1")]);
    await useUi.getState().closeTab("t1", "kill");

    await expect(restoreClosedTab()).resolves.toBe(true);

    const restored = findTab("t1");
    expect(restored?.restorePending).toBe(true);
    expect(restored?.tabId).toBeUndefined();
  });

  it("reopens a file tab with its path and a pending marker instead of a live connection", async () => {
    seedWorkspace([fileTab("f1")]);
    await useUi.getState().closeTab("f1");

    await expect(restoreClosedTab()).resolves.toBe(true);

    const restored = findTab("f1");
    expect(restored?.kind).toBe("files");
    expect(restored?.path).toBe("/a/f1.txt");
    expect(restored?.restorePending).toBe(true);
    expect(restored?.tabId).toBeUndefined();
  });

  it("restores into the original workspace even when another workspace is active", async () => {
    const ws1 = workspace("ws-1", []);
    const ws2 = workspace("ws-2", [fileTab("f1")], { sessionId: "s2", assetId: "asset-2" });
    useUi.setState({
      workspaces: [ws1, ws2],
      activeWorkspaceId: ws1.id,
      sessions: [sessionInfo(), sessionInfo({ id: "s2", assetId: "asset-2" })],
    });

    await useUi.getState().closeTab("f1");
    expect(stackEntries()[0]?.workspaceId).toBe("ws-2");

    await restoreClosedTab();

    const owner = useUi.getState().workspaces.find((w) => w.id === "ws-2");
    expect(owner?.panes[0]?.tabs.some((tab) => tab.id === "f1")).toBe(true);
  });

  it("pins a reattach to the session the process is bound to, never to a re-bound workspace", async () => {
    seedWorkspace([terminalTab("t1")]);
    await useUi.getState().closeTab("t1", "detach");
    expect(stackEntries()[0]?.sessionId).toBe("s1");

    // The original workspace rebinds to a new session; both sessions stay connected.
    useUi.getState().updateWorkspaceSession("ws-1", "s2");
    useUi.setState({ sessions: [sessionInfo(), sessionInfo({ id: "s2", assetId: "asset-2" })] });

    await expect(restoreClosedTab()).resolves.toBe(true);

    const restored = findTab("t1");
    expect(restored?.sessionId).toBe("s1");
    expect(restored?.tabId).toBe("kernel-t1");
    const owner = useUi.getState().workspaces.find((w) => w.panes.some((p) => p.tabs.some((t) => t.id === "t1")));
    expect(owner?.sessionId).toBe("s1");
    expect(owner?.id).not.toBe("ws-1");
    expect(useUi.getState().workspaces.find((w) => w.id === "ws-1")?.sessionId).toBe("s2");
  });

  it("keeps the entry retryable when the restored tab cannot be placed", async () => {
    seedWorkspace([terminalTab("t1")]);
    await useUi.getState().closeTab("t1", "kill");
    const addTab = useUi.getState().addTab;
    useUi.setState({ addTab: () => {} });
    try {
      await expect(restoreClosedTab()).resolves.toBe(false);
    } finally {
      useUi.setState({ addTab });
    }

    expect(stackEntries()).toHaveLength(1);
    expect(stackEntries()[0]?.kind).toBe("terminal");
  });

  it("returns false when the stack is empty", async () => {
    await expect(restoreClosedTab()).resolves.toBe(false);
  });
});

describe("reconnectRestoredTab reconnects only on explicit request", () => {
  it("clears the pending marker on a live session so the pane attaches on demand", async () => {
    seedWorkspace([terminalTab("t1", { tabId: undefined, restorePending: true })]);

    const outcome = await reconnectRestoredTab("t1");

    expect(outcome.ok).toBe(true);
    expect(findTab("t1")?.restorePending).toBeUndefined();
  });

  it("keeps the pending marker and reports the deleted-target guard", async () => {
    seedWorkspace([terminalTab("t1", { tabId: undefined, restorePending: true })], { deletedTarget: true });

    const outcome = await reconnectRestoredTab("t1");

    expect(outcome.ok).toBe(false);
    expect(!outcome.ok && outcome.error).toContain("已删除");
    expect(findTab("t1")?.restorePending).toBe(true);
  });

  it("reconnects a dead host through the existing workspace reconnect flow", async () => {
    seedWorkspace([terminalTab("t1", { tabId: undefined, restorePending: true })], { sessionId: "gone" });
    useUi.setState({ sessions: [] });

    const outcome = await reconnectRestoredTab("t1");

    expect(outcome.ok).toBe(true);
    expect(mocks.sessionConnect).toHaveBeenCalledWith("asset-1");
    const restored = findTab("t1");
    expect(restored?.restorePending).toBeUndefined();
    expect(restored?.sessionId).toBe("s2");
    expect(useUi.getState().sessions.some((session) => session.id === "s2")).toBe(true);
  });
});

describe("per-account persistence and generation isolation", () => {
  it("persists the stack per account and restores it when switching back", async () => {
    seedWorkspace([fileTab("f1")]);
    await useUi.getState().closeTab("f1");
    expect(window.localStorage.getItem(storageKey(ACCOUNT_A))).toContain("f1");

    useClosedTabs.getState().ensureAccount(ACCOUNT_B);
    expect(stackEntries()).toEqual([]);
    expect(useClosedTabs.getState().stack.accountId).toBe(ACCOUNT_B);
    expect(useClosedTabs.getState().stack.generation).toBeGreaterThan(0);

    useClosedTabs.getState().ensureAccount(ACCOUNT_A);
    expect(stackEntries().map((entry) => entry.id)).toEqual(["f1"]);
  });

  it("rejects a capture that lands after an account switch (generation isolation)", async () => {
    seedWorkspace([terminalTab("t1")]);
    const gate = deferred<void>();
    mocks.closeTab.mockReturnValue(gate.promise);

    const closing = useUi.getState().closeTab("t1", "detach");
    useClosedTabs.getState().ensureAccount(ACCOUNT_B);
    gate.resolve();
    await expect(closing).resolves.toBe(true);

    expect(stackEntries()).toEqual([]);
    expect(window.localStorage.getItem(storageKey(ACCOUNT_B))).toBeNull();
    const persistedA = window.localStorage.getItem(storageKey(ACCOUNT_A));
    expect(persistedA === null || !persistedA.includes("kernel-t1")).toBe(true);
  });

  it("degrades to an empty stack for corrupt persisted state", () => {
    window.localStorage.setItem(storageKey(ACCOUNT_A), "{not json");
    useClosedTabs.getState().resetForTest();

    useClosedTabs.getState().ensureAccount(ACCOUNT_A);

    expect(stackEntries()).toEqual([]);
    expect(useClosedTabs.getState().stack.accountId).toBe(ACCOUNT_A);
  });

  it("drops invalid and oversized persisted entries on load", () => {
    const valid = (index: number) => ({
      key: `file:files:s1:/a/f${index}.txt`,
      kind: "file",
      tabKind: "files",
      id: `f${index}`,
      title: `f${index}`,
      sessionId: "s1",
      path: `/a/f${index}.txt`,
      seq: index + 1,
    });
    const payload = {
      v: 1,
      accountId: ACCOUNT_A,
      generation: 2,
      nextSeq: 999,
      entries: [
        ...Array.from({ length: 25 }, (_, index) => valid(24 - index)),
        { key: "", kind: "terminal", tabKind: "terminal", id: "x", title: "x", seq: 99 },
        "garbage",
      ],
    };
    window.localStorage.setItem(storageKey(ACCOUNT_A), JSON.stringify(payload));
    useClosedTabs.getState().resetForTest();

    useClosedTabs.getState().ensureAccount(ACCOUNT_A);

    expect(stackEntries()).toHaveLength(20);
    expect(stackEntries()[0]?.id).toBe("f24");
  });
});

describe("moveWorkspace wires drag reorder into layout persistence", () => {
  it("reorders the workspaces array and keeps the new order in the serialized layout", () => {
    const ws1 = workspace("ws-1", []);
    const ws2 = workspace("ws-2", [], { sessionId: "s2" });
    const ws3 = workspace("ws-3", [], { sessionId: "s3" });
    useUi.setState({ workspaces: [ws1, ws2, ws3], activeWorkspaceId: "ws-2" });

    useUi.getState().moveWorkspace(0, 2);

    expect(useUi.getState().workspaces.map((w) => w.id)).toEqual(["ws-2", "ws-3", "ws-1"]);
    expect(useUi.getState().activeWorkspaceId).toBe("ws-2");
    const layout = serializeLayout(useUi.getState());
    expect(layout?.workspaces.map((w) => w.id)).toEqual(["ws-2", "ws-3", "ws-1"]);
    expect(layout?.activeWorkspaceId).toBe("ws-2");
  });

  it("returns the same array for no-op moves", () => {
    const ws1 = workspace("ws-1", []);
    const ws2 = workspace("ws-2", [], { sessionId: "s2" });
    useUi.setState({ workspaces: [ws1, ws2], activeWorkspaceId: null });
    const before = useUi.getState().workspaces;

    useUi.getState().moveWorkspace(1, 1);
    expect(useUi.getState().workspaces).toBe(before);

    useUi.getState().moveWorkspace(5, 0);
    expect(useUi.getState().workspaces).toBe(before);
  });
});

describe("restore entry capture guard", () => {
  it("capture rejects entries from a foreign generation and accepts current ones", () => {
    const generation = useClosedTabs.getState().stack.generation;
    const entry = captureEntry(fileTab("f1"), { workspaceId: "ws-1" });

    expect(useClosedTabs.getState().capture(entry, generation + 1)).toBe(false);
    expect(stackEntries()).toEqual([]);
    expect(useClosedTabs.getState().capture(entry, generation)).toBe(true);
    expect(stackEntries()).toHaveLength(1);
  });
});
