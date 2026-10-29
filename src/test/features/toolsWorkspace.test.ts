/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: { disconnect: vi.fn().mockResolvedValue(undefined) },
  layoutApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));
vi.mock("../../ipc/events", () => ({ listenEvent: vi.fn() }));
vi.mock("../../ui/dialogs", () => ({ ask: vi.fn(async () => true) }));

import {
  migrateToolTabs,
  openCredentialsTab,
  openFileTab,
  openFileTabInSplit,
  openLogTab,
  useUi,
  type AppTab,
  type SqlDraft,
  type Workspace,
} from "../../app/store";
import { sanitizeLayout, serializeLayout } from "../../app/layout";

function hostWorkspace(id: string, sessionId: string): Workspace {
  return {
    id,
    kind: "session",
    title: id,
    sessionId,
    closable: true,
    panes: [
      {
        id: `${id}-p1`,
        activeTabId: `${id}-t1`,
        tabs: [
          {
            id: `${id}-t1`,
            kind: "terminal",
            title: "Terminal 1",
            sessionId,
            tabId: `${id}-k1`,
            closable: true,
          },
        ],
      },
    ],
    activePaneId: `${id}-p1`,
  };
}

function toolsWorkspace(id: string, tabs: AppTab[] = []): Workspace {
  return {
    id,
    kind: "tools",
    title: "工具",
    panes: [{ id: `${id}-p1`, tabs, activeTabId: tabs[0]?.id ?? null }],
    activePaneId: `${id}-p1`,
    closable: true,
  };
}

function sqlDraft(text = "SELECT 1"): SqlDraft {
  return {
    text,
    selection: { from: 0, to: text.length },
    schema: "public",
    connId: "c1",
    dbKind: "postgres",
  };
}

function rebindWorkspace(): Workspace {
  const workspace = hostWorkspace("ws-rebind", "s-old");
  workspace.panes[0].tabs[0].containerId = "container-1";
  workspace.panes[0].tabs.push({
    id: "background-ws-rebind",
    kind: "background",
    title: "Background",
    sessionId: "s-old",
    closable: true,
  });
  workspace.panes.push({
    id: "ws-rebind-p2",
    activeTabId: "file-old",
    tabs: [
      { id: "file-old", kind: "files", title: "File", sessionId: "s-old", path: "/tmp/a", closable: true },
      { id: "log-old", kind: "log", title: "Log", sessionId: "s-old", path: "/tmp/a", closable: true },
      { id: "forward-old", kind: "forward", title: "Forward", sessionId: "s-old", closable: true },
      { id: "mount-old", kind: "mount", title: "Mount", sessionId: "s-old", closable: true },
      { id: "docker-old", kind: "docker", title: "Docker", sessionId: "s-old", closable: true },
      { id: "forward-overview", kind: "forward", title: "All forwards", closable: true },
    ],
  });
  workspace.activePaneId = "ws-rebind-p2";
  workspace.layoutTree = { type: "split" as const, id: "split-root", dir: "row" as const, ratio: 0.65, a: { type: "leaf" as const, id: "leaf-a", paneId: "ws-rebind-p1" }, b: { type: "leaf" as const, id: "leaf-b", paneId: "ws-rebind-p2" } };
  return workspace;
}

function databaseWorkspace(): Workspace {
  return {
    id: "ws-database",
    kind: "db",
    title: "Database",
    connId: "c1",
    panes: [
      {
        id: "ws-database-p1",
        activeTabId: "db-tab",
        tabs: [{ id: "db-tab", kind: "db", title: "DB", connId: "c1", closable: true }],
      },
    ],
    activePaneId: "ws-database-p1",
    closable: true,
  };
}

