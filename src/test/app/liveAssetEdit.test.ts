/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  confirm: vi.fn(),
  reconnect: vi.fn(),
  assetGet: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: { ...actual.assetApi, get: mocks.assetGet },
    vaultApi: { ...actual.vaultApi, status: vi.fn(async () => ({ initialized: true, unlocked: true, passwordless: false })) },
  };
});

vi.mock("../../app/store/workspaceReconnect", () => ({
  confirmSuspectReconnect: mocks.confirm,
  reconnectWorkspaceConnection: mocks.reconnect,
  reconnectLiveSession: vi.fn(),
  workspaceConnectionState: vi.fn(),
  workspaceNeedsReconnect: vi.fn(),
  workspaceHasOrdinaryTerminals: vi.fn(),
}));

import { noticeLiveAssetEdit, useUi } from "../../app/store";
import type { Asset } from "../../ipc/commands";

const before: Asset = {
  id: "a1",
  groupId: null,
  kind: "ssh",
  name: "web-1",
  host: "10.0.0.8",
  port: 22,
  username: "root",
  authKind: "password",
  keyPath: null,
  credId: "c1",
  options: { icon: "server", keepAlive: 30 },
  tags: "",
  note: "",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
  deletedAt: null,
  builtin: false,
};

function seed(status: "connected" | "disconnected"): void {
  useUi.setState({
    toasts: [],
    sessions: [{ id: "s1", assetId: "a1", name: "web-1", kind: "ssh", status, tabs: [], createdAt: 0 }],
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "web-1",
        sessionId: "s1",
        assetId: "a1",
        assetKind: "ssh",
        panes: [{ id: "p1", tabs: [], activeTabId: null }],
        activePaneId: "p1",
        closable: true,
      },
    ],
  });
}

beforeEach(() => {
  mocks.confirm.mockReset().mockResolvedValue(true);
  mocks.reconnect.mockReset().mockResolvedValue(true);
});

afterEach(() => {
  useUi.setState({ toasts: [], sessions: [], workspaces: [] });
});

describe("saving an asset that has a live connection", () => {
  it("says the connection keeps the old settings and offers a reconnect", async () => {
    seed("connected");
    noticeLiveAssetEdit(before, { ...before, host: "10.0.0.9" });
    const toast = useUi.getState().toasts.at(-1);
    expect(toast?.text).toBe("已保存，当前连接仍使用旧设置");
    expect(toast?.action?.label).toBe("重新连接");

    toast?.action?.onSelect();
    await vi.waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("ws1"));
    expect(mocks.confirm).toHaveBeenCalledWith("ws1");
  });

  it("does not reconnect when the user keeps the running terminals", async () => {
    seed("connected");
    mocks.confirm.mockResolvedValue(false);
    noticeLiveAssetEdit(before, { ...before, options: { ...before.options, keepAlive: 60 } });
    useUi.getState().toasts.at(-1)?.action?.onSelect();
    await vi.waitFor(() => expect(mocks.confirm).toHaveBeenCalled());
    expect(mocks.reconnect).not.toHaveBeenCalled();
  });

  it("stays quiet for display-only edits", () => {
    seed("connected");
    const renamed: Asset = { ...before, name: "web-renamed", tags: "prod", note: "n", options: { keepAlive: 30, icon: "db" } };
    noticeLiveAssetEdit(before, renamed);
    expect(useUi.getState().toasts).toEqual([]);
  });

  it("stays quiet when no session of the asset is connected", () => {
    seed("disconnected");
    noticeLiveAssetEdit(before, { ...before, port: 2222 });
    expect(useUi.getState().toasts).toEqual([]);
  });

  it("offers to reopen an open remote desktop with the saved settings", async () => {
    const rdp: Asset = { ...before, kind: "rdp", port: 3389, credId: null, options: {} };
    mocks.assetGet.mockResolvedValue(rdp);
    useUi.setState({
      toasts: [],
      sessions: [],
      workspaces: [
        {
          id: "ws-r",
          kind: "remote",
          title: "win",
          assetId: "a1",
          assetKind: "rdp",
          panes: [
            { id: "p0", tabs: [{ id: "x", kind: "files", title: "x", closable: true }], activeTabId: "x" },
            { id: "p1", tabs: [{ id: "r-old", kind: "remote", title: "远程桌面", assetId: "a1", closable: true }], activeTabId: "r-old" },
          ],
          activePaneId: "p1",
          closable: true,
        },
      ],
      activeWorkspaceId: "ws-r",
    });
    noticeLiveAssetEdit(rdp, { ...rdp, host: "10.0.0.9" });
    const toast = useUi.getState().toasts.at(-1);
    expect(toast?.text).toBe("已保存，当前连接仍使用旧设置");
    expect(toast?.action?.label).toBe("重新连接");

    toast?.action?.onSelect();
    await vi.waitFor(() => {
      const { workspaces } = useUi.getState();
      expect(workspaces.map((item) => item.id)).toEqual(["ws-r"]);
      const remote = workspaces[0]?.panes.flatMap((pane) => pane.tabs).filter((tab) => tab.kind === "remote");
      expect(remote).toHaveLength(1);
      expect(remote?.[0]?.id).not.toBe("r-old");
    });
    expect(mocks.assetGet).toHaveBeenCalledWith("a1");
  });
});
