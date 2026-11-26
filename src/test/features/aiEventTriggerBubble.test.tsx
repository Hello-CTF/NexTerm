/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { mount, type MountedView } from "./reactTestUtils";

import { ChatBubble } from "../../features/ai/panel/AiMessageBubbles";
import type { ChatItem } from "../../features/ai/conversation";

let view: MountedView | undefined;

function mountUser(item: Extract<ChatItem, { role: "user" }>): MountedView {
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

afterEach(() => {
  view?.unmount();
  view = undefined;
});

describe("terminal event trigger", () => {
  it("shows the fixed trigger note as a notice without edit or resend", () => {
    const shown = mountUser({ id: "h1", attempt: null, role: "user", text: "[NexTerm terminal event] ...", messageId: "m1", event: true });
    expect(shown.container.querySelector('[data-testid="ai-event-trigger"]')?.textContent).toContain("由终端事件触发的运行");
    expect(shown.container.querySelector('button[aria-label="编辑并重新发送"]')).toBeNull();
  });

  it("keeps a typed message as a user bubble with edit", () => {
    const shown = mountUser({ id: "h2", attempt: null, role: "user", text: "看下负载", messageId: "m2" });
    expect(shown.container.querySelector('[data-testid="ai-event-trigger"]')).toBeNull();
    expect(shown.container.querySelector('button[aria-label="编辑并重新发送"]')).not.toBeNull();
  });
});
