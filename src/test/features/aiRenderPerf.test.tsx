/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";

const markdownRenders = vi.hoisted(() => ({ byText: new Map<string, number>() }));

vi.mock("../../features/ai/Markdown", async () => {
  const { createElement: h } = await import("react");
  return {
    Markdown: ({ text }: { text: string }) => {
      markdownRenders.byText.set(text, (markdownRenders.byText.get(text) ?? 0) + 1);
      return h("span", { className: "md-mock" }, text);
    },
  };
});

const mocks = vi.hoisted(() => ({
  chat: vi.fn(),
  cancel: vi.fn(),
  getPermission: vi.fn(),
  conversationList: vi.fn(),
  conversationDelete: vi.fn(),
  messages: vi.fn(),
  runs: vi.fn(),
  runEvents: vi.fn(),
  hitlSnapshot: vi.fn(),
  hitlEvents: vi.fn(),
  overview: vi.fn(),
  toast: vi.fn(),
  channels: [] as { onEvent: (ev: Record<string, unknown>) => void }[],
}));

vi.mock("../../ipc/commands", () => ({
  aiApi: {
    chat: mocks.chat,
    cancel: mocks.cancel,
    confirm: vi.fn(),
    answer: vi.fn(),
    hitlSnapshot: mocks.hitlSnapshot,
    hitlEvents: mocks.hitlEvents,
    runs: mocks.runs,
    runEvents: mocks.runEvents,
    getPermission: mocks.getPermission,
    conversationList: mocks.conversationList,
    conversationDelete: mocks.conversationDelete,
    messages: mocks.messages,
    takeoverEnter: vi.fn(),
    takeoverRun: vi.fn(),
    takeoverExit: vi.fn(),
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
}));
vi.mock("../../ui/dialogs", () => ({ ask: vi.fn(), promptText: vi.fn() }));

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

function renderCount(text: string): number {
  return markdownRenders.byText.get(text) ?? 0;
}

describe("AI 会话流式渲染性能", () => {
  let view: MountedView | null = null;

  beforeEach(() => {
    markdownRenders.byText.clear();
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
    localStorage.clear();
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
    vi.unstubAllGlobals();
    view?.unmount();
    view = null;
    localStorage.clear();
  });

  async function send(text: string) {
    const textarea = view!.container.querySelector("textarea");
    if (!textarea) throw new Error("input textarea not found");
    setInputValue(textarea, text);
    click(view!.container.querySelector('button[title="发送 (Enter)"]')!);
    await flush();
  }

  it("流式刷新只重渲染末尾气泡，不每帧重渲染整个列表", async () => {
    await send("第一问");
    emit({ type: "delta", text: "第一答" });
    act(runFrames);
    emit({ type: "done", answer: "第一答" });
    await flush();
    const settledRenders = renderCount("第一答");
    expect(settledRenders).toBeGreaterThan(0);

    await send("第二问");
    for (let i = 1; i <= 6; i++) {
      emit({ type: "delta", text: `流${i}` });
      act(runFrames);
    }
    expect(renderCount("第一答")).toBe(settledRenders);
    const total = [...markdownRenders.byText.values()].reduce((a, b) => a + b, 0);
    expect(total - settledRenders).toBeGreaterThanOrEqual(6);
  });
});
