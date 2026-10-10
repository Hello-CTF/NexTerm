/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, flushUntil, mount, setInputValue, setSelectValue, type MountedView } from "./features/reactTestUtils";
import type { CommandEntryDto } from "../ipc/types";
import type { AccountUser } from "../ipc/authApi";
import { AuditView } from "../features/settings/AuditView";
import { useAuth } from "../features/auth/store";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    auditQuery: vi.fn(),
    auditCount: vi.fn(),
    commandQuery: vi.fn(),
    commandCount: vi.fn(),
    assetList: vi.fn(),
  };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      auditQuery: mocks.auditQuery,
      auditCount: mocks.auditCount,
      commandQuery: mocks.commandQuery,
      commandCount: mocks.commandCount,
      list: mocks.assetList,
    },
  };
});

const ASSET = {
  id: "a-1",
  groupId: null,
  kind: "ssh",
  name: "web-01",
  host: "127.0.0.1",
  port: 22,
  username: "deploy",
  authKind: null,
  keyPath: null,
  credId: null,
  options: {},
  tags: "",
  note: "",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
  deletedAt: null,
  builtin: false,
};

function makeRows(): CommandEntryDto[] {
  return [
    {
      id: 3,
      sessionId: "s-1",
      tabId: "t-1",
      assetId: "a-1",
      userId: "u-1",
      command: "[root@web-01 ~]# systemctl status nginx",
      source: "terminal",
      exitCode: 0,
      startedAt: 1700000000000,
      finishedAt: 1700000001000,
    },
    {
      id: 2,
      sessionId: "s-2",
      tabId: "t-2",
      assetId: "a-2",
      userId: null,
      command: "Get-Service Winmgmt",
      source: "exec",
      exitCode: 1,
      startedAt: 1700000002000,
      finishedAt: 1700000002500,
    },
    {
      id: 1,
      sessionId: "s-1",
      tabId: "t-1",
      assetId: "a-1",
      userId: null,
      command: "[root@web-01 ~]# tail -f /var/log/nginx/error.log",
      source: "terminal",
      exitCode: null,
      startedAt: 1700000003000,
      finishedAt: 1700000003600,
    },
  ];
}

function text(container: ParentNode): string {
  return container.textContent ?? "";
}

let mounted: MountedView | undefined;

