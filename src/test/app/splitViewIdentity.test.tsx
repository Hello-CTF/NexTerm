/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, useEffect } from "react";
import { flush, mount, type MountedView } from "../features/reactTestUtils";

const mounts = vi.hoisted(() => ({ count: new Map<string, number>(), unmounts: new Map<string, number>() }));

vi.mock("../../ui/dialogs", () => ({ ask: vi.fn(async () => true), askChoice: vi.fn() }));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    terminalApi: { closeTab: vi.fn(async () => undefined), listLive: vi.fn(async () => []) },
  });
});

// The terminal stub counts mounts and unmounts per tab: a remount would mean a reconnect in the app.
vi.mock("../../app/lazyPanels", () => {
  const stub = (name: string) => () => createElement("div", { "data-panel": name });
  function TerminalPane({ storeTabId }: { storeTabId: string }) {
    useEffect(() => {
      mounts.count.set(storeTabId, (mounts.count.get(storeTabId) ?? 0) + 1);
      return () => {
        mounts.unmounts.set(storeTabId, (mounts.unmounts.get(storeTabId) ?? 0) + 1);
      };
    }, [storeTabId]);
    return createElement("div", { "data-terminal": storeTabId });
  }
  return {
    TerminalPane,
    AiSidebar: stub("ai"),
    AssetTree: stub("assets"),
    CredentialsSidebar: stub("credentials"),
    FileTree: stub("file-tree"),
    BackgroundSessions: stub("background"),
    TranscriptHistoryPanel: stub("history"),
    FileBrowser: stub("browser"),
    MountPanel: stub("mount"),
    FileEditor: stub("editor"),
    LogViewer: stub("log"),
    ForwardPanel: stub("forward"),
    MultiHostLogTail: stub("multi-log"),
    FleetHealthPanel: stub("fleet"),
    DockerPanel: stub("docker"),
    DbPanel: stub("db"),
    ExecBatchPanel: stub("exec"),
    SettingsView: stub("settings"),
    AuditView: stub("audit"),
    CredentialsPanel: stub("credentials-panel"),
  };
});

import { useUi, type AppTab, type Workspace } from "../../app/store";
import { WorkspaceBody, type WorkspaceBodyProps } from "../../app/shell/WorkspaceBody";

let view: MountedView | null = null;

function terminal(id: string): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s", tabId: `kernel-${id}`, closable: true };
}

function workspace(): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "host",
    sessionId: "s",
    panes: [{ id: "p1", tabs: [terminal("t1")], activeTabId: "t1" }],
    activePaneId: "p1",
    closable: true,
  };
}

function props(singlePane: boolean): WorkspaceBodyProps {
  return {
    ws: useUi.getState().workspaces[0],
    visibleWorkspaces: useUi.getState().workspaces,
    activeTab: null,
    activeSessionId: "s",
    overlaySidebars: false,
    splitAllowed: true,
    singlePane,
    docks: {
      overlayDock: null,
      setOverlayDock: () => undefined,
      leftDockOpen: false,
      rightDockOpen: false,
      setLeftOpen: () => undefined,
      setRightOpen: () => undefined,
    },
    onNewTerminal: () => undefined,
    onLocalTerminal: () => undefined,
    onOpenPalette: () => undefined,
  };
}

function render(singlePane = false): void {
  if (!view) view = mount(createElement(WorkspaceBody, props(singlePane)));
  else act(() => view!.root.render(createElement(WorkspaceBody, props(singlePane))));
}

function terminalNode(id: string): HTMLElement | null {
  return view!.container.querySelector<HTMLElement>(`[data-terminal="${id}"]`);
}

function paneNode(paneId: string): HTMLElement {
  return view!.container.querySelector<HTMLElement>(`[data-pane-id="${paneId}"]`)!;
}

// Splits `paneId` and puts a new terminal tab in the new pane, as the split button would.
function split(direction: "right" | "down", paneId: string, tabId: string): string {
  let created = "";
  act(() => {
    created = useUi.getState().splitPane(direction, paneId, "ws")!;
    useUi.getState().addTab(terminal(tabId), created);
  });
  render();
  return created;
}

beforeEach(() => {
  mounts.count.clear();
  mounts.unmounts.clear();
  useUi.setState({ workspaces: [workspace()], activeWorkspaceId: "ws", sessions: [], toasts: [] });
});

afterEach(() => {
  view?.unmount();
  view = null;
});

