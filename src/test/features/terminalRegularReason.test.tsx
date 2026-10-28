/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, waitForRendered, type MountedView } from "./reactTestUtils";

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
    // The backend still knows the seeded sessions, so a resync after attach keeps them.
    list: vi.fn(async () => useUi.getState().sessions),
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
  listenEvent: vi.fn((name: string, handler: (payload: unknown) => void) => {
    harness.events.set(name, handler);
    return Promise.resolve(() => {});
  }),
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
  events: new Map<string, (payload: unknown) => void>(),
  // Counts renders of the pane's view, a probe for renders of the pane itself.
  renders: 0,
}));

vi.mock("../../features/terminal/XtermView", () => ({
  XtermView: (props: Record<string, unknown>) => {
    harness.props = props;
    harness.renders += 1;
    return null;
  },
}));

import { TerminalPane } from "../../features/terminal/TerminalPane";
import { createHelperOperationState, helperOperationReducer } from "../../features/terminal/helperOperationState";
import { fallbackTerminalReason, forgetDurableReadiness, noteHelperProgress, useUi, type AppTab } from "../../app/store";
import { TabPersistenceMark, TabStripItem, tabTooltip } from "../../app/workspace/tabStrip";
import type { PersistenceTabInfo } from "../../features/terminal/persistenceHintStore";
import { setLocalHelperStatusForTest } from "../../features/terminal/localHelperStatus";
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
      tabId: "kernel-1", sizeOwner: "me", subscribers: 1, viewers: 1, exited: false, shell: "posix", retainable: false,
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

    await waitForRendered(() => expect(storeTab()?.regularReason).toBe("持久组件准备中"));
    await waitForRendered(() => expect(hintBar()?.dataset.tone).toBe("neutral"));
    expect(hintBar()?.textContent).toContain("持久组件准备中");
  });

  it("names later fallbacks on the same connection that carry no backend notice", async () => {
    mocks.prepareDurable.mockResolvedValue({ state: "unavailable", reason: "目标主机不支持持久终端组件" });
    seed();
    mountPane();
    await flush();
    reportAttach({ durable: false, durableRequested: true });

    await waitForRendered(() => expect(storeTab()?.regularReason).toBe("持久组件不可用：目标主机不支持持久终端组件"));
  });

  it("names a durable asset override that fell back under the regular default", async () => {
    await setTerminalDefaultMode("regular");
    seed();
    mountPane();
    await flush();
    reportAttach({ durable: false, durableRequested: true });

    await waitForRendered(() => expect(storeTab()?.regularReason).toBe("持久组件准备中"));
  });

  it("names a reopened or AI-created regular terminal on a host that prefers durable terminals", async () => {
    mocks.prepareDurable.mockResolvedValue({ state: "ready" });
    seed({ tabId: "kernel-1" });
    mountPane();
    await flush();
    reportAttach({ durable: false });

    await waitForRendered(() => expect(storeTab()?.regularReason).toBe("打开时持久组件未就绪"));
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

  it("keeps the reason while the hint reports the helper ready", async () => {
    seed({ regularReason: "持久组件准备中" });
    mountPane();
    await flush();
    succeedHelper();

    await waitForRendered(() => expect(hintBar()?.textContent).toContain("该主机的持久终端已就绪"));
    expect(storeTab()?.regularReason).toBe("持久组件准备中");
  });
});

type OperationsState = ReturnType<typeof createHelperOperationState>;

function hintBar(container: ParentNode | undefined = mounted?.container): HTMLElement | null {
  return container?.querySelector<HTMLElement>('[data-testid="persistence-hint"]') ?? null;
}

function hintButton(label: string, container?: ParentNode): HTMLButtonElement | undefined {
  return [...(hintBar(container)?.querySelectorAll("button") ?? [])].find(
    (button) => button.textContent === label || button.getAttribute("aria-label") === label,
  );
}

function helperDialog(): Element | null {
  return document.querySelector('[role="dialog"][aria-label="持久终端组件"]');
}

function operations(...steps: Array<(state: OperationsState) => OperationsState>): void {
  act(() => {
    useUi.setState((state) => ({ helperOperations: steps.reduce((acc, step) => step(acc), state.helperOperations) }));
  });
}

const startOp = (kind: "deploy" | "reattach" = "deploy") => (state: OperationsState) =>
  helperOperationReducer(state, { type: "start", tabId: "t1", operationId: "op-1", kind, now: Date.now() });
