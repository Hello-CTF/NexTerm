/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, deferred, flush, flushUntil, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

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
  mfa_enabled: false,
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
  mfa_enabled: false,
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
  mocks.settingsGet.mockResolvedValue({ registration_open: false, mfa_required: false });
  mocks.settingsPut.mockResolvedValue({ registration_open: true, mfa_required: false });
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

describe("AccountCard superadmin user management", () => {
  it("does not render for non-superadmins", async () => {
    useAuth.setState({ user: { ...SELF, role: "user" } });
    mounted = mountCard();
    await flush();
    expect(mounted.container.textContent ?? "").not.toContain("用户管理");
  });

  it("lists users and their states without allowing self-disable or self-reset", async () => {
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("alice"));
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("root");
    expect(text).toContain("alice");
    expect(text).toContain("（我）");
    const selfRow = [...mounted.container.querySelectorAll("div.border-b")].find((d) =>
      d.textContent?.includes("（我）"),
    );
    expect(selfRow?.textContent).not.toContain("禁用");
  });

  it("shows disable only for active users while keeping reset for other non-self states", async () => {
    const disabled = { ...ALICE, id: "u-disabled", username: "disabled", state: "disabled" as const };
    const resetRequired = { ...ALICE, id: "u-reset", username: "reset", state: "reset_required" as const };
    mocks.users.mockResolvedValue({ users: [SELF, ALICE, disabled, resetRequired] });
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("reset"));

    const actionLabels = (username: string) => {
      const row = [...mounted!.container.querySelectorAll("div.border-b")].find((d) =>
        d.querySelector(`span[title="${username}"]`),
      );
      return [...(row?.querySelectorAll("button") ?? [])].map((b) => b.textContent?.trim());
    };
    expect(actionLabels("root")).toEqual([]);
    expect(actionLabels("alice")).toEqual(["禁用", "重置"]);
    expect(actionLabels("disabled")).toEqual(["重置"]);
    expect(actionLabels("reset")).toEqual(["重置"]);
  });

  it("submits the username and initial password when creating a user", async () => {
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("alice"));
    clickButton(mounted.container, "添加用户");
    await flushUntil(() => mounted!.container.querySelector('input[placeholder="用户名"]') !== null);

    setInputValue(mounted.container.querySelector<HTMLInputElement>('input[placeholder="用户名"]')!, "bob");
    setInputValue(mounted.container.querySelector<HTMLInputElement>('input[placeholder="初始密码（至少 8 位）"]')!, "bob-pw-123");
    await flush();
    clickButton(mounted.container, "创建");
    await flushUntil(() => mocks.createUser.mock.calls.length > 0);
    expect(mocks.createUser).toHaveBeenCalledWith("bob", "bob-pw-123", "");
    await flushUntil(() => mocks.users.mock.calls.length >= 2);
  });

  it("confirms before disabling a user, then calls the API and refreshes", async () => {
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("alice"));
    const aliceRow = [...mounted.container.querySelectorAll("div")].find(
      (d) => d.textContent?.includes("alice") && !d.textContent?.includes("（我）"),
    )!;
    const disableBtn = [...aliceRow.querySelectorAll("button")].find((b) => b.textContent?.trim() === "禁用")!;
    disableBtn.click();
    await flushUntil(() => mocks.ask.mock.calls.length > 0);
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("alice");
    await flushUntil(() => mocks.disableUser.mock.calls.length > 0);
    expect(mocks.disableUser).toHaveBeenCalledWith("u-alice");
  });

  it("does not disable the user when confirmation is canceled", async () => {
    mocks.ask.mockResolvedValue(false);
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("alice"));
    const aliceRow = [...mounted.container.querySelectorAll("div")].find(
      (d) => d.textContent?.includes("alice") && !d.textContent?.includes("（我）"),
    )!;
    const disableBtn = [...aliceRow.querySelectorAll("button")].find((b) => b.textContent?.trim() === "禁用")!;
    disableBtn.click();
    await flush();
    expect(mocks.disableUser).not.toHaveBeenCalled();
  });

  it("explains data keys, sessions, MFA bindings, and cloud ciphertext recovery in the reset confirmation", async () => {
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("alice"));
    const aliceRow = [...mounted.container.querySelectorAll("div")].find(
      (d) => d.textContent?.includes("alice") && !d.textContent?.includes("（我）"),
    )!;
    const resetBtn = [...aliceRow.querySelectorAll("button")].find((b) => b.textContent?.trim() === "重置")!;
    resetBtn.click();
    await flushUntil(() => mocks.ask.mock.calls.length > 0);
    const question = String(mocks.ask.mock.calls[0]?.[0]);
    expect(question).toContain("数据密钥");
    expect(question).toContain("所有会话");
    expect(question).toContain("两步验证绑定");
    expect(question).toContain("云端密文需从仍持有数据的设备重新同步");
    await flushUntil(() => mocks.resetUser.mock.calls.length > 0);
    expect(mocks.resetUser).toHaveBeenCalledWith("u-alice");
  });
});

describe("AccountCard registration toggle", () => {
  it("clears the prior admin's list, registration draft, and password when the account switches", async () => {
    mounted = mountCard();
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("alice"));
    clickButton(mounted.container, "添加用户");
    setInputValue(mounted.container.querySelector<HTMLInputElement>('input[placeholder="初始密码（至少 8 位）"]')!, "private-draft-123");
    act(() => mounted!.container.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());

    const oldUsers = deferred<{ users: (typeof SELF | typeof ALICE)[] }>();
    mocks.users.mockImplementationOnce(() => oldUsers.promise);
    mocks.users.mockResolvedValue({ users: [{ ...SELF, id: "u-admin-b", username: "root-b" }] });
    mocks.settingsGet.mockResolvedValueOnce({ registration_open: true, mfa_required: true });
    mocks.settingsGet.mockResolvedValue({ registration_open: false, mfa_required: false });
    clickButton(mounted.container, "刷新");
    await flushUntil(() => mocks.users.mock.calls.length === 2);

    act(() => useAuth.setState({ user: { ...SELF, id: "u-admin-b", username: "root-b" } }));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("root-b"));
    expect(mounted.container.textContent).not.toContain("alice");
    expect(mounted.container.querySelector('input[placeholder="初始密码（至少 8 位）"]')).toBeNull();
    expect(mounted.container.querySelector<HTMLInputElement>('input[type="checkbox"]')?.checked).toBe(false);

    await act(async () => { oldUsers.resolve({ users: [SELF, ALICE] }); });
    expect(mounted.container.textContent).not.toContain("alice");
    expect(mounted.container.querySelector<HTMLInputElement>('input[type="checkbox"]')?.checked).toBe(false);
  });

  it("loads the current toggle state and saves the new state", async () => {
    mounted = mountCard();
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("alice"));
    const checkbox = mounted.container.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
    expect(checkbox.checked).toBe(false);

    act(() => checkbox.click());
    expect(checkbox.checked).toBe(true);
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

  it("refreshes the /auth/status registration-toggle cache after saving", async () => {
    mounted = mountCard();
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("alice"));
    const checkbox = mounted.container.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
    act(() => checkbox.click());
    expect(checkbox.checked).toBe(true);
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
