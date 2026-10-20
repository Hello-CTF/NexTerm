/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, StrictMode } from "react";
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
import type { AiRunDto, AiRunEventDto, MessageDto } from "../../ipc/types";
import { RUN_RESTORE_LIMIT } from "../../features/ai/runRestore";

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
  onChannelReopen: () => () => undefined,
}));

vi.mock("../../ui/dialogs", () => ({
  ask: vi.fn(),
  askChoice: vi.fn(),
  promptText: vi.fn(),
}));

import { AiSidebar } from "../../features/ai/AiSidebar";
import { aiTabBusyIds, aiTabRunControls, clearAiTabRuns } from "../../features/ai/runOwnership";
import { useUi } from "../../app/store";

function runOf(overrides: Partial<AiRunDto>): AiRunDto {
  return {
    id: "run-1",
    conversationId: "conv-old",
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
    finishedAt: 1,
    ...overrides,
  };
}

function messageOf(index: number, conversationId = "conv-page"): MessageDto {
  return {
    id: `message-${index}`,
    conversationId,
    role: "user",
    content: { role: "user", content: `消息 ${index}` },
    tokensIn: null,
    tokensOut: null,
    createdAt: index,
  };
}

function statusPage(): AiRunEventDto[] {
  return Array.from({ length: 200 }, (_, index) => ({
    type: "status",
    phase: "thinking",
    seq: index + 1,
  }));
}

function pendingSnapshotOf(runId: string) {
  return {
    runId,
    checkpointId: runId,
    status: "interrupted",
    attempt: 1,
    seq: 1,
    pending: [{}],
  };
}