const failOp = (message: string) => (state: OperationsState) => {
  const started = helperOperationReducer(state, { type: "attemptStarted", tabId: "t1", operationId: "op-1", attempt: 1, now: Date.now() });
  return helperOperationReducer(started, {
    type: "attemptFailed", tabId: "t1", operationId: "op-1", attempt: 1, now: Date.now(),
    reason: { kind: "permanent", code: "upload_failed", message },
  });
};
const succeedOp = (state: OperationsState) =>
  helperOperationReducer(state, { type: "succeeded", tabId: "t1", operationId: "op-1", attempt: 1, now: Date.now() });

function succeedHelper(): void {
  operations(startOp(), succeedOp);
}

// The helper progress listener records readiness and then stores the progress, as the app shell does.
function helperProgress(stage: string, done: boolean, extra: Record<string, unknown> = {}): void {
  const progress = {
    sessionId: "s1", assetId: "asset-1", operationId: "", attempt: 0, attempts: 0, deadline: 0,
    step: 1, steps: 1, stage, transferred: 0, total: 0, percent: 0, done, ...extra,
  };
  act(() => {
    noteHelperProgress(progress);
    useUi.getState().setHelperProgress(progress);
  });
}

const marks: MountedView[] = [];
afterEach(() => {
  while (marks.length) marks.pop()?.unmount();
});

