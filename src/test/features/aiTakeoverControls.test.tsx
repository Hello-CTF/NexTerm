/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, Fragment } from "react";
import {
  click,
  clickButton,
  flush,
  flushUntil,
  mount,
  setInputValue,
  type MountedView,
} from "./reactTestUtils";
import { takeoverPaused, withTakeoverPaused } from "../../app/TakeoverBanner";

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
  askChoice: vi.fn(),
  takeoverEnter: vi.fn(),
  takeoverRun: vi.fn(),
  takeoverConfirm: vi.fn(),
  takeoverCancel: vi.fn(),
  takeoverPause: vi.fn(),
  takeoverResume: vi.fn(),
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
    takeoverConfirm: mocks.takeoverConfirm,
    takeoverCancel: mocks.takeoverCancel,
    takeoverPause: mocks.takeoverPause,
    takeoverResume: mocks.takeoverResume,
    takeoverExit: mocks.takeoverExit,
  },
  modelApi: { overview: mocks.overview, activate: vi.fn() },
  assetApi: {},
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
  askChoice: mocks.askChoice,
  promptText: mocks.promptText,
}));

import { AiSidebar } from "../../features/ai/AiSidebar";
import { TakeoverBanner } from "../../app/TakeoverBanner";
import { aiTabBusyIds, aiTabRunControls, clearAiTabRuns } from "../../features/ai/runOwnership";
import { useUi } from "../../app/store";

