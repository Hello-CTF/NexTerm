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
  hitlSnapshot: vi.fn(),
  hitlEvents: vi.fn(),
  runs: vi.fn(),
  runEvents: vi.fn(),
  getPermission: vi.fn(),
  conversationList: vi.fn(),
  conversationDelete: vi.fn(),
  messages: vi.fn(),
  overview: vi.fn(),
  promptText: vi.fn(),
  ask: vi.fn(),
  takeoverEnter: vi.fn(),
  takeoverRun: vi.fn(),
  takeoverExit: vi.fn(),
  toast: vi.fn(),
  dispose: vi.fn(),
  channels: [] as { onEvent: (ev: Record<string, unknown>) => void }[],
  reopens: new Map<unknown, () => void>(),
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
    takeoverEnter: mocks.takeoverEnter,
    takeoverRun: mocks.takeoverRun,
    takeoverExit: mocks.takeoverExit,
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
  onChannelReopen: (channel: unknown, cb: () => void) => {
    mocks.reopens.set(channel, cb);
    return () => mocks.reopens.delete(channel);
  },
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, promptText: mocks.promptText }));

import { AiSidebar } from "../../features/ai/AiSidebar";
import { useUi } from "../../app/store";
import { createConversationStream } from "../../features/ai/conversationStream";
import type { ChatItem } from "../../features/ai/conversation";

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

