/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, flushUntil, mount, type MountedView } from "./reactTestUtils";
import { ProjectWorkspaceStrip } from "../../app/ProjectWorkspaceStrip";
import {
  ALL_PROJECTS,
  UNGROUPED_PROJECT,
  projectFocusForGroup,
  type ProjectFocus,
  type ProjectInventory,
} from "../../app/projectFocus";
import { useUi, type SessionRuntimeInfo, type Workspace } from "../../app/store";
import { resetRemoteStatusForTest, setRemoteStatus } from "../../features/remote/remoteStatus";
import type { Asset, AssetGroup } from "../../ipc/commands";

const dialogMocks = vi.hoisted(() => ({ ask: vi.fn() }));
vi.mock("../../ui/dialogs", async (importOriginal) => ({ ...(await importOriginal<typeof import("../../ui/dialogs")>()), ask: dialogMocks.ask }));

const realCloseWorkspace = useUi.getState().closeWorkspace;

function group(id: string, parentId: string | null = null): AssetGroup {
  return { id, parentId, name: `Project ${id}`, sort: 0, createdAt: 1, updatedAt: 1 };
}

function asset(id: string, groupId: string | null): Asset {
  return {
    id,
    groupId,
    kind: "ssh",
    name: id,
    host: null,
    port: null,
    username: null,
    authKind: null,
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: 1,
    updatedAt: 1,
    deletedAt: null,
    builtin: false,
  };
}

function workspace(id: string, assetId?: string): Workspace {
  const tab = { id: `${id}-tab`, kind: "terminal" as const, title: id, closable: true };
  return {
    id,
    kind: "session",
    title: id,
    sessionId: `session-${id}`,
    assetId,
    assetKind: "ssh",
    panes: [{ id: `${id}-pane`, tabs: [tab], activeTabId: tab.id }],
    activePaneId: `${id}-pane`,
    closable: true,
  };
}

const inventory: ProjectInventory = {
  groups: [group("p1"), group("child", "p1"), group("p2")],
  assets: [asset("root-asset", "p1"), asset("child-asset", "child"), asset("other-asset", "p2"), asset("free-asset", null)],
};
const workspaces = [
  workspace("ws-root", "root-asset"),
  workspace("ws-child", "child-asset"),
  workspace("ws-other", "other-asset"),
  workspace("ws-free", "free-asset"),
  workspace("ws-quick"),
];
const sessions: SessionRuntimeInfo[] = workspaces.map((item) => ({
  id: item.sessionId as string,
  assetId: item.assetId ?? null,
  name: item.title,
  kind: "ssh",
  status: "connected",
  tabs: [],
  createdAt: 1,
}));

let mounted: MountedView | undefined;

function renderStrip(activeProject: ProjectFocus, onSelect = vi.fn(), onReorder?: (dragId: string, overId: string, before: boolean) => void) {
  mounted = mount(createElement(ProjectWorkspaceStrip, {
    workspaces,
    activeWorkspaceId: "ws-root",
    activeProject,
    projectInventory: inventory,
    groups: inventory.groups,
    onSelect,
    onReorder,
  }));
  return onSelect;
}

function stripWorkspaceIds(): string[] {
  return [...(mounted?.container.querySelectorAll("[data-project-workspace-id]") ?? [])]
    .map((node) => node.getAttribute("data-project-workspace-id") as string);
}

beforeEach(() => {
  document.body.replaceChildren();
  useUi.setState({ sessions, workspaces, closeWorkspace: realCloseWorkspace });
});

afterEach(() => {
  resetRemoteStatusForTest();
  dialogMocks.ask.mockReset();
  mounted?.unmount();
  mounted = undefined;
});

