/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    users: vi.fn(),
    createUser: vi.fn(),
    disableUser: vi.fn(),
    resetUser: vi.fn(),
    settingsGet: vi.fn(),
    settingsPut: vi.fn(),
    status: vi.fn(),
    ask: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../ipc/authApi", async (importOriginal) => {
  const original = await importOriginal<typeof import("../ipc/authApi")>();
  return {
    ...original,
    authApi: {
      status: mocks.status,
      logout: vi.fn().mockResolvedValue({ ok: true }),
      logoutAll: vi.fn().mockResolvedValue({ revoked: 1 }),
      devices: vi.fn().mockResolvedValue({ devices: [] }),
      deviceRevoke: vi.fn().mockResolvedValue({ ok: true }),
      enrollCode: vi.fn().mockResolvedValue({ code: "e", expires_at: 1 }),
    },
    adminApi: {
      users: mocks.users,
      createUser: mocks.createUser,
      disableUser: mocks.disableUser,
      resetUser: mocks.resetUser,
      settingsGet: mocks.settingsGet,
      settingsPut: mocks.settingsPut,
    },
  };
});
vi.mock("../ui/dialogs", () => ({ ask: mocks.ask }));

import { AccountCard } from "../features/settings/AccountCard";
import { useAuth } from "../features/auth/store";
import { useUi } from "../app/store";

const SELF = {
  id: "u-admin",
  username: "root",
  display_name: "",
  role: "superadmin" as const,
  state: "active" as const,
  must_change_password: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};
const ALICE = {
  id: "u-alice",
  username: "alice",
  display_name: "Alice",
  role: "user" as const,
  state: "active" as const,
  must_change_password: false,
  created_at: 2,
  updated_at: 2,
  last_login_at: 2,
};

let mounted: MountedView | undefined;

function mountCard(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(AccountCard)));
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth: "on" },
    user: SELF,
    dek: null,
    gate: "ready",
    pendingRecoveryKey: null,
    error: null,
  });
  mocks.users.mockResolvedValue({ users: [SELF, ALICE] });
  mocks.settingsGet.mockResolvedValue({ registration_open: false, public_base_url: "" });
  mocks.settingsPut.mockResolvedValue({ registration_open: true, public_base_url: "" });
  mocks.createUser.mockResolvedValue({ user: ALICE });
  mocks.disableUser.mockResolvedValue({ ok: true });
  mocks.resetUser.mockResolvedValue({ ok: true });
  mocks.status.mockResolvedValue({ initialized: true, registration_open: true, auth: "on" });
  mocks.ask.mockResolvedValue(true);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useAuth.setState({ user: null, dek: null, gate: "ready", pendingRecoveryKey: null, error: null, status: null });
});

describe("AccountCard 超管用户管理", () => {
  it("非超管不渲染", async () => {
    useAuth.setState({ user: { ...SELF, role: "user" } });
    mounted = mountCard();
    await flush();
    expect(mounted.container.textContent ?? "").not.toContain("用户管理");
  });

  it("列出用户与状态,自己不可禁用/重置", async () => {
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("alice"));
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("root");
    expect(text).toContain("alice");
    expect(text).toContain("(我)");
    const selfRow = [...mounted.container.querySelectorAll("div.border-b")].find((d) =>
      d.textContent?.includes("(我)"),
    );
    expect(selfRow?.textContent).not.toContain("禁用");
  });

  it("创建用户提交用户名与初始密码", async () => {
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("alice"));
    clickButton(mounted.container, "添加用户");
    await flushUntil(() => mounted!.container.querySelector('input[placeholder="用户名"]') !== null);

    setInputValue(mounted.container.querySelector<HTMLInputElement>('input[placeholder="用户名"]')!, "bob");
    setInputValue(mounted.container.querySelector<HTMLInputElement>('input[placeholder="初始密码(至少 8 位)"]')!, "bob-pw-123");
    await flush();
    clickButton(mounted.container, "创建");
    await flushUntil(() => mocks.createUser.mock.calls.length > 0);
    expect(mocks.createUser).toHaveBeenCalledWith("bob", "bob-pw-123", "");
    await flushUntil(() => mocks.users.mock.calls.length >= 2);
  });

  it("禁用用户先确认,确认后调用并刷新", async () => {
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("alice"));
    const aliceRow = [...mounted.container.querySelectorAll("div")].find(
      (d) => d.textContent?.includes("alice") && !d.textContent?.includes("(我)"),
    )!;
    const disableBtn = [...aliceRow.querySelectorAll("button")].find((b) => b.textContent?.trim() === "禁用")!;
    disableBtn.click();
    await flushUntil(() => mocks.ask.mock.calls.length > 0);
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("alice");
    await flushUntil(() => mocks.disableUser.mock.calls.length > 0);
    expect(mocks.disableUser).toHaveBeenCalledWith("u-alice");
  });

  it("取消确认时不调用禁用", async () => {
    mocks.ask.mockResolvedValue(false);
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("alice"));
    const aliceRow = [...mounted.container.querySelectorAll("div")].find(
      (d) => d.textContent?.includes("alice") && !d.textContent?.includes("(我)"),
    )!;
    const disableBtn = [...aliceRow.querySelectorAll("button")].find((b) => b.textContent?.trim() === "禁用")!;
    disableBtn.click();
    await flush();
    expect(mocks.disableUser).not.toHaveBeenCalled();
  });

  it("重置用户先确认并提示清除密钥", async () => {
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("alice"));
    const aliceRow = [...mounted.container.querySelectorAll("div")].find(
      (d) => d.textContent?.includes("alice") && !d.textContent?.includes("(我)"),
    )!;
    const resetBtn = [...aliceRow.querySelectorAll("button")].find((b) => b.textContent?.trim() === "重置")!;
    resetBtn.click();
    await flushUntil(() => mocks.ask.mock.calls.length > 0);
    const question = String(mocks.ask.mock.calls[0]?.[0]);
    expect(question).toContain("数据密钥");
    await flushUntil(() => mocks.resetUser.mock.calls.length > 0);
    expect(mocks.resetUser).toHaveBeenCalledWith("u-alice");
  });
});

describe("AccountCard 注册开关", () => {
  it("读取当前开关状态并保存新状态", async () => {
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("开放注册"));
    const checkbox = mounted.container.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
    expect(checkbox.checked).toBe(false);

    checkbox.click();
    await flushUntil(() => {
      const btn = [...mounted!.container.querySelectorAll("button")].find(
        (b) => b.textContent?.trim() === "保存",
      ) as HTMLButtonElement | undefined;
      return !!btn && !btn.disabled;
    });
    clickButton(mounted.container, "保存");
    await flushUntil(() => mocks.settingsPut.mock.calls.length > 0);
    expect(mocks.settingsPut).toHaveBeenCalledWith(
      expect.objectContaining({ registrationOpen: true }),
    );
  });

  it("保存后刷新 /auth/status 的注册开关缓存", async () => {
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("开放注册"));
    const checkbox = mounted.container.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
    checkbox.click();
    await flushUntil(() => {
      const btn = [...mounted!.container.querySelectorAll("button")].find(
        (b) => b.textContent?.trim() === "保存",
      ) as HTMLButtonElement | undefined;
      return !!btn && !btn.disabled;
    });
    clickButton(mounted.container, "保存");
    await flushUntil(() => mocks.status.mock.calls.length > 0);
    expect(useAuth.getState().status?.registration_open).toBe(true);
  });
});
