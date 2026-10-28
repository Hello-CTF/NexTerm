/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, mount, type MountedView } from "../features/reactTestUtils";
import { resetTerminalMounts, terminalMounts, workspaceBodyProps } from "../helpers/splitViewHarness";

const mocks = vi.hoisted(() => ({ detach: vi.fn(async () => undefined), closeTab: vi.fn(async () => undefined) }));

vi.mock("../../ui/dialogs", () => ({ ask: vi.fn(async () => true), askChoice: vi.fn() }));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    terminalApi: { closeTab: mocks.closeTab, detach: mocks.detach, listLive: vi.fn(async () => []) },
  });
});
vi.mock("../../app/lazyPanels", async () => (await import("../helpers/splitViewHarness")).lazyPanelsMock());

import { useUi, type AppTab, type Pane, type Workspace } from "../../app/store";
import { WorkspaceBody } from "../../app/shell/WorkspaceBody";
import { paneRects } from "../../app/splitTree";
import { workspaceLayoutTree } from "../../app/paneLayout";
import { endTabDrag } from "../../app/workspace/tabDrag";

const TAB_MIME = "application/x-nexterm-tab";
const PANE_BOX = { left: 0, top: 35, width: 800, height: 565 };

let view: MountedView | null = null;

function terminal(id: string): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s", tabId: `kernel-${id}`, closable: true };
}

function setPanes(panes: Pane[]): void {
  const workspace: Workspace = {
    id: "ws",
    kind: "session",
    title: "host",
    sessionId: "s",
    panes,
    activePaneId: panes[0].id,
    closable: true,
    ...(panes.length === 2
      ? {
          layoutTree: {
            type: "split" as const,
            id: "root",
            dir: "row" as const,
            ratio: 0.5,
            a: { type: "leaf" as const, id: "leaf-a", paneId: panes[0].id },
            b: { type: "leaf" as const, id: "leaf-b", paneId: panes[1].id },
          },
        }
      : {}),
  };
  useUi.setState({ workspaces: [workspace], activeWorkspaceId: "ws", sessions: [], toasts: [] });
}

function render(singlePane = false): void {
  const node = createElement(WorkspaceBody, workspaceBodyProps(useUi.getState().workspaces, singlePane));
  if (!view) view = mount(node);
  else act(() => view!.root.render(node));
}

function current(): Workspace {
  return useUi.getState().workspaces[0];
}

function rectOf(paneId: string) {
  return paneRects(workspaceLayoutTree(current())!).find((entry) => entry.paneId === paneId)?.rect;
}

function tabsOf(paneId: string): string[] {
  return current().panes.find((pane) => pane.id === paneId)?.tabs.map((tab) => tab.id) ?? [];
}

function terminalNode(id: string): HTMLElement | null {
  return view!.container.querySelector<HTMLElement>(`[data-terminal="${id}"]`);
}

function tabElement(id: string): HTMLElement {
  return view!.container.querySelector<HTMLElement>(`[role="tab"][data-tab-id="${id}"]`)!;
}

function dropTarget(paneId: string): HTMLElement | null {
  const target = view!.container.querySelector<HTMLElement>(`[data-tab-drop-pane="${paneId}"]`);
  if (target) target.getBoundingClientRect = () => ({ ...PANE_BOX, x: 0, y: 35, right: 800, bottom: 600 }) as DOMRect;
  return target;
}

function preview(): HTMLElement | null {
  return view!.container.querySelector<HTMLElement>(".nx-tab-drop-preview");
}

// A minimal DataTransfer: jsdom has none, and the app only uses types, setData/getData and the effects.
function dataTransfer() {
  const data = new Map<string, string>();
  return {
    get types() {
      return [...data.keys()];
    },
    effectAllowed: "",
    dropEffect: "",
    setData: (type: string, value: string) => void data.set(type, value),
    getData: (type: string) => data.get(type) ?? "",
  };
}

type Transfer = ReturnType<typeof dataTransfer>;

