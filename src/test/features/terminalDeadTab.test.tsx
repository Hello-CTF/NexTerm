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

vi.mock("../../ipc/env", () => ({ clientId: () => "me" }));

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
    expect(storeTab()?.tabId).toBeUndefined();
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

  it("reconnects with the workspace asset and recovers into a fresh terminal", async () => {
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
    await waitFor(() => expect(harness.props?.resumeTabId).toBeUndefined());
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
