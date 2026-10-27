/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ attentionList: vi.fn(), attentionSeen: vi.fn() }));
vi.mock("../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: { attentionList: mocks.attentionList, attentionSeen: mocks.attentionSeen },
  vaultApi: {},
}));

import {
  ACTIVE_USER_WINDOW_MS,
  markVisibleAwarenessSeen,
  noteUserActivity,
  reloadAwareness,
  resetAwarenessForTest,
  setAwarenessEnvironmentForTest,
} from "../app/sessionAwareness";
import { applyAwareness, awarenessBadge, tabAwareness } from "../app/sessionAwarenessStore";
import { useUi, type AppTab, type Pane, type Workspace } from "../app/store";
import type { TerminalAttentionEvent } from "../ipc/events";

function terminal(id: string, tabId: string): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s", tabId, closable: true };
}

function ws(id: string, panes: Pane[]): Workspace {
  return { id, kind: "session", title: id, sessionId: "s", panes, activePaneId: panes[0].id, closable: true };
}

function event(tabId: string, version: number, patch: Partial<TerminalAttentionEvent> = {}): TerminalAttentionEvent {
  return { tabId, state: "needs_me", confidence: "signal", source: "helper", since: `2026-10-27T10:00:0${version}Z`, version, ...patch };
}

let visible = true;
let now = 1_000_000;

beforeEach(() => {
  vi.clearAllMocks();
  mocks.attentionSeen.mockResolvedValue(undefined);
  resetAwarenessForTest();
  visible = true;
  now = 1_000_000;
  setAwarenessEnvironmentForTest({ visible: () => visible, now: () => now });
  noteUserActivity();
  useUi.setState({
    workspaces: [
      ws("ws", [{ id: "p", tabs: [terminal("t1", "T1"), terminal("t2", "T2")], activeTabId: "t1" }]),
      ws("other", [{ id: "q", tabs: [terminal("t3", "T3")], activeTabId: "t3" }]),
    ],
    activeWorkspaceId: "ws",
    toasts: [],
  });
});

afterEach(() => {
  resetAwarenessForTest();
});

describe("session awareness store", () => {
  it("keeps the newest state per tab whatever order events arrive in", () => {
    applyAwareness([event("T2", 3, { state: "running" })]);
    applyAwareness([event("T2", 2, { state: "needs_me" })]);
    expect(tabAwareness("T2")?.state).toBe("running");
    applyAwareness([event("T2", 4, { state: "just_finished", exitCode: 0 })]);
    expect(tabAwareness("T2")).toMatchObject({ state: "just_finished", exitCode: 0, version: 4 });
  });

  it("lets a list entry without a version fill only an unknown tab", () => {
    applyAwareness([event("T2", 5)]);
    applyAwareness([{ tabId: "T2", state: "unknown", version: 0 }, { tabId: "T3", state: "unknown", version: 0 }]);
    expect(tabAwareness("T2")?.state).toBe("needs_me");
    expect(tabAwareness("T3")?.state).toBe("unknown");
  });

  it("shows inferred states lighter, hides seen and quiet states, and never guesses an unknown state", () => {
    expect(awarenessBadge(tabFrom(event("T", 1)))).toMatchObject({ kind: "needs_me", inferred: false });
    expect(awarenessBadge(tabFrom(event("T", 1, { confidence: "heuristic" })))).toMatchObject({ kind: "needs_me", inferred: true });
    expect(awarenessBadge(tabFrom(event("T", 1, { seen: true })))).toBeNull();
    expect(awarenessBadge(tabFrom(event("T", 1, { state: "just_finished", exitCode: 2 })))?.label).toContain("退出码 2");
    expect(awarenessBadge(tabFrom(event("T", 1, { state: "idle" })))).toBeNull();
    expect(awarenessBadge(tabFrom(event("T", 1, { state: "exited" })))).toBeNull();
    expect(awarenessBadge(tabFrom(event("T", 1, { state: "unknown", confidence: undefined })))?.kind).toBe("unknown");
    expect(awarenessBadge(tabFrom(event("T", 1, { state: "something_new" })))?.kind).toBe("unknown");
  });

  it("drops what an earlier backend reported when the list is reloaded", async () => {
    applyAwareness([event("T1", 9)]);
    mocks.attentionList.mockResolvedValue([event("T2", 1, { state: "running" })]);
    await reloadAwareness();
    expect(tabAwareness("T1")).toBeUndefined();
    expect(tabAwareness("T2")?.state).toBe("running");
  });
});

