/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  assetGet: vi.fn(),
  sessionList: vi.fn(),
  sessionConnect: vi.fn(),
  terminalListLive: vi.fn(),
  closeTab: vi.fn(),
  dbConnect: vi.fn(),
  dbDisconnect: vi.fn(),
  ask: vi.fn(),
  askChoice: vi.fn(),
  promptText: vi.fn(),
}));

const envState = vi.hoisted(() => ({
  WEB: false,
  TRANSPORT: "desktop" as "desktop" | "web",
}));

vi.mock("../ipc/env", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/env")>();
  return {
    ...actual,
    get WEB() {
      return envState.WEB;
    },
    get TRANSPORT() {
      return envState.TRANSPORT;
    },
  };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    assetApi: { ...actual.assetApi, get: mocks.assetGet },
    sessionApi: { ...actual.sessionApi, list: mocks.sessionList, connect: mocks.sessionConnect },
    terminalApi: { ...actual.terminalApi, listLive: mocks.terminalListLive, closeTab: mocks.closeTab },
    dbApi: { ...actual.dbApi, connect: mocks.dbConnect, disconnect: mocks.dbDisconnect },
  };
});

vi.mock("../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, askChoice: mocks.askChoice, promptText: mocks.promptText };
});

import {
  applyLayoutPreset,
  captureWorkspacePreset,
  choosePresetTargetAndApply,
  deleteLayoutPreset,
  sanitizeLayoutPresets,
  saveLayoutPresetFromWorkspace,
  type LayoutPreset,
} from "../app/layoutPresets";
import { sanitizeLayout, serializeLayout } from "../app/layout";
import { useUi, type Workspace } from "../app/store";
import type { SessionInfo } from "../ipc/commands";

const SESSION_S1: SessionInfo = {
  id: "s1",
  assetId: "a1",
  name: "web-01",
  kind: "ssh",
  status: "connected",
  tabs: [],
  createdAt: 0,
};

function presetOf(extra?: Partial<LayoutPreset>): LayoutPreset {
  return {
    id: "p1",
    name: "开发环境",
    createdAt: 1,
    updatedAt: 1,
    workspace: {
      kind: "session",
      title: "web-01",
      sessionId: "s1",
      assetId: "a1",
      assetKind: "ssh",
      splitRatio: 0.5,
      panes: [
        { tabs: [{ kind: "terminal", title: "终端 1", sessionId: "s1", tabId: "T1" }], activeTabIndex: 0 },
        { tabs: [{ kind: "files", title: "文件", sessionId: "s1" }], activeTabIndex: 0 },
      ],
      activePaneIndex: 1,
    },
    ...extra,
  };
}

function workspaceOf(panes: Workspace["panes"], extra?: Partial<Workspace>): Workspace {
  return {
    id: "ws1",
    kind: "session",
    title: "web-01",
    sessionId: "s1",
    assetId: "a1",
    assetKind: "ssh",
    panes,
    activePaneId: panes[0].id,
    splitRatio: 0.5,
    closable: true,
    ...extra,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  useUi.setState({
    workspaces: [],
    activeWorkspaceId: null,
    layoutPresets: [],
    sessions: [],
    toasts: [],
  });
  mocks.sessionList.mockResolvedValue([]);
  mocks.terminalListLive.mockResolvedValue([]);
  mocks.sessionConnect.mockResolvedValue(undefined);
  mocks.ask.mockResolvedValue(true);
  mocks.askChoice.mockResolvedValue(null);
});

describe("sanitizeLayoutPresets", () => {
  it("保留合法预设并丢弃非法条目", () => {
    const out = sanitizeLayoutPresets([
      presetOf(),
      { id: "p2", name: "", workspace: presetOf().workspace },
      { id: "p3", name: "空面板", workspace: { kind: "session", panes: [] } },
      {
        id: "p4",
        name: "工具标签",
        workspace: {
          kind: "session",
          panes: [{ tabs: [{ kind: "settings", title: "设置" }], activeTabIndex: 0 }],
        },
      },
      {
        id: "p5",
        name: "终端缺会话",
        workspace: {
          kind: "session",
          panes: [{ tabs: [{ kind: "terminal", title: "t" }], activeTabIndex: 0 }],
        },
      },
      "junk",
    ]);
    expect(out.map((p) => p.id)).toEqual(["p1"]);
  });

  it("非数组输入归为空列表", () => {
    expect(sanitizeLayoutPresets(undefined)).toEqual([]);
    expect(sanitizeLayoutPresets({})).toEqual([]);
  });
});

