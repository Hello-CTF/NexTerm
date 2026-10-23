/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, flushUntil, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

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
  usageSummary: vi.fn(),
  toast: vi.fn(),
  dispose: vi.fn(),
  channels: [] as { onEvent: (ev: Record<string, unknown>) => void }[],
}));

vi.mock("../ipc/commands", () => ({
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
  modelApi: { overview: mocks.overview, activate: vi.fn(), usageSummary: mocks.usageSummary },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));
vi.mock("../ipc/events", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ipc/events")>()),
  createAiChannel: (onEvent: (ev: Record<string, unknown>) => void) => {
    const channel = { onEvent };
    mocks.channels.push(channel);
    return channel;
  },
  disposeChannel: mocks.dispose,
  onChannelReopen: () => () => undefined,
}));

import { AiSidebar, aiCreatedTerminalOf } from "../features/ai/AiSidebar";
import { aiTabBusyIds, aiTabRunControls, clearAiTabRuns } from "../features/ai/runOwnership";
import { replayableJobIds } from "../features/ai/runRestore";
import { GLOBAL_AI_BOARD_KEY, useUi } from "../app/store";

beforeEach(() => {
  clearAiTabRuns([...aiTabBusyIds, ...aiTabRunControls.keys()]);
});

function message(id: string, role: string, content: unknown) {
  return { id, conversationId: "conv-1", role, content, tokensIn: null, tokensOut: null, createdAt: 1 };
}

const structuredMessages = [
  message("m1", "user", { role: "user", content: "帮我改配置", imageCount: 0, jobId: "job-1" }),
  message("m2", "toolCall", { type: "toolCall", jobId: "job-1", id: "call-1", name: "read_file", args: {} }),
  message("m3", "toolResult", {
    type: "toolResult",
    jobId: "job-1",
    id: "call-1",
    tool: "read_file",
    ok: true,
    text: "Full file contents",
    summary: "输出摘要",
    truncated: false,
    exitCode: 0,
    panic: false,
  }),
  message("m4", "fileChange", {
    type: "fileChange",
    jobId: "job-1",
    id: "chg-1",
    path: "/etc/demo.conf",
    before: "worker_processes 1;",
    after: "worker_processes 4;",
  }),
  message("m5", "planSubmitted", { type: "planSubmitted", jobId: "job-1", plan: "先备份再修改" }),
  message("m6", "assistant", { role: "assistant", content: "已完成修改" }),
  message("m7", "toolCall", { type: "toolCall", jobId: "job-2", id: "call-2", name: "exec_commands", args: {} }),
];

const runRows = [
  {
    id: "job-1",
    conversationId: "conv-1",
    status: "completed",
    attempt: 1,
    seq: 1,
    planMode: false,
    source: "chat",
    answer: "已完成修改",
    turns: 2,
    tokensIn: 10,
    tokensOut: 5,
    cacheCreationTokens: 0,
    latencyMs: 100,
    retries: 0,
    failures: 0,
    createdAt: 1,
    updatedAt: 2,
    finishedAt: 2,
  },
  {
    id: "job-2",
    conversationId: "conv-1",
    status: "interrupted",
    attempt: 1,
    seq: 2,
    planMode: false,
    source: "chat",
    answer: "",
    turns: 1,
    tokensIn: 5,
    tokensOut: 0,
    cacheCreationTokens: 0,
    latencyMs: 50,
    retries: 0,
    failures: 0,
    createdAt: 3,
    updatedAt: 3,
  },
  {
    id: "job-3",
    conversationId: "conv-1",
    status: "superseded",
    attempt: 1,
    seq: 3,
    planMode: false,
    source: "chat",
    answer: "被替换的回答",
    turns: 1,
    tokensIn: 5,
    tokensOut: 0,
    cacheCreationTokens: 0,
    latencyMs: 50,
    retries: 0,
    failures: 0,
    createdAt: 4,
    updatedAt: 4,
    finishedAt: 4,
  },
];