function reconnect(channelIndex = -1) {
  const channel = mocks.channels.at(channelIndex);
  if (!channel) throw new Error("no fake channel");
  const cb = mocks.reopens.get(channel);
  if (!cb) throw new Error("no reopen callback registered for channel");
  act(() => cb());
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

function textOf(view: MountedView): string {
  return view.container.textContent ?? "";
}

function texts(items: ChatItem[], role: string): string[] {
  return items.filter((i) => i.role === role).map((i) => (i as { text?: string }).text ?? "");
}

describe("stream recovery: sequence cursor and reorder", () => {
  function stream() {
    const frames: (() => void)[] = [];
    const s = createConversationStream((cb) => {
      frames.push(cb);
      return () => {};
    });
    return {
      s,
      runFrame() {
        const cbs = [...frames];
        frames.length = 0;
        for (const cb of cbs) cb();
      },
    };
  }

  it("buffers an out-of-order event, reports the gap, and drains in order", () => {
    const { s, runFrame } = stream();
    s.beginRun(1);
    expect(s.pushEvent(1, { type: "delta", text: "一", seq: 1 }).accepted).toBe(true);
    const gapped = s.pushEvent(1, { type: "delta", text: "三", seq: 3 });
    expect(gapped.accepted).toBe(false);
    expect(gapped.gap).toEqual({ from: 2, to: 2 });
    expect(s.lastSeq(1)).toBe(1);
    runFrame();
    expect(texts(s.getState().items, "assistant")).toEqual(["一"]);

    const filled = s.pushEvent(1, { type: "delta", text: "二", seq: 2 });
    expect(filled.accepted).toBe(true);
    expect(s.lastSeq(1)).toBe(3);
    runFrame();
    expect(texts(s.getState().items, "assistant")).toEqual(["一二三"]);
  });

  it("rejects replays at or below the cursor without reapplying them", () => {
    const { s, runFrame } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "甲", seq: 1 });
    s.pushEvent(1, { type: "toolCall", id: "c1", name: "exec", display: "ls", seq: 2 });
    runFrame();
    expect(s.pushEvent(1, { type: "delta", text: "甲", seq: 1 }).accepted).toBe(false);
    expect(s.pushEvent(1, { type: "toolCall", id: "c1", name: "exec", display: "ls", seq: 2 }).accepted).toBe(false);
    expect(s.lastSeq(1)).toBe(2);
    expect(texts(s.getState().items, "assistant")).toEqual(["甲"]);
    expect(s.getState().items.filter((i) => i.role === "tool")).toHaveLength(1);
  });

  it("drains a buffered terminal event once the gap is filled", () => {
    const { s, runFrame } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "半", seq: 1 });
    runFrame();
    const buffered = s.pushEvent(1, { type: "done", answer: "完整答案", seq: 3 });
    expect(buffered.accepted).toBe(false);
    expect(buffered.terminal).toBeNull();
    expect(s.getState().attempts[0].outcome).toBeNull();
    const filled = s.pushEvent(1, { type: "delta", text: "截", seq: 2 });
    expect(filled.terminal).toBe("done");
    expect(s.getState().attempts[0].outcome).toBe("done");
    runFrame();
    expect(texts(s.getState().items, "assistant")).toEqual(["完整答案"]);
  });

  it("exposes the gap cursor so late binding can trigger catch-up", () => {
    const { s } = stream();
    s.beginRun(1);
    expect(s.hasGap(1)).toBe(false);
    s.pushEvent(1, { type: "delta", text: "一", seq: 1 });
    s.pushEvent(1, { type: "delta", text: "三", seq: 3 });
    expect(s.hasGap(1)).toBe(true);
    s.pushEvent(1, { type: "delta", text: "二", seq: 2 });
    expect(s.hasGap(1)).toBe(false);
    expect(s.lastSeq(1)).toBe(3);
  });

  it("a canceled event settles the attempt as canceled, closes cards and stops tool spinners", () => {
    const { s } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "toolCall", id: "c1", name: "exec", display: "ls", seq: 1 });
    s.pushEvent(1, {
      type: "confirmRequired",
      id: "c2",
      tool: "exec",
      rendered: "$ rm x",
      confirmationNonce: "n1",
      seq: 2,
    });
    const result = s.pushEvent(1, { type: "canceled", message: "已停止本轮", seq: 3 });
    expect(result.terminal).toBe("canceled");
    const state = s.getState();
    expect(state.attempts[0].outcome).toBe("canceled");
    const tool = state.items.find((i) => i.role === "tool");
    expect(tool).toMatchObject({ summary: "已停止" });
    const card = state.items.find((i) => i.role === "confirm");
    expect(card).toMatchObject({ resolution: "本轮已停止，交互已关闭" });
    expect(state.items.at(-1)).toMatchObject({ role: "outcome", outcome: "canceled", text: "已停止本轮" });
  });

  it("error and done outcomes settle still-running tools and record retryability", () => {
    const errored = stream();
    errored.s.beginRun(1);
    errored.s.pushEvent(1, { type: "toolCall", id: "c1", name: "exec", display: "ls", seq: 1 });
    errored.s.pushEvent(1, { type: "error", message: "boom", retryable: true, seq: 2 });
    const errorState = errored.s.getState();
    expect(errorState.items.find((i) => i.role === "tool")).toMatchObject({ summary: "本轮出错中断" });
    expect(errorState.items.at(-1)).toMatchObject({ outcome: "error", retryable: true });

    const nonRetryable = stream();
    nonRetryable.s.beginRun(1);
    nonRetryable.s.pushEvent(1, { type: "error", message: "expired", retryable: false, seq: 1 });
    expect(nonRetryable.s.getState().items.at(-1)).toMatchObject({ outcome: "error", retryable: false });

    const done = stream();
    done.s.beginRun(1);
    done.s.pushEvent(1, { type: "toolCall", id: "c1", name: "exec", display: "ls", seq: 1 });
    done.s.pushEvent(1, { type: "done", answer: "答案", seq: 2 });
    const doneState = done.s.getState();
    expect(doneState.items.find((i) => i.role === "tool")).toMatchObject({ summary: "未返回结果" });
    expect(doneState.attempts[0].outcome).toBe("done");
  });

  it("a rejected filler still drains a buffered terminal and settles exactly once", () => {
    const { s, runFrame } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "半", seq: 1 });
    runFrame();
    const buffered = s.pushEvent(1, { type: "done", answer: "答案", seq: 3 });
    expect(buffered.terminal).toBeNull();
    expect(s.getState().attempts[0].outcome).toBeNull();
    const filler = s.pushEvent(1, { type: "steered", text: "补充", seq: 2 });
    expect(filler.accepted).toBe(false);
    expect(filler.terminal).toBe("done");
    expect(s.getState().attempts[0].outcome).toBe("done");
    expect(s.hasGap(1)).toBe(false);
    const replayed = s.pushEvent(1, { type: "done", answer: "答案", seq: 3 });
    expect(replayed.accepted).toBe(false);
    expect(replayed.terminal).toBe("done");
    const outcomes = s.getState().items.filter((i) => i.role === "outcome");
    expect(outcomes).toHaveLength(1);
    expect(outcomes[0]).toMatchObject({ outcome: "done", text: "本轮已完成" });
  });

  it("rejected fillers drain buffered error and canceled terminals without duplicate outcomes", () => {
    const errored = stream();
    errored.s.beginRun(1);
    errored.s.pushEvent(1, { type: "toolCall", id: "c1", name: "exec", display: "ls", seq: 1 });
    errored.s.pushEvent(1, { type: "error", message: "boom", retryable: true, seq: 3 });
    const errorFiller = errored.s.pushEvent(1, { type: "steered", text: "补充", seq: 2 });
    expect(errorFiller.accepted).toBe(false);
    expect(errorFiller.terminal).toBe("error");
    const errorState = errored.s.getState();
    expect(errorState.attempts[0].outcome).toBe("error");
    expect(errorState.items.find((i) => i.role === "tool")).toMatchObject({ summary: "本轮出错中断" });
    expect(errorState.items.filter((i) => i.role === "outcome")).toHaveLength(1);

    const canceled = stream();
    canceled.s.beginRun(1);
    canceled.s.pushEvent(1, { type: "toolCall", id: "c1", name: "exec", display: "ls", seq: 1 });
    canceled.s.pushEvent(1, { type: "canceled", message: "已停止本轮", seq: 3 });
    const cancelFiller = canceled.s.pushEvent(1, { type: "steered", text: "补充", seq: 2 });
    expect(cancelFiller.accepted).toBe(false);
    expect(cancelFiller.terminal).toBe("canceled");
    const cancelState = canceled.s.getState();
    expect(cancelState.attempts[0].outcome).toBe("canceled");
    expect(cancelState.items.find((i) => i.role === "tool")).toMatchObject({ summary: "已停止" });
    expect(cancelState.items.filter((i) => i.role === "outcome")).toHaveLength(1);
  });

  it("cancelRun settles open tools so no spinner survives the stop", () => {
    const { s } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "toolCall", id: "c1", name: "exec", display: "ls", seq: 1 });
    s.cancelRun(1, true);
    const tool = s.getState().items.find((i) => i.role === "tool");
    expect(tool).toMatchObject({ summary: "已停止" });
  });
});

