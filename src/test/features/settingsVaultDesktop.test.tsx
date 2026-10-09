/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    getPermission: vi.fn(),
    vaultStatus: vi.fn(),
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
      initMaster: vi.fn(),
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

describe("桌面端凭据保护关闭路径", () => {
  it("取消勾选开关校验当前密码后走 DPAPI 迁移，而不是空口令", async () => {
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

  it("取消勾选时放弃输入则不调用任何关闭路径", async () => {
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
});
