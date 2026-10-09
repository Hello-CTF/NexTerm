/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import {
  click,
  clickButton,
  flush,
  flushUntil,
  mount,
  setInputValue,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  chat: vi.fn(),
  steer: vi.fn(),
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
    steer: mocks.steer,
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
import { GLOBAL_AI_BOARD_KEY, useUi } from "../../app/store";

let rafQueue: { cb: FrameRequestCallback; cancelled: boolean }[] = [];

function emit(ev: Record<string, unknown>, channelIndex = -1) {
  const channel = mocks.channels.at(channelIndex);
  if (!channel) throw new Error("no fake channel");
  act(() => channel.onEvent(ev));
}

function textOf(view: MountedView): string {
  return view.container.textContent ?? "";
}

async function flushReplay() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

function runOf(overrides: Record<string, unknown>) {
  return {
    id: "job-old",
    conversationId: "conv-1",
    status: "interrupted",
    attempt: 1,
    seq: 1,
    planMode: false,
    source: "chat",
    answer: "",
    turns: 0,
    tokensIn: 0,
    tokensOut: 0,
    createdAt: 1000,
    updatedAt: 1000,
    ...overrides,
  };
}

describe("AiSidebar session restore", () => {
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
    mocks.reopens.clear();
    mocks.chat.mockResolvedValue({ jobId: "job-1", conversationId: "conv-1" });
    mocks.steer.mockResolvedValue(undefined);
    mocks.cancel.mockResolvedValue(undefined);
    mocks.confirm.mockResolvedValue(undefined);
    mocks.answer.mockResolvedValue(undefined);
    mocks.hitlSnapshot.mockResolvedValue({
      runId: "job-old",
      checkpointId: "job-old",
      status: "interrupted",
      attempt: 1,
      seq: 1,
      pending: [],
    });
    mocks.hitlEvents.mockResolvedValue([]);
    mocks.runs.mockResolvedValue([]);
    mocks.runEvents.mockResolvedValue([]);
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "排查 502", updatedAt: 0 }]);
    mocks.conversationDelete.mockResolvedValue(undefined);
    mocks.messages.mockResolvedValue([
      { role: "user", content: { role: "user", content: "旧问题" } },
      { role: "assistant", content: { role: "assistant", content: "旧回答" } },
    ]);
    mocks.promptText.mockResolvedValue("安装 nginx");
    mocks.ask.mockResolvedValue(true);
    mocks.takeoverEnter.mockResolvedValue({ token: "tok-1" });
    mocks.takeoverRun.mockResolvedValue({ jobId: "job-t", token: "tok-2" });
    mocks.takeoverExit.mockResolvedValue(undefined);
    localStorage.clear();
    useUi.setState({
      rightOpen: true,
      aiBusy: false,
      takeover: null,
      pushToast: mocks.toast,
      workspaces: [],
      aiBoards: {},
      sessions: [],
    });
  });

  afterEach(() => {
    view?.unmount();
    view = null;
    localStorage.clear();
  });

  function mountSidebar() {
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
  }

  function bindConversation(id: string, title: string) {
    useUi.setState({
      aiBoards: {
        [GLOBAL_AI_BOARD_KEY]: {
          tabs: [{ id: "tab-bound", title, conversationId: id }],
          activeTabId: "tab-bound",
        },
      },
    });
  }

  async function send(text: string) {
    const textarea = view!.container.querySelector("textarea");
    if (!textarea) throw new Error("input textarea not found");
    setInputValue(textarea, text);
    click(view!.container.querySelector('button[title="发送 (Enter)"]')!);
    await flush();
  }

  it("restores the bound conversation on mount instead of a blank session", async () => {
    bindConversation("conv-1", "排查 502");
    mountSidebar();
    await flushReplay();

    expect(mocks.messages).toHaveBeenCalledWith("conv-1");
    expect(textOf(view!)).toContain("旧问题");
    expect(textOf(view!)).toContain("旧回答");
    expect(textOf(view!)).not.toContain("新建会话");
  });

  it("continues the restored conversation instead of creating a blank replacement", async () => {
    bindConversation("conv-1", "排查 502");
    mountSidebar();
    await flushReplay();

    await send("接着问");
    expect(mocks.chat).toHaveBeenCalledWith(
      expect.objectContaining({ conversationId: "conv-1" }),
    );
  });

  it("stays blank when the bound conversation no longer exists", async () => {
    bindConversation("conv-gone", "已删除的会话");
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "排查 502", updatedAt: 0 }]);
    mountSidebar();
    await flushReplay();

    expect(textOf(view!)).toContain("新建会话");
    await send("新问题");
    expect(mocks.chat).toHaveBeenCalledWith(
      expect.objectContaining({ conversationId: undefined }),
    );
  });

  it("offers retry when the message reload fails transiently", async () => {
    bindConversation("conv-1", "排查 502");
    mocks.messages.mockRejectedValueOnce(new Error("会话库不可用"));
    mountSidebar();
    await flushReplay();

    expect(textOf(view!)).toContain("会话加载失败 · 会话库不可用");
    expect(textOf(view!)).not.toContain("新建会话");

    clickButton(view!.container, "重试");
    await flushReplay();
    expect(textOf(view!)).toContain("旧问题");
    expect(textOf(view!)).not.toContain("会话加载失败");
  });

  it("恢复失败后删除当前会话会清除错误态，不留无法重试的旧错误", async () => {
    bindConversation("conv-1", "排查 502");
    mocks.messages.mockRejectedValueOnce(new Error("会话库不可用"));
    mountSidebar();
    await flushReplay();
    expect(textOf(view!)).toContain("会话加载失败 · 会话库不可用");

    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();
    click(view!.container.querySelector('button[aria-label="删除会话「排查 502」"]')!);
    await flushUntil(() => mocks.conversationDelete.mock.calls.length > 0);
    await flush();

    expect(mocks.conversationDelete).toHaveBeenCalledWith("conv-1");
    expect(textOf(view!)).not.toContain("会话加载失败");
    expect(textOf(view!)).toContain("新建会话");
  });

  it("replays an interrupted run with a pending question into the restored session", async () => {
    bindConversation("conv-1", "排查 502");
    mocks.runs.mockResolvedValue([runOf({})]);
    mocks.runEvents.mockResolvedValue([
      {
        type: "questionRequired",
        seq: 1,
        id: "ask-1",
        question: { question: "继续吗？", options: ["继续"] },
        confirmationNonce: "nonce-old",
        requestId: "req-old",
        attempt: 1,
      },
    ]);
    mocks.hitlSnapshot.mockResolvedValue({
      runId: "job-old",
      checkpointId: "job-old",
      status: "interrupted",
      attempt: 1,
      seq: 1,
      pending: [
        {
          id: "req-old",
          runId: "job-old",
          checkpointId: "job-old",
          checkpointHash: "h",
          targetId: "t",
          callId: "ask-1",
          tool: "ask_user",
          kind: "question",
          parameters: {},
          parameterHash: "p",
          nonce: "nonce-old",
          createdAt: "2026-10-03T10:00:00Z",
          expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
          attempt: 1,
          seq: 1,
          question: { id: "req-old", text: "继续吗？", options: ["继续"] },
        },
      ],
    });
    mountSidebar();
    await flushReplay();

    expect(useUi.getState().aiBusy).toBe(true);
    expect(textOf(view!)).toContain("继续吗？");
    clickButton(view!.container, "继续");
    await flush();
    expect(mocks.answer).toHaveBeenCalledWith(
      { jobId: "job-old", callId: "ask-1", nonce: "nonce-old", text: "继续" },
      expect.anything(),
    );
    emit({ type: "done", answer: "做完了" });
    await flush();
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("clears usage and todos when switching to another conversation", async () => {
    mountSidebar();
    await flush();
    await send("当前问题");
    emit({ type: "usage", promptTokens: 5000, completionTokens: 100, cachedTokens: 0, contextWindow: 10000 });
    emit({
      type: "todos",
      items: [{ content: "采集数据", status: "in_progress" }],
    });
    await flush();
    const ring = () => view!.container.querySelector('span[role="img"]');
    expect(ring()?.getAttribute("aria-label")).toContain("上下文占用");
    expect(textOf(view!)).toContain("任务清单");

    emit({ type: "done", answer: "当前回答" });
    await flush();
    mocks.conversationList.mockResolvedValue([{ id: "c-9", title: "旧会话", updatedAt: 0 }]);
    mocks.messages.mockResolvedValue([{ role: "user", content: { role: "user", content: "旧问题" } }]);
    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();
    clickButton(view!.container, "旧会话");
    await flush();

    expect(textOf(view!)).toContain("旧问题");
    expect(textOf(view!)).not.toContain("当前回答");
    expect(ring()?.getAttribute("aria-label")).toBe("尚未产生用量");
    expect(textOf(view!)).not.toContain("任务清单");
  });

  it("shows a waiting phase while a confirmation card is pending", async () => {
    mountSidebar();
    await flush();
    await send("跑个命令");
    emit({ type: "status", phase: "thinking", turn: 1 });
    await flush();
    expect(textOf(view!)).toContain("正在思考");

    emit({
      type: "confirmRequired",
      id: "call-1",
      tool: "exec_commands",
      rendered: "$ rm x",
      confirmationNonce: "nonce-1",
    });
    await flush();
    expect(textOf(view!)).toContain("等待你确认后继续");
    expect(textOf(view!)).not.toContain("正在思考");

    clickButton(view!.container, "允许一次");
    await flush();
    expect(textOf(view!)).not.toContain("等待你确认后继续");
  });

  it("shows a waiting phase while a question card is pending", async () => {
    mountSidebar();
    await flush();
    await send("问吧");
    emit({
      type: "questionRequired",
      id: "q-1",
      question: { question: "继续吗？", options: ["继续"] },
      confirmationNonce: "nonce-q",
    });
    await flush();
    expect(textOf(view!)).toContain("等待你回答后继续");

    clickButton(view!.container, "继续");
    await flush();
    expect(textOf(view!)).not.toContain("等待你回答后继续");
  });

  it("gives visible feedback when steering before the run has a job", async () => {
    mountSidebar();
    await flush();
    let resolveChat!: (v: { jobId: string; conversationId: string }) => void;
    mocks.chat.mockImplementationOnce(
      () => new Promise<{ jobId: string; conversationId: string }>((res) => { resolveChat = res; }),
    );
    await send("启动中");

    const textarea = view!.container.querySelector("textarea")!;
    setInputValue(textarea, "先补充一句");
    act(() => {
      textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    await flush();
    expect(mocks.steer).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("还在启动"));
    expect((view!.container.querySelector("textarea") as HTMLTextAreaElement).value).toBe("先补充一句");

    resolveChat({ jobId: "job-1", conversationId: "conv-1" });
    await flush();
    act(() => {
      textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    await flush();
    expect(mocks.steer).toHaveBeenCalledWith("job-1", "先补充一句");
  });
});
