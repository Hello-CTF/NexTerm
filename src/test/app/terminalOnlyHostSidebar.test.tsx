/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { mount, type MountedView } from "../features/reactTestUtils";
import { workspaceBodyProps } from "../helpers/splitViewHarness";

vi.mock("../../ui/dialogs", () => ({ ask: vi.fn(async () => true), askChoice: vi.fn() }));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    terminalApi: { closeTab: vi.fn(async () => undefined), detach: vi.fn(async () => undefined), listLive: vi.fn(async () => []) },
  });
});
vi.mock("../../app/lazyPanels", async () => (await import("../helpers/splitViewHarness")).lazyPanelsMock());

import { useUi, type Workspace } from "../../app/store";
import { WorkspaceBody } from "../../app/shell/WorkspaceBody";
import { resolveLeftView } from "../../app/shell/leftView";
import { AppRail } from "../../app/shell/AppRail";
import type { WorkspaceActions } from "../../app/shell/useWorkspaceActions";

let view: MountedView | null = null;

function workspace(assetKind: string): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "host",
    sessionId: "s",
    assetId: "a",
    assetKind,
    panes: [{ id: "p1", tabs: [], activeTabId: null }],
    activePaneId: "p1",
    closable: true,
  };
}

function render(assetKind: string): void {
  useUi.setState({ workspaces: [workspace(assetKind)], activeWorkspaceId: "ws", sessions: [], toasts: [], leftMode: "assets", leftOverride: null });
  const props = workspaceBodyProps(useUi.getState().workspaces, true);
  view = mount(createElement(WorkspaceBody, { ...props, docks: { ...props.docks, leftDockOpen: true } }));
}

beforeEach(() => {
  view?.unmount();
  view = null;
});

afterEach(() => {
  view?.unmount();
  view = null;
});

describe("terminal-only hosts have no file tree", () => {
  it("shows the asset tree instead of the file tree in a Telnet workspace", () => {
    render("telnet");
    expect(view!.container.querySelector('[data-panel="files"]')).toBeNull();
    expect(view!.container.querySelector('[data-panel="assets"]')).not.toBeNull();
  });

  it("keeps the file tree for an SSH workspace", () => {
    render("ssh");
    expect(view!.container.querySelector('[data-panel="files"]')).not.toBeNull();
  });

  it("follows the user's sidebar choice or a temporary override", () => {
    const telnet = workspace("telnet");
    expect(resolveLeftView(telnet, "credentials", null)).toBe("credentials");
    expect(resolveLeftView(telnet, "assets", "credentials")).toBe("credentials");
    expect(resolveLeftView(workspace("ssh"), "assets", null)).toBe("files");
  });
});

describe("terminal-only hosts have no host tools in the rail", () => {
  function railLabels(assetKind: string): string[] {
    const ws = workspace(assetKind);
    useUi.setState({ workspaces: [ws], activeWorkspaceId: "ws", sessions: [], toasts: [] });
    const actions = new Proxy({}, { get: () => vi.fn() }) as unknown as WorkspaceActions;
    view = mount(
      createElement(AppRail, {
        overlaySidebars: false,
        setOverlayDock: vi.fn(),
        setLeftOpen: vi.fn(),
        leftDockOpen: true,
        backgroundAvailable: false,
        workspace: ws,
        hasDbAssets: false,
        actions,
      }),
    );
    return [...view.container.querySelectorAll("button[aria-label]")].map((button) => button.getAttribute("aria-label") ?? "");
  }

  it("offers no containers, mounts, forwarding or logs on a Telnet host", () => {
    const labels = railLabels("telnet");
    for (const label of ["容器", "磁盘挂载", "端口转发", "日志"]) expect(labels).not.toContain(label);
    expect(labels).toContain("终端历史");
  });

  it("keeps them on an SSH host", () => {
    const labels = railLabels("ssh");
    for (const label of ["容器", "端口转发", "日志"]) expect(labels).toContain(label);
  });
});
