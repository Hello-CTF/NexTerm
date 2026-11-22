/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";
import type { AiHitlSnapshotDto, AiRunDto, AiRunEventDto, MessageDto } from "../../ipc/types";

const mocks = vi.hoisted(() => ({
  chat: vi.fn(),
  cancel: vi.fn(),
  confirm: vi.fn(),
  answer: vi.fn(),
  hitlSnapshot: vi.fn(),
  hitlEvents: vi.fn(),
  runs: vi.fn(),
  run: vi.fn(),
  runEvents: vi.fn(),
  getPermission: vi.fn(),
  conversationList: vi.fn(),
  messages: vi.fn(),
  overview: vi.fn(),
  toast: vi.fn(),
  dispose: vi.fn(),
  channels: [] as { onEvent: (ev: Record<string, unknown>) => void }[],
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    aiApi: {
      chat: mocks.chat,
      cancel: mocks.cancel,
      confirm: mocks.confirm,
      answer: mocks.answer,
      hitlSnapshot: mocks.hitlSnapshot,
      hitlEvents: mocks.hitlEvents,
      runs: mocks.runs,
      run: mocks.run,
      runEvents: mocks.runEvents,
      getPermission: mocks.getPermission,
      conversationList: mocks.conversationList,
      messages: mocks.messages,
    },
    modelApi: { overview: mocks.overview, activate: vi.fn() },
  });
});

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

vi.mock("../../ui/dialogs", () => ({ ask: vi.fn(), askChoice: vi.fn(), promptText: vi.fn() }));

import { AiSidebar } from "../../features/ai/AiSidebar";
import { aiTabBusyIds, aiTabRunControls, clearAiTabRuns } from "../../features/ai/runOwnership";
import { GLOBAL_AI_BOARD_KEY, useUi } from "../../app/store";

const NOW = Date.parse("2026-11-15T08:00:00Z");

function runOf(overrides: Partial<AiRunDto>): AiRunDto {
  return {
    id: "job-r",
    conversationId: "conv-a",
    status: "interrupted",
    attempt: 1,
    seq: 1,
    planMode: false,
    source: "chat",
    answer: "",
    turns: 0,
    tokensIn: 0,
    tokensOut: 0,
    cacheCreationTokens: 0,
    latencyMs: 0,
    retries: 0,
    failures: 0,
    createdAt: 1,
    updatedAt: 1,
    ...overrides,
  };
}

function messageOf(index: number, role: string, content: Record<string, unknown>): MessageDto {
  return {
    id: `message-${index}`,
    conversationId: "conv-a",
    role,
    content: { role, ...content },
    tokensIn: null,
    tokensOut: null,
    createdAt: index,
  };
}

function snapshotOf(expiresAt: number): AiHitlSnapshotDto {
  return {
    runId: "job-r",
    checkpointId: "job-r",
    status: "running",
    attempt: 1,
    seq: 1,
    pending: [
      {
        id: "req-1",
        runId: "job-r",
        checkpointId: "job-r",
        callId: "call-1",
        tool: "exec_commands",
        kind: "confirm",
        parameters: {},
        parameterHash: "hash-1",
        nonce: "nonce-1",
        createdAt: new Date(NOW).toISOString(),
        expiresAt: new Date(expiresAt).toISOString(),
        attempt: 1,
        seq: 1,
      },
    ],
  } as AiHitlSnapshotDto;
}

function serveEvents(log: Record<string, AiRunEventDto[]>) {
  mocks.runEvents.mockImplementation((jobId: string, afterSeq: number) =>
    Promise.resolve((log[jobId] ?? []).filter((event) => (event.seq as number) > afterSeq)),
  );
}

