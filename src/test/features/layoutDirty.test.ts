/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const server = vi.hoisted(() => ({
  revision: 0,
  data: null as string | null,
  putAlwaysConflicts: false,
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    layoutApi: {
      get: vi.fn(async () => ({
        revision: server.revision,
        updatedAt: 0,
        data: server.data === null ? null : JSON.parse(server.data),
      })),
      put: vi.fn(async (data: string, revision: number) => {
        if (server.putAlwaysConflicts || revision !== server.revision) {
          return { saved: false, revision: server.revision, conflict: true };
        }
        server.revision = revision + 1;
        server.data = data;
        return { saved: true, revision: server.revision, conflict: false };
      }),
    },
    terminalApi: { listLive: vi.fn(async () => []) },
  });
});
vi.mock("../../ipc/events", () => ({ listenEvent: vi.fn() }));

import { mergeDirtyEditorTabs, sanitizeLayout, type PersistedLayout } from "../../app/layout";
import type { AppTab, SqlDraft, Workspace } from "../../app/store";
import { setFileEditorDirty } from "../../features/files/editorGuards";

function tab(id: string, path?: string): AppTab {
  return { id, kind: "files", title: id, sessionId: "s", path, closable: true };
}
function terminal(): AppTab {
  return { id: "terminal", kind: "terminal", title: "terminal", sessionId: "s", tabId: "kernel", closable: true };
}
function workspace(tabs: AppTab[]): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "workspace",
    panes: [{ id: "pane", tabs, activeTabId: tabs[0]?.id ?? null }],
    activePaneId: "pane",
    closable: true,
  };
}
function layout(workspaces: Workspace[]): PersistedLayout {
  return {
    v: 2,
    leftOpen: true,
    leftMode: "assets",
    rightOpen: true,
    leftWidth: 248,
    rightWidth: 352,
    workspaces,
    activeWorkspaceId: workspaces[0]?.id ?? null,
    aiBoards: {},
  };
}
function sqlDraft(text = "SELECT 1"): SqlDraft {
  return {
    text,
    selection: { from: 0, to: text.length },
    schema: "public",
    connId: "conn-1",
    dbKind: "mysql",
  };
}
async function openSync() {
  vi.resetModules();
  const sync = await import("../../app/layout");
  const store = await import("../../app/store");
  sync.startLayoutSync();
  await sync.layoutBootstrapped;
  return { sync, store: store.useUi };
}

