import { afterEach, describe, expect, it } from "vitest";
import { holdRemoteDesktops, sanitizeLayout, serializeLayout } from "../../app/layout";
import { remoteOverviewRows } from "../../app/sessionAggregate";
import type { AppTab, Workspace } from "../../app/store";
import { buildStatusWall } from "../../features/mobile/statusWallModel";
import {
  MAX_THUMBNAILS,
  acquireThumbnailDemand,
  clearRemoteStatus,
  resetRemoteStatusForTest,
  setRemoteStatus,
  setRemoteThumbnail,
  thumbnailsWanted,
  useRemoteStatus,
} from "../../features/remote/remoteStatus";
import { workspaceLogicalKey, tabLogicalKey } from "../../app/layoutMerge";

function remoteTab(id: string): AppTab {
  return { id, kind: "remote", title: "远程桌面", assetId: "asset-1", closable: true };
}

function remoteWorkspace(id: string, tabId: string, kind = "rdp"): Workspace {
  return {
    id,
    kind: "remote",
    title: "win-01",
    assetId: "asset-1",
    assetKind: kind,
    panes: [{ id: `pane-${id}`, tabs: [remoteTab(tabId)], activeTabId: tabId }],
    activePaneId: `pane-${id}`,
    closable: true,
  };
}

const source = (workspaces: Workspace[]) => ({
  leftOpen: true,
  leftMode: "assets" as const,
  rightOpen: false,
  leftWidth: 260,
  rightWidth: 320,
  workspaces,
  activeWorkspaceId: workspaces[0]?.id ?? null,
});

afterEach(resetRemoteStatusForTest);

describe("remote desktop in the persisted layout", () => {
  it("keeps remote workspaces and tabs through sanitize", () => {
    const layout = serializeLayout(source([remoteWorkspace("ws-1", "t-1")]));
    expect(layout?.workspaces).toHaveLength(1);
    const workspace = layout!.workspaces[0];
    expect(workspace.kind).toBe("remote");
    expect(workspace.assetKind).toBe("rdp");
    expect(workspace.panes[0].tabs[0]).toMatchObject({ id: "t-1", kind: "remote", assetId: "asset-1" });
  });

  it("round-trips through JSON as the sync does", () => {
    const layout = serializeLayout(source([remoteWorkspace("ws-1", "t-1")]));
    const again = sanitizeLayout(JSON.parse(JSON.stringify(layout)));
    expect(again?.workspaces[0].panes[0].tabs[0].kind).toBe("remote");
  });

  it("keeps two connections to one asset as two workspaces", () => {
    const layout = serializeLayout(source([remoteWorkspace("ws-1", "t-1"), remoteWorkspace("ws-2", "t-2")]));
    expect(layout?.workspaces.map((workspace) => workspace.id)).toEqual(["ws-1", "ws-2"]);
    expect(workspaceLogicalKey(remoteWorkspace("ws-1", "t-1"))).not.toBe(workspaceLogicalKey(remoteWorkspace("ws-2", "t-2")));
    expect(tabLogicalKey(remoteTab("t-1"))).not.toBe(tabLogicalKey(remoteTab("t-2")));
  });

  it("still drops an unknown tab kind", () => {
    const layout = sanitizeLayout({
      v: 1,
      workspaces: [{ id: "w", kind: "remote", title: "x", panes: [{ id: "p", tabs: [{ id: "t", kind: "nonsense", title: "x" }], activeTabId: "t" }], activePaneId: "p" }],
    });
    expect(layout?.workspaces[0]?.panes[0].tabs ?? []).toHaveLength(0);
  });

  it("carries no secret: a password-shaped field on a tab is dropped", () => {
    const poisoned = { ...remoteWorkspace("ws-1", "t-1") } as Workspace;
    (poisoned.panes[0].tabs[0] as unknown as Record<string, unknown>).password = "hunter2";
    (poisoned as unknown as Record<string, unknown>).ticket = "ticket-123";
    const text = JSON.stringify(serializeLayout(source([poisoned])));
    expect(text).not.toContain("hunter2");
    expect(text).not.toContain("ticket-123");
  });
});

