/** @vitest-environment jsdom */

import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, Fragment } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, deferred, flush, flushUntil, mount, setInputValue, setSelectValue, type MountedView } from "./reactTestUtils";
import { notifyAssetDataChanged } from "../../ipc/assetEvents";
import type { SshImportPreviewDto } from "../../ipc/types";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  assetUpdate: vi.fn(),
  callDesktop: vi.fn(),
  groupList: vi.fn(),
  snippetList: vi.fn(),
  sessionList: vi.fn(),
  sessionReconnect: vi.fn(),
  probeHostKey: vi.fn(),
  vaultStatus: vi.fn(),
  listCredentials: vi.fn(),
  syncLinkGet: vi.fn(),
  syncStatus: vi.fn(),
  sshPreview: vi.fn(),
  sshApply: vi.fn(),
  listLive: vi.fn(),
  recordingStatus: vi.fn(),
  stale: false,
  listenError: false,
  attachSeq: 0,
  syncHandlers: [] as { topic: string; handler: (payload: unknown) => void }[],
}));

const asset = {
  id: "a1",
  groupId: null,
  kind: "ssh",
  name: "web-01",
  host: "127.0.0.1",
  port: 2201,
  username: "root",
  authKind: "agent",
  keyPath: null,
  credId: null,
  options: {},
  tags: "",
  note: "",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
  deletedAt: null,
  builtin: false,
};

const preview: SshImportPreviewDto = {
  source: "ssh-config",
  path: "/tmp/config",
  hosts: [{
    id: "h1",
    alias: "web-01",
    hostname: "127.0.0.1",
    port: 2202,
    username: "root",
    identityFiles: [],
    keyName: "",
    keyFingerprint: "",
    proxyJump: "",
    authMethod: "agent",
    source: "ssh-config",
    action: "conflict-alias",
    warnings: ["Alias web-01 is already used by another endpoint"],
  }],
  keys: [],
  diagnostics: [],
  truncated: false,
};

function sessionRows(stale: boolean) {
  return [{
    id: "s1",
    assetId: "a1",
    name: "web-01",
    kind: "ssh",
    status: "connected" as const,
    tabs: ["kernel-1", "kernel-2"],
    createdAt: 0,
    staleConfig: stale,
  }];
}

vi.mock("../../ipc/env", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/env")>();
  return { ...actual, WEB: false, TRANSPORT: "desktop", DESKTOP: true };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return createCommandsMock(actual, {
    assetApi: {
      // The save flow under test runs the real update implementation down to
      // the mocked callDesktop transport (see the ../../ipc/wails mock).
      update: actual.assetApi.update,
      list: mocks.assetList,
      search: vi.fn(),
      searchMeta: vi.fn(),
      groupList: mocks.groupList,
      snippetList: mocks.snippetList,
    },
    sessionApi: {
      list: mocks.sessionList,
      connectLocal: vi.fn(),
      connect: vi.fn(),
      reconnect: mocks.sessionReconnect,
      disconnect: vi.fn(),
      probe: vi.fn(),
      probeHostKey: mocks.probeHostKey,
    },
    vaultApi: { status: mocks.vaultStatus, listCredentials: mocks.listCredentials },
    syncApi: { linkGet: mocks.syncLinkGet, status: mocks.syncStatus },
    layoutApi: {
      get: vi.fn().mockResolvedValue({ revision: 0, updatedAt: 0, data: null }),
      put: vi.fn().mockResolvedValue({ saved: true, revision: 1, conflict: false }),
    },
    terminalApi: {
      listLive: mocks.listLive,
      recordingStatus: mocks.recordingStatus,
      write: vi.fn().mockResolvedValue(undefined),
      detach: vi.fn().mockResolvedValue(undefined),
      setVisible: vi.fn().mockResolvedValue(undefined),
      recordStart: vi.fn().mockResolvedValue(undefined),
      recordStop: vi.fn().mockResolvedValue(0),
      closeTab: vi.fn().mockResolvedValue(undefined),
    },
    sshImportApi: { preview: mocks.sshPreview, apply: mocks.sshApply },
  });
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: vi.fn((topic: string, handler: (payload: unknown) => void) => {
      if (mocks.listenError && (topic === "sync://status" || topic === "sync://round-complete")) {
        throw new Error("event adapter unavailable");
      }
      mocks.syncHandlers.push({ topic, handler });
      return Promise.resolve(() => {
        const index = mocks.syncHandlers.findIndex((entry) => entry.topic === topic && entry.handler === handler);
        if (index >= 0) mocks.syncHandlers.splice(index, 1);
      });
    }),
  };
});

