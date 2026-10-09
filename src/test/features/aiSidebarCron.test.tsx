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
  conversationList: vi.fn(),
  conversationDelete: vi.fn(),
  messages: vi.fn(),
  overview: vi.fn(),
  cronList: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
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
  disposeChannel: vi.fn(),
  onChannelReopen: () => () => undefined,
}));
vi.mock("../../ipc/cron", () => ({
  cronApi: {
    list: mocks.cronList,
    register: vi.fn(),
    setEnabled: vi.fn(),
    unregister: vi.fn(),
  },
  cronTimeoutMs: (job: { timeout: number }) => Math.round(job.timeout / 1_000_000),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, promptText: vi.fn() }));

import { AiSidebar } from "../../features/ai/AiSidebar";
import { useUi } from "../../app/store";

let convSeq = 0;

function textOf(view: MountedView): string {
  return view.container.textContent ?? "";
}

function cronButton(view: MountedView): HTMLButtonElement {
  const button = view.container.querySelector<HTMLButtonElement>(
    'button[title="当前会话的定时任务"]',
  );
  if (!button) throw new Error("cron entry button not found");
  return button;
}

async function send(view: MountedView, text: string) {
  const textarea = view.container.querySelector("textarea");
  if (!textarea) throw new Error("input textarea not found");
  setInputValue(textarea, text);
  click(view.container.querySelector('button[title="发送 (Enter)"]')!);
  await flush();
}

function emitDone(answer: string) {
  const channel = mocks.channels[mocks.channels.length - 1];
  if (!channel) throw new Error("no fake channel");
  act(() => channel.onEvent({ type: "done", answer }));
}

describe("AiSidebar 定时任务入口", () => {
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
    mocks.cronList.mockResolvedValue([]);
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

  it("没有会话时入口禁用并提示先开始会话", async () => {
    await flush();
    const disabled = view!.container.querySelector<HTMLButtonElement>(
      'button[title="定时任务挂在会话上：先发送消息开始一个会话"]',
    );
    expect(disabled).not.toBeNull();
    expect(disabled!.disabled).toBe(true);
    expect(mocks.cronList).not.toHaveBeenCalled();
  });

  it("有会话后入口可点，面板按当前会话读取，关闭后收起", async () => {
    await flush();
    await send(view!, "问题甲");
    emitDone("回答甲");
    await flush();
    expect(cronButton(view!).disabled).toBe(false);

    click(cronButton(view!));
    await flushUntil(() => textOf(view!).includes("这个会话还没有定时任务"));
    expect(mocks.cronList).toHaveBeenCalledWith("conv-1");
    expect(textOf(view!)).toContain("定时任务");

    click(view!.container.querySelector('button[title="关闭"]')!);
    await flush();
    expect(textOf(view!)).not.toContain("这个会话还没有定时任务");
    expect(cronButton(view!).className).not.toContain("is-active");
  });

  it("删除当前会话后面板随之关闭", async () => {
    await flush();
    await send(view!, "问题甲");
    emitDone("回答甲");
    await flush();
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "会话甲", updatedAt: 0 }]);

    click(cronButton(view!));
    await flushUntil(() => textOf(view!).includes("这个会话还没有定时任务"));

    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flushUntil(() => textOf(view!).includes("会话甲"));
    click(view!.container.querySelector('button[aria-label="删除会话「会话甲」"]')!);
    await flushUntil(() => mocks.conversationDelete.mock.calls.length > 0);
    expect(mocks.conversationDelete).toHaveBeenCalledWith("conv-1");
    await flush();
    expect(textOf(view!)).not.toContain("这个会话还没有定时任务");
  });
});