function fire(target: Element, type: string, transfer: Transfer, point: { x?: number; y?: number } = {}): MouseEvent {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, clientX: point.x ?? 0, clientY: point.y ?? 0 });
  Object.defineProperty(event, "dataTransfer", { value: transfer });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function startDrag(tabId: string): Transfer {
  const transfer = dataTransfer();
  fire(tabElement(tabId), "dragstart", transfer);
  return transfer;
}

// Points inside PANE_BOX: near each edge and in the middle.
const POINTS = {
  left: { x: 20, y: 300 },
  right: { x: 780, y: 300 },
  top: { x: 400, y: 50 },
  bottom: { x: 400, y: 590 },
  center: { x: 400, y: 300 },
} as const;

function expectStillMounted(ids: string[]): void {
  for (const id of ids) {
    expect(terminalMounts.count.get(id)).toBe(1);
    expect(terminalMounts.unmounts.get(id) ?? 0).toBe(0);
  }
  expect(mocks.detach).not.toHaveBeenCalled();
  expect(mocks.closeTab).not.toHaveBeenCalled();
}

beforeEach(() => {
  mocks.detach.mockClear();
  mocks.closeTab.mockClear();
  resetTerminalMounts();
});

afterEach(() => {
  view?.unmount();
  view = null;
  endTabDrag();
  document.body.replaceChildren();
});

