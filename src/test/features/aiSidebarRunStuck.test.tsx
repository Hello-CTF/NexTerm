/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import {
  click,
  clickButton,
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
  steer: vi.fn(),
  hitlSnapshot: vi.fn(),
  hitlEvents: vi.fn(),
  runs: vi.fn(),
  runEvents: vi.fn(),
  getPermission: vi.fn(),
  setPermission: vi.fn(),
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
    steer: mocks.steer,
    hitlSnapshot: mocks.hitlSnapshot,
    hitlEvents: mocks.hitlEvents,
    runs: mocks.runs,
    runEvents: mocks.runEvents,
    getPermission: mocks.getPermission,
    setPermission: mocks.setPermission,
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

let rafQueue: { cb: FrameRequestCallback; cancelled: boolean }[] = [];

function runFrames() {
  const queue = rafQueue;
  rafQueue = [];
  for (const entry of queue) {
    if (!entry.cancelled) entry.cb(0);
  }
}

function emit(ev: Record<string, unknown>, channelIndex = -1) {
  const channel = mocks.channels.at(channelIndex);
  if (!channel) throw new Error("no fake channel");
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

async function flushReplay() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

describe("AiSidebar 忙状态滞留修复", () => {
  let view: MountedView | null = null;

  beforeEach(() => {
    vi.clearAllMocks();
    rafQueue = [];
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      rafQueue.push({ cb, cancelled: false });
      return rafQueue.length;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => {
      const entry = rafQueue[id - 1];
      if (entry) entry.cancelled = true;
    });
    mocks.channels.length = 0;
    mocks.chat.mockResolvedValue({ jobId: "job-1", conversationId: "conv-1" });
    mocks.cancel.mockResolvedValue(undefined);
    mocks.confirm.mockResolvedValue(undefined);
    mocks.answer.mockResolvedValue(undefined);
    mocks.steer.mockResolvedValue(undefined);
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
    mocks.conversationList.mockResolvedValue([]);
    mocks.conversationDelete.mockResolvedValue(undefined);
    mocks.messages.mockResolvedValue([]);
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
    vi.useRealTimers();
    view?.unmount();
    view = null;
  });

  it("补齐永久失败后忙状态落地：标签可直接关闭", async () => {
    await flush();
    await send(view!, "你好");
    emit({ type: "delta", text: "一", seq: 1 });
    act(runFrames);
    mocks.runEvents.mockRejectedValue(new Error("offline"));

    emit({ type: "delta", text: "三", seq: 3 });
    await flushReplay();
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 1100));
    });
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 2100));
    });
    await flushReplay();

    expect(textOf(view!)).toContain("补齐失败");
    expect(useUi.getState().aiBusy).toBe(false);

    const stuckTabId = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id;
    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).not.toBe(stuckTabId);
  });

  it("补齐永久失败落地后，手动重新补齐仍能找回结局", async () => {
    await flush();
    await send(view!, "你好");
    emit({ type: "delta", text: "一", seq: 1 });
    act(runFrames);
    mocks.runEvents.mockRejectedValue(new Error("offline"));

    emit({ type: "delta", text: "三", seq: 3 });
    await flushReplay();
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 1100));
    });
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 2100));
    });
    await flushReplay();
    expect(textOf(view!)).toContain("补齐失败");
    expect(useUi.getState().aiBusy).toBe(false);

    mocks.runEvents.mockResolvedValue([
      { type: "delta", text: "二", seq: 2 },
      { type: "delta", text: "三", seq: 3 },
      { type: "done", answer: "完整答案", seq: 4 },
    ]);
    clickButton(view!.container, "重新补齐");
    await flushReplay();

    expect(textOf(view!)).toContain("本轮已完成");
    expect(textOf(view!)).toContain("完整答案");
    expect(textOf(view!)).not.toContain("补齐失败");
  });

  it("恢复的待答运行看门狗耗尽后忙状态落地，标签可关闭", async () => {
    vi.useFakeTimers();
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "旧会话", updatedAt: 0 }]);
    mocks.messages.mockResolvedValue([]);
    mocks.runs.mockResolvedValue([
      {
        id: "job-old",
        conversationId: "conv-1",
        status: "interrupted",
        attempt: 1,
        seq: 0,
        planMode: false,
        source: "chat",
        answer: "",
        turns: 1,
        tokensIn: 0,
        tokensOut: 0,
        createdAt: 1000,
        updatedAt: 1000,
        finishedAt: null,
      },
    ]);
    mocks.runEvents.mockResolvedValue([]);
    mocks.hitlSnapshot.mockResolvedValue({
      runId: "job-old",
      checkpointId: "job-old",
      status: "running",
      attempt: 1,
      seq: 0,
      pending: [
        {
          id: "req-1",
          kind: "question",
          question: "继续吗？",
          options: [],
          expiresAt: "2000-01-01T00:00:00Z",
        },
      ],
    });
    view?.unmount();
    useUi.setState({
      aiBoards: {
        [GLOBAL_AI_BOARD_KEY]: {
          tabs: [{ id: "tab-bound", title: "旧会话", conversationId: "conv-1" }],
          activeTabId: "tab-bound",
        },
      },
    });
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
    await flush();
    await flushReplay();

    expect(useUi.getState().aiBusy).toBe(true);

    for (let i = 0; i < 12; i++) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3000);
      });
      await flushReplay();
    }

    expect(useUi.getState().aiBusy).toBe(false);
    const restoredTabId = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id;
    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).not.toBe(restoredTabId);
  });

  it("发送消息后权限面板自动关闭", async () => {
    await flush();
    const shield = view!.container.querySelector<HTMLButtonElement>(
      'button[title^="AI 权限与模式"]',
    )!;
    click(shield);
    await flush();
    expect(textOf(view!)).toContain("AI 权限");

    const readonlyOption = [...view!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("只读"),
    );
    expect(readonlyOption).toBeTruthy();
    click(readonlyOption!);
    await flush();
    expect(mocks.setPermission).toHaveBeenCalledWith(
      expect.objectContaining({ mode: "read_only" }),
      { sessionId: "s1", tabId: "t1" },
    );
    expect(textOf(view!)).toContain("AI 权限");

    await send(view!, "问题");
    expect(textOf(view!)).not.toContain("AI 权限");
    expect(mocks.chat).toHaveBeenCalledTimes(1);
  });

  it("权限面板按当前设备设置模式，可清除设备覆盖跟随全局默认", async () => {
    await flush();
    mocks.getPermission.mockImplementation((scope?: unknown) =>
      Promise.resolve(
        scope ? { mode: "silent", dangerRules: [] } : { mode: "read_write", dangerRules: [] },
      ),
    );
    const shield = view!.container.querySelector<HTMLButtonElement>(
      'button[title^="AI 权限与模式"]',
    )!;
    click(shield);
    await flush();

    expect(textOf(view!)).toContain("模式只对当前设备生效");
    expect(textOf(view!)).toContain("○ 跟随全局默认");

    const readonlyOption = [...view!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("只读"),
    )!;
    click(readonlyOption);
    await flush();
    expect(mocks.setPermission).toHaveBeenCalledWith(
      expect.objectContaining({ mode: "read_only" }),
      { sessionId: "s1", tabId: "t1" },
    );

    mocks.setPermission.mockClear();
    const follow = [...view!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("跟随全局默认"),
    )!;
    click(follow);
    await flush();
    expect(mocks.setPermission).toHaveBeenCalledWith(
      expect.anything(),
      { sessionId: "s1", tabId: "t1" },
      true,
    );
  });
});