describe("AiSidebar board lifecycle", () => {
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
    mocks.confirm.mockResolvedValue(undefined);
    mocks.answer.mockResolvedValue(undefined);
    mocks.hitlSnapshot.mockResolvedValue({
      runId: "run-needed",
      checkpointId: "run-needed",
      status: "interrupted",
      attempt: 1,
      seq: 1,
      pending: [],
    });
    mocks.hitlEvents.mockResolvedValue([]);
    mocks.runs.mockResolvedValue([]);
    mocks.runEvents.mockResolvedValue([]);
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.conversationList.mockResolvedValue([
      { id: "conv-a", title: "A", createdAt: 0, updatedAt: 0, scope: {} },
      { id: "conv-old", title: "Old", createdAt: 0, updatedAt: 0, scope: {} },
      { id: "conv-page", title: "Paged", createdAt: 0, updatedAt: 0, scope: {} },
    ]);
    mocks.conversationDelete.mockResolvedValue(undefined);
    mocks.messages.mockResolvedValue([]);
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    mocks.takeoverEnter.mockResolvedValue({ token: "token-1" });
    mocks.takeoverRun.mockResolvedValue({ jobId: "job-takeover", token: "token-2" });
    mocks.takeoverExit.mockResolvedValue(undefined);
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
    vi.unstubAllGlobals();
  });

  function mountSidebar() {
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
  }

  function bindConversation(id: string) {
    useUi.setState({
      aiBoards: {
        ...useUi.getState().aiBoards,
        [boardA]: { tabs: [{ id: "tab-a", title: "Old", conversationId: id }], activeTabId: "tab-a" },
      },
    });
  }

  async function switchBoard(board: string) {
    act(() => useUi.getState().setActiveWorkspace(board));
    await flush();
  }

  async function send(text: string) {
    setInputValue(view!.container.querySelector("textarea")!, text);
    click(view!.container.querySelector('button[title="发送 (Enter)"]')!);
    await flush();
  }

  function emit(channel: { onEvent: (ev: Record<string, unknown>) => void }, ev: Record<string, unknown>) {
    act(() => channel.onEvent(ev));
  }

  function disposeCalls(channel: unknown): number {
    return mocks.dispose.mock.calls.filter(([candidate]) => candidate === channel).length;
  }

  it("detaches a streaming run on board switch without canceling and isolates late events", async () => {
    mountSidebar();
    await flush();
    await send("Live question");
    const channel = mocks.channels[0];
    emit(channel, { type: "delta", text: "OLD-PARTIAL", seq: 1 });
    await flush();
    expect(view!.container.textContent).toContain("OLD-PARTIAL");
    const messagesBeforeReturn = mocks.messages.mock.calls.length;

    await switchBoard(boardB);
    expect(disposeCalls(channel)).toBe(1);
    expect(mocks.cancel).not.toHaveBeenCalled();

    emit(channel, { type: "toolCall", id: "call-old", name: "terminal_create", display: "terminal_create", seq: 2 });
    emit(channel, {
      type: "toolResult",
      id: "call-old",
      ok: true,
      terminal: { tabId: "tab-old", sessionId: "s1", title: "OLD-TERMINAL" },
      seq: 3,
    });
    emit(channel, { type: "done", answer: "OLD-DONE", seq: 4 });
    await flush();

    expect(view!.container.textContent).not.toContain("OLD-PARTIAL");
    expect(view!.container.textContent).not.toContain("OLD-DONE");
    expect(useUi.getState().aiBoards[boardB].tabs[0].conversationId).toBeUndefined();
    const terminalIds = useUi.getState().workspaces.flatMap((workspace) =>
      workspace.panes.flatMap((pane) => pane.tabs.map((tab) => tab.tabId)),
    );
    expect(terminalIds).not.toContain("tab-old");
    expect(disposeCalls(channel)).toBe(1);
    expect(mocks.cancel).not.toHaveBeenCalled();

    await switchBoard(boardA);
    await flushUntil(() => mocks.messages.mock.calls.length > messagesBeforeReturn);
    expect(mocks.messages).toHaveBeenCalledWith("conv-a");
  });

  it("closes a live channel only once across terminal, late events and unmount", async () => {
    mountSidebar();
    await flush();
    await send("Finish once");
    const channel = mocks.channels[0];

    emit(channel, { type: "done", answer: "Done" });
    await flush();
    expect(disposeCalls(channel)).toBe(1);
    emit(channel, { type: "done", answer: "Done again" });
    view!.unmount();
    view = null;
    await flush();

    expect(disposeCalls(channel)).toBe(1);
    expect(mocks.cancel).not.toHaveBeenCalled();
  });

  it("invalidates an unfinished restore when the board changes", async () => {
    bindConversation("conv-old");
    const pendingEvents = deferred<AiRunEventDto[]>();
    mocks.messages.mockResolvedValue([messageOf(1, "conv-old")]);
    mocks.runs.mockResolvedValue([
      runOf({ id: "run-needed", status: "interrupted", finishedAt: undefined }),
    ]);
    mocks.runEvents.mockReturnValue(pendingEvents.promise);
    mountSidebar();
    await flushUntil(() => mocks.runEvents.mock.calls.length === 1);

    await switchBoard(boardB);
    pendingEvents.resolve([{ type: "delta", text: "OLD-RESTORE", seq: 1 }]);
    await flush();

    expect(view!.container.textContent).not.toContain("OLD-RESTORE");
    expect(view!.container.textContent).toContain("新建会话");
    expect(useUi.getState().aiBoards[boardB].tabs[0].conversationId).toBeUndefined();
    expect(mocks.hitlSnapshot).not.toHaveBeenCalled();
    expect(mocks.runEvents).toHaveBeenCalledTimes(1);
  });

  it("bounds run restoration and fetches events only for runs that need replay", async () => {
    bindConversation("conv-old");
    const historical = Array.from({ length: 500 }, (_, index) =>
      runOf({ id: `run-complete-${index}`, createdAt: index + 1 }),
    );
    mocks.runs.mockResolvedValue([
      runOf({ id: "run-needed", status: "interrupted", createdAt: 1000, finishedAt: undefined }),
      ...historical,
    ]);
    mountSidebar();
    await flushUntil(() => mocks.hitlSnapshot.mock.calls.length === 1);

    expect(mocks.runs).toHaveBeenCalledWith("conv-old", RUN_RESTORE_LIMIT);
    expect(mocks.runEvents).toHaveBeenCalledTimes(1);
    expect(mocks.runEvents).toHaveBeenCalledWith("run-needed", 0, 200);
    expect(mocks.hitlSnapshot).toHaveBeenCalledWith("run-needed");
  });

  it("restores the latest message page and loads older pages on demand", async () => {
    bindConversation("conv-page");
    mocks.messages.mockResolvedValue(Array.from({ length: 250 }, (_, index) => messageOf(index)));
    mountSidebar();
    await flushUntil(() => (view!.container.textContent ?? "").includes("消息 150"));

    expect(view!.container.textContent).toContain("消息 150");
    expect(view!.container.textContent).not.toContain("消息 149");
    clickButton(view!.container, "加载更早消息");
    await flush();
    expect(view!.container.textContent).toContain("消息 50");
    expect(view!.container.textContent).not.toContain("消息 49");

    clickButton(view!.container, "加载更早消息");
    await flush();
    expect(view!.container.textContent).toContain("消息 0");
    expect([...view!.container.querySelectorAll("button")].some((button) => button.textContent === "加载更早消息")).toBe(false);
    expect(mocks.messages).toHaveBeenCalledTimes(1);
  });

  it("invalidates the next restored page when a live run starts after the initial history reset", async () => {
    bindConversation("conv-old");
    const pendingEvents = deferred<AiRunEventDto[]>();
    mocks.messages.mockResolvedValue([messageOf(1, "conv-old")]);
    mocks.runs.mockResolvedValue([
      runOf({ id: "run-needed", status: "interrupted", finishedAt: undefined }),
    ]);
    mocks.chat.mockResolvedValue({ jobId: "job-live", conversationId: "conv-old" });
    mocks.hitlSnapshot.mockResolvedValue(pendingSnapshotOf("run-needed"));
    mocks.runEvents
      .mockResolvedValueOnce(statusPage())
      .mockReturnValueOnce(pendingEvents.promise);
    mountSidebar();
    await flushUntil(() => mocks.runEvents.mock.calls.length === 2);

    await send("NEW-LIVE");
    const liveChannel = mocks.channels[0];
    emit(liveChannel, {
      type: "usage",
      promptTokens: 100,
      completionTokens: 10,
      cachedTokens: 0,
      contextWindow: 1000,
    });
    pendingEvents.resolve([
      { type: "delta", text: "OLD-TAIL", seq: 201 },
      { type: "usage", seq: 202, promptTokens: 9000, completionTokens: 900, cachedTokens: 0, contextWindow: 10000 },
    ]);
    await flush();

    const text = view!.container.textContent ?? "";
    expect(text).toContain("NEW-LIVE");
    expect(text).not.toContain("OLD-TAIL");
    expect(view!.container.querySelector('span[role="img"]')?.getAttribute("aria-label")).toBe(
      "上下文占用 10%（100 / 1.0k），缓存命中 0%",
    );
    expect(mocks.channels).toHaveLength(1);
    expect(mocks.hitlSnapshot).not.toHaveBeenCalled();
  });

  it("invalidates the next restored page when a live run starts after openConversation resets history", async () => {
    const pendingEvents = deferred<AiRunEventDto[]>();
    mocks.conversationList.mockResolvedValue([
      { id: "conv-open", title: "Open", createdAt: 0, updatedAt: 0, scope: {} },
    ]);
    mocks.messages.mockResolvedValue([messageOf(1, "conv-open")]);
    mocks.runs.mockResolvedValue([
      runOf({ id: "run-needed", conversationId: "conv-open", status: "interrupted", finishedAt: undefined }),
    ]);
    mocks.chat.mockResolvedValue({ jobId: "job-live", conversationId: "conv-open" });
    mocks.hitlSnapshot.mockResolvedValue(pendingSnapshotOf("run-needed"));
    mocks.runEvents
      .mockResolvedValueOnce(statusPage())
      .mockReturnValueOnce(pendingEvents.promise);
    mountSidebar();
    await flush();
    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flush();
    clickButton(view!.container, "Open");
    await flushUntil(() => mocks.runEvents.mock.calls.length === 2);

    await send("NEW-OPEN");
    const liveChannel = mocks.channels[0];
    emit(liveChannel, {
      type: "usage",
      promptTokens: 100,
      completionTokens: 10,
      cachedTokens: 0,
      contextWindow: 1000,
    });
    pendingEvents.resolve([
      { type: "delta", text: "OLD-OPEN-TAIL", seq: 201 },
      { type: "usage", seq: 202, promptTokens: 9000, completionTokens: 900, cachedTokens: 0, contextWindow: 10000 },
    ]);
    await flush();

    const text = view!.container.textContent ?? "";
    expect(text).toContain("NEW-OPEN");
    expect(text).not.toContain("OLD-OPEN-TAIL");
    expect(view!.container.querySelector('span[role="img"]')?.getAttribute("aria-label")).toBe(
      "上下文占用 10%（100 / 1.0k），缓存命中 0%",
    );
    expect(mocks.channels).toHaveLength(1);
    expect(mocks.hitlSnapshot).not.toHaveBeenCalled();
  });

  it("keeps conversation restore usable across StrictMode effect replay", async () => {
    bindConversation("conv-page");
    mocks.messages.mockResolvedValue([messageOf(1)]);
    view = mount(
      createElement(
        StrictMode,
        null,
        createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }),
      ),
    );

    await flushUntil(() => (view!.container.textContent ?? "").includes("消息 1"));
    expect(view!.container.textContent).toContain("消息 1");
  });
});
