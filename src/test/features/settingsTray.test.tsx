/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flush, flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    getPermission: vi.fn(),
    vaultStatus: vi.fn(),
    trayGet: vi.fn(),
    traySet: vi.fn(),
    toast: vi.fn(),
  };
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
      unlock: vi.fn(),
      changePassword: vi.fn(),
      setAutoLock: vi.fn(),
    },
    desktopTrayApi: { get: mocks.trayGet, set: mocks.traySet },
    assetApi: { auditQuery: vi.fn().mockResolvedValue([]) },
  };
});
vi.mock("../../ui/dialogs", () => ({ ask: vi.fn(), promptText: vi.fn() }));
vi.mock("../../features/settings/MemoryCard", () => ({ MemoryCard: () => null }));
vi.mock("../../features/settings/KnownHostsCard", () => ({ KnownHostsCard: () => null }));
vi.mock("../../features/settings/SyncCard", () => ({ SyncCard: () => null }));
vi.mock("../../features/settings/UpdateCard", () => ({ UpdateCard: () => null }));
vi.mock("../../features/ai/ModelPanel", () => ({ ModelManager: () => null }));

import { SettingsView } from "../../features/settings/SettingsView";
import { setWindowsPlatform } from "../../app/platform";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;

function trayToggle(): HTMLInputElement | null {
  for (const label of mounted?.container.querySelectorAll("label") ?? []) {
    if (label.textContent?.includes("关闭时保留后台运行")) return label.querySelector("input[type='checkbox']");
  }
  return null;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  setWindowsPlatform(false);
  useUi.setState({ pushToast: mocks.toast });
  mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
  mocks.vaultStatus.mockResolvedValue({ initialized: true, mode: "dpapi", unlocked: true, autoLockMinutes: 0 });
  mocks.trayGet.mockResolvedValue({ enabled: true });
  mocks.traySet.mockResolvedValue({ enabled: false });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  setWindowsPlatform(false);
});

describe("desktop tray settings platform gate", () => {
  it("loads and updates the tray preference on Windows", async () => {
    setWindowsPlatform(true);
    mounted = mountWithClient(createElement(SettingsView));
    await flushUntil(() => trayToggle()?.checked === true);

    expect(mocks.trayGet).toHaveBeenCalledTimes(1);
    click(trayToggle()!);
    await flushUntil(() => mocks.traySet.mock.calls.length === 1);
    await flushUntil(() => trayToggle()?.disabled === false);
    expect(mocks.traySet).toHaveBeenCalledWith(false);
  });

  it("does not render or invoke tray commands on non-Windows desktop", async () => {
    mounted = mountWithClient(createElement(SettingsView));
    await flush();
    await flush();

    expect(trayToggle()).toBeNull();
    expect(mocks.trayGet).not.toHaveBeenCalled();
    expect(mocks.traySet).not.toHaveBeenCalled();
  });
});
