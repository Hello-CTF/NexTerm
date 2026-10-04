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
  });
}

function hitlSnapshotOf(
  status: string,
  pending: Record<string, unknown>[],
  extra: Record<string, unknown> = {},
) {
  return {
    runId: "job-1",
    checkpointId: "job-1",
    status,
    attempt: 1,
    seq: 1,
    pending,
    ...extra,
  };
}

function hitlInterruptOf(overrides: Record<string, unknown> = {}) {
  return {
    id: "req-1",
    runId: "job-1",
    checkpointId: "job-1",
    checkpointHash: "hash-1",
    targetId: "target-1",
    callId: "call-1",
    tool: "exec_commands",
    kind: "confirm",
    parameters: { command: "rm -rf /tmp/x" },
    parameterHash: "phash-1",
    nonce: "nonce-1",
    createdAt: "2026-10-03T10:00:00Z",
    expiresAt: "2026-10-03T10:05:00Z",
    attempt: 1,
    seq: 1,
    ...overrides,
  };
}

function perRun(seq: number) {
  return { seq };
}

function textOf(view: MountedView): string {
  return view.container.textContent ?? "";
}

function stubScroller(el: HTMLDivElement) {
  const metrics = { scrollTop: 0, scrollHeight: 0, clientHeight: 0 };
  for (const key of ["scrollTop", "scrollHeight", "clientHeight"] as const) {
    Object.defineProperty(el, key, {
      configurable: true,
      get: () => metrics[key],
      set: (value: number) => {
        metrics[key] = value;
      },
    });
  }
  const scrollCalls: number[] = [];
  el.scrollTo = ((options?: ScrollToOptions) => {
    const top = Number(options?.top ?? 0);
    scrollCalls.push(top);
    metrics.scrollTop = top;
  }) as typeof el.scrollTo;
  return {
    el,
    metrics,
    scrollCalls,
    setMetrics(patch: Partial<typeof metrics>) {
      Object.assign(metrics, patch);
    },
    dispatchScroll() {
      act(() => {
        el.dispatchEvent(new Event("scroll"));
      });
    },
  };
}

