/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

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
  setPermission: vi.fn(),
  conversationList: vi.fn(),
  conversationDelete: vi.fn(),
  conversationRetitle: vi.fn(),
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
    setPermission: mocks.setPermission,
    conversationList: mocks.conversationList,
    conversationDelete: mocks.conversationDelete,
    conversationRetitle: mocks.conversationRetitle,
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

function textOf(view: MountedView): string {
  return view.container.textContent ?? "";
}

function stripTabs(view: MountedView): HTMLElement[] {
  return [
    ...view.container.querySelectorAll<HTMLElement>('[aria-label="AI 任务标签"] [role="tab"]'),
  ];
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

async function send(view: MountedView, text: string) {
  const textarea = view.container.querySelector("textarea");
  if (!textarea) throw new Error("input textarea not found");
  setInputValue(textarea, text);
  click(view.container.querySelector('button[title="发送 (Enter)"]')!);
  await flush();
}

describe("AiSidebar 标签右键菜单", () => {
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
    mocks.setPermission.mockResolvedValue(undefined);
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "主机名: 旧主题", updatedAt: 0 }]);
    mocks.conversationDelete.mockResolvedValue(undefined);
    mocks.conversationRetitle.mockResolvedValue("主机名: 新主题");
    mocks.messages.mockResolvedValue([]);
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
          tabs: [{ id: "tab-1", title: "主机名: 旧主题", conversationId: "conv-1" }],
          activeTabId: "tab-1",
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

  it("右键标签弹出菜单，重新生成后按返回值更新标题", async () => {
    await flush();
    openTabMenu(view!);
    expect(menuItem(view!, "重新生成标题").disabled).toBe(false);

    click(menuItem(view!, "重新生成标题"));
    await flushUntil(() => mocks.conversationRetitle.mock.calls.length > 0);
    expect(mocks.conversationRetitle).toHaveBeenCalledWith("conv-1");
    await flushUntil(() => textOf(view!).includes("主机名: 新主题"));
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].title).toBe("主机名: 新主题");
    expect(view!.container.querySelector('[role="menu"]')).toBeNull();
  });

  it("重新生成失败时提示且标题不变", async () => {
    await flush();
    mocks.conversationRetitle.mockRejectedValue(new Error("模型不可用"));
    openTabMenu(view!);
    click(menuItem(view!, "重新生成标题"));
    await flushUntil(() => mocks.toast.mock.calls.length > 0);

    expect(mocks.toast).toHaveBeenCalledWith(
      "error",
      expect.stringContaining("重新生成标题失败"),
    );
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].title).toBe("主机名: 旧主题");
  });

  it("标签忙时菜单项禁用", async () => {
    await flush();
    await send(view!, "长跑任务");
    expect(useUi.getState().aiBusy).toBe(true);

    openTabMenu(view!);
    expect(menuItem(view!, "重新生成标题").disabled).toBe(true);
    expect(mocks.conversationRetitle).not.toHaveBeenCalled();
  });

  it("未绑定会话的标签菜单项禁用", async () => {
    await flush();
    useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    await flush();

    openTabMenu(view!, 1);
    expect(menuItem(view!, "重新生成标题").disabled).toBe(true);
  });

  it("菜单含关闭与批量关闭项，单侧无标签时对应项禁用", async () => {
    await flush();
    useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    await flush();

    openTabMenu(view!, 0);
    expect(menuItem(view!, "关闭").disabled).toBe(false);
    expect(menuItem(view!, "关闭左侧全部标签").disabled).toBe(true);
    expect(menuItem(view!, "关闭右侧全部标签").disabled).toBe(false);

    click(menuItem(view!, "关闭"));
    await flushUntil(() => useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs.length === 1);
  });

  it("关闭左侧全部标签只关对应标签", async () => {
    await flush();
    useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    await flush();
    useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    await flush();
    const ids = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs.map((t) => t.id);
    expect(ids).toHaveLength(3);

    openTabMenu(view!, 1);
    click(menuItem(view!, "关闭左侧全部标签"));
    await flushUntil(() => useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs.length === 2);
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs.map((t) => t.id)).toEqual([
      ids[1],
      ids[2],
    ]);
  });

  it("关闭右侧全部标签只关对应标签", async () => {
    await flush();
    useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    await flush();
    useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    await flush();
    const ids = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs.map((t) => t.id);

    openTabMenu(view!, 1);
    click(menuItem(view!, "关闭右侧全部标签"));
    await flushUntil(() => useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs.length === 2);
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs.map((t) => t.id)).toEqual([
      ids[0],
      ids[1],
    ]);
  });

  it("批量关闭遇到忙标签先确认，确认后停止并关闭", async () => {
    await flush();
    await send(view!, "长跑任务");
    expect(useUi.getState().aiBusy).toBe(true);
    useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    await flush();

    openTabMenu(view!, 1);
    click(menuItem(view!, "关闭左侧全部标签"));
    await flushUntil(() => mocks.ask.mock.calls.length > 0);
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("关闭将停止它"),
      expect.objectContaining({ kind: "warning" }),
    );
    await flushUntil(() => useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs.length === 1);
    expect(mocks.cancel).toHaveBeenCalledWith("job-1");
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("批量关闭遇到忙标签可拒绝，拒绝后保留", async () => {
    await flush();
    await send(view!, "长跑任务");
    useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    await flush();
    mocks.ask.mockResolvedValue(false);

    openTabMenu(view!, 1);
    click(menuItem(view!, "关闭左侧全部标签"));
    await flushUntil(() => mocks.ask.mock.calls.length > 0);
    await flush();
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs).toHaveLength(2);
    expect(useUi.getState().aiBusy).toBe(true);
    expect(mocks.cancel).not.toHaveBeenCalled();
  });
});
