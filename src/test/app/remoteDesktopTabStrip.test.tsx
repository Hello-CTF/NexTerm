/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, type MountedView } from "../features/reactTestUtils";
import { workspaceBodyProps } from "../helpers/splitViewHarness";

vi.mock("../../ui/dialogs", () => ({ ask: vi.fn(async () => true), askChoice: vi.fn() }));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    terminalApi: { listLive: vi.fn(async () => []) },
  });
});
vi.mock("../../app/lazyPanels", async () => {
  const { lazyPanelsMock } = await import("../helpers/splitViewHarness");
  const { useStriplessLeftDockToggle } = await import("../../app/workspace/leftDockToggle");
  const { createElement } = await import("react");
  // The desktop stub offers the left dock toggle the way the real desktop toolbar does.
  function RemoteDesktopPane({ tabId }: { tabId: string }) {
    const leftDock = useStriplessLeftDockToggle(tabId);
    return leftDock
      ? createElement("button", { type: "button", "data-testid": "desktop-left-dock", "aria-pressed": leftDock.open, onClick: leftDock.toggle })
      : null;
  }
  return { ...lazyPanelsMock(), RemoteDesktopPane };
});

import { useUi, type AppTab, type Workspace } from "../../app/store";
import { WorkspaceBody } from "../../app/shell/WorkspaceBody";

const DEFAULT_STRIP = 35;

let view: MountedView | null = null;

afterEach(() => {
  view?.unmount();
  view = null;
  setLeftOpen.mockReset();
});

const desktop: AppTab = { id: "rd", kind: "remote", title: "build-box", assetId: "asset-rd", closable: true };
const notes: AppTab = { id: "audit", kind: "audit", title: "审计", closable: true };

function remoteWorkspace(tabs: AppTab[]): Workspace {
  return {
    id: "ws",
    kind: "remote",
    title: "build-box",
    assetId: "asset-rd",
    assetKind: "vnc",
    panes: [{ id: "p1", tabs, activeTabId: tabs[0].id }],
    activePaneId: "p1",
    closable: true,
  };
}

const setLeftOpen = vi.fn();

async function render(): Promise<void> {
  const base = workspaceBodyProps(useUi.getState().workspaces, false);
  const props = { ...base, docks: { ...base.docks, leftDockOpen: true, setLeftOpen } };
  if (!view) view = mount(createElement(WorkspaceBody, props));
  else act(() => view!.root.render(createElement(WorkspaceBody, props)));
  await flush();
}

function tabStrip(): Element | null {
  return view!.container.querySelector('[role="tablist"]');
}

function desktopLeftDock(): HTMLButtonElement | null {
  return view!.container.querySelector<HTMLButtonElement>('[data-testid="desktop-left-dock"]');
}

function contentTop(tabId: string): string | undefined {
  return view!.container.querySelector<HTMLElement>(`[data-tab-content="${tabId}"]`)?.style.top;
}

describe("remote desktop workspace chrome", () => {
  it("shows the desktop without a tab strip, using the whole workspace", async () => {
    useUi.setState({ workspaces: [remoteWorkspace([desktop])], activeWorkspaceId: "ws" });
    await render();
    expect(tabStrip()).toBeNull();
    // jsdom measures no strip, so a pane with one is offset by the default strip height.
    expect(contentTop("rd")).not.toContain(`${DEFAULT_STRIP}px`);
  });

  it("brings the tab strip back once the workspace holds another tab", async () => {
    useUi.setState({ workspaces: [remoteWorkspace([desktop, notes])], activeWorkspaceId: "ws" });
    await render();
    expect(tabStrip()).not.toBeNull();
    expect(contentTop("rd")).toContain(`${DEFAULT_STRIP}px`);
  });

  it("lets the desktop shown alone collapse the left dock its missing tab strip would have", async () => {
    useUi.setState({ workspaces: [remoteWorkspace([desktop])], activeWorkspaceId: "ws" });
    await render();
    const toggle = desktopLeftDock();
    expect(toggle?.getAttribute("aria-pressed")).toBe("true");
    act(() => toggle!.click());
    expect(setLeftOpen).toHaveBeenCalledWith(false);
  });

  it("leaves the left dock toggle to the tab strip once the strip is back", async () => {
    useUi.setState({ workspaces: [remoteWorkspace([desktop, notes])], activeWorkspaceId: "ws" });
    await render();
    expect(desktopLeftDock()).toBeNull();
    expect(view!.container.querySelector('[aria-label="收起左侧栏"]')).not.toBeNull();
  });
});