vi.mock("../../ipc/webTransport", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/webTransport")>();
  return { ...actual, onEventsResync: () => () => {} };
});

vi.mock("../../app/layout", () => ({
  layoutBootstrapped: Promise.resolve(),
  startLayoutSync: () => {},
  advanceLayoutAccount: () => {},
  finishLayoutRestore: vi.fn(),
  layoutRestoreStatus: () => ({ pending: false, preview: false, count: 0 }),
  onRemoteChange: () => {},
  reviewPendingLayoutRestore: vi.fn(),
  subscribeLayoutRestore: () => () => {},
  undoLayoutRestore: vi.fn(),
}));
vi.mock("../../app/unread", () => ({ startUnreadTracking: () => {}, resetUnreadTracking: () => {} }));
vi.mock("../../app/useUpdate", () => ({ getUpdateState: () => ({ check: vi.fn().mockResolvedValue(null) }) }));
vi.mock("../../app/updatePrompt", () => ({ promptUpdateIfNeeded: vi.fn() }));
vi.mock("../../features/settings/useWebAutoSync", () => ({
  useDesktopAutoSync: () => {},
  useWebAutoSync: () => {},
}));
vi.mock("../../features/settings/SyncCard", () => ({
  WEB_SYNC_ERROR_EVENT: "nexterm:web-sync-error",
  WEB_SYNC_RESULT_EVENT: "nexterm:web-sync-result",
}));
vi.mock("../../features/credentials/useVaultInitGate", () => ({
  useVaultInitGate: () => ({ ensureVaultInit: vi.fn().mockResolvedValue(true), vaultInitGate: null }),
}));
vi.mock("../../ipc/wails", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/wails")>();
  return {
    ...actual,
    callDesktop: mocks.callDesktop,
    closeWindow: vi.fn(),
    minimiseWindow: vi.fn(),
    toggleMaximiseWindow: vi.fn(),
  };
});
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return {
    ...actual,
    ask: vi.fn(),
    askChoice: vi.fn(),
    pickSavePath: vi.fn(),
    promptText: vi.fn(),
  };
});

