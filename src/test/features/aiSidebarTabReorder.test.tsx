/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  chat: vi.fn(),
  cancel: vi.fn(),
  confirm: vi.fn(),
  answer: vi.fn(),
  hitlSnapshot: vi.fn(),
  hitlEvents: vi.fn(),
  runs: vi.fn(),
  runEvents: vi.fn(),
  getPermission: vi.fn(),
  conversationList: vi.fn(),
  conversationDelete: vi.fn(),
  messages: vi.fn(),
  overview: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
  dispose: vi.fn(),
  channels: [] as { onEvent: (ev: Record<string, unknown>) => void }[],
}));

vi.mock("../../ipc/commands", () => ({
  aiApi: {
    chat: mocks.chat,
    cancel: mocks.cancel,
    confirm: mocks.confirm,
    answer: mocks.answer,
    hitlSnapshot: mocks.hitlSnapshot,
    hitlEvents: mocks.hitlEvents,
    runs: mocks.runs,
    runEvents: mocks.runEvents,
    getPermission: mocks.getPermission,
    conversationList: mocks.conversationList,
    conversationDelete: mocks.conversationDelete,
    messages: mocks.messages,
  },
  modelApi: { overview: mocks.overview, activate: vi.fn() },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));
vi.mock("../../ipc/events", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/events")>()),
  createAiChannel: (onEvent: (ev: Record<string, unknown>) => void) => {
    const channel = { onEvent };
    mocks.channels.push(channel);
    return channel;
  },
  disposeChannel: mocks.dispose,
  onChannelReopen: () => () => undefined,
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, promptText: vi.fn() }));

import { AiSidebar } from "../../features/ai/AiSidebar";
import { GLOBAL_AI_BOARD_KEY, useUi } from "../../app/store";

const DRAG_AI_TAB = "application/x-nexterm-ai-tab";

function stripTabs(view: MountedView): HTMLElement[] {
  return [
    ...view.container.querySelectorAll<HTMLElement>('[aria-label="AI 任务标签"] [role="tab"]'),
  ];
}

function storeTabIds(): string[] {
  return useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs.map((t) => t.id);
}

function activeStoreTabId(): string | null {
  return useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].activeTabId;
}

function openTabMenu(view: MountedView, index = 0): void {
  act(() => {
    stripTabs(view)[index].dispatchEvent(
      new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 8, clientY: 8 }),
    );
  });
}

