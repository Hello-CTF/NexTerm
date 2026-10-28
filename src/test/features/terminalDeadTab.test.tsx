/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { clickButton, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  connect: vi.fn(),
  connectDevice: vi.fn(),
  reconnect: vi.fn(),
  disconnect: vi.fn(),
  knownHostAccept: vi.fn(),
  ask: vi.fn(),
  listLive: vi.fn(),
  listSessions: vi.fn(),
  closeTab: vi.fn(),
  write: vi.fn(),
  helperOperation: vi.fn(),
  helperUpdate: vi.fn(),
  helperOffline: vi.fn(),
  promptText: vi.fn(),
}));

vi.mock("../../app/platform", () => ({
  isMac: () => false,
  isWindows: () => false,
  modHint: () => "Ctrl",
  setMacPlatform: () => {},
  wailsDragRegionStyle: {},
  wailsNoDragRegionStyle: {},
  isWailsDragRegionTarget: () => false,
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: { knownHostAccept: mocks.knownHostAccept },
  dbApi: {},
  vaultApi: {},
  sessionApi: {
    connect: mocks.connect,
    connectDevice: mocks.connectDevice,
    reconnect: mocks.reconnect,
    disconnect: mocks.disconnect,
    list: mocks.listSessions,
    probeHostKey: vi.fn().mockResolvedValue({ state: "known" }),
  },
  terminalApi: {
    listLive: mocks.listLive,
    write: mocks.write,
    closeTab: mocks.closeTab,
    helperOperation: mocks.helperOperation,
    helperUpdate: mocks.helperUpdate,
    helperOffline: mocks.helperOffline,
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
  },
}));

vi.mock("../../ipc/events", () => ({
  listenEvent: vi.fn().mockResolvedValue(() => {}),
  EVENTS: { terminalControl: "terminal-control" },
  EventVersionGate: class {
    accept() {
      return true;
    }
  },
}));

vi.mock("../../ipc/env", () => ({ clientId: () => "me", WEB: false }));

vi.mock("../../ui/dialogs", () => ({
  describeTarget: () => "",
  finishSave: vi.fn(),
  pickSavePath: vi.fn(),
  promptText: mocks.promptText,
  ask: mocks.ask,
}));

vi.mock("../../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

const harness = vi.hoisted(() => ({
  props: null as Record<string, unknown> | null,
}));

vi.mock("../../features/terminal/XtermView", async () => {
  const { useEffect, useRef } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
      harness.props = props;
      const onHandleRef = useRef(props.onHandle);
      onHandleRef.current = props.onHandle;
      useEffect(() => {
        (onHandleRef.current as ((handle: unknown) => void) | undefined)?.({
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
      }, []);
      return <div data-testid="xterm-mock" />;
    },
  };
});

import { TerminalPane } from "../../features/terminal/TerminalPane";
import { createHelperOperationState } from "../../features/terminal/helperOperationState";
import { assetConnectionProblemKey, useUi, type AppTab } from "../../app/store";

let mounted: MountedView | undefined;

function seedTab(
  patch: Partial<AppTab> = {},
  host: { assetId: string; assetKind: string; title?: string } = { assetId: "asset-1", assetKind: "ssh" },
) {
  const tab: AppTab = {
    id: "t1",
    kind: "terminal",
    title: host.title ?? "web-01",
    sessionId: "s1",
    tabId: "kernel-1",
    closable: true,
    ...patch,
  };
  useUi.setState({
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: host.title ?? "web-01",
        sessionId: "s1",
        assetId: host.assetId,
        assetKind: host.assetKind,
        panes: [{ id: "p1", tabs: [tab], activeTabId: tab.id }],
        activePaneId: "p1",
        closable: true,
      },
    ],
    activeWorkspaceId: "ws1",
    sessions: [
      {
        id: "s1",
        assetId: host.assetId,
        name: host.title ?? "web-01",
        kind: host.assetKind,
        status: "disconnected",
        tabs: [],
        createdAt: 0,
      },
    ],
    toasts: [],
    helperProgress: {},
    helperOperations: createHelperOperationState(),
    helperOperationTabs: {},
    helperOperationIntents: {},
    helperRetries: {},
  });
  return tab;
}

function storeTab(): AppTab | undefined {
  return useUi
    .getState()
    .workspaces[0]?.panes[0]?.tabs.find((candidate) => candidate.id === "t1");
}

function mountPane(containerId?: string): MountedView {
  return mount(
    createElement(TerminalPane, {
      sessionId: "s1",
      title: "web-01",
      storeTabId: "t1",
      resumeTabId: "kernel-1",
      containerId,
    }),
  );
}

function failAttach(code: string): void {
  act(() => {
    (harness.props?.onAttachFailed as ((failed: string) => void) | undefined)?.(code);
  });
}

function succeedAttach(id = "kernel-1"): void {
  act(() => {
    (harness.props?.onAttach as ((attached: string) => void) | undefined)?.(id);
  });
}

function overlay(): HTMLElement | null {
  return [...(mounted?.container.querySelectorAll("span") ?? [])].find(
    (candidate) => candidate.textContent?.trim() === "这个终端已失效",
  ) ?? null;
}

async function openTerminalMenu(): Promise<void> {
  const body = mounted?.container.querySelector<HTMLElement>(".nx-terminal-body .relative");
  if (!body) throw new Error("terminal body not found");
  act(() => {
    body.dispatchEvent(
      new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 20, clientY: 20 }),
    );
  });
  await flush();
}