describe("applying a layout never connects a remote desktop", () => {
  it("holds a workspace that has no live pane here, with its tabs", () => {
    const [held] = holdRemoteDesktops([remoteWorkspace("ws-1", "t-1")]);
    expect(held.restorePending).toBe(true);
    expect(held.panes[0].tabs[0].restorePending).toBe(true);
  });

  it("leaves a workspace whose pane is mounted here connected, even if the layout says pending", () => {
    setRemoteStatus("t-1", "connected");
    const incoming = { ...remoteWorkspace("ws-1", "t-1"), restorePending: true };
    expect(holdRemoteDesktops([incoming])[0].restorePending).toBeUndefined();
  });

  it("does not store the waiting state, so it never travels to another device", () => {
    const stored = serializeLayout(source([{ ...remoteWorkspace("ws-1", "t-1"), restorePending: true }]));
    expect(stored?.workspaces[0].restorePending).toBeUndefined();
  });

  it("does not touch other workspace kinds", () => {
    const session: Workspace = { ...remoteWorkspace("ws-2", "t-2"), kind: "session" };
    expect(holdRemoteDesktops([session])[0]).toBe(session);
  });
});

describe("remote desktops in the overview", () => {
  it("lists remote tabs of the open workspaces with their state", () => {
    const workspaces = [remoteWorkspace("ws-1", "t-1"), remoteWorkspace("ws-2", "t-2", "vnc")];
    setRemoteStatus("t-1", "connected");
    const rows = remoteOverviewRows(workspaces, useRemoteStatus.getState().entries);
    expect(rows.map((row) => [row.tabId, row.status, row.protocol])).toEqual([
      ["t-1", "connected", "rdp"],
      ["t-2", "disconnected", "vnc"],
    ]);
  });

  it("puts connected sessions in running and dropped ones in finished, never in needs_me", () => {
    const workspaces = [remoteWorkspace("ws-1", "t-1"), remoteWorkspace("ws-2", "t-2")];
    setRemoteStatus("t-1", "connected");
    setRemoteThumbnail("t-1", "data:image/jpeg;base64,AAAA");
    const rows = remoteOverviewRows(workspaces, useRemoteStatus.getState().entries);
    const model = buildStatusWall(new Map(), workspaces, [], new Map(), rows);
    expect(model.groups.needs_me).toHaveLength(0);
    expect(model.groups.running.map((card) => card.uiTabId)).toEqual(["t-1"]);
    expect(model.groups.running[0]).toMatchObject({ stateLabel: "已连接", remote: { protocol: "rdp", thumb: "data:image/jpeg;base64,AAAA" } });
    expect(model.groups.finished.map((card) => card.uiTabId)).toEqual(["t-2"]);
    expect(model.groups.finished[0].stateLabel).toBe("已断开");
  });

  it("counts from the open workspaces: a closed tab leaves no row", () => {
    setRemoteStatus("gone", "connected");
    expect(remoteOverviewRows([], useRemoteStatus.getState().entries)).toEqual([]);
  });

  it("keeps the last picture of a dropped session", () => {
    setRemoteStatus("t-1", "connected");
    setRemoteThumbnail("t-1", "data:image/jpeg;base64,AAAA", 1);
    setRemoteStatus("t-1", "disconnected");
    expect(useRemoteStatus.getState().entries["t-1"]).toMatchObject({ status: "disconnected", thumb: "data:image/jpeg;base64,AAAA" });
  });

  it("bounds the pictures it holds and drops the oldest first", () => {
    for (let index = 0; index < MAX_THUMBNAILS + 3; index += 1) {
      setRemoteStatus(`t-${index}`, "connected");
      setRemoteThumbnail(`t-${index}`, `data:image/jpeg;base64,${index}`, index + 1);
    }
    const withPicture = Object.entries(useRemoteStatus.getState().entries).filter(([, entry]) => entry.thumb);
    expect(withPicture).toHaveLength(MAX_THUMBNAILS);
    expect(useRemoteStatus.getState().entries["t-0"].thumb).toBeUndefined();
    expect(useRemoteStatus.getState().entries[`t-${MAX_THUMBNAILS + 2}`].thumb).toBeDefined();
  });

  it("forgets a tab that closed", () => {
    setRemoteStatus("t-1", "connected");
    clearRemoteStatus("t-1");
    expect(useRemoteStatus.getState().entries["t-1"]).toBeUndefined();
  });

  it("captures pictures only while the overview asks for them", () => {
    expect(thumbnailsWanted()).toBe(false);
    const release = acquireThumbnailDemand();
    expect(thumbnailsWanted()).toBe(true);
    release();
    release();
    expect(thumbnailsWanted()).toBe(false);
  });
});