describe("remote layout dirty-editor preservation", () => {
  beforeEach(() => {
    server.revision = 0;
    server.data = null;
    server.putAlwaysConflicts = false;
  });
  afterEach(() => {
    setFileEditorDirty("s", "/dirty.txt", false);
    vi.resetModules();
    vi.clearAllMocks();
  });

  it("restores only the dirty editor when a remote change removes the whole workspace", () => {
    const dirty = tab("dirty", "/dirty.txt");
    const clean = tab("clean", "/clean.txt");
    const current = [workspace([dirty, clean, terminal()])];
    setFileEditorDirty("s", "/dirty.txt", true);
    const merged = mergeDirtyEditorTabs(layout([]), current);
    expect(merged).toHaveLength(1);
    expect(merged[0].panes).toHaveLength(1);
    expect(merged[0].panes[0].tabs).toEqual([dirty]);
  });

  it("merges a removed dirty tab without duplicating it or retaining clean tabs", () => {
    const dirty = tab("dirty", "/dirty.txt");
    const clean = tab("clean", "/clean.txt");
    const current = [workspace([dirty, clean])];
    setFileEditorDirty("s", "/dirty.txt", true);
    const remote = layout([workspace([clean])]);
    const merged = mergeDirtyEditorTabs(remote, current);
    expect(merged[0].panes[0].tabs).toEqual([clean, dirty]);
    const alreadyRemote = mergeDirtyEditorTabs(layout([workspace([dirty])]), current);
    expect(alreadyRemote[0].panes[0].tabs).toEqual([dirty]);
  });

  it("returns the remote workspaces unchanged when no editor is dirty", () => {
    const remote = layout([workspace([tab("clean", "/clean.txt")])]);
    expect(mergeDirtyEditorTabs(remote, [workspace([tab("dirty", "/dirty.txt")])])).toBe(
      remote.workspaces,
    );
  });

  it("preserves a dirty editor while normalizing legacy background ownership", () => {
    const dirty = tab("dirty", "/dirty.txt");
    const currentHost = workspace([dirty]);
    currentHost.sessionId = "s";
    currentHost.assetId = "asset-1";
    setFileEditorDirty("s", "/dirty.txt", true);

    const remoteHost = workspace([terminal()]);
    remoteHost.sessionId = "s";
    remoteHost.assetId = "asset-1";
    const tools: Workspace = {
      id: "tools",
      kind: "tools",
      title: "工具",
      panes: [
        {
          id: "tools-pane",
          tabs: [
            { id: "history", kind: "history", title: "History", closable: true },
            { id: "background", kind: "background", title: "Background", assetId: "asset-1", closable: true },
          ],
          activeTabId: "history",
        },
      ],
      activePaneId: "tools-pane",
      closable: true,
    };
    const remote = sanitizeLayout(layout([tools, remoteHost]));
    expect(remote).not.toBeNull();

    const merged = mergeDirtyEditorTabs(remote as PersistedLayout, [currentHost]);
    const host = merged.find((w) => w.id === "ws");
    expect(host?.panes[0].tabs.map((t) => t.kind)).toEqual(["terminal", "background", "files"]);
    expect(host?.panes[0].tabs[1].id).toBe("background-ws");
    expect(host?.panes[0].tabs[2]).toEqual(dirty);
    expect(merged.flatMap((w) => w.panes.flatMap((p) => p.tabs)).filter((t) => t.kind === "terminal")).toHaveLength(1);
  });

  it("preserves non-empty SQL drafts on remote layout removal and clears only unprotected runtime", async () => {
    const { sync, store } = await openSync();
    const withDraft = workspace([]);
    const empty = { ...workspace([]), id: "empty" };
    store.setState({
      workspaces: [withDraft, empty],
      activeWorkspaceId: "ws",
      aiBoards: {},
      workspaceRuntime: {},
      toasts: [],
    });
    store.getState().updateSqlDraft("ws", sqlDraft());
    store.getState().updateSqlDraft("empty", { ...sqlDraft(), text: "" });
    await sync.flushLayout();
    server.revision = 2;
    server.data = JSON.stringify(layout([]));

    sync.onRemoteChange({ revision: 2 });

    await vi.waitFor(() => {
      expect(store.getState().workspaces.some((w) => w.id === "ws")).toBe(true);
      expect(store.getState().getWorkspaceRuntime("empty")).toBeUndefined();
    });
    expect(store.getState().getWorkspaceRuntime("ws")?.sqlDraft).toEqual(sqlDraft());
  });

  it("preserves non-empty SQL drafts on conflict recovery and clears only unprotected runtime", async () => {
    const { sync, store } = await openSync();
    const withDraft = workspace([]);
    const empty = { ...workspace([]), id: "empty" };
    store.setState({
      workspaces: [withDraft, empty],
      activeWorkspaceId: "ws",
      aiBoards: {},
      workspaceRuntime: {},
      toasts: [],
    });
    store.getState().updateSqlDraft("ws", sqlDraft());
    store.getState().updateSqlDraft("empty", { ...sqlDraft(), text: "" });
    await sync.flushLayout();
    store.setState({
      workspaces: [withDraft, { ...empty, title: "local edit" }],
    });
    server.revision = 7;
    server.data = JSON.stringify(layout([]));
    server.putAlwaysConflicts = true;

    await sync.flushLayout();

    expect(store.getState().workspaces.some((w) => w.id === "ws")).toBe(true);
    expect(store.getState().getWorkspaceRuntime("ws")?.sqlDraft).toEqual(sqlDraft());
    expect(store.getState().getWorkspaceRuntime("empty")).toBeUndefined();
  });
});