function menuItem(view: MountedView, label: string): HTMLButtonElement {
  const item = [...view.container.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(
    (b) => b.textContent?.includes(label),
  );
  if (!item) throw new Error(`menu item not found: ${label}`);
  return item;
}

function pressKey(el: Element, key: string, init: { ctrl?: boolean; shift?: boolean } = {}): void {
  act(() => {
    el.dispatchEvent(
      new KeyboardEvent("keydown", {
        key,
        ctrlKey: !!init.ctrl,
        shiftKey: !!init.shift,
        bubbles: true,
        cancelable: true,
      }),
    );
  });
}

function stubRect(el: Element, left: number, width: number): void {
  el.getBoundingClientRect = () =>
    ({
      left,
      right: left + width,
      width,
      top: 0,
      bottom: 24,
      height: 24,
      x: left,
      y: 0,
      toJSON: () => ({}),
    }) as DOMRect;
}

function fireDrag(
  el: Element,
  type: string,
  opts: { data?: Record<string, string>; clientX?: number } = {},
): { event: MouseEvent; data: Map<string, string>; dt: { effectAllowed: string; dropEffect: string } } {
  const event = new MouseEvent(type, {
    bubbles: true,
    cancelable: true,
    clientX: opts.clientX ?? 0,
  });
  const data = new Map(Object.entries(opts.data ?? {}));
  const dt = {
    types: [...data.keys()],
    getData: (k: string) => data.get(k) ?? "",
    setData: (k: string, v: string) => void data.set(k, v),
    effectAllowed: "",
    dropEffect: "",
  };
  Object.defineProperty(event, "dataTransfer", { value: dt });
  act(() => {
    el.dispatchEvent(event);
  });
  return { event, data, dt };
}

describe("AiSidebar 标签排序", () => {
  let view: MountedView | null = null;

  beforeEach(() => {
    vi.clearAllMocks();
    mocks.channels.length = 0;
    mocks.chat.mockResolvedValue({ jobId: "job-1", conversationId: "conv-1" });
    mocks.cancel.mockResolvedValue(undefined);
    mocks.confirm.mockResolvedValue(undefined);
    mocks.answer.mockResolvedValue(undefined);
    mocks.hitlSnapshot.mockResolvedValue({
      runId: "job-1",
      checkpointId: "job-1",
      status: "running",
      attempt: 1,
      seq: 0,
      pending: [],
    });
    mocks.hitlEvents.mockResolvedValue([]);
    mocks.runs.mockResolvedValue([]);
    mocks.runEvents.mockResolvedValue([]);
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.conversationList.mockResolvedValue([]);
    mocks.conversationDelete.mockResolvedValue(undefined);
    mocks.messages.mockResolvedValue([]);
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    mocks.ask.mockResolvedValue(true);
    localStorage.clear();
    useUi.setState({
      rightOpen: true,
      aiBusy: false,
      takeover: null,
      pushToast: mocks.toast,
      workspaces: [],
      aiBoards: {
        [GLOBAL_AI_BOARD_KEY]: {
          tabs: [
            { id: "tab-a", title: "甲" },
            { id: "tab-b", title: "乙" },
            { id: "tab-c", title: "丙" },
          ],
          activeTabId: "tab-b",
        },
      },
      sessions: [],
      activeWorkspaceId: null,
    });
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
  });

  afterEach(() => {
    view?.unmount();
    view = null;
  });

  it("右键菜单左移/右移标签, 边界项禁用, activeTabId 不变", async () => {
    await flush();
    openTabMenu(view!, 0);
    expect(menuItem(view!, "左移标签").disabled).toBe(true);
    expect(menuItem(view!, "右移标签").disabled).toBe(false);

    click(menuItem(view!, "右移标签"));
    await flush();
    expect(storeTabIds()).toEqual(["tab-b", "tab-a", "tab-c"]);
    expect(activeStoreTabId()).toBe("tab-b");
    expect(stripTabs(view!).map((el) => el.getAttribute("aria-selected"))).toEqual([
      "true",
      "false",
      "false",
    ]);

    openTabMenu(view!, 2);
    expect(menuItem(view!, "右移标签").disabled).toBe(true);
    expect(menuItem(view!, "左移标签").disabled).toBe(false);

    click(menuItem(view!, "左移标签"));
    await flush();
    expect(storeTabIds()).toEqual(["tab-b", "tab-c", "tab-a"]);
    expect(activeStoreTabId()).toBe("tab-b");
  });

  it("Ctrl+Shift+ArrowLeft/Right 移动当前标签, 焦点保持在原节点", async () => {
    await flush();
    const tabB = stripTabs(view!)[1];
    act(() => tabB.focus());
    expect(document.activeElement).toBe(tabB);

    pressKey(tabB, "ArrowLeft", { ctrl: true, shift: true });
    await flush();
    expect(storeTabIds()).toEqual(["tab-b", "tab-a", "tab-c"]);
    expect(activeStoreTabId()).toBe("tab-b");
    expect(stripTabs(view!)[0]).toBe(tabB);
    expect(document.activeElement).toBe(tabB);

    pressKey(tabB, "ArrowRight", { ctrl: true, shift: true });
    await flush();
    expect(storeTabIds()).toEqual(["tab-a", "tab-b", "tab-c"]);
    expect(stripTabs(view!)[1]).toBe(tabB);
    expect(document.activeElement).toBe(tabB);
  });

  it("移动键在边界为 no-op, 未按 Ctrl+Shift 的方向键不触发移动", async () => {
    await flush();
    const tabB = stripTabs(view!)[1];
    act(() => tabB.focus());

    pressKey(tabB, "ArrowLeft", { ctrl: true, shift: true });
    await flush();
    expect(storeTabIds()).toEqual(["tab-b", "tab-a", "tab-c"]);

    pressKey(tabB, "ArrowLeft", { ctrl: true, shift: true });
    await flush();
    expect(storeTabIds()).toEqual(["tab-b", "tab-a", "tab-c"]);
    expect(document.activeElement).toBe(tabB);

    pressKey(tabB, "ArrowRight", { ctrl: true, shift: true });
    pressKey(tabB, "ArrowRight", { ctrl: true, shift: true });
    await flush();
    expect(storeTabIds()).toEqual(["tab-a", "tab-c", "tab-b"]);

    pressKey(tabB, "ArrowRight", { ctrl: true, shift: true });
    await flush();
    expect(storeTabIds()).toEqual(["tab-a", "tab-c", "tab-b"]);
    expect(activeStoreTabId()).toBe("tab-b");

    pressKey(tabB, "ArrowLeft");
    pressKey(tabB, "ArrowLeft", { ctrl: true });
    pressKey(tabB, "ArrowLeft", { shift: true });
    await flush();
    expect(storeTabIds()).toEqual(["tab-a", "tab-c", "tab-b"]);
  });

  it("拖拽排序: 前半段插到目标之前, 后半段插到目标之后, activeTabId 不变", async () => {
    await flush();
    const tabs = stripTabs(view!);
    tabs.forEach((el, i) => stubRect(el, i * 100, 80));

    const started = fireDrag(tabs[0], "dragstart");
    expect(started.data.get(DRAG_AI_TAB)).toBe("tab-a");
    expect(started.dt.effectAllowed).toBe("move");

    const dropped = fireDrag(tabs[2], "drop", { data: { [DRAG_AI_TAB]: "tab-a" }, clientX: 250 });
    expect(dropped.event.defaultPrevented).toBe(true);
    await flush();
    expect(storeTabIds()).toEqual(["tab-b", "tab-c", "tab-a"]);
    expect(activeStoreTabId()).toBe("tab-b");
    expect(stripTabs(view!).map((el) => el.textContent)).toEqual(["乙", "丙", "甲"]);

    const reordered = stripTabs(view!);
    reordered.forEach((el, i) => stubRect(el, i * 100, 80));
    fireDrag(reordered[2], "dragstart");
    fireDrag(reordered[0], "drop", { data: { [DRAG_AI_TAB]: "tab-a" }, clientX: 10 });
    await flush();
    expect(storeTabIds()).toEqual(["tab-a", "tab-b", "tab-c"]);
    expect(activeStoreTabId()).toBe("tab-b");
    expect(stripTabs(view!).map((el) => el.textContent)).toEqual(["甲", "乙", "丙"]);
  });

  it("dragover 指示 class 随位置切换, dragleave/dragend 清除且不改动顺序", async () => {
    await flush();
    const tabs = stripTabs(view!);
    tabs.forEach((el, i) => stubRect(el, i * 100, 80));

    fireDrag(tabs[0], "dragstart");
    const overBefore = fireDrag(tabs[2], "dragover", {
      data: { [DRAG_AI_TAB]: "tab-a" },
      clientX: 210,
    });
    expect(overBefore.event.defaultPrevented).toBe(true);
    expect(tabs[2].className).toContain("is-drop-before");

    fireDrag(tabs[2], "dragover", { data: { [DRAG_AI_TAB]: "tab-a" }, clientX: 260 });
    expect(tabs[2].className).toContain("is-drop-after");

    fireDrag(tabs[2], "dragleave", { data: { [DRAG_AI_TAB]: "tab-a" } });
    expect(tabs[2].className).not.toContain("is-drop-after");

    fireDrag(tabs[1], "dragover", { data: { [DRAG_AI_TAB]: "tab-a" }, clientX: 110 });
    expect(tabs[1].className).toContain("is-drop-before");

    fireDrag(tabs[0], "dragend");
    expect(stripTabs(view!).every((el) => !el.className.includes("is-drop-"))).toBe(true);
    expect(storeTabIds()).toEqual(["tab-a", "tab-b", "tab-c"]);
  });

  it("异源拖拽与自身投放被忽略", async () => {
    await flush();
    const tabs = stripTabs(view!);
    tabs.forEach((el, i) => stubRect(el, i * 100, 80));

    const foreignOver = fireDrag(tabs[1], "dragover", {
      data: { "text/plain": "x" },
      clientX: 110,
    });
    expect(foreignOver.event.defaultPrevented).toBe(false);
    expect(tabs[1].className).not.toContain("is-drop-");

    fireDrag(tabs[1], "drop", { data: { "text/plain": "x" }, clientX: 110 });
    await flush();
    expect(storeTabIds()).toEqual(["tab-a", "tab-b", "tab-c"]);

    fireDrag(tabs[1], "drop", { data: { [DRAG_AI_TAB]: "tab-b" }, clientX: 110 });
    await flush();
    expect(storeTabIds()).toEqual(["tab-a", "tab-b", "tab-c"]);
    expect(activeStoreTabId()).toBe("tab-b");
  });
});
