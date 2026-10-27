/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";

const mocks = vi.hoisted(() => ({
  sessionsAggregate: vi.fn(),
  listSessions: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    sessionApi: { list: mocks.listSessions },
    terminalApi: { sessionsAggregate: mocks.sessionsAggregate },
  });
});

import { StatusWall, PULL_REFRESH_PX } from "../../features/mobile/StatusWall";
import { buildStatusWall, WALL_GROUPS } from "../../features/mobile/statusWall";
import { aggregateRows, applyAggregateList, applyAggregateUpdate, resetSessionAggregateForTest } from "../../app/sessionAggregate";
import { applyAwareness, resetAwarenessStore, toAwareness, type SessionAwareness } from "../../app/sessionAwarenessStore";
import { useUi, type AppTab, type Workspace } from "../../app/store";
import { flush, mount, waitFor, type MountedView } from "./reactTestUtils";
import type { SessionInfo } from "../../ipc/commands";
import type { AggregateHostStatus, AggregateList, AggregateSession, AttentionEvent } from "../../ipc/generated/types";

const NOW = Date.parse("2026-10-27T12:00:00Z");
const ssh: SessionInfo = { id: "s1", assetId: "asset-web", name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 0 };

function attention(tabId: string, state: string, minutesAgo: number, patch: Partial<AttentionEvent> = {}): AttentionEvent {
  return { tabId, state, confidence: "signal", source: "helper", since: new Date(NOW - minutesAgo * 60_000).toISOString(), version: 1, ...patch };
}

function host(key: string, patch: Partial<AggregateHostStatus> = {}): AggregateHostStatus {
  return { key, kind: "device", name: key, status: "ok", fetchedAt: NOW, ...patch };
}

function item(hostKey: string, tabId: string, state: string, minutesAgo: number, patch: Partial<AggregateSession> = {}): AggregateSession {
  return { hostKey, tabId, title: `任务 ${tabId}`, cols: 80, rows: 24, attention: attention(tabId, state, minutesAgo), ...patch };
}

function terminal(id: string, tabId: string, patch: Partial<AppTab> = {}): AppTab {
  return { id, kind: "terminal", title: `标签 ${id}`, sessionId: "s1", tabId, closable: true, ...patch };
}

function workspace(id: string, title: string, tabs: AppTab[]): Workspace {
  return { id, kind: "session", title, sessionId: "s1", closable: true, activePaneId: `${id}-p`, panes: [{ id: `${id}-p`, tabs, activeTabId: tabs[0]?.id ?? null }] };
}

function listing(hosts: AggregateHostStatus[], sessions: AggregateSession[]): AggregateList {
  return { hosts, sessions, complete: hosts.every((entry) => entry.status !== "pending") };
}

function cards(view: MountedView, group: string): string[] {
  return [...view.container.querySelectorAll<HTMLElement>(`[data-wall-group="${group}"] [data-wall-card]`)].map((node) => node.dataset.wallCard ?? "");
}

function groupOrder(view: MountedView): string[] {
  return [...view.container.querySelectorAll<HTMLElement>("[data-wall-group]")].map((node) => node.dataset.wallGroup ?? "");
}

function cardButton(view: MountedView, tabId: string): HTMLButtonElement {
  const found = view.container.querySelector<HTMLButtonElement>(`[data-wall-card="${tabId}"] button`);
  if (!found) throw new Error(`card ${tabId} not found`);
  return found;
}

function touch(target: Element, type: string, clientY?: number): void {
  const event = new Event(type, { bubbles: true });
  Object.defineProperty(event, "touches", { value: clientY === undefined ? [] : [{ clientY }] });
  act(() => {
    target.dispatchEvent(event);
  });
}

beforeEach(() => {
  vi.resetAllMocks();
  vi.spyOn(Date, "now").mockReturnValue(NOW);
  resetSessionAggregateForTest();
  resetAwarenessStore();
  mocks.sessionsAggregate.mockResolvedValue(listing([], []));
  mocks.listSessions.mockResolvedValue([ssh]);
  useUi.setState({ workspaces: [], activeWorkspaceId: null, sessions: [ssh], toasts: [] });
});

afterEach(() => {
  vi.restoreAllMocks();
  resetSessionAggregateForTest();
  resetAwarenessStore();
});