describe("布局 blob 中的预设", () => {
  const base = {
    v: 1,
    leftOpen: true,
    leftMode: "assets",
    rightOpen: true,
    leftWidth: 248,
    rightWidth: 352,
    workspaces: [],
    activeWorkspaceId: null,
  };

  it("sanitizeLayout 缺省 presets 为空数组", () => {
    expect(sanitizeLayout(base)?.presets).toEqual([]);
  });

  it("sanitizeLayout 保留合法 presets", () => {
    const parsed = sanitizeLayout({ ...base, presets: [presetOf()] });
    expect(parsed?.presets).toHaveLength(1);
    expect(parsed?.presets[0].name).toBe("开发环境");
  });

  it("serializeLayout 把 store 里的预设写进布局", () => {
    const out = serializeLayout({
      leftOpen: true,
      leftMode: "assets",
      rightOpen: true,
      leftWidth: 248,
      rightWidth: 352,
      workspaces: [],
      activeWorkspaceId: null,
      layoutPresets: [presetOf()],
    });
    expect(out?.presets).toHaveLength(1);
    expect(out?.presets[0].workspace.panes).toHaveLength(2);
  });
});

describe("captureWorkspacePreset", () => {
  it("过滤工具标签并映射活动面板与分屏比例", () => {
    const ws = workspaceOf(
      [
        {
          id: "pane1",
          tabs: [
            { id: "t1", kind: "terminal", title: "终端 1", sessionId: "s1", tabId: "T1", closable: true },
            { id: "t2", kind: "settings", title: "设置", closable: true },
          ],
          activeTabId: "t2",
        },
        {
          id: "pane2",
          tabs: [{ id: "t3", kind: "files", title: "文件", sessionId: "s1", closable: true }],
          activeTabId: "t3",
        },
      ],
      { activePaneId: "pane2", splitRatio: 0.4 },
    );
    const preset = captureWorkspacePreset(ws);
    expect(preset).not.toBeNull();
    expect(preset!.panes).toHaveLength(2);
    expect(preset!.panes[0].tabs.map((t) => t.kind)).toEqual(["terminal"]);
    // 活动标签「设置」被过滤后回落到面板内最后一个标签
    expect(preset!.panes[0].activeTabIndex).toBe(0);
    expect(preset!.panes[0].tabs[0].tabId).toBe("T1");
    expect(preset!.activePaneIndex).toBe(1);
    expect(preset!.splitRatio).toBe(0.4);
  });

  it("没有可保存标签时返回 null", () => {
    const ws = workspaceOf([
      { id: "pane1", tabs: [{ id: "t1", kind: "audit", title: "审计", closable: true }], activeTabId: "t1" },
    ]);
    expect(captureWorkspacePreset(ws)).toBeNull();
  });
});

describe("saveLayoutPresetFromWorkspace", () => {
  it("保存当前工作区并覆盖同名预设", async () => {
    const ws = workspaceOf([
      {
        id: "pane1",
        tabs: [{ id: "t1", kind: "terminal", title: "终端 1", sessionId: "s1", tabId: "T1", closable: true }],
        activeTabId: "t1",
      },
    ]);
    useUi.setState({ workspaces: [ws], activeWorkspaceId: "ws1" });
    mocks.promptText.mockResolvedValue("  我的预设  ");
    await saveLayoutPresetFromWorkspace();
    let presets = useUi.getState().layoutPresets;
    expect(presets).toHaveLength(1);
    expect(presets[0].name).toBe("我的预设");
    expect(presets[0].workspace.panes[0].tabs[0].tabId).toBe("T1");
    const id = presets[0].id;

    mocks.promptText.mockResolvedValue("我的预设");
    await saveLayoutPresetFromWorkspace();
    presets = useUi.getState().layoutPresets;
    expect(presets).toHaveLength(1);
    expect(presets[0].id).toBe(id);
  });

  it("没有工作区时提示且不保存", async () => {
    await saveLayoutPresetFromWorkspace();
    expect(useUi.getState().layoutPresets).toHaveLength(0);
    expect(useUi.getState().toasts.some((t) => t.text.includes("先连接一台主机"))).toBe(true);
  });
});

