/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import {
  click,
  clickButton,
  deferred,
  flush,
  flushUntil,
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
  run: vi.fn(),
  runEvents: vi.fn(),
  getPermission: vi.fn(),
  permissionState: vi.fn(),
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
    run: mocks.run,
    runEvents: mocks.runEvents,
    getPermission: mocks.getPermission,
    permissionState: mocks.permissionState,
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

import {
  AI_RESYNC_BACKOFF_MS,
  AI_RESYNC_MAX_ATTEMPTS,
  AiSidebar,
} from "../../features/ai/AiSidebar";
import { aiTabBusyIds, aiTabRunControls, clearAiTabRuns } from "../../features/ai/runOwnership";
import { GLOBAL_AI_BOARD_KEY, useUi } from "../../app/store";
import { installMatchMediaStub } from "../setup";

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

function permissionStateOf(mode: string, hostMode?: string) {
  return {
    config: { mode: hostMode ?? mode, dangerRules: [] },
    globalMode: mode,
    hostMode,
    hostResolved: true,
    source: hostMode ? "host" : "global",
  };
}

function stripTabs(view: MountedView): HTMLElement[] {
  return [
    ...view.container.querySelectorAll<HTMLElement>('[aria-label="会话标签页"] [role="tab"]'),
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

describe("AiSidebar stuck busy-state fixes", () => {
  let view: MountedView | null = null;

  beforeEach(async () => {
    vi.clearAllMocks();
    clearAiTabRuns([...aiTabBusyIds, ...aiTabRunControls.keys()]);
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
    mocks.run.mockResolvedValue({ id: "job-1", conversationId: "conv-1", status: "running", finishedAt: null });
    mocks.runEvents.mockResolvedValue([]);
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.permissionState.mockResolvedValue(permissionStateOf("read_write"));
    mocks.setPermission.mockImplementation((config: { mode: string }) => Promise.resolve(permissionStateOf(config.mode)));
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
    await flush();
  });

  afterEach(() => {
    view?.unmount();
    view = null;
    vi.useRealTimers();
  });

  it("recovers the final outcome when the run completed but its terminal event never reached the channel", async () => {
    vi.useFakeTimers();
    await flush();
    await send(view!, "Hello");
    expect(useUi.getState().aiBusy).toBe(true);
    mocks.run.mockResolvedValue({
      id: "job-1", conversationId: "conv-1", status: "completed", source: "chat",
      answer: "已完成的回答", turns: 1, tokensIn: 2, tokensOut: 3,
      cacheCreationTokens: 0, latencyMs: 100, finishedAt: 2, createdAt: 1, updatedAt: 2,
    });

    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    await flushReplay();

    expect(mocks.run).toHaveBeenCalledWith("job-1");
    expect(textOf(view!)).toContain("已完成的回答");
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("confirms a cancellation from persisted run status after its terminal event is lost", async () => {
    vi.useFakeTimers();
    await flush();
    await send(view!, "Long-running task");
    click(view!.container.querySelector('button[title^="停止当前任务"]')!);
    await flush();

    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    await flushReplay();
    expect(useUi.getState().aiBusy).toBe(true);
    expect(textOf(view!)).toContain("停止未确认");

    mocks.run.mockResolvedValue({ id: "job-1", conversationId: "conv-1", status: "canceled", finishedAt: 2 });
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    await flushReplay();

    expect(mocks.run).toHaveBeenCalledWith("job-1");
    expect(textOf(view!)).toContain("已停止");
    expect(textOf(view!)).not.toContain("停止未确认");
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("keeps the run active after a permanent replay failure and applies later events and the final answer", async () => {
    vi.useFakeTimers();
    await flush();
    await send(view!, "Hello");
    emit({ type: "delta", text: "one", seq: 1 });
    act(runFrames);
    mocks.runEvents.mockRejectedValue(new Error("offline"));

    emit({ type: "delta", text: "three", seq: 3 });
    await flushReplay();
    for (let attempt = 1; attempt <= AI_RESYNC_MAX_ATTEMPTS; attempt += 1) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(AI_RESYNC_BACKOFF_MS * attempt);
      });
      await flushReplay();
    }

    expect(textOf(view!)).toContain("补齐失败");
    expect(textOf(view!)).toContain("重新补齐");
    expect(useUi.getState().aiBusy).toBe(true);

    // The terminal poll catches up the missing event even while live events arrive.
    mocks.runEvents.mockResolvedValue([{ type: "delta", text: "two", seq: 2 }]);
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    await flushReplay();
    act(runFrames);
    expect(textOf(view!)).toContain("onetwothree");
    expect(textOf(view!)).not.toContain("补齐失败");

    emit({ type: "delta", text: " four", seq: 4 });
    act(runFrames);
    emit({ type: "done", answer: "完整答案", seq: 5 });
    await flushReplay();
    expect(textOf(view!)).toContain("完整答案");
    expect(textOf(view!)).toContain("回答已完成");
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("manually replaying after a permanent failure still recovers the final outcome", async () => {
    vi.useFakeTimers();
    await flush();
    await send(view!, "Hello");
    emit({ type: "delta", text: "one", seq: 1 });
    act(runFrames);
    mocks.runEvents.mockRejectedValue(new Error("offline"));

    emit({ type: "delta", text: "three", seq: 3 });
    await flushReplay();
    for (let attempt = 1; attempt <= AI_RESYNC_MAX_ATTEMPTS; attempt += 1) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(AI_RESYNC_BACKOFF_MS * attempt);
      });
      await flushReplay();
    }
    expect(textOf(view!)).toContain("补齐失败");
    expect(useUi.getState().aiBusy).toBe(true);

    mocks.runEvents.mockResolvedValue([
      { type: "delta", text: "two", seq: 2 },
      { type: "delta", text: "three", seq: 3 },
    ]);
    clickButton(view!.container, "重新补齐");
    await flushReplay();
    expect(textOf(view!)).not.toContain("补齐失败");

    // Events after the manual replay still apply on the live channel.
    emit({ type: "done", answer: "完整答案", seq: 4 });
    await flushReplay();
    expect(textOf(view!)).toContain("回答已完成");
    expect(textOf(view!)).toContain("完整答案");
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("leaves a restored pending-answer run to the terminal poll after its expiry watch runs out", async () => {
    vi.useFakeTimers();
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "Old conversation", updatedAt: 0 }]);
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
          question: "Continue?",
          options: [],
          expiresAt: "2000-01-01T00:00:00Z",
        },
      ],
    });
    view?.unmount();
    useUi.setState({
      aiBoards: {
        [GLOBAL_AI_BOARD_KEY]: {
          tabs: [{ id: "tab-bound", title: "Old conversation", conversationId: "conv-1" }],
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

    // No terminal event and no terminal run row: the run is not settled locally.
    expect(useUi.getState().aiBusy).toBe(true);

    mocks.run.mockResolvedValue({ id: "job-old", conversationId: "conv-1", status: "expired", error: "等待回答已过期", finishedAt: 2 });
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    await flushReplay();
    expect(textOf(view!)).toContain("等待回答已过期");
    expect(useUi.getState().aiBusy).toBe(false);
    const restoredTabId = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id;
    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).not.toBe(restoredTabId);
  });

  it("closes the permission panel automatically after sending a message", async () => {
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
      undefined,
    );
    expect(textOf(view!)).toContain("AI 权限");

    await send(view!, "Question");
    expect(textOf(view!)).not.toContain("AI 权限");
    expect(mocks.chat).toHaveBeenCalledTimes(1);
  });

  it("edits an explicit host override in place and can make the host follow the global mode", async () => {
    let stored = permissionStateOf("read_write", "silent");
    mocks.permissionState.mockImplementation(() => Promise.resolve(stored));
    mocks.setPermission.mockImplementation((config: { mode: string }, _scope: unknown, options?: { hostOnly?: boolean; followGlobal?: boolean }) => {
      stored = options?.followGlobal ? permissionStateOf("read_write") : permissionStateOf("read_write", config.mode);
      return Promise.resolve(stored);
    });
    await flush();
    const shield = view!.container.querySelector<HTMLButtonElement>(
      'button[title^="AI 权限与模式"]',
    )!;
    // Opening the panel reloads the permission, which now reports the host's own mode.
    click(shield);
    await flush();
    expect(shield.title).toContain("自动执行（这台主机单独设置）");

    expect(textOf(view!)).toContain("这台主机的模式");
    const override = view!.container.querySelector<HTMLButtonElement>('button[role="switch"][aria-checked="true"]')!;
    expect(override.textContent).toContain("这台主机单独设置");
    expect(override.textContent).toContain("全局模式为「读写」");

    const readonlyOption = view!.container.querySelector<HTMLButtonElement>('[role="radiogroup"] [role="radio"]')!;
    expect(readonlyOption.textContent).toBe("只读");
    click(readonlyOption);
    await flush();
    expect(mocks.setPermission).toHaveBeenCalledWith(
      expect.objectContaining({ mode: "read_only" }),
      { sessionId: "s1", tabId: "t1" },
      { hostOnly: true },
    );

    mocks.setPermission.mockClear();
    click(view!.container.querySelector<HTMLButtonElement>('button[role="switch"][aria-checked="true"]')!);
    await flush();
    expect(mocks.setPermission).toHaveBeenCalledWith(
      expect.anything(),
      { sessionId: "s1", tabId: "t1" },
      { followGlobal: true },
    );
    expect(view!.container.querySelector('[role="radio"][aria-checked="true"]')?.textContent).toBe("读写");
    expect(textOf(view!)).toContain("全局模式");
  });

  it("keeps a tab open with an unconfirmed state when the cancel request times out", async () => {
    vi.useFakeTimers();
    await flush();
    await send(view!, "Long-running task");
    mocks.cancel.mockImplementationOnce(() => new Promise<void>(() => undefined));
    const before = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id;

    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });

    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).toBe(before);
    expect(useUi.getState().aiBusy).toBe(true);
    expect(textOf(view!)).toContain("停止未确认");
    expect(textOf(view!)).toContain("重试");
    expect(textOf(view!)).toContain("查看任务");

    clickButton(view!.container, "重试");
    await flush();
    expect(mocks.cancel).toHaveBeenCalledTimes(2);
    emit({ type: "canceled", message: "已停止" });
    await flush();
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("keeps the tab unconfirmed when the terminal query is offline", async () => {
    vi.useFakeTimers();
    await flush();
    await send(view!, "Long-running task");
    mocks.runs.mockRejectedValue(new Error("offline"));
    const before = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id;

    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });

    expect(mocks.cancel).toHaveBeenCalledWith("job-1");
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).toBe(before);
    expect(useUi.getState().aiBusy).toBe(true);
    expect(textOf(view!)).toContain("停止未确认");
  });

  it("closes after a run query confirms the terminal state without waiting for an event", async () => {
    await flush();
    await send(view!, "Long-running task");
    const before = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id;
    mocks.runs.mockResolvedValue([
      {
        id: "job-1",
        conversationId: "conv-1",
        status: "canceled",
        finishedAt: 2000,
      },
    ]);

    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();

    expect(mocks.cancel).toHaveBeenCalledWith("job-1");
    expect(mocks.runs).toHaveBeenCalledWith("conv-1");
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).not.toBe(before);
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("uses the same terminal confirmation path when closing a restored pending run", async () => {
    mocks.conversationList.mockResolvedValue([{ id: "conv-old", title: "Old conversation", updatedAt: 0 }]);
    mocks.messages.mockResolvedValue([]);
    mocks.runs.mockResolvedValue([
      {
        id: "job-old",
        conversationId: "conv-old",
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
          question: "Continue?",
          options: [],
          expiresAt: "2099-01-01T00:00:00Z",
        },
      ],
    });
    view?.unmount();
    useUi.setState({
      aiBoards: {
        [GLOBAL_AI_BOARD_KEY]: {
          tabs: [{ id: "tab-bound", title: "Old conversation", conversationId: "conv-old" }],
          activeTabId: "tab-bound",
        },
      },
    });
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
    await flushReplay();
    expect(useUi.getState().aiBusy).toBe(true);

    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();
    expect(mocks.cancel).toHaveBeenCalledWith("job-old");
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).toBe("tab-bound");

    emit({ type: "canceled", message: "已停止" });
    await flush();
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).not.toBe("tab-bound");
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("keeps steering on the original job after the workspace scope changes", async () => {
    await flush();
    await send(view!, "Original target");
    expect(mocks.chat.mock.calls[0][0].scope).toEqual({ sessionId: "s1", tabId: "t1" });

    act(() => {
      view!.root.render(createElement(AiSidebar, { sessionId: "s2", tabId: "t2" }));
    });
    const textarea = view!.container.querySelector("textarea")!;
    setInputValue(textarea, "Continue original task");
    act(() => {
      textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    await flush();

    expect(mocks.steer).toHaveBeenCalledWith("job-1", "Continue original task");
    expect(mocks.chat).toHaveBeenCalledTimes(1);
  });

  it("shows a canceled outcome only after the terminal event and then allows a new run", async () => {
    await flush();
    await send(view!, "Long task");

    click(view!.container.querySelector('button[title^="停止当前任务"]')!);
    await flush();
    expect(mocks.cancel).toHaveBeenCalledWith("job-1");
    expect(textOf(view!)).toContain("正在停止");
    expect(textOf(view!)).not.toContain("已停止");
    expect(useUi.getState().aiBusy).toBe(true);

    emit({ type: "canceled", message: "已停止" });
    await flush();
    expect(textOf(view!)).toContain("已停止");
    expect(useUi.getState().aiBusy).toBe(false);

    await send(view!, "Next message");
    expect(mocks.chat).toHaveBeenCalledTimes(2);
  });

  it("replays the real terminal outcome when a stop is confirmed by the run query first", async () => {
    await flush();
    await send(view!, "Long task");
    const textarea = view!.container.querySelector("textarea")!;
    setInputValue(textarea, "Pending steer");
    act(() => {
      textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    await flush();
    expect(textOf(view!)).toContain("等待送达");

    const events = deferred<Record<string, unknown>[]>();
    mocks.runs.mockResolvedValue([
      {
        id: "job-1",
        conversationId: "conv-1",
        status: "canceled",
        finishedAt: 2000,
      },
    ]);
    mocks.runEvents.mockReturnValue(events.promise);
    click(view!.container.querySelector('button[title^="停止当前任务"]')!);
    await flushUntil(() => mocks.runEvents.mock.calls.length > 0);
    expect(mocks.runEvents).toHaveBeenCalledWith("job-1", 0, 200);

    events.resolve([{ type: "canceled", message: "已停止", seq: 1 }]);
    await flushReplay();
    expect(textOf(view!)).toContain("已停止");
    // A stopped run hands the instruction it never read back to the composer instead of sending it.
    expect(view!.container.querySelector("textarea")!.value).toBe("Pending steer");
    expect(textOf(view!)).not.toContain("未送达");
    expect(useUi.getState().aiBusy).toBe(false);

    emit({ type: "done", answer: "late answer must not replace canceled" });
    await flush();
    expect(textOf(view!)).not.toContain("late answer must not replace canceled");
    expect(textOf(view!)).toContain("已停止");

    await send(view!, "Next message");
    expect(mocks.chat).toHaveBeenCalledTimes(2);
  });

  it.each([
    {
      status: "canceled",
      event: { type: "canceled", message: "已停止", seq: 1 },
      expected: "已停止",
    },
    {
      status: "completed",
      event: { type: "done", answer: "真实答案", seq: 1 },
      expected: "真实答案",
    },
    {
      status: "failed",
      event: { type: "error", message: "真实错误", retryable: true, seq: 1 },
      expected: "真实错误",
    },
  ])("presents a delayed $status outcome after the close waiter times out", async ({ status, event, expected }) => {
    vi.useFakeTimers();
    await flush();
    await send(view!, "Long task");
    const cancelRequest = deferred<void>();
    mocks.cancel.mockReturnValueOnce(cancelRequest.promise);
    mocks.runs.mockResolvedValue([
      {
        id: "job-1",
        conversationId: "conv-1",
        status,
        answer: event.type === "done" ? event.answer : "",
        error: event.type === "error" ? event.message : undefined,
        finishedAt: 2000,
      },
    ]);
    mocks.runEvents.mockResolvedValue([event]);
    const before = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id;

    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).toBe(before);
    expect(textOf(view!)).toContain("停止未确认");

    await act(async () => { cancelRequest.resolve(); });
    await flushReplay();
    expect(mocks.runEvents).toHaveBeenCalledWith("job-1", 0, 200);
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).toBe(before);
    expect(textOf(view!)).toContain(expected);
    expect(textOf(view!)).not.toContain("停止未确认");
    expect(useUi.getState().aiBusy).toBe(false);
    expect(mocks.cancel).toHaveBeenCalledTimes(1);
  });

  it("keeps run ownership when switching boards before workspace close", async () => {
    view?.unmount();
    useUi.setState({ workspaces: [], activeWorkspaceId: null, aiBoards: {}, workspaceRuntime: {} });
    const workspaceA = useUi.getState().ensureWorkspace({ kind: "session", sessionId: "s1", title: "A" });
    const workspaceB = useUi.getState().ensureWorkspace({ kind: "session", sessionId: "s2", title: "B" });
    useUi.getState().setActiveWorkspace(workspaceA);
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
    await send(view!, "Run in A");
    expect(useUi.getState().aiBoards[workspaceA]).toBeDefined();

    act(() => useUi.getState().setActiveWorkspace(workspaceB));
    await flush();
    expect(mocks.cancel).not.toHaveBeenCalled();

    let closing!: Promise<boolean>;
    act(() => {
      closing = useUi.getState().closeWorkspace(workspaceA);
    });
    await flushUntil(() =>
      mocks.cancel.mock.calls.length > 0 ||
      !useUi.getState().workspaces.some((workspace) => workspace.id === workspaceA)
    );

    expect(mocks.cancel).toHaveBeenCalledWith("job-1");
    expect(useUi.getState().workspaces.some((workspace) => workspace.id === workspaceA)).toBe(true);
    mocks.runs.mockResolvedValue([
      { id: "job-1", conversationId: "conv-1", status: "canceled", finishedAt: 2000 },
    ]);
    await act(async () => {
      await closing;
    });
    expect(useUi.getState().workspaces.some((workspace) => workspace.id === workspaceA)).toBe(false);
    expect(useUi.getState().workspaces.some((workspace) => workspace.id === workspaceB)).toBe(true);
    expect(useUi.getState().aiBoards[workspaceA]).toBeUndefined();
    expect(useUi.getState().aiBoards[workspaceB]).toBeDefined();
  });

  it("does not let a pending outcome replay block a later tab close", async () => {
    await flush();
    await send(view!, "Long task");
    const events = deferred<Record<string, unknown>[]>();
    mocks.runs.mockResolvedValue([
      {
        id: "job-1",
        conversationId: "conv-1",
        status: "canceled",
        finishedAt: 2000,
      },
    ]);
    mocks.runEvents.mockReturnValue(events.promise);

    click(view!.container.querySelector('button[title^="停止当前任务"]')!);
    await flushUntil(() => mocks.runEvents.mock.calls.length > 0);
    const before = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id;
    click(stripTabs(view!)[0].querySelector("button")!);
    await flushUntil(() => useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id !== before);

    expect(mocks.cancel).toHaveBeenCalledTimes(1);
    expect(useUi.getState().aiBusy).toBe(false);
    events.resolve([{ type: "canceled", message: "已停止", seq: 1 }]);
    await flushReplay();
  });

  it.each(["host", "tools"] as const)("confirms a detached run through the backend after a %s board round trip", async (startKind) => {
    view?.unmount();
    useUi.setState({ workspaces: [], activeWorkspaceId: null, aiBoards: {}, workspaceRuntime: {} });
    const first = useUi.getState().ensureWorkspace(
      startKind === "tools"
        ? { kind: "tools", title: "Tools" }
        : { kind: "session", sessionId: "s1", title: "A" },
    );
    const second = useUi.getState().ensureWorkspace({ kind: "session", sessionId: "s2", title: "B" });
    useUi.getState().setActiveWorkspace(first);
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
    await send(view!, "Run across boards");
    const boardKey = startKind === "tools" ? GLOBAL_AI_BOARD_KEY : first;
    const tabId = useUi.getState().aiBoards[boardKey].tabs[0].id;

    act(() => useUi.getState().setActiveWorkspace(second));
    await flush();
    act(() => useUi.getState().setActiveWorkspace(first));
    await flush();
    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();

    expect(mocks.cancel).toHaveBeenCalledWith("job-1");
    expect(useUi.getState().aiBoards[boardKey].tabs.some((tab) => tab.id === tabId)).toBe(true);
    emit({ type: "canceled", message: "已停止" }, 0);
    await flush();
    expect(useUi.getState().aiBoards[boardKey].tabs.some((tab) => tab.id === tabId)).toBe(true);
    mocks.runs.mockResolvedValue([
      { id: "job-1", conversationId: "conv-1", status: "canceled", finishedAt: 2000 },
    ]);
    await flushUntil(
      () => !useUi.getState().aiBoards[boardKey].tabs.some((tab) => tab.id === tabId),
      300,
    );
  });

  it("does not settle or clear busy when replay permanently fails during cancellation", async () => {
    vi.useFakeTimers();
    await flush();
    await send(view!, "Long task");
    emit({ type: "delta", text: "one", seq: 1 }, 0);
    act(runFrames);
    mocks.runEvents.mockRejectedValue(new Error("offline"));
    click(view!.container.querySelector('button[title^="停止当前任务"]')!);
    await flush();

    emit({ type: "delta", text: "three", seq: 3 }, 0);
    await flushReplay();
    for (let attempt = 1; attempt <= AI_RESYNC_MAX_ATTEMPTS; attempt += 1) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(AI_RESYNC_BACKOFF_MS * attempt);
      });
      await flushReplay();
    }

    expect(textOf(view!)).toContain("补齐失败");
    expect(useUi.getState().aiBusy).toBe(true);
    emit({ type: "canceled", message: "已停止" }, 0);
    await flush();
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("does not let the restored run watchdog settle a pending cancellation", async () => {
    vi.useFakeTimers();
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "Old conversation", updatedAt: 0 }]);
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
          question: "Continue?",
          options: [],
          expiresAt: "2000-01-01T00:00:00Z",
        },
      ],
    });
    view?.unmount();
    useUi.setState({
      aiBoards: {
        [GLOBAL_AI_BOARD_KEY]: {
          tabs: [{ id: "tab-bound", title: "Old conversation", conversationId: "conv-1" }],
          activeTabId: "tab-bound",
        },
      },
    });
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
    await flushReplay();
    expect(useUi.getState().aiBusy).toBe(true);

    click(view!.container.querySelector('button[title^="停止当前任务"]')!);
    await flush();
    for (let i = 0; i < 12; i++) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3000);
      });
      await flushReplay();
    }

    expect(useUi.getState().aiBusy).toBe(true);
    emit({ type: "canceled", message: "已停止" }, 0);
    await flush();
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("queries the run after cancel rejects not_found and closes when terminal is confirmed", async () => {
    await flush();
    await send(view!, "Long task");
    mocks.cancel.mockRejectedValueOnce(new Error("not_found"));
    mocks.runs.mockResolvedValue([
      {
        id: "job-1",
        conversationId: "conv-1",
        status: "canceled",
        finishedAt: 2000,
      },
    ]);
    const before = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id;

    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();

    expect(mocks.runs).toHaveBeenCalledWith("conv-1");
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).not.toBe(before);
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("keeps cancellation unconfirmed when the query after cancel rejection fails", async () => {
    await flush();
    await send(view!, "Long task");
    mocks.cancel.mockRejectedValueOnce(new Error("not_found"));
    mocks.runs.mockRejectedValue(new Error("offline"));
    const before = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id;

    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();

    expect(mocks.runs).toHaveBeenCalledWith("conv-1");
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).toBe(before);
    expect(useUi.getState().aiBusy).toBe(true);
    expect(textOf(view!)).toContain("停止未确认");
  });

  it("keeps cancellation unconfirmed when the query after cancel rejection is still running", async () => {
    await flush();
    await send(view!, "Long task");
    mocks.cancel.mockRejectedValueOnce(new Error("not_found"));
    mocks.runs.mockResolvedValue([
      {
        id: "job-1",
        conversationId: "conv-1",
        status: "running",
        finishedAt: null,
      },
    ]);
    const before = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id;

    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();

    expect(mocks.runs).toHaveBeenCalledWith("conv-1");
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).toBe(before);
    expect(useUi.getState().aiBusy).toBe(true);
    expect(textOf(view!)).toContain("停止未确认");
  });

  it("on touch screens Enter starts a new line, the send button sends, and a send button steers a running task", async () => {
    window.matchMedia = ((query: string) => ({
      matches: query === "(pointer: coarse)",
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    })) as typeof window.matchMedia;
    try {
      act(() => {
        view!.root.render(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
      });
      const textarea = () => view!.container.querySelector<HTMLTextAreaElement>('textarea[aria-label="消息输入"]')!;
      expect(textarea().getAttribute("enterkeyhint")).toBe("enter");
      setInputValue(textarea(), "first line");
      const enter = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
      act(() => {
        textarea().dispatchEvent(enter);
      });
      await flush();
      // The default action (a new line) is left to the soft keyboard and nothing is sent.
      expect(enter.defaultPrevented).toBe(false);
      expect(mocks.chat).not.toHaveBeenCalled();

      click(view!.container.querySelector<HTMLButtonElement>('button.nx-send-btn[aria-label="发送"]')!);
      await flush();
      expect(mocks.chat).toHaveBeenCalledTimes(1);
      expect(mocks.chat.mock.calls[0][0].message).toBe("first line");

      // While the task runs the stop button takes the send button's place; typed text gets its own send button.
      expect(view!.container.querySelector('button.nx-send-btn[aria-label="发送"]')).toBeNull();
      setInputValue(textarea(), "and check the logs");
      click(view!.container.querySelector<HTMLButtonElement>('button.nx-send-btn[aria-label="发送"]')!);
      await flush();
      expect(mocks.steer).toHaveBeenCalledWith("job-1", "and check the logs");
      expect(view!.container.querySelector('button.nx-send-btn-stop')).not.toBeNull();
    } finally {
      installMatchMediaStub();
    }
  });
});