async function openToolbarOverflow(): Promise<void> {
  const button = mounted?.container.querySelector<HTMLElement>('button[aria-label="更多终端操作"]');
  if (!button) throw new Error("toolbar overflow button not found");
  await act(async () => {
    button.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

async function clickMenuItem(label: string): Promise<void> {
  const item = [...document.querySelectorAll<HTMLElement>(".nx-menu .nx-menu-item")].find(
    (candidate) => candidate.textContent?.includes(label),
  );
  if (!item) throw new Error(`menu item ${label} not found`);
  await act(async () => {
    item.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

function menuLabels(): string[] {
  return [...document.querySelectorAll<HTMLElement>(".nx-menu .nx-menu-item")].map(
    (candidate) => candidate.textContent ?? "",
  );
}

function toastTexts(): string {
  return useUi
    .getState()
    .toasts.map((t) => t.text)
    .join("\n");
}

const CHANGED_HOST_KEY = {
  code: "host_key_pending",
  message: "SSH host key for 127.0.0.1:22 changed",
  detail: {
    host: "127.0.0.1",
    port: 22,
    keyType: "ssh-ed25519",
    fingerprint: "SHA256:new",
    key: "ZmFrZS1kZWFkLXRhYi1rZXk=",
    changed: true,
    known: [{ keyType: "ssh-ed25519", fingerprint: "SHA256:old" }],
  },
};

describe("terminal dead-tab overlay", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    harness.props = null;
    mocks.listLive.mockResolvedValue([]);
    mocks.listSessions.mockImplementation(async () => useUi.getState().sessions);
    mocks.write.mockResolvedValue(undefined);
    mocks.closeTab.mockResolvedValue(undefined);
    mocks.connect.mockResolvedValue({
      id: "s2",
      assetId: "asset-1",
      name: "web-01",
      kind: "ssh",
      status: "connected",
      tabs: [],
      createdAt: 1,
    });
    mocks.connectDevice.mockResolvedValue({
      id: "s-dev",
      assetId: "device/d-1",
      name: "build-box",
      kind: "device",
      status: "connected",
      tabs: [],
      createdAt: 1,
    });
    mocks.knownHostAccept.mockResolvedValue(undefined);
    mocks.ask.mockResolvedValue(false);
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("marks the tab dead and shows the reconnect overlay on not_found", async () => {
    seedTab();
    mounted = mountPane();
    await flush();

    failAttach("not_found");

    await waitFor(() => expect(overlay()).not.toBeNull());
    expect(storeTab()?.dead).toBe(true);
    expect(storeTab()?.tabId).toBe("kernel-1");
  });

  it.each([
    { kind: "ssh", expected: ["kernel-1", "detach", "s1"] },
    { kind: "device", expected: ["kernel-1", "detach", "s1"] },
    { kind: "local", expected: ["kernel-1", undefined] },
  ])("uses the safe default close path for an attach-dead $kind terminal", async ({ kind, expected }) => {
    seedTab({}, { assetId: kind === "device" ? "device/d-1" : "asset-1", assetKind: kind });
    mounted = mountPane();
    await flush();
    failAttach("not_found");
    await waitFor(() => expect(overlay()).not.toBeNull());

    clickButton(mounted.container, "关闭标签");

    await waitFor(() => expect(mocks.closeTab).toHaveBeenCalledWith(...expected));
    expect(storeTab()).toBeUndefined();
  });

  it("opens a genuinely new ordinary shell only after an explicit restore-failure choice", async () => {
    seedTab();
    mounted = mountPane();
    await flush();
    failAttach("not_found");
    await waitFor(() => expect(overlay()).not.toBeNull());

    expect(mounted.container.querySelector('[role="dialog"]')).toBeNull();
    const notice = useUi.getState().toasts.find((toast) => toast.text.includes("恢复失败"));
    expect(notice?.action?.label).toBe("查看选项");
    act(() => notice?.action?.onSelect());
    clickButton(mounted.container, "新建临时终端");

    await waitFor(() => expect(harness.props?.forceRegular).toBe(true));
    expect(harness.props?.resumeTabId).toBeUndefined();
    expect(storeTab()?.dead).toBe(true);
    expect(storeTab()?.tabId).toBeUndefined();
    expect(useUi.getState().helperOperations.operations.t1?.originalSession).toBe("not_restored");

    succeedAttach("kernel-new-ordinary");

    await waitFor(() => expect(storeTab()?.dead).toBe(false));
    expect(storeTab()?.tabId).toBe("kernel-new-ordinary");
    expect(useUi.getState().helperOperations.operations.t1).toBeUndefined();
  });

  it("keeps remote survival uncertainty visible when detach returns not_found", async () => {
    mocks.closeTab.mockRejectedValue({ code: "not_found", message: "local tab missing" });
    seedTab();
    mounted = mountPane();
    await flush();
    failAttach("not_found");
    await waitFor(() => expect(overlay()).not.toBeNull());

    clickButton(mounted.container, "关闭标签");

    await waitFor(() => expect(storeTab()).toBeUndefined());
    expect(toastTexts()).toContain("远端持久终端可能仍在运行");
  });

  it("closes a dead remote tab while keeping unknown survival visible on disconnected", async () => {
    mocks.closeTab.mockRejectedValue({ code: "disconnected", message: "close outcome unknown" });
    seedTab();
    mounted = mountPane();
    await flush();
    failAttach("not_found");
    await waitFor(() => expect(overlay()).not.toBeNull());

    clickButton(mounted.container, "关闭标签");

    await waitFor(() => expect(storeTab()).toBeUndefined());
    expect(toastTexts()).toContain("远端持久终端可能仍在运行");
  });

  it.each([false, true])("synchronizes attach info with exited=%s and clears stale close state", async (exited) => {
    seedTab({ dead: true, exited: !exited, closeError: "stale" });
    mocks.listLive.mockResolvedValue([{
      tabId: "kernel-1",
      sessionId: "s1",
      sessionName: "web-01",
      sessionKind: "ssh",
      cols: 80,
      rows: 24,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited,
      lastOutputMsAgo: 0,
    }]);
    mounted = mountPane();
    await flush();

    act(() => {
      (harness.props?.onAttachInfo as ((info: Record<string, unknown>) => void) | undefined)?.({
        tabId: "kernel-1",
        sizeOwner: "me",
        subscribers: 1,
        viewers: 1,
        exited,
        retainable: true,
        shell: "posix",
      });
      (harness.props?.onAttach as ((id: string) => void) | undefined)?.("kernel-1");
    });

    await waitFor(() => {
      expect(storeTab()?.exited).toBe(exited);
      expect(storeTab()?.dead).toBe(false);
      expect(storeTab()?.closeError).toBeUndefined();
    });
  });

  it("keeps the terminal on other attach failures such as bad_param", async () => {
    seedTab();
    mounted = mountPane();
    await flush();

    failAttach("bad_param");

    await flush();
    expect(overlay()).toBeNull();
    expect(storeTab()?.dead).not.toBe(true);
  });

  it("reconnects with the workspace asset and resumes the old terminal id first", async () => {
    seedTab();
    mounted = mountPane();
    await flush();
    failAttach("not_found");
    await waitFor(() => expect(overlay()).not.toBeNull());

    clickButton(mounted.container, "重新连接这台主机");

    await flush();
    succeedAttach();
    await waitFor(() => expect(overlay()).toBeNull());
    expect(mocks.connect).toHaveBeenCalledWith("asset-1");
    expect(storeTab()?.dead).toBe(false);
    expect(storeTab()?.sessionId).toBe("s2");
    await waitFor(() => expect(harness.props?.resumeTabId).toBe("kernel-1"));
    expect(storeTab()?.tabId).toBe("kernel-1");
    expect(toastTexts()).toContain("已重新连接");
  });

  it("retries a restored tab when the initial session list arrives after the layout", async () => {
    seedTab();
    useUi.setState({ sessions: [] });
    mounted = mountPane();
    await flush();
    failAttach("not_found");
    await waitFor(() => expect(overlay()).not.toBeNull());
    act(() => {
      useUi.getState().setSessions([{
        id: "s1", assetId: "asset-1", name: "web-01", kind: "ssh",
        status: "connected", tabs: [], createdAt: 1,
      }]);
    });
    succeedAttach();
    await waitFor(() => expect(overlay()).toBeNull());
    expect(harness.props?.resumeTabId).toBe("kernel-1");
    expect(storeTab()?.tabId).toBe("kernel-1");
    expect(mocks.connect).not.toHaveBeenCalled();
  });

  it("follows a second backend tab id across a layout identity change without creating a third id", async () => {
    seedTab();
    mounted = mountPane();
    await flush();
    expect(harness.props?.resumeTabId).toBe("kernel-1");

    act(() => {
      useUi.getState().updateTab("t1", { tabId: "kernel-2" });
      mounted!.root.render(
        createElement(TerminalPane, {
          sessionId: "s1",
          title: "web-01",
          storeTabId: "t1",
          resumeTabId: "kernel-2",
        }),
      );
    });

    await waitFor(() => expect(harness.props?.resumeTabId).toBe("kernel-2"));
    expect(storeTab()?.tabId).toBe("kernel-2");
  });

  it("does not resume again when its own attached id is written back to the layout", async () => {
    seedTab({ tabId: undefined }, { assetId: "local", assetKind: "local" });
    mounted = mount(
      createElement(TerminalPane, {
        sessionId: "s1",
        title: "local",
        storeTabId: "t1",
        resumeTabId: undefined,
      }),
    );
    await flush();
    mocks.listSessions.mockResolvedValue(useUi.getState().sessions);

    act(() => {
      (harness.props?.onAttach as ((id: string) => void) | undefined)?.("kernel-1");
    });
    await waitFor(() => expect(storeTab()?.tabId).toBe("kernel-1"));
    act(() => {
      mounted!.root.render(
        createElement(TerminalPane, {
          sessionId: "s1",
          title: "local",
          storeTabId: "t1",
          resumeTabId: "kernel-1",
        }),
      );
    });

    await flush();
    expect(harness.props?.resumeTabId).toBeUndefined();
  });

  it("reconnects a dead device terminal through the fleet device path", async () => {
    seedTab({}, { assetId: "device/d-1", assetKind: "device", title: "build-box" });
    mounted = mountPane();
    await flush();
    failAttach("not_found");
    await waitFor(() => expect(overlay()).not.toBeNull());

    clickButton(mounted.container, "重新连接这台主机");

    await flush();
    succeedAttach();
    await waitFor(() => expect(overlay()).toBeNull());
    expect(mocks.connectDevice).toHaveBeenCalledWith("d-1", "build-box");
    expect(mocks.connectDevice).toHaveBeenCalledTimes(1);
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(storeTab()?.sessionId).toBe("s-dev");
    expect(storeTab()?.dead).toBe(false);
    expect(toastTexts()).toContain("已重新连接");
  });

  it("keeps the original remote terminal identity when recovery still reports not found", async () => {
    seedTab();
    mounted = mountPane();
    await flush();
    failAttach("not_found");
    await waitFor(() => expect(overlay()).not.toBeNull());

    clickButton(mounted.container, "重新连接这台主机");
    await flush();
    await waitFor(() => expect(harness.props?.resumeTabId).toBe("kernel-1"));

    failAttach("not_found");

    await waitFor(() => expect(overlay()).not.toBeNull());
    expect(harness.props?.resumeTabId).toBe("kernel-1");
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(storeTab()?.dead).toBe(true);
    expect(storeTab()?.tabId).toBe("kernel-1");
    expect(toastTexts()).not.toContain("正在恢复原终端");

    clickButton(mounted.container, "重新连接这台主机");
    await flush();
    expect(harness.props?.resumeTabId).toBe("kernel-1");
    expect(storeTab()?.tabId).toBe("kernel-1");
  });

  it("marks a missing container exec as ended and offers an explicit new process", async () => {
    seedTab({ containerId: "c1" });
    useUi.setState({
      sessions: [
        {
          id: "s1",
          assetId: "asset-1",
          name: "web-01",
          kind: "ssh",
          status: "connected",
          tabs: [],
          createdAt: 0,
        },
      ],
    });
    mounted = mount(
      createElement(TerminalPane, {
        sessionId: "s1",
        title: "web-01",
        storeTabId: "t1",
        resumeTabId: "kernel-1",
        containerId: "c1",
      }),
    );
    await flush();

    failAttach("not_found");

    await waitFor(() => expect(mounted?.container.textContent).toContain("容器 exec 已中断"));
    expect(storeTab()?.dead).not.toBe(true);
    expect(storeTab()?.exited).toBe(true);
    expect(storeTab()?.tabId).toBe("kernel-1");
  });

  it("does not open a fresh terminal when the host key confirmation is cancelled", async () => {
    seedTab();
    mounted = mountPane();
    await flush();
    failAttach("not_found");
    await waitFor(() => expect(overlay()).not.toBeNull());
    mocks.connect.mockRejectedValue({
      code: "host_key_pending",
      message: "unknown SSH host key for 127.0.0.1:22",
      detail: {
        host: "127.0.0.1",
        port: 22,
        keyType: "ssh-ed25519",
        fingerprint: "SHA256:new",
        changed: false,
      },
    });

    clickButton(mounted.container, "重新连接这台主机");

    await waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(1));
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("首次连接 127.0.0.1:22");
    await waitFor(() => expect(toastTexts()).toContain("已取消重连"));
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();

    failAttach("not_found");
    await flush();

    expect(storeTab()?.tabId).toBe("kernel-1");
    expect(storeTab()?.dead).toBe(true);
    expect(overlay()).not.toBeNull();
  });

  it("never auto-trusts a changed host key during reconnect", async () => {
    seedTab();
    mounted = mountPane();
    await flush();
    failAttach("not_found");
    await waitFor(() => expect(overlay()).not.toBeNull());
    mocks.connect.mockRejectedValue({
      code: "host_key_pending",
      message: "主机密钥已变更",
      detail: {
        host: "127.0.0.1",
        port: 22,
        keyType: "ssh-ed25519",
        fingerprint: "SHA256:new",
        changed: true,
        known: [{ keyType: "ssh-ed25519", fingerprint: "SHA256:old" }],
      },
    });

    clickButton(mounted.container, "重新连接这台主机");

    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(1));
    await flush();
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(overlay()).not.toBeNull();
    expect(storeTab()?.dead).toBe(true);
  });

  it("discards helper metadata returned by an old session after reconnect", async () => {
    let resolveStatus: ((value: unknown) => void) | undefined;
    mocks.helperOperation.mockReturnValue(new Promise((resolve) => {
      resolveStatus = resolve;
    }));
    seedTab();
    mounted = mountPane();
    await flush();
    await openToolbarOverflow();
    await clickMenuItem("持久终端组件…");
    act(() => {
      useUi.getState().setHelperProgress({
        sessionId: "s1",
        assetId: "asset-1",
        operationId: "stale-operation",
        attempt: 1,
        attempts: 3,
        deadline: Date.now() + 60_000,
        step: 1,
        steps: 6,
        stage: "discover",
        transferred: 0,
        total: 0,
        percent: -1,
        done: false,
      });
    });
    expect(useUi.getState().helperOperations.operations.t1?.operationId).toBe("stale-operation");

    act(() => {
      mounted!.root.render(createElement(TerminalPane, {
        sessionId: "s2",
        title: "web-01",
        storeTabId: "t1",
        resumeTabId: "kernel-1",
      }));
    });
    resolveStatus?.({
      sessionId: "s1",
      action: "status",
      accepted: false,
      local: { known: true, release: "stale-release", protocol: 2, sha256: "d".repeat(64) },
      running: { known: false },
      compatibility: "unknown",
    });
    await flush();

    expect(useUi.getState().helperOperations.operations.t1).toBeUndefined();
    expect(mounted.container.textContent).not.toContain("stale-release");
    expect(mounted.container.textContent).not.toContain("持久终端组件");
  });

  it("keeps only the newest same-session status result when update finishes first", async () => {
    let resolveStaleStatus: ((value: unknown) => void) | undefined;
    const currentSHA = "e".repeat(64);
    mocks.helperOperation
      .mockResolvedValueOnce({
        sessionId: "s1",
        action: "status",
        accepted: false,
        local: { known: true, release: "current", protocol: 2, sha256: currentSHA },
        running: { known: true, release: "running", protocol: 2, sha256: currentSHA },
        compatibility: "compatible",
        update: { status: "available" },
      })
      .mockReturnValueOnce(new Promise((resolve) => {
        resolveStaleStatus = resolve;
      }));
    mocks.helperUpdate.mockResolvedValue({
      sessionId: "s1",
      action: "update_interrupt",
      accepted: true,
      local: { known: true, release: "updated", protocol: 2, sha256: "f".repeat(64) },
      running: { known: true, release: "updated-running", protocol: 2, sha256: "f".repeat(64) },
      compatibility: "compatible",
      update: { status: "updated", updated: true, interrupted: true, rolledBack: false },
    });
    seedTab();
    mounted = mountPane();
    await flush();
    await openToolbarOverflow();
    await clickMenuItem("持久终端组件…");
    await waitFor(() => expect(mounted?.container.textContent).toContain("持久终端组件有可用更新"));

    await openToolbarOverflow();
    await clickMenuItem("持久终端组件…");
    mocks.ask.mockResolvedValueOnce(true);
    clickButton(mounted.container, "中断活动进程并更新");
    await waitFor(() => expect(mocks.helperUpdate).toHaveBeenCalledWith("s1", true));
    await waitFor(() => expect(mounted?.container.querySelector('[role="dialog"]')).toBeNull());

    resolveStaleStatus?.({
      sessionId: "s1",
      action: "status",
      accepted: false,
      local: { known: true, release: "stale-before-update", protocol: 2, sha256: "d".repeat(64) },
      running: { known: true, release: "stale-running", protocol: 2, sha256: "d".repeat(64) },
      compatibility: "compatible",
      update: { status: "available" },
    });
    await flush();

    expect(mounted.container.querySelector('[role="dialog"]')).toBeNull();
    expect(mounted.container.textContent).not.toContain("stale-before-update");
    expect(mounted.container.textContent).not.toContain("stale-running");
  });

  it("closes the helper window on later and Escape without requesting an update", async () => {
    mocks.helperOperation.mockResolvedValue({
      sessionId: "s1", action: "status", accepted: false,
      local: { known: true, protocol: 2, sha256: "a".repeat(64) },
      running: { known: true, protocol: 2, sha256: "b".repeat(64) },
      compatibility: "compatible", update: { status: "available" },
    });
    seedTab();
    mounted = mountPane();
    await flush();
    await openToolbarOverflow();
    await clickMenuItem("持久终端组件…");
    await waitFor(() => expect(mounted?.container.textContent).toContain("持久终端组件有可用更新"));
    expect(mounted.container.querySelector("details")?.open).toBe(false);
    clickButton(mounted.container, "稍后更新");
    expect(mounted.container.querySelector('[role="dialog"]')).toBeNull();
    expect(mocks.helperUpdate).not.toHaveBeenCalled();
    await openToolbarOverflow();
    await clickMenuItem("持久终端组件…");
    await waitFor(() => expect(mounted?.container.textContent).toContain("持久终端组件有可用更新"));
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); });
    expect(mounted.container.querySelector('[role="dialog"]')).toBeNull();
  });

  it("requires confirmation before interrupting active processes", async () => {
    mocks.helperOperation.mockResolvedValue({
      sessionId: "s1", action: "status", accepted: false,
      local: { known: true, protocol: 2, sha256: "a".repeat(64) },
      running: { known: true, protocol: 2, sha256: "b".repeat(64) },
      compatibility: "compatible", update: { status: "available" },
    });
    mocks.ask.mockResolvedValueOnce(false);
    seedTab();
    mounted = mountPane();
    await flush();
    await openToolbarOverflow();
    await clickMenuItem("持久终端组件…");
    await waitFor(() => expect(mounted?.container.textContent).toContain("持久终端组件有可用更新"));
    clickButton(mounted.container, "中断活动进程并更新");
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(expect.stringContaining("无法恢复"), expect.objectContaining({ kind: "warning" }));
    expect(mocks.helperUpdate).not.toHaveBeenCalled();
    expect(mounted.container.querySelector('[role="dialog"]')).not.toBeNull();
  });

  it.each(["current", "deferred"])("closes the update window with visible %s feedback", async (status) => {
    const metadata = { known: true, protocol: 2, sha256: "a".repeat(64) };
    mocks.helperOperation.mockResolvedValue({
      sessionId: "s1", action: "status", accepted: false, local: metadata, running: metadata,
      compatibility: "compatible", update: { status: "available" },
    });
    mocks.helperUpdate.mockResolvedValue({
      sessionId: "s1", action: "update", accepted: true, local: metadata, running: metadata,
      compatibility: "compatible", update: { status },
    });
    seedTab();
    mounted = mountPane();
    await flush();
    await openToolbarOverflow();
    await clickMenuItem("持久终端组件…");
    await waitFor(() => expect(mounted?.container.textContent).toContain("持久终端组件有可用更新"));
    clickButton(mounted.container, "更新组件");
    await waitFor(() => expect(mocks.helperUpdate).toHaveBeenCalledWith("s1", false));
    await flush();
    expect(mounted.container.querySelector('[role="dialog"]')).toBeNull();
    expect(useUi.getState().toasts.some((toast) => toast.text.includes(status === "current" ? "最新版本" : "更新已延后"))).toBe(true);
  });

  it("stays quiet while the helper deploys and offers a durable tab when ready", async () => {
    seedTab({ tabId: undefined });
    mounted = mountPane();
    await flush();
    const progress = {
      sessionId: "s1", assetId: "asset-1", operationId: "first-install", attempt: 1, attempts: 3,
      deadline: Date.now() + 600_000, step: 3, steps: 6, stage: "upload", transferred: 50, total: 100, percent: 50, done: false,
    };
    act(() => useUi.getState().setHelperProgress(progress));
    const hint = () => mounted?.container.querySelector<HTMLElement>('[data-testid="persistence-hint"]');
    expect(hint()).toBeNull();
    expect(mounted.container.querySelector('[role="dialog"]')).toBeNull();
    expect(mounted.container.textContent).not.toContain("尝试次数");
    act(() => useUi.getState().setHelperProgress({ ...progress, stage: "ready", done: true }));
    await flush();
    expect(hint()?.textContent).toContain("这台主机的持久终端已就绪");
    expect([...(hint()?.querySelectorAll("button") ?? [])].map((button) => button.textContent)).toContain("新建持久终端");
    expect(mounted.container.querySelector('[role="dialog"]')).toBeNull();
    expect(useUi.getState().toasts.some((toast) => toast.text.includes("持久终端已就绪"))).toBe(false);
    expect(mocks.helperOperation).not.toHaveBeenCalled();
  });

  it("keeps the update result when reopened while the update is still running", async () => {
    const status = {
      sessionId: "s1", action: "status", accepted: false,
      local: { known: true, protocol: 2, sha256: "a".repeat(64) },
      running: { known: true, protocol: 2, sha256: "b".repeat(64) },
      compatibility: "compatible", update: { status: "available" },
    };
    let finishUpdate: ((value: unknown) => void) | undefined;
    mocks.helperOperation.mockResolvedValue(status);
    mocks.helperUpdate.mockImplementation(() => new Promise((resolve) => { finishUpdate = resolve; }));
    seedTab();
    mounted = mountPane();
    await flush();
    await openToolbarOverflow();
    await clickMenuItem("持久终端组件…");
    await flush();
    clickButton(mounted.container, "更新组件");
    await flush();
    expect(mounted.container.textContent).toContain("正在更新持久终端组件");
    const close = mounted.container.querySelector<HTMLButtonElement>('[aria-label="关闭持久终端组件窗口"]');
    if (!close) throw new Error("Missing close button");
    act(() => close.click());
    await openToolbarOverflow();
    await clickMenuItem("持久终端组件…");
    await flush();
    expect(mocks.helperOperation).toHaveBeenCalledTimes(1);
    expect(mounted.container.textContent).toContain("正在更新持久终端组件");
    finishUpdate?.({ ...status, action: "update", update: { status: "updated", updated: true } });
    await flush();
    expect(mounted.container.querySelector('[role="dialog"]')).toBeNull();
    expect(useUi.getState().toasts.some((toast) => toast.text.includes("持久终端组件已更新"))).toBe(true);
  });

  it("shows real helper versions and sends explicit default/interrupt update actions", async () => {
    const localSHA = "a".repeat(64);
    const runningSHA = "b".repeat(64);
    const status = {
      sessionId: "s1",
      action: "status",
      accepted: false,
      local: { known: true, release: "v1.4.0", commit: "local-commit", protocol: 2, sha256: localSHA },
      running: { known: true, release: "v1.3.9", commit: "running-commit", protocol: 2, sha256: runningSHA },
      compatibility: "compatible",
      update: { status: "available", detail: "检测到活动 PTY" },
    };
    mocks.helperOperation.mockResolvedValue(status);
    mocks.helperUpdate.mockImplementation(async (_sessionId, interruptActive) => ({
      ...status,
      action: interruptActive ? "update_interrupt" : "update",
      accepted: true,
      update: { status: "updated", detail: interruptActive ? "已明确中断活动进程" : "已完成空闲更新" },
    }));
    seedTab();
    mounted = mountPane();
    await flush();

    await openToolbarOverflow();
    await clickMenuItem("持久终端组件…");

    await waitFor(() => expect(mounted?.container.textContent).toContain("v1.4.0"));
    expect(mounted.container.textContent).toContain("local-commit");
    expect(mounted.container.textContent).toContain(localSHA);
    expect(mounted.container.textContent).toContain("v1.3.9");
    expect(mounted.container.textContent).toContain(runningSHA);
    expect(mounted.container.textContent).toContain("协议兼容，可继续使用；有可用更新");
    expect(mocks.helperOperation).toHaveBeenCalledWith("s1", "", "status");

    clickButton(mounted.container, "更新组件");
    await waitFor(() => expect(mocks.helperUpdate).toHaveBeenCalledWith("s1", false));
    await waitFor(() => expect(mounted?.container.querySelector('[role="dialog"]')).toBeNull());
    expect(useUi.getState().toasts.some((toast) => toast.text.includes("持久终端组件已更新"))).toBe(true);

    await openToolbarOverflow();
    await clickMenuItem("持久终端组件…");
    await waitFor(() => expect(mounted?.container.textContent).toContain("持久终端组件有可用更新"));
    mocks.ask.mockResolvedValueOnce(true);
    clickButton(mounted.container, "中断活动进程并更新");
    await waitFor(() => expect(mocks.helperUpdate).toHaveBeenCalledWith("s1", true));
  });

  it("renders structured rollback details from update errors", async () => {
    const status = {
      sessionId: "s1",
      action: "status",
      accepted: false,
      local: { known: true, release: "v1.4.0", protocol: 2, sha256: "a".repeat(64) },
      running: { known: true, release: "v1.3.9", protocol: 2, sha256: "b".repeat(64) },
      compatibility: "compatible",
      update: { status: "available" },
    };
    mocks.helperOperation.mockResolvedValue(status);
    mocks.helperUpdate.mockRejectedValue({
      code: "unavailable",
      message: "helper update failed; previous daemon restored",
      detail: {
        ...status,
        action: "update",
        update: {
          status: "rolled_back",
          detail: "previous daemon restored",
          updated: false,
          interrupted: false,
          rolledBack: true,
        },
      },
    });
    seedTab();
    mounted = mountPane();
    await flush();
    await openToolbarOverflow();
    await clickMenuItem("持久终端组件…");
    await waitFor(() => expect(mounted?.container.textContent).toContain("持久终端组件有可用更新"));

    clickButton(mounted.container, "更新组件");

    await waitFor(() => expect(mocks.helperUpdate).toHaveBeenCalledWith("s1", false));
    await waitFor(() => expect(mounted?.container.textContent).toContain("已恢复为更新前的版本"));
    expect(mounted.container.textContent).toContain("更新失败，已恢复为原版本");
    expect(mounted.container.textContent).toContain("helper update failed; previous daemon restored");
  });

  it("requires an explicit offline SHA confirmation through the real terminal menu", async () => {
    const sha256 = "c".repeat(64);
    const inspected = {
      sessionId: "s1",
      action: "offline",
      accepted: false,
      local: { known: true, release: "v1.4.0", commit: "offline-commit", protocol: 2, sha256 },
      running: { known: false },
      compatibility: "compatible",
      offline: { fileName: "offline-helper", sha256, protocol: 2, compatibility: "compatible", trusted: false },
    };
    mocks.promptText.mockResolvedValue("/tmp/offline-helper");
    mocks.helperOffline
      .mockResolvedValueOnce(inspected)
      .mockResolvedValueOnce({
        ...inspected,
        accepted: true,
        offline: { ...inspected.offline, trusted: true },
      });
    seedTab();
    mounted = mountPane();
    await flush();

    await openToolbarOverflow();
    await clickMenuItem("导入离线组件文件…");

    await waitFor(() => expect(mounted?.container.textContent).toContain(sha256));
    expect(mounted.container.textContent).toContain("协议兼容；发布版本不同不妨碍继续");
    clickButton(mounted.container, "确认 SHA-256 并信任离线文件");

    await waitFor(() => expect(mocks.helperOffline).toHaveBeenCalledWith("s1", "/tmp/offline-helper", sha256, true));
    await waitFor(() => expect(mounted?.container.textContent).not.toContain("确认 SHA-256 并信任离线文件"));
  });
});

