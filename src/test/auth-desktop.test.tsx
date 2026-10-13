/** @vitest-environment jsdom */
// Desktop account card: identity comes from the server session of the "Account Sync" link (syncApi.status),
// role is probed via remoteAccountApi.adminUsers; all account operations go through the remoteAccountApi proxy,
// features not covered by the proxy (logout / TOTP / pairing codes / registration toggle) are hidden as-is with a note to manage in the browser.
import { act, createElement, Fragment } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    status: vi.fn(),
    devices: vi.fn(),
    deviceRevoke: vi.fn(),
    adminUsers: vi.fn(),
    adminCreateUser: vi.fn(),
    adminSetUserState: vi.fn(),
    changePassword: vi.fn(),
    ask: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    syncApi: { ...actual.syncApi, status: mocks.status },
  };
});

vi.mock("../ipc/remoteApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/remoteApi")>();
  return {
    ...actual,
    remoteAccountApi: {
      devices: mocks.devices,
      deviceRevoke: mocks.deviceRevoke,
      adminUsers: mocks.adminUsers,
      adminCreateUser: mocks.adminCreateUser,
      adminSetUserState: mocks.adminSetUserState,
      changePassword: mocks.changePassword,
    },
  };
});

vi.mock("../ui/dialogs", () => ({ ask: mocks.ask }));

import { AuthCard } from "../features/settings/AuthCard";
import { AccountCard } from "../features/settings/AccountCard";
import { useUi } from "../app/store";

const ADMIN = {
  id: "u-admin",
  username: "root",
  display_name: "",
  role: "superadmin" as const,
  state: "active" as const,
  must_change_password: false,
  mfa_enabled: true,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1700000000000,
};
const ALICE = {
  id: "u-alice",
  username: "alice",
  display_name: "Alice",
  role: "user" as const,
  state: "active" as const,
  must_change_password: false,
  mfa_enabled: false,
  created_at: 2,
  updated_at: 2,
  last_login_at: 1700000000000,
};
const MACBOOK = { id: "d-1", name: "MacBook", kind: "desktop", created_at: 1, last_seen_at: 1700000000000, revoked_at: 0 };

function syncStatus(loggedIn: boolean, username = "", userId = "") {
  return {
    configured: loggedIn,
    loggedIn,
    ...(username ? { username, userId } : {}),
    head: "head-1",
    seq: 7,
    verifiedAt: 1700000000000,
    lastError: "",
  };
}

// Regular user: the adminUsers probe is rejected by the server (403), identity falls back to the minimal view.
function seedUser() {
  mocks.status.mockResolvedValue(syncStatus(true, "alice", "u-alice"));
  mocks.adminUsers.mockRejectedValue({ code: "forbidden", message: "需要超级管理员权限" });
}

function seedAdmin() {
  mocks.status.mockResolvedValue(syncStatus(true, "root", "u-admin"));
  mocks.adminUsers.mockResolvedValue({ users: [ADMIN, ALICE] });
}

let mounted: MountedView | undefined;

function mountCards(both: "auth" | "account" | "all"): { client: QueryClient } {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const node =
    both === "auth"
      ? createElement(AuthCard)
      : both === "account"
        ? createElement(AccountCard)
        : createElement(Fragment, null, createElement(AuthCard), createElement(AccountCard));
  mounted = mount(createElement(QueryClientProvider, { client }, node));
  return { client };
}

function rowOf(container: ParentNode, username: string): HTMLElement {
  const row = [...container.querySelectorAll<HTMLElement>("div.border-b")].find((d) =>
    d.querySelector(`span[title="${username}"]`),
  );
  if (!row) throw new Error(`row not found: ${username}`);
  return row;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  mocks.devices.mockResolvedValue({ devices: [MACBOOK] });
  mocks.deviceRevoke.mockResolvedValue({ ok: true });
  mocks.adminCreateUser.mockResolvedValue({ user: ALICE });
  mocks.adminSetUserState.mockResolvedValue({ ok: true });
  mocks.ask.mockResolvedValue(true);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("桌面端账号卡·未登录", () => {
  it("link 未登录时两卡均不渲染", async () => {
    mocks.status.mockResolvedValue(syncStatus(false));
    mountCards("all");
    await flush();
    await flush();
    expect(mounted!.container.textContent ?? "").toBe("");
    expect(mocks.adminUsers).not.toHaveBeenCalled();
  });

  it("invalidate [\"sync-link\"] 后身份自动刷新: 登出即隐藏", async () => {
    seedUser();
    const { client } = mountCards("auth");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("alice"));

    mocks.status.mockResolvedValue(syncStatus(false));
    await act(async () => {
      await client.invalidateQueries({ queryKey: ["sync-link"] });
    });
    await flushUntil(() => (mounted!.container.textContent ?? "") === "");
  });
});

