import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  layoutApi: { get: vi.fn(), put: vi.fn() },
  terminalApi: { listLive: vi.fn() },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
}));
vi.mock("../../ipc/events", () => ({ listenEvent: vi.fn() }));

import { mergeDirtyEditorTabs, type PersistedLayout } from "../../app/layout";
import type { AppTab, Workspace } from "../../app/store";
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
    splitRatio: 0.5,
    closable: true,
  };
}
function layout(workspaces: Workspace[]): PersistedLayout {
  return {
    v: 1,
    leftOpen: true,
    leftMode: "assets",
    rightOpen: true,
    leftWidth: 248,
    rightWidth: 352,
    workspaces,
    activeWorkspaceId: workspaces[0]?.id ?? null,
  };
}

describe("remote layout dirty-editor preservation", () => {
  afterEach(() => setFileEditorDirty("s", "/dirty.txt", false));

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
});