describe("status wall model", () => {
  it("groups by needs me, running, finished and unknown, newest first, each session once", () => {
    const awareness = new Map<string, SessionAwareness>([
      ["held-run", toAwareness(attention("held-run", "running", 9))],
      // The listing also reports this tab; the tab's own state wins.
      ["held-ask", toAwareness(attention("held-ask", "needs_me", 1))],
    ]);
    const workspaces = [workspace("ws", "web-01", [
      terminal("t1", "held-run"),
      terminal("t2", "held-ask"),
      terminal("t3", "held-done", { exited: true }),
      terminal("t4", "held-unknown"),
    ])];
    const hosts = new Map([
      ["device:box", host("device:box", { name: "build box", deviceId: "box" })],
      ["device:off", host("device:off", { name: "laptop", status: "offline", fetchedAt: undefined })],
    ]);
    const rows = aggregateRows({
      hosts,
      sessionsByHost: new Map([["device:box", [
        item("device:box", "held-ask", "running", 30),
        item("device:box", "far-ask", "needs_me", 5),
        item("device:box", "far-idle", "idle", 2),
        item("device:box", "far-done", "just_finished", 3, { attention: attention("far-done", "just_finished", 3, { exitCode: 2, seen: true }) }),
      ]]]),
    });
    const model = buildStatusWall(awareness, workspaces, rows, hosts);
    expect(Object.keys(model.groups)).toEqual([...WALL_GROUPS]);
    expect(model.groups.needs_me.map((card) => card.tabId)).toEqual(["held-ask", "far-ask"]);
    expect(model.groups.running.map((card) => card.tabId)).toEqual(["far-idle", "held-run"]);
    expect(model.groups.finished.map((card) => card.tabId)).toEqual(["far-done", "held-done"]);
    expect(model.groups.unknown.map((card) => card.tabId)).toEqual(["held-unknown"]);
    const far = model.groups.needs_me[1];
    expect(far).toMatchObject({ hostName: "build box", title: "任务 far-ask", uiTabId: undefined, stateLabel: "需要我" });
    expect(far.badge?.kind).toBe("needs_me");
    expect(far.host?.deviceId).toBe("box");
    // A seen finished command keeps its badge on the wall, with its outcome.
    expect(model.groups.finished[0].badge).toMatchObject({ kind: "just_finished", outcome: "failed" });
    expect(model.hosts).toEqual([{ key: "device:off", name: "laptop", status: "offline", error: undefined }]);
  });
});

