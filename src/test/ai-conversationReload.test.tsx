/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { flush, flushUntil, mount, type MountedView } from "./features/reactTestUtils";

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

import { AiSidebar } from "../features/ai/AiSidebar";
import { replayableJobIds } from "../features/ai/runRestore";
import { useUi } from "../app/store";

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
    text: "文件全文",
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
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "演示", createdAt: 0, updatedAt: 0, scope: {} }]);
    mocks.messages.mockResolvedValue(structuredMessages);
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    mocks.usageSummary.mockResolvedValue([]);
    localStorage.setItem("nexterm.ai.conversation.v1", "conv-1");
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
    localStorage.removeItem("nexterm.ai.conversation.v1");
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

    const execBadges = text().split("exec_commands").length - 1;
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
