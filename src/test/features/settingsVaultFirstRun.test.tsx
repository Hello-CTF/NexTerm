/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    getPermission: vi.fn(),
    vaultStatus: vi.fn(),
    initMaster: vi.fn(),
    initDpapi: vi.fn(),
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
};
const DPAPI_VAULT = { initialized: true, mode: "dpapi" as const, unlocked: true, autoLockMinutes: 30, passwordless: false };
const PASSWORDLESS_VAULT = {
  initialized: true,
  mode: "master" as const,
  unlocked: true,
  autoLockMinutes: 30,
  passwordless: true,
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
  mocks.changePassword.mockResolvedValue(undefined);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("SettingsView 凭据保护首次初始化引导", () => {
  it("未初始化时显示「未初始化」并说明初始化前无法保存凭据", async () => {
    mounted = withClient(createElement(SettingsView));
    await waitForVaultText("未初始化");

    const text = vaultCard()?.textContent ?? "";
    expect(text).toContain("打开这个开关并设置保护密码即可完成初始化");
    expect(text).toContain("初始化前无法保存任何密码或私钥");
    expect(text).not.toContain("无需密码");
  });

  it("勾选开关后设置密码即完成初始化并刷新状态", async () => {
    mounted = withClient(createElement(SettingsView));
    await waitForVaultText("尚未初始化");

    protectionToggle()!.click();
    await flushUntil(() => passwordInput() !== null);
    expect(vaultCard()?.textContent).toContain("忘记后无法找回");

    setInputValue(passwordInput()!, "first-run-password");
    const enableButton = Array.from(vaultCard()!.querySelectorAll("button")).find(
      (b) => b.textContent === "启用保护",
    );
    expect(enableButton).toBeDefined();
    enableButton!.click();
    await flushUntil(() => mocks.initMaster.mock.calls.length > 0);

    expect(mocks.initMaster).toHaveBeenCalledWith("first-run-password");
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已开启密码保护"));
    await flushUntil(() => mocks.vaultStatus.mock.calls.length >= 2);
    await flush();
    expect(passwordInput()).toBeNull();
  });

  it("主密码模式显示已开启与每次启动需输入密码", async () => {
    mocks.vaultStatus.mockResolvedValue(MASTER_VAULT);
    mounted = withClient(createElement(SettingsView));
    await waitForVaultText("已开启");

    const text = vaultCard()?.textContent ?? "";
    expect(text).toContain("每次启动需输入密码后方可使用凭据");
    expect(protectionToggle()!.checked).toBe(true);
  });

  it("系统免密模式显示未开启与系统级密钥保护说明", async () => {
    mocks.vaultStatus.mockResolvedValue(DPAPI_VAULT);
    mounted = withClient(createElement(SettingsView));
    await waitForVaultText("未开启");

    const text = vaultCard()?.textContent ?? "";
    expect(text).toContain("凭据由系统级密钥保护，启动后直接使用");
    expect(text).not.toContain("尚未初始化");
  });

  it("留空密码也能完成初始化", async () => {
    mounted = withClient(createElement(SettingsView));
    await waitForVaultText("尚未初始化");

    protectionToggle()!.click();
    await flushUntil(() => passwordInput() !== null);
    expect(vaultCard()?.textContent).toContain("留空则不设置密码");

    const enableButton = Array.from(vaultCard()!.querySelectorAll("button")).find(
      (b) => b.textContent === "启用保护",
    );
    expect(enableButton).toBeDefined();
    expect(enableButton!.disabled).toBe(false);
    enableButton!.click();
    await flushUntil(() => mocks.initMaster.mock.calls.length > 0);

    expect(mocks.initMaster).toHaveBeenCalledWith("");
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("success", "已完成初始化，未设置密码");
  });

  it("无密码凭据库显示未开启，且不展示自动锁定", async () => {
    mocks.vaultStatus.mockResolvedValue(PASSWORDLESS_VAULT);
    mounted = withClient(createElement(SettingsView));
    await waitForVaultText("未开启");

    const text = vaultCard()?.textContent ?? "";
    expect(text).toContain("未设置保护密码，凭据加密保存、无需密码即可使用，但任何拿到数据目录的人都能解密");
    expect(text).not.toContain("闲置自动锁定");
    expect(protectionToggle()!.checked).toBe(false);
  });

  it("取消勾选开关时改空密码关闭保护", async () => {
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
