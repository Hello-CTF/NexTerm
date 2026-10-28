import { beforeEach, describe, expect, it } from "vitest";
import {
  aggregateAttention,
  applyAwareness,
  awarenessBadge,
  needsAttention,
  resetAwarenessStore,
  tabAwareness,
  toAwareness,
  useAwarenessStore,
} from "../../app/sessionAwarenessStore";
import { jumpAttentionOf } from "../../app/jump/jumpAttention";
import type { JumpSessionEntry } from "../../app/jump/jumpSessions";
import { buildInbox } from "../../features/inbox/inbox";
import { buildStatusWall } from "../../features/mobile/statusWallModel";
import type { AppTab, Workspace } from "../../app/store";
import type { AggregateRow } from "../../app/sessionAggregate";
import type { TerminalAttentionEvent } from "../../ipc/events";

let version = 0;

function event(tabId: string, patch: Record<string, unknown>): TerminalAttentionEvent {
  version += 1;
  return {
    tabId,
    state: "needs_me",
    confidence: "signal",
    source: "helper",
    reason: "bell",
    since: "2026-10-27T10:00:00Z",
    version,
    ...patch,
  } as TerminalAttentionEvent;
}

// What a helper started before the screen-shape guess was removed still reports.
function oldHelper(tabId: string): TerminalAttentionEvent {
  return event(tabId, { confidence: "heuristic", reason: "prompt", prompt: "Continue? [y/N]", epoch: 3, inferred: true });
}

const tab: AppTab = { id: "ui1", kind: "terminal", title: "build", sessionId: "s1", tabId: "T1", closable: true };
const workspaces: Workspace[] = [
  { id: "ws1", kind: "session", title: "web-01", panes: [{ id: "p", tabs: [tab], activeTabId: "ui1" }], activePaneId: "p", closable: true },
];

beforeEach(() => {
  resetAwarenessStore();
});

describe("guessed attention from an old helper", () => {
  it("is ingested as quiet running state and never as attention", () => {
    applyAwareness([oldHelper("T1")]);
    const awareness = tabAwareness("T1");
    expect(awareness?.state).toBe("running");
    expect(needsAttention(awareness)).toBe(false);
    expect(awareness?.reason).toBeUndefined();
  });

  it("does not reach the inbox, the workspace badge, jump ranking or the status wall", () => {
    applyAwareness([oldHelper("T1")]);
    const byTabId = useAwarenessStore.getState().byTabId;
    expect(buildInbox(byTabId, workspaces)).toEqual([]);
    expect(aggregateAttention(byTabId, ["T1"])).toBeNull();
    expect(awarenessBadge(byTabId.get("T1"))?.kind).toBe("running");
    const entry = { terminalId: "T1" } as JumpSessionEntry;
    expect(jumpAttentionOf(entry)).toBeUndefined();
    expect(buildStatusWall(byTabId, workspaces, [], new Map()).groups.needs_me).toEqual([]);
  });

  it("does not reach the cross-host listing", () => {
    const row = {
      session: { tabId: "T9", title: "remote", attention: oldHelper("T9") },
      host: { key: "h", name: "db-01", stale: false },
    } as unknown as AggregateRow;
    expect(buildInbox(new Map(), [], [row])).toEqual([]);
    expect(buildStatusWall(new Map(), [], [row], new Map()).groups.needs_me).toEqual([]);
  });

  it("drops a needs_me with reason prompt or a heuristic just_finished", () => {
    expect(needsAttention(toAwareness(event("T1", { reason: "prompt" })))).toBe(false);
    expect(needsAttention(toAwareness(event("T1", { state: "just_finished", confidence: "heuristic", reason: "command_end" })))).toBe(false);
  });
});

describe("explicit attention", () => {
  it.each([
    ["bell", "signal"],
    ["notify", "signal"],
    ["rule", "rule"],
  ])("still asks for the user: %s", (reason, confidence) => {
    applyAwareness([event("T1", { reason, confidence })]);
    const byTabId = useAwarenessStore.getState().byTabId;
    expect(needsAttention(byTabId.get("T1"))).toBe(true);
    expect(buildInbox(byTabId, workspaces)).toHaveLength(1);
    expect(aggregateAttention(byTabId, ["T1"])?.kind).toBe("needs_me");
    expect(jumpAttentionOf({ terminalId: "T1" } as JumpSessionEntry)).toBe("needsMe");
    expect(buildStatusWall(byTabId, workspaces, [], new Map()).groups.needs_me).toHaveLength(1);
  });

  it("clears when the exit of the tab arrives", () => {
    applyAwareness([event("T1", { reason: "bell" })]);
    applyAwareness([event("T1", { state: "exited", reason: "exit" })]);
    expect(buildInbox(useAwarenessStore.getState().byTabId, [])).toEqual([]);
  });
});
