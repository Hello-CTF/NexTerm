/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { mount, type MountedView } from "./reactTestUtils";
import type { ChatItem, ConfirmItem, ToolItem } from "../../features/ai/conversation";
import { AiChatMessageStream } from "../../features/ai/panel/AiChatMessageStream";
import type { AiChatCtx } from "../../features/ai/panel/aiChatCtx";
import { confirmCommandText } from "../../features/ai/panel/aiConfirmLabels";

let view: MountedView | undefined;

afterEach(() => {
  view?.unmount();
  view = undefined;
});

const COMMAND = "brew update && brew outdated";

function sendKeysCall(partial: Partial<ToolItem> = {}): ToolItem {
  return {
    id: "g1:tool:call-1",
    attempt: 1,
    role: "tool",
    callId: "call-1",
    name: "send_keys",
    display: `send_keys "${COMMAND}"`,
    ...partial,
  };
}

function sendKeysConfirm(partial: Partial<ConfirmItem> = {}): ConfirmItem {
  return {
    id: "g1:confirm:call-1",
    attempt: 1,
    role: "confirm",
    jobId: "job",
    callId: "call-1",
    tool: "send_keys",
    rendered: `send_keys ${COMMAND} <enter>`,
    reason: "软件包管理操作",
    risk: "needs_confirm",
    nonce: "n",
    ...partial,
  };
}

function renderStream(items: ChatItem[], confirmCard: ConfirmItem | null): HTMLElement {
  const ctx = {
    setScrollEl: () => {},
    messageWindowStart: 0,
    loadOlderMessages: () => {},
    conv: { items, todos: [], attempts: [] },
    conversationError: null,
    conversationIdRef: { current: "conv" },
    restoreConversation: async () => {},
    virtual: {
      range: { active: false, start: 0, end: items.length, padTop: 0, padBottom: 0 },
      measureRef: () => {},
    },
    activeMatchIndex: -1,
    busy: true,
    handleApprovePlan: vi.fn(),
    handleRetryRun: vi.fn(),
    handleEditResend: vi.fn(),
    agentToolCallFor: () => null,
    submitAgentApproval: vi.fn(),
    submittingCardId: null,
    confirmCard,
    confirmCardRef: { current: null },
    confirm: vi.fn(),
    productionNames: null,
    handleProductionState: vi.fn(),
    productionBlocked: false,
    questionCard: null,
    answer: vi.fn(),
    questionInput: "",
    setQuestionInput: vi.fn(),
    follow: { newOutput: false, jumpToLatest: vi.fn() },
    setModelPanelOpen: vi.fn(),
  } as unknown as AiChatCtx;
  view = mount(createElement(AiChatMessageStream, { ctx }));
  return view.container;
}

function occurrences(text: string, needle: string): number {
  return text.split(needle).length - 1;
}

describe("AI confirmation card", () => {
  it("shows a pending call once, in the card, with the keys as typed", () => {
    const card = sendKeysConfirm();
    const container = renderStream([{ id: "u", attempt: 1, role: "user", text: "哪些包过期了" }, sendKeysCall(), card], card);
    const text = container.textContent ?? "";
    expect(occurrences(text, COMMAND)).toBe(1);
    expect(text).not.toContain("\\u0026");
    expect(text).not.toContain("等待确认");
    expect(text).not.toContain("工具执行中");
    const group = container.querySelector('[role="group"]');
    expect(group?.getAttribute("aria-label")).toBe("需要你确认：发送按键");
    expect(group?.querySelector("pre")?.textContent).toBe(`${COMMAND} <enter>`);
    expect(group?.textContent).toContain("软件包管理操作");
    const buttons = [...(group?.querySelectorAll("button") ?? [])].map((button) => button.textContent);
    expect(buttons).toEqual(expect.arrayContaining(["允许一次", "拒绝"]));
  });

  it("keeps a parser error behind the details disclosure", () => {
    const card = sendKeysConfirm({
      risk: "unknowable",
      reason: "命令语法无法解析，无法判断会执行什么",
      reasonDetail: "1:1: `foo(` must be followed by `)`",
    });
    const container = renderStream([sendKeysCall(), card], card);
    const group = container.querySelector('[role="group"]');
    expect(group?.textContent).toContain("无法判断影响，需要你确认");
    expect(group?.textContent).toContain("命令语法无法解析，无法判断会执行什么");
    const details = group?.querySelector("details");
    expect(details?.open).toBe(false);
    expect(details?.querySelector("summary")?.textContent).toBe("详情");
    expect(details?.querySelector("pre")?.textContent).toBe("1:1: `foo(` must be followed by `)`");
  });

  it("returns the call and the decision to the stream once resolved", () => {
    const container = renderStream(
      [
        sendKeysCall({ summary: "已发送", text: "已发送", ok: true, exitCode: null }),
        sendKeysConfirm({ resolution: "已允许一次" }),
      ],
      null,
    );
    const text = container.textContent ?? "";
    expect(container.querySelector('[role="group"]')).toBeNull();
    expect(text).toContain("发送按键 · 已允许一次");
    expect(occurrences(text, COMMAND)).toBe(1);
  });

  it("strips only the leading tool name from the rendered call", () => {
    expect(confirmCommandText("send_keys", "send_keys ls <enter>")).toBe("ls <enter>");
    expect(confirmCommandText("send_keys", '{\n  "keys": "ls"\n}')).toBe('{\n  "keys": "ls"\n}');
    expect(confirmCommandText("", "ls")).toBe("ls");
  });
});
