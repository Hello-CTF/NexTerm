/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Workspace } from "../../app/store";
import { captureDeviceLayout, deviceWorkspaceDocks } from "../../app/layoutFocus";

vi.mock("../../ipc/commands", () => ({
  sessionApi: {},
  assetApi: {},
  dbApi: {},
  terminalApi: {},
  vaultApi: {},
}));

function workspace(id: string): Workspace {
  return {
    id,
    kind: "tools",
    title: id,
    panes: [{ id: `pane-${id}`, tabs: [], activeTabId: null }],
    activePaneId: `pane-${id}`,
    closable: true,
  };
}

async function freshStore() {
  vi.resetModules();
  const { useUi } = await import("../../app/store");
  useUi.setState({ workspaces: [workspace("ws-a"), workspace("ws-b")], activeWorkspaceId: "ws-a", leftOpen: true, rightOpen: true });
  return useUi;
}

beforeEach(() => {
  localStorage.clear();
});

describe("sidebars per workspace", () => {
  it("restores what each workspace showed when switching back to it", async () => {
    const store = await freshStore();
    store.getState().setRightOpen(false);

    store.setState({ activeWorkspaceId: "ws-b" });
    // A workspace without a record keeps the current sidebars.
    expect(store.getState()).toMatchObject({ leftOpen: true, rightOpen: false });
    store.getState().setRightOpen(true);
    store.getState().setLeftOpen(false);

    store.setState({ activeWorkspaceId: "ws-a" });
    expect(store.getState()).toMatchObject({ leftOpen: true, rightOpen: false });

    store.setState({ activeWorkspaceId: "ws-b" });
    expect(store.getState()).toMatchObject({ leftOpen: false, rightOpen: true });
  });

  it("stores each workspace's sidebars in the device layout", async () => {
    const store = await freshStore();
    store.getState().setRightOpen(false);
    store.setState({ activeWorkspaceId: "ws-b" });
    store.getState().setRightOpen(true);
    store.getState().setLeftOpen(false);

    const device = captureDeviceLayout(store.getState());

    expect(deviceWorkspaceDocks(device)).toEqual({
      "ws-a": { leftOpen: true, rightOpen: false },
      "ws-b": { leftOpen: false, rightOpen: true },
    });
  });
});
