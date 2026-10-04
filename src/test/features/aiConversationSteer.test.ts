/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import {
  click,
  deferred,
  flush,
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

function emit(ev: Record<string, unknown>, channelIndex = -1) {
  const channel = mocks.channels.at(channelIndex);
  if (!channel) throw new Error("no fake channel");
  act(() => channel.onEvent(ev));
}

function textOf(view: MountedView): string {
  return view.container.textContent ?? "";
}

function textareaOf(view: MountedView): HTMLTextAreaElement {
  const textarea = view.container.querySelector("textarea");
  if (!textarea) throw new Error("input textarea not found");
  return textarea;
}

async function send(view: MountedView, text: string) {
  setInputValue(textareaOf(view), text);
  click(view.container.querySelector('button[title="发送 (Enter)"]')!);
  await flush();
}

async function steer(view: MountedView, text: string) {
  const textarea = textareaOf(view);
  setInputValue(textarea, text);
  act(() => {
    textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
  });
  await flush();
}

describe("AiSidebar mid-run steering", () => {
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
    mocks.steer.mockResolvedValue(undefined);
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

  it("steers the running job through the same input — no new panel, no new run", async () => {
    await send(view!, "排查一下");
    expect(mocks.chat).toHaveBeenCalledOnce();
    expect(textareaOf(view!).placeholder).toContain("Enter 发送补充指令");
    expect(view!.container.querySelectorAll("textarea")).toHaveLength(1);

    await steer(view!, "顺便看看内存");
    expect(mocks.steer).toHaveBeenCalledWith("job-1", "顺便看看内存");
    expect(mocks.chat).toHaveBeenCalledOnce();
    expect(textOf(view!)).toContain("顺便看看内存");
    expect(textOf(view!)).toContain("等待注入");

    emit({ type: "steered", text: "顺便看看内存" });
    await flush();
    expect(textOf(view!)).toContain("已注入当前运行");
    expect(textOf(view!)).not.toContain("等待注入");

    emit({ type: "done", answer: "排查完了" });
    await flush();
    expect(textOf(view!)).toContain("排查完了");
  });

  it("keeps the bubble delivered when the steered event races ahead of the RPC response", async () => {
    await send(view!, "跑个长任务");
    const rpc = deferred<void>();
    mocks.steer.mockReturnValueOnce(rpc.promise);

    const textarea = textareaOf(view!);
    setInputValue(textarea, "先别动数据库");
    act(() => {
      textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    emit({ type: "steered", text: "先别动数据库" });
    await flush();
    expect(textOf(view!)).toContain("已注入当前运行");

    rpc.resolve();
    await flush();
    expect(textOf(view!)).toContain("已注入当前运行");
    emit({ type: "done", answer: "完了" });
    await flush();
  });

  it("marks the steer dropped and restores the input when the kernel rejects it", async () => {
    await send(view!, "跑个命令");
    mocks.steer.mockRejectedValueOnce(new Error("AI 任务不存在或已结束"));
    await steer(view!, "其实别跑");
    expect(mocks.steer).toHaveBeenCalledWith("job-1", "其实别跑");
    expect(textOf(view!)).toContain("未送达（模型未看到）");
    expect(textareaOf(view!).value).toBe("其实别跑");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("补充指令未送达"));
  });

  it("settles steer bubbles in FIFO order — dropped first, then delivered", async () => {
    await send(view!, "继续");
    await steer(view!, "第一条");
    await steer(view!, "第二条");
    expect(textOf(view!).match(/等待注入/g)?.length).toBe(2);

    emit({ type: "steerDropped", text: "第一条" });
    await flush();
    let texts = textOf(view!);
    expect(texts).toContain("第一条");
    expect(texts.indexOf("未送达")).toBeLessThan(texts.indexOf("第二条"));
    expect(texts.match(/等待注入/g)?.length).toBe(1);

    emit({ type: "steered", text: "第二条" });
    await flush();
    texts = textOf(view!);
    expect(texts).toContain("已注入当前运行");
    expect(texts).not.toContain("等待注入");

    emit({ type: "steered", text: "第二条" });
    await flush();
    expect(textOf(view!)).toBe(texts);
  });

  it("settles pending steers as dropped when the run ends, and Enter then starts a new run", async () => {
    await send(view!, "查一下");
    await steer(view!, "等等，先看磁盘");
    expect(textOf(view!)).toContain("等待注入");

    emit({ type: "done", answer: "查完了" });
    await flush();
    expect(textOf(view!)).toContain("未送达（模型未看到）");

    await steer(view!, "下一个问题");
    expect(mocks.chat).toHaveBeenCalledTimes(2);
    expect(mocks.steer).toHaveBeenCalledOnce();
  });

  it("stop drops pending steers immediately", async () => {
    await send(view!, "跑起来");
    await steer(view!, "补充一句");
    expect(textOf(view!)).toContain("等待注入");

    click(view!.container.querySelector('button[title^="停止这一轮"]')!);
    await flush();
    expect(mocks.cancel).toHaveBeenCalledWith("job-1");
    expect(textOf(view!)).toContain("未送达（模型未看到）");
  });

  it("Enter with an empty input while busy is a no-op", async () => {
    await send(view!, "跑着");
    await steer(view!, "   ");
    expect(mocks.steer).not.toHaveBeenCalled();
    expect(mocks.chat).toHaveBeenCalledOnce();
  });
});