describe("dragging a tab onto a pane", () => {
  it("shows no drop targets until a tab drag starts, so terminals keep their mouse handling", async () => {
    setPanes([{ id: "p1", tabs: [terminal("t1"), terminal("t2")], activeTabId: "t1" }]);
    render();
    await flush();
    expect(view!.container.querySelector("[data-tab-drop-pane]")).toBeNull();

    const transfer = startDrag("t2");
    expect(transfer.getData(TAB_MIME)).toBe("t2");
    expect(dropTarget("p1")).not.toBeNull();
    fire(tabElement("t2"), "dragend", transfer);
    expect(dropTarget("p1")).toBeNull();
  });

  it.each([
    { edge: "right", dir: "row", newRect: { x: 0.5, y: 0, width: 0.5, height: 1 } },
    { edge: "left", dir: "row", newRect: { x: 0, y: 0, width: 0.5, height: 1 } },
    { edge: "top", dir: "col", newRect: { x: 0, y: 0, width: 1, height: 0.5 } },
    { edge: "bottom", dir: "col", newRect: { x: 0, y: 0.5, width: 1, height: 0.5 } },
  ] as const)("splits on a drop at the $edge edge and moves the tab into the new pane, keeping its terminal", async ({ edge, newRect }) => {
    setPanes([{ id: "p1", tabs: [terminal("t1"), terminal("t2")], activeTabId: "t2" }]);
    render();
    await flush();
    const moving = terminalNode("t2");
    const staying = terminalNode("t1");

    const transfer = startDrag("t2");
    const over = fire(dropTarget("p1")!, "dragover", transfer, POINTS[edge]);
    expect(over.defaultPrevented).toBe(true);
    expect(transfer.dropEffect).toBe("move");
    expect(preview()?.dataset.dropRegion).toBe(edge);

    const drop = fire(dropTarget("p1")!, "drop", transfer, POINTS[edge]);
    expect(drop.defaultPrevented).toBe(true);
    render();
    await flush();

    const created = current().activePaneId;
    expect(created).not.toBe("p1");
    expect(tabsOf(created)).toEqual(["t2"]);
    expect(tabsOf("p1")).toEqual(["t1"]);
    expect(rectOf(created)).toEqual(newRect);
    // The drop ended the drag: the targets are gone.
    expect(view!.container.querySelector("[data-tab-drop-pane]")).toBeNull();
    expect(terminalNode("t2")).toBe(moving);
    expect(terminalNode("t1")).toBe(staying);
    expect(moving!.closest<HTMLElement>("[data-tab-content]")!.dataset.contentPane).toBe(created);
    expectStillMounted(["t1", "t2"]);
  });

  it("moves the tab into another pane on a centre drop, closing the pane it leaves empty", async () => {
    setPanes([
      { id: "left", tabs: [terminal("a")], activeTabId: "a" },
      { id: "right", tabs: [terminal("b")], activeTabId: "b" },
    ]);
    render();
    await flush();
    const node = terminalNode("a");

    const transfer = startDrag("a");
    // The source pane cannot take its only tab: neither its centre nor its edges accept the drop.
    for (const point of Object.values(POINTS)) {
      expect(fire(dropTarget("left")!, "dragover", transfer, point).defaultPrevented).toBe(false);
    }
    expect(preview()).toBeNull();
    expect(current().panes).toHaveLength(2);

    expect(fire(dropTarget("right")!, "dragover", transfer, POINTS.center).defaultPrevented).toBe(true);
    expect(preview()?.dataset.dropRegion).toBe("center");
    fire(dropTarget("right")!, "drop", transfer, POINTS.center);
    render();
    await flush();
    expect(current().panes.map((pane) => pane.id)).toEqual(["right"]);
    expect(tabsOf("right")).toEqual(["b", "a"]);
    expect(terminalNode("a")).toBe(node);
    expectStillMounted(["a", "b"]);
  });

  it("splits another pane with a pane's only tab and closes the emptied source pane", async () => {
    setPanes([
      { id: "left", tabs: [terminal("a")], activeTabId: "a" },
      { id: "right", tabs: [terminal("b")], activeTabId: "b" },
    ]);
    render();
    await flush();
    const node = terminalNode("a");
    const transfer = startDrag("a");
    fire(dropTarget("right")!, "dragover", transfer, POINTS.bottom);
    fire(dropTarget("right")!, "drop", transfer, POINTS.bottom);
    render();
    await flush();
    const created = current().activePaneId;
    expect(current().panes.map((pane) => pane.id).sort()).toEqual([created, "right"].sort());
    expect(rectOf("right")).toEqual({ x: 0, y: 0, width: 1, height: 0.5 });
    expect(tabsOf(created)).toEqual(["a"]);
    expect(terminalNode("a")).toBe(node);
    expectStillMounted(["a", "b"]);
  });

  it("refuses an edge drop on a pane too small to split, with a notice, and leaves the layout alone", async () => {
    setPanes([{ id: "p1", tabs: [terminal("t1"), terminal("t2")], activeTabId: "t1" }]);
    render();
    await flush();
    const pane = view!.container.querySelector<HTMLElement>('[data-pane-id="p1"]')!;
    pane.getBoundingClientRect = () => ({ left: 0, top: 0, width: 300, height: 300 }) as DOMRect;
    const before = useUi.getState().workspaces;

    const transfer = startDrag("t2");
    fire(dropTarget("p1")!, "dragover", transfer, POINTS.left);
    fire(dropTarget("p1")!, "drop", transfer, POINTS.left);
    render();
    expect(useUi.getState().workspaces).toBe(before);
    expect(useUi.getState().toasts.at(-1)?.text).toContain("窗格太小，无法向左分屏");
    expect(view!.container.querySelector("[data-tab-drop-pane]")).toBeNull();
    expectStillMounted(["t1"]);
  });

  it("cancels the drag on Escape: the targets disappear and a later drop changes nothing", async () => {
    setPanes([{ id: "p1", tabs: [terminal("t1"), terminal("t2")], activeTabId: "t1" }]);
    render();
    await flush();
    const transfer = startDrag("t2");
    const target = dropTarget("p1")!;
    fire(target, "dragover", transfer, POINTS.right);
    expect(preview()).not.toBeNull();

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(view!.container.querySelector("[data-tab-drop-pane]")).toBeNull();
    expect(tabElement("t2").className).not.toContain("nx-drag-source");
    // The browser may still deliver the drop to what is under the pointer; nothing accepts it.
    expect(fire(target, "drop", transfer, POINTS.right).defaultPrevented).toBe(false);
    expect(fire(tabElement("t1"), "drop", transfer, { x: 1 }).defaultPrevented).toBe(false);
    expect(current().panes).toHaveLength(1);
    expect(tabsOf("p1")).toEqual(["t1", "t2"]);
  });

  it("offers only centre moves in the single-pane layout, so a drag there never splits", async () => {
    setPanes([
      { id: "left", tabs: [terminal("a"), terminal("c")], activeTabId: "a" },
      { id: "right", tabs: [terminal("b")], activeTabId: "b" },
    ]);
    render(true);
    await flush();
    const transfer = startDrag("a");
    // Only the shown pane gets a target, and it is the drag's own pane: nothing to do there.
    expect(dropTarget("right")).toBeNull();
    for (const point of Object.values(POINTS)) {
      expect(fire(dropTarget("left")!, "dragover", transfer, point).defaultPrevented).toBe(false);
    }
    fire(dropTarget("left")!, "drop", transfer, POINTS.right);
    expect(current().panes).toHaveLength(2);
    expect(tabsOf("left")).toEqual(["a", "c"]);
    fire(tabElement("a"), "dragend", transfer);
  });

  it("ignores drags of something other than a tab", async () => {
    setPanes([{ id: "p1", tabs: [terminal("t1"), terminal("t2")], activeTabId: "t1" }]);
    render();
    await flush();
    startDrag("t2");
    const files = dataTransfer();
    files.setData("Files", "");
    expect(fire(dropTarget("p1")!, "dragover", files, POINTS.right).defaultPrevented).toBe(false);
    act(() => endTabDrag());
    render();
    expect(dropTarget("p1")).toBeNull();
  });
});

