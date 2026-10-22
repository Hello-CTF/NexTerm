/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it } from "vitest";
import {
  cycleActivePane,
  cycleActiveTab,
  cycleActiveWorkspace,
  useUi,
  type AppTab,
  type Pane,
  type Workspace,
} from "../../app/store";

function tab(id: string): AppTab {
  return { id, kind: "terminal", title: id, closable: true };
}

function pane(id: string, tabs: AppTab[] = []): Pane {
  return { id, tabs, activeTabId: tabs[0]?.id ?? null };
}

function workspace(id: string, panes: Pane[]): Workspace {
  return {
    id,
    kind: "session",
    title: id,
    panes,
    activePaneId: panes[0]?.id ?? "",
    splitRatio: 0.5,
    closable: true,
  };
}

beforeEach(() => {
  useUi.setState({ workspaces: [], activeWorkspaceId: null });
});

describe("workspace navigation", () => {
  it("cycles in both directions and recovers from a missing active workspace", () => {
    const workspaces = [
      workspace("ws-a", [pane("a-pane")]),
      workspace("ws-b", [pane("b-pane")]),
      workspace("ws-c", [pane("c-pane")]),
    ];
    useUi.setState({ workspaces, activeWorkspaceId: "ws-b" });

    expect(cycleActiveWorkspace(1)).toBe("ws-c");
    expect(cycleActiveWorkspace(1)).toBe("ws-a");
    expect(cycleActiveWorkspace(-1)).toBe("ws-c");
    expect(useUi.getState().activeWorkspaceId).toBe("ws-c");

    useUi.setState({ activeWorkspaceId: "missing" });
    expect(cycleActiveWorkspace(1)).toBe("ws-a");
    expect(useUi.getState().activeWorkspaceId).toBe("ws-a");

    useUi.setState({ workspaces: [], activeWorkspaceId: null });
    expect(cycleActiveWorkspace(1)).toBeNull();
    expect(cycleActiveWorkspace(-1)).toBeNull();
  });

  it("cycles tabs only in the active visible pane and skips a stale active tab id", () => {
    const upper = pane("upper", [tab("t1"), tab("t2"), tab("t3")]);
    const lower = pane("lower", [tab("other")]);
    const current = workspace("ws", [upper, lower]);
    useUi.setState({ workspaces: [current], activeWorkspaceId: "ws" });
    useUi.getState().setActiveTab("t2");

    expect(cycleActiveTab(1)).toBe("t3");
    expect(cycleActiveTab(1)).toBe("t1");
    expect(cycleActiveTab(-1)).toBe("t3");
    expect(useUi.getState().workspaces[0].activePaneId).toBe("upper");
    expect(useUi.getState().workspaces[0].panes[1].activeTabId).toBe("other");

    useUi.setState((state) => ({
      workspaces: state.workspaces.map((item) => ({
        ...item,
        panes: item.panes.map((candidate) =>
          candidate.id === "upper" ? { ...candidate, activeTabId: "missing" } : candidate,
        ),
      })),
    }));
    expect(cycleActiveTab(1)).toBe("t1");

    useUi.setState({
      workspaces: [workspace("empty", [pane("empty-pane")])],
      activeWorkspaceId: "empty",
    });
    expect(cycleActiveTab(1)).toBeNull();
    expect(cycleActiveTab(-1)).toBeNull();
  });

  it("cycles dual-pane focus and never selects an unrendered third pane", () => {
    const current = workspace("ws", [
      pane("upper", [tab("t1")]),
      pane("lower", [tab("t2")]),
      pane("hidden-third", [tab("t3")]),
    ]);
    useUi.setState({ workspaces: [current], activeWorkspaceId: "ws" });

    expect(cycleActivePane(1)).toBe("lower");
    expect(cycleActivePane(1)).toBe("upper");
    expect(cycleActivePane(-1)).toBe("lower");
    expect(cycleActivePane(-1)).toBe("upper");

    useUi.setState((state) => ({
      workspaces: state.workspaces.map((item) => ({ ...item, activePaneId: "hidden-third" })),
    }));
    expect(cycleActivePane(1)).toBe("lower");
    expect(useUi.getState().workspaces[0].activePaneId).toBe("lower");
  });
});
