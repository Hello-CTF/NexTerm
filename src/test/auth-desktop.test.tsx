/** @vitest-environment jsdom */
// Desktop account card: identity comes from the server session of the "Account Sync" link (syncApi.status),
// role is probed via remoteAccountApi.adminUsers; all account operations go through the remoteAccountApi proxy,
// features not covered by the proxy (logout / TOTP / registration toggle) are hidden as-is with a note to manage in the browser.
import { act, createElement, Fragment } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, deferred, flush, flushUntil as flushUntilBase, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

async function flushUntil(predicate: () => boolean, maxTicks = 60): Promise<void> {
  await flush();
  await flushUntilBase(predicate, maxTicks);
  await flush();
}

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    status: vi.fn(),
    linkGet: vi.fn(),
    me: vi.fn(),
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
    syncApi: { ...actual.syncApi, status: mocks.status, linkGet: mocks.linkGet },
  };
});

vi.mock("../ipc/remoteApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/remoteApi")>();
  return {
    ...actual,
    remoteAccountApi: {
      me: mocks.me,
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
  mocks.me.mockResolvedValue({ user: ALICE });
  mocks.linkGet.mockResolvedValue({ url: "https://server-a.example", username: "alice", insecure: false, hasPassword: true, verifiedAt: 1, lastError: "" });
  mocks.adminUsers.mockRejectedValue({ code: "forbidden", message: "Super administrator access required" });
}

function seedAdmin() {
  mocks.status.mockResolvedValue(syncStatus(true, "root", "u-admin"));
  mocks.me.mockResolvedValue({ user: ADMIN });
  mocks.linkGet.mockResolvedValue({ url: "https://server-a.example", username: "root", insecure: false, hasPassword: true, verifiedAt: 1, lastError: "" });
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

afterEach(async () => {
  await flush();
  mounted?.unmount();
  mounted = undefined;
  await flush();
});

describe("desktop account card signed-out state", () => {
  it("renders neither card while the link is signed out", async () => {
    mocks.status.mockResolvedValue(syncStatus(false));
    mountCards("all");
    await flush();
    await flush();
    expect(mounted!.container.textContent ?? "").toBe("");
    expect(mocks.adminUsers).not.toHaveBeenCalled();
  });

  it("refreshes identity after invalidating [\"sync-link\"] and hides the card on logout", async () => {
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

describe("desktop account card regular user", () => {
  it("shows the current account and regular-user badge, hides logout/add-device actions, and notes browser-managed TOTP", async () => {
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
    // Non-super-admin: the user management card is not rendered
    expect(text).not.toContain("用户管理");
    expect(text).not.toContain("开放注册");
  });

  it("changes the password through remoteAccountApi.changePassword and immediately shows the one-time recovery key", async () => {
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
    clickButton(mounted!.container, "我已保存");
  });

  it("keeps a late one-time recovery key with the account that changed its password", async () => {
    seedUser();
    const changed = deferred<{ ok: boolean; recoveryKey: string }>();
    mocks.changePassword.mockReturnValueOnce(changed.promise);
    const { client } = mountCards("auth");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("修改密码"));

    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="当前密码"]')!, "old-pass-123");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="新密码（至少 8 位）"]')!, "new-pass-456");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="确认新密码"]')!, "new-pass-456");
    await flush();
    clickButton(mounted!.container, "修改密码");
    await flushUntil(() => mocks.changePassword.mock.calls.length === 1);

    const bob = { ...ALICE, id: "u-bob", username: "bob" };
    mocks.status.mockResolvedValue(syncStatus(true, bob.username, bob.id));
    mocks.me.mockResolvedValue({ user: bob });
    mocks.linkGet.mockResolvedValue({ url: "https://server-b.example", username: bob.username, insecure: false, hasPassword: true, verifiedAt: 2, lastError: "" });
    await act(async () => { await client.invalidateQueries({ queryKey: ["sync-link"] }); });
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("bob"));

    await act(async () => { changed.resolve({ ok: true, recoveryKey: "LATE-KEY-FOR-ALICE" }); });
    expect(mounted!.container.textContent).not.toContain("LATE-KEY-FOR-ALICE");

    seedUser();
    await act(async () => { await client.invalidateQueries({ queryKey: ["sync-link"] }); });
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("LATE-KEY-FOR-ALICE"));
    clickButton(mounted!.container, "我已保存");
  });

  it("shows password-change failures as-is", async () => {
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

  it("confirms before revoking a device, then revokes through the proxy and refreshes the list", async () => {
    seedUser();
    mountCards("auth");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("MacBook"));

    clickButton(mounted!.container, "吊销");
    await flushUntil(() => mocks.deviceRevoke.mock.calls.length === 1);
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("MacBook");
    expect(mocks.deviceRevoke).toHaveBeenCalledWith("d-1");
    await flushUntil(() => mocks.devices.mock.calls.length >= 2);
  });

  it("does not revoke the device when confirmation is canceled", async () => {
    seedUser();
    mocks.ask.mockResolvedValue(false);
    mountCards("auth");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("MacBook"));

    clickButton(mounted!.container, "吊销");
    await flush();
    expect(mocks.deviceRevoke).not.toHaveBeenCalled();
  });
});