describe("a drag never outlives its gesture", () => {
  function foreignDrag(): Transfer {
    // Another window's tab drag carries the same type but did not start here.
    const transfer = dataTransfer();
    transfer.setData(TAB_MIME, "t1");
    return transfer;
  }

  it("ends when the dragged tab is removed mid-drag, and then rejects another window's drag", async () => {
    setPanes([{ id: "p1", tabs: [terminal("t1"), terminal("t2")], activeTabId: "t1" }]);
    render();
    await flush();
    startDrag("t2");
    expect(dropTarget("p1")).not.toBeNull();

    // The tab's session exits and the tab closes: its element goes, and its dragend never arrives.
    act(() => {
      useUi.setState((state) => ({
        workspaces: state.workspaces.map((item) => ({
          ...item,
          panes: item.panes.map((target) => ({ ...target, tabs: target.tabs.filter((tab) => tab.id !== "t2"), activeTabId: "t1" })),
        })),
      }));
    });
    render();
    await flush();
    expect(dropTarget("p1")).toBeNull();
    const tabOver = fire(tabElement("t1"), "dragover", foreignDrag(), { x: 1 });
    expect(tabOver.defaultPrevented).toBe(false);
  });

  it("ends when the dragged tab's pane goes away", async () => {
    setPanes([
      { id: "left", tabs: [terminal("a"), terminal("c")], activeTabId: "a" },
      { id: "right", tabs: [terminal("b")], activeTabId: "b" },
    ]);
    render();
    await flush();
    startDrag("a");
    // A layout sync replaces the source pane wholesale.
    act(() => {
      useUi.setState((state) => ({
        workspaces: state.workspaces.map((item) => ({
          ...item,
          activePaneId: "right",
          layoutTree: undefined,
          panes: [{ ...item.panes[1], tabs: [...item.panes[1].tabs, ...item.panes[0].tabs] }],
        })),
      }));
    });
    render();
    await flush();
    expect(view!.container.querySelector("[data-tab-drop-pane]")).toBeNull();
  });

  it.each([
    { name: "window blur", end: () => window.dispatchEvent(new Event("blur")) },
    { name: "a dragend reaching the window", end: () => window.dispatchEvent(new Event("dragend")) },
    { name: "the first pointer event after the drag", end: () => window.dispatchEvent(new Event("pointerdown")) },
  ])("ends on $name", async ({ end }) => {
    setPanes([{ id: "p1", tabs: [terminal("t1"), terminal("t2")], activeTabId: "t1" }]);
    render();
    await flush();
    startDrag("t2");
    // Pointer events are watched only from the next tick, so the gesture that started the drag cannot end it.
    act(() => {
      window.dispatchEvent(new Event("mousemove"));
    });
    expect(dropTarget("p1")).not.toBeNull();
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    act(() => {
      end();
    });
    expect(dropTarget("p1")).toBeNull();
    expect(fire(tabElement("t1"), "dragover", foreignDrag(), { x: 1 }).defaultPrevented).toBe(false);
  });
});

