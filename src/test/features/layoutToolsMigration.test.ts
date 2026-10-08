/** @vitest-environment jsdom */
// 工具工作区迁移: 多余的 tools 工作区(多端各自创建后汇入)必须合并成唯一一个;
// 迁入标签时 activeTabId 不得指向目标工作区其他 pane 的标签(跨 pane 悬空)。
import { describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  layoutApi: { get: vi.fn(), put: vi.fn() },
  terminalApi: { listLive: vi.fn() },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
}));
vi.mock("../../ipc/events", () => ({ listenEvent: vi.fn() }));

import { sanitizeLayout } from "../../app/layout";
import type { AppTab, Workspace } from "../../app/store";

function terminal(id: string): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s1", tabId: `k-${id}`, closable: true };
}
function tool(id: string, kind: AppTab["kind"]): AppTab {
  return { id, kind, title: id, closable: true };
}
function hostWorkspace(tabs: AppTab[], activeTabId: string | null): Workspace {
  return {
    id: "ws-host",
    kind: "session",
    title: "web-01",
    sessionId: "s1",
    panes: [{ id: "p1", tabs, activeTabId }],
    activePaneId: "p1",
    splitRatio: 0.5,
    closable: true,
  };
}
function toolsWorkspace(id: string, panes: Workspace["panes"]): Workspace {
  return {
    id,
    kind: "tools",
    title: "工具",
    panes,
    activePaneId: panes[panes.length - 1].id,
    splitRatio: 0.5,
    closable: true,
  };
}
function layout(workspaces: Workspace[], activeWorkspaceId: string | null) {
  return {
    v: 1,
    leftOpen: true,
    leftMode: "assets",
    rightOpen: true,
    leftWidth: 248,
    rightWidth: 352,
    workspaces,
    activeWorkspaceId,
  };
}

describe("migrateToolTabs 多余 tools 工作区", () => {
  it("两个 tools 工作区合并成一个: 标签按 id 去重, 活动工作区重映射到幸存者", () => {
    const parsed = sanitizeLayout(
      layout(
        [
          hostWorkspace([terminal("t1"), tool("s-tab", "settings")], "s-tab"),
          toolsWorkspace("ws-tools", [{ id: "tp1", tabs: [tool("audit-1", "audit")], activeTabId: "audit-1" }]),
          toolsWorkspace("ws-tools-2", [
            { id: "tp2", tabs: [tool("s-tab", "settings"), tool("devices-1", "devices")], activeTabId: "devices-1" },
          ]),
        ],
        "ws-tools-2",
      ),
    );

    const toolsList = parsed?.workspaces.filter((w) => w.kind === "tools") ?? [];
    expect(toolsList).toHaveLength(1);
    expect(toolsList[0].id).toBe("ws-tools");
    // 目标 pane 原有标签在前, 散入标签与并入标签按序去重追加
    expect(toolsList[0].panes[0].tabs.map((t) => t.id)).toEqual(["audit-1", "s-tab", "devices-1"]);
    // 散入工作区的活动标签优先成为合并后的活动标签
    expect(toolsList[0].panes[0].activeTabId).toBe("s-tab");
    // 原活动工作区是多余的 tools 工作区: 落到幸存的那个, 而不是任意第一个
    expect(parsed?.activeWorkspaceId).toBe("ws-tools");
    const host = parsed?.workspaces.find((w) => w.id === "ws-host");
    expect(host?.panes[0].tabs.map((t) => t.id)).toEqual(["t1"]);
    expect(host?.panes[0].activeTabId).toBe("t1");
  });

  it("合并结果再次 sanitize 保持不变(幂等)", () => {
    const first = sanitizeLayout(
      layout(
        [
          hostWorkspace([terminal("t1"), tool("s-tab", "settings")], "s-tab"),
          toolsWorkspace("ws-tools", [{ id: "tp1", tabs: [tool("audit-1", "audit")], activeTabId: "audit-1" }]),
          toolsWorkspace("ws-tools-2", [
            { id: "tp2", tabs: [tool("devices-1", "devices")], activeTabId: "devices-1" },
          ]),
        ],
        "ws-host",
      ),
    );
    const second = sanitizeLayout(JSON.parse(JSON.stringify(first)));
    expect(second?.workspaces).toEqual(first?.workspaces);
    expect(second?.activeWorkspaceId).toBe(first?.activeWorkspaceId);
  });
});

describe("migrateToolTabs 跨 pane activeTabId", () => {
  it("散入工作区的活动标签因 id 重复留在目标工作区其他 pane 时, 合并 pane 保留自己的活动标签", () => {
    const parsed = sanitizeLayout(
      layout(
        [
          hostWorkspace([terminal("t1"), tool("audit", "audit"), tool("settings-2", "settings")], "audit"),
          toolsWorkspace("ws-tools", [
            { id: "tp1", tabs: [tool("settings", "settings")], activeTabId: "settings" },
            { id: "tp2", tabs: [tool("audit", "audit")], activeTabId: "audit" },
          ]),
        ],
        "ws-tools",
      ),
    );

    const tools = parsed?.workspaces.find((w) => w.kind === "tools");
    expect(tools?.panes).toHaveLength(2);
    expect(tools?.panes[0].tabs.map((t) => t.id)).toEqual(["settings", "settings-2"]);
    // "audit" 是 tp2 的标签: tp1 的 activeTabId 不得跨 pane 指向它
    expect(tools?.panes[0].activeTabId).toBe("settings");
    expect(tools?.panes[1].activeTabId).toBe("audit");
    expect(parsed?.activeWorkspaceId).toBe("ws-tools");
  });

  it("散入工作区的活动标签确实迁入合并 pane 时成为活动标签", () => {
    const parsed = sanitizeLayout(
      layout(
        [
          hostWorkspace([terminal("t1"), tool("audit-9", "audit")], "audit-9"),
          toolsWorkspace("ws-tools", [{ id: "tp1", tabs: [tool("settings", "settings")], activeTabId: "settings" }]),
        ],
        "ws-host",
      ),
    );

    const tools = parsed?.workspaces.find((w) => w.kind === "tools");
    expect(tools?.panes[0].tabs.map((t) => t.id)).toEqual(["settings", "audit-9"]);
    expect(tools?.panes[0].activeTabId).toBe("audit-9");
  });
});