describe("AiSidebar history reload", () => {
  let view: MountedView | null = null;

  beforeEach(async () => {
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      setTimeout(cb, 0);
      return 1;
    });
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
    mocks.channels.length = 0;
    mocks.runs.mockResolvedValue(runRows);
    mocks.runEvents.mockImplementation((jobId: string) =>
      Promise.resolve(
        jobId === "job-2"
          ? [
              { type: "toolCall", id: "call-2", name: "exec_commands", display: "systemctl reload demo", seq: 1 },
              { type: "toolResult", id: "call-2", ok: true, summary: "reload 完成", text: "", seq: 2 },
            ]
          : jobId === "job-3"
            ? [
                { type: "toolCall", id: "call-3", name: "exec_commands", display: "被替换的工具", seq: 1 },
                { type: "delta", text: "被替换的回答", seq: 2 },
                { type: "done", answer: "被替换的回答", turns: 1, tokensIn: 1, tokensOut: 1, seq: 3 },
              ]
            : [],
      ),
    );
    mocks.hitlSnapshot.mockResolvedValue({
      runId: "job-2",
      checkpointId: "job-2",
      status: "running",
      attempt: 1,
      seq: 0,
      pending: [],
    });
    mocks.hitlEvents.mockResolvedValue([]);
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "Demo", createdAt: 0, updatedAt: 0, scope: {} }]);
    mocks.messages.mockResolvedValue(structuredMessages);
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    mocks.usageSummary.mockResolvedValue([]);
    useUi.setState({
      rightOpen: true,
      aiBusy: false,
      takeover: null,
      pushToast: mocks.toast,
      workspaces: [],
      aiBoards: {
        [GLOBAL_AI_BOARD_KEY]: {
          tabs: [{ id: "tab-bound", title: "Demo", conversationId: "conv-1" }],
          activeTabId: "tab-bound",
        },
      },
      sessions: [],
    });
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
  });

  afterEach(() => {
    view?.unmount();
    view = null;
    vi.unstubAllGlobals();
  });

  it("renders tool, diff and plan items from structured history while replaying interrupted runs once", async () => {
    const text = () => view!.container.textContent ?? "";
    await flushUntil(() => text().includes("systemctl reload demo"));

    expect(text()).toContain("帮我改配置");
    expect(text()).toContain("输出摘要");
    expect(text()).toContain("/etc/demo.conf");
    expect(text()).toContain("先备份再修改");
    expect(text()).toContain("已完成修改");
    expect(text()).toContain("reload 完成");

    const execBadges = view!.container.querySelectorAll('[title="exec_commands"]').length;
    expect(execBadges).toBe(1);
  });

  it("never replays superseded runs: replaced tools, answers and outcomes stay absent", async () => {
    const text = () => view!.container.textContent ?? "";
    await flushUntil(() => text().includes("systemctl reload demo"));
    await flush();

    expect(text()).not.toContain("被替换的工具");
    expect(text()).not.toContain("被替换的回答");
    expect(replayableJobIds(runRows)).toEqual(new Set(["job-2"]));
  });
});

describe("aiCreatedTerminalOf", () => {
  it("extracts the foreground terminal payload and rejects anything else", () => {
    expect(aiCreatedTerminalOf({ type: "toolResult", ok: true, terminal: { tabId: "tab-9", sessionId: "s1", title: "构建机" } })).toEqual({
      tabId: "tab-9",
      sessionId: "s1",
      title: "构建机",
    });
    expect(aiCreatedTerminalOf({ type: "toolResult", ok: true, terminal: { tabId: "tab-9", sessionId: "s1" } })).toEqual({
      tabId: "tab-9",
      sessionId: "s1",
      title: undefined,
    });
    expect(aiCreatedTerminalOf({ type: "toolCall", ok: true, terminal: { tabId: "tab-9", sessionId: "s1" } })).toBeNull();
    expect(aiCreatedTerminalOf({ type: "toolResult", ok: false, terminal: { tabId: "tab-9", sessionId: "s1" } })).toBeNull();
    expect(aiCreatedTerminalOf({ type: "toolResult", ok: true })).toBeNull();
    expect(aiCreatedTerminalOf({ type: "toolResult", ok: true, terminal: { tabId: "tab-9" } })).toBeNull();
  });
});

