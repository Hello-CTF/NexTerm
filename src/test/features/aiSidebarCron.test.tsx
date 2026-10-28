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
  const button = view.container.querySelector<HTMLButtonElement>('button[aria-label^="后台任务"]');
  if (!button) throw new Error("background tasks button not found");
  return button;
}

function cronJob() {
  return {
    id: "job-1",
    sessionId: "conv-1",
    name: "每日巡检",
    prompt: "检查磁盘",
    schedule: "0 9 * * *",
    timezone: "Asia/Shanghai",
    enabled: true,
    timeout: 60_000_000_000,
    createdAt: "2026-01-01T00:00:00Z",
    updatedAt: "2026-01-01T00:00:00Z",
    revision: 1,
    nextRunAt: "2026-01-02T09:00:00Z",
    lease: {},
    run: { id: "", scheduledFor: "", startedAt: "", deadline: "" },
  };
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

describe("AiSidebar cron entry point", () => {
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
    await flush();
  });

  afterEach(async () => {
    await flush();
    view?.unmount();
    view = null;
  });

  it("offers no background tasks button before a conversation exists or while no job is scheduled", async () => {
    await flush();
    expect(view!.container.querySelector('button[aria-label^="后台任务"]')).toBeNull();
    expect(mocks.cronList).not.toHaveBeenCalled();
    await send(view!, "Question A");
    emitDone("Answer A");
    await flush();
    expect(mocks.cronList).toHaveBeenCalledWith("conv-1");
    expect(view!.container.querySelector('button[aria-label^="后台任务"]')).toBeNull();
  });

  it("shows the button with a count when a job exists, opens the schedule section for the current conversation, and closes only on request", async () => {
    mocks.cronList.mockResolvedValue([cronJob()]);
    await flush();
    act(() => {
      useUi.getState().ensureWorkspace({
        kind: "session",
        title: "prod-host",
        sessionId: "s1",
        assetId: "asset-1",
      });
    });
    await flush();
    await send(view!, "Question A");
    emitDone("Answer A");
    await flushUntil(() => view!.container.querySelector('button[aria-label^="后台任务"]') !== null);
    expect(cronButton(view!).getAttribute("aria-label")).toBe("后台任务，共 1 项");

    click(cronButton(view!));
    await flushUntil(() => textOf(view!).includes("每日巡检"));
    expect(mocks.cronList).toHaveBeenCalledWith("conv-1");
    expect(textOf(view!)).toContain("定时任务");
    expect(textOf(view!)).toContain("绑定主机：prod-host");
    expect(cronButton(view!).getAttribute("aria-expanded")).toBe("true");

    click(view!.container.querySelector('button[title="关闭"]')!);
    await flush();
    expect(textOf(view!)).not.toContain("每日巡检");
    expect(cronButton(view!).getAttribute("aria-expanded")).toBe("false");
  });

  it("closes the panel when the current conversation is deleted", async () => {
    mocks.cronList.mockResolvedValue([cronJob()]);
    await flush();
    await send(view!, "Question A");
    emitDone("Answer A");
    await flushUntil(() => view!.container.querySelector('button[aria-label^="后台任务"]') !== null);
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "Conversation A", updatedAt: 0 }]);

    click(cronButton(view!));
    await flushUntil(() => textOf(view!).includes("每日巡检"));

    click(view!.container.querySelector('button[title="历史会话"]')!);
    await flushUntil(() => textOf(view!).includes("Conversation A"));
    click(view!.container.querySelector('button[aria-label="删除会话「Conversation A」"]')!);
    await flushUntil(() => mocks.conversationDelete.mock.calls.length > 0);
    expect(mocks.conversationDelete).toHaveBeenCalledWith("conv-1");
    await flush();
    expect(textOf(view!)).not.toContain("每日巡检");
  });

  it("keeps the panel open on its own when the last job disappears and shows an empty state", async () => {
    mocks.cronList.mockResolvedValue([cronJob()]);
    await flush();
    await send(view!, "Question A");
    emitDone("Answer A");
    await flushUntil(() => view!.container.querySelector('button[aria-label^="后台任务"]') !== null);
    click(cronButton(view!));
    await flushUntil(() => textOf(view!).includes("每日巡检"));

    mocks.cronList.mockResolvedValue([]);
    click(view!.container.querySelector('button[aria-label="刷新定时任务"]')!);
    await flushUntil(() => textOf(view!).includes("这个会话还没有定时任务"));
    expect(cronButton(view!).getAttribute("aria-expanded")).toBe("true");
    expect(view!.container.querySelector('section[aria-label="后台任务"]')).not.toBeNull();
  });
});