function tabMark(): HTMLButtonElement | null {
  const view = mount(createElement(TabPersistenceMark, { tab: storeTab()! }));
  marks.push(view);
  return view.container.querySelector<HTMLButtonElement>('button[aria-label^="重新显示提示"]');
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

  it("shows nothing on a terminal regular by choice although the helper is ready", async () => {
    seed({ tabId: "kernel-1" });
    mountPane();
    await flush();
    helperProgress("ready", true);
    await flush();
    expect(hintBar()).toBeNull();
  });

  it("stays quiet while the deployment this tab started runs", async () => {
    seed({ regularReason: "持久组件准备中" });
    mountPane();
    await flush();
    operations(startOp());
    await flush();
    expect(hintBar()).toBeNull();
  });

  it("explains an expected limitation with a neutral dot and the full reason", async () => {
    seed({ regularReason: "打开时持久组件未就绪" });
    mountPane();
    await flush();

    expect(hintBar()?.dataset.tone).toBe("neutral");
    expect(hintBar()?.textContent).toContain("本标签页是临时终端（打开时持久组件未就绪），断开后进程会结束。");
    expect(hintButton("持久终端组件…")).toBeDefined();
  });

  it("marks a failed durable terminal in red with the full reason and opens the helper dialog", async () => {
    mocks.helperOperation.mockReturnValue(new Promise(() => {}));
    seed({ regularReason: "持久组件不可用：创建持久终端失败，已改用临时终端" });
    mountPane();
    await flush();

    expect(hintBar()?.dataset.tone).toBe("danger");
    expect(hintBar()?.textContent).toContain("创建持久终端失败，已改用临时终端");
    act(() => hintButton("持久终端组件…")?.click());
    expect(mocks.helperOperation).toHaveBeenCalledWith("s1", "", "status");
  });

  it("asks for attention when the helper deployment needs a choice, without a duplicate toast", async () => {
    seed({ regularReason: "持久组件准备中" });
    mountPane();
    await flush();
    operations(startOp(), failOp("磁盘空间不足"));

    expect(hintBar()?.dataset.tone).toBe("danger");
    expect(hintBar()?.textContent).toContain("持久终端暂时不可用：磁盘空间不足");
    expect(useUi.getState().toasts).toHaveLength(0);
    expect(helperDialog()).toBeNull();
    act(() => hintButton("处理")?.click());
    expect(helperDialog()).not.toBeNull();
  });

  it("keeps the toast for a deployment choice on a hidden pane", async () => {
    seed({ regularReason: "持久组件准备中" });
    mounted = mount(createElement(TerminalPane, { sessionId: "s1", title: "web-01", storeTabId: "t1", visible: false }));
    await flush();
    operations(startOp(), failOp("磁盘空间不足"));

    expect(useUi.getState().toasts.map((toast) => toast.text)).toContain("暂时无法使用持久终端，该标签页已改用临时终端");
  });

  it("names a failed restore of a durable terminal as such", async () => {
    seed({ tabId: "kernel-1", durable: true });
    mountPane();
    await flush();
    operations(startOp("reattach"), failOp("远端进程已退出"));

    expect(hintBar()?.dataset.tone).toBe("danger");
    expect(hintBar()?.textContent).toContain("原持久终端恢复失败：远端进程已退出");
    expect(hintBar()?.textContent).not.toContain("本标签页是临时终端");
    act(() => hintButton("处理")?.click());
    expect(helperDialog()).not.toBeNull();
  });

  it("reopens the terminal as durable from the ready hint", async () => {
    mocks.prepareDurable.mockResolvedValue({ state: "ready" });
    seed({ regularReason: "持久组件准备中" });
    mountPane();
    await flush();
    succeedHelper();
    await waitForRendered(() => expect(hintBar()?.dataset.tone).toBe("accent"));

    act(() => hintButton("新建持久终端")?.click());
    await waitForRendered(() => expect(useUi.getState().workspaces[0]?.panes[0]?.tabs).toHaveLength(2));
    const reopened = useUi.getState().workspaces[0]?.panes[0]?.tabs.find((tab) => tab.id !== "t1");
    expect(reopened).toMatchObject({ kind: "terminal", sessionId: "s1" });
    expect(reopened?.regularReason).toBeUndefined();
    expect(useUi.getState().toasts).toHaveLength(0);
  });

  it("opens the new durable terminal in the folder this one is in, keeping this tab", async () => {
    mocks.prepareDurable.mockResolvedValue({ state: "ready" });
    seed({ tabId: "kernel-1", regularReason: "打开时持久组件未就绪" });
    mountPane();
    await flush();
    act(() => (harness.props?.onAttach as ((id: string) => void) | undefined)?.("kernel-1"));
    act(() => harness.events.get("terminal-control")?.({
      tabId: "kernel-1", version: 1, sizeOwner: "me", subscribers: 1, viewers: 1, exited: false,
      cols: 80, rows: 24, gridRevision: 0, cwd: "/srv/app",
    }));
    helperProgress("ready", true);
    await waitForRendered(() => expect(hintBar()?.dataset.tone).toBe("accent"));

    act(() => hintButton("新建持久终端")?.click());
    await waitForRendered(() => expect(useUi.getState().workspaces[0]?.panes[0]?.tabs).toHaveLength(2));
    const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
    expect(tabs.map((tab) => tab.id)).toContain("t1");
    expect(tabs.find((tab) => tab.id !== "t1")?.pendingDirectory).toBe("/srv/app");
  });

  it("offers every regular tab on a session the ready helper, including after a remount", async () => {
    const second: AppTab = { id: "t2", kind: "terminal", title: "终端 2", sessionId: "s1", closable: true, regularReason: "持久组件准备中" };
    seed({ regularReason: "持久组件准备中" });
    useUi.setState((state) => ({
      workspaces: state.workspaces.map((ws) => ({ ...ws, panes: ws.panes.map((pane) => ({ ...pane, tabs: [...pane.tabs, second] })) })),
    }));
    mountPane();
    const other = mount(createElement(TerminalPane, { sessionId: "s1", title: "web-01", storeTabId: "t2" }));
    try {
      await flush();
      expect(hintBar()?.dataset.tone).toBe("neutral");
      expect(hintBar(other.container)?.dataset.tone).toBe("neutral");

      helperProgress("ready", true);
      await waitForRendered(() => expect(hintBar()?.dataset.tone).toBe("accent"));
      expect(hintButton("新建持久终端", other.container)).toBeDefined();

      mounted?.unmount();
      mountPane();
      await flush();
      expect(hintBar()?.dataset.tone).toBe("accent");
    } finally {
      other.unmount();
    }
  });

  it("reports an unavailable helper on a tab that opened while it was preparing", async () => {
    seed({ regularReason: "持久组件准备中" });
    mountPane();
    await flush();
    helperProgress("failed", true, { error: "目标主机不支持持久终端组件" });
    await waitForRendered(() => expect(hintBar()?.dataset.tone).toBe("danger"));
    expect(hintBar()?.textContent).toContain("目标主机不支持持久终端组件");
  });

  it("names the live state in the tab tooltip instead of the reason recorded at open", async () => {
    seed({ regularReason: "持久组件准备中" });
    mountPane();
    await flush();
    const tooltip = mount(
      createElement(TabStripItem, {
        tab: storeTab()!,
        children: (unread: boolean, persistence: PersistenceTabInfo) => createElement("span", null, tabTooltip(storeTab()!, unread, persistence)),
      }),
    );
    marks.push(tooltip);
    expect(tooltip.container.textContent).toContain("持久组件准备中");

    helperProgress("ready", true);
    await waitForRendered(() => expect(tooltip.container.textContent).toContain("持久终端已就绪"));
    expect(tooltip.container.textContent).not.toContain("持久组件准备中");
  });

  it("updates the hint when a readiness query answers, without any other change", async () => {
    mocks.prepareDurable.mockResolvedValue({ state: "ready" });
    seed({ regularReason: "持久组件准备中" });
    mountPane();
    await flush();
    expect(hintBar()?.dataset.tone).toBe("neutral");

    await act(async () => {
      await fallbackTerminalReason("s1", { fallbackNotice: false, durableRequested: true, kind: "ssh" });
    });
    expect(hintBar()?.dataset.tone).toBe("accent");
  });

  it("does not re-render a pane for helper progress on another session", async () => {
    seed({ regularReason: "持久组件准备中" });
    mountPane();
    await flush();
    const before = harness.renders;

    for (const percent of [10, 40, 80]) {
      helperProgress("upload", false, { sessionId: "s2", assetId: "asset-2", transferred: percent, total: 100, percent });
    }
    await flush();
    expect(harness.renders).toBe(before);
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
    await waitForRendered(() => expect(hintBar()?.dataset.tone).toBe("accent"));
  });

  it("leaves a named red mark on the tab after a failure hint is dismissed", async () => {
    seed({ regularReason: "持久组件不可用：创建持久终端失败，已改用临时终端" });
    mountPane();
    await flush();
    expect(tabMark()).toBeNull();

    act(() => hintButton("关闭提示")?.click());
    expect(hintBar()).toBeNull();
    const mark = tabMark();
    expect(mark?.getAttribute("aria-label")).toContain("创建持久终端失败，已改用临时终端");
    expect(mark?.getAttribute("title")).toContain("创建持久终端失败，已改用临时终端");

    act(() => mark?.click());
    await flush();
    expect(hintBar()?.dataset.tone).toBe("danger");
    expect(tabMark()).toBeNull();
  });

  it("leaves no mark after an expected limitation is dismissed", async () => {
    seed({ regularReason: "打开时持久组件未就绪" });
    mountPane();
    await flush();
    act(() => hintButton("关闭提示")?.click());
    expect(tabMark()).toBeNull();
  });
});

