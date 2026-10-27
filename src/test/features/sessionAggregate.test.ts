/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  sessionsAggregate: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, terminalApi: { ...actual.terminalApi, sessionsAggregate: mocks.sessionsAggregate } };
});

import type { Workspace } from "../../app/store";
import {
  AGGREGATE_FOLLOW_UP_MS,
  aggregateRows,
  applyAggregateList,
  applyAggregateUpdate,
  reloadSessionAggregate,
  resetSessionAggregate,
  resetSessionAggregateForTest,
  useSessionAggregate,
} from "../../app/sessionAggregate";
import { applyAwareness, resetAwarenessStore, useAwarenessStore } from "../../app/sessionAwarenessStore";
import { buildInbox } from "../../features/inbox/inbox";
import { aggregateBackgroundTabs, collectBackgroundTabs, visibleBackgroundTabs } from "../../features/terminal/backgroundSessionsAccess";
import type { SessionInfo } from "../../ipc/commands";
import type { AggregateHostStatus, AggregateList, AggregateSession, AttentionEvent } from "../../ipc/generated/types";

function host(key: string, patch: Partial<AggregateHostStatus> = {}): AggregateHostStatus {
  return { key, kind: "device", name: key, status: "ok", fetchedAt: 1, ...patch };
}

function attention(tabId: string, patch: Partial<AttentionEvent> = {}): AttentionEvent {
  return { tabId, state: "needs_me", confidence: "signal", source: "helper", reason: "bell", since: "2026-10-27T10:00:00Z", version: 0, ...patch };
}

function item(hostKey: string, tabId: string, patch: Partial<AggregateSession> = {}): AggregateSession {
  return { hostKey, tabId, title: `title-${tabId}`, cols: 80, rows: 24, attention: attention(tabId), ...patch };
}

function listing(hosts: AggregateHostStatus[], sessions: AggregateSession[]): AggregateList {
  return { hosts, sessions, complete: hosts.every((entry) => entry.status !== "pending") };
}

function workspace(id: string, title: string, tabId: string): Workspace {
  return {
    id, kind: "session", title, closable: true, activePaneId: `${id}-p`,
    panes: [{ id: `${id}-p`, activeTabId: `${id}-t`, tabs: [{ id: `${id}-t`, kind: "terminal", title: "open tab", sessionId: "s1", tabId, closable: true }] }],
  };
}

beforeEach(() => {
  resetSessionAggregateForTest();
  resetAwarenessStore();
  mocks.sessionsAggregate.mockReset();
});

afterEach(() => {
  resetSessionAggregateForTest();
  resetAwarenessStore();
});

