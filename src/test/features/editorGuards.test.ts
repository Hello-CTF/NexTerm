import { afterEach, describe, expect, it } from "vitest";
import type { AppTab, Pane, Workspace } from "../../app/store";
import {
  dirtyFileEditors,
  setFileEditorDirty,
  shouldClearEditorDirty,
  shouldHandleEditorSave,
  type EditorGuardState,
} from "../../features/files/editorGuards";

function tab(id: string, path?: string, terminal = false): AppTab {
  return {
    id,
    kind: terminal ? "terminal" : "files",
    title: id,
    sessionId: "session",
    path,
    tabId: terminal ? `kernel-${id}` : undefined,
    closable: true,
  };
}

function workspace(id: string, panes: Pane[]): Workspace {
  return {
    id,
    kind: "session",
    title: id,
    panes,
    activePaneId: panes[0].id,
    splitRatio: 0.5,
    closable: true,
  };
}

function state(workspaces: Workspace[], activeWorkspaceId: string): EditorGuardState {
  return { workspaces, activeWorkspaceId };
}

describe("active dirty editor routing", () => {
  const a = tab("a", "/a.txt");
  const b = tab("b", "/b.txt");
  const c = tab("c", "/c.txt");
  const d = tab("d", "/d.txt");
  const ws1 = workspace("ws1", [
    { id: "p1", tabs: [a, b], activeTabId: "a" },
    { id: "p2", tabs: [c], activeTabId: "c" },
  ]);
  const ws2 = workspace("ws2", [{ id: "p3", tabs: [d], activeTabId: "d" }]);

  it("selects only the active editor among hidden tabs, panes and workspaces", () => {
    const current = state([ws1, ws2], "ws1");
    expect(shouldHandleEditorSave(current, "session", "/a.txt", true, false)).toBe(true);
    expect(shouldHandleEditorSave(current, "session", "/b.txt", true, false)).toBe(false);
    expect(shouldHandleEditorSave(current, "session", "/c.txt", true, false)).toBe(false);
    expect(shouldHandleEditorSave(current, "session", "/d.txt", true, false)).toBe(false);
    expect(shouldHandleEditorSave(state([ws1, ws2], "ws2"), "session", "/d.txt", true, false)).toBe(true);
  });

  it("does not save clean or already in-flight editors", () => {
    const current = state([ws1], "ws1");
    expect(shouldHandleEditorSave(current, "session", "/a.txt", false, false)).toBe(false);
    expect(shouldHandleEditorSave(current, "session", "/a.txt", true, true)).toBe(false);
  });

  it("clears dirty only when no newer edit happened during the write", () => {
    expect(shouldClearEditorDirty(4, 4)).toBe(true);
    expect(shouldClearEditorDirty(4, 5)).toBe(false);
  });
});

describe("dirty editor registry", () => {
  afterEach(() => {
    setFileEditorDirty("session", "/a.txt", false);
    setFileEditorDirty("session", "/b.txt", false);
  });

  it("includes only dirty file editors, not browsers, clean editors or terminals", () => {
    const a = tab("a", "/a.txt");
    const b = tab("b", "/b.txt");
    setFileEditorDirty("session", "/a.txt", true);
    expect(dirtyFileEditors([a, b, tab("browser"), tab("term", undefined, true)])).toEqual([a]);
  });
});
