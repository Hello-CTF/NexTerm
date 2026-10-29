/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, mount, type MountedView } from "./reactTestUtils";

import { ChatBubble } from "../../features/ai/panel/AiMessageBubbles";
import type { ChatItem } from "../../features/ai/conversation";

type ToolItem = Extract<ChatItem, { role: "tool" }>;

const COMMANDS = '["ps -eo pid,ppid,etime,command | head -15","docker stats --no-stream"]';

function toolItem(partial: Partial<ToolItem> = {}): ToolItem {
  return {
    id: "item-1",
    role: "tool",
    callId: "call-1",
    name: "exec_commands",
    display: COMMANDS,
    summary: "$ ps -eo pid\n  PID  PPID",
    text: "$ ps -eo pid\n  PID  PPID\n 1234  1 full tail",
    ok: true,
    exitCode: 0,
    ...partial,
  } as ToolItem;
}

let view: MountedView | undefined;

function mountBubble(item: ToolItem): MountedView {
  view = mount(
    createElement(ChatBubble, {
      item,
      streaming: false,
      planApproved: false,
      onApprovePlan: vi.fn(),
      onRetry: vi.fn(),
      onEdit: vi.fn(),
      agentToolCall: null,
      onAgentApproval: vi.fn(),
      agentApprovalBusy: false,
    }),
  );
  return view;
}

function toggle(): HTMLButtonElement {
  const button = view!.container.querySelector<HTMLButtonElement>("button[aria-expanded]");
  if (!button) throw new Error("toggle not found");
  return button;
}

afterEach(() => {
  view?.unmount();
  view = undefined;
});

describe("tool bubble", () => {
  it("shows one line while closed and the whole input and output once opened", () => {
    mountBubble(toolItem());
    expect(toggle().getAttribute("aria-expanded")).toBe("false");
    expect(view!.container.querySelectorAll("pre")).toHaveLength(0);
    expect(view!.container.textContent).not.toContain("full tail");

    click(toggle());
    expect(toggle().getAttribute("aria-expanded")).toBe("true");
    const blocks = [...view!.container.querySelectorAll("pre")].map((el) => el.textContent);
    expect(blocks).toEqual([COMMANDS, toolItem().text]);

    click(toggle());
    expect(view!.container.querySelectorAll("pre")).toHaveLength(0);
  });

  it("keeps the first line of a failure visible while closed", () => {
    mountBubble(toolItem({ ok: false, exitCode: 2, summary: "ls: cannot access x", text: "ls: cannot access x\nsecond line" }));
    expect(view!.container.textContent).toContain("ls: cannot access x");
    expect(view!.container.textContent).not.toContain("second line");
  });

  it("keeps the reason of a refusal visible while closed", () => {
    mountBubble(toolItem({ ok: false, exitCode: null, summary: "权限策略已拒绝: 命中危险规则", text: "权限策略已拒绝: 命中危险规则" }));
    expect(view!.container.textContent).toContain("拦截原因：命中危险规则");
    expect(view!.container.textContent).toContain("已拦截");
  });

  it("has no body while the call is still running", () => {
    mountBubble(toolItem({ summary: undefined, text: undefined, ok: undefined, exitCode: undefined }));
    expect(view!.container.querySelector('[aria-busy="true"]')).not.toBeNull();
    expect(view!.container.querySelectorAll("pre")).toHaveLength(0);
  });
});