describe("桌面端账号卡·普通用户", () => {
  it("显示当前账号与普通用户徽章,无退出登录/添加设备入口,TOTP 注明浏览器管理", async () => {
    seedUser();
    mountCards("all");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("MacBook"));

    const text = mounted!.container.textContent ?? "";
    expect(text).toContain("alice");
    expect(text).toContain("普通用户");
    const buttonLabels = [...mounted!.container.querySelectorAll("button")].map((b) => b.textContent?.trim());
    expect(buttonLabels).not.toContain("退出登录");
    expect(buttonLabels).not.toContain("退出全部会话");
    expect(buttonLabels).not.toContain("添加设备");
    expect(text).toContain("两步验证的绑定、换绑与关闭暂未接入桌面端");
    expect(text).toContain("配对码添加新设备请在浏览器中打开服务端 Web 界面操作");
    // Non-super-admin: the user management card is not rendered
    expect(text).not.toContain("用户管理");
    expect(text).not.toContain("开放注册");
  });

  it("改密走 remoteAccountApi.changePassword 并当场展示一次性恢复密钥", async () => {
    seedUser();
    mocks.changePassword.mockResolvedValue({ ok: true, recoveryKey: "AAAA-BBBB-CCCC-DDDD" });
    mountCards("auth");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("修改密码"));

    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="当前密码"]')!, "old-pass-123");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="新密码（至少 8 位）"]')!, "new-pass-456");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="确认新密码"]')!, "new-pass-456");
    await flush();
    clickButton(mounted!.container, "修改密码");

    await flushUntil(() => mocks.changePassword.mock.calls.length === 1);
    expect(mocks.changePassword).toHaveBeenCalledWith("old-pass-123", "new-pass-456");
    expect(mocks.toast).toHaveBeenCalledWith("success", "密码已修改，其他设备上的会话已退出");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("AAAA-BBBB-CCCC-DDDD"));
    expect(mounted!.container.textContent ?? "").toContain("新的恢复密钥（仅显示一次）");
  });

  it("改密失败如实显示错误", async () => {
    seedUser();
    mocks.changePassword.mockRejectedValue({ code: "invalid_credentials", message: "当前密码不正确" });
    mountCards("auth");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("修改密码"));

    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="当前密码"]')!, "wrong-pass-1");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="新密码（至少 8 位）"]')!, "new-pass-456");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="确认新密码"]')!, "new-pass-456");
    await flush();
    clickButton(mounted!.container, "修改密码");

    await flushUntil(() => (mounted!.container.textContent ?? "").includes("当前密码不正确"));
  });

  it("吊销设备先确认,确认后经代理吊销并刷新列表", async () => {
    seedUser();
    mountCards("auth");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("MacBook"));

    clickButton(mounted!.container, "吊销");
    await flushUntil(() => mocks.deviceRevoke.mock.calls.length === 1);
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("MacBook");
    expect(mocks.deviceRevoke).toHaveBeenCalledWith("d-1");
    await flushUntil(() => mocks.devices.mock.calls.length >= 2);
  });

  it("取消确认时不吊销", async () => {
    seedUser();
    mocks.ask.mockResolvedValue(false);
    mountCards("auth");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("MacBook"));

    clickButton(mounted!.container, "吊销");
    await flush();
    expect(mocks.deviceRevoke).not.toHaveBeenCalled();
  });
});

describe("桌面端账号卡·超级管理员", () => {
  it("身份探测命中用户列表: 显示超级管理员徽章与最近登录", async () => {
    seedAdmin();
    mountCards("auth");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("root"));

    const text = mounted!.container.textContent ?? "";
    expect(text).toContain("超级管理员");
    expect(text).toContain("最近登录");
  });

  it("用户管理列出全部用户并标注自己,注册开关区隐藏并注明", async () => {
    seedAdmin();
    mountCards("account");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("alice"));

    const text = mounted!.container.textContent ?? "";
    expect(text).toContain("root");
    expect(text).toContain("（我）");
    expect(text).toContain("开放注册与两步验证（MFA）策略暂未接入桌面端");
    expect(mounted!.container.querySelector('input[type="checkbox"]')).toBeNull();
    const labels = [...rowOf(mounted!.container, "alice").querySelectorAll("button")].map((b) => b.textContent?.trim());
    expect(labels).toEqual(["禁用", "重置"]);
    expect(rowOf(mounted!.container, "root").querySelectorAll("button")).toHaveLength(0);
  });

  it("创建用户经代理提交用户名与初始密码", async () => {
    seedAdmin();
    mountCards("account");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("alice"));

    clickButton(mounted!.container, "添加用户");
    await flushUntil(() => mounted!.container.querySelector('input[placeholder="用户名"]') !== null);
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="用户名"]')!, "bob");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="初始密码（至少 8 位）"]')!, "bob-pw-12345");
    await flush();
    clickButton(mounted!.container, "创建");

    await flushUntil(() => mocks.adminCreateUser.mock.calls.length === 1);
    expect(mocks.adminCreateUser).toHaveBeenCalledWith("bob", "bob-pw-12345", "");
    await flushUntil(() => mocks.adminUsers.mock.calls.length >= 2);
  });

  it("禁用用户经 adminSetUserState(disabled) 并刷新", async () => {
    seedAdmin();
    mountCards("account");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("alice"));

    const btn = [...rowOf(mounted!.container, "alice").querySelectorAll("button")].find((b) => b.textContent?.trim() === "禁用")!;
    act(() => {
      btn.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await flushUntil(() => mocks.adminSetUserState.mock.calls.length === 1);
    expect(mocks.adminSetUserState).toHaveBeenCalledWith("u-alice", "disabled");
    await flushUntil(() => mocks.adminUsers.mock.calls.length >= 2);
  });

  it("重置用户经 adminSetUserState(reset_required) 并保留确认文案", async () => {
    seedAdmin();
    mountCards("account");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("alice"));

    const btn = [...rowOf(mounted!.container, "alice").querySelectorAll("button")].find((b) => b.textContent?.trim() === "重置")!;
    act(() => {
      btn.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await flushUntil(() => mocks.adminSetUserState.mock.calls.length === 1);
    const question = String(mocks.ask.mock.calls[0]?.[0]);
    expect(question).toContain("数据密钥");
    expect(question).toContain("两步验证绑定");
    expect(mocks.adminSetUserState).toHaveBeenCalledWith("u-alice", "reset_required");
  });
});
