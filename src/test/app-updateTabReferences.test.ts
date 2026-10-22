/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));

import { useUi, type AppTab, type Pane, type Workspace } from "../app/store";

function terminal(id: string): AppTab {
  return { id, kind: "terminal", title: id, sessionId: `session-${id}`, tabId: `kernel-${id}`, closable: true };
}

function pane(id: string, tabPrefix: string): Pane {
  return {
    id,
    tabs: [terminal(`${tabPrefix}-a`), terminal(`${tabPrefix}-b`)],
    activeTabId: `${tabPrefix}-a`,
  };
}

function workspace(id: string, index: number): Workspace {
  const panes = [pane(`${id}-pane-a`, `${id}-a`), pane(`${id}-pane-b`, `${id}-b`)];
  return {
    id,
    kind: "session",
    title: `workspace-${index}`,
    sessionId: `workspace-session-${index}`,
    panes,
    activePaneId: panes[0].id,
    splitRatio: 0.5,
    closable: true,
  };
}

function changedReferences(before: Workspace[], after: Workspace[]) {
  const previousWorkspaces = new Map(before.map((item) => [item.id, item]));
  const previousPanes = new Map(before.flatMap((item) => item.panes).map((item) => [item.id, item]));
  const previousTabs = new Map(
    before.flatMap((item) => item.panes).flatMap((item) => item.tabs).map((item) => [item.id, item]),
  );
  return {
    workspaces: after.filter((item) => previousWorkspaces.get(item.id) !== item).length,
    panes: after.flatMap((item) => item.panes).filter((item) => previousPanes.get(item.id) !== item).length,
    tabs: after
      .flatMap((item) => item.panes)
      .flatMap((item) => item.tabs)
      .filter((item) => previousTabs.get(item.id) !== item).length,
  };
}

beforeEach(() => {
  useUi.setState({
    workspaces: [workspace("ws-a", 0), workspace("ws-b", 1), workspace("ws-c", 2)],
    activeWorkspaceId: "ws-a",
  });
});

describe("updateTab reference stability", () => {
  it("rebuilds only the target workspace, pane, and tab", () => {
    const before = useUi.getState().workspaces;

    useUi.getState().updateTab("ws-b-b-b", { title: "renamed" });

    const after = useUi.getState().workspaces;
    const changed = changedReferences(before, after);
    const env = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env;
    if (env?.NEXTERM_PERF_MICRO === "1") {
      console.warn(JSON.stringify({ scenario: "updateTab-title", total: { workspaces: 3, panes: 6, tabs: 12 }, changed }));
    }
    expect(changed).toEqual({ workspaces: 1, panes: 1, tabs: 1 });
    expect(after[0]).toBe(before[0]);
    expect(after[2]).toBe(before[2]);
    expect(after[1]).not.toBe(before[1]);
    expect(after[1].panes[0]).toBe(before[1].panes[0]);
    expect(after[1].panes[1]).not.toBe(before[1].panes[1]);
    expect(after[1].panes[1].tabs[0]).toBe(before[1].panes[1].tabs[0]);
    expect(after[1].panes[1].tabs[1]).not.toBe(before[1].panes[1].tabs[1]);
    expect(after[1].panes[1].tabs[1].title).toBe("renamed");
  });
});