describe("AiSidebar automatic terminal_create opening", () => {
  let view: MountedView | null = null;

  beforeEach(async () => {
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      setTimeout(cb, 0);
      return 1;
    });
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
    mocks.channels.length = 0;
    mocks.chat.mockResolvedValue({ conversationId: "conv-1", jobId: "job-live" });
    mocks.runs.mockResolvedValue([]);
    mocks.runEvents.mockResolvedValue([]);
    mocks.hitlSnapshot.mockRejectedValue(new Error("none"));
    mocks.hitlEvents.mockResolvedValue([]);
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "Demo", createdAt: 0, updatedAt: 0, scope: {} }]);
    mocks.messages.mockResolvedValue([]);
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    mocks.usageSummary.mockResolvedValue([]);
    useUi.setState({
      rightOpen: true,
      aiBusy: false,
      takeover: null,
      pushToast: mocks.toast,
      workspaces: [],
      aiBoards: {
        [GLOBAL_AI_BOARD_KEY]: {
          tabs: [{ id: "tab-bound", title: "Demo", conversationId: "conv-1" }],
          activeTabId: "tab-bound",
        },
      },
      sessions: [
        { id: "s1", assetId: "a1", name: "web-1", kind: "ssh", status: "connected", tabs: [], createdAt: 1 },
      ],
    });
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
  });

  afterEach(() => {
    view?.unmount();
    view = null;
    vi.unstubAllGlobals();
  });

  const sendMessage = async () => {
    setInputValue(view!.container.querySelector("textarea")!, "Open a terminal");
    click(view!.container.querySelector<HTMLButtonElement>('button[title="发送 (Enter)"]')!);
    await flush();
    const channel = mocks.channels[0];
    return { onEvent: (ev: Record<string, unknown>) => act(() => channel.onEvent(ev)) };
  };

  const workspaceTabs = () => useUi.getState().workspaces.flatMap((w) => w.panes).flatMap((p) => p.tabs);

  it("a foreground terminal_create result automatically opens a tab in the workspace", async () => {
    const channel = await sendMessage();
    channel.onEvent({ type: "toolCall", id: "call-1", name: "terminal_create", display: "terminal_create" });
    channel.onEvent({
      type: "toolResult",
      id: "call-1",
      ok: true,
      summary: "Terminal tab created",
      text: "Created and displayed a terminal tab in the foreground (tab_id=tab-9); AI is working in it, and the user can take over input at any time",
      exitCode: 0,
      terminal: { tabId: "tab-9", sessionId: "s1", title: "构建机" },
    });
    await flush();
    const tab = workspaceTabs().find((t) => t.tabId === "tab-9")!;
    expect(tab.kind).toBe("terminal");
    expect(tab.sessionId).toBe("s1");
    expect(tab.title).toBe("构建机");
    expect(tab.renamedByUser).toBe(true);
    expect(tab.closable).toBe(true);
  });

  it("a toolResult without a terminal payload (background creation) does not open a tab", async () => {
    const channel = await sendMessage();
    channel.onEvent({ type: "toolCall", id: "call-1", name: "terminal_create", display: "terminal_create" });
    channel.onEvent({
      type: "toolResult",
      id: "call-1",
      ok: true,
      summary: "Terminal tab created",
      text: "Created a detached terminal in the background (tab_id=tab-9); AI can work in it, and the user can take over from Background Sessions",
      exitCode: 0,
    });
    await flush();
    expect(workspaceTabs().every((t) => t.tabId === undefined)).toBe(true);
  });

  it("terminal_create results released from the reorder buffer also open automatically exactly once", async () => {
    const channel = await sendMessage();
    const toolResult = {
      type: "toolResult",
      id: "call-1",
      ok: true,
      summary: "Terminal tab created",
      text: "Created and displayed a terminal tab in the foreground (tab_id=tab-9); AI is working in it, and the user can take over input at any time",
      exitCode: 0,
      terminal: { tabId: "tab-9", sessionId: "s1" },
      seq: 3,
    };
    channel.onEvent({ type: "toolCall", id: "call-1", name: "terminal_create", display: "terminal_create", seq: 1 });
    channel.onEvent(toolResult);
    await flush();
    expect(workspaceTabs().every((t) => t.tabId === undefined)).toBe(true);
    channel.onEvent({ type: "status", phase: "thinking", seq: 2 });
    await flush();
    channel.onEvent(toolResult);
    await flush();
    expect(workspaceTabs().filter((t) => t.tabId === "tab-9")).toHaveLength(1);
  });

  it("replaying historical runs does not reopen tabs", async () => {
    view!.unmount();
    mocks.runs.mockResolvedValue([
      {
        id: "job-old",
        conversationId: "conv-1",
        status: "interrupted",
        attempt: 1,
        seq: 1,
        planMode: false,
        source: "chat",
        answer: "",
        turns: 1,
        tokensIn: 1,
        tokensOut: 1,
        cacheCreationTokens: 0,
        latencyMs: 1,
        retries: 0,
        failures: 0,
        createdAt: 1,
        updatedAt: 2,
      },
    ]);
    mocks.runEvents.mockResolvedValue([
      { type: "toolCall", id: "call-9", name: "terminal_create", display: "terminal_create", seq: 1 },
      {
        type: "toolResult",
        id: "call-9",
        ok: true,
        summary: "Terminal tab created",
        text: "Created and displayed a terminal tab in the foreground (tab_id=tab-old); AI is working in it, and the user can take over input at any time",
        exitCode: 0,
        terminal: { tabId: "tab-old", sessionId: "s1" },
        seq: 2,
      },
    ]);
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flushUntil(() => (view!.container.textContent ?? "").includes("terminal_create"));
    expect(workspaceTabs().every((t) => t.tabId === undefined)).toBe(true);
  });
});