describe("split view terminal identity", () => {
  it("renders a three-level nested layout and keeps every terminal mounted across splits and closes", async () => {
    render();
    await flush();
    const first = terminalNode("t1");
    expect(first).not.toBeNull();

    const right = split("right", "p1", "t2");
    const rightBottom = split("down", right, "t3");
    const deepest = split("right", rightBottom, "t4");
    await flush();

    // [ p1 | [ right / [ rightBottom | deepest ] ] ]: three nested splits, one divider each.
    expect(view!.container.querySelectorAll('[role="separator"]')).toHaveLength(3);
    expect(view!.container.querySelectorAll('[role="separator"][aria-orientation="vertical"]')).toHaveLength(2);
    expect(paneNode(deepest).style.left).toBe("calc(75% + 3px)");
    expect(paneNode(deepest).style.top).toBe("calc(50% + 3px)");
    for (const id of ["t1", "t2", "t3", "t4"]) expect(terminalNode(id)).not.toBeNull();
    expect(terminalNode("t1")).toBe(first);
    const deepNode = terminalNode("t4");

    await act(async () => {
      await useUi.getState().closePane(right, "ws");
    });
    render();
    await flush();
    expect(terminalNode("t2")).toBeNull();
    expect(terminalNode("t1")).toBe(first);
    expect(terminalNode("t4")).toBe(deepNode);
    expect(view!.container.querySelectorAll('[role="separator"]')).toHaveLength(2);

    for (const id of ["t1", "t3", "t4"]) {
      expect(mounts.count.get(id)).toBe(1);
      expect(mounts.unmounts.get(id) ?? 0).toBe(0);
    }
  });

  it("keeps the terminal node across divider moves and desktop/compact switches, showing only the active pane when compact", async () => {
    render();
    await flush();
    const right = split("right", "p1", "t2");
    await flush();
    const first = terminalNode("t1");
    const second = terminalNode("t2");

    const splitId = useUi.getState().workspaces[0].layoutTree!.id;
    act(() => useUi.getState().setSplitRatio(splitId, 0.3, "ws"));
    render();
    expect(paneNode(right).style.left).toBe("calc(30% + 3px)");

    render(true);
    expect(view!.container.querySelector('[role="separator"]')).toBeNull();
    expect(paneNode(right).className).not.toContain("hidden");
    expect(paneNode("p1").className).toBe("hidden");
    expect(view!.container.textContent).toContain("窗格 2/2");

    act(() => useUi.getState().setActivePane("p1", "ws"));
    render(true);
    expect(paneNode("p1").className).not.toContain("hidden");
    expect(paneNode(right).className).toBe("hidden");

    render(false);
    expect(paneNode(right).className).not.toContain("hidden");
    expect(terminalNode("t1")).toBe(first);
    expect(terminalNode("t2")).toBe(second);
    expect(mounts.count.get("t1")).toBe(1);
    expect(mounts.count.get("t2")).toBe(1);
    expect(mounts.unmounts.get("t1") ?? 0).toBe(0);
  });

  it("switches panes from the compact pane switcher", async () => {
    render();
    const right = split("right", "p1", "t2");
    render(true);
    await flush();
    const next = view!.container.querySelector<HTMLButtonElement>(`[data-pane-id="${right}"] button[aria-label="下一个窗格"]`)!;
    act(() => next.click());
    expect(useUi.getState().workspaces[0].activePaneId).toBe("p1");
    render(true);
    const previous = view!.container.querySelector<HTMLButtonElement>('[data-pane-id="p1"] button[aria-label="上一个窗格"]')!;
    act(() => previous.click());
    expect(useUi.getState().workspaces[0].activePaneId).toBe(right);
  });

  it("splits and closes from the pane toolbar", async () => {
    render();
    await flush();
    const splitRight = view!.container.querySelector<HTMLButtonElement>('[data-pane-id="p1"] button[aria-label="向右分屏"]')!;
    act(() => splitRight.click());
    render();
    const created = useUi.getState().workspaces[0].activePaneId;
    expect(created).not.toBe("p1");
    const splitDown = view!.container.querySelector<HTMLButtonElement>(`[data-pane-id="${created}"] button[aria-label="向下分屏"]`)!;
    act(() => splitDown.click());
    render();
    expect(useUi.getState().workspaces[0].panes).toHaveLength(3);
    const close = view!.container.querySelector<HTMLButtonElement>(`[data-pane-id="${created}"] button[aria-label="关闭此窗格"]`)!;
    await act(async () => {
      close.click();
      await Promise.resolve();
    });
    await flush();
    expect(useUi.getState().workspaces[0].panes.map((pane) => pane.id)).not.toContain(created);
    expect(terminalNode("t1")).not.toBeNull();
  });
});
