/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";
import { buttonWithText, passwordField, submitPasswordForm } from "./passwordFormTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    getPermission: vi.fn(),
    vaultStatus: vi.fn(),
    initMaster: vi.fn(),
    initDpapi: vi.fn(),
    unlock: vi.fn(),
    changePassword: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    aiApi: {
      getPermission: mocks.getPermission,
      setPermission: vi.fn(),
      testProvider: vi.fn(),
    },
    vaultApi: {
      status: mocks.vaultStatus,
      initDpapi: mocks.initDpapi,
      initMaster: mocks.initMaster,
      unlock: mocks.unlock,
      changePassword: mocks.changePassword,
      setAutoLock: vi.fn(),
    },
    assetApi: {
      auditQuery: vi.fn().mockResolvedValue([]),
    },
  };
});
vi.mock("../../ui/dialogs", () => ({ ask: vi.fn(), promptText: vi.fn() }));
vi.mock("../../features/settings/MemoryCard", () => ({ MemoryCard: () => null }));
vi.mock("../../features/settings/KnownHostsCard", () => ({ KnownHostsCard: () => null }));
vi.mock("../../features/settings/SyncCard", () => ({ SyncCard: () => null }));
vi.mock("../../features/ai/ModelPanel", () => ({ ModelManager: () => null }));

import { SettingsView } from "../../features/settings/SettingsView";
import { useUi } from "../../app/store";

const NOT_INIT_VAULT = {
  initialized: false,
  mode: "not_init" as const,
  unlocked: false,
  autoLockMinutes: 30,
  passwordless: false,
};
const MASTER_VAULT = {
  initialized: true,
  mode: "master" as const,
  unlocked: true,
  autoLockMinutes: 30,
  passwordless: false,
  systemProtection: false,
};
const DPAPI_VAULT = { initialized: true, mode: "dpapi" as const, unlocked: true, autoLockMinutes: 30, passwordless: false };
const PASSWORDLESS_VAULT = {
  initialized: true,
  mode: "master" as const,
  unlocked: true,
  autoLockMinutes: 30,
  passwordless: true,
  systemProtection: false,
};

let mounted: MountedView | undefined;

function withClient(node: React.ReactElement): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function vaultCard(): HTMLElement | null {
  for (const section of mounted?.container.querySelectorAll("section") ?? []) {
    if (section.textContent?.includes("凭据保护")) return section;
  }
  return null;
}

function protectionToggle(): HTMLInputElement | null {
  return vaultCard()?.querySelector<HTMLInputElement>('input[type="checkbox"]') ?? null;
}

function passwordInput(): HTMLInputElement | null {
  return vaultCard()?.querySelector<HTMLInputElement>('input[type="password"]') ?? null;
}

