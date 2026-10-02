/** @vitest-environment jsdom */

// AI 侧栏端到端（假 IPC + 假事件流 + 手动帧）：
// 发送 → 流式 → 终态、HITL 确认 / 提问、停止、历史切换、接管、跟随滚动。
// 帧由测试手动驱动：delta 先进合并队列，runFrames() 才落屏 —— 与生产 rAF 同一条路。
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
  getPermission: vi.fn(),
  conversationList: vi.fn(),
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
}));

vi.mock("../../ipc/commands", () => ({
  aiApi: {
    chat: mocks.chat,
    cancel: mocks.cancel,
    confirm: mocks.confirm,
    answer: mocks.answer,
    getPermission: mocks.getPermission,
    conversationList: mocks.conversationList,
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

function textOf(view: MountedView): string {
  return view.container.textContent ?? "";
}

describe("AiSidebar conversation stream UX", () => {
  let view: MountedView | null = null;

  beforeEach(() => {
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
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    mocks.conversationList.mockResolvedValue([]);
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
    // 合并队列未落帧前，正文不上屏。
    expect(textOf(view!)).not.toContain("流式回答");
    act(runFrames);
    expect(textOf(view!)).toContain("流式回答");

    emit({ type: "done", answer: "流式回答" });
    await flush();
    expect(textOf(view!)).toContain("本轮已完成");
    expect(view!.container.querySelector('button[title="发送 (Enter)"]')).not.toBeNull();

    // 重放终态：不重复气泡、不重复终态行，通道仍然幂等释放。
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

    // 终态不会重开已结算的卡片。
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

    // 自由文本回答走另一张卡：输入草稿 → 提交 → 结算留痕。
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
    // 已停止后可以发起新一轮。
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
    // 先展开历史列表，再点进旧会话。
    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();
    clickButton(view!.container, "旧会话");
    await flush();
    expect(mocks.messages).toHaveBeenCalledWith("c-9");
    expect(textOf(view!)).toContain("旧问题");
    expect(textOf(view!)).toContain("旧回答");
    expect(textOf(view!)).not.toContain("当前回答");

    // 旧通道的迟到事件不得渗进历史视图。
    emit({ type: "delta", text: "残影" });
    act(runFrames);
    expect(textOf(view!)).not.toContain("残影");

    click(view!.container.querySelector('button[title="新建会话"]')!);
    await flush();
    expect(textOf(view!)).not.toContain("旧问题");
    expect(textOf(view!)).toContain("命令与输出全程留痕");
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
    const scroller = view!.container.querySelector('[role="log"]') as HTMLDivElement;
    const metrics = { scrollTop: 0, scrollHeight: 0, clientHeight: 0 };
    for (const key of ["scrollTop", "scrollHeight", "clientHeight"] as const) {
      Object.defineProperty(scroller, key, {
        configurable: true,
        get: () => metrics[key],
        set: (value: number) => {
          metrics[key] = value;
        },
      });
    }
    const scrollCalls: number[] = [];
    scroller.scrollTo = ((options?: ScrollToOptions) => {
      const top = Number(options?.top ?? 0);
      scrollCalls.push(top);
      metrics.scrollTop = top;
    }) as typeof scroller.scrollTo;

    await send("滚动测试");
    metrics.scrollHeight = 1000;
    metrics.clientHeight = 200;
    metrics.scrollTop = 800;
    emit({ type: "delta", text: "第一段" });
    act(runFrames);
    expect(scrollCalls.length).toBeGreaterThan(0);

    // 用户上翻：之后的流式输出不再拽动视图，改亮「回到最新」。
    metrics.scrollTop = 100;
    act(() => {
      scroller.dispatchEvent(new Event("scroll"));
    });
    const callsBefore = scrollCalls.length;
    metrics.scrollHeight = 1600;
    emit({ type: "delta", text: "第二段" });
    act(runFrames);
    expect(scrollCalls.length).toBe(callsBefore);
    expect(textOf(view!)).toContain("↓ 新输出");
    expect(metrics.scrollTop).toBe(100);

    clickButton(view!.container, "↓ 新输出");
    expect(scrollCalls.at(-1)).toBe(1600);
    expect(textOf(view!)).not.toContain("↓ 新输出");
    metrics.scrollHeight = 2000;
    emit({ type: "done", answer: "第一段第二段" });
    await flush();
    expect(scrollCalls.at(-1)).toBe(2000);
  });
});