describe("seen state", () => {
  it("reports a needs_me state on a visible tab once, and not for tabs out of view", () => {
    applyAwareness([event("T1", 1), event("T2", 2), event("T3", 3)]);
    markVisibleAwarenessSeen();
    markVisibleAwarenessSeen();
    expect(mocks.attentionSeen).toHaveBeenCalledTimes(1);
    expect(mocks.attentionSeen).toHaveBeenCalledWith("T1", event("T1", 1).since);
  });

  it("does not report a state nobody looks at, nor one already seen elsewhere", () => {
    visible = false;
    applyAwareness([event("T1", 1)]);
    markVisibleAwarenessSeen();
    expect(mocks.attentionSeen).not.toHaveBeenCalled();
    visible = true;
    resetAwarenessForTest();
    noteUserActivity();
    applyAwareness([event("T1", 2, { seen: true })]);
    markVisibleAwarenessSeen();
    expect(mocks.attentionSeen).not.toHaveBeenCalled();
  });

  it("waits while the user is away and reports once they are active again", () => {
    now += ACTIVE_USER_WINDOW_MS + 1;
    applyAwareness([event("T1", 1)]);
    markVisibleAwarenessSeen();
    expect(mocks.attentionSeen).not.toHaveBeenCalled();
    now += 60_000;
    noteUserActivity();
    expect(mocks.attentionSeen).toHaveBeenCalledOnce();
    expect(mocks.attentionSeen).toHaveBeenCalledWith("T1", event("T1", 1).since);
  });

  it("keeps the mark on this device when the helper cannot share it, without asking again", async () => {
    mocks.attentionSeen.mockRejectedValue({
      code: "unsupported",
      message: "helper does not share seen attention states",
      detail: { reason: "seen_unsupported" },
    });
    applyAwareness([event("T1", 1)]);
    markVisibleAwarenessSeen();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(tabAwareness("T1")?.seen).toBe(true);
    expect(awarenessBadge(tabAwareness("T1"))).toBeNull();
    noteUserActivity();
    markVisibleAwarenessSeen();
    expect(mocks.attentionSeen).toHaveBeenCalledOnce();
  });

  it("retries an unsupported answer that is not seen_unsupported instead of marking it local-only", async () => {
    mocks.attentionSeen.mockRejectedValueOnce({ code: "unsupported", message: "durable terminal unavailable" });
    applyAwareness([event("T1", 1)]);
    markVisibleAwarenessSeen();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(tabAwareness("T1")?.seen).toBeFalsy();
    markVisibleAwarenessSeen();
    expect(mocks.attentionSeen).toHaveBeenCalledTimes(2);
  });

  it("reports the next state again and retries a report that failed", async () => {
    mocks.attentionSeen.mockRejectedValueOnce(new Error("offline"));
    applyAwareness([event("T1", 1, { state: "just_finished" })]);
    markVisibleAwarenessSeen();
    // Let the failed report settle before the next change.
    await new Promise((resolve) => setTimeout(resolve, 0));
    markVisibleAwarenessSeen();
    expect(mocks.attentionSeen).toHaveBeenCalledTimes(2);
    applyAwareness([event("T1", 2)]);
    markVisibleAwarenessSeen();
    expect(mocks.attentionSeen).toHaveBeenLastCalledWith("T1", event("T1", 2).since);
  });

  it("clears the badge on this device when another device saw the state", () => {
    applyAwareness([event("T2", 1)]);
    expect(awarenessBadge(tabAwareness("T2"))?.kind).toBe("needs_me");
    applyAwareness([event("T2", 2, { since: event("T2", 1).since, seen: true })]);
    expect(awarenessBadge(tabAwareness("T2"))).toBeNull();
  });
});

function tabFrom(raw: TerminalAttentionEvent) {
  applyAwareness([{ ...raw, version: (tabAwareness(raw.tabId)?.version ?? 0) + 1 }]);
  return tabAwareness(raw.tabId);
}
