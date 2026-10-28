/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flushUntil, mount, type MountedView } from "./reactTestUtils";
import { buttonWithText, passwordField, submitPasswordForm } from "./passwordFormTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    getPermission: vi.fn(),
    vaultStatus: vi.fn(),
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
      initMaster: vi.fn(),
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
vi.mock("../../features/settings/UpdateCard", () => ({ UpdateCard: () => null }));
vi.mock("../../features/ai/ModelPanel", () => ({ ModelManager: () => null }));

import { SettingsView } from "../../features/settings/SettingsView";
import { useUi } from "../../app/store";

const MASTER_VAULT = {
  initialized: true,
  mode: "master" as const,
  unlocked: true,
  autoLockMinutes: 30,
  passwordless: false,
  systemProtection: true,
};
const MASTER_VAULT_NO_SYSTEM = { ...MASTER_VAULT, systemProtection: false };
const PASSWORDLESS_SYSTEM_VAULT = {
  initialized: true,
  mode: "master" as const,
  unlocked: true,
  autoLockMinutes: 30,
  passwordless: true,
  systemProtection: true,
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

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
  mocks.vaultStatus.mockResolvedValue(MASTER_VAULT);
  mocks.initDpapi.mockResolvedValue(undefined);
  mocks.changePassword.mockResolvedValue(undefined);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("Desktop credential protection disable paths", () => {
  it("verifies the current password and uses DPAPI migration instead of an empty password when protection is toggled off", async () => {
    const { ask, promptText } = await import("../../ui/dialogs");
    vi.mocked(ask).mockResolvedValue(true);
    vi.mocked(promptText).mockResolvedValue("current-password");
    mounted = withClient(createElement(SettingsView));
    await flushUntil(() => (vaultCard()?.textContent ?? "").includes("已开启"));

    protectionToggle()!.click();
    await flushUntil(() => mocks.initDpapi.mock.calls.length > 0);

    expect(ask).toHaveBeenCalledWith(expect.stringContaining("系统级密钥保护"));
    expect(promptText).toHaveBeenCalledWith("输入当前保护密码：", "", { secret: true });
    expect(mocks.initDpapi).toHaveBeenCalledWith("current-password");
    expect(mocks.changePassword).not.toHaveBeenCalled();
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("success", "已关闭密码保护");
  });

  it("does not invoke any disable path when password entry is canceled", async () => {
    const { ask, promptText } = await import("../../ui/dialogs");
    vi.mocked(ask).mockResolvedValue(true);
    vi.mocked(promptText).mockResolvedValue(null);
    mounted = withClient(createElement(SettingsView));
    await flushUntil(() => (vaultCard()?.textContent ?? "").includes("已开启"));

    protectionToggle()!.click();
    await flushUntil(() => vi.mocked(promptText).mock.calls.length > 0);
    await flushUntil(() => (vaultCard()?.textContent ?? "").includes("已开启"));
    expect(mocks.initDpapi).not.toHaveBeenCalled();
    expect(mocks.changePassword).not.toHaveBeenCalled();
  });

  it("uses an empty password and warns about data-directory access when system passwordless protection is unavailable, such as on Linux desktop", async () => {
    mocks.vaultStatus.mockResolvedValue(MASTER_VAULT_NO_SYSTEM);
    const { ask, promptText } = await import("../../ui/dialogs");
    vi.mocked(ask).mockResolvedValue(true);
    vi.mocked(promptText).mockResolvedValue("current-password");
    mounted = withClient(createElement(SettingsView));
    await flushUntil(() => (vaultCard()?.textContent ?? "").includes("已开启"));

    protectionToggle()!.click();
    await flushUntil(() => mocks.changePassword.mock.calls.length > 0);

    expect(ask).toHaveBeenCalledWith(expect.stringContaining("任何获得数据目录的人均可解密"));
    expect(mocks.changePassword).toHaveBeenCalledWith("current-password", "");
    expect(mocks.initDpapi).not.toHaveBeenCalled();
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("success", "已关闭密码保护");
  });

  it("uses DPAPI migration instead of an empty password when both new fields are left blank", async () => {
    mounted = withClient(createElement(SettingsView));
    await flushUntil(() => (vaultCard()?.textContent ?? "").includes("已开启"));

    click(buttonWithText(vaultCard()!, "修改密码")!);
    await flushUntil(() => passwordField(vaultCard()!, "当前密码") !== null);
    expect(vaultCard()?.textContent).toContain("将关闭密码保护");
    await submitPasswordForm(vaultCard()!, { current: "current-password" }, "关闭密码保护");
    await flushUntil(() => mocks.initDpapi.mock.calls.length > 0);

    expect(mocks.initDpapi).toHaveBeenCalledWith("current-password");
    expect(mocks.changePassword).not.toHaveBeenCalled();
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("success", "已关闭密码保护 · 凭据改由系统级密钥保护");
  });
  it("keeps the form open with the error when the current password is wrong", async () => {
    mocks.unlock.mockRejectedValueOnce({ code: "bad_master_password", message: "incorrect protection password" });
    mounted = withClient(createElement(SettingsView));
    await flushUntil(() => (vaultCard()?.textContent ?? "").includes("已开启"));

    click(buttonWithText(vaultCard()!, "修改密码")!);
    await flushUntil(() => passwordField(vaultCard()!, "当前密码") !== null);
    await submitPasswordForm(vaultCard()!, { current: "wrong-password", next: "new-password-1" }, "确认修改");
    await flushUntil(() => (vaultCard()?.textContent ?? "").includes("保护密码不正确，请重新输入"));

    expect(mocks.unlock).toHaveBeenCalledWith("wrong-password");
    expect(mocks.changePassword).not.toHaveBeenCalled();
    expect(mocks.initDpapi).not.toHaveBeenCalled();
    expect(passwordField(vaultCard()!, "当前密码")).not.toBeNull();
  });
  it("distinguishes local key storage from sync upload requirements in desktop copy", async () => {
    mounted = withClient(createElement(SettingsView));
    await flushUntil(() =>
      (vaultCard()?.textContent ?? "").includes("已开启") &&
      (mounted!.container.textContent ?? "").includes("密钥加密保存在当前实例"),
    );
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("启用档案同步并解锁凭据库后");
    expect(text).toContain("端到端加密同步");
    expect(text).not.toContain("不会上传");
  });
});

describe("Desktop passwordless vault", () => {
  it("shows encryption enabled without a password, with set-password primary and disable-encryption secondary actions", async () => {
    mocks.vaultStatus.mockResolvedValue(PASSWORDLESS_SYSTEM_VAULT);
    mounted = withClient(createElement(SettingsView));
    await flushUntil(() => (vaultCard()?.textContent ?? "").includes("加密已启用 · 未设置密码"));

    const buttons = Array.from(vaultCard()?.querySelectorAll("button") ?? []).map(
      (b) => b.textContent,
    );
    expect(buttons).toContain("设置密码");
    expect(buttons).toContain("关闭加密");
    expect(buttons).not.toContain("修改密码");
    expect(protectionToggle()!.checked).toBe(false);
    expect(vaultCard()?.textContent).not.toContain("闲置自动锁定");
  });

  it("sets a new password with an empty old password without asking for the old password", async () => {
    mocks.vaultStatus.mockResolvedValue(PASSWORDLESS_SYSTEM_VAULT);
    mounted = withClient(createElement(SettingsView));
    await flushUntil(() => (vaultCard()?.textContent ?? "").includes("加密已启用 · 未设置密码"));

    click(buttonWithText(vaultCard()!, "设置密码")!);
    await flushUntil(() => passwordField(vaultCard()!, "新密码") !== null);
    expect(passwordField(vaultCard()!, "当前密码")).toBeNull();
    await submitPasswordForm(vaultCard()!, { next: "brand-new-password" }, "设置密码");
    await flushUntil(() => mocks.changePassword.mock.calls.length > 0);

    expect(mocks.unlock).toHaveBeenCalledWith("");
    expect(mocks.changePassword).toHaveBeenCalledWith("", "brand-new-password");
    expect(mocks.initDpapi).not.toHaveBeenCalled();
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("success", "已开启密码保护 · 凭据不受影响");
  });
  it("migrates to system key protection with an empty password after disable-encryption confirmation", async () => {
    mocks.vaultStatus.mockResolvedValue(PASSWORDLESS_SYSTEM_VAULT);
    const { ask, promptText } = await import("../../ui/dialogs");
    vi.mocked(ask).mockResolvedValue(true);
    mounted = withClient(createElement(SettingsView));
    await flushUntil(() => (vaultCard()?.textContent ?? "").includes("加密已启用 · 未设置密码"));

    const offButton = Array.from(vaultCard()?.querySelectorAll("button") ?? []).find(
      (b) => b.textContent === "关闭加密",
    )!;
    offButton.click();
    await flushUntil(() => mocks.initDpapi.mock.calls.length > 0);

    expect(ask).toHaveBeenCalledWith(expect.stringContaining("系统级密钥保护"));
    expect(mocks.initDpapi).toHaveBeenCalledWith("");
    expect(mocks.changePassword).not.toHaveBeenCalled();
    expect(promptText).not.toHaveBeenCalled();
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("success", "已关闭加密 · 凭据改由系统级密钥保护");
  });

  it("does not call any API when disable-encryption confirmation is canceled", async () => {
    mocks.vaultStatus.mockResolvedValue(PASSWORDLESS_SYSTEM_VAULT);
    const { ask } = await import("../../ui/dialogs");
    vi.mocked(ask).mockResolvedValue(false);
    mounted = withClient(createElement(SettingsView));
    await flushUntil(() => (vaultCard()?.textContent ?? "").includes("加密已启用 · 未设置密码"));

    const offButton = Array.from(vaultCard()?.querySelectorAll("button") ?? []).find(
      (b) => b.textContent === "关闭加密",
    )!;
    offButton.click();
    await flushUntil(() => vi.mocked(ask).mock.calls.length > 0);

    expect(mocks.initDpapi).not.toHaveBeenCalled();
    expect(mocks.changePassword).not.toHaveBeenCalled();
  });
});
