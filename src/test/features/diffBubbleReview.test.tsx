/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, mount, type MountedView } from "./reactTestUtils";

import { ChatBubble } from "../../features/ai/panel/AiMessageBubbles";
import type { ChatItem } from "../../features/ai/conversation";
import { takeFileReview } from "../../features/files/aiReview";
import { useUi } from "../../app/store";

type DiffItem = Extract<ChatItem, { role: "diff" }>;

let view: MountedView | undefined;

function mountDiff(item: Partial<DiffItem>, readOnly = false): MountedView {
  view = mount(
    createElement(ChatBubble, {
      item: { id: "d1", attempt: null, role: "diff", path: "/srv/a.conf", before: "a\n", after: "b\n", ...item } as DiffItem,
      streaming: false,
      planApproved: false,
      onApprovePlan: vi.fn(),
      onRetry: vi.fn(),
      onEdit: vi.fn(),
      agentToolCall: null,
      onAgentApproval: vi.fn(),
      agentApprovalBusy: false,
      readOnly,
    }),
  );
  return view;
}

function reviewButton(): HTMLButtonElement | undefined {
  return [...view!.container.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "在编辑器中查看差异");
}

describe("an AI file change opens for review in the editor", () => {
  const addTab = vi.fn();

  beforeEach(() => {
    addTab.mockReset();
    useUi.setState({ addTab, pushToast: vi.fn(), sessions: [{ id: "s1", name: "web-01", kind: "ssh" }] as never });
  });

  afterEach(() => {
    view?.unmount();
    view = undefined;
  });

  it("opens the file on the session it was written on with the change's before and after", () => {
    mountDiff({ sessionId: "s1" });
    click(reviewButton()!);
    expect(addTab).toHaveBeenCalledWith(expect.objectContaining({ kind: "files", sessionId: "s1", path: "/srv/a.conf" }));
    expect(takeFileReview("s1", "/srv/a.conf")).toEqual({ before: "a\n", after: "b\n" });
  });

  it("is disabled while that session is closed", () => {
    mountDiff({ sessionId: "gone" });
    expect(reviewButton()!.disabled).toBe(true);
    expect(reviewButton()!.title).toBe("该文件所在的会话已关闭");
  });

  it("is absent without a recorded session and in a read-only conversation", () => {
    mountDiff({});
    expect(reviewButton()).toBeUndefined();
    view!.unmount();
    mountDiff({ sessionId: "s1" }, true);
    expect(reviewButton()).toBeUndefined();
  });
});