function expectReboundWorkspace(): void {
  const state = useUi.getState();
  const workspace = state.workspaces.find((w) => w.id === "ws-rebind");
  expect(workspace?.sessionId).toBe("s-new");
  const tabs = workspace?.panes.flatMap((pane) => pane.tabs) ?? [];
  expect(tabs).toHaveLength(8);
  for (const tab of tabs) {
    expect(tab.sessionId).toBe(tab.id === "forward-overview" ? undefined : "s-new");
  }
  expect(tabs[0]).toMatchObject({ tabId: "ws-rebind-k1", containerId: "container-1" });
  expect(workspace?.panes.map((pane) => pane.id)).toEqual(["ws-rebind-p1", "ws-rebind-p2"]);
  expect(workspace?.activePaneId).toBe("ws-rebind-p2");
  expect(workspace?.layoutTree).toEqual({ type: "split" as const, id: "split-root", dir: "row" as const, ratio: 0.65, a: { type: "leaf" as const, id: "leaf-a", paneId: "ws-rebind-p1" }, b: { type: "leaf" as const, id: "leaf-b", paneId: "ws-rebind-p2" } });
  const databaseTab = state.workspaces.find((w) => w.id === "ws-database")?.panes[0].tabs[0];
  expect(databaseTab?.connId).toBe("c1");
  expect(databaseTab?.sessionId).toBeUndefined();
}

function allTabs() {
  return useUi.getState().workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs));
}

beforeEach(() => {
  useUi.setState({
    workspaces: [hostWorkspace("ws-host", "s1")],
    activeWorkspaceId: "ws-host",
    sessions: [],
    aiBoards: {},
    workspaceRuntime: {},
    toasts: [],
  });
});

