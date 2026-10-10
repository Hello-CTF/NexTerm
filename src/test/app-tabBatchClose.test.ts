/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  closeTab: vi.fn(),
  dbDisconnect: vi.fn(),
}));
vi.mock("../ui/dialogs", () => ({ ask: mocks.ask }));
vi.mock("../ipc/commands", () => ({
  assetApi: {},
  dbApi: { disconnect: mocks.dbDisconnect },
  sessionApi: {},
  terminalApi: { closeTab: mocks.closeTab },
  vaultApi: {},
}));

import {
  requestCloseTab,
  requestCloseTabs,
  useUi,
  type AppTab,
  type Pane,
  type Workspace,
} from "../app/store";
import type { SessionInfo } from "../ipc/commands";

function terminal(id: string): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s", tabId: `kernel-${id}`, closable: true };
}

function sessionInfo(): SessionInfo {
  return { id: "s", assetId: "a", name: "session", kind: "ssh", status: "connected", tabs: [], createdAt: 0 };
}

function ws(panes: Pane[]): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "workspace",
    sessionId: "s",
    panes,
    activePaneId: panes[0].id,
    splitRatio: 0.5,
    closable: true,
  };
}

function tabById(id: string): AppTab | undefined {
  return useUi
    .getState()
    .workspaces.flatMap((w) => w.panes)
    .flatMap((p) => p.tabs)
    .find((t) => t.id === id);
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.ask.mockResolvedValue(true);
  mocks.dbDisconnect.mockResolvedValue(undefined);
  useUi.setState({ workspaces: [], activeWorkspaceId: null, sessions: [sessionInfo()], toasts: [] });
});

describe("批量关闭部分失败", () => {
  it("成功的关闭,失败的保留并在标签上标出原因", async () => {
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [terminal("t1"), terminal("t2"), terminal("t3")], activeTabId: "t1" }])],
      activeWorkspaceId: "ws",
    });
    mocks.closeTab.mockImplementation((tabId: string) =>
      tabId === "kernel-t2" ? Promise.reject({ code: "not_found", message: "tab not found" }) : Promise.resolve(),
    );

    await requestCloseTabs(["t1", "t2", "t3"]);

    expect(tabById("t1")).toBeUndefined();
    expect(tabById("t3")).toBeUndefined();
    expect(tabById("t2")?.closeError).toBe("资源不存在：tab not found");
    const texts = useUi.getState().toasts.map((t) => t.text);
    expect(texts.some((t) => t.includes("终端操作失败：资源不存在：tab not found"))).toBe(true);
    expect(texts).toContain("1 个标签未能关闭，已保留并在标签上标出原因");
  });

  it("用户取消确认不算失败,不标记也不进汇总", async () => {
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [terminal("t1"), terminal("t2")], activeTabId: "t1" }])],
      activeWorkspaceId: "ws",
      sessions: [{ ...sessionInfo(), kind: "winrm" }],
    });
    mocks.ask.mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    mocks.closeTab.mockResolvedValue(undefined);

    await requestCloseTabs(["t1", "t2"]);

    expect(tabById("t1")).toBeUndefined();
    expect(tabById("t2")?.closeError).toBeUndefined();
    expect(useUi.getState().toasts.map((t) => t.text)).not.toContain(
      "1 个标签未能关闭，已保留并在标签上标出原因",
    );
  });

  it("带旧失败标记的标签本轮被取消时不进汇总", async () => {
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [terminal("t1"), { ...terminal("t2"), closeError: "上次失败" }], activeTabId: "t1" }])],
      activeWorkspaceId: "ws",
      sessions: [{ ...sessionInfo(), kind: "winrm" }],
    });
    mocks.ask.mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    mocks.closeTab.mockResolvedValue(undefined);

    await requestCloseTabs(["t1", "t2"]);

    expect(tabById("t1")).toBeUndefined();
    expect(tabById("t2")?.closeError).toBeUndefined();
    expect(useUi.getState().toasts.map((t) => t.text)).not.toContain(
      "1 个标签未能关闭，已保留并在标签上标出原因",
    );
  });

  it("单个标签关闭失败同样标出原因,重试成功后标签消失", async () => {
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [terminal("t1")], activeTabId: "t1" }])],
      activeWorkspaceId: "ws",
    });
    mocks.closeTab.mockRejectedValueOnce(new Error("boom"));

    await requestCloseTab("t1");

    expect(tabById("t1")?.closeError).toBe("boom");
    expect(useUi.getState().toasts.map((t) => t.text)).not.toContain(
      "1 个标签未能关闭，已保留并在标签上标出原因",
    );

    mocks.closeTab.mockResolvedValueOnce(undefined);
    await requestCloseTab("t1");
    expect(tabById("t1")).toBeUndefined();
  });

  it("数据库标签断开失败时保留并标出原因", async () => {
    const dbTab: AppTab = { id: "db1", kind: "db", title: "SQL", connId: "c1", dbKind: "mysql", closable: true };
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [terminal("t1"), dbTab], activeTabId: "t1" }])],
      activeWorkspaceId: "ws",
    });
    mocks.closeTab.mockResolvedValue(undefined);
    mocks.dbDisconnect.mockRejectedValueOnce({ code: "io", message: "connection reset" });

    await requestCloseTabs(["t1", "db1"]);

    expect(tabById("t1")).toBeUndefined();
    expect(tabById("db1")?.closeError).toBe("IO 错误：connection reset");
    expect(useUi.getState().toasts.map((t) => t.text)).toContain(
      "1 个标签未能关闭，已保留并在标签上标出原因",
    );
  });
});