describe("applyLayoutPreset", () => {
  it("到新工作区：会话仍连接时按预设重建分屏与标签", async () => {
    useUi.setState({ layoutPresets: [presetOf()], sessions: [SESSION_S1] });
    mocks.sessionList.mockResolvedValue([SESSION_S1]);
    await applyLayoutPreset("p1", "new");
    const st = useUi.getState();
    expect(st.workspaces).toHaveLength(1);
    const ws = st.workspaces[0];
    expect(ws.kind).toBe("session");
    expect(ws.sessionId).toBe("s1");
    expect(ws.panes).toHaveLength(2);
    expect(ws.panes[0].tabs[0]).toMatchObject({ kind: "terminal", sessionId: "s1", title: "终端 1" });
    expect(ws.panes[1].tabs[0]).toMatchObject({ kind: "files", sessionId: "s1" });
    expect(ws.activePaneId).toBe(ws.panes[1].id);
    expect(ws.panes[1].activeTabId).toBe(ws.panes[1].tabs[0].id);
    expect(st.activeWorkspaceId).toBe(ws.id);
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
  });

  it("守护终端仍存活时接管原 tabId，不重连资产", async () => {
    useUi.setState({ layoutPresets: [presetOf()] });
    mocks.sessionList.mockResolvedValue([]);
    mocks.terminalListLive.mockResolvedValue([
      {
        tabId: "T1",
        sessionId: "s1",
        sessionName: "web-01",
        sessionKind: "ssh",
        cols: 80,
        rows: 24,
        controller: null,
        subscribers: 0,
        viewers: 0,
        exited: false,
        lastOutputMsAgo: 0,
      },
    ]);
    await applyLayoutPreset("p1", "new");
    const ws = useUi.getState().workspaces[0];
    expect(ws.panes[0].tabs[0]).toMatchObject({ kind: "terminal", sessionId: "s1", tabId: "T1" });
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
  });

  it("守护终端已退出时回落到新终端标签", async () => {
    useUi.setState({ layoutPresets: [presetOf()], sessions: [SESSION_S1] });
    mocks.sessionList.mockResolvedValue([SESSION_S1]);
    mocks.terminalListLive.mockResolvedValue([
      {
        tabId: "T1",
        sessionId: "s1",
        sessionName: "web-01",
        sessionKind: "ssh",
        cols: 80,
        rows: 24,
        controller: null,
        subscribers: 0,
        viewers: 0,
        exited: true,
        lastOutputMsAgo: 0,
      },
    ]);
    await applyLayoutPreset("p1", "new");
    const ws = useUi.getState().workspaces[0];
    expect(ws.panes[0].tabs[0]).toMatchObject({ kind: "terminal", sessionId: "s1" });
    expect(ws.panes[0].tabs[0].tabId).toBeUndefined();
  });

  it("会话失效时重连原资产并挂到新会话", async () => {
    const gone = presetOf();
    gone.workspace.sessionId = "s-gone";
    useUi.setState({ layoutPresets: [gone] });
    mocks.sessionList.mockResolvedValue([]);
    mocks.assetGet.mockResolvedValue({ id: "a1", name: "web-01", kind: "ssh", credId: null });
    mocks.sessionConnect.mockResolvedValue({ ...SESSION_S1, id: "s2" });
    await applyLayoutPreset("p1", "new");
    expect(mocks.assetGet).toHaveBeenCalledWith("a1");
    expect(mocks.sessionConnect).toHaveBeenCalledWith("a1");
    const ws = useUi.getState().workspaces[0];
    expect(ws.sessionId).toBe("s2");
    expect(ws.panes[0].tabs[0].sessionId).toBe("s2");
    expect(ws.panes[1].tabs[0].sessionId).toBe("s2");
  });

  it("到当前工作区：收回现有标签后按预设重建", async () => {
    const oldWs = workspaceOf(
      [
        {
          id: "pane-old",
          tabs: [
            { id: "t-old", kind: "terminal", title: "旧终端", sessionId: "s-old", tabId: "T-old", closable: true },
          ],
          activeTabId: "t-old",
        },
      ],
      { id: "ws-old", title: "旧", sessionId: "s-old", assetId: "a-old" },
    );
    const oldSession: SessionInfo = {
      id: "s-old",
      assetId: "a-old",
      name: "旧",
      kind: "ssh",
      status: "connected",
      tabs: [],
      createdAt: 0,
    };
    useUi.setState({
      workspaces: [oldWs],
      activeWorkspaceId: "ws-old",
      sessions: [SESSION_S1, oldSession],
      layoutPresets: [presetOf()],
    });
    mocks.sessionList.mockResolvedValue([SESSION_S1, oldSession]);
    await applyLayoutPreset("p1", "current");
    expect(mocks.closeTab).toHaveBeenCalledWith("T-old", "detach");
    const st = useUi.getState();
    expect(st.workspaces).toHaveLength(1);
    const ws = st.workspaces[0];
    expect(ws.id).toBe("ws-old");
    expect(ws.title).toBe("web-01");
    expect(ws.sessionId).toBe("s1");
    expect(ws.panes).toHaveLength(2);
    expect(ws.panes[0].tabs[0]).toMatchObject({ kind: "terminal", sessionId: "s1" });
  });

  it("数据库预设通过 assetId 重连并换新 connId", async () => {
    const dbPreset = presetOf({
      workspace: {
        kind: "db",
        title: "db-01 · SQL",
        assetId: "a-db",
        dbKind: "mysql",
        splitRatio: 0.5,
        panes: [{ tabs: [{ kind: "db", title: "db-01 · SQL", dbKind: "mysql" }], activeTabIndex: 0 }],
        activePaneIndex: 0,
      },
    });
    useUi.setState({ layoutPresets: [dbPreset] });
    mocks.assetGet.mockResolvedValue({ id: "a-db", name: "db-01", kind: "mysql", credId: null });
    mocks.dbConnect.mockResolvedValue({ connId: "c2" });
    await applyLayoutPreset("p1", "new");
    expect(mocks.dbConnect).toHaveBeenCalledWith("a-db");
    const ws = useUi.getState().workspaces[0];
    expect(ws.kind).toBe("db");
    expect(ws.connId).toBe("c2");
    expect(ws.panes[0].tabs[0]).toMatchObject({ kind: "db", connId: "c2", dbKind: "mysql" });
  });

  it("会话与资产都不可用时跳过标签并给出汇总", async () => {
    useUi.setState({ layoutPresets: [presetOf()], sessions: [] });
    mocks.sessionList.mockResolvedValue([]);
    mocks.assetGet.mockRejectedValue(new Error("not found"));
    await applyLayoutPreset("p1", "new");
    const st = useUi.getState();
    expect(st.workspaces).toHaveLength(1);
    expect(st.workspaces[0].panes.flatMap((p) => p.tabs)).toHaveLength(0);
    expect(st.toasts.some((t) => t.text.includes("未能恢复"))).toBe(true);
  });
});

