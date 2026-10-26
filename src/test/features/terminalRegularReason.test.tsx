/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  prepareDurable: vi.fn(),
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
  assetApi: {},
  dbApi: {},
  vaultApi: {},
  sessionApi: {
    prepareDurable: mocks.prepareDurable,
    list: vi.fn().mockResolvedValue([]),
  },
  terminalApi: {
    listLive: vi.fn().mockResolvedValue([]),
    write: vi.fn(),
    closeTab: vi.fn(),
    renameTab: vi.fn().mockResolvedValue(undefined),
    helperOperation: vi.fn(),
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
  ask: vi.fn(),
}));

vi.mock("../../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

const harness = vi.hoisted(() => ({
  props: null as Record<string, unknown> | null,
}));

vi.mock("../../features/terminal/XtermView", () => ({
  XtermView: (props: Record<string, unknown>) => {
    harness.props = props;
    return null;
  },
}));

import { TerminalPane } from "../../features/terminal/TerminalPane";
import { createHelperOperationState, helperOperationReducer } from "../../features/terminal/helperOperationState";
import { forgetDurableReadiness, useUi, type AppTab } from "../../app/store";
import { setTerminalDefaultMode } from "../../app/preferences";

let mounted: MountedView | undefined;

function seed(patch: Partial<AppTab> = {}, kind = "ssh") {
  const tab: AppTab = { id: "t1", kind: "terminal", title: "终端 1", sessionId: "s1", closable: true, ...patch };
  useUi.setState({
    workspaces: [{
      id: "ws1", kind: "session", title: "web-01", sessionId: "s1", assetId: "asset-1", assetKind: kind,
      panes: [{ id: "p1", tabs: [tab], activeTabId: tab.id }], activePaneId: "p1", closable: true,
    }],
    activeWorkspaceId: "ws1",
    sessions: [{ id: "s1", assetId: "asset-1", name: "web-01", kind, status: "connected", tabs: [], createdAt: 0 }],
    toasts: [],
    helperProgress: {},
    helperOperations: createHelperOperationState(),
    helperOperationTabs: {},
    helperOperationIntents: {},
    helperRetries: {},
  });
}

function storeTab(): AppTab | undefined {
  return useUi.getState().workspaces[0]?.panes[0]?.tabs.find((tab) => tab.id === "t1");
}

function mountPane(): void {
  mounted = mount(createElement(TerminalPane, { sessionId: "s1", title: "web-01", storeTabId: "t1" }));
}

function reportAttach(info: Record<string, unknown>): void {
  act(() => {
    (harness.props?.onAttachInfo as ((value: unknown) => void) | undefined)?.({
      tabId: "kernel-1", controller: "me", subscribers: 1, viewers: 1, exited: false, shell: "posix", retainable: false,
      ...info,
    });
  });
}

beforeEach(async () => {
  vi.clearAllMocks();
  localStorage.clear();
  forgetDurableReadiness();
  await setTerminalDefaultMode("durable");
  harness.props = null;
  mocks.prepareDurable.mockResolvedValue({ state: "preparing" });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("regular SSH terminals always say why", () => {
  it("asks the view for a regular shell when the tab was opened as regular", async () => {
    seed({ regularReason: "持久组件准备中" });
    mountPane();
    await flush();

    expect(harness.props?.requestRegular).toBe(true);
  });

  it("names a backend fallback that happened while the helper was still preparing", async () => {
    seed();
    mountPane();
    await flush();
    reportAttach({ durable: false, durableRequested: true, fallbackNotice: "持久终端不可用" });
    act(() => (harness.props?.onAttach as ((id: string) => void) | undefined)?.("kernel-1"));

    await waitFor(() => expect(storeTab()?.regularReason).toBe("持久组件准备中"));
    expect(mounted?.container.textContent).toContain("普通终端 · 持久组件准备中");
  });

  it("names later fallbacks on the same connection that carry no backend notice", async () => {
    mocks.prepareDurable.mockResolvedValue({ state: "unavailable", reason: "目标主机不支持持久终端组件" });
    seed();
    mountPane();
    await flush();
    reportAttach({ durable: false, durableRequested: true });

    await waitFor(() => expect(storeTab()?.regularReason).toBe("持久组件不可用：目标主机不支持持久终端组件"));
  });

  it("names a durable asset override that fell back under the regular default", async () => {
    await setTerminalDefaultMode("regular");
    seed();
    mountPane();
    await flush();
    reportAttach({ durable: false, durableRequested: true });

    await waitFor(() => expect(storeTab()?.regularReason).toBe("持久组件准备中"));
  });

  it("names a reopened or AI-created regular terminal on a host that prefers durable terminals", async () => {
    mocks.prepareDurable.mockResolvedValue({ state: "ready" });
    seed({ tabId: "kernel-1" });
    mountPane();
    await flush();
    reportAttach({ durable: false });

    await waitFor(() => expect(storeTab()?.regularReason).toBe("打开时持久组件未就绪"));
  });

  it("leaves terminals regular by choice unmarked", async () => {
    await setTerminalDefaultMode("regular");
    seed({ tabId: "kernel-1" });
    mountPane();
    await flush();
    reportAttach({ durable: false });
    reportAttach({ durable: false, durableRequested: false });
    await flush();

    expect(mocks.prepareDurable).not.toHaveBeenCalled();
    expect(storeTab()?.regularReason).toBeUndefined();
  });

  it("leaves terminals unmarked when the account or asset chose regular terminals", async () => {
    mocks.prepareDurable.mockResolvedValue({ state: "disabled" });
    seed();
    mountPane();
    await flush();
    reportAttach({ durable: false, durableRequested: true });
    await flush();

    expect(mocks.prepareDurable).toHaveBeenCalledWith("s1");
    expect(storeTab()?.regularReason).toBeUndefined();
  });

  it("drops the tab-strip marker once the header reports the helper ready", async () => {
    seed({ regularReason: "持久组件准备中" });
    mountPane();
    await flush();
    act(() => {
      useUi.setState((state) => {
        let operations = helperOperationReducer(state.helperOperations, {
          type: "start", tabId: "t1", operationId: "op-1", kind: "deploy", now: Date.now(),
        });
        operations = helperOperationReducer(operations, { type: "succeeded", tabId: "t1", operationId: "op-1", attempt: 1, now: Date.now() });
        return { helperOperations: operations };
      });
    });

    await waitFor(() => expect(storeTab()?.regularReason).toBeUndefined());
    expect(mounted?.container.textContent).toContain("持久已就绪");
  });
});