describe("AiSidebar stream recovery", () => {
  let view: MountedView | null = null;

  beforeEach(async () => {
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
    mocks.reopens.clear();
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
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    mocks.conversationList.mockResolvedValue([]);
    mocks.conversationDelete.mockResolvedValue(undefined);
    mocks.messages.mockResolvedValue([]);
    mocks.promptText.mockResolvedValue("安装 nginx");
    mocks.ask.mockResolvedValue(true);
    mocks.takeoverEnter.mockResolvedValue({ token: "tok-1" });
    mocks.takeoverRun.mockResolvedValue({ jobId: "job-t", token: "tok-2" });
    mocks.takeoverExit.mockResolvedValue(undefined);
    useUi.setState({
      rightOpen: true,
      aiBusy: false,
      takeover: null,
      pushToast: mocks.toast,
      workspaces: [],
      sessions: [],
    });
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
  });

  afterEach(() => {
    view?.unmount();
    view = null;
  });

  async function send(text: string) {
    const textarea = view!.container.querySelector("textarea");
    if (!textarea) throw new Error("input textarea not found");
    setInputValue(textarea, text);
    click(view!.container.querySelector('button[title="发送 (Enter)"]')!);
    await flush();
  }

  it("resumes a live run from the last delivered seq on reconnect and reports the resync", async () => {
    await send("你好");
    emit({ type: "delta", text: "流式", seq: 1 });
    act(runFrames);

    reconnect();
    expect(textOf(view!)).toContain("正在补齐");
    await flushReplay();

    expect(mocks.runEvents).toHaveBeenCalledWith("job-1", 1, 200);
    expect(mocks.hitlEvents).toHaveBeenCalledWith("job-1", 0);
    expect(textOf(view!)).toContain("输出无缺失");
    expect(textOf(view!)).toContain("流式");
  });

  it("catches up missed events when a live seq gap appears", async () => {
    await send("你好");
    emit({ type: "delta", text: "一", seq: 1 });
    act(runFrames);
    mocks.runEvents.mockResolvedValue([
      { type: "delta", text: "二", seq: 2 },
      { type: "delta", text: "三", seq: 3 },
    ]);

    emit({ type: "delta", text: "三", seq: 3 });
    await flushReplay();
    expect(mocks.runEvents).toHaveBeenCalledWith("job-1", 1, 200);
    act(runFrames);
    expect(textOf(view!)).toContain("一二三");
  });

  it("settles with a canceled outcome when the server reports user cancel", async () => {
    await send("你好");
    emit({ type: "delta", text: "半截", seq: 1 });
    emit({ type: "canceled", message: "已停止本轮", seq: 2 });
    await flush();

    expect(textOf(view!)).toContain("已停止本轮");
    expect(textOf(view!)).not.toContain("本轮出错");
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("retries a retryable failure with the original message in the same conversation", async () => {
    await send("重启服务");
    emit({ type: "error", message: "模型超时", retryable: true, seq: 1 });
    await flush();

    expect(textOf(view!)).toContain("本轮出错：模型超时");
    clickButton(view!.container, "重试");
    await flush();

    expect(mocks.chat).toHaveBeenCalledTimes(2);
    const secondCall = mocks.chat.mock.calls[1][0] as { message: string; conversationId: string };
    expect(secondCall.message).toBe("重启服务");
    expect(secondCall.conversationId).toBe("conv-1");
  });

  it("settles when a rejected filler drains the buffered terminal during replay", async () => {
    await send("你好");
    emit({ type: "delta", text: "半截", seq: 1 });
    act(runFrames);
    mocks.runEvents.mockResolvedValue([{ type: "steered", text: "补充", seq: 2 }]);

    emit({ type: "done", answer: "答案", seq: 3 });
    await flushReplay();

    expect(mocks.runEvents).toHaveBeenCalledWith("job-1", 1, 200);
    expect(useUi.getState().aiBusy).toBe(false);
    const text = textOf(view!);
    expect(text).toContain("本轮已完成");
    expect(text.split("本轮已完成").length - 1).toBe(1);
  });

  it("settles when a rejected filler arrives live after a buffered terminal", async () => {
    await send("你好");
    emit({ type: "delta", text: "半截", seq: 1 });
    act(runFrames);
    mocks.runEvents.mockResolvedValue([]);

    emit({ type: "done", answer: "答案", seq: 3 });
    await flushReplay();
    expect(useUi.getState().aiBusy).toBe(true);

    emit({ type: "steered", text: "补充", seq: 2 });
    await flush();
    expect(useUi.getState().aiBusy).toBe(false);
    expect(textOf(view!)).toContain("本轮已完成");
    expect(textOf(view!).split("本轮已完成").length - 1).toBe(1);
  });

  it("keeps an honest syncing state on replay failure and succeeds on retry", async () => {
    await send("你好");
    emit({ type: "delta", text: "一", seq: 1 });
    act(runFrames);
    mocks.runEvents
      .mockRejectedValueOnce(new Error("offline"))
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValue([{ type: "delta", text: "二", seq: 2 }]);

    emit({ type: "delta", text: "三", seq: 3 });
    await flushReplay();
    expect(textOf(view!)).toContain("正在补齐");
    expect(textOf(view!)).not.toContain("输出无缺失");

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 1100));
    });
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 2100));
    });
    await flushReplay();

    expect(textOf(view!)).toContain("已补齐");
    act(runFrames);
    expect(textOf(view!)).toContain("一二三");
    expect(useUi.getState().aiBusy).toBe(true);
  });

  it("shows a retryable failure state after repeated replay failures and recovers via the manual action", async () => {
    await send("你好");
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

    const failedText = textOf(view!);
    expect(failedText).toContain("补齐失败");
    expect(failedText).not.toContain("输出无缺失");
    expect(mocks.runEvents).toHaveBeenCalledTimes(3);

    mocks.runEvents.mockResolvedValue([{ type: "delta", text: "二", seq: 2 }]);
    clickButton(view!.container, "重新补齐");
    await flushReplay();
    expect(textOf(view!)).toContain("已补齐");
    act(runFrames);
    expect(textOf(view!)).toContain("一二三");
  });

  it("clears the syncing notice and pending retry when a live drain settles the run", async () => {
    await send("你好");
    emit({ type: "delta", text: "半", seq: 1 });
    act(runFrames);
    mocks.runEvents.mockRejectedValue(new Error("offline"));

    emit({ type: "done", answer: "答案", seq: 3 });
    await flushReplay();
    expect(textOf(view!)).toContain("正在补齐");

    emit({ type: "steered", text: "补充", seq: 2 });
    await flush();

    expect(useUi.getState().aiBusy).toBe(false);
    const settledText = textOf(view!);
    expect(settledText).toContain("本轮已完成");
    expect(settledText).not.toContain("正在补齐");

    const callsBefore = mocks.runEvents.mock.calls.length;
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 1100));
    });
    expect(mocks.runEvents.mock.calls.length).toBe(callsBefore);
    expect(textOf(view!)).not.toContain("正在补齐");
  });

  it("keeps a new run's pending retry when a stale run's late replay completes", async () => {
    mocks.chat
      .mockResolvedValueOnce({ jobId: "job-1", conversationId: "conv-1" })
      .mockResolvedValueOnce({ jobId: "job-2", conversationId: "conv-1" });
    let resolveRun1Replay: (value: { type: string; seq: number; answer?: string }[]) => void = () => {};
    mocks.runEvents
      .mockImplementationOnce(
        () =>
          new Promise<{ type: string; seq: number; answer?: string }[]>((resolve) => {
            resolveRun1Replay = resolve;
          }),
      )
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValue([{ type: "delta", text: "二", seq: 2 }]);

    await send("第一");
    emit({ type: "delta", text: "一", seq: 1 }, 0);
    act(runFrames);
    emit({ type: "done", answer: "一", seq: 3 }, 0);
    await flushReplay();
    emit({ type: "steered", text: "补充", seq: 2 }, 0);
    await flush();
    expect(useUi.getState().aiBusy).toBe(false);

    await send("第二");
    emit({ type: "delta", text: "一", seq: 1 }, 1);
    act(runFrames);
    emit({ type: "delta", text: "三", seq: 3 }, 1);
    await flushReplay();
    expect(textOf(view!)).toContain("正在补齐");

    resolveRun1Replay([{ type: "done", answer: "一", seq: 3 }]);
    await flushReplay();
    expect(textOf(view!)).toContain("正在补齐");

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 1100));
    });
    await flushReplay();
    expect(mocks.runEvents).toHaveBeenCalledTimes(3);
    expect(textOf(view!)).toContain("已补齐");
  });

  it("dismisses the new run's synced banner on schedule when a stale run's late replay succeeds", async () => {
    mocks.chat
      .mockResolvedValueOnce({ jobId: "job-1", conversationId: "conv-1" })
      .mockResolvedValueOnce({ jobId: "job-2", conversationId: "conv-1" });
    let resolveRun1Replay: (value: { type: string; seq: number; answer?: string }[]) => void = () => {};
    mocks.runEvents
      .mockImplementationOnce(
        () =>
          new Promise<{ type: string; seq: number; answer?: string }[]>((resolve) => {
            resolveRun1Replay = resolve;
          }),
      )
      .mockResolvedValueOnce([{ type: "delta", text: "二", seq: 2 }]);

    await send("第一");
    emit({ type: "delta", text: "一", seq: 1 }, 0);
    act(runFrames);
    emit({ type: "done", answer: "一", seq: 3 }, 0);
    await flushReplay();
    emit({ type: "steered", text: "补充", seq: 2 }, 0);
    await flush();
    expect(useUi.getState().aiBusy).toBe(false);

    await send("第二");
    emit({ type: "delta", text: "一", seq: 1 }, 1);
    act(runFrames);
    emit({ type: "delta", text: "三", seq: 3 }, 1);
    await flushReplay();
    expect(textOf(view!)).toContain("已补齐断线期间的 1 条事件");

    resolveRun1Replay([{ type: "done", answer: "一", seq: 3 }]);
    await flushReplay();

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 2600));
    });
    expect(textOf(view!)).not.toContain("已补齐");
  });

  it("replays an interrupted run's canceled terminal without error styling", async () => {
    mocks.conversationList.mockResolvedValue([{ id: "c-9", title: "旧会话", updatedAt: 0 }]);
    mocks.messages.mockResolvedValue([{ role: "user", content: "旧问题" }]);
    mocks.runs.mockResolvedValue([
      {
        id: "job-old", conversationId: "c-9", status: "canceled", attempt: 1, seq: 2,
        planMode: false, source: "chat", answer: "", turns: 1, tokensIn: 0, tokensOut: 0,
        createdAt: 1000, updatedAt: 1000, finishedAt: 1500,
      },
    ]);
    mocks.runEvents.mockResolvedValue([
      { type: "delta", text: "半截回答", seq: 1 },
      { type: "canceled", message: "已停止本轮", seq: 2 },
    ]);
    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();
    clickButton(view!.container, "旧会话");
    await flush();
    await flushReplay();

    expect(textOf(view!)).toContain("半截回答");
    expect(textOf(view!)).toContain("已停止本轮");
    expect(textOf(view!)).not.toContain("本轮出错");
    expect(useUi.getState().aiBusy).toBe(false);
  });
});