describe("AiSidebar conversation stream UX", () => {
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

  it("streams deltas through the frame coalescer and settles exactly once on done", async () => {
    await send("你好");
    expect(textOf(view!)).toContain("你好");
    expect(mocks.chat).toHaveBeenCalledOnce();

    emit({ type: "delta", text: "流式" });
    emit({ type: "delta", text: "回答" });
    expect(textOf(view!)).not.toContain("流式回答");
    act(runFrames);
    expect(textOf(view!)).toContain("流式回答");

    emit({ type: "done", answer: "流式回答" });
    await flush();
    expect(textOf(view!)).toContain("本轮已完成");
    expect(view!.container.querySelector('button[title="发送 (Enter)"]')).not.toBeNull();

    const bubblesBefore = textOf(view!);
    emit({ type: "done", answer: "流式回答" });
    emit({ type: "error", message: "迟到错误" });
    await flush();
    expect(textOf(view!)).toBe(bubblesBefore);
    expect(textOf(view!)).not.toContain("迟到错误");
    expect(mocks.dispose).toHaveBeenCalled();
  });

  it("resolves confirmations with full identity and leaves a permanent record", async () => {
    await send("跑个命令");
    emit({
      type: "confirmRequired",
      id: "call-1",
      tool: "exec_commands",
      rendered: "$ rm -rf /tmp/x",
      confirmationNonce: "nonce-1",
    });
    expect(textOf(view!)).toContain("需要你确认");
    clickButton(view!.container, "允许一次");
    await flush();
    expect(mocks.confirm).toHaveBeenCalledWith({
      jobId: "job-1",
      callId: "call-1",
      nonce: "nonce-1",
      decision: "allow",
    });
    expect(textOf(view!)).not.toContain("需要你确认");
    expect(textOf(view!)).toContain("exec_commands · 已允许一次");

    emit({ type: "done", answer: "跑完了" });
    await flush();
    expect(textOf(view!)).toContain("exec_commands · 已允许一次");
    expect(textOf(view!)).not.toContain("需要你确认");
  });

  it("answers questions by option and by free text, cleaning up the card", async () => {
    await send("问吧");
    emit({
      type: "questionRequired",
      id: "q-1",
      question: { question: "继续吗？", options: ["继续", "停止"] },
      confirmationNonce: "nonce-q",
    });
    expect(textOf(view!)).toContain("AI 需要你回答");
    clickButton(view!.container, "继续");
    await flush();
    expect(mocks.answer).toHaveBeenCalledWith({
      jobId: "job-1",
      callId: "q-1",
      nonce: "nonce-q",
      text: "继续",
    });
    expect(textOf(view!)).not.toContain("AI 需要你回答");
    expect(textOf(view!)).toContain("提问 · 已回答：继续");

    emit({
      type: "questionRequired",
      id: "q-2",
      question: { question: "部署到哪？", options: [] },
      confirmationNonce: "nonce-q2",
    });
    const draft = view!.container.querySelector("form.nx-alert textarea");
    if (!draft) throw new Error("question textarea not found");
    setInputValue(draft as HTMLTextAreaElement, "生产环境");
    clickButton(view!.container, "回答");
    await flush();
    expect(mocks.answer).toHaveBeenCalledWith({
      jobId: "job-1",
      callId: "q-2",
      nonce: "nonce-q2",
      text: "生产环境",
    });
    expect(textOf(view!)).toContain("提问 · 已回答：生产环境");
  });

  it("shows a persistent alert on error and rejects a late done", async () => {
    await send("会失败的一轮");
    emit({ type: "delta", text: "失败前的输出" });
    act(runFrames);
    emit({ type: "error", message: "网络断开", retryable: true });
    await flush();
    const alert = view!.container.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain("网络断开");
    expect(textOf(view!)).toContain("失败前的输出");
    expect(mocks.toast).toHaveBeenCalledWith("error", "AI: 网络断开");
    expect(view!.container.querySelector('button[title="发送 (Enter)"]')).not.toBeNull();

    emit({ type: "done", answer: "迟到答案" });
    await flush();
    expect(textOf(view!)).not.toContain("迟到答案");
    expect(view!.container.querySelectorAll('[role="alert"]')).toHaveLength(1);
  });

  it("streams reasoning in an open details block that folds at run end", async () => {
    await send("想一想");
    emit({ type: "reasoning", text: "第一段推理" });
    emit({ type: "reasoning", text: "第二段" });
    act(runFrames);
    const reasoning = view!.container.querySelector("details");
    expect(reasoning?.open).toBe(true);
    expect(reasoning?.textContent).toContain("第一段推理第二段");
    emit({ type: "done", answer: "想好了" });
    await flush();
    const settled = view!.container.querySelector("details");
    expect(settled?.open).toBe(false);
    expect(settled?.textContent).toContain("第一段推理第二段");
  });

  it("stop records a canceled outcome and a late done cannot overwrite it", async () => {
    await send("长任务");
    emit({ type: "delta", text: "已流出的部分" });
    act(runFrames);
    click(view!.container.querySelector('button[title^="停止这一轮"]')!);
    await flush();
    expect(mocks.cancel).toHaveBeenCalledWith("job-1");
    expect(textOf(view!)).toContain("已停止本轮");
    expect(textOf(view!)).toContain("已流出的部分");

    emit({ type: "done", answer: "迟到的完整答案" });
    await flush();
    expect(textOf(view!)).not.toContain("迟到的完整答案");
    expect(textOf(view!)).toContain("已停止本轮");
    await send("下一句");
    expect(mocks.chat).toHaveBeenCalledTimes(2);
  });

  it("switches to history and back to a fresh conversation without stale bleed", async () => {
    await send("当前问题");
    emit({ type: "done", answer: "当前回答" });
    await flush();
    mocks.conversationList.mockResolvedValue([{ id: "c-9", title: "旧会话", updatedAt: 0 }]);
    mocks.messages.mockResolvedValue([
      { role: "user", content: "旧问题" },
      { role: "assistant", content: "旧回答" },
    ]);
    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();
    clickButton(view!.container, "旧会话");
    await flush();
    expect(mocks.messages).toHaveBeenCalledWith("c-9");
    expect(textOf(view!)).toContain("旧问题");
    expect(textOf(view!)).toContain("旧回答");
    expect(textOf(view!)).not.toContain("当前回答");

    emit({ type: "delta", text: "残影" });
    act(runFrames);
    expect(textOf(view!)).not.toContain("残影");

    click(view!.container.querySelector('button[title="新建会话"]')!);
    await flush();
    expect(textOf(view!)).not.toContain("旧问题");
    expect(textOf(view!)).toContain("命令与输出全程留痕");
  });

  it("deletes a history conversation only after explicit confirmation", async () => {
    mocks.conversationList.mockResolvedValue([
      { id: "c-1", title: "会话一", updatedAt: 0 },
      { id: "c-2", title: "会话二", updatedAt: 0 },
    ]);
    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();

    mocks.ask.mockResolvedValueOnce(false);
    click(view!.container.querySelector('button[title="删除「会话二」"]')!);
    await flush();
    expect(mocks.conversationDelete).not.toHaveBeenCalled();
    expect(textOf(view!)).toContain("会话二");

    click(view!.container.querySelector('button[title="删除「会话二」"]')!);
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(expect.stringContaining("会话二"), expect.anything());
    expect(mocks.conversationDelete).toHaveBeenCalledWith("c-2");
    expect(textOf(view!)).not.toContain("会话二");
    expect(textOf(view!)).toContain("会话一");
  });

  it("deleting the open conversation drops back to a fresh one", async () => {
    await send("当前问题");
    emit({ type: "done", answer: "当前回答" });
    await flush();
    mocks.conversationList.mockResolvedValue([{ id: "c-9", title: "旧会话", updatedAt: 0 }]);
    mocks.messages.mockResolvedValue([{ role: "user", content: "旧问题" }]);
    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();
    clickButton(view!.container, "旧会话");
    await flush();
    expect(textOf(view!)).toContain("旧问题");

    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();
    click(view!.container.querySelector('button[title="删除「旧会话」"]')!);
    await flush();
    expect(mocks.conversationDelete).toHaveBeenCalledWith("c-9");
    expect(textOf(view!)).not.toContain("旧问题");
    expect(textOf(view!)).toContain("命令与输出全程留痕");
    mocks.chat.mockResolvedValueOnce({ jobId: "job-2", conversationId: "conv-2" });
    await send("继续");
    expect(mocks.chat).toHaveBeenCalledTimes(2);
    expect(mocks.chat).toHaveBeenLastCalledWith(
      expect.objectContaining({ conversationId: undefined }),
    );
  });

  it("refuses to delete the current conversation while its run is active", async () => {
    await send("运行中的问题");
    await flush();
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "当前会话", updatedAt: 0 }]);
    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();

    click(view!.container.querySelector('button[title="删除「当前会话」"]')!);
    await flush();
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.conversationDelete).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("请先停止"));
    expect(textOf(view!)).toContain("当前会话");

    emit({ type: "done", answer: "运行中的回答" });
    await flush();
    expect(textOf(view!)).toContain("本轮已完成");
    expect(view!.container.querySelector('button[title="发送 (Enter)"]')).not.toBeNull();
  });

  it("re-checks the run guard after the confirm dialog resolves", async () => {
    await send("第一轮");
    emit({ type: "done", answer: "第一轮回答" });
    await flush();
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "当前会话", updatedAt: 0 }]);
    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();

    let resolveAsk: ((ok: boolean) => void) | undefined;
    mocks.ask.mockImplementationOnce(
      () => new Promise<boolean>((res) => { resolveAsk = res; }),
    );
    click(view!.container.querySelector('button[title="删除「当前会话」"]')!);
    await send("第二轮");
    resolveAsk?.(true);
    await flush();
    expect(mocks.conversationDelete).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("请先停止"));
    expect(textOf(view!)).toContain("当前会话");

    emit({ type: "done", answer: "第二轮回答" });
    await flush();
    expect(textOf(view!)).toContain("本轮已完成");
  });

  it("keeps the run settleable when it starts while the delete RPC is in flight", async () => {
    await send("第一轮");
    emit({ type: "done", answer: "第一轮回答" });
    await flush();
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "当前会话", updatedAt: 0 }]);
    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();

    let resolveDelete: (() => void) | undefined;
    mocks.conversationDelete.mockImplementationOnce(
      () => new Promise<void>((res) => { resolveDelete = res; }),
    );
    click(view!.container.querySelector('button[title="删除「当前会话」"]')!);
    await flush();
    expect(mocks.conversationDelete).toHaveBeenCalledWith("conv-1");
    await send("第二轮");

    resolveDelete?.();
    await flush();
    expect(textOf(view!)).not.toContain("当前会话");
    expect(textOf(view!)).toContain("第一轮回答");
    expect(textOf(view!)).not.toContain("命令与输出全程留痕");
    emit({ type: "delta", text: "第二轮输出" });
    act(runFrames);
    expect(textOf(view!)).toContain("第二轮输出");

    emit({ type: "done", answer: "第二轮回答" });
    await flush();
    expect(textOf(view!)).toContain("本轮已完成");
    expect(view!.container.querySelector('button[title="发送 (Enter)"]')).not.toBeNull();
    await send("第三轮");
    expect(mocks.chat).toHaveBeenLastCalledWith(
      expect.objectContaining({ conversationId: undefined }),
    );
  });

  it("does not resurrect a deleted id when the chat RPC resolves after the delete", async () => {
    await send("第一轮");
    emit({ type: "done", answer: "第一轮回答" });
    await flush();
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "当前会话", updatedAt: 0 }]);
    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();

    let resolveDelete: (() => void) | undefined;
    mocks.conversationDelete.mockImplementationOnce(
      () => new Promise<void>((res) => { resolveDelete = res; }),
    );
    let resolveChat: ((res: { jobId: string; conversationId: string }) => void) | undefined;
    mocks.chat.mockImplementationOnce(
      () => new Promise<{ jobId: string; conversationId: string }>((res) => { resolveChat = res; }),
    );
    click(view!.container.querySelector('button[title="删除「当前会话」"]')!);
    await flush();
    await send("第二轮");

    resolveDelete?.();
    await flush();
    expect(textOf(view!)).not.toContain("当前会话");
    expect(textOf(view!)).toContain("第一轮回答");

    resolveChat?.({ jobId: "job-2", conversationId: "conv-1" });
    await flush();
    emit({ type: "delta", text: "第二轮输出" });
    act(runFrames);
    expect(textOf(view!)).toContain("第二轮输出");
    emit({ type: "done", answer: "第二轮回答" });
    await flush();
    expect(textOf(view!)).toContain("本轮已完成");
    expect(view!.container.querySelector('button[title="发送 (Enter)"]')).not.toBeNull();
    await send("第三轮");
    expect(mocks.chat).toHaveBeenLastCalledWith(
      expect.objectContaining({ conversationId: undefined }),
    );
  });

  it("takeover streams narration and closes with a persistent outcome", async () => {
    click(view!.container.querySelector('button[title^="终端接管（实验性功能）：AI"]')!);
    await flush();
    await flush();
    expect(mocks.takeoverRun).toHaveBeenCalledOnce();
    expect(useUi.getState().takeover?.jobId).toBe("job-t");
    emit({ type: "screen", text: "nginx 安装日志" });
    emit({ type: "delta", text: "正在安装" });
    act(runFrames);
    expect(textOf(view!)).toContain("正在安装");
    emit({ type: "done", answer: "装好了" });
    await flush();
    expect(textOf(view!)).toContain("接管结束：装好了");
    expect(textOf(view!)).toContain("接管已完成");
    expect(useUi.getState().takeover).toBeNull();
  });

  it("follows output only at the bottom and offers a jump-back affordance", async () => {
    const sc = stubScroller(view!.container.querySelector('[role="log"]') as HTMLDivElement);

    await send("滚动测试");
    sc.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    emit({ type: "delta", text: "第一段" });
    act(runFrames);
    expect(sc.scrollCalls.length).toBeGreaterThan(0);

    sc.setMetrics({ scrollTop: 100 });
    sc.dispatchScroll();
    const callsBefore = sc.scrollCalls.length;
    sc.setMetrics({ scrollHeight: 1600 });
    emit({ type: "delta", text: "第二段" });
    act(runFrames);
    expect(sc.scrollCalls.length).toBe(callsBefore);
    expect(textOf(view!)).toContain("↓ 新输出");
    expect(sc.metrics.scrollTop).toBe(100);

    clickButton(view!.container, "↓ 新输出");
    expect(sc.scrollCalls.at(-1)).toBe(1600);
    expect(textOf(view!)).not.toContain("↓ 新输出");
    sc.setMetrics({ scrollHeight: 2000 });
    emit({ type: "done", answer: "第一段第二段" });
    await flush();
    expect(sc.scrollCalls.at(-1)).toBe(2000);
  });

  it("keeps honoring manual scroll-up after a sidebar close/reopen cycle", async () => {
    await send("滚动生命周期");
    const first = view!.container.querySelector('[role="log"]');
    act(() => useUi.setState({ rightOpen: false }));
    expect(view!.container.querySelector('[role="log"]')).toBeNull();
    act(() => useUi.setState({ rightOpen: true }));
    await flush();
    const sc = stubScroller(view!.container.querySelector('[role="log"]') as HTMLDivElement);
    expect(sc.el).not.toBe(first);

    sc.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    emit({ type: "delta", text: "重开后的输出" });
    act(runFrames);
    expect(sc.scrollCalls.length).toBeGreaterThan(0);

    sc.setMetrics({ scrollTop: 100 });
    sc.dispatchScroll();
    const callsBefore = sc.scrollCalls.length;
    sc.setMetrics({ scrollHeight: 1600 });
    emit({ type: "delta", text: "继续输出" });
    act(runFrames);
    expect(sc.scrollCalls.length).toBe(callsBefore);
    expect(sc.metrics.scrollTop).toBe(100);
    expect(textOf(view!)).toContain("↓ 新输出");
  });

  it("reconnect replay does not duplicate streamed text end to end", async () => {
    await send("重连测试");
    emit({ type: "delta", text: "abcdefghij", ...perRun(1) });
    act(runFrames);
    emit({ type: "delta", text: "abcdefghij", ...perRun(1) });
    emit({ type: "delta", text: " XYZ", ...perRun(2) });
    act(runFrames);
    expect(textOf(view!)).toContain("abcdefghij XYZ");
    expect(textOf(view!)).not.toContain("abcdefghijabcdefghij");
    emit({ type: "done", answer: "abcdefghij XYZ" });
    await flush();
    expect(textOf(view!).match(/abcdefghij XYZ/g)).toHaveLength(1);
    expect(textOf(view!)).toContain("本轮已完成");
  });

  it("keeps genuinely new identical reasoning after reconnect, end to end", async () => {
    await send("重复内容");
    emit({ type: "reasoning", text: "ha", ...perRun(1) });
    act(runFrames);
    emit({ type: "reasoning", text: "ha", ...perRun(2) });
    emit({ type: "reasoning", text: "!", ...perRun(3) });
    act(runFrames);
    emit({ type: "done", answer: "答案" });
    await flush();
    expect(textOf(view!)).toContain("haha!");
    expect(textOf(view!)).not.toContain("haha!haha!");
  });

  it("replays a pending HITL request on reconnect even when its live event was lost", async () => {
    await send("跑个命令");
    mocks.hitlEvents.mockResolvedValueOnce([
      {
        runId: "job-1",
        checkpointId: "job-1",
        requestId: "req-1",
        kind: "interrupted",
        reason: "interrupted",
        attempt: 1,
        seq: 1,
      },
    ]);
    mocks.hitlSnapshot.mockResolvedValueOnce(
      hitlSnapshotOf("interrupted", [hitlInterruptOf({ callId: "call-9", nonce: "nonce-9" })]),
    );
    reconnect();
    await flushReplay();
    expect(textOf(view!)).toContain("需要你确认");
    expect(view!.container.querySelectorAll(".nx-alert")).toHaveLength(1);
    expect(mocks.hitlEvents).toHaveBeenCalledWith("job-1", 0);

    clickButton(view!.container, "允许一次");
    await flush();
    expect(mocks.confirm).toHaveBeenCalledWith({
      jobId: "job-1",
      callId: "call-9",
      nonce: "nonce-9",
      decision: "allow",
    });
    expect(textOf(view!)).not.toContain("需要你确认");
    expect(textOf(view!)).toContain("exec_commands · 已允许一次");

    emit({
      type: "confirmRequired",
      id: "call-9",
      tool: "exec_commands",
      rendered: "$ rm -rf /tmp/x",
      confirmationNonce: "nonce-9",
      requestId: "req-1",
      attempt: 1,
    });
    expect(textOf(view!)).not.toContain("需要你确认");
    expect(view!.container.querySelectorAll(".nx-alert")).toHaveLength(0);
  });

  it("replays a pending question from the snapshot and answers it", async () => {
    await send("问吧");
    mocks.hitlEvents.mockResolvedValueOnce([]);
    mocks.hitlSnapshot.mockResolvedValueOnce(
      hitlSnapshotOf("interrupted", [
        hitlInterruptOf({
          id: "req-q",
          callId: "q-1",
          tool: "ask_user",
          kind: "question",
          parameters: {},
          nonce: "nonce-q",
          question: { id: "req-q", text: "继续吗？", options: ["继续", "停止"] },
        }),
      ]),
    );
    reconnect();
    await flushReplay();
    expect(textOf(view!)).toContain("AI 需要你回答");
    expect(textOf(view!)).toContain("继续吗？");
    clickButton(view!.container, "继续");
    await flush();
    expect(mocks.answer).toHaveBeenCalledWith({
      jobId: "job-1",
      callId: "q-1",
      nonce: "nonce-q",
      text: "继续",
    });
    expect(textOf(view!)).toContain("提问 · 已回答：继续");
  });

  it("settles a card that was answered elsewhere while disconnected", async () => {
    await send("跑个命令");
    emit({
      type: "confirmRequired",
      id: "call-1",
      tool: "exec_commands",
      rendered: "$ rm x",
      confirmationNonce: "nonce-1",
      requestId: "req-1",
      attempt: 1,
    });
    expect(textOf(view!)).toContain("需要你确认");
    mocks.hitlEvents.mockResolvedValueOnce([
      {
        runId: "job-1",
        checkpointId: "job-1",
        requestId: "req-1",
        kind: "resumed",
        reason: "",
        attempt: 2,
        seq: 2,
      },
    ]);
    mocks.hitlSnapshot.mockResolvedValueOnce(hitlSnapshotOf("running", [], { attempt: 2, seq: 2 }));
    reconnect();
    await flushReplay();
    expect(textOf(view!)).not.toContain("需要你确认");
    expect(textOf(view!)).toContain("exec_commands · 已在服务端回答");
  });

  it("closes pending cards when the run terminated while disconnected", async () => {
    await send("跑个命令");
    emit({
      type: "confirmRequired",
      id: "call-1",
      tool: "exec_commands",
      rendered: "$ rm x",
      confirmationNonce: "nonce-1",
      requestId: "req-1",
      attempt: 1,
    });
    mocks.hitlEvents.mockResolvedValueOnce([
      {
        runId: "job-1",
        checkpointId: "job-1",
        kind: "terminal",
        reason: "completed",
        attempt: 2,
        seq: 2,
      },
    ]);
    mocks.hitlSnapshot.mockResolvedValueOnce(
      hitlSnapshotOf("completed", [], {
        attempt: 2,
        seq: 2,
        terminal: {
          runId: "job-1",
          checkpointId: "job-1",
          kind: "terminal",
          reason: "completed",
          attempt: 2,
          seq: 2,
        },
      }),
    );
    reconnect();
    await flushReplay();
    expect(textOf(view!)).not.toContain("需要你确认");
    expect(textOf(view!)).toContain("本轮已结束，交互已关闭");
  });

  it("settles a rejected confirmation from the server snapshot instead of hanging", async () => {
    await send("跑个命令");
    emit({
      type: "confirmRequired",
      id: "call-1",
      tool: "exec_commands",
      rendered: "$ rm x",
      confirmationNonce: "nonce-1",
      requestId: "req-1",
      attempt: 1,
    });
    expect(textOf(view!)).toContain("需要你确认");
    mocks.confirm.mockRejectedValueOnce(new Error("确认已过期、重复或不属于当前工具调用"));
    mocks.hitlSnapshot.mockResolvedValueOnce(hitlSnapshotOf("running", [], { attempt: 2, seq: 2 }));
    clickButton(view!.container, "允许一次");
    await flushReplay();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("确认已过期"));
    expect(textOf(view!)).not.toContain("需要你确认");
    expect(textOf(view!)).toContain("exec_commands · 该交互已在服务端结束");
    expect(view!.container.querySelector('button[title="发送 (Enter)"]')).toBeNull();
  });

  it("keeps the card when a confirmation fails but the server still has it pending", async () => {
    await send("跑个命令");
    emit({
      type: "confirmRequired",
      id: "call-1",
      tool: "exec_commands",
      rendered: "$ rm x",
      confirmationNonce: "nonce-1",
      requestId: "req-1",
      attempt: 1,
    });
    mocks.confirm.mockRejectedValueOnce(new Error("网络抖动"));
    mocks.hitlSnapshot.mockResolvedValueOnce(
      hitlSnapshotOf("interrupted", [hitlInterruptOf()], { attempt: 1, seq: 1 }),
    );
    clickButton(view!.container, "允许一次");
    await flushReplay();
    expect(textOf(view!)).toContain("需要你确认");
    clickButton(view!.container, "拒绝");
    await flush();
    expect(mocks.confirm).toHaveBeenLastCalledWith({
      jobId: "job-1",
      callId: "call-1",
      nonce: "nonce-1",
      decision: "deny",
    });
    expect(textOf(view!)).toContain("exec_commands · 已拒绝");
  });

  it("ignores a duplicate click while the confirmation RPC is in flight", async () => {
    await send("跑个命令");
    emit({
      type: "confirmRequired",
      id: "call-1",
      tool: "exec_commands",
      rendered: "$ rm x",
      confirmationNonce: "nonce-1",
    });
    let release!: () => void;
    mocks.confirm.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          release = resolve;
        }),
    );
    const allow = [...view!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "允许一次",
    );
    if (!allow) throw new Error("allow button not found");
    click(allow);
    click(allow);
    await flush();
    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    release();
    await flush();
    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    expect(textOf(view!)).toContain("exec_commands · 已允许一次");
  });

  it("does not fire HITL replay for a settled run or the takeover channel", async () => {
    await send("跑个命令");
    const chatChannel = mocks.channels.at(-1);
    expect(mocks.reopens.has(chatChannel)).toBe(true);
    emit({ type: "done", answer: "完成" });
    await flush();
    expect(mocks.reopens.has(chatChannel)).toBe(false);
    expect(mocks.hitlEvents).not.toHaveBeenCalled();
    expect(mocks.hitlSnapshot).not.toHaveBeenCalled();

    click(view!.container.querySelector('button[title^="终端接管（实验性功能）：AI"]')!);
    await flush();
    await flush();
    const takeoverChannel = mocks.channels.at(-1);
    expect(takeoverChannel).toBeDefined();
    expect(mocks.reopens.has(takeoverChannel)).toBe(false);
  });

  it("does not bind a finished interrupted run or leave the sidebar busy", async () => {
    mocks.conversationList.mockResolvedValue([{ id: "c-9", title: "旧会话", updatedAt: 0 }]);
    mocks.messages.mockResolvedValue([{ role: "user", content: "旧问题" }]);
    mocks.runs.mockResolvedValue([
      {
        id: "job-dead", conversationId: "c-9", status: "interrupted", attempt: 1, seq: 2,
        planMode: false, source: "chat", answer: "", turns: 1, tokensIn: 0, tokensOut: 0,
        error: "AI 任务因应用重启而中断", createdAt: 1000, updatedAt: 1000, finishedAt: 1500,
      },
    ]);
    mocks.runEvents.mockResolvedValue([
      { type: "delta", seq: 1, text: "半截回答" },
      { type: "error", seq: 2, message: "AI 任务因应用重启而中断", retryable: true },
    ]);
    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();
    clickButton(view!.container, "旧会话");
    await flush();
    await flushReplay();

    expect(textOf(view!)).toContain("半截回答");
    expect(textOf(view!)).toContain("AI 任务因应用重启而中断");
    expect(useUi.getState().aiBusy).toBe(false);

    await send("新问题");
    expect(mocks.chat).toHaveBeenCalled();
  });

  it("binds the older interrupted run when only it still has a pending question", async () => {
    mocks.conversationList.mockResolvedValue([{ id: "c-9", title: "旧会话", updatedAt: 0 }]);
    mocks.messages.mockResolvedValue([{ role: "user", content: "旧问题" }]);
    const interruptedRun = (id: string, createdAt: number) => ({
      id, conversationId: "c-9", status: "interrupted", attempt: 1, seq: 1,
      planMode: false, source: "chat", answer: "", turns: 0, tokensIn: 0, tokensOut: 0,
      createdAt, updatedAt: createdAt,
    });
    mocks.runs.mockResolvedValue([interruptedRun("job-new", 3000), interruptedRun("job-old", 1000)]);
    mocks.runEvents.mockImplementation(async (jobId: string) =>
      jobId === "job-old"
        ? [{ type: "questionRequired", seq: 1, id: "ask-1", question: { question: "继续吗？", options: ["继续"] }, confirmationNonce: "nonce-old", requestId: "req-old", attempt: 1 }]
        : [{ type: "delta", seq: 1, text: "较新的中断" }],
    );
    mocks.hitlSnapshot.mockImplementation(async (jobId: string) =>
      jobId === "job-old"
        ? hitlSnapshotOf("interrupted", [
            hitlInterruptOf({ id: "req-old", runId: "job-old", checkpointId: "job-old", callId: "ask-1", tool: "ask_user", kind: "question", parameters: {}, nonce: "nonce-old", question: { id: "req-old", text: "继续吗？", options: ["继续"] } }),
          ], { runId: "job-old", checkpointId: "job-old" })
        : hitlSnapshotOf("interrupted", [], { runId: "job-new", checkpointId: "job-new" }),
    );
    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();
    clickButton(view!.container, "旧会话");
    await flush();
    await flushReplay();

    expect(useUi.getState().aiBusy).toBe(true);
    expect(textOf(view!)).toContain("继续吗？");
    clickButton(view!.container, "继续");
    await flush();
    expect(mocks.answer).toHaveBeenCalledWith(
      { jobId: "job-old", callId: "ask-1", nonce: "nonce-old", text: "继续" },
      expect.anything(),
    );
  });

  it("settles the restored run when a reconnect replays its terminal event", async () => {
    mocks.conversationList.mockResolvedValue([{ id: "c-9", title: "旧会话", updatedAt: 0 }]);
    mocks.messages.mockResolvedValue([{ role: "user", content: "旧问题" }]);
    mocks.runs.mockResolvedValue([
      {
        id: "job-old", conversationId: "c-9", status: "interrupted", attempt: 1, seq: 1,
        planMode: false, source: "chat", answer: "", turns: 0, tokensIn: 0, tokensOut: 0,
        createdAt: 1000, updatedAt: 1000,
      },
    ]);
    const questionJournal = [
      { type: "questionRequired", seq: 1, id: "ask-1", question: { question: "继续吗？", options: ["继续"] }, confirmationNonce: "nonce-old", requestId: "req-old", attempt: 1 },
    ];
    mocks.runEvents.mockResolvedValue(questionJournal);
    mocks.hitlSnapshot.mockResolvedValue(
      hitlSnapshotOf("interrupted", [
        hitlInterruptOf({ id: "req-old", runId: "job-old", checkpointId: "job-old", callId: "ask-1", tool: "ask_user", kind: "question", parameters: {}, nonce: "nonce-old", question: { id: "req-old", text: "继续吗？", options: ["继续"] } }),
      ], { runId: "job-old", checkpointId: "job-old" }),
    );
    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();
    clickButton(view!.container, "旧会话");
    await flush();
    await flushReplay();

    expect(useUi.getState().aiBusy).toBe(true);
    const restoredChannel = mocks.channels.at(-1);
    expect(mocks.reopens.has(restoredChannel)).toBe(true);

    mocks.runEvents.mockResolvedValue([
      ...questionJournal,
      { type: "done", seq: 2, answer: "做完了", turns: 1, tokensIn: 1, tokensOut: 1 },
    ]);
    mocks.hitlSnapshot.mockResolvedValue(
      hitlSnapshotOf("completed", [], {
        runId: "job-old", checkpointId: "job-old",
        terminal: { runId: "job-old", checkpointId: "job-old", kind: "terminal", reason: "completed", attempt: 2, seq: 2 },
      }),
    );
    reconnect();
    await flushReplay();

    expect(useUi.getState().aiBusy).toBe(false);
    expect(mocks.dispose).toHaveBeenCalledWith(restoredChannel);
    expect(textOf(view!)).toContain("本轮已完成");
  });

  it("clears busy and disposes the restored channel when the bound run expired and the answer fails", async () => {
    mocks.conversationList.mockResolvedValue([{ id: "c-9", title: "旧会话", updatedAt: 0 }]);
    mocks.messages.mockResolvedValue([{ role: "user", content: "旧问题" }]);
    mocks.runs.mockResolvedValue([
      {
        id: "job-old", conversationId: "c-9", status: "interrupted", attempt: 1, seq: 1,
        planMode: false, source: "chat", answer: "", turns: 0, tokensIn: 0, tokensOut: 0,
        createdAt: 1000, updatedAt: 1000,
      },
    ]);
    const questionJournal = [
      { type: "questionRequired", seq: 1, id: "ask-1", question: { question: "继续吗？", options: ["继续"] }, confirmationNonce: "nonce-old", requestId: "req-old", attempt: 1 },
    ];
    mocks.runEvents
      .mockResolvedValueOnce(questionJournal)
      .mockResolvedValue([
        ...questionJournal,
        { type: "error", seq: 2, message: "AI 确认请求已过期，请重新发送", retryable: false },
      ]);
    const pendingSnapshot = () =>
      hitlSnapshotOf("interrupted", [
        hitlInterruptOf({ id: "req-old", runId: "job-old", checkpointId: "job-old", callId: "ask-1", tool: "ask_user", kind: "question", parameters: {}, nonce: "nonce-old", question: { id: "req-old", text: "继续吗？", options: ["继续"] }, expiresAt: new Date(Date.now() + 3_600_000).toISOString() }),
      ], { runId: "job-old", checkpointId: "job-old" });
    mocks.hitlSnapshot
      .mockResolvedValueOnce(pendingSnapshot())
      .mockResolvedValueOnce(pendingSnapshot())
      .mockResolvedValue(
        hitlSnapshotOf("expired", [], {
          runId: "job-old", checkpointId: "job-old",
          terminal: { runId: "job-old", checkpointId: "job-old", kind: "terminal", reason: "expired", attempt: 1, seq: 2 },
        }),
      );
    mocks.answer.mockRejectedValue(new Error("AI 任务不存在或已结束"));
    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();
    clickButton(view!.container, "旧会话");
    await flush();
    await flushReplay();

    expect(useUi.getState().aiBusy).toBe(true);
    const restoredChannel = mocks.channels.at(-1);
    expect(mocks.reopens.has(restoredChannel)).toBe(true);

    clickButton(view!.container, "继续");
    await flush();
    await flushReplay();

    expect(useUi.getState().aiBusy).toBe(false);
    expect(mocks.dispose).toHaveBeenCalledWith(restoredChannel);
    expect(textOf(view!)).toContain("过期");
    await send("继续新问题");
    expect(mocks.chat).toHaveBeenCalled();
  });

  it("settles the bound restored run by itself when it expires while viewing", async () => {
    mocks.conversationList.mockResolvedValue([{ id: "c-9", title: "旧会话", updatedAt: 0 }]);
    mocks.messages.mockResolvedValue([{ role: "user", content: "旧问题" }]);
    mocks.runs.mockResolvedValue([
      {
        id: "job-old", conversationId: "c-9", status: "interrupted", attempt: 1, seq: 1,
        planMode: false, source: "chat", answer: "", turns: 0, tokensIn: 0, tokensOut: 0,
        createdAt: 1000, updatedAt: 1000,
      },
    ]);
    const questionJournal = [
      { type: "questionRequired", seq: 1, id: "ask-1", question: { question: "继续吗？", options: ["继续"] }, confirmationNonce: "nonce-old", requestId: "req-old", attempt: 1 },
    ];
    mocks.runEvents
      .mockResolvedValueOnce(questionJournal)
      .mockResolvedValue([
        ...questionJournal,
        { type: "error", seq: 2, message: "AI 确认请求已过期，请重新发送", retryable: false },
      ]);
    mocks.hitlSnapshot.mockResolvedValue(
      hitlSnapshotOf("interrupted", [
        hitlInterruptOf({
          id: "req-old", runId: "job-old", checkpointId: "job-old", callId: "ask-1",
          tool: "ask_user", kind: "question", parameters: {}, nonce: "nonce-old",
          question: { id: "req-old", text: "继续吗？", options: ["继续"] },
          expiresAt: new Date(Date.now() + 80).toISOString(),
        }),
      ], { runId: "job-old", checkpointId: "job-old" }),
    );
    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();
    clickButton(view!.container, "旧会话");
    await flush();
    await flushReplay();

    expect(useUi.getState().aiBusy).toBe(true);
    const restoredChannel = mocks.channels.at(-1);

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 1000));
    });

    expect(mocks.runEvents).toHaveBeenCalledTimes(2);
    expect(useUi.getState().aiBusy).toBe(false);
    expect(mocks.dispose).toHaveBeenCalledWith(restoredChannel);
    await send("过期之后");
    expect(mocks.chat).toHaveBeenCalled();
  });
});