describe("tool tab routing", () => {
  it("routes every account tool kind into one Tools workspace", () => {
    const tabs: AppTab[] = [
      { id: "settings", kind: "settings", title: "Settings", closable: true },
      { id: "audit", kind: "audit", title: "Audit log", closable: true },
      { id: "devices", kind: "devices", title: "Devices", closable: true },
      { id: "history", kind: "history", title: "Terminal history", closable: true },
      { id: "tab-credentials", kind: "credentials", title: "Credentials", closable: true },
    ];
    for (const tab of tabs) useUi.getState().addTab(tab);

    const state = useUi.getState();
    const host = state.workspaces.find((w) => w.id === "ws-host");
    const tools = state.workspaces.filter((w) => w.kind === "tools");
    expect(host?.panes[0].tabs.map((t) => t.kind)).toEqual(["terminal"]);
    expect(tools).toHaveLength(1);
    expect(tools[0].title).toBe("工具");
    expect(tools[0].panes[0].tabs.map((t) => t.kind)).toEqual(tabs.map((t) => t.kind));
    expect(state.activeWorkspaceId).toBe(tools[0].id);
  });

  it("deduplicates tool tabs from multiple entry points and focuses the existing tab", () => {
    useUi.getState().addTab({ id: "audit", kind: "audit", title: "Audit log", closable: true });
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "Settings", closable: true });
    useUi.getState().addTab({ id: "audit", kind: "audit", title: "Audit log", closable: true });

    const state = useUi.getState();
    const tools = state.workspaces.find((w) => w.kind === "tools");
    expect(tools?.panes[0].tabs.map((t) => t.kind)).toEqual(["audit", "settings"]);
    expect(tools?.panes[0].activeTabId).toBe("audit");
    expect(allTabs().filter((t) => t.kind === "audit")).toHaveLength(1);
    expect(state.activeWorkspaceId).toBe(tools?.id);
  });

  it("migrates and deduplicates legacy tool tabs across session workspaces", () => {
    const first = hostWorkspace("ws-host", "s1");
    const second = hostWorkspace("ws-host2", "s2");
    first.panes[0].tabs.push({ id: "settings-a", kind: "settings", title: "Settings", closable: true });
    second.panes[0].tabs.push({ id: "settings-b", kind: "settings", title: "Settings", closable: true });
    first.panes[0].activeTabId = "settings-a";
    second.panes[0].activeTabId = "settings-b";
    useUi.setState({ workspaces: [first, second], activeWorkspaceId: second.id });

    useUi.getState().addTab({ id: "settings", kind: "settings", title: "Settings", closable: true });

    const state = useUi.getState();
    const tools = state.workspaces.find((w) => w.kind === "tools");
    expect(allTabs().filter((t) => t.kind === "settings")).toHaveLength(1);
    expect(tools?.panes[0].tabs.map((t) => t.id)).toEqual(["settings-a"]);
    expect(tools?.panes[0].activeTabId).toBe("settings-a");
    expect(state.activeWorkspaceId).toBe(tools?.id);
    for (const id of ["ws-host", "ws-host2"]) {
      const host = state.workspaces.find((w) => w.id === id);
      expect(host?.panes[0].tabs.map((t) => t.kind)).toEqual(["terminal"]);
      expect(host?.panes[0].activeTabId).toBe(`${id}-t1`);
    }
  });

  it("routes the credentials shortcut through migration without losing its selection", () => {
    const host = hostWorkspace("ws-host", "s1");
    host.panes[0].tabs.push({
      id: "tab-credentials",
      kind: "credentials",
      title: "Credentials",
      credId: "cred-old",
      closable: true,
    });
    host.panes[0].activeTabId = "tab-credentials";
    useUi.setState({ workspaces: [host], activeWorkspaceId: host.id });

    openCredentialsTab();
    let tools = useUi.getState().workspaces.find((w) => w.kind === "tools");
    expect(tools?.panes[0].tabs[0]).toMatchObject({ id: "tab-credentials", credId: "cred-old" });
    expect(useUi.getState().activeWorkspaceId).toBe(tools?.id);

    openCredentialsTab("cred-new");
    tools = useUi.getState().workspaces.find((w) => w.kind === "tools");
    expect(allTabs().filter((t) => t.kind === "credentials")).toHaveLength(1);
    expect(tools?.panes[0].tabs[0].credId).toBe("cred-new");
  });

  it("keeps one background tab per owning workspace across two hosts and repeated opens", () => {
    const first = hostWorkspace("ws-host", "s1");
    const second = hostWorkspace("ws-host2", "s2");
    first.assetId = "asset-1";
    second.assetId = "asset-2";
    useUi.setState({ workspaces: [first, second], activeWorkspaceId: second.id });
    const openBackground = (sessionId: string) => {
      useUi.getState().addTab({
        id: "background",
        kind: "background",
        title: "Background",
        sessionId,
        closable: true,
      });
    };

    openBackground("s1");
    openBackground("s2");
    openBackground("s1");

    const state = useUi.getState();
    const firstBackground = state.workspaces
      .find((w) => w.id === first.id)
      ?.panes.flatMap((p) => p.tabs)
      .filter((t) => t.kind === "background");
    const secondBackground = state.workspaces
      .find((w) => w.id === second.id)
      ?.panes.flatMap((p) => p.tabs)
      .filter((t) => t.kind === "background");
    expect(firstBackground).toHaveLength(1);
    expect(secondBackground).toHaveLength(1);
    expect(firstBackground?.[0]).toMatchObject({
      id: `background-${first.id}`,
      sessionId: "s1",
      assetId: "asset-1",
    });
    expect(secondBackground?.[0]).toMatchObject({
      id: `background-${second.id}`,
      sessionId: "s2",
      assetId: "asset-2",
    });
    expect(allTabs().filter((t) => t.kind === "background")).toHaveLength(2);
    expect(state.workspaces.some((w) => w.kind === "tools")).toBe(false);
    expect(state.activeWorkspaceId).toBe(first.id);
  });

  it("uses an explicit owning pane for background instead of the active workspace", () => {
    const first = hostWorkspace("ws-host", "s1");
    const second = hostWorkspace("ws-host2", "s2");
    first.assetId = "asset-1";
    second.assetId = "asset-2";
    useUi.setState({ workspaces: [first, second], activeWorkspaceId: second.id });

    useUi.getState().addTab(
      { id: "background", kind: "background", title: "Background", closable: true },
      first.panes[0].id,
    );

    const state = useUi.getState();
    expect(
      state.workspaces
        .find((w) => w.id === first.id)
        ?.panes.flatMap((p) => p.tabs)
        .filter((t) => t.kind === "background"),
    ).toEqual([
      expect.objectContaining({
        id: `background-${first.id}`,
        sessionId: "s1",
        assetId: "asset-1",
      }),
    ]);
    expect(
      state.workspaces
        .find((w) => w.id === second.id)
        ?.panes.flatMap((p) => p.tabs)
        .some((t) => t.kind === "background"),
    ).toBe(false);
    expect(state.workspaces.some((w) => w.kind === "tools")).toBe(false);
  });

  it("rejects a legacy background open without an explicit owner", () => {
    useUi.getState().addTab({ id: "background", kind: "background", title: "Background", closable: true });

    const state = useUi.getState();
    expect(allTabs().some((t) => t.kind === "background")).toBe(false);
    expect(state.workspaces).toHaveLength(1);
    expect(state.activeWorkspaceId).toBe("ws-host");
    expect(state.toasts.some((toast) => toast.text === "请先打开该主机的工作区，再查看它的后台会话")).toBe(true);
  });

  it("preserves history filters on ordinary opens and updates them only for an explicit source", () => {
    const first = hostWorkspace("ws-host", "s1");
    const second = hostWorkspace("ws-host2", "s2");
    first.assetId = "asset-1";
    second.assetId = "asset-2";
    useUi.setState({ workspaces: [first, second], activeWorkspaceId: first.id });
    useUi.getState().addTab({
      id: "history",
      kind: "history",
      title: "History",
      sessionId: "s1",
      assetId: "asset-1",
      closable: true,
    });
    useUi.getState().updateTab("history", { assetId: "asset-user" });

    useUi.getState().setActiveWorkspace(second.id);
    useUi.getState().addTab({ id: "history", kind: "history", title: "History", closable: true });

    let histories = allTabs().filter((t) => t.kind === "history");
    expect(histories).toHaveLength(1);
    expect(histories[0]).toMatchObject({ assetId: "asset-user" });
    expect(useUi.getState().workspaces.find((w) => w.kind === "tools")).toBeDefined();

    useUi.getState().setActiveWorkspace(first.id);
    useUi.getState().addTab({
      id: "history",
      kind: "history",
      title: "History",
      sessionId: "s2",
      assetId: "asset-2",
      closable: true,
    });

    histories = allTabs().filter((t) => t.kind === "history");
    expect(histories).toHaveLength(1);
    expect(histories[0]).toMatchObject({ sessionId: "s2", assetId: "asset-2" });
  });

  it("keeps session panels in their owning session workspace", () => {
    const tabs: AppTab[] = [
      { id: "term-2", kind: "terminal", title: "Terminal 2", sessionId: "s1", tabId: "k2", closable: true },
      { id: "forward-2", kind: "forward", title: "Port forwarding", sessionId: "s1", closable: true },
      { id: "files-2", kind: "files", title: "Files", sessionId: "s1", closable: true },
      { id: "log-2", kind: "log", title: "Log", sessionId: "s1", path: "/tmp/app.log", closable: true },
      { id: "mount-2", kind: "mount", title: "Mount", sessionId: "s1", closable: true },
      { id: "docker-2", kind: "docker", title: "Containers", sessionId: "s1", closable: true },
    ];
    for (const tab of tabs) useUi.getState().addTab(tab);

    const state = useUi.getState();
    const host = state.workspaces.find((w) => w.id === "ws-host");
    expect(host?.panes[0].tabs.map((t) => t.id)).toEqual(["ws-host-t1", ...tabs.map((t) => t.id)]);
    expect(state.workspaces.some((w) => w.kind === "tools")).toBe(false);
    expect(state.activeWorkspaceId).toBe("ws-host");
  });

  it("does not merge same-path file and log tabs across hosts", () => {
    const first = hostWorkspace("ws-host", "s1");
    const second = hostWorkspace("ws-host2", "s2");
    useUi.setState({ workspaces: [first, second], activeWorkspaceId: second.id });
    const path = "/var/log/app.log";

    openFileTab("s1", path);
    openFileTab("s2", path);
    openLogTab("s1", path);
    openLogTab("s2", path);
    openFileTab("s1", path);
    openLogTab("s1", path);

    for (const [workspaceId, sessionId] of [
      [first.id, "s1"],
      [second.id, "s2"],
    ] as const) {
      const tabs = useUi
        .getState()
        .workspaces.find((w) => w.id === workspaceId)
        ?.panes.flatMap((p) => p.tabs)
        .filter((t) => t.path === path);
      expect(tabs?.map((t) => t.kind)).toEqual(["files", "log"]);
      expect(tabs?.every((t) => t.sessionId === sessionId)).toBe(true);
    }
    expect(allTabs().filter((t) => t.path === path)).toHaveLength(4);
  });

  it("routes a scoped forward panel to its host and the global overview to Tools", () => {
    useUi.getState().addTab({
      id: "forward-scoped",
      kind: "forward",
      title: "Port forwarding",
      sessionId: "s1",
      closable: true,
    });
    useUi.getState().addTab({ id: "forward-overview", kind: "forward", title: "All forwards", closable: true });
    useUi.getState().addTab({ id: "forward-overview", kind: "forward", title: "All forwards", closable: true });

    const hostForwards = useUi
      .getState()
      .workspaces.find((w) => w.id === "ws-host")
      ?.panes.flatMap((p) => p.tabs)
      .filter((t) => t.kind === "forward");
    expect(hostForwards?.map((tab) => tab.id)).toEqual(["forward-scoped"]);
    const toolForwards = useUi
      .getState()
      .workspaces.find((w) => w.kind === "tools")
      ?.panes.flatMap((p) => p.tabs)
      .filter((t) => t.kind === "forward");
    expect(toolForwards?.map((tab) => tab.id)).toEqual(["forward-overview"]);
  });

  it("keeps database tabs in one connection workspace and deduplicates repeated opens", () => {
    useUi.getState().addTab({
      id: "db-1",
      kind: "db",
      title: "PostgreSQL",
      connId: "c1",
      dbKind: "postgres",
      closable: true,
    });
    useUi.getState().addTab({
      id: "db-2",
      kind: "db",
      title: "PostgreSQL",
      connId: "c1",
      dbKind: "postgres",
      closable: true,
    });

    const state = useUi.getState();
    const databases = state.workspaces.filter((w) => w.kind === "db");
    expect(databases).toHaveLength(1);
    expect(databases[0].panes[0].tabs.map((t) => t.id)).toEqual(["db-1"]);
    expect(state.activeWorkspaceId).toBe(databases[0].id);
  });

  it("preserves empty Tools panes when no migration is needed", () => {
    const tools: Workspace = {
      id: "tools",
      kind: "tools",
      title: "工具",
      panes: [
        {
          id: "tools-main",
          tabs: [{ id: "settings", kind: "settings", title: "Settings", closable: true }],
          activeTabId: "settings",
        },
        { id: "tools-empty", tabs: [], activeTabId: null },
      ],
      activePaneId: "tools-main",
      closable: true,
    };
    const workspaces = [tools];
    expect(migrateToolTabs(workspaces)).toBe(workspaces);

    useUi.setState({ workspaces, activeWorkspaceId: "tools" });
    useUi.getState().addTab({ id: "audit", kind: "audit", title: "Audit", closable: true });
    const current = useUi.getState().workspaces[0];
    expect(current.panes.map((pane) => pane.id)).toEqual(["tools-main", "tools-empty"]);
    expect(current.panes[1].tabs).toEqual([]);
  });

  it("creates a Tools workspace when none exists", () => {
    useUi.setState({ workspaces: [], activeWorkspaceId: null });

    useUi.getState().addTab({ id: "settings", kind: "settings", title: "Settings", closable: true });

    const state = useUi.getState();
    expect(state.workspaces).toHaveLength(1);
    const tools = state.workspaces[0];
    expect(tools.kind).toBe("tools");
    expect(tools.title).toBe("工具");
    expect(tools.panes[0].tabs.map((t) => t.kind)).toEqual(["settings"]);
    expect(state.activeWorkspaceId).toBe(tools.id);
  });

  it("keeps the global forward overview in Tools instead of the active host workspace", async () => {
    const host: Workspace = {
      id: "ws-host",
      kind: "session",
      sessionId: "s1",
      title: "Host",
      panes: [{ id: "p1", tabs: [], activeTabId: null }],
      activePaneId: "p1",
      closable: true,
    };
    useUi.setState({ workspaces: [host], activeWorkspaceId: host.id });

    useUi.getState().addTab({ id: "forward-overview", kind: "forward", title: "端口转发", closable: true });

    let state = useUi.getState();
    const tools = state.workspaces.find((workspace) => workspace.kind === "tools");
    expect(tools?.panes[0].tabs.map((tab) => tab.id)).toEqual(["forward-overview"]);
    expect(host.panes[0].tabs.some((tab) => tab.kind === "forward")).toBe(false);

    await state.closeWorkspace(host.id);
    state = useUi.getState();
    expect(state.workspaces.some((workspace) => workspace.id === host.id)).toBe(false);
    expect(state.workspaces.find((workspace) => workspace.kind === "tools")?.panes[0].tabs.map((tab) => tab.id)).toEqual(["forward-overview"]);
  });

  it("restores an active global forward overview to Tools instead of a scoped forward tab", () => {
    const parsed = sanitizeLayout({
      v: 4,
      leftOpen: true,
      leftMode: "assets",
      leftWidth: 280,
      rightWidth: 360,
      activeWorkspaceId: "host",
      aiBoards: {},
      workspaces: [
        {
          id: "host",
          kind: "session",
          sessionId: "s1",
          title: "Host",
          panes: [{
            id: "host-pane",
            tabs: [
              { id: "forward-scoped", kind: "forward", title: "Host forward", sessionId: "s1", closable: true },
              { id: "forward-overview", kind: "forward", title: "All forwards", closable: true },
            ],
            activeTabId: "forward-overview",
          }],
          activePaneId: "host-pane",
          closable: true,
        },
        {
          id: "tools",
          kind: "tools",
          title: "工具",
          panes: [{ id: "tools-pane", tabs: [], activeTabId: null }],
          activePaneId: "tools-pane",
          closable: true,
        },
      ],
    });

    expect(parsed).not.toBeNull();
    const host = parsed!.workspaces.find((workspace) => workspace.id === "host");
    const tools = parsed!.workspaces.find((workspace) => workspace.kind === "tools");
    expect(host?.panes[0].tabs.map((tab) => tab.id)).toEqual(["forward-scoped"]);
    expect(tools?.panes[0].tabs.map((tab) => tab.id)).toEqual(["forward-overview"]);
    expect(parsed!.activeWorkspaceId).toBe(tools?.id);
    expect(tools?.panes[0].activeTabId).toBe("forward-overview");
  });

  it("preserves the batch execution tool through layout serialization", () => {
    useUi.setState({ workspaces: [], activeWorkspaceId: null });
    useUi.getState().addTab({ id: "exec-batch", kind: "execBatch", title: "批量执行", closable: true });

    const persisted = serializeLayout(useUi.getState());
    expect(persisted?.workspaces[0]?.panes[0].tabs.map((tab) => tab.id)).toEqual(["exec-batch"]);
    const restored = sanitizeLayout(JSON.parse(JSON.stringify(persisted)));
    const tools = restored?.workspaces.find((workspace) => workspace.kind === "tools");
    expect(tools?.panes[0].tabs.map((tab) => tab.id)).toEqual(["exec-batch"]);
  });

  it("keeps one host detail tab in Tools that follows the last opened host and survives a layout round trip", () => {
    useUi.getState().addTab({ id: "host-detail", kind: "host", title: "主机详情", assetId: "a-1", closable: true });
    useUi.getState().addTab({ id: "host-detail", kind: "host", title: "主机详情", assetId: "device/d-1", closable: true });

    const state = useUi.getState();
    expect(state.workspaces.find((w) => w.id === "ws-host")?.panes[0].tabs.map((t) => t.kind)).toEqual(["terminal"]);
    const hostTabs = state.workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs)).filter((t) => t.kind === "host");
    expect(hostTabs.map((t) => t.assetId)).toEqual(["device/d-1"]);

    const restored = sanitizeLayout(JSON.parse(JSON.stringify(serializeLayout(useUi.getState()))));
    const tools = restored?.workspaces.find((workspace) => workspace.kind === "tools");
    expect(tools?.panes[0].tabs.map((tab) => [tab.kind, tab.assetId])).toEqual([["host", "device/d-1"]]);
  });
});

