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
import { aiTabBusyIds, aiTabRunControls, clearAiTabRuns } from "../../features/ai/runOwnership";
import { GLOBAL_AI_BOARD_KEY, useUi } from "../../app/store";

function textOf(view: MountedView): string {
  return view.container.textContent ?? "";
}

function stripTabs(view: MountedView): HTMLElement[] {
  return [
    ...view.container.querySelectorAll<HTMLElement>('[aria-label="会话标签"] [role="tab"]'),
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

describe("AiSidebar tab context menu", () => {
  let view: MountedView | null = null;

  beforeEach(async () => {
    vi.clearAllMocks();
    clearAiTabRuns([...aiTabBusyIds, ...aiTabRunControls.keys()]);
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
    await flush();
  });

  afterEach(async () => {
    await flush();
    view?.unmount();
    view = null;
  });

  it("opens the context menu on right-click and updates the title from the regenerated value", async () => {
    await flush();
    openTabMenu(view!);
    expect(menuItem(view!, "重新生成标题").disabled).toBe(false);

    click(menuItem(view!, "重新生成标题"));
    await flush();
    expect(mocks.conversationRetitle).toHaveBeenCalledWith("conv-1");
    await flushUntil(() => textOf(view!).includes("主机名: 新主题"));
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].title).toBe("主机名: 新主题");
    expect(view!.container.querySelector('[role="menu"]')).toBeNull();
  });

  it("shows an error and keeps the title unchanged when regeneration fails", async () => {
    await flush();
    mocks.conversationRetitle.mockRejectedValue(new Error("Model unavailable"));
    openTabMenu(view!);
    click(menuItem(view!, "重新生成标题"));
    await flushUntil(() => mocks.toast.mock.calls.length > 0);

    expect(mocks.toast).toHaveBeenCalledWith(
      "error",
      expect.stringContaining("无法重新生成标题"),
    );
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].title).toBe("主机名: 旧主题");
  });

  it("disables menu items while the tab is busy", async () => {
    await flush();
    await send(view!, "Long-running task");
    expect(useUi.getState().aiBusy).toBe(true);

    openTabMenu(view!);
    expect(menuItem(view!, "重新生成标题").disabled).toBe(true);
    expect(mocks.conversationRetitle).not.toHaveBeenCalled();
  });

  it("disables menu items for tabs without a bound conversation", async () => {
    await flush();
    await act(async () => {
      useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    });
    await flush();

    openTabMenu(view!, 1);
    expect(menuItem(view!, "重新生成标题").disabled).toBe(true);
  });

  it("includes close and bulk-close items, disabling the side without tabs", async () => {
    await flush();
    await act(async () => {
      useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    });
    await flush();

    openTabMenu(view!, 0);
    expect(menuItem(view!, "关闭").disabled).toBe(false);
    expect(menuItem(view!, "关闭左侧全部标签").disabled).toBe(true);
    expect(menuItem(view!, "关闭右侧全部标签").disabled).toBe(false);

    click(menuItem(view!, "关闭"));
    await flushUntil(() => useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs.length === 1);
  });

  it("closes only the tabs to the left", async () => {
    await flush();
    await act(async () => {
      useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    });
    await flush();
    await act(async () => {
      useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    });
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

  it("closes only the tabs to the right", async () => {
    await flush();
    await act(async () => {
      useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    });
    await flush();
    await act(async () => {
      useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    });
    await flush();
    const ids = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs.map((t) => t.id);

    openTabMenu(view!, 1);
    click(menuItem(view!, "关闭右侧全部标签"));
    await flush();
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs.map((t) => t.id)).toEqual([
      ids[0],
      ids[1],
    ]);
  });

  it("waits for the terminal event before closing a busy tab in bulk", async () => {
    await flush();
    await send(view!, "Long-running task");
    expect(useUi.getState().aiBusy).toBe(true);
    await act(async () => {
      useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    });
    await flush();

    openTabMenu(view!, 1);
    click(menuItem(view!, "关闭左侧全部标签"));
    await flushUntil(() => mocks.cancel.mock.calls.length > 0);
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("会停止它"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs).toHaveLength(2);
    expect(useUi.getState().aiBusy).toBe(true);

    act(() => mocks.channels[0].onEvent({ type: "canceled", message: "已停止" }));
    await flushUntil(() => useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs.length === 1);
    expect(mocks.cancel).toHaveBeenCalledWith("job-1");
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("keeps a busy tab when bulk-close confirmation is rejected", async () => {
    await flush();
    await send(view!, "Long-running task");
    await act(async () => {
      useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    });
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

  it("stops a bulk close at the first unconfirmed cancellation and keeps every remaining tab", async () => {
    await flush();
    let sequence = 0;
    mocks.chat.mockImplementation(() => {
      sequence += 1;
      return Promise.resolve({ jobId: `job-${sequence}`, conversationId: `conv-${sequence}` });
    });
    await send(view!, "First task");
    await act(async () => {
      useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    });
    await flush();
    await send(view!, "Second task");
    await act(async () => {
      useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    });
    await flush();
    mocks.cancel.mockRejectedValueOnce(new Error("offline"));

    openTabMenu(view!, 2);
    click(menuItem(view!, "关闭左侧全部标签"));
    await flushUntil(() => mocks.toast.mock.calls.some(([, text]) => String(text).includes("没能确认任务已停止")));

    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs).toHaveLength(3);
    expect(mocks.cancel).toHaveBeenCalledTimes(1);
    expect(mocks.cancel).toHaveBeenCalledWith("job-1");
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(useUi.getState().aiBusy).toBe(true);
  });
});
