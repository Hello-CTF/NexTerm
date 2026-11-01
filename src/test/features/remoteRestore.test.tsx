/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";

const mocks = vi.hoisted(() => ({
  assetGet: vi.fn(),
  paneMounted: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    assetApi: { get: mocks.assetGet },
  });
});
vi.mock("../../features/remote/RemoteDesktopPane", () => ({
  RemoteDesktopPane: () => {
    mocks.paneMounted();
    return createElement("div", { "data-testid": "remote-pane-stub" });
  },
}));

import { PaneForTab } from "../../app/workspace/PaneForTab";
import { useUi, type AppTab, type Workspace } from "../../app/store";
import { flush, mount, type MountedView } from "./reactTestUtils";

const tab: AppTab = { id: "tab-1", kind: "remote", title: "远程桌面", assetId: "asset-1", closable: true };

function workspace(restorePending: boolean): Workspace {
  return {
    id: "ws-1",
    kind: "remote",
    title: "win-01",
    assetId: "asset-1",
    assetKind: "rdp",
    restorePending: restorePending || undefined,
    panes: [{ id: "pane-1", tabs: [tab], activeTabId: tab.id }],
    activePaneId: "pane-1",
    closable: true,
  };
}

let view: MountedView | undefined;
afterEach(() => {
  view?.unmount();
  view = undefined;
  vi.clearAllMocks();
});

describe("restoring a remote desktop workspace", () => {
  it("shows a reconnect placeholder and does not start the pane", async () => {
    useUi.setState({ workspaces: [workspace(true)], activeWorkspaceId: "ws-1" });
    view = mount(createElement(PaneForTab, { tab, active: true, workspaceId: "ws-1", onClose: async () => undefined }));
    await flush();
    expect(mocks.paneMounted).not.toHaveBeenCalled();
    expect(view.container.textContent).toContain("远程桌面已断开");
    expect(view.container.textContent).toContain("不会自动连接");
    expect(view.container.querySelector("button")?.textContent).toBe("连接");
  });

  it("starts the pane only after the user asks to reconnect", async () => {
    mocks.assetGet.mockResolvedValue({ id: "asset-1", kind: "rdp", name: "win-01", credId: null });
    useUi.setState({ workspaces: [workspace(true)], activeWorkspaceId: "ws-1" });
    view = mount(createElement(PaneForTab, { tab, active: true, workspaceId: "ws-1", onClose: async () => undefined }));
    await flush();
    const button = view.container.querySelector("button")!;
    await act(async () => button.click());
    await flush();
    await flush();
    expect(useUi.getState().workspaces[0].restorePending).toBeUndefined();
    expect(mocks.paneMounted).toHaveBeenCalled();
  });

  it("mounts the pane straight away for a workspace that was opened, not restored", async () => {
    useUi.setState({ workspaces: [workspace(false)], activeWorkspaceId: "ws-1" });
    view = mount(createElement(PaneForTab, { tab, active: true, workspaceId: "ws-1", onClose: async () => undefined }));
    await flush();
    await flush();
    expect(mocks.paneMounted).toHaveBeenCalled();
  });
});
