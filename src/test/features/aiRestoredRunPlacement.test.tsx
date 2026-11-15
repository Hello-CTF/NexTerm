/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";
import type { AiRunDto, AiRunEventDto, MessageDto } from "../../ipc/types";

const mocks = vi.hoisted(() => ({
  chat: vi.fn(),
  cancel: vi.fn(),
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
import { useUi } from "../../app/store";

function runOf(overrides: Partial<AiRunDto>): AiRunDto {
  return {
    id: "job-a",
    conversationId: "conv-a",
    status: "completed",
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

// serveEvents answers aiApi.runEvents from a per-job event log the test can extend.
function serveEvents(log: Record<string, AiRunEventDto[]>) {
  mocks.runEvents.mockImplementation((jobId: string, afterSeq: number) =>
    Promise.resolve((log[jobId] ?? []).filter((event) => (event.seq as number) > afterSeq)),
  );
}

describe("AiSidebar restored run placement", () => {
  let view: MountedView | null = null;
  let boardA = "";
  let boardB = "";

  beforeEach(() => {
    vi.clearAllMocks();
    clearAiTabRuns([...aiTabBusyIds, ...aiTabRunControls.keys()]);
    mocks.channels.length = 0;
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      setTimeout(cb, 0);
      return 1;
    });
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
    mocks.chat.mockResolvedValue({ jobId: "job-a", conversationId: "conv-a" });
    mocks.cancel.mockResolvedValue(undefined);
    mocks.hitlSnapshot.mockResolvedValue(null);
    mocks.hitlEvents.mockResolvedValue([]);
    mocks.runs.mockResolvedValue([]);
    mocks.run.mockResolvedValue(runOf({ status: "running" }));
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
      sessions: [
        { id: "s1", assetId: "asset-1", name: "host-1", kind: "ssh", status: "connected", tabs: [], createdAt: 1 },
      ],
    });
    boardA = useUi.getState().ensureWorkspace({ kind: "session", sessionId: "s1", title: "Workspace A" });
    boardB = useUi.getState().ensureWorkspace({ kind: "session", sessionId: "s2", title: "Workspace B" });
    useUi.setState({
      activeWorkspaceId: boardA,
      aiBoards: {
        [boardA]: { tabs: [{ id: "tab-a", title: "新会话" }], activeTabId: "tab-a" },
        [boardB]: { tabs: [{ id: "tab-b", title: "新会话" }], activeTabId: "tab-b" },
      },
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

  function mountSidebar() {
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
  }

  function text(): string {
    return view!.container.textContent ?? "";
  }

  async function switchBoard(board: string) {
    act(() => useUi.getState().setActiveWorkspace(board));
    await flush();
  }

  async function send(message: string) {
    setInputValue(view!.container.querySelector("textarea")!, message);
    click(view!.container.querySelector('button[title="发送 (Enter)"]')!);
    await flush();
  }

  function expectInOrder(parts: string[]) {
    const content = text();
    const positions = parts.map((part) => content.indexOf(part));
    for (const [index, position] of positions.entries()) {
      if (position < 0) throw new Error(`missing "${parts[index]}" in: ${content}`);
    }
    expect(positions).toEqual([...positions].sort((a, b) => a - b));
  }

  function toastTexts(): string[] {
    return mocks.toast.mock.calls.map((call) => String(call[1]));
  }

  it("retries a run that failed while its board was away with the original message", async () => {
    mountSidebar();
    await flush();
    await send("部署服务");
    act(() => mocks.channels[0].onEvent({ type: "toolCall", id: "call-1", name: "exec_commands", display: "执行部署", seq: 1 }));
    await flush();

    await switchBoard(boardB);
    mocks.messages.mockResolvedValue([
      messageOf(1, "user", { content: "部署服务", imageCount: 1, jobId: "job-a" }),
    ]);
    mocks.runs.mockResolvedValue([runOf({ status: "failed", finishedAt: 2 })]);
    serveEvents({
      "job-a": [
        { type: "toolCall", id: "call-1", name: "exec_commands", display: "执行部署", seq: 1 },
        { type: "toolResult", id: "call-1", ok: true, summary: "部署脚本已执行", seq: 2 },
        { type: "error", message: "模型服务暂时不可用", retryable: true, seq: 3 },
      ],
    });
    await switchBoard(boardA);
    await flushUntil(() => text().includes("模型服务暂时不可用"));

    expectInOrder(["部署服务", "执行部署", "工具执行成功", "模型服务暂时不可用"]);
    expect(text()).not.toContain("工具执行中");

    mocks.chat.mockResolvedValue({ jobId: "job-b", conversationId: "conv-a" });
    clickButton(view!.container.querySelector('[role="alert"]')!, "重试");
    await flush();

    expect(mocks.chat).toHaveBeenCalledTimes(2);
    expect(mocks.chat).toHaveBeenLastCalledWith(
      expect.objectContaining({ conversationId: "conv-a", message: "部署服务" }),
    );
    expect(toastTexts()).not.toContain("未找到原消息内容，请手动重新发送");
    expect(toastTexts()).toContain("原消息的 1 张图片无法自动恢复，如需图片请重新添加后发送");
  });

  it("keeps replayed runs next to their messages around a completed run", async () => {
    useUi.setState({
      aiBoards: {
        ...useUi.getState().aiBoards,
        [boardA]: { tabs: [{ id: "tab-a", title: "A", conversationId: "conv-a" }], activeTabId: "tab-a" },
      },
    });
    mocks.messages.mockResolvedValue([
      messageOf(1, "user", { content: "问题一", jobId: "job-1" }),
      messageOf(2, "user", { content: "补充一", jobId: "job-1", steered: true }),
      messageOf(3, "toolCall", { type: "toolCall", jobId: "job-1", id: "c1", name: "read_file" }),
      messageOf(4, "user", { content: "问题二", jobId: "job-2" }),
      messageOf(5, "assistant", { content: "回答二" }),
      messageOf(6, "user", { content: "问题三", jobId: "job-3" }),
    ]);
    // Same creation time for both failed runs: the id decides the replay order.
    mocks.runs.mockResolvedValue([
      runOf({ id: "job-3", status: "failed", createdAt: 1000, finishedAt: 3 }),
      runOf({ id: "job-2", status: "completed", createdAt: 900, finishedAt: 2 }),
      runOf({ id: "job-1", status: "failed", createdAt: 1000, finishedAt: 1 }),
    ]);
    serveEvents({
      "job-1": [
        { type: "toolCall", id: "c1", name: "read_file", display: "读取一", seq: 1 },
        { type: "error", message: "错误一", retryable: true, seq: 2 },
      ],
      "job-3": [
        { type: "toolCall", id: "c3", name: "read_file", display: "读取三", seq: 1 },
        { type: "error", message: "错误三", retryable: true, seq: 2 },
      ],
    });
    mountSidebar();
    await flushUntil(() => text().includes("错误三") && text().includes("错误一"));

    expectInOrder(["问题一", "补充一", "读取一", "错误一", "问题二", "回答二", "问题三", "读取三", "错误三"]);
  });

  it("shows a tool result emitted while the board was away once the adopted run reports it", async () => {
    vi.useFakeTimers();
    mountSidebar();
    await flush();
    await send("等服务就绪");
    act(() => mocks.channels[0].onEvent({ type: "toolCall", id: "call-w", name: "wait_for", display: "等待就绪", seq: 1 }));
    await flush();

    await switchBoard(boardB);
    const log: Record<string, AiRunEventDto[]> = {
      "job-a": [{ type: "toolCall", id: "call-w", name: "wait_for", display: "等待就绪", seq: 1 }],
    };
    serveEvents(log);
    mocks.messages.mockResolvedValue([messageOf(1, "user", { content: "等服务就绪", jobId: "job-a" })]);
    mocks.runs.mockResolvedValue([runOf({ status: "running" })]);
    await switchBoard(boardA);
    await flushUntil(() => text().includes("等待就绪"));
    expect(text()).toContain("工具执行中");
    expect(view!.container.querySelector('button[aria-label="停止"]')).not.toBeNull();

    log["job-a"].push(
      { type: "toolResult", id: "call-w", ok: true, summary: "服务已就绪", seq: 2 },
      { type: "toolCall", id: "call-x", name: "exec_commands", display: "检查端口", seq: 3 },
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    await flush();
    expectInOrder(["等服务就绪", "等待就绪", "工具执行成功", "检查端口", "工具执行中"]);

    log["job-a"].push(
      { type: "toolResult", id: "call-x", ok: true, summary: "端口已开放", seq: 4 },
      { type: "done", answer: "服务已启动", seq: 5 },
    );
    mocks.run.mockResolvedValue(runOf({ status: "completed", finishedAt: 5 }));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    await flush();
    expect(text()).toContain("服务已启动");
    expect(text()).not.toContain("工具执行中");
    expect(useUi.getState().aiBusy).toBe(false);
    expectInOrder(["等服务就绪", "等待就绪", "检查端口", "服务已启动"]);
  });
});