describe("AiSidebar takeover controls", () => {
  let view: MountedView | null = null;

  beforeEach(async () => {
    vi.clearAllMocks();
    clearAiTabRuns([...aiTabBusyIds, ...aiTabRunControls.keys()]);
    mocks.channels.length = 0;
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      setTimeout(cb, 0);
      return 1;
    });
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
    mocks.chat.mockResolvedValue({ jobId: "job-chat", conversationId: "conv-chat" });
    mocks.cancel.mockResolvedValue(undefined);
    mocks.confirm.mockResolvedValue(undefined);
    mocks.answer.mockResolvedValue(undefined);
    mocks.hitlSnapshot.mockResolvedValue({ runId: "job-chat", checkpointId: "job-chat", status: "running", attempt: 1, seq: 0, pending: [] });
    mocks.hitlEvents.mockResolvedValue([]);
    mocks.runs.mockResolvedValue([]);
    mocks.runEvents.mockResolvedValue([]);
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.conversationList.mockResolvedValue([{ id: "conv-chat", title: "Chat", createdAt: 0, updatedAt: 0, scope: {} }]);
    mocks.conversationDelete.mockResolvedValue(undefined);
    mocks.messages.mockResolvedValue([]);
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    mocks.promptText.mockResolvedValue("Install nginx");
    mocks.askChoice.mockResolvedValue("allow");
    mocks.takeoverEnter.mockResolvedValue({ token: "tok-1" });
    mocks.takeoverRun.mockResolvedValue({ jobId: "job-t", token: "tok-2" });
    mocks.takeoverConfirm.mockResolvedValue(undefined);
    mocks.takeoverCancel.mockResolvedValue(undefined);
    mocks.takeoverPause.mockResolvedValue(undefined);
    mocks.takeoverResume.mockResolvedValue({ jobId: "job-t", token: "tok-2" });
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
      sessions: [],
    });
    view = mount(
      createElement(
        Fragment,
        null,
        createElement(TakeoverBanner),
        createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }),
      ),
    );
    await flush();
  });

  afterEach(async () => {
    await flush();
    view?.unmount();
    view = null;
    clearAiTabRuns([...aiTabBusyIds, ...aiTabRunControls.keys()]);
    useUi.getState().setTakeover(null);
    localStorage.clear();
    vi.unstubAllGlobals();
  });

  function emit(channel: { onEvent: (ev: Record<string, unknown>) => void }, ev: Record<string, unknown>) {
    act(() => channel.onEvent(ev));
  }

  function disposeCalls(channel: unknown): number {
    return mocks.dispose.mock.calls.filter(([candidate]) => candidate === channel).length;
  }

  async function startTakeover() {
    click(view!.container.querySelector('button[aria-label="更多"]')!);
    click(view!.container.querySelector('button[title^="终端接管（实验性功能）"]')!);
    await flush();
    return mocks.channels.at(-1)!;
  }

  async function sendChat() {
    setInputValue(view!.container.querySelector("textarea")!, "Chat question");
    click(view!.container.querySelector('button[title="发送 (Enter)"]')!);
    await flush();
    return mocks.channels[0];
  }

  it("dispatches chat and takeover confirm/cancel by run kind", async () => {
    const chatChannel = await sendChat();
    emit(chatChannel, {
      type: "confirmRequired",
      id: "call-chat",
      tool: "exec_commands",
      rendered: "$ ls",
      confirmationNonce: "nonce-chat",
    });
    await flush();
    clickButton(view!.container, "允许一次");
    await flush();
    expect(mocks.confirm).toHaveBeenCalledWith({
      jobId: "job-chat",
      callId: "call-chat",
      nonce: "nonce-chat",
      decision: "allow",
    });
    expect(mocks.takeoverConfirm).not.toHaveBeenCalled();

    click(view!.container.querySelector('button[title^="停止当前任务"]')!);
    await flushUntil(() => mocks.cancel.mock.calls.length === 1);
    expect(mocks.takeoverCancel).not.toHaveBeenCalled();
    emit(chatChannel, { type: "canceled", message: "已停止本轮" });
    await flush();

    const takeoverChannel = await startTakeover();
    emit(takeoverChannel, {
      type: "confirmRequired",
      id: "call-takeover",
      tool: "exec_commands",
      rendered: "$ nginx",
      confirmationNonce: "nonce-takeover",
    });
    await flush();
    clickButton(view!.container, "允许一次");
    await flush();
    expect(mocks.takeoverConfirm).toHaveBeenCalledWith({
      jobId: "job-t",
      callId: "call-takeover",
      nonce: "nonce-takeover",
      decision: "allow",
    });
    expect(mocks.confirm).toHaveBeenCalledTimes(1);

    click(view!.container.querySelector('button[title^="停止当前任务"]')!);
    await flushUntil(() => mocks.takeoverCancel.mock.calls.length === 1);
    expect(mocks.takeoverCancel).toHaveBeenCalledWith("job-t");
    expect(mocks.cancel).toHaveBeenCalledTimes(1);
    emit(takeoverChannel, { type: "canceled", message: "已停止接管" });
    await flush();
  });

  it("routes a permanent takeover confirmation only through takeoverConfirm", async () => {
    const takeoverChannel = await startTakeover();
    emit(takeoverChannel, {
      type: "confirmRequired",
      id: "call-persistent",
      tool: "exec_commands",
      rendered: "$ nginx",
      risk: "needs_confirm",
      confirmationNonce: "nonce-persistent",
    });
    await flush();

    clickButton(view!.container, "永久允许");
    await flush();

    expect(mocks.takeoverConfirm).toHaveBeenCalledWith({
      jobId: "job-t",
      callId: "call-persistent",
      nonce: "nonce-persistent",
      decision: "allow_persistent",
    });
    expect(mocks.takeoverConfirm).toHaveBeenCalledTimes(1);
    expect(mocks.confirm).not.toHaveBeenCalled();
  });

  it("shows paused state and resumes the same job on a rebound channel with continuous sequence", async () => {
    const original = await startTakeover();
    clickButton(view!.container, "暂停");
    await flush();
    expect(mocks.takeoverPause).toHaveBeenCalledWith("t1", "tok-2");

    emit(original, { type: "paused", reason: "用户暂停", seq: 1 });
    await flush();
    expect(view!.container.textContent).toContain("接管已暂停");
    expect(view!.container.textContent).toContain("AI 接管已暂停");
    expect(takeoverPaused(useUi.getState().takeover!)).toBe(true);
    expect(JSON.parse(localStorage.getItem("nexterm.takeover.v1")!).paused).toBe(true);

    clickButton(view!.container, "继续");
    await flush();
    const resumed = mocks.channels[1];
    expect(mocks.takeoverResume).toHaveBeenCalledWith({
      tabId: "t1",
      token: "tok-2",
      jobId: "job-t",
      channel: resumed,
    });
    expect(disposeCalls(original)).toBe(1);

    emit(original, { type: "paused", reason: "迟到暂停", seq: 2 });
    await flush();
    expect(takeoverPaused(useUi.getState().takeover!)).toBe(false);
    expect(view!.container.textContent).not.toContain("迟到暂停");
    emit(resumed, { type: "delta", text: "RESUMED-OUTPUT", seq: 2 });
    emit(resumed, { type: "done", answer: "完成", seq: 3 });
    await flush();

    expect(view!.container.textContent).toContain("RESUMED-OUTPUT");
    expect(useUi.getState().takeover).toBeNull();
    expect(localStorage.getItem("nexterm.takeover.v1")).toBeNull();
    expect(mocks.runEvents).not.toHaveBeenCalled();
  });

  it("keeps a failed resume paused and retries with the same job and a fresh channel", async () => {
    const original = await startTakeover();
    emit(original, { type: "paused", reason: "用户暂停", seq: 1 });
    await flush();
    mocks.takeoverResume
      .mockRejectedValueOnce(new Error("factory failed"))
      .mockResolvedValueOnce({ jobId: "job-t", token: "tok-2" });

    clickButton(view!.container, "继续");
    await flush();
    await flushUntil(() => mocks.toast.mock.calls.some(([kind, text]) => kind === "error" && String(text).includes("factory failed")));
    expect(takeoverPaused(useUi.getState().takeover!)).toBe(true);
    expect(disposeCalls(original)).toBe(0);
    expect(disposeCalls(mocks.channels[1])).toBe(1);

    clickButton(view!.container, "继续");
    await flush();
    const resumed = mocks.channels[2];
    expect(mocks.takeoverResume).toHaveBeenLastCalledWith({
      tabId: "t1",
      token: "tok-2",
      jobId: "job-t",
      channel: resumed,
    });
    expect(disposeCalls(original)).toBe(1);
    expect(takeoverPaused(useUi.getState().takeover!)).toBe(false);

    emit(resumed, { type: "delta", text: "RETRY-OUTPUT", seq: 2 });
    emit(resumed, { type: "done", answer: "完成", seq: 3 });
    await flush();
    expect(view!.container.textContent).toContain("RETRY-OUTPUT");
    expect(useUi.getState().takeover).toBeNull();
  });

  it("rebuilds a paused takeover binding and resumes without starting a new job", async () => {
    view!.unmount();
    useUi.getState().setTakeover(
      withTakeoverPaused(
        {
          tabId: "t1",
          jobId: "job-t",
          token: "tok-2",
          task: "Install nginx",
          allowWrite: true,
          startedAt: Date.now(),
        },
        true,
      ),
    );
    view = mount(
      createElement(
        Fragment,
        null,
        createElement(TakeoverBanner),
        createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }),
      ),
    );
    await flush();

    clickButton(view.container, "继续");
    await flush();
    expect(mocks.takeoverRun).not.toHaveBeenCalled();
    expect(mocks.takeoverResume).toHaveBeenCalledWith({
      tabId: "t1",
      token: "tok-2",
      jobId: "job-t",
      channel: mocks.channels[0],
    });

    emit(mocks.channels[0], { type: "delta", text: "RESTORED-OUTPUT", seq: 2 });
    emit(mocks.channels[0], { type: "done", answer: "完成", seq: 3 });
    await flush();
    expect(view.container.textContent).toContain("RESTORED-OUTPUT");
    expect(useUi.getState().takeover).toBeNull();
  });
});
