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
  hitlSnapshot: vi.fn(),
  hitlEvents: vi.fn(),
  runs: vi.fn(),
  runEvents: vi.fn(),
  getPermission: vi.fn(),
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
  disposeChannel: mocks.dispose,
  onChannelReopen: () => () => undefined,
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, promptText: vi.fn() }));

import { AiSidebar } from "../../features/ai/AiSidebar";
import { GLOBAL_AI_BOARD_KEY, useUi } from "../../app/store";

let convSeq = 0;

function emit(ev: Record<string, unknown>, channelIndex: number) {
  const channel = mocks.channels[channelIndex];
  if (!channel) throw new Error(`no fake channel at ${channelIndex}`);
  act(() => channel.onEvent(ev));
}

function textOf(view: MountedView): string {
  return view.container.textContent ?? "";
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

describe("AiSidebar multiple tabs", () => {
  let view: MountedView | null = null;

  beforeEach(async () => {
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

  afterEach(async () => {
    await flush();
    view?.unmount();
    view = null;
  });

  it("creates, switches, and closes tabs without mixing conversation contexts", async () => {
    await flush();
    expect(stripTabs(view!)).toHaveLength(1);

    // A tab holding a draft is not "empty", so the plus button adds a new one.
    setInputValue(view!.container.querySelector("textarea")!, "草稿");
    await flush();
    click(view!.container.querySelector('button[title="新建会话"]')!);
    await flush();
    expect(stripTabs(view!)).toHaveLength(2);

    await send(view!, "问题甲");
    expect(textOf(view!)).toContain("问题甲");

    click(stripTabs(view!)[0]);
    await flush();
    expect(textOf(view!)).not.toContain("问题甲");

    await send(view!, "问题乙");
    expect(textOf(view!)).toContain("问题乙");

    click(stripTabs(view!)[1]);
    await flush();
    expect(textOf(view!)).toContain("问题甲");
    expect(textOf(view!)).not.toContain("问题乙");

    emit({ type: "done", answer: "Answer A" }, 0);
    await flush();
    click(stripTabs(view!)[1].querySelector("button")!);
    await flush();
    expect(stripTabs(view!)).toHaveLength(1);
    expect(textOf(view!)).toContain("问题乙");
    expect(textOf(view!)).not.toContain("问题甲");
  });

  it("focuses the existing empty tab instead of stacking another blank one", async () => {
    await flush();
    const plus = () => view!.container.querySelector('button[title="新建会话"]')!;
    click(plus());
    await flush();
    click(plus());
    await flush();
    expect(stripTabs(view!)).toHaveLength(1);
    expect(stripTabs(view!)[0].getAttribute("aria-selected")).toBe("true");
  });

  it("shows how to get started when no model is configured", async () => {
    await flush();
    expect(textOf(view!)).toContain("尚未配置模型");
    expect(textOf(view!)).toContain("添加模型");
  });

  it("allows two tabs to run in parallel without one busy tab blocking messages in the other", async () => {
    await flush();
    await send(view!, "Parallel one");
    expect(useUi.getState().aiBusy).toBe(true);

    click(view!.container.querySelector('button[title="新建会话"]')!);
    await flush();
    await send(view!, "Parallel two");
    expect(mocks.chat).toHaveBeenCalledTimes(2);
    expect(useUi.getState().aiBusy).toBe(true);

    emit({ type: "done", answer: "回答一" }, 0);
    await flush();
    expect(useUi.getState().aiBusy).toBe(true);

    emit({ type: "done", answer: "回答二" }, 1);
    await flush();
    expect(useUi.getState().aiBusy).toBe(false);

    click(stripTabs(view!)[0]);
    await flush();
    expect(textOf(view!)).toContain("回答一");
    expect(textOf(view!)).not.toContain("回答二");
  });

  it("asks before closing and waits for the canceled terminal event instead of treating cancel acceptance as completion", async () => {
    await flush();
    await send(view!, "Long-running task");
    mocks.ask.mockResolvedValue(false);
    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("将停止本次回答"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.cancel).not.toHaveBeenCalled();
    expect(stripTabs(view!)).toHaveLength(1);
    expect(useUi.getState().aiBusy).toBe(true);

    mocks.ask.mockResolvedValue(true);
    const before = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id;
    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();
    expect(mocks.cancel).toHaveBeenCalledWith("job-1");
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).toBe(before);
    expect(useUi.getState().aiBusy).toBe(true);
    expect(textOf(view!)).toContain("正在停止");

    emit({ type: "canceled", message: "已停止" }, 0);
    await flush();
    const board = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY];
    expect(board.tabs).toHaveLength(1);
    expect(board.tabs[0].id).not.toBe(before);
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("keeps the tab with a retryable unconfirmed state when cancel is rejected", async () => {
    await flush();
    await send(view!, "Long-running task");
    mocks.cancel.mockRejectedValueOnce(new Error("offline"));
    const before = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id;

    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();

    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).toBe(before);
    expect(useUi.getState().aiBusy).toBe(true);
    expect(textOf(view!)).toContain("停止未确认");
    clickButton(view!.container, "查看任务");
    await flush();
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).toBe(before);

    clickButton(view!.container, "重试");
    await flush();
    expect(mocks.cancel).toHaveBeenCalledTimes(2);
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).toBe(before);
    emit({ type: "canceled", message: "已停止" }, 0);
    await flush();
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("records cancel intent while spawn is pending and sends it after the job is bound", async () => {
    await flush();
    const pending = deferred<{ jobId: string; conversationId: string }>();
    mocks.chat.mockReturnValueOnce(pending.promise);
    await send(view!, "Pending spawn");
    const before = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id;

    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();
    expect(mocks.cancel).not.toHaveBeenCalled();
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).toBe(before);
    expect(textOf(view!)).toContain("正在停止");

    pending.resolve({ jobId: "job-pending", conversationId: "conv-pending" });
    await flushUntil(() => mocks.cancel.mock.calls.length > 0);
    expect(mocks.cancel).toHaveBeenCalledWith("job-pending");
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).toBe(before);

    emit({ type: "canceled", message: "已停止" }, 0);
    await flush();
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).not.toBe(before);
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("deduplicates repeated close requests and lets a terminal event win over a pending cancel response", async () => {
    await flush();
    await send(view!, "Long-running task");
    const cancelPending = deferred<void>();
    mocks.cancel.mockReturnValueOnce(cancelPending.promise);
    const closeButton = stripTabs(view!)[0].querySelector("button")!;

    click(closeButton);
    click(closeButton);
    await flush();

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.cancel).toHaveBeenCalledTimes(1);
    emit({ type: "canceled", message: "已停止" }, 0);
    await flush();
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs).toHaveLength(1);
    expect(textOf(view!)).not.toContain("Long-running task");
    cancelPending.resolve();
    await flush();
  });

  it("closes without canceling when the run completes while the close confirmation is open", async () => {
    await flush();
    await send(view!, "Race completion");
    const confirmation = deferred<boolean>();
    mocks.ask.mockReturnValueOnce(confirmation.promise);
    const before = useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id;
    click(stripTabs(view!)[0].querySelector("button")!);
    await flush();

    emit({ type: "done", answer: "Completed first" }, 0);
    confirmation.resolve(true);
    await flush();

    expect(mocks.cancel).not.toHaveBeenCalled();
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs[0].id).not.toBe(before);
    expect(useUi.getState().aiBusy).toBe(false);
  });

  it("uses the live event target for a created terminal without stealing focus from another AI tab", async () => {
    await flush();
    act(() => {
      useUi.setState({
        sessions: [
          { id: "s-event", assetId: "a-event", name: "event-host", kind: "ssh", status: "connected", tabs: [], createdAt: 1 },
        ],
      });
    });
    await send(view!, "Open terminal");
    await act(async () => {
      useUi.getState().addAiTab(GLOBAL_AI_BOARD_KEY);
    });
    await flush();
    const previousWorkspaceId = useUi.getState().activeWorkspaceId;

    emit({ type: "toolCall", id: "call-bg", name: "terminal_create", display: "terminal_create" }, 0);
    emit({
      type: "toolResult",
      id: "call-bg",
      ok: true,
      summary: "created",
      terminal: { tabId: "tab-bg", sessionId: "s-event", title: "事件终端" },
    }, 0);
    await flush();

    const tabs = useUi.getState().workspaces.flatMap((workspace) => workspace.panes).flatMap((pane) => pane.tabs);
    expect(tabs.find((tab) => tab.tabId === "tab-bg")).toMatchObject({ sessionId: "s-event", title: "事件终端" });
    expect(useUi.getState().activeWorkspaceId).toBe(previousWorkspaceId);
  });
});
