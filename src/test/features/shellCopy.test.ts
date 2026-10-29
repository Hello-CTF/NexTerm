/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoice: vi.fn(),
  closeTab: vi.fn(),
  sessionList: vi.fn().mockResolvedValue([]),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, askChoice: mocks.askChoice }));
vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: { list: mocks.sessionList },
  terminalApi: { closeTab: mocks.closeTab },
  vaultApi: {},
}));

import { modHint, setMacPlatform } from "../../app/platform";
import { setTerminalCloseAction } from "../../app/preferences";
import { connectProgressText, connectStageText, sessionStatusText, useUi, type AppTab, type Pane, type Workspace } from "../../app/store";
import { setFileEditorDirty } from "../../features/files/editorGuards";
import { describeError, describeErrorDetail, detailForText } from "../../ui/errorText";
import type { SessionInfo } from "../../ipc/commands";
import type { ConnectProgressEvent } from "../../ipc/events";

function terminal(id = "terminal", sessionId = "s"): AppTab {
  return { id, kind: "terminal", title: id, sessionId, tabId: `kernel-${id}`, closable: true, retainable: true };
}

function sessionInfo(kind: string, id = "s"): SessionInfo {
  return { id, assetId: "a", name: "session", kind, status: "connected", tabs: [], createdAt: 0 };
}

function ws(panes: Pane[]): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "workspace",
    panes,
    activePaneId: panes[0].id,
    closable: true,
  };
}

describe("describeError", () => {
  it("turns technical messages into plain Chinese by code and keeps the raw text for the details disclosure", () => {
    const err = { code: "not_found", message: "session not found" };
    expect(describeError(err)).toBe("未找到对应内容，可能已被删除");
    expect(describeErrorDetail(err)).toBe("code=not_found | session not found");
    expect(describeError({ code: "timeout", message: "context deadline exceeded" })).toBe(
      "等待超时，请检查网络后重试",
    );
    expect(describeError({ code: "sftp", message: "SFTP list /tmp: permission denied" })).toBe(
      "文件传输失败，请检查连接和路径权限后重试",
    );
    expect(describeError({ code: "ssh", message: "SSH dial host:22: refused" })).toContain("SSH");
  });

  it("keeps messages that are already readable Chinese", () => {
    expect(describeError({ code: "bad_param", message: "参数错误: 请选择 schema" })).toBe("参数错误: 请选择 schema");
    expect(describeError({ code: "vault_locked", message: "凭据库已锁定，请先解锁" })).toBe("凭据库已锁定，请先解锁");
    expect(describeErrorDetail({ code: "vault_locked", message: "凭据库已锁定，请先解锁" })).toBe(
      "code=vault_locked | 凭据库已锁定，请先解锁",
    );
  });

  it("never leaks HTTP status or non-JSON wording", () => {
    const err = { code: "internal", message: "内部错误：服务端返回了非 JSON 响应（HTTP 404）：<html>", detail: { httpStatus: 404 } };
    const text = describeError(err);
    expect(text).not.toMatch(/HTTP|JSON|404/);
    expect(describeErrorDetail(err)).toContain("404");
    expect(describeError(new Error("上传失败（HTTP 502）"))).toBe("上传失败");
    expect(describeError(new TypeError("Failed to fetch"))).toBe("网络请求失败，请检查网络连接后重试");
  });

  it("falls back to a generic sentence for unknown codes with technical text", () => {
    const text = describeError({ code: "some_future_code", message: "checksum mismatch" });
    expect(text).not.toContain("checksum");
    expect(describeErrorDetail({ code: "some_future_code", message: "checksum mismatch" })).toContain("checksum mismatch");
  });

  it("remembers raw detail so a toast built from describeError can offer it", () => {
    const text = `同步失败：${describeError({ code: "db", message: "database is locked (SQLITE_BUSY)" })}`;
    expect(detailForText(text)).toContain("SQLITE_BUSY");
  });

  it("handles non-AppError inputs", () => {
    expect(describeError(null)).toBe("未知错误");
    expect(describeError("plain")).toBe("plain");
    expect(describeError(new Error("boom"))).toBe("boom");
  });
});

