/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));

import { migrateToolTabs, openCredentialsTab, useUi, type AppTab, type Workspace } from "../../app/store";

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
    splitRatio: 0.5,
  };
}

function allTabs() {
  return useUi.getState().workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs));
}

beforeEach(() => {
  useUi.setState({
    workspaces: [hostWorkspace("ws-host", "s1")],
    activeWorkspaceId: "ws-host",
    sessions: [],
    toasts: [],
  });
});

describe("tool tab routing", () => {
  it("routes every global tool kind into one Tools workspace", () => {
    const tabs: AppTab[] = [
      { id: "settings", kind: "settings", title: "Settings", closable: true },
      { id: "audit", kind: "audit", title: "Audit log", closable: true },
      { id: "devices", kind: "devices", title: "Devices", closable: true },
      { id: "background", kind: "background", title: "Background sessions", closable: true },
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

  it("keeps session panels in their owning session workspace", () => {
    const tabs: AppTab[] = [
      { id: "term-2", kind: "terminal", title: "Terminal 2", sessionId: "s1", tabId: "k2", closable: true },
      { id: "forward-2", kind: "forward", title: "Port forwarding", sessionId: "s1", closable: true },
      { id: "files-2", kind: "files", title: "Files", sessionId: "s1", closable: true },
      { id: "docker-2", kind: "docker", title: "Containers", sessionId: "s1", closable: true },
    ];
    for (const tab of tabs) useUi.getState().addTab(tab);

    const state = useUi.getState();
    const host = state.workspaces.find((w) => w.id === "ws-host");
    expect(host?.panes[0].tabs.map((t) => t.id)).toEqual(["ws-host-t1", ...tabs.map((t) => t.id)]);
    expect(state.workspaces.some((w) => w.kind === "tools")).toBe(false);
    expect(state.activeWorkspaceId).toBe("ws-host");
  });

  it("updates background and history source scope on repeated opens", () => {
    const first = hostWorkspace("ws-host", "s1");
    const second = hostWorkspace("ws-host2", "s2");
    first.assetId = "asset-1";
    second.assetId = "asset-2";
    useUi.setState({ workspaces: [first, second], activeWorkspaceId: first.id });
    const openTabs = (workspaceId: string) => {
      useUi.getState().setActiveWorkspace(workspaceId);
      useUi.getState().addTab({ id: "background", kind: "background", title: "Background", closable: true });
      useUi.getState().setActiveWorkspace(workspaceId);
      useUi.getState().addTab({ id: "history", kind: "history", title: "History", closable: true });
    };

    openTabs(first.id);
    expect(allTabs().find((t) => t.kind === "background")).toMatchObject({
      sessionId: "s1",
      assetId: "asset-1",
    });
    expect(allTabs().find((t) => t.kind === "history")).toMatchObject({
      sessionId: "s1",
      assetId: "asset-1",
    });

    openTabs(second.id);
    expect(allTabs().filter((t) => t.kind === "background")).toHaveLength(1);
    expect(allTabs().filter((t) => t.kind === "history")).toHaveLength(1);
    expect(allTabs().find((t) => t.kind === "background")).toMatchObject({
      sessionId: "s2",
      assetId: "asset-2",
    });
    expect(allTabs().find((t) => t.kind === "history")).toMatchObject({
      sessionId: "s2",
      assetId: "asset-2",
    });
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
      splitRatio: 0.5,
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
});