describe("session aggregate store", () => {
  it("fills in hosts that answer later, including hosts the listing did not name yet", () => {
    applyAggregateList(listing([host("device:a"), host("device:b", { status: "pending", fetchedAt: undefined })], [item("device:a", "t1")]));
    applyAggregateUpdate({ host: host("device:b"), sessions: [item("device:b", "t2")] });
    applyAggregateUpdate({ host: host("device:new"), sessions: [item("device:new", "t3")] });
    const rows = aggregateRows(useSessionAggregate.getState());
    expect(rows.map((row) => [row.host.key, row.session.tabId])).toEqual([["device:a", "t1"], ["device:b", "t2"], ["device:new", "t3"]]);
    expect(useSessionAggregate.getState().hosts.get("device:b")?.status).toBe("ok");
  });

  it("never replaces a host with an older reading", () => {
    applyAggregateList(listing([host("device:a", { fetchedAt: 20 })], [item("device:a", "fresh")]));
    applyAggregateUpdate({ host: host("device:a", { fetchedAt: 10 }), sessions: [item("device:a", "old-event")] });
    applyAggregateList(listing([host("device:a", { fetchedAt: 5 })], [item("device:a", "old-reply")]));
    expect(aggregateRows(useSessionAggregate.getState()).map((row) => row.session.tabId)).toEqual(["fresh"]);
  });

  it("applies events that arrive during a reload after its reply", async () => {
    let answer: (list: AggregateList) => void = () => {};
    mocks.sessionsAggregate.mockReturnValueOnce(new Promise<AggregateList>((resolve) => { answer = resolve; }));
    const pending = reloadSessionAggregate();
    applyAggregateUpdate({ host: host("device:a", { fetchedAt: 30 }), sessions: [item("device:a", "from-event")] });
    expect(useSessionAggregate.getState().hosts.size).toBe(0);
    answer(listing([host("device:a", { status: "pending", fetchedAt: undefined })], []));
    await pending;
    expect(aggregateRows(useSessionAggregate.getState()).map((row) => row.session.tabId)).toEqual(["from-event"]);
  });

  it("asks again shortly while hosts are still pending", async () => {
    vi.useFakeTimers();
    try {
      mocks.sessionsAggregate
        .mockResolvedValueOnce(listing([host("device:slow", { status: "pending", fetchedAt: undefined })], []))
        .mockResolvedValueOnce(listing([host("device:slow")], [item("device:slow", "t1")]));
      await reloadSessionAggregate();
      expect(mocks.sessionsAggregate).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(AGGREGATE_FOLLOW_UP_MS);
      expect(mocks.sessionsAggregate).toHaveBeenCalledTimes(2);
      expect(aggregateRows(useSessionAggregate.getState()).map((row) => row.session.tabId)).toEqual(["t1"]);
      await vi.advanceTimersByTimeAsync(AGGREGATE_FOLLOW_UP_MS * 2);
      expect(mocks.sessionsAggregate).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("lists a session reached through two hosts once", () => {
    applyAggregateList(listing([host("local"), host("device:self")], [item("local", "same"), item("device:self", "same")]));
    expect(aggregateRows(useSessionAggregate.getState()).map((row) => row.host.key)).toEqual(["local"]);
  });

  it("drops a reply that an account switch overtook", async () => {
    let answer: (list: AggregateList) => void = () => {};
    mocks.sessionsAggregate.mockReturnValueOnce(new Promise<AggregateList>((resolve) => { answer = resolve; }));
    const pending = reloadSessionAggregate();
    resetSessionAggregate();
    answer(listing([host("device:old-account")], [item("device:old-account", "t1")]));
    await pending;
    expect(useSessionAggregate.getState().hosts.size).toBe(0);
    expect(mocks.sessionsAggregate).toHaveBeenCalledWith(1500);
  });
});

describe("inbox with the cross-host listing", () => {
  it("lists a session that needs the user on a host no tab here shows", () => {
    const rows = aggregateRows({
      hosts: new Map([["device:box", host("device:box", { name: "build box", deviceId: "box" })]]),
      sessionsByHost: new Map([["device:box", [item("device:box", "remote", { title: "deploy" }), item("device:box", "quiet", { attention: attention("quiet", { state: "running" }) })]]]),
    });
    const entries = buildInbox(new Map(), [], rows);
    expect(entries).toHaveLength(1);
    expect(entries[0]).toMatchObject({ tabId: "remote", title: "deploy", place: "build box", uiTabId: undefined });
    expect(entries[0].host?.deviceId).toBe("box");
  });

  it("prefers the state a tab here reports over the listing", () => {
    applyAwareness([attention("held", { state: "running", version: 3 })]);
    const rows = aggregateRows({
      hosts: new Map([["local", host("local", { kind: "local", name: "本机" })]]),
      sessionsByHost: new Map([["local", [item("local", "held")]]]),
    });
    expect(buildInbox(useAwarenessStore.getState().byTabId, [workspace("ws", "web-01", "held")], rows)).toEqual([]);
  });

  it("names the host of a session the awareness store knows but no tab here shows", () => {
    applyAwareness([attention("elsewhere", { version: 2 })]);
    const rows = aggregateRows({
      hosts: new Map([["device:box", host("device:box", { name: "build box", deviceId: "box" })]]),
      sessionsByHost: new Map([["device:box", [item("device:box", "elsewhere", { title: "deploy" })]]]),
    });
    const [entry] = buildInbox(useAwarenessStore.getState().byTabId, [], rows);
    expect(entry).toMatchObject({ tabId: "elsewhere", title: "deploy", place: "build box" });
    expect(entry.host?.deviceId).toBe("box");
    expect(entry.listedOnly).toBeUndefined();
  });

  it("leaves out a state that was seen on another device", () => {
    const rows = aggregateRows({
      hosts: new Map([["device:box", host("device:box")]]),
      sessionsByHost: new Map([["device:box", [item("device:box", "seen", { attention: attention("seen", { seen: true }) })]]]),
    });
    expect(buildInbox(new Map(), [], rows)).toEqual([]);
  });
});

describe("background list with the cross-host listing", () => {
  const ssh: SessionInfo = { id: "s-ssh", assetId: "a-ssh", name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 0 };

  it("adds sessions of hosts it did not list directly and keeps them in the all-hosts view", () => {
    const rows = aggregateRows({
      hosts: new Map([
        ["asset:a-ssh", host("asset:a-ssh", { kind: "ssh", name: "web-01", sessionId: "s-ssh" })],
        ["device:box", host("device:box", { name: "build box", deviceId: "box" })],
      ]),
      sessionsByHost: new Map([
        ["asset:a-ssh", [item("asset:a-ssh", "direct")]],
        ["device:box", [item("device:box", "far", { attached: true }), item("device:box", "opened"), item("device:box", "dead", { dead: true })]],
      ]),
    });
    const scope = { sessions: [ssh], workspaces: [], verified: {} };
    const openTabIds = new Set(["opened"]);
    const collected = collectBackgroundTabs(scope, { live: [], remote: [{ session: ssh, tabs: [] }], openTabIds });
    const extra = aggregateBackgroundTabs(rows, { ...collected, openTabIds });
    expect(extra.map((tab) => tab.tabId)).toEqual(["far"]);
    const far = extra[0];
    expect(far).toMatchObject({ sessionId: "", sessionName: "build box", state: "needs_me", attachedElsewhere: true });
    expect(far.aggregateHost?.deviceId).toBe("box");
    expect(visibleBackgroundTabs(extra, scope, { hostSessionId: null, showAll: true, blocked: false })).toHaveLength(1);
  });
});