describe("workspace session rebinding and split routing", () => {
  function withSplitHeight(run: () => void): void {
    const descriptor = Object.getOwnPropertyDescriptor(window, "innerHeight");
    Object.defineProperty(window, "innerHeight", { configurable: true, writable: true, value: 844 });
    try {
      run();
    } finally {
      if (descriptor) Object.defineProperty(window, "innerHeight", descriptor);
    }
  }

  it("rebinds the workspace and every sibling through updateWorkspaceSession", () => {
    useUi.setState({
      workspaces: [rebindWorkspace(), databaseWorkspace()],
      activeWorkspaceId: "ws-rebind",
    });

    useUi.getState().updateWorkspaceSession("ws-rebind", "s-new");

    expectReboundWorkspace();
  });

  it("rebinds the workspace and every sibling through updateTab", () => {
    useUi.setState({
      workspaces: [rebindWorkspace(), databaseWorkspace()],
      activeWorkspaceId: "ws-rebind",
    });

    useUi.getState().updateTab("ws-rebind-t1", { sessionId: "s-new" });

    expectReboundWorkspace();
  });

  it("resolves the source workspace before creating a lower pane", () => {
    const first = hostWorkspace("ws-host", "s1");
    const second = hostWorkspace("ws-host2", "s2");
    useUi.setState({ workspaces: [first, second], activeWorkspaceId: second.id });

    withSplitHeight(() => openFileTabInSplit("s1", "/tmp/source.ts"));

    const state = useUi.getState();
    const source = state.workspaces.find((w) => w.id === first.id);
    const active = state.workspaces.find((w) => w.id === second.id);
    expect(source?.panes).toHaveLength(2);
    expect(source?.panes[0].tabs.some((t) => t.kind === "files")).toBe(false);
    expect(source?.panes[1].tabs).toEqual([
      expect.objectContaining({ kind: "files", sessionId: "s1", path: "/tmp/source.ts" }),
    ]);
    expect(source?.activePaneId).toBe(source?.panes[1].id);
    expect(active?.panes).toHaveLength(1);
    expect(active?.panes[0].tabs.map((t) => t.id)).toEqual(["ws-host2-t1"]);
  });

  it("rejects pane requests whose workspace does not own the tab", () => {
    const first = hostWorkspace("ws-host", "s1");
    const second = hostWorkspace("ws-host2", "s2");
    useUi.setState({ workspaces: [first, second], activeWorkspaceId: second.id });
    const before = useUi.getState().workspaces;
    const tab: AppTab = {
      id: "file-s1-/tmp/source.ts",
      kind: "files",
      title: "source.ts",
      sessionId: "s1",
      path: "/tmp/source.ts",
      closable: true,
    };

    withSplitHeight(() => {
      useUi.getState().openInLowerPane(tab, second.id);
      useUi.getState().addTab(tab, second.panes[0].id);
    });

    expect(useUi.getState().workspaces).toEqual(before);
    expect(allTabs().some((t) => t.kind === "files")).toBe(false);
  });

  it("does not create a workspace for an unknown session with a foreign pane", () => {
    const first = hostWorkspace("ws-host", "s1");
    const second = hostWorkspace("ws-host2", "s2");
    useUi.setState({ workspaces: [first, second], activeWorkspaceId: second.id });
    const before = useUi.getState().workspaces;
    const activeBefore = useUi.getState().activeWorkspaceId;
    const tab: AppTab = {
      id: "file-unknown-/tmp/source.ts",
      kind: "files",
      title: "source.ts",
      sessionId: "s-unknown",
      path: "/tmp/source.ts",
      closable: true,
    };

    useUi.getState().addTab(tab, first.panes[0].id);

    expect(useUi.getState().workspaces).toBe(before);
    expect(useUi.getState().activeWorkspaceId).toBe(activeBefore);
  });

  it("does not create a Tools workspace for a tool tab on a host pane", () => {
    const before = useUi.getState().workspaces;
    const activeBefore = useUi.getState().activeWorkspaceId;
    const paneId = before[0].panes[0].id;

    useUi.getState().addTab({ id: "settings", kind: "settings", title: "Settings", closable: true }, paneId);

    expect(useUi.getState().workspaces).toBe(before);
    expect(useUi.getState().activeWorkspaceId).toBe(activeBefore);
  });

  it("does not create a workspace for an unknown session with a foreign lower-pane target", () => {
    const first = hostWorkspace("ws-host", "s1");
    const second = hostWorkspace("ws-host2", "s2");
    useUi.setState({ workspaces: [first, second], activeWorkspaceId: second.id });
    const before = useUi.getState().workspaces;
    const activeBefore = useUi.getState().activeWorkspaceId;
    const tab: AppTab = {
      id: "file-unknown-/tmp/source.ts",
      kind: "files",
      title: "source.ts",
      sessionId: "s-unknown",
      path: "/tmp/source.ts",
      closable: true,
    };

    withSplitHeight(() => useUi.getState().openInLowerPane(tab, first.id));

    expect(useUi.getState().workspaces).toBe(before);
    expect(useUi.getState().activeWorkspaceId).toBe(activeBefore);
  });
});

