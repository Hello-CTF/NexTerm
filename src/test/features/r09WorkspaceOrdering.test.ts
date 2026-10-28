/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));
vi.mock("../../ipc/events", () => ({ listenEvent: vi.fn() }));

import {
  WORKSPACE_ORDER_LIMIT,
  applyWorkspaceOrder,
  parseWorkspaceOrder,
  reorderWorkspaces,
  sanitizeWorkspaceOrder,
  serializeWorkspaceOrder,
  workspaceOrderFromWorkspaces,
} from "../../app/workspaceOrdering";
import { sanitizeLayout, serializeLayout } from "../../app/layout";
import { useUi, type Workspace } from "../../app/store";

function workspace(id: string): Workspace {
  const tab = {
    id: `${id}-tab`,
    kind: "terminal" as const,
    title: `${id}-tab`,
    sessionId: `s-${id}`,
    tabId: `kernel-${id}`,
    closable: true,
  };
  return {
    id,
    kind: "session",
    title: `Host ${id}`,
    sessionId: `s-${id}`,
    panes: [{ id: `${id}-pane`, tabs: [tab], activeTabId: tab.id }],
    activePaneId: `${id}-pane`,
    closable: true,
  };
}

function workspaces(...ids: string[]): Workspace[] {
  return ids.map(workspace);
}

beforeEach(() => {
  localStorage.clear();
  useUi.setState({ workspaces: [], activeWorkspaceId: null, aiBoards: {}, toasts: [] });
});

describe("workspace drag reorder", () => {
  it("moves a workspace to the dragged position without mutating the input", () => {
    const input = workspaces("ws-1", "ws-2", "ws-3", "ws-4");
    const reordered = reorderWorkspaces(input, 0, 2);
    expect(reordered.map((item) => item.id)).toEqual(["ws-2", "ws-3", "ws-1", "ws-4"]);
    expect(input.map((item) => item.id)).toEqual(["ws-1", "ws-2", "ws-3", "ws-4"]);
    expect(reordered[0]).toBe(input[1]);
  });

  it("clamps out-of-range drop positions to the nearest end", () => {
    const input = workspaces("ws-1", "ws-2", "ws-3");
    expect(reorderWorkspaces(input, 0, 99).map((item) => item.id)).toEqual(["ws-2", "ws-3", "ws-1"]);
    expect(reorderWorkspaces(input, 2, -5).map((item) => item.id)).toEqual(["ws-3", "ws-1", "ws-2"]);
  });

  it("returns the original array for invalid or no-op drags", () => {
    const input = workspaces("ws-1", "ws-2", "ws-3");
    expect(reorderWorkspaces(input, -1, 0)).toBe(input);
    expect(reorderWorkspaces(input, 3, 0)).toBe(input);
    expect(reorderWorkspaces(input, 0.5, 1)).toBe(input);
    expect(reorderWorkspaces(input, 0, Number.NaN)).toBe(input);
    expect(reorderWorkspaces(input, 1, 1)).toBe(input);
    expect(reorderWorkspaces([], 0, 0)).toEqual([]);
  });

  it("derives the explicit order from the live workspace array", () => {
    expect(workspaceOrderFromWorkspaces(workspaces("ws-1", "ws-2"))).toEqual(["ws-1", "ws-2"]);
    expect(workspaceOrderFromWorkspaces([])).toEqual([]);
  });
});

describe("workspace order state", () => {
  it("applies an explicit order first and keeps the remaining workspaces in their relative order", () => {
    const input = workspaces("ws-1", "ws-2", "ws-3", "ws-4");
    const applied = applyWorkspaceOrder(input, ["ws-3", "ws-1", "missing"]);
    expect(applied.map((item) => item.id)).toEqual(["ws-3", "ws-1", "ws-2", "ws-4"]);
  });

  it("treats duplicate order ids as first occurrence wins and ignores an empty order", () => {
    const input = workspaces("ws-1", "ws-2", "ws-3");
    expect(applyWorkspaceOrder(input, ["ws-2", "ws-2", "ws-1"]).map((item) => item.id)).toEqual([
      "ws-2",
      "ws-1",
      "ws-3",
    ]);
    expect(applyWorkspaceOrder(input, [])).toBe(input);
  });

  it("sanitizes invalid order input: non-arrays, non-strings, duplicates, and oversize lists", () => {
    expect(sanitizeWorkspaceOrder(null)).toEqual([]);
    expect(sanitizeWorkspaceOrder("ws-1")).toEqual([]);
    expect(sanitizeWorkspaceOrder([1, null, "", "ws-1", undefined, "ws-1", "ws-2"])).toEqual(["ws-1", "ws-2"]);
    const oversize = Array.from({ length: WORKSPACE_ORDER_LIMIT + 5 }, (_, index) => `ws-${index}`);
    const sanitized = sanitizeWorkspaceOrder(oversize);
    expect(sanitized).toHaveLength(WORKSPACE_ORDER_LIMIT);
    expect(sanitized[0]).toBe("ws-0");
    expect(sanitized[WORKSPACE_ORDER_LIMIT - 1]).toBe(`ws-${WORKSPACE_ORDER_LIMIT - 1}`);
  });

  it("round-trips the order deterministically through serialize and parse", () => {
    const ids = ["ws-3", "ws-1", "ws-2"];
    const text = serializeWorkspaceOrder(ids);
    expect(text).toBe(serializeWorkspaceOrder(ids));
    expect(parseWorkspaceOrder(text)).toEqual(ids);
    expect(parseWorkspaceOrder(text)).toEqual(sanitizeWorkspaceOrder(ids));
  });

  it("parses corrupt or foreign payloads to an empty order", () => {
    expect(parseWorkspaceOrder("not json")).toEqual([]);
    expect(parseWorkspaceOrder("[]")).toEqual([]);
    expect(parseWorkspaceOrder('{"v":2,"ids":["ws-1"]}')).toEqual([]);
    expect(parseWorkspaceOrder('{"v":1,"ids":"ws-1"}')).toEqual([]);
  });

  it("round-trips a drag: reorder, derive, serialize, parse, and reapply yield the same order", () => {
    const input = workspaces("ws-1", "ws-2", "ws-3");
    const dragged = reorderWorkspaces(input, 2, 0);
    const order = workspaceOrderFromWorkspaces(dragged);
    expect(order).toEqual(["ws-3", "ws-1", "ws-2"]);
    const reapplied = applyWorkspaceOrder(workspaces("ws-1", "ws-2", "ws-3"), parseWorkspaceOrder(serializeWorkspaceOrder(order)));
    expect(reapplied.map((item) => item.id)).toEqual(["ws-3", "ws-1", "ws-2"]);
  });
});

describe("workspace order and layout contracts", () => {
  it("keeps the dragged workspace order across a layout serialize/sanitize round trip", () => {
    const input = workspaces("ws-1", "ws-2", "ws-3");
    const dragged = reorderWorkspaces(input, 2, 0);
    useUi.setState({ workspaces: dragged, activeWorkspaceId: "ws-3" });

    const dto = serializeLayout(useUi.getState());
    const parsed = sanitizeLayout(JSON.parse(JSON.stringify(dto)));
    expect(parsed?.workspaces.map((item) => item.id)).toEqual(["ws-3", "ws-1", "ws-2"]);
    expect(parsed?.activeWorkspaceId).toBe("ws-3");
  });
});