function accountOf(role: AccountUser["role"]): AccountUser {
  return {
    id: "u-1",
    username: "alice",
    display_name: "",
    role,
    state: "active",
    must_change_password: false,
    mfa_enabled: false,
    created_at: 1,
    updated_at: 1,
    last_login_at: 0,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useAuth.setState({ user: null });
  mocks.assetList.mockResolvedValue([ASSET]);
  mocks.auditCount.mockResolvedValue({ total: 0 });
  mocks.auditQuery.mockResolvedValue([]);
  mocks.commandCount.mockResolvedValue({ total: 3 });
  mocks.commandQuery.mockResolvedValue(makeRows());
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("AuditView 命令视图", () => {
  it("切换到命令视图按 command_query 加载, 渲染命令/来源/退出码并保留 OSC 133 覆盖说明", async () => {
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mocks.auditQuery.mock.calls.length > 0);

    clickButton(mounted.container, "命令");
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 3);

    expect(mocks.commandQuery).toHaveBeenCalledWith({ assetId: undefined, userId: undefined, sessionId: undefined, limit: 100, offset: 0 });
    const body = text(mounted.container);
    expect(body).toContain("命令记录");
    expect(body).toContain("[root@web-01 ~]# systemctl status nginx");
    expect(body).toContain("Get-Service Winmgmt");
    expect(body).toContain("web-01");
    expect(body).toContain("u-1");
    expect(body).toContain("s-1");
    const badges = [...mounted.container.querySelectorAll("tbody .nx-badge")].map((b) => b.textContent);
    expect(badges).toContain("终端");
    expect(badges).toContain("执行");
    expect(body).toContain("✓ 0");
    expect(body).toContain("✗ 1");
    expect(body).toContain("—");
    expect(body).toContain("仅覆盖启用 OSC 133 shell 集成的会话");
    expect(body).toContain("共 3 条");
  });

  it("设备/用户/会话过滤传入 command_query 与 command_count", async () => {
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mocks.auditQuery.mock.calls.length > 0);
    clickButton(mounted.container, "命令");
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 3);

    const deviceSelect = mounted.container.querySelector<HTMLSelectElement>('select[aria-label="按设备过滤"]');
    if (!deviceSelect) throw new Error("设备过滤下拉未出现");
    setSelectValue(deviceSelect, "a-1");
    await flushUntil(() => mocks.commandQuery.mock.calls.length >= 2);
    expect(mocks.commandQuery).toHaveBeenLastCalledWith({ assetId: "a-1", userId: undefined, sessionId: undefined, limit: 100, offset: 0 });
    expect(mocks.commandCount).toHaveBeenLastCalledWith({ assetId: "a-1", userId: undefined, sessionId: undefined });

    const userInput = mounted.container.querySelector<HTMLInputElement>('input[aria-label="按用户过滤"]');
    if (!userInput) throw new Error("用户过滤输入框未出现");
    setInputValue(userInput, "u-1");
    await flushUntil(() => mocks.commandQuery.mock.calls.length >= 3);
    expect(mocks.commandQuery).toHaveBeenLastCalledWith({ assetId: "a-1", userId: "u-1", sessionId: undefined, limit: 100, offset: 0 });

    const sessionInput = mounted.container.querySelector<HTMLInputElement>('input[aria-label="按会话过滤"]');
    if (!sessionInput) throw new Error("会话过滤输入框未出现");
    setInputValue(sessionInput, "s-1");
    await flushUntil(() => mocks.commandQuery.mock.calls.length >= 4);
    expect(mocks.commandQuery).toHaveBeenLastCalledWith({ assetId: "a-1", userId: "u-1", sessionId: "s-1", limit: 100, offset: 0 });
    expect(mocks.commandCount).toHaveBeenLastCalledWith({ assetId: "a-1", userId: "u-1", sessionId: "s-1" });
  });

  it("空态与失败态如实呈现覆盖限制与错误", async () => {
    mocks.commandCount.mockResolvedValue({ total: 0 });
    mocks.commandQuery.mockResolvedValue([]);
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mocks.auditQuery.mock.calls.length > 0);
    clickButton(mounted.container, "命令");
    await flushUntil(() => text(mounted!.container).includes("暂无记录"));
    expect(text(mounted.container)).toContain("仅记录启用了 OSC 133 集成的 shell 中执行的命令");
    expect(text(mounted.container)).toContain("命令文本取自执行时的屏幕行");

    mocks.commandQuery.mockRejectedValue(new Error("网络中断"));
    clickButton(mounted.container, "刷新");
    await flushUntil(() => text(mounted!.container).includes("命令记录加载失败"));
    expect(text(mounted.container)).toContain("网络中断");
  });

  it("非超管登录用户不提供用户过滤入口且查询不带 userId (服务端强制收敛)", async () => {
    useAuth.setState({ user: accountOf("user") });
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mocks.auditQuery.mock.calls.length > 0);
    clickButton(mounted.container, "命令");
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 3);

    expect(mounted.container.querySelector('input[aria-label="按用户过滤"]')).toBeNull();
    expect(mocks.commandQuery).toHaveBeenCalledWith({ assetId: undefined, userId: undefined, sessionId: undefined, limit: 100, offset: 0 });
    expect(mocks.commandCount).toHaveBeenCalledWith({ assetId: undefined, userId: undefined, sessionId: undefined });
  });

  it("超管登录用户保留用户过滤入口", async () => {
    useAuth.setState({ user: accountOf("superadmin") });
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mocks.auditQuery.mock.calls.length > 0);
    clickButton(mounted.container, "命令");
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 3);

    expect(mounted.container.querySelector('input[aria-label="按用户过滤"]')).not.toBeNull();
  });

  it("命令视图可切回审计视图", async () => {
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mocks.auditQuery.mock.calls.length > 0);
    clickButton(mounted.container, "命令");
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 3);

    clickButton(mounted.container, "审计");
    await flushUntil(() => text(mounted!.container).includes("审计日志"));
    expect(mocks.auditQuery.mock.calls.length).toBeGreaterThan(0);
    expect(mounted.container.querySelector('input[aria-label="按用户过滤"]')).toBeNull();
  });
});
