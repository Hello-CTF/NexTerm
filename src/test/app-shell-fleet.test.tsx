/** @vitest-environment jsdom */
// FLEET149 app shell: desktop also shows the "Device Management" entry (leading to the login onboarding view); entry display only, no HTTP.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flush, flushUntil, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    fetch: vi.fn(),
    assetList: vi.fn(),
    assetSearch: vi.fn(),
    sessionList: vi.fn(),
    vaultStatus: vi.fn(),
    updateCheck: vi.fn(),
  };
});

vi.mock("../ipc/commands", () => ({
  assetApi: { list: mocks.assetList, search: mocks.assetSearch, groupList: vi.fn(), snippetList: vi.fn() },
  sessionApi: {
    list: mocks.sessionList,
    connectLocal: vi.fn(),
    connect: vi.fn(),
    reconnect: vi.fn(),
    probe: vi.fn(),
  },
  vaultApi: { status: mocks.vaultStatus, listCredentials: vi.fn() },
  syncApi: { linkGet: vi.fn().mockResolvedValue({ url: "" }) },
  appUpdateApi: { check: mocks.updateCheck },
  terminalApi: {},
  dbApi: {},
}));

vi.mock("../features/settings/useWebAutoSync", () => ({
  useDesktopAutoSync: () => {},
  useWebAutoSync: () => {},
}));
vi.mock("../features/terminal/TerminalPane", () => ({ TerminalPane: () => null }));
vi.mock("../app/layout", () => ({ layoutBootstrapped: Promise.resolve(), startLayoutSync: () => {} }));
vi.mock("../features/terminal/BackgroundSessions", () => ({ BackgroundSessions: () => null }));
vi.mock("../features/files/FileBrowser", () => ({ FileBrowser: () => null }));
vi.mock("../features/files/FileTree", () => ({ FileTree: () => null }));
vi.mock("../features/files/MountPanel", () => ({ MountPanel: () => null }));
vi.mock("../features/files/FileEditor", () => ({ FileEditor: () => null }));
vi.mock("../features/forward/ForwardPanel", () => ({ ForwardPanel: () => null }));
vi.mock("../features/docker/DockerPanel", () => ({ DockerPanel: () => null }));
vi.mock("../features/db/DbPanel", () => ({ DbPanel: () => null }));
vi.mock("../features/ai/AiSidebar", () => ({ AiSidebar: () => null }));
vi.mock("../features/settings/SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../features/explorer/AssetTree", () => ({ AssetTree: () => null }));
vi.mock("../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));
vi.mock("../app/UpdateBanner", () => ({ UpdateBanner: () => null }));
vi.mock("../app/unread", () => ({ startUnreadTracking: () => {} }));

import App from "../app/App";
import { useUi } from "../app/store";
import { useUpdateStore } from "../app/useUpdate";

let mounted: MountedView | undefined;

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.assetList.mockResolvedValue([]);
  mocks.assetSearch.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  mocks.updateCheck.mockResolvedValue({
    available: false,
    currentVersion: "0.2.2",
    version: "",
    notes: "",
    prerelease: false,
    assetName: "",
    assetSize: 0,
    canInstall: true,
    unavailableReason: "",
  });
  useUi.setState({ toasts: [], sessions: [], leftOpen: true, rightOpen: false, leftMode: "assets" });
  useUpdateStore.setState({ status: null, checking: false, checkError: null, remindedVersion: null });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("app shell device management navigation", () => {
  it("shows the device management entry on desktop without making HTTP requests", async () => {
    vi.stubGlobal("fetch", mocks.fetch);
    mounted = mountApp();
    await flushUntil(() => document.querySelector('button[aria-label="设置"]') !== null);
    await flush();

    expect(document.querySelector('button[aria-label="设备管理"]')).not.toBeNull();
    expect(mocks.fetch).not.toHaveBeenCalled();
  });
});

describe("desktop update checks", () => {
  it("checks at startup and after four hours, then stops after unmount", async () => {
    vi.useFakeTimers();
    mounted = mountApp();
    await flush();
    expect(mocks.updateCheck).toHaveBeenCalledTimes(1);

    await act(() => vi.advanceTimersByTimeAsync(4 * 60 * 60 * 1000));
    expect(mocks.updateCheck).toHaveBeenCalledTimes(2);

    mounted.unmount();
    mounted = undefined;
    await act(() => vi.advanceTimersByTimeAsync(4 * 60 * 60 * 1000));
    expect(mocks.updateCheck).toHaveBeenCalledTimes(2);
  });
});