describe("deleteLayoutPreset", () => {
  it("确认后删除，取消则保留", async () => {
    useUi.setState({ layoutPresets: [presetOf()] });
    mocks.ask.mockResolvedValue(false);
    await deleteLayoutPreset("p1");
    expect(useUi.getState().layoutPresets).toHaveLength(1);
    mocks.ask.mockResolvedValue(true);
    await deleteLayoutPreset("p1");
    expect(useUi.getState().layoutPresets).toHaveLength(0);
  });
});

describe("choosePresetTargetAndApply", () => {
  it("没有工作区时直接落到新工作区", async () => {
    useUi.setState({ layoutPresets: [presetOf()], sessions: [SESSION_S1] });
    mocks.sessionList.mockResolvedValue([SESSION_S1]);
    await choosePresetTargetAndApply("p1");
    expect(mocks.askChoice).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces).toHaveLength(1);
  });

  it("有工作区时按选择应用", async () => {
    const ws = workspaceOf([
      { id: "pane1", tabs: [], activeTabId: null },
    ]);
    useUi.setState({
      workspaces: [ws],
      activeWorkspaceId: "ws1",
      layoutPresets: [presetOf()],
      sessions: [SESSION_S1],
    });
    mocks.sessionList.mockResolvedValue([SESSION_S1]);
    mocks.askChoice.mockResolvedValue("current");
    await choosePresetTargetAndApply("p1");
    const st = useUi.getState();
    expect(st.workspaces).toHaveLength(1);
    expect(st.workspaces[0].panes).toHaveLength(2);
    expect(st.workspaces[0].sessionId).toBe("s1");
  });
});