describe("ProjectWorkspaceStrip", () => {
  it("keeps the existing all view by rendering nothing", () => {
    renderStrip(ALL_PROJECTS);
    expect(mounted?.container.childElementCount).toBe(0);
  });

  it("shows only the selected top-level project, including nested groups", () => {
    const onSelect = renderStrip(projectFocusForGroup("p1"));
    expect(stripWorkspaceIds()).toEqual(["ws-root", "ws-child"]);
    expect(mounted?.container.textContent).toContain("Project p1");
    expect(mounted?.container.textContent).not.toContain("ws-other");

    const child = mounted?.container.querySelector<HTMLButtonElement>('[data-project-workspace-id="ws-child"]');
    if (!child) throw new Error("Nested project workspace not found");
    click(child);
    expect(onSelect).toHaveBeenCalledWith("ws-child");
  });

  it("collects ungrouped and quick-target workspaces", () => {
    renderStrip(UNGROUPED_PROJECT);
    expect(stripWorkspaceIds()).toEqual(["ws-free", "ws-quick"]);
    expect(mounted?.container.textContent).toContain("未分组");
  });

  it("preserves tab keyboard navigation within the filtered workspaces", () => {
    const onSelect = renderStrip(projectFocusForGroup("p1"));
    const root = mounted?.container.querySelector<HTMLButtonElement>('[data-project-workspace-id="ws-root"]');
    if (!root) throw new Error("Root workspace not found");
    act(() => root.focus());
    act(() => {
      root.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true, cancelable: true }));
    });
    expect(onSelect).toHaveBeenCalledWith("ws-child");
    expect(document.activeElement?.getAttribute("data-project-workspace-id")).toBe("ws-child");
  });
});