describe("AI board-aware tool migration", () => {
  it("preserves a boarded duplicate Tools workspace through addTab and repeated serialization", () => {
    const board = {
      tabs: [{ id: "ai-board-tab", title: "Investigation", conversationId: "conv-1" }],
      activeTabId: "ai-board-tab",
    };
    const primary = toolsWorkspace("tools-primary", [
      { id: "settings", kind: "settings", title: "Settings", closable: true },
    ]);
    const boarded = toolsWorkspace("tools-board", [
      { id: "audit", kind: "audit", title: "Audit", closable: true },
    ]);
    const plain = toolsWorkspace("tools-plain", [
      { id: "devices", kind: "devices", title: "Devices", closable: true },
    ]);
    useUi.setState({
      workspaces: [primary, boarded, plain],
      activeWorkspaceId: boarded.id,
      aiBoards: { [boarded.id]: board },
    });

    useUi.getState().addTab({ id: "history", kind: "history", title: "History", closable: true });

    const first = serializeLayout(useUi.getState());
    const second = serializeLayout(JSON.parse(JSON.stringify(first)));
    for (const dto of [first, second]) {
      expect(dto).not.toBeNull();
      const ids = dto?.workspaces.map((w) => w.id) ?? [];
      expect(ids).toContain(primary.id);
      expect(ids).toContain(boarded.id);
      expect(ids).not.toContain(plain.id);
      expect(dto?.aiBoards[boarded.id]).toEqual(board);
      const tabs = dto?.workspaces.flatMap((w) => w.panes.flatMap((pane) => pane.tabs)) ?? [];
      for (const kind of ["settings", "audit", "devices", "history"]) {
        expect(tabs.filter((tab) => tab.kind === kind)).toHaveLength(1);
      }
      const shell = dto?.workspaces.find((w) => w.id === boarded.id);
      expect(shell?.panes.every((pane) => pane.tabs.length === 0)).toBe(true);
    }
  });
});

