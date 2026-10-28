/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it } from "vitest";
import {
  cycleActiveTab,
  focusActivePane,
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

  it("moves pane focus by on-screen geometry and stops at the edges", () => {
    // [ left | [ top-right / bottom-right ] ]
    const current: Workspace = {
      ...workspace("ws", [pane("left", [tab("t1")]), pane("top-right", [tab("t2")]), pane("bottom-right", [tab("t3")])]),
      layoutTree: {
        type: "split",
        id: "root",
        dir: "row",
        ratio: 0.5,
        a: { type: "leaf", id: "leaf-left", paneId: "left" },
        b: {
          type: "split",
          id: "right",
          dir: "col",
          ratio: 0.5,
          a: { type: "leaf", id: "leaf-top-right", paneId: "top-right" },
          b: { type: "leaf", id: "leaf-bottom-right", paneId: "bottom-right" },
        },
      },
    };
    useUi.setState({ workspaces: [current], activeWorkspaceId: "ws" });

    expect(focusActivePane("left")).toBeNull();
    expect(focusActivePane("right")).toBe("top-right");
    expect(focusActivePane("down")).toBe("bottom-right");
    expect(focusActivePane("down")).toBeNull();
    expect(focusActivePane("left")).toBe("left");
    expect(useUi.getState().workspaces[0].activePaneId).toBe("left");
  });

  it("treats a workspace without a stored tree as panes stacked top to bottom", () => {
    useUi.setState({
      workspaces: [workspace("ws", [pane("upper", [tab("t1")]), pane("lower", [tab("t2")])])],
      activeWorkspaceId: "ws",
    });
    expect(focusActivePane("down")).toBe("lower");
    expect(focusActivePane("right")).toBeNull();
    expect(focusActivePane("up")).toBe("upper");
    useUi.setState({ workspaces: [], activeWorkspaceId: null });
    expect(focusActivePane("up")).toBeNull();
  });
});
