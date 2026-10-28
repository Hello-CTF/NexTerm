/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  sessionConnect: vi.fn(),
  prepareDurable: vi.fn(),
  vaultStatus: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    sessionApi: { connect: mocks.sessionConnect, prepareDurable: mocks.prepareDurable },
    vaultApi: { status: mocks.vaultStatus },
  });
});

import { connectAsset, useUi } from "../../app/store";
import { useMobileTerminalDockCollapse, useOverlayDocks, type OverlayDocks } from "../../app/shell/useOverlayDocks";

const SSH_ASSET = { id: "asset-1", name: "web-01", kind: "ssh" };

let docks: OverlayDocks | null = null;
let view: MountedView | null = null;

function activeTabKind(): string | null {
  const state = useUi.getState();
  const workspace = state.workspaces.find((item) => item.id === state.activeWorkspaceId);
  const pane = workspace?.panes.find((item) => item.id === workspace.activePaneId);
  return pane?.tabs.find((tab) => tab.id === pane.activeTabId)?.kind ?? null;
}

function MobileDocks() {
  const current = useOverlayDocks(true);
  const kind = useUi(() => activeTabKind());
  useMobileTerminalDockCollapse(current, true, kind);
  docks = current;
  return null;
}

beforeEach(() => {
  vi.clearAllMocks();
  useUi.setState({
    sessions: [],
    workspaces: [],
    activeWorkspaceId: null,
    connectingAssetIds: [],
    connectionProblems: {},
    connectFocusRevision: 0,
    leftOpen: true,
  });
  mocks.sessionConnect.mockResolvedValue({
    id: "s1", assetId: "asset-1", name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 0,
  });
  mocks.prepareDurable.mockResolvedValue({ state: "preparing" });
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  view = mount(createElement(MobileDocks));
});

afterEach(() => {
  view?.unmount();
  view = null;
  docks = null;
});

describe("opening a host on a narrow layout", () => {
  it("closes the asset drawer so the empty workspace is visible", async () => {
    act(() => docks?.setLeftOpen(true));
    expect(docks?.overlayDock).toBe("left");

    await act(async () => {
      await connectAsset(SSH_ASSET);
    });
    await flush();

    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs).toEqual([]);
    expect(docks?.overlayDock).toBeNull();
  });

  it("closes the drawer again when the already open host is tapped", async () => {
    await act(async () => {
      await connectAsset(SSH_ASSET);
    });
    act(() => docks?.setLeftOpen(true));
    expect(docks?.overlayDock).toBe("left");

    await act(async () => {
      await connectAsset(SSH_ASSET);
    });
    await flush();

    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);
    expect(docks?.overlayDock).toBeNull();
  });

  it("does not bring back a drawer remembered from a terminal when switching to a host without one", async () => {
    act(() => {
      useUi.setState({
        workspaces: [{
          id: "ws-tools", kind: "tools", title: "工具", closable: true, activePaneId: "p",
          panes: [{
            id: "p",
            tabs: [
              { id: "settings", kind: "settings", title: "设置", closable: true },
              { id: "t", kind: "terminal", title: "终端 1", closable: true },
            ],
            activeTabId: "settings",
          }],
        }],
        activeWorkspaceId: "ws-tools",
      });
    });
    act(() => docks?.setLeftOpen(true));
    act(() => useUi.getState().setActiveTab("t"));
    expect(docks?.overlayDock).toBeNull();

    await act(async () => {
      await connectAsset(SSH_ASSET);
    });
    await flush();

    expect(activeTabKind()).toBeNull();
    expect(docks?.overlayDock).toBeNull();
  });
});