describe("ProjectWorkspaceStrip close menu", () => {
  function openMenu(workspaceId: string): void {
    const tab = mounted?.container.querySelector(`[data-project-workspace-id="${workspaceId}"]`);
    if (!tab) throw new Error(`Workspace tab not found: ${workspaceId}`);
    act(() => {
      tab.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 8, clientY: 8 }));
    });
  }

  function menuItem(label: string): HTMLButtonElement {
    const item = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
      .find((candidate) => candidate.textContent?.trim() === label);
    if (!item) throw new Error(`Menu item not found: ${label}`);
    return item;
  }

  const batch = [
    workspace("ws-current", "root-asset"),
    workspace("ws-right-1", "root-asset"),
    workspace("ws-right-2", "root-asset"),
    workspace("ws-right-3", "root-asset"),
    workspace("ws-other", "other-asset"),
  ];

  function renderBatch(closeWorkspace: (id: string) => Promise<boolean>, list: Workspace[] = batch, activeId = "ws-current"): void {
    useUi.setState({ closeWorkspace: closeWorkspace as typeof realCloseWorkspace, workspaces: list });
    mounted = mount(createElement(ProjectWorkspaceStrip, {
      workspaces: list,
      activeWorkspaceId: activeId,
      activeProject: projectFocusForGroup("p1"),
      projectInventory: inventory,
      groups: inventory.groups,
      onSelect: vi.fn(),
    }));
  }

  it.each([
    [projectFocusForGroup("p1"), "ws-child"],
    [UNGROUPED_PROJECT, "ws-quick"],
  ] as const)("closes the current workspace in the selected strip", async (focus, workspaceId) => {
    const closeWorkspace = vi.fn().mockResolvedValue(true);
    useUi.setState({ closeWorkspace });
    renderStrip(focus);
    openMenu(workspaceId);

    expect(menuItem("关闭工作区").disabled).toBe(false);
    click(menuItem("关闭工作区"));
    await flush();

    expect(closeWorkspace).toHaveBeenCalledExactlyOnceWith(workspaceId);
  });

  it("closes only visible workspaces on the requested side and disables empty sides", async () => {
    const closeWorkspace = vi.fn().mockResolvedValue(true);
    useUi.setState({ closeWorkspace });
    renderStrip(projectFocusForGroup("p1"));

    openMenu("ws-child");
    expect(menuItem("关闭左侧全部工作区").disabled).toBe(false);
    expect(menuItem("关闭右侧全部工作区").disabled).toBe(true);
    click(menuItem("关闭左侧全部工作区"));
    await flush();

    openMenu("ws-root");
    expect(menuItem("关闭左侧全部工作区").disabled).toBe(true);
    expect(menuItem("关闭右侧全部工作区").disabled).toBe(false);
    click(menuItem("关闭右侧全部工作区"));
    await flush();

    expect(closeWorkspace.mock.calls).toEqual([["ws-root"], ["ws-child"]]);
  });

  it("closes batches in order and stops when a workspace close is declined", async () => {
    const closeWorkspace = vi.fn().mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    renderBatch(closeWorkspace);
    openMenu("ws-current");
    click(menuItem("关闭右侧全部工作区"));
    await flush();

    expect(closeWorkspace.mock.calls).toEqual([["ws-right-1"], ["ws-right-2"]]);
  });

  it("skips workspaces that are not closable", async () => {
    const closeWorkspace = vi.fn().mockResolvedValue(true);
    const pinned = { ...workspace("ws-pinned", "root-asset"), closable: false };
    renderBatch(closeWorkspace, [workspace("ws-current", "root-asset"), pinned, workspace("ws-right-1", "root-asset")]);
    openMenu("ws-current");
    click(menuItem("关闭右侧全部工作区"));
    await flush();

    expect(closeWorkspace.mock.calls).toEqual([["ws-right-1"]]);
  });

  it("closes left workspaces in strip order and stops when one is declined", async () => {
    const closeWorkspace = vi.fn().mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    renderBatch(closeWorkspace, [
      workspace("ws-left-1", "root-asset"),
      workspace("ws-left-2", "root-asset"),
      workspace("ws-left-3", "root-asset"),
      workspace("ws-current", "root-asset"),
    ], "ws-current");
    openMenu("ws-current");
    click(menuItem("关闭左侧全部工作区"));
    await flush();

    expect(closeWorkspace.mock.calls).toEqual([["ws-left-1"], ["ws-left-2"]]);
  });

  it("does not treat a target closed during the batch as a decline", async () => {
    const closeWorkspace = vi.fn(async (id: string) => {
      if (id === "ws-right-1") {
        useUi.setState({ workspaces: useUi.getState().workspaces.filter((item) => item.id !== "ws-right-2") });
      }
      return true;
    });
    renderBatch(closeWorkspace);
    openMenu("ws-current");
    click(menuItem("关闭右侧全部工作区"));
    await flush();

    expect(closeWorkspace.mock.calls.map(([id]) => id)).toEqual(["ws-right-1", "ws-right-3"]);
  });

  it("keeps the reconnect item beside the close items", () => {
    renderBatch(vi.fn().mockResolvedValue(true));
    useUi.setState({ sessions: sessions.map((item) => ({ ...item, status: "disconnected" as const })) });
    // The batch workspaces have no session records, so they read as disconnected.
    openMenu("ws-current");

    const labels = [...document.querySelectorAll('[role="menuitem"]')].map((node) => node.textContent?.trim());
    expect(labels).toEqual(expect.arrayContaining([expect.stringMatching(/^重新连接/), "关闭工作区", "关闭左侧全部工作区", "关闭右侧全部工作区"]));
  });

  describe("with the real close path", () => {
    function remoteWorkspace(id: string): Workspace {
      const tab = { id: `${id}-tab`, kind: "remote" as const, title: "远程桌面", closable: true, assetId: "root-asset" };
      return {
        id,
        kind: "remote",
        title: id,
        assetId: "root-asset",
        assetKind: "rdp",
        panes: [{ id: `${id}-pane`, tabs: [tab], activeTabId: tab.id }],
        activePaneId: `${id}-pane`,
        closable: true,
      };
    }

    it("confirms a connected remote desktop in the batch and closes it once accepted", async () => {
      dialogMocks.ask.mockResolvedValue(true);
      const remote = remoteWorkspace("ws-remote");
      setRemoteStatus("ws-remote-tab", "connected");
      renderBatch(realCloseWorkspace, [workspace("ws-current", "root-asset"), remote]);
      openMenu("ws-current");
      click(menuItem("关闭右侧全部工作区"));
      await flushUntil(() => dialogMocks.ask.mock.calls.length > 0);
      await flushUntil(() => useUi.getState().workspaces.length === 1);

      expect(dialogMocks.ask).toHaveBeenCalledTimes(1);
      expect(String(dialogMocks.ask.mock.calls[0][0])).toContain("断开后会话结束");
      expect(useUi.getState().workspaces.map((item) => item.id)).toEqual(["ws-current"]);
    });

    it("keeps the remote desktop and stops the batch when the confirmation is declined", async () => {
      dialogMocks.ask.mockResolvedValue(false);
      setRemoteStatus("ws-remote-tab", "connected");
      renderBatch(realCloseWorkspace, [workspace("ws-current", "root-asset"), remoteWorkspace("ws-remote"), remoteWorkspace("ws-remote-2")]);
      openMenu("ws-current");
      click(menuItem("关闭右侧全部工作区"));
      await flushUntil(() => dialogMocks.ask.mock.calls.length > 0);
      await flush();

      expect(dialogMocks.ask).toHaveBeenCalledTimes(1);
      expect(useUi.getState().workspaces.map((item) => item.id)).toEqual(["ws-current", "ws-remote", "ws-remote-2"]);
    });
  });
});

