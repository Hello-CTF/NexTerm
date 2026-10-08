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
} from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  chat: vi.fn(),
  cancel: vi.fn(),
  confirm: vi.fn(),
  answer: vi.fn(),
  editResend: vi.fn(),
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
  reopens: new Map<unknown, () => void>(),
}));

vi.mock("../ipc/commands", () => ({
  aiApi: {
    chat: mocks.chat,
    cancel: mocks.cancel,
    confirm: mocks.confirm,
    answer: mocks.answer,
    editResend: mocks.editResend,
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
  onChannelReopen: (channel: unknown, cb: () => void) => {
    mocks.reopens.set(channel, cb);
    return () => mocks.reopens.delete(channel);
  },
}));

import { AiSidebar } from "../features/ai/AiSidebar";
import { useUi } from "../app/store";

function message(id: string, role: string, content: unknown) {
  return { id, conversationId: "conv-1", role, content, tokensIn: null, tokensOut: null, createdAt: 1 };
}

const historyMessages = [
  message("m1", "user", { role: "user", content: "第一问", imageCount: 0, jobId: "job-1" }),
  message("m2", "assistant", { role: "assistant", content: "第一答" }),
  message("m3", "toolCall", { type: "toolCall", jobId: "job-1", id: "call-1", name: "read_file", args: {} }),
  message("m4", "toolResult", {
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
  message("m5", "user", { role: "user", content: "第二问", imageCount: 0, jobId: "job-2" }),
  message("m6", "assistant", { role: "assistant", content: "第二答" }),
];

function runRow(id: string, status: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    conversationId: "conv-1",
    status,
    attempt: 1,
    seq: 1,
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
    createdAt: 1,
    updatedAt: 1,
    finishedAt: 1,
    ...extra,
  };
}

function editButtonFor(view: MountedView, text: string): HTMLButtonElement {
  const wrappers = [...view.container.querySelectorAll("[data-conversation-index]")];
  const wrapper = wrappers.find((w) => w.textContent?.includes(text));
  const button = wrapper?.querySelector('button[aria-label="编辑并重新发送"]');
  if (!button) throw new Error(`edit button not found for: ${text}`);
  return button as HTMLButtonElement;
}

function hasEditButton(view: MountedView, text: string): boolean {
  try {
    editButtonFor(view, text);
    return true;
  } catch {
    return false;
  }
}

function composerOf(view: MountedView): HTMLTextAreaElement {
  const textarea = view.container.querySelector("textarea[aria-label='消息输入']");
  if (!textarea) throw new Error("composer not found");
  return textarea as HTMLTextAreaElement;
}

describe("AiSidebar edit and resend", () => {
  let view: MountedView | null = null;

  beforeEach(async () => {
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      setTimeout(cb, 0);
      return 1;
    });
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
    mocks.channels.length = 0;
    mocks.reopens.clear();
    mocks.chat.mockResolvedValue({ jobId: "job-9", conversationId: "conv-1" });
    mocks.cancel.mockResolvedValue(undefined);
    mocks.confirm.mockResolvedValue(undefined);
    mocks.editResend.mockResolvedValue(undefined);
    mocks.hitlSnapshot.mockResolvedValue({
      runId: "job-x",
      checkpointId: "job-x",
      status: "running",
      attempt: 1,
      seq: 0,
      pending: [],
    });
    mocks.hitlEvents.mockResolvedValue([]);
    mocks.runs.mockResolvedValue([runRow("job-1", "completed"), runRow("job-2", "completed")]);
    mocks.runEvents.mockResolvedValue([]);
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "演示", createdAt: 0, updatedAt: 0, scope: {} }]);
    mocks.messages.mockResolvedValue(historyMessages);
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

  it("编辑重发成功：editResend 截断后以编辑文本重新发送", async () => {
    const text = () => view!.container.textContent ?? "";
    await flushUntil(() => text().includes("第二答"));

    click(editButtonFor(view!, "第一问"));
    await flush();
    expect(composerOf(view!).value).toBe("第一问");
    expect(text()).toContain("编辑重发：这条消息之后的消息与运行会被删除");

    setInputValue(composerOf(view!), "第一问（改）");
    clickButton(view!.container, "替换并重新发送");
    await flushUntil(() => mocks.chat.mock.calls.length > 0);

    expect(mocks.editResend).toHaveBeenCalledWith("conv-1", "m1");
    expect(mocks.chat).toHaveBeenCalledWith(
      expect.objectContaining({ conversationId: "conv-1", message: "第一问（改）" }),
    );
    expect(text()).not.toContain("编辑重发：这条消息之后的消息与运行会被删除");
  });

  it("旧消息截断后的 UI：锚点原文保留，其后的回答/工具/后续消息全部移除", async () => {
    const text = () => view!.container.textContent ?? "";
    await flushUntil(() => text().includes("第二答"));
    expect(text()).toContain("输出摘要");

    click(editButtonFor(view!, "第一问"));
    await flush();
    setInputValue(composerOf(view!), "第一问（改）");
    clickButton(view!.container, "替换并重新发送");
    await flushUntil(() => mocks.chat.mock.calls.length > 0);
    await flush();

    expect(text()).toContain("第一问");
    expect(text()).not.toContain("第一答");
    expect(text()).not.toContain("输出摘要");
    expect(text()).not.toContain("第二问");
    expect(text()).not.toContain("第二答");
  });

  it("活动 job/HITL：编辑重发由后端取消清理，前端不重复调用清理 IPC", async () => {
    const text = () => view!.container.textContent ?? "";
    mocks.runs.mockResolvedValue([
      runRow("job-1", "completed"),
      runRow("job-2", "interrupted", { finishedAt: undefined }),
    ]);
    mocks.runEvents.mockImplementation((jobId: string) =>
      Promise.resolve(
        jobId === "job-2"
          ? [
              {
                type: "confirmRequired",
                id: "call-9",
                tool: "exec_commands",
                rendered: "$ rm -rf /tmp/x",
                confirmationNonce: "n-9",
                requestId: "req-9",
                attempt: 1,
                seq: 1,
              },
            ]
          : [],
      ),
    );
    const interrupt = {
      id: "req-9",
      runId: "job-2",
      checkpointId: "job-2",
      checkpointHash: "hash-9",
      targetId: "target-9",
      callId: "call-9",
      tool: "exec_commands",
      kind: "confirm",
      parameters: {},
      parameterHash: "phash-9",
      nonce: "n-9",
      createdAt: "2026-10-06T10:00:00Z",
      expiresAt: "2099-10-06T10:05:00Z",
      attempt: 1,
      seq: 1,
    };
    mocks.hitlSnapshot.mockResolvedValue({
      runId: "job-2",
      checkpointId: "job-2",
      status: "running",
      attempt: 1,
      seq: 1,
      pending: [interrupt],
    });

    view?.unmount();
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flushUntil(() => text().includes("需要你确认"));
    expect(useUi.getState().aiBusy).toBe(true);
    const restoredChannel = mocks.channels.at(-1);
    expect(restoredChannel).toBeDefined();

    click(editButtonFor(view!, "第一问"));
    await flush();
    expect(composerOf(view!).value).toBe("第一问");
    setInputValue(composerOf(view!), "第一问（改）");
    clickButton(view!.container, "替换并重新发送");
    await flushUntil(() => mocks.chat.mock.calls.length > 0);

    expect(mocks.editResend).toHaveBeenCalledWith("conv-1", "m1");
    expect(mocks.cancel).not.toHaveBeenCalled();
    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(mocks.dispose).toHaveBeenCalledWith(restoredChannel);
    expect(text()).not.toContain("需要你确认");
    expect(text()).not.toContain("第二答");
    expect(mocks.chat).toHaveBeenCalledWith(
      expect.objectContaining({ conversationId: "conv-1", message: "第一问（改）" }),
    );
  });

  it("失败路径：editResend 拒绝时不截断、不重发、保留编辑态", async () => {
    const text = () => view!.container.textContent ?? "";
    await flushUntil(() => text().includes("第二答"));
    mocks.editResend.mockRejectedValue(new Error("消息不存在"));

    click(editButtonFor(view!, "第一问"));
    await flush();
    setInputValue(composerOf(view!), "第一问（改）");
    clickButton(view!.container, "替换并重新发送");
    await flushUntil(() => mocks.toast.mock.calls.length > 0);

    expect(mocks.toast).toHaveBeenCalledWith(
      "error",
      expect.stringContaining("编辑重发失败"),
    );
    expect(mocks.chat).not.toHaveBeenCalled();
    expect(text()).toContain("第一答");
    expect(text()).toContain("输出摘要");
    expect(text()).toContain("编辑重发：这条消息之后的消息与运行会被删除");
    expect(composerOf(view!).value).toBe("第一问（改）");
  });

  it("提交串行化：deferred double-submit 只发一次 editResend/chat，后续 continuation 不 dispose 首个新 run", async () => {
    const text = () => view!.container.textContent ?? "";
    await flushUntil(() => text().includes("第二答"));
    const editGate = deferred<void>();
    const chatGate = deferred<{ jobId: string; conversationId: string }>();
    mocks.editResend.mockReturnValue(editGate.promise);
    mocks.chat.mockReturnValue(chatGate.promise);

    click(editButtonFor(view!, "第一问"));
    await flush();
    setInputValue(composerOf(view!), "第一问（改）");
    const submitButton = clickButton(view!.container, "替换并重新发送");
    expect(submitButton.disabled).toBe(true);
    expect(text()).toContain("正在替换并重新发送…");

    act(() => {
      composerOf(view!).dispatchEvent(
        new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
      );
    });
    await flush();
    expect(mocks.editResend).toHaveBeenCalledTimes(1);

    editGate.resolve();
    await flushUntil(() => mocks.chat.mock.calls.length > 0);
    expect(mocks.chat).toHaveBeenCalledTimes(1);
    const newRunChannel = mocks.channels.at(-1);
    expect(newRunChannel).toBeDefined();

    chatGate.resolve({ jobId: "job-9", conversationId: "conv-1" });
    await flush();
    expect(mocks.dispose).not.toHaveBeenCalledWith(newRunChannel);
    expect(text()).not.toContain("正在替换并重新发送…");
  });

  it("新发送完成的消息无需 remount 即可编辑：chat 后通过 messages IPC 补齐 messageId", async () => {
    const text = () => view!.container.textContent ?? "";
    await flushUntil(() => text().includes("第二答"));
    mocks.chat.mockResolvedValue({ jobId: "job-1", conversationId: "conv-1" });
    mocks.messages.mockImplementation(() =>
      Promise.resolve([
        ...historyMessages,
        message("m-100", "user", { role: "user", content: "第三问", imageCount: 0, jobId: "job-1" }),
        message("m-101", "assistant", { role: "assistant", content: "第三答" }),
      ]),
    );

    setInputValue(composerOf(view!), "第三问");
    click(view!.container.querySelector('button[title="发送 (Enter)"]')!);
    await flushUntil(() => mocks.messages.mock.calls.length >= 2);
    await flushUntil(() => hasEditButton(view!, "第三问"));

    click(editButtonFor(view!, "第三问"));
    await flush();
    expect(composerOf(view!).value).toBe("第三问");
    setInputValue(composerOf(view!), "第三问（改）");
    clickButton(view!.container, "替换并重新发送");
    await flushUntil(() => mocks.editResend.mock.calls.length > 0);
    expect(mocks.editResend).toHaveBeenCalledWith("conv-1", "m-100");
  });

  it("取消编辑：不发起任何 IPC，保留输入框内容", async () => {
    const text = () => view!.container.textContent ?? "";
    await flushUntil(() => text().includes("第二答"));

    click(editButtonFor(view!, "第一问"));
    await flush();
    clickButton(view!.container, "取消");
    await flush();

    expect(mocks.editResend).not.toHaveBeenCalled();
    expect(mocks.chat).not.toHaveBeenCalled();
    expect(text()).not.toContain("编辑重发：这条消息之后的消息与运行会被删除");
    expect(composerOf(view!).value).toBe("第一问");
  });
});
