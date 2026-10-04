/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoice: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, askChoice: mocks.askChoice }));
vi.mock("../../ipc/commands", () => ({
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));

import type { AppTab, Pane, Workspace } from "../../app/store";
import { openFileTabInSplit, useUi } from "../../app/store";

const innerHeightDescriptor = Object.getOwnPropertyDescriptor(window, "innerHeight");

function stubInnerHeight(value: number): void {
  Object.defineProperty(window, "innerHeight", { configurable: true, writable: true, value });
}

function fileTab(path = "/home/deploy/.bashrc"): AppTab {
  return { id: `file-s-${path}`, kind: "files", title: ".bashrc", sessionId: "s", path, closable: true };
}

function ws(panes: Pane[]): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "workspace",
    sessionId: "s",
    panes,
    activePaneId: panes[0].id,
    splitRatio: 0.5,
    closable: true,
  };
}

function paneWithTerminal(): Pane {
  return {
    id: "p1",
    tabs: [{ id: "term", kind: "terminal", title: "term", sessionId: "s", tabId: "kernel", closable: true }],
    activeTabId: "term",
  };
}

describe("split height guard in the shared lower-pane entry", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useUi.setState({ workspaces: [], activeWorkspaceId: null, toasts: [] });
  });

  afterEach(() => {
    if (innerHeightDescriptor) {
      Object.defineProperty(window, "innerHeight", innerHeightDescriptor);
    }
  });

  it("falls back to the current pane with an explicit toast at insufficient height", () => {
    stubInnerHeight(320);
    useUi.setState({
      workspaces: [ws([paneWithTerminal()])],
      activeWorkspaceId: "ws",
    });
    const tab = fileTab();
    useUi.getState().openInLowerPane(tab, "ws");
    const state = useUi.getState();
    expect(state.workspaces[0].panes).toHaveLength(1);
    expect(state.workspaces[0].panes[0].tabs.map((t) => t.id)).toEqual(["term", tab.id]);
    expect(state.workspaces[0].activePaneId).toBe("p1");
    expect(state.toasts.some((t) => t.text.includes("已在当前栏打开"))).toBe(true);
  });

  it("routes the public openFileTabInSplit entry through the same fallback", () => {
    stubInnerHeight(320);
    useUi.setState({
      workspaces: [ws([paneWithTerminal()])],
      activeWorkspaceId: "ws",
    });
    openFileTabInSplit("s", "/home/deploy/.bashrc");
    const state = useUi.getState();
    expect(state.workspaces[0].panes).toHaveLength(1);
    expect(state.workspaces[0].panes[0].tabs.some((t) => t.kind === "files" && t.path === "/home/deploy/.bashrc")).toBe(true);
    expect(state.toasts.some((t) => t.text.includes("已在当前栏打开"))).toBe(true);
  });

  it("creates the lower pane at sufficient height without a fallback toast", () => {
    stubInnerHeight(844);
    useUi.setState({
      workspaces: [ws([paneWithTerminal()])],
      activeWorkspaceId: "ws",
    });
    const tab = fileTab();
    useUi.getState().openInLowerPane(tab, "ws");
    const state = useUi.getState();
    expect(state.workspaces[0].panes).toHaveLength(2);
    expect(state.workspaces[0].panes[1].tabs.map((t) => t.id)).toEqual([tab.id]);
    expect(state.workspaces[0].activePaneId).toBe(state.workspaces[0].panes[1].id);
    expect(state.toasts).toHaveLength(0);
  });

  it("targets the existing lower pane even at insufficient height", () => {
    stubInnerHeight(320);
    const panes = [paneWithTerminal(), { id: "p2", tabs: [], activeTabId: null }];
    useUi.setState({
      workspaces: [ws(panes)],
      activeWorkspaceId: "ws",
    });
    const tab = fileTab();
    useUi.getState().openInLowerPane(tab, "ws");
    const state = useUi.getState();
    expect(state.workspaces[0].panes).toHaveLength(2);
    expect(state.workspaces[0].panes[1].tabs.map((t) => t.id)).toEqual([tab.id]);
    expect(state.toasts).toHaveLength(0);
  });

  it("keeps unsplit available at insufficient height", async () => {
    stubInnerHeight(320);
    const panes = [paneWithTerminal(), { id: "p2", tabs: [], activeTabId: null }];
    useUi.setState({
      workspaces: [ws(panes)],
      activeWorkspaceId: "ws",
    });
    await useUi.getState().unsplitWorkspace("p2", "ws");
    expect(useUi.getState().workspaces[0].panes).toHaveLength(1);
  });
});
