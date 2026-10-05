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
import { sessionStatusText, useUi, type AppTab, type Pane, type Workspace } from "../../app/store";
import { describeError } from "../../ui/errorText";
import type { SessionInfo } from "../../ipc/commands";

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

function toastText(): string {
  return useUi.getState().toasts.map((t) => t.text).join("\n");
}

describe("describeError", () => {
  it("已知 code 映射为短中文标签并保留原始 message", () => {
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

  it("message 已带同义中文前缀时不再叠加 code 前缀", () => {
    expect(describeError({ code: "internal", message: "内部错误: SSH handshake: x" })).toBe(
      "内部错误: SSH handshake: x",
    );
    expect(describeError({ code: "bad_param", message: "参数错误: host is required" })).toBe(
      "参数错误: host is required",
    );
    expect(describeError({ code: "io", message: "IO 错误: no space left" })).toBe("IO 错误: no space left");
  });

  it("已知 code db_migrate 映射为中文标签，message 已带前缀时不叠加", () => {
    expect(describeError({ code: "db_migrate", message: "checksum mismatch" })).toBe(
      "数据库迁移错误：checksum mismatch",
    );
    expect(describeError({ code: "db_migrate", message: "数据库迁移错误: checksum mismatch" })).toBe(
      "数据库迁移错误: checksum mismatch",
    );
  });

  it("未知 code 保留 code: message 诊断", () => {
    expect(describeError({ code: "some_future_code", message: "checksum mismatch" })).toBe(
      "some_future_code: checksum mismatch",
    );
  });

  it("非 AppError 输入保持原有行为", () => {
    expect(describeError(null)).toBe("未知错误");
    expect(describeError(undefined)).toBe("未知错误");
    expect(describeError("plain")).toBe("plain");
    expect(describeError(new Error("boom"))).toBe("boom");
    expect(describeError({ message: "only message" })).toBe("only message");
    expect(describeError({ code: "only_code" })).toBe("only_code");
  });
});

describe("sessionStatusText", () => {
  it("会话状态映射为中文标签", () => {
    expect(sessionStatusText("connected")).toBe("已连接");
    expect(sessionStatusText("connecting")).toBe("连接中");
    expect(sessionStatusText("reconnecting")).toBe("重连中");
    expect(sessionStatusText("failed")).toBe("连接失败");
    expect(sessionStatusText("disconnected")).toBe("已断开");
    expect(sessionStatusText(undefined)).toBe("已断开");
  });
});

describe("modHint", () => {
  afterEach(() => {
    setMacPlatform(false);
  });

  it("Mac 显示 ⌘，其他平台显示 Ctrl", () => {
    setMacPlatform(true);
    expect(modHint()).toBe("⌘");
    setMacPlatform(false);
    expect(modHint()).toBe("Ctrl");
  });
});

describe("closeWorkspace 批量关闭", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
  });

  it("关闭失败 toast 用「关闭失败」并带中文标签诊断", async () => {
    const tab = terminal();
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
      sessions: [sessionInfo("ssh")],
      toasts: [],
    });
    mocks.closeTab.mockRejectedValue({ code: "not_found", message: "tab not found" });

    await useUi.getState().closeWorkspace("ws");

    expect(toastText()).toContain("1/1 个终端关闭失败：资源不存在：tab not found");
  });
});