describe("terminal dead-tab context menu reconnect", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    harness.props = null;
    mocks.listLive.mockResolvedValue([]);
    mocks.connect.mockResolvedValue({
      id: "s2",
      assetId: "asset-1",
      name: "web-01",
      kind: "ssh",
      status: "connected",
      tabs: [],
      createdAt: 1,
    });
    mocks.knownHostAccept.mockResolvedValue(undefined);
    mocks.ask.mockResolvedValue(false);
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("offers the fresh host connect instead of StartReconnect and reuses the tab", async () => {
    seedTab();
    mounted = mountPane();
    await flush();
    failAttach("not_found");
    await waitFor(() => expect(overlay()).not.toBeNull());

    await openTerminalMenu();
    expect(menuLabels().some((t) => t.includes("重新连接这台主机"))).toBe(true);
    expect(menuLabels().some((t) => t.includes("重连会话"))).toBe(false);

    await clickMenuItem("重新连接这台主机");

    await waitFor(() => expect(mocks.connect).toHaveBeenCalledWith("asset-1"));
    succeedAttach();
    await waitFor(() => expect(overlay()).toBeNull());
    expect(mocks.reconnect).not.toHaveBeenCalled();
    const st = useUi.getState();
    expect(st.workspaces).toHaveLength(1);
    expect(st.workspaces[0]?.panes[0]?.tabs).toHaveLength(1);
    expect(storeTab()?.sessionId).toBe("s2");
    expect(storeTab()?.dead).toBe(false);
    expect(toastTexts()).toContain("已重新连接");
  });

  it("restores the terminal after the session reconnects from the menu", async () => {
    seedTab();
    mocks.reconnect.mockResolvedValue(true);
    mocks.listSessions.mockResolvedValue([
      {
        id: "s1",
        assetId: "asset-1",
        name: "web-01",
        kind: "ssh",
        status: "connected",
        tabs: [],
        createdAt: 1,
      },
    ]);
    mounted = mountPane();
    await flush();

    await openTerminalMenu();
    expect(menuLabels().some((t) => t.includes("重连会话"))).toBe(true);
    await clickMenuItem("重连会话");

    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));
    await waitFor(() => expect(harness.props?.resumeTabId).toBe("kernel-1"));
    expect(storeTab()?.tabId).toBe("kernel-1");
    expect(useUi.getState().sessions.find((s) => s.id === "s1")?.status).toBe("connected");
    expect(toastTexts()).toContain("已重新连接");

    failAttach("not_found");

    await waitFor(() => expect(overlay()).not.toBeNull());
    expect(harness.props?.resumeTabId).toBe("kernel-1");
    expect(storeTab()?.tabId).toBe("kernel-1");
  });

  it("records a current-session reconnect failure and clears it after retry", async () => {
    useUi.setState({ workspaceRuntime: {}, connectionProblems: {} });
    seedTab();
    mocks.reconnect.mockResolvedValueOnce(false).mockResolvedValue(true);
    mounted = mountPane();
    await flush();

    await openTerminalMenu();
    await clickMenuItem("重连会话");
    await waitFor(() => expect(toastTexts()).toContain("重连失败：重连未能启动"));
    expect(useUi.getState().workspaceRuntime.ws1?.problems?.[0]).toMatchObject({
      connectionKey: assetConnectionProblemKey("asset-1"),
      summary: "重连未能启动",
    });

    mocks.listSessions.mockResolvedValue([
      {
        id: "s1",
        assetId: "asset-1",
        name: "web-01",
        kind: "ssh",
        status: "connected",
        tabs: [],
        createdAt: 1,
      },
    ]);
    await openTerminalMenu();
    await clickMenuItem("重连会话");
    await waitFor(() => expect(toastTexts()).toContain("已重新连接"));
    expect(useUi.getState().workspaceRuntime.ws1?.problems ?? []).toEqual([]);
  });

  it("cancelling the changed host key confirmation neither trusts nor retries", async () => {
    seedTab();
    mounted = mountPane();
    await flush();
    failAttach("not_found");
    await waitFor(() => expect(overlay()).not.toBeNull());
    mocks.connect.mockRejectedValue(CHANGED_HOST_KEY);

    await openTerminalMenu();
    await clickMenuItem("重新连接这台主机");

    await waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(1));
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("主机 127.0.0.1:22 的密钥已变更");
    await waitFor(() => expect(toastTexts()).toContain("已取消重连"));
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(storeTab()?.dead).toBe(true);
    expect(overlay()).not.toBeNull();
  });

  it("accepting the changed host key trusts it and recovers the same tab", async () => {
    seedTab();
    mounted = mountPane();
    await flush();
    failAttach("not_found");
    await waitFor(() => expect(overlay()).not.toBeNull());
    mocks.connect.mockRejectedValueOnce(CHANGED_HOST_KEY);
    mocks.ask.mockResolvedValue(true);

    await openTerminalMenu();
    await clickMenuItem("重新连接这台主机");

    await waitFor(() => expect(toastTexts()).toContain("已重新连接"));
    expect(mocks.knownHostAccept).toHaveBeenCalledWith("127.0.0.1", 22, "ssh-ed25519", "SHA256:new", "ZmFrZS1kZWFkLXRhYi1rZXk=");
    expect(mocks.connect).toHaveBeenCalledTimes(2);
    const st = useUi.getState();
    expect(st.workspaces).toHaveLength(1);
    expect(st.workspaces[0]?.panes[0]?.tabs).toHaveLength(1);
    expect(storeTab()?.sessionId).toBe("s2");
    succeedAttach();
    await waitFor(() => expect(storeTab()?.dead).toBe(false));
  });

  it("keeps the tab dead and reports the error when the fresh connect fails", async () => {
    seedTab();
    mounted = mountPane();
    await flush();
    failAttach("not_found");
    await waitFor(() => expect(overlay()).not.toBeNull());
    mocks.connect.mockRejectedValue(new Error("dial tcp 127.0.0.1:22: connect: connection refused"));

    await openTerminalMenu();
    await clickMenuItem("重新连接这台主机");

    await waitFor(() => expect(toastTexts()).toContain("重新连接失败"));
    expect(mocks.reconnect).not.toHaveBeenCalled();
    expect(storeTab()?.dead).toBe(true);
    expect(overlay()).not.toBeNull();
  });

  it("drops the StartReconnect item when the session was deleted without a dead tab", async () => {
    seedTab();
    useUi.setState({ sessions: [] });
    mounted = mountPane();
    await flush();

    await openTerminalMenu();
    expect(menuLabels().some((t) => t.includes("重新连接这台主机"))).toBe(true);
    expect(menuLabels().some((t) => t.includes("重连会话"))).toBe(false);

    await clickMenuItem("重新连接这台主机");

    await waitFor(() => expect(mocks.connect).toHaveBeenCalledWith("asset-1"));
    expect(mocks.reconnect).not.toHaveBeenCalled();
    await waitFor(() => expect(storeTab()?.sessionId).toBe("s2"));
    expect(storeTab()?.dead).not.toBe(true);
  });

  it("ends container exec on host reconnect and only starts a new process after an explicit retry", async () => {
    seedTab({ containerId: "container-1", tabId: undefined }, { assetId: "asset-1", assetKind: "docker" });
    useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, status: "connected" as const })) }));
    mounted = mountPane("container-1");
    await flush();
    act(() => {
      (harness.props?.onAttach as ((id: string) => void) | undefined)?.("exec-1");
    });
    await waitFor(() => expect(storeTab()?.tabId).toBe("exec-1"));

    act(() => {
      useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, status: "reconnecting" as const })) }));
    });
    await waitFor(() => expect(mounted?.container.textContent).toContain("容器 exec 已中断"));
    expect(storeTab()?.exited).toBe(true);
    expect(mocks.closeTab).toHaveBeenCalledWith("exec-1", "kill", "s1");
    act(() => {
      (harness.props?.onData as ((text: string) => void) | undefined)?.("unsafe-input\n");
    });
    expect(mocks.write).not.toHaveBeenCalled();

    act(() => {
      useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, status: "connected" as const })) }));
    });
    expect(mounted.container.textContent).toContain("原进程和输入不能恢复");
    expect(storeTab()?.tabId).toBe("exec-1");
    clickButton(mounted.container, "重新打开 exec");
    await waitFor(() => expect(storeTab()?.exited).toBeUndefined());
    expect(storeTab()?.tabId).toBeUndefined();
  });

  it("does not restart a container exec against a changed asset", async () => {
    seedTab({ containerId: "container-1", tabId: undefined }, { assetId: "asset-1", assetKind: "docker" });
    useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, status: "connected" as const })) }));
    mounted = mountPane("container-1");
    await flush();
    failAttach("interrupted");
    act(() => {
      useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, assetId: "asset-2" })) }));
    });
    await waitFor(() => expect(mounted?.container.textContent).toContain("原主机已改变"));
    const retry = [...mounted.container.querySelectorAll("button")].find((button) => button.textContent?.includes("重新打开 exec"));
    expect(retry?.disabled).toBe(true);
  });
});