describe("desktop account card superadmin", () => {
  it("hides the old same-named account while the new server link arrives before identity", async () => {
    seedAdmin();
    const { client } = mountCards("account");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("alice"));

    const nextIdentity = deferred<{ user: typeof ADMIN }>();
    const nextAdmin = { ...ADMIN, id: "u-admin-on-server-b" };
    mocks.me.mockImplementationOnce(() => nextIdentity.promise);
    mocks.linkGet.mockResolvedValue({ url: "https://server-b.example", username: "root", insecure: false, hasPassword: true, verifiedAt: 2, lastError: "" });
    mocks.adminUsers.mockResolvedValue({ users: [nextAdmin, { ...ALICE, id: "u-bob", username: "bob" }] });

    let invalidation: Promise<unknown> = Promise.resolve();
    act(() => { invalidation = client.invalidateQueries({ queryKey: ["sync-link"] }); });
    await flushUntil(() => mocks.me.mock.calls.length >= 2 && mocks.linkGet.mock.calls.length >= 2);
    await flush();
    expect(mounted!.container.textContent).not.toContain("用户管理");
    expect(mounted!.container.textContent).not.toContain("alice");

    await act(async () => {
      nextIdentity.resolve({ user: nextAdmin });
      await invalidation;
    });
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("bob"));
    expect(mounted!.container.textContent).not.toContain("alice");
  });

  it("separates identical admin identities across servers and ignores an old list response", async () => {
    seedAdmin();
    const { client } = mountCards("account");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("alice"));
    clickButton(mounted!.container, "添加用户");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="用户名"]')!, "draft-user");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="初始密码（至少 8 位）"]')!, "private-draft-123");

    const oldList = deferred<{ users: (typeof ADMIN | typeof ALICE)[] }>();
    mocks.adminUsers.mockImplementationOnce(() => oldList.promise);
    mocks.adminUsers.mockResolvedValue({ users: [ADMIN, { ...ALICE, id: "u-bob", username: "bob" }] });
    clickButton(mounted!.container, "刷新");
    await flushUntil(() => mocks.adminUsers.mock.calls.length === 2);

    mocks.linkGet.mockResolvedValue({ url: "https://server-b.example", username: "root", insecure: false, hasPassword: true, verifiedAt: 2, lastError: "" });
    await act(async () => { await client.invalidateQueries({ queryKey: ["sync-link"] }); });
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("bob"));
    expect(mounted!.container.textContent).not.toContain("alice");
    expect(mounted!.container.querySelector('input[placeholder="初始密码（至少 8 位）"]')).toBeNull();

    await act(async () => { oldList.resolve({ users: [ADMIN, ALICE] }); });
    expect(mounted!.container.textContent).toContain("bob");
    expect(mounted!.container.textContent).not.toContain("alice");
  });

  it("does not execute an old admin confirmation after the account switches", async () => {
    seedAdmin();
    const { client } = mountCards("account");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("alice"));
    const confirmation = deferred<boolean>();
    mocks.ask.mockImplementationOnce(() => confirmation.promise);
    const disable = [...rowOf(mounted!.container, "alice").querySelectorAll("button")].find((button) => button.textContent?.trim() === "禁用")!;
    act(() => { disable.dispatchEvent(new MouseEvent("click", { bubbles: true })); });
    await flushUntil(() => mocks.ask.mock.calls.length === 1);

    const nextAdmin = { ...ADMIN, id: "u-admin-b", username: "second-admin" };
    mocks.status.mockResolvedValue(syncStatus(true, nextAdmin.username, nextAdmin.id));
    mocks.me.mockResolvedValue({ user: nextAdmin });
    mocks.linkGet.mockResolvedValue({ url: "https://server-a.example", username: nextAdmin.username, insecure: false, hasPassword: true, verifiedAt: 2, lastError: "" });
    mocks.adminUsers.mockResolvedValue({ users: [nextAdmin] });
    await act(async () => { await client.invalidateQueries({ queryKey: ["sync-link"] }); });
    await flushUntil(() => (mounted!.container.textContent ?? "").includes(nextAdmin.username));

    await act(async () => { confirmation.resolve(true); });
    expect(mocks.adminSetUserState).not.toHaveBeenCalled();
    expect(mounted!.container.textContent).not.toContain("alice");
  });

  it("shows the superadmin badge and latest login after the identity probe finds the user list", async () => {
    seedAdmin();
    mountCards("auth");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("root"));

    const text = mounted!.container.textContent ?? "";
    expect(text).toContain("超级管理员");
    expect(text).toContain("最近登录");
  });

  it("lists every user with a self marker and hides the registration toggle with an explanation", async () => {
    seedAdmin();
    mountCards("account");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("alice"));

    const text = mounted!.container.textContent ?? "";
    expect(text).toContain("root");
    expect(text).toContain("（我）");
    expect(text).toContain("开放注册与两步验证（MFA）策略在服务端 Web 界面管理");
    expect(mounted!.container.querySelector('input[type="checkbox"]')).toBeNull();
    const labels = [...rowOf(mounted!.container, "alice").querySelectorAll("button")].map((b) => b.textContent?.trim());
    expect(labels).toEqual(["禁用", "重置"]);
    expect(rowOf(mounted!.container, "root").querySelectorAll("button")).toHaveLength(0);
  });

  it("creates a user through the proxy with a username and initial password", async () => {
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

  it("disables a user through adminSetUserState(disabled) and refreshes", async () => {
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

  it("resets a user through adminSetUserState(reset_required) and preserves the confirmation copy", async () => {
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
