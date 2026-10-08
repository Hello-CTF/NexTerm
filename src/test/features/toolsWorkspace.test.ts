/** @vitest-environment jsdom */
// 主机优先单层: 全局工具标签统一落入唯一的「工具」工作区, 不寄生主机工作区。
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));

import { useUi, type Workspace } from "../../app/store";

function hostWorkspace(): Workspace {
  return {
    id: "ws-host",
    kind: "session",
    title: "web-01",
    sessionId: "s1",
    closable: true,
    panes: [
      {
        id: "p1",
        activeTabId: "t1",
        tabs: [
          { id: "t1", kind: "terminal", title: "终端 1", sessionId: "s1", tabId: "k1", closable: true },
        ],
      },
    ],
    activePaneId: "p1",
    splitRatio: 0.5,
  };
}

beforeEach(() => {
  useUi.setState({
    workspaces: [hostWorkspace()],
    activeWorkspaceId: "ws-host",
    sessions: [],
    toasts: [],
  });
});

describe("工具工作区路由", () => {
  it("全局工具标签落入唯一的「工具」工作区并成为活动工作区", () => {
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });

    const workspaces = useUi.getState().workspaces;
    const tools = workspaces.find((w) => w.kind === "tools");
    expect(tools).toBeTruthy();
    expect(tools?.title).toBe("工具");
    expect(tools?.panes[0].tabs.map((t) => t.kind)).toEqual(["settings"]);
    expect(useUi.getState().activeWorkspaceId).toBe(tools?.id);

    const host = workspaces.find((w) => w.id === "ws-host");
    expect(host?.panes[0].tabs.map((t) => t.kind)).toEqual(["terminal"]);
  });

  it("多个入口打开的工具标签收进同一个工作区, sameTab 去重并聚焦", () => {
    useUi.getState().addTab({ id: "audit", kind: "audit", title: "审计日志", closable: true });
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
    useUi.getState().addTab({ id: "audit", kind: "audit", title: "审计日志", closable: true });

    const toolsList = useUi.getState().workspaces.filter((w) => w.kind === "tools");
    expect(toolsList).toHaveLength(1);
    expect(toolsList[0].panes.flatMap((p) => p.tabs).map((t) => t.kind)).toEqual([
      "audit",
      "settings",
    ]);
    expect(toolsList[0].panes[0].activeTabId).toBe("audit");
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

    const host = useUi.getState().workspaces.find((w) => w.id === "ws-host");
    expect(host?.panes[0].tabs.map((t) => t.id)).toEqual(["t1", "term-2"]);
    expect(useUi.getState().workspaces.some((w) => w.kind === "tools")).toBe(false);
  });
});