describe("sessionStatusText", () => {
  it("maps session states to Chinese labels", () => {
    expect(sessionStatusText("connected")).toBe("已连接");
    expect(sessionStatusText("connecting")).toBe("连接中");
    expect(sessionStatusText("reconnecting")).toBe("重新连接中");
    expect(sessionStatusText("failed")).toBe("连接失败");
    expect(sessionStatusText("disconnected")).toBe("已断开");
    expect(sessionStatusText(undefined)).toBe("已断开");
  });
});

describe("connectProgressText", () => {
  const at = (stage: string, hop: number, hops = 0): ConnectProgressEvent => ({
    assetId: "a1",
    stage,
    hop,
    hops,
    host: "10.0.0.8",
    port: 22,
  });

  it("maps connection stages to Chinese copy", () => {
    expect(connectStageText("auth")).toBe("准备认证");
    expect(connectStageText("dial")).toBe("TCP 拨号");
    expect(connectStageText("channel")).toBe("经跳板打开通道");
    expect(connectStageText("handshake")).toBe("SSH 握手认证");
    expect(connectStageText("unexpected")).toBe("unexpected");
  });

  it("distinguishes the target host from the hop number", () => {
    expect(connectProgressText(at("dial", 0))).toBe("目标主机 · TCP 拨号");
    expect(connectProgressText(at("handshake", 1, 2))).toBe("跳板 1/2 · SSH 握手认证");
    expect(connectProgressText(at("channel", 2, 2))).toBe("跳板 2/2 · 经跳板打开通道");
  });
});

describe("modHint", () => {
  afterEach(() => {
    setMacPlatform(false);
  });

  it("shows the command symbol on macOS and Ctrl elsewhere", () => {
    setMacPlatform(true);
    expect(modHint()).toBe("⌘");
    setMacPlatform(false);
    expect(modHint()).toBe("Ctrl");
  });
});

describe("closeWorkspace batch close", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
    setTerminalCloseAction("detach");
  });

  it("keeps failed terminals and runtime while removing successful tabs", async () => {
    const succeeded = terminal("succeeded");
    const failed = terminal("failed");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [succeeded, failed], activeTabId: succeeded.id }])],
      activeWorkspaceId: "ws",
      sessions: [sessionInfo("ssh")],
      toasts: [],
    });
    useUi.getState().updateSqlDraft("ws", {
      text: "SELECT 1",
      selection: { from: 0, to: 8 },
      schema: "public",
      connId: "conn-1",
      dbKind: "mysql",
    });
    useUi.getState().ensureAiBoard("ws");
    mocks.closeTab.mockImplementation(async (id: string) => {
      if (id === failed.tabId) throw { code: "disconnected", message: "network unavailable" };
    });

    await useUi.getState().closeWorkspace("ws");

    const toasts = useUi.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0].kind).toBe("error");
    expect(toasts[0].text).toContain("1 个终端操作失败：连接已断开，请重新连接后再试");
    expect(useUi.getState().workspaces).toHaveLength(1);
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([
      { ...failed, closeError: "连接已断开，请重新连接后再试" },
    ]);
    expect(useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft?.text).toBe("SELECT 1");
    expect(useUi.getState().aiBoards["ws"]).toBeDefined();
  });

  it("performs no IPC or closes when the summary confirmation is canceled", async () => {
    const tab = terminal();
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
      sessions: [sessionInfo("winrm")],
      toasts: [],
    });
    mocks.ask.mockResolvedValueOnce(false);

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces).toHaveLength(1);
    expect(useUi.getState().toasts).toHaveLength(0);
  });

  it("combines dirty and blocked tabs into one summary confirmation", async () => {
    const tab = terminal();
    const dirtyTab: AppTab = {
      id: "file-1",
      kind: "files",
      title: "notes.txt",
      sessionId: "s",
      path: "/notes.txt",
      closable: true,
    };
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab, dirtyTab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
      sessions: [sessionInfo("winrm")],
      toasts: [],
    });
    setFileEditorDirty("s", "/notes.txt", true);
    mocks.ask.mockResolvedValueOnce(true);
    mocks.closeTab.mockResolvedValue(undefined);

    await useUi.getState().closeWorkspace("ws");
    setFileEditorDirty("s", "/notes.txt", false);

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const message = mocks.ask.mock.calls[0][0] as string;
    expect(message).toContain("尚未保存");
    expect(message).toContain("notes.txt");
    expect(message).toContain("WinRM 非交互");
    expect(useUi.getState().workspaces).toHaveLength(0);
    const toasts = useUi.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0].text).toContain("1 个进程已结束");
  });
});