describe("touch shells", () => {
  it("do not make tabs draggable; the tab menu is their way to move tabs", async () => {
    const matchMedia = window.matchMedia;
    window.matchMedia = ((query: string) =>
      ({ matches: query.includes("coarse"), media: query, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} }) as unknown as MediaQueryList);
    try {
      setPanes([{ id: "p1", tabs: [terminal("t1"), terminal("t2")], activeTabId: "t1" }]);
      render();
      await flush();
      expect(tabElement("t1").getAttribute("draggable")).toBe("false");
    } finally {
      window.matchMedia = matchMedia;
    }
    view?.unmount();
    view = null;
    render();
    await flush();
    expect(tabElement("t1").getAttribute("draggable")).toBe("true");
  });
});

describe("keyboard equivalents in the tab menu", () => {
  function menuItem(tabId: string, label: string): HTMLButtonElement | null {
    const tab = tabElement(tabId);
    act(() => {
      tab.dispatchEvent(new KeyboardEvent("keydown", { key: "F10", shiftKey: true, bubbles: true, cancelable: true }));
    });
    const menu = document.querySelector<HTMLElement>('[role="menu"]');
    return [...(menu?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]') ?? [])].find((item) =>
      item.textContent?.includes(label),
    ) ?? null;
  }

  it("moves the tab to a new pane on the right or below without remounting its terminal", async () => {
    setPanes([{ id: "p1", tabs: [terminal("t1"), terminal("t2"), terminal("t3")], activeTabId: "t1" }]);
    render();
    await flush();
    const node = terminalNode("t2");

    click(menuItem("t2", "移到右侧新窗格")!);
    render();
    await flush();
    const right = current().activePaneId;
    expect(rectOf(right)).toEqual({ x: 0.5, y: 0, width: 0.5, height: 1 });
    expect(tabsOf(right)).toEqual(["t2"]);

    click(menuItem("t3", "移到下方新窗格")!);
    render();
    await flush();
    const below = current().activePaneId;
    expect(rectOf(below)).toEqual({ x: 0, y: 0.5, width: 0.5, height: 0.5 });
    expect(tabsOf("p1")).toEqual(["t1"]);
    expect(terminalNode("t2")).toBe(node);
    expectStillMounted(["t1", "t2", "t3"]);

    // A pane's only tab cannot leave it for a new pane, and the menu says so.
    const lone = menuItem("t2", "移到右侧新窗格")!;
    expect(lone.disabled || lone.getAttribute("aria-disabled") === "true").toBe(true);
  });

  it("hides the new-pane moves in the single-pane layout", async () => {
    setPanes([{ id: "p1", tabs: [terminal("t1"), terminal("t2")], activeTabId: "t1" }]);
    render(true);
    await flush();
    expect(menuItem("t1", "移到右侧新窗格")).toBeNull();
    expect(menuItem("t1", "关闭标签")).not.toBeNull();
  });

  it("closes a closable tab with the middle button and ignores other buttons", async () => {
    setPanes([{ id: "p1", tabs: [terminal("t1"), terminal("t2")], activeTabId: "t1" }]);
    render();
    await flush();
    const dispatch = (button: number) =>
      act(async () => {
        tabElement("t2").dispatchEvent(new MouseEvent("auxclick", { bubbles: true, cancelable: true, button }));
      });
    await dispatch(2);
    await flush();
    expect(tabsOf("p1")).toEqual(["t1", "t2"]);
    await dispatch(1);
    await flush();
    await flush();
    expect(tabsOf("p1")).toEqual(["t1"]);
  });
});