async function waitForVaultText(fragment: string) {
  await flushUntil(() => (vaultCard()?.textContent ?? "").includes(fragment));
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
  mocks.vaultStatus.mockResolvedValue(NOT_INIT_VAULT);
  mocks.initMaster.mockResolvedValue(undefined);
  mocks.unlock.mockResolvedValue(undefined);
  mocks.changePassword.mockResolvedValue(undefined);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("SettingsView first-run credential protection guidance", () => {
  it("explains that credentials cannot be saved before protection is initialized", async () => {
    mounted = withClient(createElement(SettingsView));
    await waitForVaultText("未初始化");

    const text = vaultCard()?.textContent ?? "";
    expect(text).toContain("尚未初始化：启用保护后才能保存密码或私钥");
    expect(text).not.toContain("无需密码");
  });

  it("initializes protection and refreshes state after the toggle is enabled and a password is set", async () => {
    mounted = withClient(createElement(SettingsView));
    await waitForVaultText("尚未初始化");

    mocks.vaultStatus.mockResolvedValue(MASTER_VAULT);
    click(protectionToggle()!);
    await flushUntil(() => passwordInput() !== null);
    expect(vaultCard()?.textContent).toContain("忘记密码后，已保存的凭据将无法恢复");

    setInputValue(passwordInput()!, "first-run-password");
    const enableButton = Array.from(vaultCard()!.querySelectorAll("button")).find(
      (b) => b.textContent === "启用保护",
    );
    expect(enableButton).toBeDefined();
    click(enableButton!);
    await flushUntil(() =>
      mocks.initMaster.mock.calls.length > 0 && mocks.toast.mock.calls.length > 0 &&
      (vaultCard()?.textContent ?? "").includes("已开启") && passwordInput() === null,
    );

    expect(mocks.initMaster).toHaveBeenCalledWith("first-run-password");
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已开启密码保护"));
    expect(mocks.vaultStatus.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(passwordInput()).toBeNull();
  });

  it("shows enabled state and startup password requirement in master-password mode", async () => {
    mocks.vaultStatus.mockResolvedValue(MASTER_VAULT);
    mounted = withClient(createElement(SettingsView));
    await waitForVaultText("已开启");

    const text = vaultCard()?.textContent ?? "";
    expect(text).toContain("每次启动需输入密码后方可使用凭据");
    expect(protectionToggle()!.checked).toBe(true);
  });

  it("shows disabled state and system key protection guidance in system passwordless mode", async () => {
    mocks.vaultStatus.mockResolvedValue(DPAPI_VAULT);
    mounted = withClient(createElement(SettingsView));
    await waitForVaultText("未开启");

    const text = vaultCard()?.textContent ?? "";
    expect(text).toContain("凭据由系统级密钥保护，启动后直接使用");
    expect(text).not.toContain("尚未初始化");
  });

  it("allows initialization with a blank password", async () => {
    mounted = withClient(createElement(SettingsView));
    await waitForVaultText("尚未初始化");

    mocks.vaultStatus.mockResolvedValue(MASTER_VAULT);
    click(protectionToggle()!);
    await flushUntil(() => passwordInput() !== null);
    expect(vaultCard()?.textContent).toContain("留空则不设密码");

    const enableButton = Array.from(vaultCard()!.querySelectorAll("button")).find(
      (b) => b.textContent === "启用保护",
    );
    expect(enableButton).toBeDefined();
    expect(enableButton!.disabled).toBe(false);
    click(enableButton!);
    await flushUntil(() =>
      mocks.initMaster.mock.calls.length > 0 && mocks.toast.mock.calls.length > 0 &&
      (vaultCard()?.textContent ?? "").includes("已开启") && passwordInput() === null,
    );

    expect(mocks.initMaster).toHaveBeenCalledWith("");
    expect(mocks.toast).toHaveBeenCalledWith("success", "已完成初始化，未设置密码");
    expect(mocks.vaultStatus.mock.calls.length).toBeGreaterThanOrEqual(2);
  });

  it("shows encryption enabled without a password and the set-password entry in a passwordless vault, without auto-lock", async () => {
    mocks.vaultStatus.mockResolvedValue(PASSWORDLESS_VAULT);
    mounted = withClient(createElement(SettingsView));
    await waitForVaultText("加密已启用 · 未设密码");

    const text = vaultCard()?.textContent ?? "";
    expect(text).toContain("凭据加密保存、无需密码即可使用，但任何拿到数据目录的人都能解密");
    expect(text).not.toContain("未开启");
    expect(text).not.toContain("尚未初始化");
    expect(text).not.toContain("闲置自动锁定");
    expect(text).not.toContain("锁定与解锁在「凭据」页");
    expect(protectionToggle()!.checked).toBe(false);
    const buttons = Array.from(vaultCard()!.querySelectorAll("button")).map((b) => b.textContent);
    expect(buttons).toContain("设置密码");
    expect(buttons).not.toContain("修改密码");
  });

  it("uses the change-password flow with an empty old password when setting a password in a passwordless vault", async () => {
    mocks.vaultStatus.mockResolvedValue(PASSWORDLESS_VAULT);
    mounted = withClient(createElement(SettingsView));
    await waitForVaultText("加密已启用 · 未设密码");

    click(buttonWithText(vaultCard()!, "设置密码")!);
    await flushUntil(() => passwordField(vaultCard()!, "新密码") !== null);
    await submitPasswordForm(vaultCard()!, { next: "brand-new-password" }, "设置密码");
    await flushUntil(() => mocks.changePassword.mock.calls.length > 0);

    expect(mocks.unlock).toHaveBeenCalledWith("");
    expect(mocks.changePassword).toHaveBeenCalledWith("", "brand-new-password");
    expect(mocks.initMaster).not.toHaveBeenCalled();
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("success", "已开启密码保护 · 凭据不受影响");
    await flushUntil(() => mocks.vaultStatus.mock.calls.length >= 2);
  });
  it("enters the set-password flow instead of reinitializing when the passwordless vault toggle is changed", async () => {
    mocks.vaultStatus.mockResolvedValue(PASSWORDLESS_VAULT);
    mounted = withClient(createElement(SettingsView));
    await waitForVaultText("加密已启用 · 未设密码");

    click(protectionToggle()!);
    await flushUntil(() => passwordField(vaultCard()!, "新密码") !== null);
    await submitPasswordForm(vaultCard()!, { next: "brand-new-password" }, "设置密码");
    await flushUntil(() => mocks.changePassword.mock.calls.length > 0);

    expect(mocks.initMaster).not.toHaveBeenCalled();
    expect(mocks.changePassword).toHaveBeenCalledWith("", "brand-new-password");
    await flushUntil(() => passwordInput() === null);
  });
  it("cannot set an empty or mismatched password and cancels without changes", async () => {
    mocks.vaultStatus.mockResolvedValue(PASSWORDLESS_VAULT);
    mounted = withClient(createElement(SettingsView));
    await waitForVaultText("加密已启用 · 未设密码");

    click(buttonWithText(vaultCard()!, "设置密码")!);
    await flushUntil(() => passwordField(vaultCard()!, "新密码") !== null);
    expect(buttonWithText(vaultCard()!, "设置密码")?.disabled).toBe(true);
    setInputValue(passwordField(vaultCard()!, "新密码")!, "brand-new-password");
    setInputValue(passwordField(vaultCard()!, "确认新密码")!, "brand-new-passw0rd");
    await flush();
    expect(vaultCard()?.textContent).toContain("两次输入不一致");
    expect(buttonWithText(vaultCard()!, "设置密码")?.disabled).toBe(true);
    click(buttonWithText(vaultCard()!, "取消")!);
    await flush();

    expect(passwordField(vaultCard()!, "新密码")).toBeNull();
    expect(mocks.changePassword).not.toHaveBeenCalled();
    expect(mocks.initMaster).not.toHaveBeenCalled();
  });
  it("uses an empty password to disable protection when the toggle is switched off", async () => {
    mocks.vaultStatus.mockResolvedValue(MASTER_VAULT);
    const { ask, promptText } = await import("../../ui/dialogs");
    vi.mocked(ask).mockResolvedValue(true);
    vi.mocked(promptText).mockResolvedValue("current-password");
    mounted = withClient(createElement(SettingsView));
    await waitForVaultText("已开启");

    protectionToggle()!.click();
    await flushUntil(() => mocks.changePassword.mock.calls.length > 0);

    expect(ask).toHaveBeenCalledWith(expect.stringContaining("任何拿到数据目录的人都能解密"));
    expect(mocks.changePassword).toHaveBeenCalledWith("current-password", "");
    expect(mocks.initDpapi).not.toHaveBeenCalled();
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("success", "已关闭密码保护");
  });
});
