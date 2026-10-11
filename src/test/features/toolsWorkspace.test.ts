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
            title: "终端 1",
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

describe("工具标签路由", () => {
  it("工具标签落入当前活动工作区, 终端标签保持可见, 不建「工具」工作区", () => {
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });

    const state = useUi.getState();
    const host = state.workspaces.find((w) => w.id === "ws-host");
    expect(host?.panes[0].tabs.map((t) => t.kind)).toEqual(["terminal", "settings"]);
    expect(host?.panes[0].activeTabId).toBe("settings");
    expect(state.workspaces.some((w) => w.kind === "tools")).toBe(false);
    expect(state.activeWorkspaceId).toBe("ws-host");
  });

  it("多个入口打开的工具标签收进当前工作区, sameTab 去重并聚焦", () => {
    useUi.getState().addTab({ id: "audit", kind: "audit", title: "审计日志", closable: true });
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
    useUi.getState().addTab({ id: "audit", kind: "audit", title: "审计日志", closable: true });

    const state = useUi.getState();
    const host = state.workspaces.find((w) => w.id === "ws-host");
    expect(host?.panes[0].tabs.map((t) => t.kind)).toEqual(["terminal", "audit", "settings"]);
    expect(host?.panes[0].activeTabId).toBe("audit");
    expect(state.workspaces).toHaveLength(1);
  });

  it("工具标签已在其它工作区打开时去重激活原标签, 不重复创建", () => {
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
    useUi.setState((st) => ({
      workspaces: [...st.workspaces, hostWorkspace("ws-host2", "s2")],
      activeWorkspaceId: "ws-host2",
    }));

    useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });

    const state = useUi.getState();
    expect(allTabs().filter((t) => t.id === "settings")).toHaveLength(1);
    const host = state.workspaces.find((w) => w.id === "ws-host");
    expect(host?.panes[0].activeTabId).toBe("settings");
    expect(state.activeWorkspaceId).toBe("ws-host");
    const host2 = state.workspaces.find((w) => w.id === "ws-host2");
    expect(host2?.panes[0].tabs.map((t) => t.kind)).toEqual(["terminal"]);
  });

  it("终端标签仍落在所属会话工作区, 不会误建工具工作区", () => {
    useUi.getState().addTab({
      id: "term-2",
      kind: "terminal",
      title: "终端 2",
      sessionId: "s1",
      tabId: "k2",
      closable: true,
    });

    const state = useUi.getState();
    const host = state.workspaces.find((w) => w.id === "ws-host");
    expect(host?.panes[0].tabs.map((t) => t.id)).toEqual(["ws-host-t1", "term-2"]);
    expect(state.workspaces.some((w) => w.kind === "tools")).toBe(false);
  });

  it("没有任何工作区时回退创建「工具」工作区", () => {
    useUi.setState({ workspaces: [], activeWorkspaceId: null });

    useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });

    const state = useUi.getState();
    expect(state.workspaces).toHaveLength(1);
    const tools = state.workspaces[0];
    expect(tools.kind).toBe("tools");
    expect(tools.title).toBe("工具");
    expect(tools.panes[0].tabs.map((t) => t.kind)).toEqual(["settings"]);
    expect(state.activeWorkspaceId).toBe(tools.id);
  });
});
