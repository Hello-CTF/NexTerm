/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, type MountedView } from "../features/reactTestUtils";
import { resetTerminalMounts, terminalMounts as mounts, workspaceBodyProps } from "../helpers/splitViewHarness";

const mocks = vi.hoisted(() => ({ detach: vi.fn(async () => undefined), closeTab: vi.fn(async () => undefined) }));

vi.mock("../../ui/dialogs", () => ({ ask: vi.fn(async () => true), askChoice: vi.fn() }));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    terminalApi: { closeTab: mocks.closeTab, detach: mocks.detach, listLive: vi.fn(async () => []) },
  });
});

// The terminal stub counts mounts and unmounts per tab: a remount would mean a reconnect in the app.
vi.mock("../../app/lazyPanels", async () => (await import("../helpers/splitViewHarness")).lazyPanelsMock());

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
  return workspaceBodyProps(useUi.getState().workspaces, singlePane);
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
  mocks.detach.mockClear();
  mocks.closeTab.mockClear();
  resetTerminalMounts();
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

    const t2 = terminalNode("t2");
    act(() => useUi.getState().closePane(right, "ws"));
    render();
    await flush();
    // The closed pane's terminal moved into the pane that took its space, still mounted.
    expect(terminalNode("t2")).toBe(t2);
    expect(terminalNode("t1")).toBe(first);
    expect(terminalNode("t4")).toBe(deepNode);
    expect(view!.container.querySelectorAll('[role="separator"]')).toHaveLength(2);
    expect(mocks.detach).not.toHaveBeenCalled();

    for (const id of ["t1", "t2", "t3", "t4"]) {
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

  it.each([
    { name: "a WinRM line tab", tab: { ...terminal("winrm"), sessionId: "s" } },
    { name: "a container exec tab", tab: { ...terminal("exec"), containerId: "container-1" } },
  ])("moves $name to another pane without remounting its terminal", async ({ tab }) => {
    act(() => {
      useUi.setState((state) => ({
        workspaces: state.workspaces.map((item) => ({
          ...item,
          panes: [{ ...item.panes[0], tabs: [...item.panes[0].tabs, tab], activeTabId: tab.id }],
        })),
      }));
    });
    render();
    await flush();
    const right = split("right", "p1", "t2");
    await flush();
    const node = terminalNode(tab.id);
    expect(node).not.toBeNull();

    act(() => useUi.getState().moveTabToPane(tab.id, right));
    render();
    await flush();
    const moved = useUi.getState().workspaces[0].panes.find((item) => item.id === right)!;
    expect(moved.tabs.map((item) => item.id)).toContain(tab.id);
    expect(terminalNode(tab.id)).toBe(node);
    // The content now sits over the right pane's content area.
    expect(node!.closest<HTMLElement>("[data-tab-content]")!.dataset.contentPane).toBe(right);
    expect(mounts.count.get(tab.id)).toBe(1);
    expect(mounts.unmounts.get(tab.id) ?? 0).toBe(0);

    // Moving it back and closing the pane it left behind keeps it mounted too.
    act(() => useUi.getState().moveTabToPane(tab.id, "p1"));
    render();
    await flush();
    expect(terminalNode(tab.id)).toBe(node);
    expect(mounts.unmounts.get(tab.id) ?? 0).toBe(0);
  });

  it("keeps terminals mounted when a sync renames their pane", async () => {
    render();
    await flush();
    const node = terminalNode("t1");
    // First sync pairs this pane with another device's copy and adopts that copy's id.
    act(() => {
      useUi.setState((state) => ({
        workspaces: state.workspaces.map((item) => ({
          ...item,
          activePaneId: "pane-from-other-device",
          panes: item.panes.map((target) => ({ ...target, id: "pane-from-other-device" })),
        })),
      }));
    });
    render();
    await flush();
    expect(paneNode("pane-from-other-device")).not.toBeNull();
    expect(terminalNode("t1")).toBe(node);
    expect(mounts.count.get("t1")).toBe(1);
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
    const close = view!.container.querySelector<HTMLButtonElement>(`[data-pane-id="${created}"] button[aria-label="关闭窗格（保留标签页）"]`)!;
    act(() => close.click());
    render();
    await flush();
    expect(useUi.getState().workspaces[0].panes.map((pane) => pane.id)).not.toContain(created);
    expect(terminalNode("t1")).not.toBeNull();
    expect(mocks.closeTab).not.toHaveBeenCalled();
  });

  it("walks Tab and Shift+Tab through each pane's strip and then its panel in on-screen order", async () => {
    const settings: AppTab = { id: "z-settings", kind: "settings", title: "Settings", closable: true };
    const files: AppTab = { id: "a-files", kind: "files", title: "Files", sessionId: "s", closable: true };
    act(() => {
      useUi.setState({
        workspaces: [
          {
            ...workspace(),
            panes: [
              { id: "left", tabs: [settings], activeTabId: settings.id },
              { id: "right", tabs: [files], activeTabId: files.id },
            ],
            activePaneId: "left",
            layoutTree: {
              type: "split",
              id: "root",
              dir: "row",
              ratio: 0.5,
              a: { type: "leaf", id: "leaf-left", paneId: "left" },
              b: { type: "leaf", id: "leaf-right", paneId: "right" },
            },
          },
        ],
      });
    });
    render();
    await flush();
    const button = (text: string) =>
      [...view!.container.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.textContent === text)!;
    const leftStripLast = [...paneNode("left").querySelectorAll<HTMLButtonElement>(".nx-tabstrip button")].at(-1)!;
    const rightTab = paneNode("right").querySelector<HTMLElement>('[role="tab"]')!;
    const press = (shiftKey = false) => {
      const event = new KeyboardEvent("keydown", { key: "Tab", shiftKey, bubbles: true, cancelable: true });
      act(() => {
        (document.activeElement as HTMLElement).dispatchEvent(event);
      });
      return event;
    };

    // In DOM order the panels follow both strips (and the file panel comes first); focus still
    // goes strip, panel, next strip, panel.
    leftStripLast.focus();
    expect(press().defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(button("settings-action"));
    press();
    expect(document.activeElement).toBe(rightTab);
    press(true);
    expect(document.activeElement).toBe(button("settings-action"));
    press(true);
    expect(document.activeElement).toBe(leftStripLast);

    const rightStripLast = [...paneNode("right").querySelectorAll<HTMLButtonElement>(".nx-tabstrip button")].at(-1)!;
    rightStripLast.focus();
    press();
    expect(document.activeElement).toBe(button("browser-action"));
  });

  it("moves into a panel without focusable content and leaves terminals their own Tab", async () => {
    const audit: AppTab = { id: "audit", kind: "audit", title: "Audit", closable: true };
    act(() => {
      useUi.setState({
        workspaces: [{ ...workspace(), panes: [{ id: "p1", tabs: [audit], activeTabId: audit.id }] }],
      });
    });
    render();
    await flush();
    const panel = view!.container.querySelector<HTMLElement>('[data-tab-content="audit"] [role="tabpanel"]')!;
    panel.querySelector("button")!.remove();
    const last = [...paneNode("p1").querySelectorAll<HTMLButtonElement>(".nx-tabstrip button")].at(-1)!;
    last.focus();
    act(() => {
      last.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true }));
    });
    expect(document.activeElement).toBe(panel);

    // Keys inside a terminal are the terminal's.
    const xterm = document.createElement("div");
    xterm.className = "xterm";
    const input = document.createElement("textarea");
    xterm.append(input);
    panel.append(xterm);
    input.focus();
    const event = new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true });
    act(() => {
      input.dispatchEvent(event);
    });
    expect(event.defaultPrevented).toBe(false);
    expect(document.activeElement).toBe(input);
  });

  it("places tab content below the measured strip, measuring a pane as soon as compact mode shows it", async () => {
    const original = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
      configurable: true,
      get(this: HTMLElement) {
        return this.classList.contains("nx-tabstrip") && !this.closest(".hidden") ? 50 : 0;
      },
    });
    try {
      render(true);
      await flush();
      const content = (id: string) => view!.container.querySelector<HTMLElement>(`[data-tab-content="${id}"]`)!;
      expect(content("t1").style.top).toBe("50px");
      // A second pane arrives while compact mode shows only the first, so it mounts hidden and unmeasured.
      act(() => {
        useUi.setState((state) => ({
          workspaces: state.workspaces.map((item) => ({
            ...item,
            panes: [...item.panes, { id: "p2", tabs: [terminal("t2")], activeTabId: "t2" }],
          })),
        }));
      });
      render(true);
      act(() => useUi.getState().setActivePane("p2", "ws"));
      render(true);
      expect(content("t2").style.top).toBe("50px");
    } finally {
      if (original) Object.defineProperty(HTMLElement.prototype, "offsetHeight", original);
    }
  });
});
