/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoice: vi.fn(),
  closeTab: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, askChoice: mocks.askChoice }));
vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: { closeTab: mocks.closeTab },
  vaultApi: {},
}));

import { modHint, setMacPlatform } from "../../app/platform";
import { connectProgressText, connectStageText, sessionStatusText, useUi, type AppTab, type Pane, type Workspace } from "../../app/store";
import { setFileEditorDirty } from "../../features/files/editorGuards";
import { describeError } from "../../ui/errorText";
import type { SessionInfo } from "../../ipc/commands";
import type { ConnectProgressEvent } from "../../ipc/events";

function terminal(id = "terminal", sessionId = "s"): AppTab {
  return { id, kind: "terminal", title: id, sessionId, tabId: `kernel-${id}`, closable: true };
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
    splitRatio: 0.5,
    closable: true,
  };
}

describe("describeError", () => {
  it("maps known codes to short Chinese labels and preserves the original message", () => {
    expect(describeError({ code: "not_found", message: "session not found" })).toBe(
      "资源不存在：session not found",
    );
    expect(describeError({ code: "unsupported", message: "session capability unsupported" })).toBe(
      "不支持：session capability unsupported",
    );
    expect(describeError({ code: "timeout", message: "context deadline exceeded" })).toBe(
      "超时：context deadline exceeded",
    );
  });

  it("does not prepend the code label when the message already has an equivalent Chinese prefix", () => {
    expect(describeError({ code: "internal", message: "内部错误: SSH handshake: x" })).toBe(
      "内部错误: SSH handshake: x",
    );
    expect(describeError({ code: "bad_param", message: "参数错误: host is required" })).toBe(
      "参数错误: host is required",
    );
    expect(describeError({ code: "io", message: "IO 错误: no space left" })).toBe("IO 错误: no space left");
  });

  it("maps db_migrate to a Chinese label without duplicating an existing prefix", () => {
    expect(describeError({ code: "db_migrate", message: "checksum mismatch" })).toBe(
      "数据库迁移错误：checksum mismatch",
    );
    expect(describeError({ code: "db_migrate", message: "数据库迁移错误: checksum mismatch" })).toBe(
      "数据库迁移错误: checksum mismatch",
    );
  });

  it("preserves code: message diagnostics for unknown codes", () => {
    expect(describeError({ code: "some_future_code", message: "checksum mismatch" })).toBe(
      "some_future_code: checksum mismatch",
    );
    expect(describeError({ code: "ai_provider", message: "provider unreachable" })).toBe(
      "ai_provider: provider unreachable",
    );
  });

  it("maps provider and migration codes to Chinese labels", () => {
    expect(describeError({ code: "winrm", message: "WinRM host is empty" })).toBe(
      "WinRM 错误：WinRM host is empty",
    );
    expect(describeError({ code: "sftp", message: "SFTP list /tmp: permission denied" })).toBe(
      "SFTP 错误：SFTP list /tmp: permission denied",
    );
    expect(describeError({ code: "ssh", message: "SSH dial host:22: refused" })).toBe(
      "SSH 错误：SSH dial host:22: refused",
    );
    expect(describeError({ code: "db_migrate", message: "数据库迁移错误: checksum mismatch" })).toBe(
      "数据库迁移错误: checksum mismatch",
    );
  });

  it("preserves existing behavior for non-AppError inputs", () => {
    expect(describeError(null)).toBe("未知错误");
    expect(describeError(undefined)).toBe("未知错误");
    expect(describeError("plain")).toBe("plain");
    expect(describeError(new Error("boom"))).toBe("boom");
    expect(describeError({ message: "only message" })).toBe("only message");
    expect(describeError({ code: "only_code" })).toBe("only_code");
  });
});

describe("sessionStatusText", () => {
  it("maps session states to Chinese labels", () => {
    expect(sessionStatusText("connected")).toBe("已连接");
    expect(sessionStatusText("connecting")).toBe("连接中");
    expect(sessionStatusText("reconnecting")).toBe("重连中");
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
  });

  it("summarizes close failures in one toast with Chinese diagnostic labels", async () => {
    const tab = terminal();
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
      sessions: [sessionInfo("ssh")],
      toasts: [],
    });
    mocks.closeTab.mockRejectedValue({ code: "not_found", message: "tab not found" });

    await useUi.getState().closeWorkspace("ws");

    const toasts = useUi.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0].kind).toBe("error");
    expect(toasts[0].text).toContain("1 个终端操作失败：资源不存在：tab not found");
    // Paths other than cancel also send only one: the workspace is still closed, no second "moved to background" toast follows
    expect(useUi.getState().workspaces).toHaveLength(0);
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
