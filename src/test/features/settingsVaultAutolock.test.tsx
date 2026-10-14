/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    getPermission: vi.fn(),
    vaultStatus: vi.fn(),
    setAutoLock: vi.fn(),
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
      initDpapi: vi.fn(),
      initMaster: vi.fn(),
      changePassword: vi.fn(),
      setAutoLock: mocks.setAutoLock,
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

const MASTER_VAULT = {
  initialized: true,
  mode: "master" as const,
  unlocked: true,
  autoLockMinutes: 30,
};
const DPAPI_VAULT = { initialized: true, mode: "dpapi" as const, unlocked: true, autoLockMinutes: 30 };

let mounted: MountedView | undefined;
let client: QueryClient | undefined;

function withClient(node: React.ReactElement): MountedView {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function autoLockInput(): HTMLInputElement | null {
  return mounted?.container.querySelector<HTMLInputElement>('section input[type="number"]') ?? null;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
  mocks.vaultStatus.mockResolvedValue(MASTER_VAULT);
  mocks.setAutoLock.mockResolvedValue(undefined);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("SettingsView vault idle auto-lock", () => {
  it("shows the current value in master-password mode and calls vault_set_autolock on save", async () => {
    mounted = withClient(createElement(SettingsView));
    await flushUntil(() => autoLockInput() !== null);
    expect(autoLockInput()!.value).toBe("30");
    setInputValue(autoLockInput()!, "5");
    clickButton(mounted.container, "保存");
    await flushUntil(() => mocks.setAutoLock.mock.calls.length > 0);
    expect(mocks.setAutoLock).toHaveBeenCalledWith(5);
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("success", "闲置 5 分钟后自动锁定");
    await flushUntil(() => mocks.vaultStatus.mock.calls.length >= 2);
  });

  it("refreshes through the shared query after vault-status changes elsewhere without remounting", async () => {
    mounted = withClient(createElement(SettingsView));
    await flushUntil(() => autoLockInput() !== null);
    expect(autoLockInput()!.value).toBe("30");

    mocks.vaultStatus.mockResolvedValue(DPAPI_VAULT);
    await act(() => client!.invalidateQueries({ queryKey: ["vault-status"] }));
    await flush();

    expect(autoLockInput()).toBeNull();
  });

  it("saves zero to disable auto-lock", async () => {
    mounted = withClient(createElement(SettingsView));
    await flushUntil(() => autoLockInput() !== null);
    setInputValue(autoLockInput()!, "0");
    clickButton(mounted.container, "保存");
    await flushUntil(() => mocks.setAutoLock.mock.calls.length > 0);
    expect(mocks.setAutoLock).toHaveBeenCalledWith(0);
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("success", "已禁用闲置自动锁定");
  });

  it("rejects out-of-range and non-integer input without making a call", async () => {
    mounted = withClient(createElement(SettingsView));
    await flushUntil(() => autoLockInput() !== null);
    for (const bad of ["1441", "-1", "1.5", ""]) {
      setInputValue(autoLockInput()!, bad);
      clickButton(mounted.container, "保存");
      await flush();
      expect(mocks.setAutoLock).not.toHaveBeenCalled();
      expect(mocks.toast).toHaveBeenCalledWith("error", "自动锁定时长须为 0 至 1440 的整数分钟，0 表示禁用");
      mocks.toast.mockClear();
    }
  });

  it("shows the error on save failure without an optimistic update", async () => {
    mocks.setAutoLock.mockRejectedValueOnce({ code: "bad_param", message: "auto-lock minutes must be between 0 (disabled) and 1440" });
    mounted = withClient(createElement(SettingsView));
    await flushUntil(() => autoLockInput() !== null);
    setInputValue(autoLockInput()!, "1440");
    clickButton(mounted.container, "保存");
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("error", "参数错误：auto-lock minutes must be between 0 (disabled) and 1440");
    expect(autoLockInput()!.value).toBe("1440");
  });

  it("hides auto-lock settings outside master-password mode", async () => {
    mocks.vaultStatus.mockResolvedValue(DPAPI_VAULT);
    mounted = withClient(createElement(SettingsView));
    await flush();
    await flush();
    expect(autoLockInput()).toBeNull();
  });
});
