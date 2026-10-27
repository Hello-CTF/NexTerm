/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  prepareDurable: vi.fn(),
  helperOperation: vi.fn(),
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
    helperOperation: mocks.helperOperation,
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
    // Background preparation is not worth an interruption; the reason stays in the tab tooltip.
    expect(hintBar()).toBeNull();
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

  it("drops the tab reason once the hint reports the helper ready", async () => {
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
    expect(hintBar()?.textContent).toContain("这台主机的持久终端已就绪");
  });
});

function hintBar(): HTMLElement | null {
  return mounted?.container.querySelector<HTMLElement>('[data-testid="persistence-hint"]') ?? null;
}

function hintButton(label: string): HTMLButtonElement | undefined {
  return [...(hintBar()?.querySelectorAll("button") ?? [])].find(
    (button) => button.textContent === label || button.getAttribute("aria-label") === label,
  );
}

function helperDialog(): Element | null {
  return document.querySelector('[role="dialog"][aria-label="持久终端组件"]');
}

function succeedHelper(): void {
  act(() => {
    useUi.setState((state) => {
      let operations = helperOperationReducer(state.helperOperations, {
        type: "start", tabId: "t1", operationId: "op-1", kind: "deploy", now: Date.now(),
      });
      operations = helperOperationReducer(operations, { type: "succeeded", tabId: "t1", operationId: "op-1", attempt: 1, now: Date.now() });
      return { helperOperations: operations };
    });
  });
}

describe("persistence hint bar", () => {
  it("shows nothing on a durable terminal or one regular by choice", async () => {
    seed({ tabId: "kernel-1" });
    mountPane();
    await flush();
    reportAttach({ durable: true });
    await flush();
    expect(hintBar()).toBeNull();

    mounted?.unmount();
    await setTerminalDefaultMode("regular");
    seed({ tabId: "kernel-1" });
    mountPane();
    await flush();
    reportAttach({ durable: false, durableRequested: false });
    await flush();
    expect(hintBar()).toBeNull();
  });

  it("explains an expected limitation with a neutral dot and the full reason", async () => {
    seed({ regularReason: "打开时持久组件未就绪" });
    mountPane();
    await flush();

    expect(hintBar()?.dataset.tone).toBe("neutral");
    expect(hintBar()?.textContent).toContain("本标签是普通终端（打开时持久组件未就绪），断开后进程会结束。");
    expect(hintButton("持久终端组件…")).toBeDefined();
  });

  it("marks a failed durable terminal in red with the full reason and opens the helper dialog", async () => {
    mocks.helperOperation.mockReturnValue(new Promise(() => {}));
    seed({ regularReason: "持久组件不可用：创建持久终端失败，已改用普通终端" });
    mountPane();
    await flush();

    expect(hintBar()?.dataset.tone).toBe("danger");
    expect(hintBar()?.textContent).toContain("创建持久终端失败，已改用普通终端");
    act(() => hintButton("持久终端组件…")?.click());
    expect(mocks.helperOperation).toHaveBeenCalledWith("s1", "", "status");
  });

  it("explains local terminals without offering the SSH helper", async () => {
    seed({ regularReason: "持久组件不可用：本机终端暂不支持持久模式" }, "local");
    mountPane();
    await flush();

    expect(hintBar()?.dataset.tone).toBe("neutral");
    expect(hintBar()?.textContent).toContain("本机终端暂不支持持久模式");
    expect(hintButton("持久终端组件…")).toBeUndefined();
  });

  it("asks for attention when the helper deployment needs a choice", async () => {
    seed({ regularReason: "持久组件准备中" });
    mountPane();
    await flush();
    act(() => {
      useUi.setState((state) => {
        let operations = helperOperationReducer(state.helperOperations, {
          type: "start", tabId: "t1", operationId: "op-1", kind: "deploy", now: Date.now(),
        });
        operations = helperOperationReducer(operations, { type: "attemptStarted", tabId: "t1", operationId: "op-1", attempt: 1, now: Date.now() });
        operations = helperOperationReducer(operations, {
          type: "attemptFailed", tabId: "t1", operationId: "op-1", attempt: 1, now: Date.now(),
          reason: { kind: "permanent", code: "upload_failed", message: "磁盘空间不足" },
        });
        return { helperOperations: operations };
      });
    });

    expect(hintBar()?.dataset.tone).toBe("danger");
    expect(hintBar()?.textContent).toContain("磁盘空间不足");
    expect(helperDialog()).toBeNull();
    act(() => hintButton("处理")?.click());
    expect(helperDialog()).not.toBeNull();
  });

  it("reopens the terminal as durable from the ready hint", async () => {
    mocks.prepareDurable.mockResolvedValue({ state: "ready" });
    seed({ regularReason: "持久组件准备中" });
    mountPane();
    await flush();
    expect(hintBar()).toBeNull();
    succeedHelper();
    await waitFor(() => expect(hintBar()?.dataset.tone).toBe("accent"));

    act(() => hintButton("在持久终端中重开")?.click());
    await waitFor(() => expect(useUi.getState().workspaces[0]?.panes[0]?.tabs).toHaveLength(2));
    const reopened = useUi.getState().workspaces[0]?.panes[0]?.tabs.find((tab) => tab.id !== "t1");
    expect(reopened).toMatchObject({ kind: "terminal", sessionId: "s1" });
    expect(reopened?.regularReason).toBeUndefined();
    expect(useUi.getState().toasts).toHaveLength(0);
  });

  it("stays dismissed for the tab until a different state appears", async () => {
    seed({ regularReason: "打开时持久组件未就绪" });
    mountPane();
    await flush();
    act(() => hintButton("关闭提示")?.click());
    expect(hintBar()).toBeNull();

    // A remount of the same live tab keeps the dismissal.
    mounted?.unmount();
    mountPane();
    await flush();
    expect(hintBar()).toBeNull();

    succeedHelper();
    await waitFor(() => expect(hintBar()?.dataset.tone).toBe("accent"));
  });
});
