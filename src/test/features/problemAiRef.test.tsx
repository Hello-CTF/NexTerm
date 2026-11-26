/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, mount, type MountedView } from "./reactTestUtils";
import { useUi, type WorkspaceProblem } from "../../app/store";
import { WorkspaceProblems } from "../../features/problems/WorkspaceProblems";
import { REF_TEXT_RUNES } from "../../features/ai/refText";

function problem(overrides: Partial<WorkspaceProblem> = {}): WorkspaceProblem {
  return {
    id: 1,
    generation: 1,
    createdAt: Date.UTC(2026, 10, 25, 8, 0, 0),
    source: "sql-query",
    operation: "执行 SQL",
    target: { title: "orders-db", identity: "postgres@db-01:5432/orders" },
    summary: "relation \"order_items\" does not exist",
    detail: "ERROR: relation \"order_items\" does not exist (SQLSTATE 42P01)",
    detailTruncated: false,
    canRetryRead: false,
    sql: "select * from order_items where password='hunter2'",
    ...overrides,
  };
}

describe("asking the AI about a recorded problem", () => {
  let view: MountedView | null = null;
  const pushToast = vi.fn();
  const setRightOpen = vi.fn();

  beforeEach(() => {
    pushToast.mockReset();
    setRightOpen.mockReset();
    useUi.setState({ aiPendingRefs: [], pushToast, setRightOpen, workspaceRuntime: { w1: { generation: 1, problems: [problem()] } } });
    view = mount(createElement(WorkspaceProblems, { workspaceId: "w1" }));
  });

  afterEach(() => {
    view?.unmount();
    view = null;
  });

  it("adds a problem chip with its text, source and statement, redacted, and opens the AI sidebar", () => {
    click(view!.container.querySelector('button[aria-label="让 AI 看看：relation \\"order_items\\" does not exist"]')!);
    const [ref] = useUi.getState().aiPendingRefs;
    expect(ref).toMatchObject({
      kind: "problem",
      label: "问题 · relation \"order_items\" d…",
      detail: "执行 SQL · orders-db · postgres@db-01:5432/orders",
    });
    expect(ref.refId).toBe(ref.id);
    expect(ref.text).toContain("摘要：relation \"order_items\" does not exist");
    expect(ref.text).toContain("详情：ERROR: relation \"order_items\" does not exist (SQLSTATE 42P01)");
    expect(ref.text).toContain("来源：SQL 执行");
    expect(ref.text).toContain("select * from order_items");
    expect(ref.text).not.toContain("hunter2");
    expect(setRightOpen).toHaveBeenCalledWith(true);
    expect(pushToast).not.toHaveBeenCalled();
  });

  it("keeps the head of a long problem within the quote budget and says so", () => {
    act(() => {
      useUi.setState({ workspaceRuntime: { w1: { generation: 1, problems: [problem({ detail: "x".repeat(REF_TEXT_RUNES * 2), sql: undefined })] } } });
    });
    click(view!.container.querySelector('button[aria-label^="让 AI 看看"]')!);
    const [ref] = useUi.getState().aiPendingRefs;
    expect(Array.from(ref.text!)).toHaveLength(REF_TEXT_RUNES);
    expect(ref.text!.startsWith("时间：")).toBe(true);
    expect(pushToast).toHaveBeenCalledWith("info", `问题详情较长，AI 只会看到前 ${REF_TEXT_RUNES} 个字符`);
  });
});
