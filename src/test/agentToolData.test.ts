import { describe, expect, it } from "vitest";

import {
  ToolHandleRegistry,
  approvalFromConfirmCard,
  buildAgentToolCall,
  runSummaryFromAttempt,
} from "../features/ai/agentToolData";
import type { ConfirmItem, ToolItem } from "../features/ai/conversation";
import type { AgentRosterEntry } from "../features/ai/agentTypes";

function rosterEntry(partial: Partial<AgentRosterEntry> = {}): AgentRosterEntry {
  return {
    handle: "build",
    alias: "build",
    host: "web-01",
    cwd: "/srv/app",
    ownerId: "u-1",
    ownerName: "alice",
    isMine: true,
    production: true,
    persistent: true,
    incarnation: "inc-1",
    presence: "attached",
    evidence: null,
    releasePending: false,
    tabId: "tab-1",
    lastRun: null,
    ...partial,
  };
}

describe("ToolHandleRegistry", () => {
  it("keeps numeric call IDs distinct across run generations", () => {
    const registry = new ToolHandleRegistry();
    registry.record(1, { type: "toolCall", id: "23", handle: "first" });
    expect(registry.lookup(12, "3")).toEqual({ kind: "unknown" });
    registry.record(12, { type: "toolCall", id: "3", handle: "second" });
    expect(registry.lookup(1, "23")).toEqual({ kind: "handle", handle: "first" });
    expect(registry.lookup(12, "3")).toEqual({ kind: "handle", handle: "second" });
  });

  it("records verified handles from toolCall and toolResult events of one run", () => {
    const registry = new ToolHandleRegistry();
    expect(registry.record(1, { type: "toolCall", id: "call-1", handle: "build" })).toBe(true);
    expect(registry.lookup(1, "call-1")).toEqual({ kind: "handle", handle: "build" });
    expect(registry.handles()).toEqual(["build"]);
    // The same handle through the result event is not a change.
    expect(registry.record(1, { type: "toolResult", id: "call-1", handle: "build" })).toBe(false);
    expect(registry.lookup(1, "call-1")).toEqual({ kind: "handle", handle: "build" });
  });

  it("ignores handle-less, id-less and non-tool events without clearing evidence", () => {
    const registry = new ToolHandleRegistry();
    registry.record(1, { type: "toolCall", id: "call-1", handle: "build" });
    expect(registry.record(1, { type: "toolResult", id: "call-1" })).toBe(false);
    expect(registry.record(1, { type: "toolCall", id: "call-1", handle: "" })).toBe(false);
    expect(registry.record(1, { type: "toolCall", handle: "deploy" })).toBe(false);
    expect(registry.record(1, { type: "delta", id: "call-2", handle: "deploy" })).toBe(false);
    expect(registry.lookup(1, "call-1")).toEqual({ kind: "handle", handle: "build" });
    expect(registry.lookup(1, "call-2")).toEqual({ kind: "unknown" });
  });

  it("scopes records to their run: a second run reusing the id never borrows the first run's handle", () => {
    const registry = new ToolHandleRegistry();
    registry.record(1, { type: "toolCall", id: "c1", handle: "build" });
    // Run 2 reuses the same call id against a non-roster target: no handle.
    expect(registry.lookup(2, "c1")).toEqual({ kind: "unknown" });
    // Run 2's own verified handle is its own, not a conflict with run 1's.
    expect(registry.record(2, { type: "toolCall", id: "c1", handle: "deploy" })).toBe(true);
    expect(registry.lookup(2, "c1")).toEqual({ kind: "handle", handle: "deploy" });
    expect(registry.lookup(1, "c1")).toEqual({ kind: "handle", handle: "build" });
    expect(registry.handles()).toEqual(["build", "deploy"]);
  });

  it("marks only the conflicting call ambiguous, never both runs' history", () => {
    const registry = new ToolHandleRegistry();
    registry.record(1, { type: "toolCall", id: "c1", handle: "build" });
    expect(registry.record(1, { type: "toolResult", id: "c1", handle: "deploy" })).toBe(true);
    expect(registry.lookup(1, "c1")).toEqual({ kind: "ambiguous" });
    // Ambiguity stays with that call: another run's same-id call is unaffected.
    registry.record(2, { type: "toolCall", id: "c1", handle: "deploy" });
    expect(registry.lookup(2, "c1")).toEqual({ kind: "handle", handle: "deploy" });
    // Later agreeing evidence never reopens the ambiguous call.
    expect(registry.record(1, { type: "toolResult", id: "c1", handle: "build" })).toBe(false);
    expect(registry.lookup(1, "c1")).toEqual({ kind: "ambiguous" });
    expect(registry.handles()).toEqual(["deploy"]);
  });

  it("keeps the toolCall's verified handle for a handle-less toolResult of the same run", () => {
    const registry = new ToolHandleRegistry();
    registry.record(3, { type: "toolCall", id: "c1", handle: "build" });
    expect(registry.record(3, { type: "toolResult", id: "c1" })).toBe(false);
    expect(registry.lookup(3, "c1")).toEqual({ kind: "handle", handle: "build" });
  });

  it("resolves history items without a run generation as unknown", () => {
    const registry = new ToolHandleRegistry();
    registry.record(1, { type: "toolCall", id: "c1", handle: "build" });
    expect(registry.lookup(null, "c1")).toEqual({ kind: "unknown" });
  });

  it("keeps calls isolated and clears on conversation switch", () => {
    const registry = new ToolHandleRegistry();
    registry.record(1, { type: "toolCall", id: "call-1", handle: "build" });
    registry.record(1, { type: "toolCall", id: "call-2", handle: "deploy" });
    expect(registry.lookup(1, "call-2")).toEqual({ kind: "handle", handle: "deploy" });
    registry.clear();
    expect(registry.lookup(1, "call-1")).toEqual({ kind: "unknown" });
    expect(registry.handles()).toEqual([]);
  });
});

