import { afterEach, describe, expect, it } from "vitest";
import type { AppTab, Pane, Workspace } from "../../app/store";
import {
  acknowledgeFileEditorLocation,
  beginFileEditorLoad,
  beginFileEditorRename,
  beginFileEditorSave,
  dirtyFileEditors,
  endFileEditorOperation,
  finishFileEditorRename,
  isDirtyFileEditor,
  migratedFileEditorPath,
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

  it("treats slash and backslash spellings as the same dirty locator", () => {
    setFileEditorDirty("session", "C:\\work\\a.txt", true);
    expect(isDirtyFileEditor(tab("windows", "C:/work/a.txt"))).toBe(true);
    setFileEditorDirty("session", "C:/work/a.txt", false);
  });
});

describe("file editor locator migration", () => {
  it("matches only exact files and directory boundaries, including Windows separators", () => {
    expect(migratedFileEditorPath("/a/file.txt", "/a", "/b", "dir")).toBe("/b/file.txt");
    expect(migratedFileEditorPath("/a/nested/file.txt", "/a", "/b", "dir")).toBe(
      "/b/nested/file.txt",
    );
    expect(migratedFileEditorPath("/abc/file.txt", "/a", "/b", "dir")).toBeNull();
    expect(migratedFileEditorPath("C:\\a\\nested\\file.txt", "C:\\a", "C:/b", "dir")).toBe(
      "C:/b/nested/file.txt",
    );
    expect(migratedFileEditorPath("C:\\abc\\file.txt", "C:\\a", "C:/b", "dir")).toBeNull();
    expect(migratedFileEditorPath("C:\\a\\file.txt", "C:\\a", "C:/A", "dir")).toBe(
      "C:/A/file.txt",
    );
    expect(migratedFileEditorPath("C:\\A\\file.txt", "C:\\a", "C:/b", "dir")).toBeNull();
    expect(migratedFileEditorPath("~/a.txt", "~/link", "~/link2", "symlink")).toBeNull();
    expect(migratedFileEditorPath("~/link", "~/link", "~/link2", "symlink")).toBe("~/link2");
  });
});

describe("file editor operation guards", () => {
  it("serializes saves and renames in both directions", () => {
    const save = beginFileEditorSave("session", "/a.txt");
    if (typeof save === "string") throw new Error(`save blocked: ${save}`);
    expect(beginFileEditorRename("session", "/a.txt", "/b.txt", "file")).toBe("busy");
    endFileEditorOperation(save);

    const rename = beginFileEditorRename("session", "/a.txt", "/b.txt", "file");
    if (typeof rename === "string") throw new Error(`rename blocked: ${rename}`);
    expect(beginFileEditorSave("session", "/a.txt")).toBe("rename");
    expect(beginFileEditorSave("session", "/b.txt")).toBe("rename");
    finishFileEditorRename(rename, false);

    const nextSave = beginFileEditorSave("session", "/b.txt");
    if (typeof nextSave === "string") throw new Error(`next save blocked: ${nextSave}`);
    endFileEditorOperation(nextSave);
  });

  it("blocks old-path saves for an unknown outcome until explicit confirmation", () => {
    const rename = beginFileEditorRename("session", "/a.txt", "/b.txt", "file");
    if (typeof rename === "string") throw new Error(`rename blocked: ${rename}`);
    finishFileEditorRename(rename, true);
    expect(beginFileEditorSave("session", "/a.txt")).toBe("unknown");

    const targetSave = beginFileEditorSave("session", "/b.txt");
    if (typeof targetSave === "string") throw new Error(`target save blocked: ${targetSave}`);
    endFileEditorOperation(targetSave);
    acknowledgeFileEditorLocation("session", "/a.txt");
    const oldSave = beginFileEditorSave("session", "/a.txt");
    if (typeof oldSave === "string") throw new Error(`confirmed save blocked: ${oldSave}`);
    endFileEditorOperation(oldSave);
  });

  it("folds Windows locator case for dirty keys, loads, saves and migration but preserves display case", () => {
    setFileEditorDirty("session", "C:\\Work\\Draft.TXT", true, true);
    expect(isDirtyFileEditor(tab("windows-dirty", "c:/work/draft.txt"), true)).toBe(true);
    expect(
      migratedFileEditorPath("C:\\Work\\Draft.TXT", "c:/work/draft.txt", "D:/Next/Final.TxT", "file", true),
    ).toBe("D:/Next/Final.TxT");

    const save = beginFileEditorSave("session", "C:\\Work\\Draft.TXT", true);
    if (typeof save === "string") throw new Error(`Windows save blocked: ${save}`);
    expect(beginFileEditorLoad("session", "c:/work/draft.txt", true)).toBeNull();
    expect(beginFileEditorRename("session", "c:/work/draft.txt", "C:/Work/other.txt", "file", true)).toBe(
      "busy",
    );
    endFileEditorOperation(save);
    setFileEditorDirty("session", "c:/work/draft.txt", false, true);
    expect(isDirtyFileEditor(tab("windows-dirty", "C:/Work/Draft.TXT"), true)).toBe(false);
  });

  it("keeps POSIX locator case distinct", () => {
    setFileEditorDirty("session", "/tmp/Draft.TXT", true);
    expect(isDirtyFileEditor(tab("posix-dirty", "/tmp/draft.txt"))).toBe(false);
    expect(migratedFileEditorPath("/tmp/Draft.TXT", "/tmp/draft.txt", "/tmp/final.txt", "file")).toBeNull();
    const save = beginFileEditorSave("session", "/tmp/Draft.TXT");
    if (typeof save === "string") throw new Error(`POSIX save blocked: ${save}`);
    const other = beginFileEditorLoad("session", "/tmp/draft.txt");
    expect(other).not.toBeNull();
    if (other !== null) endFileEditorOperation(other);
    endFileEditorOperation(save);
    setFileEditorDirty("session", "/tmp/Draft.TXT", false);
  });
});