describe("StatusWall", () => {
  it("renders a needs-me session from the known listing on its first render, on top", () => {
    applyAggregateList(listing([host("device:box", { name: "build box", deviceId: "box" })], [
      item("device:box", "busy", "running", 1),
      item("device:box", "ask", "needs_me", 4),
    ]));
    const view = mount(createElement(StatusWall, { onOpened: vi.fn() }));
    try {
      // No await: what is known shows at once, without waiting for any host.
      expect(groupOrder(view)).toEqual(["needs_me", "running"]);
      expect(cards(view, "needs_me")).toEqual(["ask"]);
      const card = view.container.querySelector<HTMLElement>('[data-wall-card="ask"]')!;
      expect(card.querySelector('[data-testid="wall-host"]')?.textContent).toBe("build box");
      expect(card.querySelector('[data-testid="wall-state"]')?.textContent).toBe("需要我");
      expect(card.querySelector('[data-testid="wall-time"]')?.textContent).toBe("4 分钟前");
      expect(card.querySelector('[data-attention="needs_me"]')).not.toBeNull();
      expect(mocks.sessionsAggregate).not.toHaveBeenCalled();
    } finally {
      view.unmount();
    }
  });

  it("asks for the listing when none is known yet and shows sessions as hosts answer", async () => {
    let answer: (list: AggregateList) => void = () => {};
    mocks.sessionsAggregate.mockReturnValueOnce(new Promise<AggregateList>((resolve) => { answer = resolve; }));
    const view = mount(createElement(StatusWall, { onOpened: vi.fn() }));
    try {
      expect(view.container.querySelector('[data-testid="wall-empty"]')?.textContent).toContain("正在读取");
      await act(async () => {
        answer(listing([host("device:a", { name: "A" }), host("device:b", { name: "B", status: "pending", fetchedAt: undefined })], [item("device:a", "one", "running", 1)]));
        await flush();
      });
      expect(cards(view, "running")).toEqual(["one"]);
      expect(view.container.querySelector('[data-testid="wall-progress"]')?.textContent).toBe("正在查询 1 台主机…");
      act(() => applyAggregateUpdate({ host: host("device:b", { name: "B" }), sessions: [item("device:b", "two", "needs_me", 1)] }));
      expect(cards(view, "needs_me")).toEqual(["two"]);
    } finally {
      view.unmount();
    }
  });

  it("keeps cards in place while shown, moves a changed state to its group at once, and re-sorts on refresh", async () => {
    applyAggregateList(listing([host("device:box")], [
      item("device:box", "older", "running", 10),
      item("device:box", "newer", "running", 1),
    ]));
    const view = mount(createElement(StatusWall, { onOpened: vi.fn() }));
    try {
      expect(cards(view, "running")).toEqual(["newer", "older"]);
      // older gets fresh output; it stays where it is while the wall is shown.
      act(() => applyAwareness([attention("older", "running", 0, { version: 2 })]));
      expect(cards(view, "running")).toEqual(["newer", "older"]);
      act(() => applyAwareness([attention("newer", "needs_me", 0, { version: 2 })]));
      expect(cards(view, "needs_me")).toEqual(["newer"]);
      expect(cards(view, "running")).toEqual(["older"]);
      mocks.sessionsAggregate.mockResolvedValue(listing([host("device:box")], [
        item("device:box", "older", "running", 0),
        item("device:box", "newer", "needs_me", 0),
        item("device:box", "third", "running", 5),
      ]));
      const scroller = view.container.querySelector("[data-status-wall] > div.overflow-y-auto")!;
      touch(scroller, "touchstart", 100);
      touch(scroller, "touchmove", 100 + PULL_REFRESH_PX + 4);
      expect(view.container.querySelector('[data-testid="wall-pull"]')?.textContent).toBe("松开刷新");
      touch(scroller, "touchend");
      await flush();
      expect(mocks.sessionsAggregate).toHaveBeenCalledTimes(1);
      expect(cards(view, "running")).toEqual(["older", "third"]);
    } finally {
      view.unmount();
    }
  });

  it("opens a session a tab here holds, and takes over one known only from the listing", async () => {
    useUi.setState({ workspaces: [workspace("ws", "web-01", [terminal("other", "x"), terminal("held", "held-tab")])], activeWorkspaceId: "ws" });
    applyAwareness([attention("held-tab", "needs_me", 1)]);
    applyAggregateList(listing([host("asset:web", { kind: "ssh", name: "web-01", sessionId: "s1" })], [item("asset:web", "far-tab", "needs_me", 2)]));
    const onOpened = vi.fn();
    const view = mount(createElement(StatusWall, { onOpened }));
    try {
      act(() => cardButton(view, "held-tab").click());
      expect(onOpened).toHaveBeenCalledTimes(1);
      expect(useUi.getState().workspaces[0].panes[0].activeTabId).toBe("held");

      await act(async () => {
        cardButton(view, "far-tab").click();
        await flush();
      });
      await waitFor(() => expect(onOpened).toHaveBeenCalledTimes(2));
      const tabs = useUi.getState().workspaces.flatMap((entry) => entry.panes.flatMap((pane) => pane.tabs));
      expect(tabs.some((tab) => tab.kind === "terminal" && tab.tabId === "far-tab" && tab.sessionId === "s1")).toBe(true);
    } finally {
      view.unmount();
    }
  });

  it("names hosts whose sessions are unknown and leaves room for card actions", () => {
    applyAggregateList(listing([
      host("device:box", { name: "build box" }),
      host("device:off", { name: "laptop", status: "offline", fetchedAt: undefined }),
      host("asset:db", { kind: "ssh", name: "db-02", sessionId: "s9", status: "offline", errorCode: "helper_not_ready", fetchedAt: undefined }),
    ], [item("device:box", "ask", "needs_me", 1)]));
    const view = mount(createElement(StatusWall, {
      onOpened: vi.fn(),
      cardActions: (card) => (card.state === "needs_me" ? createElement("button", { type: "button" }, "允许") : null),
    }));
    try {
      const offline = view.container.querySelector('[data-wall-host="device:off"]');
      expect(offline?.textContent).toContain("laptop");
      expect(offline?.textContent).toContain("会话状态未知");
      expect(view.container.querySelector('[data-wall-host="asset:db"]')?.textContent).toContain("在这台主机上打开一个终端");
      expect(view.container.querySelector('[data-wall-card="ask"] [data-wall-actions] button')?.textContent).toBe("允许");
    } finally {
      view.unmount();
    }
  });

  it("guides an empty wall to the assets", async () => {
    applyAggregateList(listing([], []));
    const onOpenAssets = vi.fn();
    const view = mount(createElement(StatusWall, { onOpened: vi.fn(), onOpenAssets }));
    try {
      expect(view.container.querySelector('[data-testid="wall-empty"]')?.textContent).toContain("还没有会话");
      act(() => [...view.container.querySelectorAll("button")].find((button) => button.textContent === "打开资产")?.click());
      expect(onOpenAssets).toHaveBeenCalledTimes(1);
    } finally {
      view.unmount();
    }
  });
});