describe("local terminal hints", () => {
  afterEach(() => {
    act(() => setLocalHelperStatusForTest(null));
  });

  it("offers a ready local helper to a regular local tab", async () => {
    setLocalHelperStatusForTest({ state: "ready" });
    seed({ regularReason: "持久组件准备中" }, "local");
    mountPane();
    await flush();

    expect(hintBar()?.dataset.tone).toBe("accent");
    expect(hintBar()?.textContent).toContain("本机的持久终端已就绪");
    expect(hintButton("新建持久终端")).toBeDefined();
  });

  it("keeps a starting local helper neutral and a failed one red with its settings", async () => {
    setLocalHelperStatusForTest({ state: "starting" });
    seed({ regularReason: "持久组件准备中" }, "local");
    mountPane();
    await flush();
    expect(hintBar()?.dataset.tone).toBe("neutral");
    expect(hintButton("本机持久组件设置…")).toBeUndefined();

    act(() => setLocalHelperStatusForTest({ state: "failed", reason: "本机持久组件启动失败" }));
    await flush();
    expect(hintBar()?.dataset.tone).toBe("danger");
    expect(hintBar()?.textContent).toContain("本机持久组件启动失败");
    act(() => hintButton("本机持久组件设置…")?.click());
    expect(useUi.getState().workspaces.some((ws) => ws.panes.some((pane) => pane.tabs.some((tab) => tab.kind === "settings")))).toBe(true);
  });

  it("keeps a remote host's ready hint while the local helper changes state", async () => {
    seed({ regularReason: "持久组件准备中" });
    mountPane();
    await flush();
    helperProgress("ready", true);
    await waitForRendered(() => expect(hintBar()?.dataset.tone).toBe("accent"));

    for (const state of ["idle", "starting", "ready"]) {
      act(() => setLocalHelperStatusForTest({ state }));
      await flush();
      expect(hintBar()?.dataset.tone).toBe("accent");
    }
    expect(hintBar()?.textContent).toContain("该主机的持久终端已就绪");
  });

  it("shows nothing when this device keeps local terminals regular", async () => {
    setLocalHelperStatusForTest({ state: "disabled" });
    seed({ regularReason: "持久组件准备中" }, "local");
    mountPane();
    await flush();
    expect(hintBar()).toBeNull();
  });
});
