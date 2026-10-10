/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { clickButton, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  connect: vi.fn(),
  reconnect: vi.fn(),
  disconnect: vi.fn(),
  knownHostAccept: vi.fn(),
  ask: vi.fn(),
  listLive: vi.fn(),
}));

vi.mock("../../app/platform", () => ({
  isMac: () => false,
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
    reconnect: mocks.reconnect,
    disconnect: mocks.disconnect,
    probeHostKey: vi.fn().mockResolvedValue({ state: "known" }),
  },
  terminalApi: {
    listLive: mocks.listLive,
    write: vi.fn().mockResolvedValue(undefined),
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
  promptText: vi.fn(),
  ask: mocks.ask,
}));

vi.mock("../../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

const harness = vi.hoisted(() => ({
  props: null as Record<string, unknown> | null,
}));

vi.mock("../../features/terminal/XtermView", async () => {
  const { useEffect } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
      useEffect(() => {
        harness.props = props;
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
      }, []);
      return <div data-testid="xterm-mock" />;
    },
  };
});

import { TerminalPane } from "../../features/terminal/TerminalPane";
import { useUi, type AppTab } from "../../app/store";

let mounted: MountedView | undefined;

function seedTab(patch: Partial<AppTab> = {}) {
  const tab: AppTab = {
    id: "t1",
    kind: "terminal",
    title: "web-01",
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
        title: "web-01",
        sessionId: "s1",
        assetId: "asset-1",
        assetKind: "ssh",
        panes: [{ id: "p1", tabs: [tab], activeTabId: tab.id }],
        activePaneId: "p1",
        splitRatio: 0.5,
        closable: true,
      },
    ],
    activeWorkspaceId: "ws1",
    sessions: [
      {
        id: "s1",
        assetId: "asset-1",
        name: "web-01",
        kind: "ssh",
        status: "disconnected",
        tabs: [],
        createdAt: 0,
      },
    ],
    toasts: [],
  });
  return tab;
}

function storeTab(): AppTab | undefined {
  return useUi
    .getState()
    .workspaces[0]?.panes[0]?.tabs.find((candidate) => candidate.id === "t1");
}

function mountPane(): MountedView {
  return mount(
    createElement(TerminalPane, {
      sessionId: "s1",
      title: "web-01",
      storeTabId: "t1",
      resumeTabId: "kernel-1",
    }),
  );
}

function failAttach(code: string): void {
  act(() => {
    (harness.props?.onAttachFailed as ((failed: string) => void) | undefined)?.(code);
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

async function clickMenuItem(label: string): Promise<void> {
  const item = [...document.querySelectorAll<HTMLElement>(".nx-menu .nx-menu-item")].find(
    (candidate) => candidate.textContent?.includes(label),
  );
  if (!item) throw new Error(`menu item ${label} not found`);
  act(() => item.dispatchEvent(new MouseEvent("click", { bubbles: true })));
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

  it("marks the tab dead and shows the reconnect overlay on not_found", async () => {
    seedTab();
    mounted = mountPane();
    await flush();

    failAttach("not_found");

    await waitFor(() => expect(overlay()).not.toBeNull());
    expect(storeTab()?.dead).toBe(true);
    expect(storeTab()?.tabId).toBe("kernel-1");
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
    await waitFor(() => expect(overlay()).toBeNull());
    expect(mocks.connect).toHaveBeenCalledWith("asset-1");
    expect(storeTab()?.dead).toBe(false);
    expect(storeTab()?.sessionId).toBe("s2");
    await waitFor(() => expect(harness.props?.resumeTabId).toBe("kernel-1"));
    expect(storeTab()?.tabId).toBe("kernel-1");
    expect(toastTexts()).toContain("正在接回原来的终端");
  });

  it("falls back to a fresh terminal when the resumed id is still not found", async () => {
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

    clickButton(mounted.container, "重新连接这台主机");
    await flush();
    await waitFor(() => expect(overlay()).toBeNull());
    expect(mocks.connect).toHaveBeenCalledTimes(2);
    expect(storeTab()?.dead).toBe(false);
    expect(storeTab()?.tabId).toBeUndefined();
    await waitFor(() => expect(harness.props?.resumeTabId).toBeUndefined());
    expect(toastTexts()).toContain("正在打开一个新的终端");
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
    await waitFor(() => expect(overlay()).toBeNull());
    expect(mocks.reconnect).not.toHaveBeenCalled();
    const st = useUi.getState();
    expect(st.workspaces).toHaveLength(1);
    expect(st.workspaces[0]?.panes[0]?.tabs).toHaveLength(1);
    expect(storeTab()?.sessionId).toBe("s2");
    expect(storeTab()?.dead).toBe(false);
    expect(toastTexts()).toContain("已重新连接");
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
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("主机密钥已变更 127.0.0.1:22");
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
    expect(mocks.knownHostAccept).toHaveBeenCalledWith("127.0.0.1", 22, "ssh-ed25519", "SHA256:new");
    expect(mocks.connect).toHaveBeenCalledTimes(2);
    const st = useUi.getState();
    expect(st.workspaces).toHaveLength(1);
    expect(st.workspaces[0]?.panes[0]?.tabs).toHaveLength(1);
    expect(storeTab()?.sessionId).toBe("s2");
    expect(storeTab()?.dead).toBe(false);
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
});