describe("ProjectWorkspaceStrip reorder", () => {
  interface StubDataTransfer {
    types: string[];
    effectAllowed: string;
    dropEffect: string;
    setData: (type: string, value: string) => void;
    getData: (type: string) => string;
  }

  function makeDataTransfer(): StubDataTransfer {
    const data = new Map<string, string>();
    return {
      get types() {
        return [...data.keys()];
      },
      effectAllowed: "",
      dropEffect: "",
      setData: (type, value) => {
        data.set(type, value);
      },
      getData: (type) => data.get(type) ?? "",
    };
  }

  function dragEvent(type: string, dt: StubDataTransfer, init: MouseEventInit = {}): MouseEvent {
    const event = new MouseEvent(type, { bubbles: true, cancelable: true, ...init });
    Object.defineProperty(event, "dataTransfer", { value: dt });
    return event;
  }

  function stubRect(el: HTMLElement, left: number, width: number): void {
    el.getBoundingClientRect = () =>
      ({
        left,
        width,
        right: left + width,
        top: 0,
        bottom: 28,
        height: 28,
        x: left,
        y: 0,
        toJSON: () => ({}),
      }) as DOMRect;
  }

  function renderReorderStrip(onReorder = vi.fn()) {
    renderStrip(projectFocusForGroup("p1"), undefined, onReorder);
    return onReorder;
  }

  function workspaceButton(id: string): HTMLButtonElement {
    const button = mounted?.container.querySelector<HTMLButtonElement>(`[data-project-workspace-id="${id}"]`);
    if (!button) throw new Error(`Workspace button not found: ${id}`);
    return button;
  }

  it("drag-reorders a workspace onto another with edge awareness", () => {
    const onReorder = renderReorderStrip();
    const root = workspaceButton("ws-root");
    const child = workspaceButton("ws-child");
    stubRect(child, 100, 80);
    const dt = makeDataTransfer();

    act(() => {
      root.dispatchEvent(dragEvent("dragstart", dt));
    });
    expect(root.className).toContain("nx-drag-source");

    const over = dragEvent("dragover", dt, { clientX: 190 });
    act(() => {
      child.dispatchEvent(over);
    });
    expect(over.defaultPrevented).toBe(true);
    expect(child.className).toContain("nx-drop-after");

    const drop = dragEvent("drop", dt, { clientX: 190 });
    act(() => {
      child.dispatchEvent(drop);
    });
    expect(drop.defaultPrevented).toBe(true);
    expect(onReorder).toHaveBeenCalledWith("ws-root", "ws-child", false);

    act(() => {
      root.dispatchEvent(dragEvent("dragend", dt));
    });
    expect(root.className).not.toContain("nx-drag-source");
    expect(child.className).not.toContain("nx-drop-after");
  });

  it("drops before the target when dragging over the left half and ignores foreign drags", () => {
    const onReorder = renderReorderStrip();
    const root = workspaceButton("ws-root");
    const child = workspaceButton("ws-child");
    stubRect(child, 100, 80);
    const dt = makeDataTransfer();

    act(() => {
      root.dispatchEvent(dragEvent("dragstart", dt));
    });
    act(() => {
      child.dispatchEvent(dragEvent("drop", dt, { clientX: 105 }));
    });
    expect(onReorder).toHaveBeenCalledWith("ws-root", "ws-child", true);

    const foreign = makeDataTransfer();
    foreign.setData("text/plain", "ws-root");
    const over = dragEvent("dragover", foreign, { clientX: 110 });
    act(() => {
      child.dispatchEvent(over);
    });
    expect(over.defaultPrevented).toBe(false);
  });

  it("does not reorder when dropped onto itself", () => {
    const onReorder = renderReorderStrip();
    const root = workspaceButton("ws-root");
    stubRect(root, 0, 80);
    const dt = makeDataTransfer();

    act(() => {
      root.dispatchEvent(dragEvent("dragstart", dt));
    });
    act(() => {
      root.dispatchEvent(dragEvent("drop", dt, { clientX: 10 }));
    });
    expect(onReorder).not.toHaveBeenCalled();
  });

  it("moves a workspace with Ctrl+Shift+ArrowRight and keeps focus on it", () => {
    const onReorder = renderReorderStrip();
    const root = workspaceButton("ws-root");
    act(() => root.focus());

    const event = new KeyboardEvent("keydown", { key: "ArrowRight", ctrlKey: true, shiftKey: true, bubbles: true, cancelable: true });
    act(() => {
      root.dispatchEvent(event);
    });

    expect(event.defaultPrevented).toBe(true);
    expect(onReorder).toHaveBeenCalledWith("ws-root", "ws-child", false);
    expect(document.activeElement?.getAttribute("data-project-workspace-id")).toBe("ws-root");
  });

  it("moves a workspace left with Ctrl+Shift+ArrowLeft and does not consume the key at the boundary", () => {
    const onReorder = renderReorderStrip();
    const root = workspaceButton("ws-root");
    act(() => root.focus());

    const boundary = new KeyboardEvent("keydown", { key: "ArrowLeft", ctrlKey: true, shiftKey: true, bubbles: true, cancelable: true });
    act(() => {
      root.dispatchEvent(boundary);
    });
    expect(boundary.defaultPrevented).toBe(false);
    expect(onReorder).not.toHaveBeenCalled();

    const child = workspaceButton("ws-child");
    act(() => child.focus());
    const move = new KeyboardEvent("keydown", { key: "ArrowLeft", ctrlKey: true, shiftKey: true, bubbles: true, cancelable: true });
    act(() => {
      child.dispatchEvent(move);
    });
    expect(move.defaultPrevented).toBe(true);
    expect(onReorder).toHaveBeenCalledWith("ws-child", "ws-root", true);
  });

  it("keeps the tabs non-draggable and consumes no reorder keys without an onReorder handler", () => {
    renderStrip(projectFocusForGroup("p1"));
    const root = workspaceButton("ws-root");
    expect(root.getAttribute("draggable")).toBe("false");

    act(() => root.focus());
    const event = new KeyboardEvent("keydown", { key: "ArrowRight", ctrlKey: true, shiftKey: true, bubbles: true, cancelable: true });
    act(() => {
      root.dispatchEvent(event);
    });
    expect(event.defaultPrevented).toBe(false);
  });
});