vi.mock("../../features/terminal/XtermView", async () => {
  const { useEffect, useRef } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
      const attachFiredRef = useRef(false);
      useEffect(() => {
        if (attachFiredRef.current) return;
        attachFiredRef.current = true;
        (props.onHandle as ((handle: unknown) => void) | undefined)?.({
          copyBlock: () => "",
          scrollToBlock: () => {},
          navigateBlock: () => null,
          clearBlocks: () => {},
          clear: () => {},
          getSelection: () => "",
          focus: () => {},
          fit: () => {},
          dimensions: () => ({ cols: 80, rows: 24 }),
        });
        mocks.attachSeq += 1;
        (props.onAttach as ((id: string) => void) | undefined)?.(`kernel-${mocks.attachSeq}`);
      }, [props.onAttach, props.onHandle]);
      return <div className="h-full w-full min-h-0" />;
    },
  };
});
vi.mock("../../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));
vi.mock("../../features/terminal/BackgroundSessions", () => ({ BackgroundSessions: () => null }));
vi.mock("../../features/terminal/WorkspaceRestoreBanner", () => ({ WorkspaceRestoreBanner: () => null }));
vi.mock("../../features/terminal/TranscriptHistoryPanel", () => ({ TranscriptHistoryPanel: () => null }));
vi.mock("../../features/files/FileBrowser", () => ({ FileBrowser: () => null }));
vi.mock("../../features/files/FileTree", () => ({ FileTree: () => null }));
vi.mock("../../features/files/MountPanel", () => ({ MountPanel: () => null }));
vi.mock("../../features/forward/ForwardPanel", () => ({ ForwardPanel: () => null }));
vi.mock("../../features/files/FileEditor", () => ({ FileEditor: () => null }));
vi.mock("../../features/files/LogViewer", () => ({ LogViewer: () => null }));
vi.mock("../../features/docker/DockerPanel", () => ({ DockerPanel: () => null }));
vi.mock("../../features/db/DbPanel", () => ({ DbPanel: () => null }));
vi.mock("../../features/execbatch/ExecBatchPanel", () => ({ ExecBatchPanel: () => null }));
vi.mock("../../features/ai/AiSidebar", () => ({ AiSidebar: () => null }));
vi.mock("../../features/settings/SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));
vi.mock("../../app/UpdateBanner", () => ({ UpdateBanner: () => null }));
vi.mock("../../app/DekUnlockBanner", () => ({ DekUnlockBanner: () => null }));
vi.mock("../../features/fleet/DeviceSessionBoundary", () => ({ DeviceSessionBoundary: () => null }));
vi.mock("../../features/fleet/DeviceTreeSection", () => ({ DeviceTreeSection: () => null }));
vi.mock("../../features/explorer/SnippetsPanel", () => ({ SnippetsPanel: () => null }));

import App from "../../app/App";
import { SshImportDialog } from "../../features/explorer/SshImportDialog";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;

function mountApp(withImport = false, leftOpen = false, paneCount = 1): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mocks.attachSeq = 0;
  useUi.setState({ leftOpen });
  const panes = Array.from({ length: paneCount }, (_, index) => ({
    id: `p${index + 1}`,
    activeTabId: `t${index + 1}`,
    tabs: [{
      id: `t${index + 1}`,
      kind: "terminal" as const,
      title: "web-01",
      sessionId: "s1",
      tabId: `kernel-${index + 1}`,
      assetId: "a1",
      closable: true,
    }],
  }));
  useUi.setState({
    workspaces: [{
      id: "ws1",
      kind: "session",
      title: "web-01",
      sessionId: "s1",
      assetId: "a1",
      assetKind: "ssh",
      closable: true,
      panes,
      activePaneId: "p1",
    }],
    activeWorkspaceId: "ws1",
  });
  return mount(createElement(
    QueryClientProvider,
    { client },
    withImport
      ? createElement(Fragment, null, createElement(App), createElement(SshImportDialog, { onClose: () => undefined }))
      : createElement(App),
  ));
}

// The terminal pane, command palette and asset editor are React.lazy panels. A cold import transforms the module
// in real time, which no tick budget bounds under machine load, so warm them before flushUntil counts ticks.
beforeAll(async () => {
  await Promise.all([
    import("../../features/terminal/TerminalPane"),
    import("../../app/JumpPalette"),
    import("../../features/explorer/AssetTree"),
  ]);
});

beforeEach(() => {
  vi.clearAllMocks();
  mocks.syncHandlers.length = 0;
  mocks.stale = false;
  mocks.listenError = false;
  document.body.replaceChildren();
  window.innerWidth = 1280;
  mocks.assetList.mockResolvedValue([asset]);
  mocks.groupList.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.callDesktop.mockImplementation(async (command: string, input: unknown) => {
    if (command !== "asset_update") throw new Error(`unexpected desktop call: ${command}`);
    const envelope = input as { args?: Record<string, unknown> };
    const fields = envelope.args ?? (input as Record<string, unknown>);
    mocks.assetUpdate(fields);
    mocks.stale = true;
    return { ...asset, ...fields };
  });
  mocks.sessionList.mockImplementation(() => Promise.resolve(sessionRows(mocks.stale)));
  mocks.sessionReconnect.mockResolvedValue(true);
  mocks.probeHostKey.mockResolvedValue({ state: "known" });
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  mocks.listCredentials.mockResolvedValue([]);
  mocks.syncLinkGet.mockResolvedValue({
    url: "", username: "", insecure: false, hasPassword: false, verifiedAt: 0, lastError: null,
  });
  mocks.syncStatus.mockResolvedValue({ configured: false, loggedIn: false, warnings: [] });
  mocks.sshPreview.mockResolvedValue(preview);
  mocks.sshApply.mockImplementation(async () => {
    mocks.stale = true;
    return { assetsCreated: 0, assetsUpdated: 1, credentialsCreated: 0, credentialsUpdated: 0, skipped: 0, warnings: [] };
  });
  mocks.listLive.mockResolvedValue([]);
  mocks.recordingStatus.mockResolvedValue({ tabId: "kernel-1", state: "never-started", bytes: 0, generation: 0 });
  useUi.setState({
    toasts: [],
    leftOpen: false,
    rightOpen: false,
    leftMode: "assets",
    sessions: sessionRows(false),
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function fireSyncEvent(topic: string, payload: unknown = { applied: true }): void {
  act(() => {
    for (const entry of [...mocks.syncHandlers]) {
      if (entry.topic === topic) entry.handler(payload);
    }
  });
}

async function openPaletteEditor(): Promise<void> {
  const palette = document.querySelector<HTMLButtonElement>('button[aria-label^="跳转面板"]');
  expect(palette).not.toBeNull();
  click(palette!);
  await flushUntil(() => document.querySelector('input[aria-label="搜索会话、主机或命令"]') !== null);
  const search = document.querySelector<HTMLInputElement>('input[aria-label="搜索会话、主机或命令"]');
  expect(search).not.toBeNull();
  setInputValue(search!, "编辑资产 web-01");
  await flush();
  const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find((item) =>
    item.textContent?.includes("编辑资产 web-01"),
  );
  expect(option).not.toBeUndefined();
  click(option!);
  await flushUntil(() => document.querySelector('.nx-modal input[type="number"]') !== null);
  const port = document.querySelector<HTMLInputElement>('.nx-modal input[type="number"]');
  expect(port).not.toBeNull();
  setInputValue(port!, "2202");
  await flush();
}

async function saveAndExpectStale(): Promise<void> {
  const before = mocks.sessionList.mock.calls.length;
  clickButton(document.body, "保存");
  await flushUntil(() =>
    mocks.sessionList.mock.calls.length > before && (document.body.textContent?.includes("资产配置已修改，重新连接后生效") ?? false),
  );
}

async function reconnectAndExpectStaleCleared(): Promise<void> {
  const button = document.querySelector<HTMLButtonElement>('button[title="使用新的资产配置重新连接，保留当前终端"]');
  expect(button).not.toBeNull();
  mocks.stale = false;
  // The manual reconnect must observe the session engage before the connected frame counts as fresh.
  let engaged = false;
  mocks.sessionList.mockImplementation(() => {
    if (!engaged) {
      engaged = true;
      return Promise.resolve(sessionRows(false).map((row) => ({ ...row, status: "reconnecting" as const })));
    }
    return Promise.resolve(sessionRows(mocks.stale));
  });
  // The reconnect polls the session list on a real-time interval; fake it so the second poll does not wait on the clock.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  click(button!);
  await flushUntil(() => engaged);
  expect(useUi.getState().sessions[0]?.staleConfig).toBe(true);
  await act(async () => {
    await vi.runOnlyPendingTimersAsync();
  });
  // The reconnect settles the session list first; the stale banner follows the store, not the poll.
  await flushUntil(() => useUi.getState().sessions[0]?.staleConfig === false);
  expect(document.body.textContent).not.toContain("资产配置已修改，重新连接后生效");
  expect(mocks.sessionReconnect).toHaveBeenCalledWith("s1");
}

describe("persistent asset session refresh", () => {
  for (const leftOpen of [false, true]) {
    it(`uses real JumpPalette and AssetEditor with leftOpen=${leftOpen}, then clears stale after reconnect`, async () => {
      mounted = mountApp(false, leftOpen);
      await flush();
      await openPaletteEditor();
      await saveAndExpectStale();

      expect(mocks.assetUpdate).toHaveBeenCalledWith(expect.objectContaining({ id: "a1", port: 2202 }));
      expect(useUi.getState().leftOpen).toBe(leftOpen);
      expect(useUi.getState().sessions[0]?.staleConfig).toBe(true);
      expect(document.body.textContent).toContain("资产配置已修改，重新连接后生效");
      await reconnectAndExpectStaleCleared();
      expect(useUi.getState().sessions[0]?.staleConfig).toBe(false);
      expect(document.body.textContent).not.toContain("资产配置已修改，重新连接后生效");
    });

    it(`executes the real conflict overwrite flow with leftOpen=${leftOpen}`, async () => {
      mounted = mountApp(true, leftOpen);
      await flush();
      clickButton(document.body, "预览");
      await flushUntil(() => document.body.textContent?.includes("导入选中项") ?? false);
      const strategy = document.querySelector<HTMLSelectElement>('select[aria-label="主机 web-01 的冲突处理"]');
      expect(strategy).not.toBeNull();
      setSelectValue(strategy!, "overwrite");
      await flush();

      const before = mocks.sessionList.mock.calls.length;
      clickButton(document.body, "导入选中项");
      await flushUntil(() =>
        mocks.sshApply.mock.calls.length === 1 && mocks.sessionList.mock.calls.length > before &&
        (document.body.textContent?.includes("资产配置已修改，重新连接后生效") ?? false),
      );

      expect(mocks.sshApply).toHaveBeenCalledWith(expect.objectContaining({
        hosts: [{ id: "h1", action: "overwrite" }],
        keys: [],
      }));
      expect(useUi.getState().leftOpen).toBe(leftOpen);
      expect(useUi.getState().sessions[0]?.staleConfig).toBe(true);
      expect(document.body.textContent).toContain("资产配置已修改，重新连接后生效");
    });
  }

  it("coalesces local and sync events with one trailing request and no per-pane amplification", async () => {
    mounted = mountApp(false, true, 2);
    await flush();
    expect(mocks.syncHandlers.filter((entry) => entry.topic === "sync://status" || entry.topic === "sync://round-complete")).toHaveLength(1);

    const first = deferred<ReturnType<typeof sessionRows>>();
    const second = deferred<ReturnType<typeof sessionRows>>();
    let eventCalls = 0;
    mocks.sessionList.mockImplementation(() => {
      eventCalls += 1;
      return eventCalls === 1 ? first.promise : second.promise;
    });
    mocks.stale = true;
    act(() => {
      notifyAssetDataChanged();
    });
    fireSyncEvent("sync://status");
    act(() => {
      notifyAssetDataChanged();
    });
    expect(eventCalls).toBe(1);

    first.resolve(sessionRows(true));
    await flushUntil(() => eventCalls === 2);
    second.resolve(sessionRows(false));
    await flushUntil(() => !(document.body.textContent?.includes("资产配置已修改，重新连接后生效") ?? false));
    expect(eventCalls).toBe(2);
    expect(useUi.getState().workspaces[0]?.panes).toHaveLength(2);
  });

  it("ignores sync status events that applied no remote changes", async () => {
    mounted = mountApp(false, true, 2);
    await flush();
    mocks.sessionList.mockClear();
    fireSyncEvent("sync://status", {});
    fireSyncEvent("sync://status", { ownerId: "a" });
    await flush();
    expect(mocks.sessionList).not.toHaveBeenCalled();
  });

  it("keeps AppShell mounted and warns when the event adapter throws synchronously", async () => {
    const warning = vi.spyOn(console, "warn").mockImplementation(() => {});
    mocks.listenError = true;
    mounted = mountApp();
    await flush();

    expect(document.body.textContent).toContain("web-01");
    expect(document.body.textContent).not.toContain("应用界面发生错误");
    expect(warning).toHaveBeenCalledWith("[NexTerm] Failed to subscribe asset session refresh", expect.any(Error));
    warning.mockRestore();
  });
});