describe("workspace SQL runtime", () => {
  it("keeps a draft across workspace switches without serializing it", () => {
    const database = databaseWorkspace();
    const other = hostWorkspace("ws-other", "s2");
    useUi.setState({ workspaces: [database, other], activeWorkspaceId: database.id });
    const draft = sqlDraft();
    useUi.getState().updateSqlDraft(database.id, draft);
    const generation = useUi.getState().getWorkspaceRuntime(database.id)?.generation;

    useUi.getState().setActiveWorkspace(other.id);

    expect(useUi.getState().getWorkspaceRuntime(database.id)).toEqual({ generation, sqlDraft: draft });
    const serialized = JSON.stringify(serializeLayout(useUi.getState()));
    expect(serialized).not.toContain("workspaceRuntime");
    expect(serialized).not.toContain(draft.text);

    useUi.getState().clearSqlDraft(database.id, generation);
    expect(useUi.getState().getWorkspaceRuntime(database.id)).toEqual({ generation, sqlDraft: undefined });
  });

  it("rejects stale draft writes after closing and does not recreate runtime", async () => {
    const database = databaseWorkspace();
    useUi.setState({ workspaces: [database], activeWorkspaceId: database.id });
    useUi.getState().updateSqlDraft(database.id, sqlDraft("old"));
    const generation = useUi.getState().getWorkspaceRuntime(database.id)?.generation;

    await useUi.getState().closeWorkspace(database.id);

    expect(useUi.getState().workspaces).toHaveLength(0);
    expect(useUi.getState().getWorkspaceRuntime(database.id)).toBeUndefined();
    useUi.getState().updateSqlDraft(database.id, sqlDraft("late"), generation);
    expect(useUi.getState().getWorkspaceRuntime(database.id)).toBeUndefined();
    expect(useUi.getState().workspaces).toHaveLength(0);

    useUi.setState({ workspaces: [database], activeWorkspaceId: database.id, workspaceRuntime: {} });
    useUi.getState().updateSqlDraft(database.id, sqlDraft("stale"), generation);
    expect(useUi.getState().getWorkspaceRuntime(database.id)).toBeUndefined();

    useUi.getState().updateSqlDraft(database.id, sqlDraft("fresh"));
    const runtime = useUi.getState().getWorkspaceRuntime(database.id);
    expect(runtime?.generation).not.toBe(generation);
    expect(runtime?.sqlDraft?.text).toBe("fresh");
  });
});
