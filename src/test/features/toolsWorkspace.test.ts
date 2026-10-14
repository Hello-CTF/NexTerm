/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));

import { useUi, type Workspace } from "../../app/store";

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
  it("places tool tabs in the active workspace, keeps terminal tabs visible, and creates no Tools workspace", () => {
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "Settings", closable: true });

    const state = useUi.getState();
    const host = state.workspaces.find((w) => w.id === "ws-host");
    expect(host?.panes[0].tabs.map((t) => t.kind)).toEqual(["terminal", "settings"]);
    expect(host?.panes[0].activeTabId).toBe("settings");
    expect(state.workspaces.some((w) => w.kind === "tools")).toBe(false);
    expect(state.activeWorkspaceId).toBe("ws-host");
  });

  it("collects tool tabs from multiple entry points in the current workspace, deduplicates with sameTab, and focuses", () => {
    useUi.getState().addTab({ id: "audit", kind: "audit", title: "Audit log", closable: true });
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "Settings", closable: true });
    useUi.getState().addTab({ id: "audit", kind: "audit", title: "Audit log", closable: true });

    const state = useUi.getState();
    const host = state.workspaces.find((w) => w.id === "ws-host");
    expect(host?.panes[0].tabs.map((t) => t.kind)).toEqual(["terminal", "audit", "settings"]);
    expect(host?.panes[0].activeTabId).toBe("audit");
    expect(state.workspaces).toHaveLength(1);
  });

  it("activates an existing tool tab in another workspace instead of creating a duplicate", () => {
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "Settings", closable: true });
    useUi.setState((st) => ({
      workspaces: [...st.workspaces, hostWorkspace("ws-host2", "s2")],
      activeWorkspaceId: "ws-host2",
    }));

    useUi.getState().addTab({ id: "settings", kind: "settings", title: "Settings", closable: true });

    const state = useUi.getState();
    expect(allTabs().filter((t) => t.id === "settings")).toHaveLength(1);
    const host = state.workspaces.find((w) => w.id === "ws-host");
    expect(host?.panes[0].activeTabId).toBe("settings");
    expect(state.activeWorkspaceId).toBe("ws-host");
    const host2 = state.workspaces.find((w) => w.id === "ws-host2");
    expect(host2?.panes[0].tabs.map((t) => t.kind)).toEqual(["terminal"]);
  });

  it("keeps terminal tabs in their owning session workspace instead of creating a tools workspace", () => {
    useUi.getState().addTab({
      id: "term-2",
      kind: "terminal",
      title: "Terminal 2",
      sessionId: "s1",
      tabId: "k2",
      closable: true,
    });

    const state = useUi.getState();
    const host = state.workspaces.find((w) => w.id === "ws-host");
    expect(host?.panes[0].tabs.map((t) => t.id)).toEqual(["ws-host-t1", "term-2"]);
    expect(state.workspaces.some((w) => w.kind === "tools")).toBe(false);
  });

  it("falls back to creating a Tools workspace when none exists", () => {
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
