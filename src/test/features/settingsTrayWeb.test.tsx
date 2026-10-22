/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { flush, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    getPermission: vi.fn(),
    vaultStatus: vi.fn(),
    trayGet: vi.fn(),
    traySet: vi.fn(),
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
vi.mock("../../features/ai/ModelPanel", () => ({ ModelManager: () => null }));

import { SettingsView } from "../../features/settings/SettingsView";
import { setWindowsPlatform } from "../../app/platform";

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  setWindowsPlatform(true);
  mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
  mocks.vaultStatus.mockResolvedValue({ initialized: true, mode: "dpapi", unlocked: true, autoLockMinutes: 0 });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  setWindowsPlatform(false);
});

describe("web tray settings platform gate", () => {
  it("does not render or invoke desktop-only tray commands", async () => {
    mounted = mountWithClient(createElement(SettingsView));
    await flush();
    await flush();

    expect(mounted.container.textContent).not.toContain("关闭时保留后台运行");
    expect(mocks.trayGet).not.toHaveBeenCalled();
    expect(mocks.traySet).not.toHaveBeenCalled();
  });
});
