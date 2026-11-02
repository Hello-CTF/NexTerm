/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  sessionConnect: vi.fn(),
  dbConnect: vi.fn(),
  ask: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    sessionApi: { connect: mocks.sessionConnect },
    dbApi: { connect: mocks.dbConnect },
    vaultApi: { status: vi.fn(async () => ({ initialized: true, unlocked: true, passwordless: false })) },
  };
});
vi.mock("../../ui/dialogs", async (importOriginal) => ({ ...(await importOriginal<typeof import("../../ui/dialogs")>()), ask: mocks.ask }));

import { closeTabHint, collectWorkspaceClose, confirmWorkspaceClose, connectAsset, useSessionSummary, useUi } from "../../app/store";
import { act, createElement } from "react";
import { flush, mount } from "./reactTestUtils";
import { resetRemoteStatusForTest, setRemoteStatus } from "../../features/remote/remoteStatus";

const rdp = { id: "asset-1", name: "win-01", kind: "rdp" };

afterEach(() => {
  useUi.setState({ workspaces: [], activeWorkspaceId: null });
  resetRemoteStatusForTest();
  vi.clearAllMocks();
});

describe("opening a remote desktop asset", () => {
  it("opens a remote workspace and does not use the terminal session connect", async () => {
    const outcome = await connectAsset(rdp);
    expect(outcome).toEqual({ ok: true });
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    expect(mocks.dbConnect).not.toHaveBeenCalled();
    const { workspaces, activeWorkspaceId } = useUi.getState();
    expect(workspaces).toHaveLength(1);
    expect(workspaces[0]).toMatchObject({ kind: "remote", assetId: "asset-1", assetKind: "rdp", title: "win-01" });
    expect(activeWorkspaceId).toBe(workspaces[0].id);
    expect(workspaces[0].panes[0].tabs[0]).toMatchObject({ kind: "remote", assetId: "asset-1", title: "远程桌面" });
  });

  it("allows several connections to one asset, each its own workspace", async () => {
    await connectAsset(rdp);
    await connectAsset(rdp);
    const { workspaces } = useUi.getState();
    expect(workspaces).toHaveLength(2);
    expect(workspaces.map((workspace) => workspace.title)).toEqual(["win-01", "win-01（2）"]);
    expect(new Set(workspaces.flatMap((workspace) => workspace.panes.flatMap((pane) => pane.tabs.map((tab) => tab.id)))).size).toBe(2);
  });

  it("opens a vnc asset the same way", async () => {
    await connectAsset({ id: "asset-2", name: "box", kind: "vnc" });
    expect(useUi.getState().workspaces[0]).toMatchObject({ kind: "remote", assetKind: "vnc" });
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
  });
});

describe("closing remote desktops", () => {
  it("asks before closing a workspace with a connected remote desktop", async () => {
    await connectAsset(rdp);
    const workspace = useUi.getState().workspaces[0];
    const tab = workspace.panes[0].tabs[0];
    setRemoteStatus(tab.id, "connected");
    mocks.ask.mockResolvedValue(true);
    const summary = collectWorkspaceClose([tab], "rdp", "remote", workspace.id);
    expect(summary.liveRemote).toHaveLength(1);
    expect(await confirmWorkspaceClose(summary, workspace.title)).toBe(true);
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(String(mocks.ask.mock.calls[0][0])).toContain("断开后会话结束");
  });

  it("returns false when the user keeps the connection", async () => {
    await connectAsset(rdp);
    const workspace = useUi.getState().workspaces[0];
    const tab = workspace.panes[0].tabs[0];
    setRemoteStatus(tab.id, "connected");
    mocks.ask.mockResolvedValue(false);
    expect(await confirmWorkspaceClose(collectWorkspaceClose([tab], "rdp", "remote", workspace.id), workspace.title)).toBe(false);
  });

  it("closes a disconnected remote desktop without asking", async () => {
    await connectAsset(rdp);
    const workspace = useUi.getState().workspaces[0];
    const tab = workspace.panes[0].tabs[0];
    setRemoteStatus(tab.id, "disconnected");
    expect(await confirmWorkspaceClose(collectWorkspaceClose([tab], "rdp", "remote", workspace.id), workspace.title)).toBe(true);
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("describes the close button as ending the session", () => {
    expect(closeTabHint({ id: "t", kind: "remote", title: "远程桌面", closable: true })).toContain("断开后会话结束");
  });
});

describe("status bar count", () => {
  function Summary() {
    const summary = useSessionSummary();
    return createElement("span", { "data-testid": "summary" }, `${summary.total}/${summary.connected}`);
  }

  it("counts remote desktops from the open workspaces, connected ones apart", async () => {
    await connectAsset(rdp);
    await connectAsset({ id: "asset-2", name: "box", kind: "vnc" });
    const tabs = useUi.getState().workspaces.map((workspace) => workspace.panes[0].tabs[0].id);
    setRemoteStatus(tabs[0], "connected");
    const view = mount(createElement(Summary));
    await flush();
    expect(view.container.textContent).toBe("2/1");
    await act(async () => {
      await useUi.getState().closeWorkspace(useUi.getState().workspaces[1].id);
    });
    expect(view.container.textContent).toBe("1/1");
    view.unmount();
  });
});