describe("AiSidebar run-state sync", () => {
  let view: MountedView | null = null;

  beforeEach(() => {
    vi.clearAllMocks();
    clearAiTabRuns([...aiTabBusyIds, ...aiTabRunControls.keys()]);
    mocks.channels.length = 0;
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      setTimeout(cb, 0);
      return 1;
    });
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
    mocks.chat.mockResolvedValue({ jobId: "job-1", conversationId: "conv-a" });
    mocks.cancel.mockResolvedValue(undefined);
    mocks.confirm.mockResolvedValue(undefined);
    mocks.answer.mockResolvedValue(undefined);
    mocks.hitlSnapshot.mockResolvedValue(null);
    mocks.hitlEvents.mockResolvedValue([]);
    mocks.runs.mockResolvedValue([]);
    mocks.run.mockResolvedValue(runOf({ id: "job-1", status: "running" }));
    mocks.runEvents.mockResolvedValue([]);
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.conversationList.mockResolvedValue([{ id: "conv-a", title: "A", createdAt: 0, updatedAt: 0, scope: {} }]);
    mocks.messages.mockResolvedValue([]);
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    localStorage.clear();
    useUi.setState({
      rightOpen: true,
      aiBusy: false,
      takeover: null,
      pushToast: mocks.toast,
      workspaces: [],
      activeWorkspaceId: null,
      aiBoards: {},
      sessions: [],
    });
  });

  afterEach(async () => {
    view?.unmount();
    view = null;
    await flush();
    clearAiTabRuns([...aiTabBusyIds, ...aiTabRunControls.keys()]);
    localStorage.clear();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function text(): string {
    return view!.container.textContent ?? "";
  }

  function mountSidebar() {
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
  }

  function bindConversation() {
    useUi.setState({
      aiBoards: {
        [GLOBAL_AI_BOARD_KEY]: {
          tabs: [{ id: "tab-bound", title: "A", conversationId: "conv-a" }],
          activeTabId: "tab-bound",
        },
      },
    });
  }

  async function send(message: string) {
    setInputValue(view!.container.querySelector("textarea")!, message);
    click(view!.container.querySelector('button[title="发送（Enter）"]')!);
    await flush();
  }

  function emit(ev: Record<string, unknown>) {
    const channel = mocks.channels.at(-1);
    if (!channel) throw new Error("no fake channel");
    act(() => channel.onEvent(ev));
  }

  async function advance(ms: number) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });
    await flush();
  }

  function hasButton(label: string): boolean {
    return [...view!.container.querySelectorAll("button")].some((button) => button.textContent?.trim() === label);
  }

  // A run restored after a restart, waiting on a confirmation whose deadline is
  // `expiresAt`; its restored channel never receives the resumed run's events.
  async function restorePendingRun(expiresAt: number): Promise<Record<string, AiRunEventDto[]>> {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);
    const log: Record<string, AiRunEventDto[]> = {
      "job-r": [
        { type: "toolCall", id: "call-1", name: "exec_commands", display: "清理缓存", seq: 1 },
        {
          type: "confirmRequired",
          id: "call-1",
          tool: "exec_commands",
          rendered: "$ rm -rf /tmp/cache",
          confirmationNonce: "nonce-1",
          requestId: "req-1",
          attempt: 1,
          seq: 2,
        },
      ],
    };
    serveEvents(log);
    mocks.messages.mockResolvedValue([messageOf(1, "user", { content: "清理缓存", jobId: "job-r" })]);
    mocks.runs.mockResolvedValue([runOf({ status: "interrupted", finishedAt: undefined })]);
    mocks.hitlSnapshot.mockResolvedValue(snapshotOf(expiresAt));
    mocks.run.mockResolvedValue(runOf({ status: "running" }));
    bindConversation();
    mountSidebar();
    await flushUntil(() => text().includes("需要你确认"));
    expect(useUi.getState().aiBusy).toBe(true);
    return log;
  }

  it("keeps a restored run going after a confirm past its original deadline and shows the final answer", async () => {
    const log = await restorePendingRun(NOW + 20_000);

    clickButton(view!.container, "允许一次");
    await flush();
    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    expect(mocks.confirm.mock.calls[0][1]).toBe(mocks.channels.at(-1));

    // Far past the deadline plus every expiry retry: the run must stay active.
    for (let i = 0; i < 12; i += 1) await advance(5000);
    expect(useUi.getState().aiBusy).toBe(true);
    expect(view!.container.querySelector('button[aria-label="停止"]')).not.toBeNull();

    log["job-r"].push(
      { type: "toolResult", id: "call-1", ok: true, summary: "缓存已清理", seq: 3 },
      { type: "done", answer: "缓存清理完成", seq: 4 },
    );
    mocks.run.mockResolvedValue(runOf({ status: "completed", answer: "缓存清理完成", finishedAt: 4 }));
    await advance(5000);
    await flushUntil(() => text().includes("缓存清理完成"));
    expect(text()).toContain("回答已完成");
    expect(text()).not.toContain("工具执行中");
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("catches up a restored run's progress after a confirm while its channel stays silent", async () => {
    const log = await restorePendingRun(NOW + 3_600_000);

    clickButton(view!.container, "允许一次");
    await flush();
    expect(text()).not.toContain("需要你确认");

    log["job-r"].push(
      { type: "toolResult", id: "call-1", ok: true, summary: "缓存已清理", seq: 3 },
      { type: "toolCall", id: "call-2", name: "exec_commands", display: "检查空间", seq: 4 },
    );
    await advance(5000);
    await flushUntil(() => text().includes("检查空间"));
    expect(text()).toContain("工具执行成功");
    expect(useUi.getState().aiBusy).toBe(true);

    // Events already applied are not applied twice.
    const calls = mocks.runEvents.mock.calls.length;
    await advance(5000);
    expect(mocks.runEvents.mock.calls.length).toBeGreaterThan(calls);
    expect(text().split("检查空间").length - 1).toBe(1);
  });

  it("resolves a pending confirmation another client decided", async () => {
    mountSidebar();
    await flush();
    await send("清理缓存");
    emit({ type: "toolCall", id: "call-1", name: "exec_commands", display: "清理缓存", seq: 1 });
    emit({ type: "confirmRequired", id: "call-1", tool: "exec_commands", rendered: "$ rm x", confirmationNonce: "nonce-1", seq: 2 });
    await flush();
    expect(hasButton("允许一次")).toBe(true);

    emit({ type: "interactionResolved", callId: "call-1", decision: "deny", seq: 3 });
    await flush();
    expect(hasButton("允许一次")).toBe(false);
    expect(text()).toContain("执行命令 · 已拒绝");
    expect(mocks.confirm).not.toHaveBeenCalled();
  });

  it("resolves a pending question when its tool result arrives from a server without interactionResolved", async () => {
    mountSidebar();
    await flush();
    await send("问我");
    emit({ type: "toolCall", id: "call-q", name: "ask_user", display: "提问", seq: 1 });
    emit({ type: "questionRequired", id: "call-q", question: { question: "继续部署吗？", options: ["继续"] }, confirmationNonce: "nonce-q", seq: 2 });
    await flush();
    expect(text()).toContain("等待你回答后继续");

    emit({ type: "toolResult", id: "call-q", ok: true, summary: "继续", text: "继续", seq: 3 });
    await flush();
    expect(text()).not.toContain("等待你回答后继续");
    expect(text()).toContain("提问 · 已在其他地方处理");
  });

  it("closes a question answered elsewhere with a calm notice", async () => {
    mountSidebar();
    await flush();
    await send("问我");
    emit({ type: "questionRequired", id: "call-q", question: { question: "继续部署吗？", options: ["继续"] }, confirmationNonce: "nonce-q", seq: 1 });
    await flush();
    mocks.answer.mockRejectedValueOnce({ code: "bad_param", message: "确认已过期、重复或不属于当前工具调用: 没有待处理的确认请求匹配该调用" });
    clickButton(view!.container, "继续");
    await flush();

    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("已在其他地方处理"));
    expect(mocks.toast).not.toHaveBeenCalledWith("error", expect.anything());
    expect(text()).not.toContain("等待你回答后继续");
    expect(text()).toContain("提问 · 已在其他地方处理");
  });

  it("shows a busy-conversation refusal and keeps the typed message", async () => {
    mountSidebar();
    await flush();
    mocks.chat.mockRejectedValueOnce({ code: "forbidden", message: "该对话仍有任务在运行或等待确认，请在任务结束后再发送" });
    await send("重启服务");
    await flush();

    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("仍有任务在运行"));
    expect(text()).toContain("仍有任务在运行");
    expect(view!.container.querySelector("textarea")!.value).toBe("重启服务");
    expect(useUi.getState().aiBusy).toBe(false);
  });
});