describe("runSummaryFromAttempt", () => {
  it("reports a live run as running without evidence", () => {
    expect(runSummaryFromAttempt(null, undefined)).toEqual({ state: "running", evidence: null });
  });

  it("evidences terminal outcomes only from the run row's own finish record", () => {
    expect(
      runSummaryFromAttempt("done", { answer: "全部完成", finishedAt: 1_760_000_000_000 }),
    ).toEqual({
      state: "done",
      evidence: { kind: "output", observedAt: 1_760_000_000_000, summary: "全部完成" },
    });
    expect(
      runSummaryFromAttempt("error", { error: "编译失败", finishedAt: 1_760_000_000_000 }),
    ).toEqual({
      state: "error",
      evidence: { kind: "exit", observedAt: 1_760_000_000_000, summary: "编译失败" },
    });
    expect(runSummaryFromAttempt("canceled", { finishedAt: 1_760_000_000_000 })).toEqual({
      state: "canceled",
      evidence: { kind: "exit", observedAt: 1_760_000_000_000, summary: "已取消" },
    });
  });

  it("keeps terminal claims evidence-less without a finish record", () => {
    expect(runSummaryFromAttempt("done", undefined)).toEqual({ state: "done", evidence: null });
    expect(runSummaryFromAttempt("done", { answer: "完成但未结束" })).toEqual({
      state: "done",
      evidence: null,
    });
  });
});

describe("approvalFromConfirmCard", () => {
  function card(partial: Partial<ConfirmItem> = {}): ConfirmItem {
    return {
      id: "g1:confirm:call-1",
      attempt: 1,
      role: "confirm",
      jobId: "job-1",
      callId: "call-1",
      tool: "exec_commands",
      rendered: "$ ls",
      nonce: "n-1",
      requestId: "req-1",
      hitlAttempt: 2,
      ...partial,
    } as ConfirmItem;
  }

  it("builds a pending approval from the unresolved card", () => {
    expect(approvalFromConfirmCard(card())).toEqual({
      requestId: "req-1",
      nonce: "n-1",
      attempt: 2,
      decision: null,
    });
  });

  it("maps every stamped resolution back to its decision", () => {
    expect(approvalFromConfirmCard(card({ resolution: "已允许一次" }))?.decision).toBe("approve_once");
    expect(approvalFromConfirmCard(card({ resolution: "本轮已允许此类" }))?.decision).toBe("approve_session");
    expect(
      approvalFromConfirmCard(card({ resolution: "已永久允许（可随时在授权管理中撤销）" }))?.decision,
    ).toBe("approve_persistent");
    expect(approvalFromConfirmCard(card({ resolution: "已拒绝" }))?.decision).toBe("deny");
  });

  it("hides the approval when the request id is missing or the resolution is unmapped", () => {
    expect(approvalFromConfirmCard(card({ requestId: undefined }))).toBeNull();
    expect(approvalFromConfirmCard(card({ resolution: "别的解决方式" }))).toBeNull();
    expect(approvalFromConfirmCard(null)).toBeNull();
  });
});

describe("buildAgentToolCall", () => {
  function toolItem(partial: Partial<ToolItem> = {}): ToolItem {
    return {
      id: "g1:tool:call-1",
      attempt: 1,
      role: "tool",
      callId: "call-1",
      name: "exec_commands",
      display: "exec_commands",
      ...partial,
    };
  }

  it("joins the roster entry for host/cwd, production and presence", () => {
    const call = buildAgentToolCall(
      toolItem({ summary: "ok", ok: true, exitCode: 0 }),
      "build",
      rosterEntry(),
      null,
      null,
    );
    expect(call.handle).toEqual({ handle: "build", host: "web-01", cwd: "/srv/app" });
    expect(call.production).toBe(true);
    expect(call.persistent).toBe(true);
    expect(call.presence).toBe("attached");
    expect(call.isMine).toBe(true);
    expect(call.state).toBe("ok");
  });

  it("keeps every joined field honestly unknown when the row is missing", () => {
    const call = buildAgentToolCall(toolItem({ summary: "ok", ok: true }), "ghost", undefined, null, null);
    expect(call.handle).toEqual({ handle: "ghost", host: "", cwd: "" });
    expect(call.presence).toBe("unverifiable");
    // Missing roster evidence means unknown persistence, never a claimed
    // non-persistent target.
    expect(call.persistent).toBeNull();
    expect(call.production).toBe(false);
    expect(call.ownerName).toBe("");
  });

  it("maps the tool item outcome onto the card state", () => {
    expect(buildAgentToolCall(toolItem(), "build", undefined, null, null).state).toBe("running");
    expect(
      buildAgentToolCall(toolItem({ summary: "failed", ok: false }), "build", undefined, null, null).state,
    ).toBe("error");
    expect(
      buildAgentToolCall(toolItem({ summary: "?" }), "build", undefined, null, null).state,
    ).toBe("unknown");
  });
});
