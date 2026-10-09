/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import {
  click,
  flush,
  mount,
  setInputValue,
  type MountedView,
} from "./reactTestUtils";

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

let convSeq = 0;

function emit(ev: Record<string, unknown>, channelIndex: number) {
  const channel = mocks.channels[channelIndex];
  if (!channel) throw new Error(`no fake channel at ${channelIndex}`);
  act(() => channel.onEvent(ev));
}

function textOf(view: MountedView): string {
  return view.container.textContent ?? "";
}

function stripTabs(view: MountedView): HTMLElement[] {
  return [
    ...view.container.querySelectorAll<HTMLElement>('[aria-label="AI 任务标签"] [role="tab"]'),
  ];
}

async function send(view: MountedView, text: string) {
  const textarea = view.container.querySelector("textarea");
  if (!textarea) throw new Error("input textarea not found");
  setInputValue(textarea, text);
  click(view.container.querySelector('button[title="发送 (Enter)"]')!);
  await flush();
}

describe("AiSidebar 多标签", () => {
  let view: MountedView | null = null;

  beforeEach(() => {
    convSeq = 0;
    mocks.channels.length = 0;
    mocks.chat.mockImplementation(() => {
      convSeq += 1;
      return Promise.resolve({ jobId: `job-${convSeq}`, conversationId: `conv-${convSeq}` });
    });
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
      aiBoards: {},
      sessions: [],
      activeWorkspaceId: null,
    });
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
  });

  afterEach(() => {
    view?.unmount();
    view = null;
  });

  it("新建、切换、关闭标签, 每个标签的会话上下文互不串台", async () => {
    await flush();
    expect(stripTabs(view!)).toHaveLength(1);

    click(view!.container.querySelector('button[title="新建 AI 标签"]')!);
    await flush();
    expect(stripTabs(view!)).toHaveLength(2);

    await send(view!, "问题甲");
    expect(textOf(view!)).toContain("问题甲");

    click(stripTabs(view!)[0]);
    await flush();
    expect(textOf(view!)).not.toContain("问题甲");
    expect(textOf(view!)).toContain("新建会话");

    await send(view!, "问题乙");
    expect(textOf(view!)).toContain("问题乙");

    click(stripTabs(view!)[1]);
    await flush();
    expect(textOf(view!)).toContain("问题甲");
    expect(textOf(view!)).not.toContain("问题乙");

    emit({ type: "done", answer: "回答甲" }, 0);
    await flush();
    click(stripTabs(view!)[1].querySelector("button")!);
    await flush();
    expect(stripTabs(view!)).toHaveLength(1);
    expect(textOf(view!)).toContain("问题乙");
    expect(textOf(view!)).not.toContain("问题甲");
  });

  it("两个标签可以并行运行: 一个标签忙不阻塞另一个标签发消息", async () => {
    await flush();
    await send(view!, "并行一");
    expect(useUi.getState().aiBusy).toBe(true);

    click(view!.container.querySelector('button[title="新建 AI 标签"]')!);
    await flush();
    await send(view!, "并行二");
    expect(mocks.chat).toHaveBeenCalledTimes(2);
    expect(useUi.getState().aiBusy).toBe(true);

    emit({ type: "done", answer: "回答一" }, 0);
    await flush();
    expect(useUi.getState().aiBusy).toBe(true);

    emit({ type: "done", answer: "回答二" }, 1);
    await flush();
    expect(useUi.getState().aiBusy).toBe(false);

    click(stripTabs(view!)[0]);
    await flush();
    expect(textOf(view!)).toContain("回答一");
    expect(textOf(view!)).not.toContain("回答二");
  });

  it("运行中的标签关闭需确认：拒绝则保留，确认则停止运行并关闭", async () => {
    await flush();
    await send(view!, "长跑任务");
    mocks.ask.mockResolvedValue(false);
    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("关闭将停止它"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.cancel).not.toHaveBeenCalled();
    expect(stripTabs(view!)).toHaveLength(1);
    expect(useUi.getState().aiBusy).toBe(true);

    mocks.ask.mockResolvedValue(true);
    const before = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id;
    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();
    expect(mocks.cancel).toHaveBeenCalledWith("job-1");
    const board = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY];
    expect(board.tabs).toHaveLength(1);
    expect(board.tabs[0].id).not.toBe(before);
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("旧版遗留的会话键一次性清除且不再被任何看板继承", async () => {
    localStorage.setItem("nexterm.ai.conversation.v1", "conv-legacy");
    mocks.conversationList.mockResolvedValue([{ id: "conv-legacy", title: "旧会话", updatedAt: 0 }]);
    view?.unmount();
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();

    expect(stripTabs(view!)).toHaveLength(1);
    expect(mocks.messages).not.toHaveBeenCalledWith("conv-legacy");
    expect(localStorage.getItem("nexterm.ai.conversation.v1")).toBeNull();
    expect(textOf(view!)).toContain("新建会话");
  });
});
