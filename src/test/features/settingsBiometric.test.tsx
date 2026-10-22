/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, flush, flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    getPermission: vi.fn(),
    vaultStatus: vi.fn(),
    unlock: vi.fn(),
    changePassword: vi.fn(),
    bioStatus: vi.fn(),
    bioEnable: vi.fn(),
    promptText: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../app/platform", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../app/platform")>();
  return { ...actual, isMobile: () => true, isWindows: () => false };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    aiApi: { getPermission: mocks.getPermission, setPermission: vi.fn(), testProvider: vi.fn() },
    vaultApi: {
      status: mocks.vaultStatus,
      initDpapi: vi.fn(),
      initMaster: vi.fn(),
      unlock: mocks.unlock,
      changePassword: mocks.changePassword,
      setAutoLock: vi.fn(),
    },
    biometricApi: { status: mocks.bioStatus, enable: mocks.bioEnable },
    assetApi: { auditQuery: vi.fn().mockResolvedValue([]) },
  };
});
vi.mock("../../ui/dialogs", () => ({ ask: vi.fn(), promptText: mocks.promptText }));
vi.mock("../../features/settings/MemoryCard", () => ({ MemoryCard: () => null }));
vi.mock("../../features/settings/KnownHostsCard", () => ({ KnownHostsCard: () => null }));
vi.mock("../../features/settings/SyncCard", () => ({ SyncCard: () => null }));
vi.mock("../../features/settings/UpdateCard", () => ({ UpdateCard: () => null }));
vi.mock("../../features/ai/ModelPanel", () => ({ ModelManager: () => null }));

import { SettingsView } from "../../features/settings/SettingsView";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;

function vaultCard(): HTMLElement | null {
  for (const section of mounted?.container.querySelectorAll("section") ?? []) {
    if (section.textContent?.includes("凭据保护")) return section;
  }
  return null;
}

async function changePassword(): Promise<void> {
  clickButton(vaultCard()!, "修改密码");
  await flushUntil(() => mocks.changePassword.mock.calls.length === 1);
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
  mocks.vaultStatus.mockResolvedValue({
    initialized: true,
    mode: "master",
    unlocked: true,
    autoLockMinutes: 30,
    passwordless: false,
    systemProtection: false,
  });
  mocks.unlock.mockResolvedValue(undefined);
  mocks.changePassword.mockResolvedValue(undefined);
  mocks.bioStatus.mockResolvedValue({ available: true, canEvaluate: true, hasMaterial: true });
  mocks.bioEnable.mockResolvedValue(undefined);
  mocks.promptText
    .mockResolvedValueOnce("old-master-password")
    .mockResolvedValueOnce("new-master-password");
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("mobile biometric material after password changes", () => {
  it("rewrites enrolled unlock material with the new password", async () => {
    mounted = mountWithClient(createElement(SettingsView));
    await flushUntil(() => (vaultCard()?.textContent ?? "").includes("已开启"));

    await changePassword();
    await flushUntil(() => mocks.bioEnable.mock.calls.length === 1);

    expect(mocks.unlock).toHaveBeenCalledWith("old-master-password");
    expect(mocks.changePassword).toHaveBeenCalledWith("old-master-password", "new-master-password");
    expect(mocks.bioStatus).toHaveBeenCalledTimes(1);
    expect(mocks.bioEnable).toHaveBeenCalledWith("new-master-password");
  });

  it("does not store the new password when no biometric material is enrolled", async () => {
    mocks.bioStatus.mockResolvedValue({ available: true, canEvaluate: true, hasMaterial: false });
    mounted = mountWithClient(createElement(SettingsView));
    await flushUntil(() => (vaultCard()?.textContent ?? "").includes("已开启"));

    await changePassword();
    await flushUntil(() => mocks.toast.mock.calls.some(([kind, text]) => kind === "success" && text === "密码已修改 · 凭据不受影响"));
    await flush();

    expect(mocks.changePassword).toHaveBeenCalledWith("old-master-password", "new-master-password");
    expect(mocks.bioEnable).not.toHaveBeenCalled();
  });
});
