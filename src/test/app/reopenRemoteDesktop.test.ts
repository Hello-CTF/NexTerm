/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Workspace } from "../../app/store";

const mocks = vi.hoisted(() => ({ get: vi.fn() }));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, assetApi: { ...actual.assetApi, get: mocks.get } };
});

import { reopenRemoteDesktopTab, useUi } from "../../app/store";

function remoteWorkspace(): Workspace {
  return {
    id: "ws",
    kind: "remote",
    title: "desk",
    assetId: "a1",
    assetKind: "rdp",
    panes: [
      { id: "p1", tabs: [], activeTabId: null },
      { id: "p2", tabs: [], activeTabId: null },
    ],
    activePaneId: "p1",
    closable: true,
  };
}

function remoteTabCount(): number {
  return useUi.getState().workspaces[0].panes.reduce((n, p) => n + p.tabs.filter((t) => t.kind === "remote").length, 0);
}

beforeEach(() => {
  mocks.get.mockReset().mockResolvedValue({ id: "a1", name: "desk", kind: "rdp" });
  useUi.setState({ workspaces: [remoteWorkspace()], activeWorkspaceId: "ws", toasts: [], workspaceRuntimeEpoch: 0 });
});

describe("reopenRemoteDesktopTab", () => {
  it("adds one remote tab to the requested pane", async () => {
    expect(await reopenRemoteDesktopTab("ws", "p2")).toEqual({ ok: true });
    const panes = useUi.getState().workspaces[0].panes;
    expect(panes[1].tabs.map((t) => t.kind)).toEqual(["remote"]);
    expect(panes[0].tabs).toEqual([]);
  });

  it("does not add a second tab when two panes reconnect concurrently", async () => {
    const [a, b] = await Promise.all([reopenRemoteDesktopTab("ws", "p1"), reopenRemoteDesktopTab("ws", "p2")]);
    expect(remoteTabCount()).toBe(1);
    expect([a.ok, b.ok].filter(Boolean)).toHaveLength(1);
  });

  it("refuses when the asset is no longer a remote desktop", async () => {
    mocks.get.mockResolvedValue({ id: "a1", name: "desk", kind: "ssh" });
    const result = await reopenRemoteDesktopTab("ws", "p1");
    expect(result.ok).toBe(false);
    expect(remoteTabCount()).toBe(0);
  });
});
