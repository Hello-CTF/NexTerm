/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, StrictMode, type ReactElement } from "react";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  clickButton,
  deferred,
  flush,
  mount,
  chooseSelectOption,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  realAsk: null as null | ((message: string, options?: { title?: string; kind?: "info" | "warning" | "error" }) => Promise<boolean>),
  askChoice: vi.fn(),
  promptText: vi.fn(),
  pickLocalFile: vi.fn(),
  pickSavePath: vi.fn(),
  finishSave: vi.fn(),
  discardStaged: vi.fn(),
  list: vi.fn(),
  read: vi.fn(),
  readRange: vi.fn(),
  write: vi.fn(),
  rename: vi.fn(),
  chmod: vi.fn(),
  checksum: vi.fn(),
  mkdir: vi.fn(),
  remove: vi.fn(),
  upload: vi.fn(),
  download: vi.fn(),
  packDownload: vi.fn(),
  extract: vi.fn(),
  pickFolderUpload: vi.fn(),
  dropFolderUpload: vi.fn(),
  termWrite: vi.fn(),
  toast: vi.fn(),
  listenEvent: vi.fn(),
}));
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  mocks.realAsk = actual.ask;
  return {
    ...actual,
    ask: mocks.ask,
    askChoice: mocks.askChoice,
    promptText: mocks.promptText,
    pickLocalFile: mocks.pickLocalFile,
    pickSavePath: mocks.pickSavePath,
    pickDownloadTarget: mocks.pickSavePath,
    finishSave: mocks.finishSave,
    discardStaged: mocks.discardStaged,
  };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    fsApi: {
      list: mocks.list,
      read: mocks.read,
      readRange: mocks.readRange,
      write: mocks.write,
      rename: mocks.rename,
      chmod: mocks.chmod,
      checksum: mocks.checksum,
      mkdir: mocks.mkdir,
      delete: mocks.remove,
      upload: mocks.upload,
      download: mocks.download,
      packDownload: mocks.packDownload,
      extract: mocks.extract,
    },
    terminalApi: { write: mocks.termWrite },
    sessionApi: {},
  };
});
vi.mock("../../features/ai/runOwnership", () => ({
  aiTabBusyIds: new Set<string>(),
  aiTabRunControls: new Map<string, () => Promise<boolean>>(),
  confirmAiRunCancellation: vi.fn(),
  runningAiTabs: vi.fn(() => []),
  clearAiTabRuns: vi.fn(),
}));
vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: mocks.listenEvent,
    EVENTS: { fsProgress: "fs://progress" },
  };
});
vi.mock("../../features/files/folderUpload", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../features/files/folderUpload")>();
  return {
    ...actual,
    pickFolderUpload: mocks.pickFolderUpload,
    dropFolderUpload: mocks.dropFolderUpload,
  };
});
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, i) => ({ index: i, start: i * 30, size: 30, key: i })),
    getTotalSize: () => count * 30,
  }),
}));

interface FakeEditorState {
  doc: { toString: () => string; length: number };
  extensions: unknown[];
  selection: { from: number; to: number };
  undoDepth: number;
}
interface FakeEditorView {
  state: FakeEditorState;
  effects: unknown[];
  destroyed: boolean;
  dispatch: (change: {
    changes?: { from: number; to: number; insert: string };
    effects?: unknown;
  }) => void;
}
const editors = vi.hoisted(() => ({ views: [] as FakeEditorView[] }));

vi.mock("@codemirror/state", () => {
  class Doc {
    constructor(private readonly text: string) {}
    toString() {
      return this.text;
    }
    get length() {
      return this.text.length;
    }
  }
  class Compartment {
    of(extension: unknown) {
      return { compartment: this, extension };
    }
    reconfigure(extension: unknown) {
      return { compartment: this, extension };
    }
  }
  return {
    Compartment,
    EditorState: {
      create: ({ doc, extensions }: { doc: string; extensions: unknown[] }) => ({
        doc: new Doc(doc),
        extensions,
        selection: { from: 0, to: 0 },
        undoDepth: 0,
      }),
    },
    Prec: { highest: (extension: unknown) => extension },
  };
});
vi.mock("@codemirror/view", () => {
  class EditorView {
    static updateListener = {
      of: (listener: unknown) => ({ codeMirrorListener: listener }),
    };
    state: FakeEditorState;
    effects: unknown[] = [];
    destroyed = false;
    private readonly listeners: Array<(update: unknown) => void> = [];
    constructor(config: { state: FakeEditorState }) {
      this.state = config.state;
      const visit = (value: unknown): void => {
        if (Array.isArray(value)) {
          value.forEach(visit);
        } else if (value && typeof value === "object") {
          if ("codeMirrorListener" in value) {
            this.listeners.push((value as { codeMirrorListener: (update: unknown) => void }).codeMirrorListener);
          } else {
            Object.values(value).forEach(visit);
          }
        }
      };
      visit(this.state.extensions);
      editors.views.push(this);
    }
    dispatch({
      changes,
      effects,
    }: {
      changes?: { from: number; to: number; insert: string };
      effects?: unknown;
    }) {
      if (effects !== undefined) {
        this.effects.push(effects);
        return;
      }
      if (!changes) return;
      const current = this.state.doc.toString() as string;
      const next = `${current.slice(0, changes.from)}${changes.insert}${current.slice(changes.to)}`;
      this.state = {
        ...this.state,
        doc: { toString: () => next, length: next.length },
        undoDepth: this.state.undoDepth + 1,
      };
      this.listeners.forEach((listener) => listener({ docChanged: true, state: this.state }));
    }
    destroy() {
      this.destroyed = true;
    }
  }
  return { EditorView, keymap: { of: (bindings: unknown) => bindings } };
});
vi.mock("codemirror", () => ({ basicSetup: [] }));
vi.mock("@codemirror/commands", () => ({
  indentWithTab: {},
  redo: vi.fn(),
  undo: vi.fn(),
}));
vi.mock("@codemirror/search", () => ({ search: () => [], openSearchPanel: vi.fn() }));
vi.mock("@codemirror/language", () => ({ StreamLanguage: { define: (mode: unknown) => mode } }));
vi.mock("@codemirror/lang-sql", () => ({ sql: () => [] }));
vi.mock("@codemirror/legacy-modes/mode/shell", () => ({ shell: {} }));
vi.mock("@codemirror/legacy-modes/mode/nginx", () => ({ nginx: {} }));
vi.mock("@codemirror/legacy-modes/mode/yaml", () => ({ yaml: {} }));
vi.mock("@codemirror/legacy-modes/mode/properties", () => ({ properties: {} }));
vi.mock("@codemirror/legacy-modes/mode/javascript", () => ({ javascript: {} }));
vi.mock("@codemirror/legacy-modes/mode/python", () => ({ python: {} }));
vi.mock("../../ui/editorTheme", () => ({ nxHighlight: [] }));

import type { AppTab, Workspace } from "../../app/store";
import { openFileTab, openLogTab, useUi } from "../../app/store";
import { dialogLevelForKind } from "../../app/App";
import { registerDialogHandlers } from "../../ui/dialogs";
import { DialogHost } from "../../ui/DialogHost";
import type { FileEntryDto } from "../../ipc/types";
import {
  acknowledgeFileEditorLocation,
  beginFileEditorRename,
  beginFileEditorSave,
  endFileEditorOperation,
  finishFileEditorRename,
  isDirtyFileEditor,
  setFileEditorDirty,
} from "../../features/files/editorGuards";
import {
  CHECKSUM_ALGOS,
  DEFAULT_CHECKSUM_ALGO,
  losesOwnerAccess,
  parseOctalMode,
  validateEntryName,
} from "../../features/files/fileOps";
import { useTransferStore } from "../../features/files/transferStore";
import { FileTree } from "../../features/files/FileTree";
import { FileEditor } from "../../features/files/FileEditor";
import { LogViewer } from "../../features/files/LogViewer";

const SID = "s1";

type AskOptions = { title?: string; kind?: "info" | "warning" | "error" };

function useRealAsk(): void {
  mocks.ask.mockImplementation((message: string, options?: AskOptions) =>
    mocks.realAsk!(message, options),
  );
}

function installRealDialogs(): void {
  registerDialogHandlers({
    ask: (message, options) =>
      new Promise<boolean>((resolve) => {
        useUi.getState().openAppDialog({
          kind: "ask",
          message,
          title: options?.title,
          level: dialogLevelForKind(options?.kind),
          resolve,
        });
      }),
    confirm: (message) =>
      new Promise<boolean>((resolve) => {
        useUi.getState().openAppDialog({ kind: "confirm", message, level: "warning", resolve });
      }),
    message: (message) =>
      new Promise<void>((resolve) => {
        useUi
          .getState()
          .openAppDialog({ kind: "message", message, level: "info", resolve: () => resolve() });
      }),
    choose: mocks.askChoice,
  });
}

function entry(name: string, kind: string, extra: Partial<FileEntryDto> = {}): FileEntryDto {
  return {
    name,
    path: `~/${name}`,
    kind,
    size: kind === "dir" ? 4096 : 10,
    mode: kind === "dir" ? "755" : "644",
    owner: "u",
    group: "u",
    mtime: 1,
    symlinkTarget: null,
    ...extra,
  };
}

const entries = [
  entry("a.txt", "file"),
  entry("b.txt", "file"),
  entry("sub", "dir"),
  entry("link", "symlink", { symlinkTarget: "~/a.txt", mode: "" }),
];

function editorTab(path: string): AppTab {
  return { id: `file-${SID}-${path}`, kind: "files", title: "a.txt", sessionId: SID, path, closable: true };
}

function StoreFileEditor({ tabId }: { tabId: string }) {
  const tab = useUi((state) => {
    for (const workspace of state.workspaces) {
      for (const pane of workspace.panes) {
        const hit = pane.tabs.find((candidate) => candidate.id === tabId);
        if (hit) return hit;
      }
    }
    return undefined;
  });
  if (!tab?.sessionId || !tab.path) return null;
  return createElement(FileEditor, { sessionId: tab.sessionId, path: tab.path });
}

function mountTreeWithEditor(tabId: string): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(
        "div",
        null,
        createElement(FileTree, { sessionId: SID }),
        createElement(StoreFileEditor, { tabId }),
      ),
    ),
  );
}

function StoreLogViewer({ tabId }: { tabId: string }) {
  const tab = useUi((state) => {
    for (const workspace of state.workspaces) {
      for (const pane of workspace.panes) {
        const hit = pane.tabs.find((candidate) => candidate.id === tabId);
        if (hit) return hit;
      }
    }
    return undefined;
  });
  if (!tab?.sessionId || !tab.path) return null;
  return createElement(LogViewer, { sessionId: tab.sessionId, path: tab.path });
}

function mountTreeWithLog(tabId: string): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(
        "div",
        null,
        createElement(FileTree, { sessionId: SID }),
        createElement(StoreLogViewer, { tabId }),
      ),
    ),
  );
}

function mountTree(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(FileTree, { sessionId: SID })));
}

function rowByPath(container: HTMLElement, path: string): HTMLElement {
  const row = [...container.querySelectorAll<HTMLElement>("div[title]")].find((d) =>
    (d.getAttribute("title") ?? "").startsWith(path),
  );
  if (!row) throw new Error(`Row not found: ${path}`);
  return row;
}

function openRowMenu(container: HTMLElement, path: string): void {
  act(() => {
    rowByPath(container, path).dispatchEvent(
      new MouseEvent("contextmenu", { bubbles: true, cancelable: true }),
    );
  });
}

function menuItem(container: ParentNode, label: string): HTMLButtonElement {
  const button = [...container.querySelectorAll('[role="menuitem"]')].find((b) =>
    b.textContent?.includes(label),
  );
  if (!button) throw new Error(`Menu item not found: ${label}`);
  return button as HTMLButtonElement;
}

function clickMenuItem(container: ParentNode, label: string): void {
  click(menuItem(container, label));
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.ask.mockResolvedValue(true);
  mocks.promptText.mockResolvedValue(null);
  mocks.askChoice.mockResolvedValue(null);
  // WinRM trees start at "/" instead of "~", so both roots list the same entries.
  mocks.list.mockImplementation((_s: string, p: string) =>
    Promise.resolve(p === "~" || p === "/" ? entries.map((e) => ({ ...e })) : []),
  );
  mocks.readRange.mockRejectedValue({ code: "unsupported", message: "unsupported" });
  mocks.write.mockResolvedValue(undefined);
  mocks.mkdir.mockResolvedValue(undefined);
  mocks.rename.mockResolvedValue(undefined);
  mocks.chmod.mockResolvedValue(undefined);
  mocks.checksum.mockResolvedValue("deadbeef");
  mocks.listenEvent.mockReturnValue(Promise.resolve(() => {}));
  useUi.setState({
    pushToast: mocks.toast,
    leftOpen: true,
    leftWidth: 260,
    workspaces: [],
    activeWorkspaceId: null,
    sessions: [],
  });
  setFileEditorDirty(SID, "~/a.txt", false);
  setFileEditorDirty(SID, "~/other.txt", false);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("fileOps preflight checks (pure functions)", () => {
  it("rejects path separators and dot directory names", () => {
    expect(validateEntryName("a/b")).toMatch(/路径分隔符/);
    expect(validateEntryName("a\\b")).toMatch(/路径分隔符/);
    expect(validateEntryName("..")).toBeTruthy();
    expect(validateEntryName(".")).toBeTruthy();
    expect(validateEntryName("  ")).toMatch(/不能为空/);
    expect(validateEntryName(" x ")).toMatch(/首尾/);
    expect(validateEntryName("ok.txt")).toBeNull();
  });

  it("accepts only octal digits in mode and rejects values above 0o777", () => {
    expect(parseOctalMode("644")).toEqual({ ok: true, mode: 0o644 });
    expect(parseOctalMode("0644")).toEqual({ ok: true, mode: 0o644 });
    expect(parseOctalMode("888").ok).toBe(false);
    expect(parseOctalMode("rwx").ok).toBe(false);
    expect(parseOctalMode("7777").ok).toBe(false);
    expect(parseOctalMode("").ok).toBe(false);
  });

  it("counts only removing owner read or write access as permission narrowing", () => {
    expect(losesOwnerAccess(0o644, 0o000)).toBe(true);
    expect(losesOwnerAccess(0o644, 0o444)).toBe(true);
    expect(losesOwnerAccess(0o644, 0o755)).toBe(false);
    expect(losesOwnerAccess(0o000, 0o644)).toBe(false);
  });

  it("aligns checksum algorithms with the backend allowlist (md5/sha256, default sha256)", () => {
    expect(CHECKSUM_ALGOS.map((a) => a.key)).toEqual(["sha256", "md5"]);
    expect(DEFAULT_CHECKSUM_ALGO).toBe("sha256");
  });
});

describe("rename (FileTree)", () => {
  async function triggerRename(name: string | null): Promise<void> {
    mocks.promptText.mockResolvedValue(name);
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
    openRowMenu(mounted!.container, "~/a.txt");
    clickMenuItem(mounted!.container, "重命名");
    await flush();
  }

  beforeEach(async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("rejects path-crossing input without issuing an RPC", async () => {
    await triggerRename("../evil.txt");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("路径分隔符"));
    expect(mocks.rename).not.toHaveBeenCalled();
    await triggerRename("sub/inner.txt");
    expect(mocks.rename).not.toHaveBeenCalled();
  });

  it("rejects empty names and names with leading or trailing whitespace", async () => {
    await triggerRename("   ");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("不能为空"));
    await triggerRename(" b.txt ");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("首尾"));
    expect(mocks.rename).not.toHaveBeenCalled();
  });

  it("renames within the same directory and refreshes the list", async () => {
    await triggerRename("c.txt");
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/c.txt");
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已重命名"));
    await waitFor(() => expect(mocks.list.mock.calls.length).toBeGreaterThan(1));
  });

  it("requires explicit confirmation before overwriting an existing name and aborts on cancel", async () => {
    mocks.ask.mockResolvedValueOnce(false);
    await triggerRename("b.txt");
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("已存在"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("继续将替换原有内容，且无法恢复"),
      expect.anything(),
    );
    expect(mocks.rename).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    await triggerRename("b.txt");
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/b.txt");
  });

  function seedKind(kind: string): void {
    act(() => {
      useUi.setState({
        sessions: [
          { id: SID, assetId: null, name: "h", kind, status: "connected", tabs: [], createdAt: 1 },
        ],
      });
    });
  }

  it("winrm: requires confirmation when renaming to an existing name that differs only by case", async () => {
    seedKind("winrm");
    mocks.ask.mockResolvedValueOnce(false);
    await triggerRename("B.TXT");
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("已存在"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.rename).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    await triggerRename("B.TXT");
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/B.TXT");
  });

  it("ssh: case-only differences do not conflict, so rename proceeds without confirmation", async () => {
    seedKind("ssh");
    await triggerRename("B.TXT");
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/B.TXT");
  });

  it("symlinks: explains that only the link itself is renamed before proceeding", async () => {
    mocks.promptText.mockResolvedValue("link2");
    openRowMenu(mounted!.container, "~/link");
    clickMenuItem(mounted!.container, "重命名");
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("符号链接"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/link", "~/link2");
  });

  it("migrates a dirty open editor without changing its identity or dirty state", async () => {
    const tab = editorTab("~/a.txt");
    act(() => {
      useUi.setState({
        workspaces: [
          {
            id: "ws",
            kind: "session",
            title: "w",
            panes: [{ id: "p", tabs: [tab], activeTabId: tab.id }],
            activePaneId: "p",
            closable: true,
          },
        ],
        activeWorkspaceId: "ws",
      });
    });
    setFileEditorDirty(SID, "~/a.txt", true);
    await triggerRename("c.txt");
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/c.txt");
    const migrated = useUi.getState().workspaces[0]?.panes[0]?.tabs[0] ?? tab;
    expect(migrated).toMatchObject({ id: tab.id, path: "~/c.txt", title: "c.txt" });
    expect(isDirtyFileEditor(migrated)).toBe(true);
    expect(isDirtyFileEditor(tab)).toBe(false);
    setFileEditorDirty(SID, "~/c.txt", false);
  });

  it("migrates a clean open editor instead of rejecting the rename", async () => {
    const tab = editorTab("~/a.txt");
    act(() => {
      useUi.setState({
        workspaces: [
          {
            id: "ws",
            kind: "session",
            title: "w",
            panes: [{ id: "p", tabs: [tab], activeTabId: tab.id }],
            activePaneId: "p",
            closable: true,
          },
        ],
        activeWorkspaceId: "ws",
      });
    });
    await triggerRename("c.txt");
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/c.txt");
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs[0]).toMatchObject({
      id: tab.id,
      path: "~/c.txt",
      title: "c.txt",
    });
  });

  it("presents backend rejection verbatim and allows retrying with the same input", async () => {
    mocks.rename.mockRejectedValueOnce({ code: "internal", message: "SFTP：目标已存在" });
    await triggerRename("c.txt");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("SFTP：目标已存在"));
    await triggerRename("c.txt");
    expect(mocks.rename).toHaveBeenCalledTimes(2);
  });

  it("blocks a second trigger while an operation is in progress (double-submit)", async () => {
    const pending = deferred<void>();
    mocks.rename.mockReturnValueOnce(pending.promise);
    await triggerRename("c.txt");
    expect(mocks.rename).toHaveBeenCalledTimes(1);

    openRowMenu(mounted!.container, "~/b.txt");
    const renameItem = menuItem(mounted!.container, "重命名");
    expect(renameItem.disabled).toBe(true);
    click(renameItem);
    clickMenuItem(mounted!.container, "权限…");
    expect(mocks.chmod).not.toHaveBeenCalled();
    expect(mocks.promptText).not.toHaveBeenCalledWith(
      expect.stringContaining("权限"),
      expect.anything(),
    );

    pending.resolve();
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已重命名"));
  });

  it("ignores completion callbacks that arrive after unmount (stale completion)", async () => {
    const pending = deferred<void>();
    mocks.rename.mockReturnValueOnce(pending.promise);
    await triggerRename("c.txt");
    mounted!.unmount();
    mounted = undefined;
    pending.resolve();
    await flush();
    expect(mocks.toast).not.toHaveBeenCalledWith("success", expect.stringContaining("已重命名"));
    expect(mocks.toast).not.toHaveBeenCalledWith("error", expect.anything());
  });
});

describe("R2 rename editor locator migration", () => {
  beforeEach(() => {
    editors.views = [];
    mocks.read.mockResolvedValue({
      path: "~/a.txt",
      size: 8,
      contentBase64: btoa("original"),
    });
  });

  afterEach(() => {
    mocks.askChoice.mockReset();
    acknowledgeFileEditorLocation(SID, "~/a.txt");
    acknowledgeFileEditorLocation(SID, "/a");
    setFileEditorDirty(SID, "~/a.txt", false);
    setFileEditorDirty(SID, "~/c.txt", false);
    setFileEditorDirty(SID, "/a/file.txt", false);
  });

  function seedWorkspaces(workspaces: Workspace[], activeWorkspaceId = workspaces[0]?.id): void {
    act(() => {
      useUi.setState({ workspaces, activeWorkspaceId: activeWorkspaceId ?? null });
    });
  }

  function seedEditor(tab: AppTab): void {
    seedWorkspaces([
      {
        id: "ws",
        kind: "session",
        title: "w",
        sessionId: SID,
        panes: [{ id: "p", tabs: [tab], activeTabId: tab.id }],
        activePaneId: "p",
        closable: true,
      },
    ]);
  }

  function storedTab(id: string): AppTab {
    for (const workspace of useUi.getState().workspaces) {
      for (const pane of workspace.panes) {
        const tab = pane.tabs.find((candidate) => candidate.id === id);
        if (tab) return tab;
      }
    }
    throw new Error(`Tab not found: ${id}`);
  }

  async function triggerRename(name: string, path = "~/a.txt"): Promise<void> {
    mocks.promptText.mockResolvedValue(name);
    openRowMenu(mounted!.container, path);
    clickMenuItem(mounted!.container, "重命名");
    await flush();
  }

  function saveButton(): HTMLButtonElement {
    return mounted!.container.querySelector<HTMLButtonElement>('button[title="保存"]')!;
  }

  it("blocks saving during rename, then keeps the same document and saves only to the new locator", async () => {
    const tab = { ...editorTab("~/a.txt"), id: "stable-editor" };
    seedEditor(tab);
    mounted = mountTreeWithEditor(tab.id);
    await waitFor(() => expect(editors.views).toHaveLength(1));
    const view = editors.views[0];
    act(() => {
      view.dispatch({ changes: { from: 0, to: 8, insert: "changed!" } });
    });
    view.state.selection = { from: 2, to: 5 };
    const undoDepth = view.state.undoDepth;
    const pending = deferred<void>();
    mocks.rename.mockReturnValueOnce(pending.promise);

    await triggerRename("c.py");
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/c.py");
    click(saveButton());
    await flush();
    expect(mocks.write).not.toHaveBeenCalled();

    pending.resolve();
    await waitFor(() => {
      const migrated = useUi.getState().workspaces[0]?.panes[0]?.tabs[0];
      expect(migrated).toMatchObject({ id: tab.id, path: "~/c.py", title: "c.py" });
    });
    expect(editors.views).toHaveLength(1);
    expect(view.destroyed).toBe(false);
    expect(view.state.doc.toString()).toBe("changed!");
    expect(view.state.selection).toEqual({ from: 2, to: 5 });
    expect(view.state.undoDepth).toBe(undoDepth);
    expect(view.effects).toHaveLength(1);
    expect(mocks.read).toHaveBeenCalledTimes(1);
    expect(isDirtyFileEditor({ ...tab, path: "~/c.py", title: "c.py" })).toBe(true);

    click(saveButton());
    await waitFor(() => expect(mocks.write).toHaveBeenCalledTimes(1));
    expect(mocks.write).toHaveBeenCalledWith(SID, "~/c.py", expect.any(String));
    expect(mocks.write).not.toHaveBeenCalledWith(SID, "~/a.txt", expect.any(String));
  });

  it("migrates a clean editor without rereading, then saves later edits to the new locator", async () => {
    const tab = { ...editorTab("~/a.txt"), id: "clean-editor" };
    seedEditor(tab);
    mounted = mountTreeWithEditor(tab.id);
    await waitFor(() => expect(editors.views).toHaveLength(1));
    const view = editors.views[0];

    await triggerRename("c.txt");
    await waitFor(() => expect(storedTab(tab.id).path).toBe("~/c.txt"));
    expect(editors.views).toHaveLength(1);
    expect(view.destroyed).toBe(false);
    expect(mocks.read).toHaveBeenCalledTimes(1);
    expect(isDirtyFileEditor(storedTab(tab.id))).toBe(false);

    act(() => {
      view.dispatch({ changes: { from: 0, to: 8, insert: "after" } });
    });
    click(saveButton());
    await waitFor(() => expect(mocks.write).toHaveBeenCalledTimes(1));
    expect(mocks.write).toHaveBeenCalledWith(SID, "~/c.txt", expect.any(String));
  });

  it("rejects rename while a save is in flight and allows it after that save settles", async () => {
    const tab = { ...editorTab("~/a.txt"), id: "saving-editor" };
    seedEditor(tab);
    mounted = mountTreeWithEditor(tab.id);
    await waitFor(() => expect(editors.views).toHaveLength(1));
    const view = editors.views[0];
    act(() => {
      view.dispatch({ changes: { from: 0, to: 8, insert: "changed" } });
    });
    const pending = deferred<void>();
    mocks.write.mockReturnValueOnce(pending.promise);
    click(saveButton());
    await waitFor(() => expect(mocks.write).toHaveBeenCalledWith(SID, "~/a.txt", expect.any(String)));

    await triggerRename("c.txt");
    expect(mocks.rename).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("正在保存"));

    pending.resolve();
    await flush();
    await triggerRename("c.txt");
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/c.txt");
    expect(storedTab(tab.id).path).toBe("~/c.txt");
    expect(mocks.write).toHaveBeenCalledTimes(1);
  });

  it("rejects rename while the initial read is in flight instead of racing the locator", async () => {
    const tab = { ...editorTab("~/a.txt"), id: "loading-editor" };
    seedEditor(tab);
    const pending = deferred<{ path: string; offset: number; size: number; contentBase64: string; truncated: boolean }>();
    mocks.readRange.mockReturnValueOnce(pending.promise);
    mounted = mountTreeWithEditor(tab.id);
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());

    await triggerRename("c.txt");
    expect(mocks.rename).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("读取"));

    pending.resolve({ path: "~/a.txt", offset: 0, size: 8, contentBase64: "", truncated: false });
    await waitFor(() => expect(editors.views).toHaveLength(1));
    await triggerRename("c.txt");
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/c.txt");
    expect(storedTab(tab.id).path).toBe("~/c.txt");
  });

  it("activates and rejects an already-open target editor instead of merging drafts", async () => {
    const source = { ...editorTab("~/a.txt"), id: "source" };
    const target = { ...editorTab("~/b.txt"), id: "target", title: "b.txt" };
    seedWorkspaces([
      {
        id: "ws",
        kind: "session",
        title: "w",
        sessionId: SID,
        panes: [
          { id: "p1", tabs: [source], activeTabId: source.id },
          { id: "p2", tabs: [target], activeTabId: target.id },
        ],
        activePaneId: "p1",
        closable: true,
      },
    ]);
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());

    await triggerRename("b.txt");
    expect(mocks.rename).not.toHaveBeenCalled();
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(storedTab(source.id).path).toBe("~/a.txt");
    expect(storedTab(target.id).path).toBe("~/b.txt");
    expect(useUi.getState().workspaces[0]).toMatchObject({ activePaneId: "p2" });
    expect(useUi.getState().workspaces[0]?.panes[1]?.activeTabId).toBe(target.id);
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("目标"));
  });

  it("keeps source locators and dirty state when overwrite confirmation is canceled", async () => {
    const tab = { ...editorTab("~/a.txt"), id: "overwrite-source" };
    seedEditor(tab);
    setFileEditorDirty(SID, "~/a.txt", true);
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
    mocks.ask.mockResolvedValueOnce(false);

    await triggerRename("b.txt");
    expect(mocks.rename).not.toHaveBeenCalled();
    expect(storedTab(tab.id).path).toBe("~/a.txt");
    expect(isDirtyFileEditor(storedTab(tab.id))).toBe(true);
    setFileEditorDirty(SID, "~/a.txt", false);
  });

  it("atomically migrates matching editors across panes and workspaces with directory boundaries", async () => {
    const first = { ...editorTab("/a/one.txt"), id: "first", title: "one.txt" };
    const second = { ...editorTab("/a/nested/two.py"), id: "second", title: "two.py" };
    const neighbor = { ...editorTab("/abc/no.txt"), id: "neighbor", title: "no.txt" };
    const log: AppTab = {
      id: "log-first",
      kind: "log",
      title: "one.txt",
      sessionId: SID,
      path: "/a/one.txt",
      closable: true,
    };
    const otherSession = { ...editorTab("/a/other.txt"), id: "other-session", sessionId: "s2" };
    seedWorkspaces(
      [
        {
          id: "ws1",
          kind: "session",
          title: "w1",
          sessionId: SID,
          panes: [
            { id: "p1", tabs: [first], activeTabId: first.id },
            { id: "p2", tabs: [second], activeTabId: second.id },
          ],
          activePaneId: "p2",
          closable: true,
        },
        {
          id: "ws2",
          kind: "session",
          title: "w2",
          sessionId: SID,
          panes: [{ id: "p3", tabs: [neighbor, log], activeTabId: log.id }],
          activePaneId: "p3",
          closable: true,
        },
        {
          id: "ws3",
          kind: "session",
          title: "w3",
          sessionId: "s2",
          panes: [{ id: "p4", tabs: [otherSession], activeTabId: otherSession.id }],
          activePaneId: "p4",
          closable: true,
        },
      ],
      "ws2",
    );
    setFileEditorDirty(SID, "/a/one.txt", true);
    setFileEditorDirty(SID, "/a/nested/two.py", true);
    mocks.list.mockImplementation((_s: string, path: string) =>
      Promise.resolve(
        path === "~"
          ? [entry("a", "dir", { path: "/a" }), entry("abc", "dir", { path: "/abc" })]
          : [],
      ),
    );
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "/a")).toBeTruthy());
    let workspaceUpdates = 0;
    const unsubscribe = useUi.subscribe((state, previous) => {
      if (state.workspaces !== previous.workspaces) workspaceUpdates += 1;
    });

    const pending = deferred<void>();
    mocks.rename.mockReturnValueOnce(pending.promise);
    await triggerRename("renamed", "/a");
    act(() => useUi.getState().setActiveWorkspace("ws1"));
    pending.resolve();
    await flush();
    unsubscribe();
    expect(mocks.rename).toHaveBeenCalledWith(SID, "/a", "/renamed");
    expect(workspaceUpdates).toBe(1);
    expect(storedTab(first.id)).toMatchObject({ path: "/renamed/one.txt", title: "one.txt" });
    expect(storedTab(second.id)).toMatchObject({ path: "/renamed/nested/two.py", title: "two.py" });
    expect(storedTab(neighbor.id).path).toBe("/abc/no.txt");
    expect(storedTab(log.id)).toMatchObject({ path: "/renamed/one.txt", title: "one.txt" });
    expect(storedTab(otherSession.id).path).toBe("/a/other.txt");
    expect(isDirtyFileEditor(storedTab(first.id))).toBe(true);
    expect(isDirtyFileEditor(storedTab(second.id))).toBe(true);
    expect(useUi.getState().activeWorkspaceId).toBe("ws1");
    expect(useUi.getState().workspaces[0]?.activePaneId).toBe("p2");
    expect(useUi.getState().workspaces[1]?.panes[0]?.activeTabId).toBe(log.id);
    setFileEditorDirty(SID, "/renamed/one.txt", false);
    setFileEditorDirty(SID, "/renamed/nested/two.py", false);
  });

  it("migrates only a symlink's own locator and never follows its target", async () => {
    const link = { ...editorTab("~/link"), id: "link-editor", title: "link" };
    const target = { ...editorTab("~/a.txt"), id: "target-editor" };
    seedWorkspaces([
      {
        id: "ws",
        kind: "session",
        title: "w",
        sessionId: SID,
        panes: [{ id: "p", tabs: [link, target], activeTabId: link.id }],
        activePaneId: "p",
        closable: true,
      },
    ]);
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/link")).toBeTruthy());

    await triggerRename("link2", "~/link");
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/link", "~/link2");
    expect(storedTab(link.id)).toMatchObject({ path: "~/link2", title: "link2" });
    expect(storedTab(target.id).path).toBe("~/a.txt");
  });

  it("commits locator migration when the remote rename settles after unmount", async () => {
    const tab = { ...editorTab("~/a.txt"), id: "unmounted-editor" };
    seedEditor(tab);
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
    const pending = deferred<void>();
    mocks.rename.mockReturnValueOnce(pending.promise);
    await triggerRename("c.txt");
    mounted.unmount();
    mounted = undefined;

    pending.resolve();
    await flush();
    expect(storedTab(tab.id)).toMatchObject({ path: "~/c.txt", title: "c.txt" });
    expect(mocks.toast).not.toHaveBeenCalledWith("success", expect.stringContaining("已重命名"));
    expect(mocks.toast).not.toHaveBeenCalledWith("error", expect.anything());
  });

  it("keeps the original locator and document after a definite remote rejection", async () => {
    const tab = { ...editorTab("~/a.txt"), id: "rejected-editor" };
    seedEditor(tab);
    mounted = mountTreeWithEditor(tab.id);
    await waitFor(() => expect(editors.views).toHaveLength(1));
    const view = editors.views[0];
    act(() => {
      view.dispatch({ changes: { from: 0, to: 8, insert: "changed" } });
    });
    mocks.rename.mockRejectedValueOnce({ code: "forbidden", message: "permission denied" });

    await triggerRename("c.txt");
    expect(storedTab(tab.id)).toMatchObject({ id: tab.id, path: "~/a.txt" });
    expect(view.state.doc.toString()).toBe("changed");
    expect(isDirtyFileEditor(storedTab(tab.id))).toBe(true);
    expect(mocks.rename).toHaveBeenCalledTimes(1);

    click(saveButton());
    await waitFor(() => expect(mocks.write).toHaveBeenCalledTimes(1));
    expect(mocks.write).toHaveBeenCalledWith(SID, "~/a.txt", expect.any(String));
  });

  it("keeps the original locator and blocks old-path saves when the rename outcome is unknown", async () => {
    const tab = { ...editorTab("~/a.txt"), id: "unknown-editor" };
    seedEditor(tab);
    mounted = mountTreeWithEditor(tab.id);
    await waitFor(() => expect(editors.views).toHaveLength(1));
    const view = editors.views[0];
    act(() => {
      view.dispatch({ changes: { from: 0, to: 8, insert: "changed" } });
    });
    mocks.rename.mockRejectedValueOnce({ code: "disconnected", message: "connection lost" });

    await triggerRename("c.txt");
    expect(storedTab(tab.id).path).toBe("~/a.txt");
    expect(view.state.doc.toString()).toBe("changed");
    expect(isDirtyFileEditor(storedTab(tab.id))).toBe(true);
    mocks.askChoice.mockResolvedValueOnce(null);
    click(saveButton());
    await flush();

    expect(mocks.write).not.toHaveBeenCalled();
    expect(mocks.rename).toHaveBeenCalledTimes(1);
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("结果未知"));
    expect(mocks.askChoice).toHaveBeenCalledWith(
      expect.stringContaining("确认旧路径和新路径"),
      expect.objectContaining({ title: "确认文件位置" }),
    );
    expect(mocks.askChoice.mock.calls[0]?.[1]?.choices.map((choice: { key: string }) => choice.key)).toEqual([
      "not-renamed",
      "renamed",
    ]);
    expect(storedTab(tab.id).path).toBe("~/a.txt");
    expect(view.state.doc.toString()).toBe("changed");
    acknowledgeFileEditorLocation(SID, "~/a.txt");
    setFileEditorDirty(SID, "~/a.txt", false);
  });

  async function mountDirtyEditor(id: string, path = "~/a.txt") {
    const tab = { ...editorTab(path), id };
    seedEditor(tab);
    mounted = mountTreeWithEditor(tab.id);
    await waitFor(() => expect(editors.views).toHaveLength(1));
    const view = editors.views[0];
    act(() => {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: "changed" } });
    });
    return { tab, view };
  }

  for (const [label, error] of [
    ["timeout", { code: "timeout", message: "deadline exceeded" }],
    ["EOF", new Error("EOF")],
    ["HTTP fetch", new TypeError("fetch failed")],
    ["response read", { code: "internal", message: "读取响应失败" }],
    ["SFTP stream loss", { code: "sftp", message: "SFTP rename: unexpected EOF" }],
  ] as const) {
    it(`keeps ${label} unknown and never writes the old locator before reconciliation`, async () => {
      const { tab, view } = await mountDirtyEditor(`unknown-${label}`);
      mocks.rename.mockRejectedValueOnce(error);
      await triggerRename("c.txt");
      mocks.askChoice.mockResolvedValueOnce(null);
      click(saveButton());
      await flush();

      expect(storedTab(tab.id)).toMatchObject({ path: "~/a.txt" });
      expect(view.state.doc.toString()).toBe("changed");
      expect(mocks.write).not.toHaveBeenCalled();
      expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("结果未知"));
      expect(mocks.askChoice).toHaveBeenCalled();
      acknowledgeFileEditorLocation(SID, "~/a.txt");
      setFileEditorDirty(SID, "~/a.txt", false);
    });
  }

  it("migrates atomically after confirming a rename applied by the server despite a lost response", async () => {
    const { tab, view } = await mountDirtyEditor("response-lost-applied");
    mocks.rename.mockRejectedValueOnce({ code: "internal", message: "读取响应失败" });
    await triggerRename("c.txt");
    expect(storedTab(tab.id).path).toBe("~/a.txt");

    mocks.askChoice.mockResolvedValueOnce("renamed");
    click(saveButton());
    await waitFor(() => expect(storedTab(tab.id).path).toBe("~/c.txt"));
    expect(view.state.doc.toString()).toBe("changed");
    expect(mocks.write).not.toHaveBeenCalled();
    expect(mocks.rename).toHaveBeenCalledTimes(1);

    click(saveButton());
    await waitFor(() => expect(mocks.write).toHaveBeenCalledTimes(1));
    expect(mocks.write).toHaveBeenCalledWith(SID, "~/c.txt", expect.any(String));
    expect(mocks.write).not.toHaveBeenCalledWith(SID, "~/a.txt", expect.any(String));
    setFileEditorDirty(SID, "~/c.txt", false);
  });

  it("clears the whole directory unknown record after confirming the rename did not happen", async () => {
    mocks.list.mockResolvedValue([entry("a", "dir", { path: "/a" })]);
    const { tab } = await mountDirtyEditor("response-lost-directory", "/a/file.txt");
    mocks.rename.mockRejectedValueOnce({ code: "disconnected", message: "connection lost" });
    await triggerRename("b", "/a");
    expect(storedTab(tab.id).path).toBe("/a/file.txt");

    mocks.askChoice.mockResolvedValueOnce("not-renamed");
    click(saveButton());
    await flush();
    expect(mocks.write).not.toHaveBeenCalled();
    expect(storedTab(tab.id).path).toBe("/a/file.txt");

    click(saveButton());
    await waitFor(() => expect(mocks.write).toHaveBeenCalledWith(SID, "/a/file.txt", expect.any(String)));
    await triggerRename("c", "/a");
    expect(mocks.rename).toHaveBeenCalledTimes(2);
    expect(mocks.rename).toHaveBeenLastCalledWith(SID, "/a", "/c");
  });

  it("rechecks an opened target before migrating an unknown rename", async () => {
    const { tab } = await mountDirtyEditor("unknown-target-race");
    mocks.rename.mockRejectedValueOnce({ code: "disconnected", message: "connection lost" });
    await triggerRename("c.txt");
    const source = storedTab(tab.id);
    const target = { ...editorTab("~/c.txt"), id: "unknown-target-opened", title: "c.txt" };
    seedWorkspaces([
      {
        id: "ws",
        kind: "session",
        title: "w",
        sessionId: SID,
        panes: [
          { id: "p1", tabs: [source], activeTabId: source.id },
          { id: "p2", tabs: [target], activeTabId: target.id },
        ],
        activePaneId: "p1",
        closable: true,
      },
    ]);
    setFileEditorDirty(SID, "~/c.txt", true);
    mocks.askChoice.mockResolvedValueOnce("renamed").mockResolvedValueOnce(null);

    click(saveButton());
    await flush();

    expect(storedTab(source.id).path).toBe("~/a.txt");
    expect(storedTab(target.id).path).toBe("~/c.txt");
    expect(isDirtyFileEditor(storedTab(source.id))).toBe(true);
    expect(isDirtyFileEditor(storedTab(target.id))).toBe(true);
    expect(useUi.getState().workspaces[0]?.activePaneId).toBe("p2");
    expect(mocks.write).not.toHaveBeenCalled();

    const confirmations = mocks.askChoice.mock.calls.length;
    click(saveButton());
    await flush();
    expect(mocks.askChoice.mock.calls.length).toBe(confirmations + 1);
    expect(mocks.write).not.toHaveBeenCalled();
  });

  it("offers location confirmation without an editor and refreshes after not-renamed", async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
    mocks.rename.mockRejectedValueOnce({ code: "disconnected", message: "connection lost" });
    mocks.askChoice.mockResolvedValueOnce(null);

    await triggerRename("c.txt");
    expect(mocks.askChoice).toHaveBeenCalledTimes(1);

    mocks.askChoice.mockResolvedValueOnce("not-renamed");
    await triggerRename("c.txt");
    expect(mocks.askChoice).toHaveBeenCalledTimes(2);
    await waitFor(() => expect(mocks.list.mock.calls.length).toBeGreaterThan(1));

    await triggerRename("c.txt");
    expect(mocks.rename).toHaveBeenCalledTimes(2);
  });

  it("confirms renamed and invalidates caches after the initiating panel unmounts", async () => {
    const tab = { ...editorTab("~/a.txt"), id: "unknown-clean-unmounted" };
    seedEditor(tab);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const invalidate = vi.spyOn(client, "invalidateQueries");
    mounted = mount(
      createElement(QueryClientProvider, { client }, createElement(FileTree, { sessionId: SID })),
    );
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
    const pending = deferred<void>();
    mocks.rename.mockReturnValueOnce(pending.promise);
    mocks.askChoice.mockResolvedValueOnce("renamed");
    await triggerRename("c.txt");
    mounted.unmount();
    mounted = undefined;

    pending.reject({ code: "disconnected", message: "connection lost" });
    await flush();

    expect(mocks.askChoice).toHaveBeenCalledOnce();
    expect(storedTab(tab.id).path).toBe("~/c.txt");
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["fs", SID] });
    expect(mocks.toast).not.toHaveBeenCalledWith("error", expect.anything());
  });

  it("reconciles the original unknown operation from its target path for a clean editor", async () => {
    const tab = { ...editorTab("~/a.txt"), id: "unknown-target-entry-clean" };
    seedEditor(tab);
    let visible = [entry("a.txt", "file")];
    mocks.list.mockImplementation(() => Promise.resolve(visible));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const invalidate = vi.spyOn(client, "invalidateQueries");
    mounted = mount(
      createElement(QueryClientProvider, { client }, createElement(FileTree, { sessionId: SID })),
    );
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
    mocks.rename.mockRejectedValueOnce({ code: "disconnected", message: "connection lost" });
    mocks.askChoice.mockResolvedValueOnce(null);
    await triggerRename("c.txt");
    expect(beginFileEditorSave(SID, "~/a.txt")).toBe("unknown");

    visible = [entry("c.txt", "file")];
    click(mounted.container.querySelector<HTMLButtonElement>('button[title="刷新"]')!);
    await waitFor(() => expect(rowByPath(mounted!.container, "~/c.txt")).toBeTruthy());
    invalidate.mockClear();
    mocks.askChoice.mockResolvedValueOnce("renamed");

    await triggerRename("d.txt", "~/c.txt");

    expect(mocks.askChoice).toHaveBeenCalledTimes(2);
    expect(storedTab(tab.id)).toMatchObject({ path: "~/c.txt", title: "c.txt" });
    expect(mocks.rename).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["fs", SID] });
  });

  it("reconciles not-renamed from the target path without an editor and invalidates caches", async () => {
    let visible = [entry("a.txt", "file")];
    mocks.list.mockImplementation(() => Promise.resolve(visible));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const invalidate = vi.spyOn(client, "invalidateQueries");
    mounted = mount(
      createElement(QueryClientProvider, { client }, createElement(FileTree, { sessionId: SID })),
    );
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
    mocks.rename.mockRejectedValueOnce({ code: "disconnected", message: "connection lost" });
    mocks.askChoice.mockResolvedValueOnce(null);
    await triggerRename("c.txt");

    visible = [entry("c.txt", "file")];
    click(mounted.container.querySelector<HTMLButtonElement>('button[title="刷新"]')!);
    await waitFor(() => expect(rowByPath(mounted!.container, "~/c.txt")).toBeTruthy());
    invalidate.mockClear();
    mocks.askChoice.mockResolvedValueOnce("not-renamed");

    await triggerRename("d.txt", "~/c.txt");

    expect(mocks.askChoice).toHaveBeenCalledTimes(2);
    expect(mocks.rename).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["fs", SID] });
    const save = beginFileEditorSave(SID, "~/a.txt");
    expect(typeof save).toBe("number");
    if (typeof save === "number") endFileEditorOperation(save);
  });

  it("keeps a real WinRM case-folded target in overwrite preflight", async () => {
    useUi.setState({
      sessions: [
        { id: SID, assetId: null, name: "h", kind: "winrm", status: "connected", tabs: [], createdAt: 1 },
      ],
    });
    mocks.list.mockResolvedValue([entry("a.txt", "file"), entry("A.TXT", "file")]);
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
    mocks.ask.mockResolvedValueOnce(false);

    await triggerRename("A.TXT");

    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("已存在"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.rename).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    await triggerRename("A.TXT");
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/A.TXT");
  });

  it("allows a WinRM case-only rename while its source editor is open", async () => {
    const source = { ...editorTab("~/a.txt"), id: "windows-case-source" };
    seedEditor(source);
    useUi.setState({
      sessions: [
        { id: SID, assetId: null, name: "h", kind: "winrm", status: "connected", tabs: [], createdAt: 1 },
      ],
    });
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());

    await triggerRename("A.TXT");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/A.TXT");
    expect(storedTab(source.id)).toMatchObject({ id: source.id, path: "~/A.TXT", title: "A.TXT" });
  });

  it("does not treat the source entry as an overwrite conflict for a WinRM case-only rename", async () => {
    useUi.setState({
      sessions: [
        { id: SID, assetId: null, name: "h", kind: "winrm", status: "connected", tabs: [], createdAt: 1 },
      ],
    });
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());

    await triggerRename("A.TXT");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/A.TXT");
  });

  it("rejects an open WinRM target that differs only by case", async () => {
    const target = { ...editorTab("~/B.TXT"), id: "windows-target", title: "B.TXT" };
    seedEditor(target);
    useUi.setState({
      sessions: [
        { id: SID, assetId: null, name: "h", kind: "winrm", status: "connected", tabs: [], createdAt: 1 },
      ],
    });
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());

    await triggerRename("b.txt");
    expect(mocks.rename).not.toHaveBeenCalled();
    expect(storedTab(target.id).path).toBe("~/B.TXT");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("目标"));
  });

  it("does not open source, target or log locators while their rename is pending", async () => {
    seedWorkspaces([
      {
        id: "ws",
        kind: "session",
        title: "w",
        sessionId: SID,
        panes: [{ id: "p", tabs: [], activeTabId: null }],
        activePaneId: "p",
        closable: true,
      },
    ]);
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
    const pending = deferred<void>();
    mocks.rename.mockReturnValueOnce(pending.promise);
    await triggerRename("c.txt");

    try {
      act(() => {
        openFileTab(SID, "~/a.txt");
        openFileTab(SID, "~/c.txt");
        openLogTab(SID, "~/a.txt");
        openLogTab(SID, "~/c.txt");
      });
      expect(useUi.getState().workspaces[0]?.panes[0]?.tabs).toHaveLength(0);
      expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("重命名进行中"));
    } finally {
      pending.resolve();
      await flush();
    }
  });

  it("closes an editor instead of leaving an empty tab when its load guard is rejected", async () => {
    const onClose = vi.fn();
    const rename = beginFileEditorRename(SID, "~/a.txt", "~/c.txt", "file");
    if (typeof rename === "string") throw new Error(`rename blocked: ${rename}`);
    try {
      mounted = mount(
        createElement(
          QueryClientProvider,
          { client: new QueryClient({ defaultOptions: { queries: { retry: false } } }) },
          createElement(FileEditor, { sessionId: SID, path: "~/c.txt", onClose }),
        ),
      );
      await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
      expect(mocks.read).not.toHaveBeenCalled();
    } finally {
      finishFileEditorRename(rename, false);
    }
  });

  it("reloads LogViewer from the migrated locator after rename", async () => {
    const log: AppTab = {
      id: "log-editor",
      kind: "log",
      title: "a.txt",
      sessionId: SID,
      path: "~/a.txt",
      closable: true,
    };
    seedEditor(log);
    mocks.readRange.mockResolvedValue({
      path: "~/a.txt",
      offset: 0,
      size: 8,
      contentBase64: btoa("original"),
      truncated: false,
    });
    mounted = mountTreeWithLog(log.id);
    await waitFor(() => expect(mocks.readRange).toHaveBeenCalledWith(SID, "~/a.txt", 0, expect.any(Number)));
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());

    await triggerRename("c.txt");
    await waitFor(() => expect(storedTab(log.id).path).toBe("~/c.txt"));
    await waitFor(() => expect(mocks.readRange).toHaveBeenCalledWith(SID, "~/c.txt", 0, expect.any(Number)));
  });
});

describe("chmod permission changes (FileTree)", () => {
  async function triggerChmod(path: string, mode: string | null): Promise<void> {
    mocks.promptText.mockResolvedValue(mode);
    openRowMenu(mounted!.container, path);
    clickMenuItem(mounted!.container, "权限…");
    await flush();
  }

  beforeEach(async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("rejects non-octal and out-of-range input before issuing an RPC", async () => {
    await triggerChmod("~/a.txt", "888");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("八进制"));
    await triggerChmod("~/a.txt", "7777");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("范围"));
    await triggerChmod("~/a.txt", "rwx");
    expect(mocks.chmod).not.toHaveBeenCalled();
  });

  it("accepts octal input with a leading zero and sends a numeric mode", async () => {
    await triggerChmod("~/a.txt", "0755");
    expect(mocks.chmod).toHaveBeenCalledWith(SID, "~/a.txt", 0o755);
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("权限已更新"));
  });

  it("requires danger confirmation before removing owner read or write access and aborts on cancel", async () => {
    mocks.ask.mockResolvedValueOnce(false);
    await triggerChmod("~/a.txt", "000");
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("所有者"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.chmod).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    await triggerChmod("~/a.txt", "000");
    expect(mocks.chmod).toHaveBeenCalledWith(SID, "~/a.txt", 0);
  });

  it("symlinks: warns that chmod changes the target", async () => {
    await triggerChmod("~/link", "600");
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("符号链接"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.chmod).toHaveBeenCalledWith(SID, "~/link", 0o600);
  });
});

describe("checksum (FileTree)", () => {
  beforeEach(async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("offers only md5/sha256 in the algorithm picker and presents the result in a copyable input", async () => {
    mocks.askChoice.mockResolvedValue("md5");
    mocks.checksum.mockResolvedValue("md5-hash-value");
    openRowMenu(mounted!.container, "~/a.txt");
    clickMenuItem(mounted!.container, "校验值…");
    await flush();

    const choices = mocks.askChoice.mock.calls[0]?.[1]?.choices ?? [];
    expect(choices.map((c: { key: string }) => c.key)).toEqual(["sha256", "md5"]);
    expect(choices.find((c: { key: string }) => c.key === "sha256")?.primary).toBe(true);
    expect(mocks.checksum).toHaveBeenCalledWith(SID, "~/a.txt", "md5");
    expect(mocks.promptText).toHaveBeenCalledWith(
      expect.stringContaining("MD5"),
      "md5-hash-value",
      expect.objectContaining({ multiLine: false }),
    );
  });

  it("does not touch the backend when algorithm selection is canceled", async () => {
    mocks.askChoice.mockResolvedValue(null);
    openRowMenu(mounted!.container, "~/a.txt");
    clickMenuItem(mounted!.container, "校验值…");
    await flush();
    expect(mocks.checksum).not.toHaveBeenCalled();
    expect(mocks.promptText).not.toHaveBeenCalled();
  });

  it("does not offer checksum for directories", async () => {
    openRowMenu(mounted!.container, "~/sub");
    expect(() => menuItem(mounted!.container, "校验值…")).toThrow();
  });
});

describe("FileTree entry points and state migration", () => {
  beforeEach(async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("directory rows offer rename and permissions but not checksum", () => {
    openRowMenu(mounted!.container, "~/sub");
    expect(menuItem(mounted!.container, "重命名")).toBeTruthy();
    expect(menuItem(mounted!.container, "权限…")).toBeTruthy();
    expect(() => menuItem(mounted!.container, "校验值…")).toThrow();
  });

  it("moves expansion and selection state to the new path after renaming a directory", async () => {
    click(rowByPath(mounted!.container, "~/sub"));
    await waitFor(() =>
      expect(mocks.list).toHaveBeenCalledWith(SID, "~/sub"),
    );
    mocks.promptText.mockResolvedValue("sub2");
    openRowMenu(mounted!.container, "~/sub");
    clickMenuItem(mounted!.container, "重命名");
    await flush();
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/sub", "~/sub2");
    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "~/sub2"));
  });
});

describe("name preflight checks for new files and folders", () => {
  const invalidNames = ["..", "a/b", "a\\b", " name ", "a\0b"];

  for (const [label, ariaLabel, rpc] of [
    ["new file", "新建文件", "write"],
    ["new directory", "新建目录", "mkdir"],
  ] as const) {
    it(`FileTree ${label} rejects dangerous names before joinPath`, async () => {
      mounted = mountTree();
      await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
      const button = mounted.container.querySelector<HTMLButtonElement>(
        `button[aria-label="${ariaLabel}"]`,
      )!;
      for (const name of invalidNames) {
        mocks.promptText.mockResolvedValueOnce(name);
        click(button);
        await flush();
      }
      expect(mocks[rpc]).not.toHaveBeenCalled();
      expect(mocks.toast).toHaveBeenCalledTimes(invalidNames.length);
    });
  }

  it("creates valid names in the current directory", async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
    mocks.promptText.mockResolvedValue("ok.txt");
    click(mounted.container.querySelector('button[aria-label="新建文件"]')!);
    await waitFor(() => expect(mocks.write).toHaveBeenCalledWith(SID, "~/ok.txt", "", false));
  });
});

describe("selection and expansion state after FileTree navigation", () => {
  beforeEach(() => {
    mocks.list.mockImplementation((_s: string, path: string) => {
      if (path === "~") {
        return Promise.resolve([entry("sub", "dir"), entry("other", "dir")]);
      }
      if (path === "~/sub") {
        return Promise.resolve([
          entry("inner", "dir", { path: "~/sub/inner" }),
          entry("a.txt", "file", { path: "~/sub/a.txt" }),
        ]);
      }
      if (path === "~/sub/inner") {
        return Promise.resolve([entry("deep.txt", "file", { path: "~/sub/inner/deep.txt" })]);
      }
      if (path === "~/other") {
        return Promise.resolve([entry("other.txt", "file", { path: "~/other/other.txt" })]);
      }
      if (path === "/") {
        return Promise.resolve([entry("root.txt", "file", { path: "/root.txt" })]);
      }
      return Promise.resolve([]);
    });
  });

  async function enterSubdirectory(): Promise<void> {
    act(() => {
      rowByPath(mounted!.container, "~/sub").dispatchEvent(
        new MouseEvent("dblclick", { bubbles: true, cancelable: true }),
      );
    });
    await waitFor(() => expect(rowByPath(mounted!.container, "~/sub/a.txt")).toBeTruthy());
  }

  it("parent navigation clears stale selection and prunes expanded directories", async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/sub")).toBeTruthy());
    click(rowByPath(mounted.container, "~/sub"));
    await waitFor(() => expect(rowByPath(mounted!.container, "~/sub/a.txt")).toBeTruthy());
    click(rowByPath(mounted.container, "~/sub/a.txt"));
    click(mounted.container.querySelector('button[aria-label^="上级目录"]')!);
    await waitFor(() => expect(rowByPath(mounted!.container, "/root.txt")).toBeTruthy());

    expect(mounted.container.querySelectorAll('[aria-selected="true"]')).toHaveLength(0);
    mocks.promptText.mockResolvedValue("fresh.txt");
    click(mounted.container.querySelector('button[aria-label="新建文件"]')!);
    await waitFor(() => expect(mocks.write).toHaveBeenCalledWith(SID, "/fresh.txt", "", false));

    click(mounted.container.querySelector('button[aria-label="回到家目录 (~)"]')!);
    await waitFor(() => expect(rowByPath(mounted!.container, "~/sub")).toBeTruthy());
    expect(rowByPath(mounted.container, "~/sub").getAttribute("aria-expanded")).toBe("false");
  });

  it("create actions do not use the previous directory's selection after Home navigation", async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/sub")).toBeTruthy());
    await enterSubdirectory();
    click(rowByPath(mounted.container, "~/sub/a.txt"));
    click(mounted.container.querySelector('button[aria-label="回到家目录 (~)"]')!);
    await waitFor(() => expect(rowByPath(mounted!.container, "~/sub")).toBeTruthy());

    expect(mounted.container.querySelectorAll('[aria-selected="true"]')).toHaveLength(0);
    mocks.promptText.mockResolvedValue("fresh.txt");
    click(mounted.container.querySelector('button[aria-label="新建文件"]')!);
    await waitFor(() => expect(mocks.write).toHaveBeenCalledWith(SID, "~/fresh.txt", "", false));
  });

  it("create actions do not use the previous directory's selection after breadcrumb navigation", async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/sub")).toBeTruthy());
    await enterSubdirectory();
    click(rowByPath(mounted.container, "~/sub/a.txt"));
    click(mounted.container.querySelector('button.nx-path-crumb[title="~"]')!);
    await waitFor(() => expect(rowByPath(mounted!.container, "~/sub")).toBeTruthy());

    expect(mounted.container.querySelectorAll('[aria-selected="true"]')).toHaveLength(0);
    mocks.promptText.mockResolvedValue("fresh-dir");
    click(mounted.container.querySelector('button[aria-label="新建目录"]')!);
    await waitFor(() => expect(mocks.mkdir).toHaveBeenCalledWith(SID, "~/fresh-dir"));
  });
});

describe("FileTree selected-item action validation", () => {
  beforeEach(async () => {
    mocks.list.mockResolvedValue([
      entry("a.txt", "file"),
      entry("image.png", "file"),
      entry("sub", "dir"),
    ]);
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("keeps menu items consistent for directories and non-text files", () => {
    openRowMenu(mounted!.container, "~/sub");
    expect([...document.querySelectorAll('[role="menuitem"]')].some((b) => b.textContent?.includes("查看日志"))).toBe(false);
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });

    openRowMenu(mounted!.container, "~/image.png");
    expect(menuItem(mounted!.container, "查看日志").disabled).toBe(true);
    expect(menuItem(mounted!.container, "在下方编辑").disabled).toBe(true);
  });

  it("a selection that disappears from a refreshed listing is cleared and triggers no action", async () => {
    click(rowByPath(mounted!.container, "~/a.txt"));
    expect(rowByPath(mounted!.container, "~/a.txt").getAttribute("aria-selected")).toBe("true");
    mocks.list.mockResolvedValue([entry("sub", "dir")]);
    click(mounted!.container.querySelector<HTMLButtonElement>('button[aria-label="刷新文件列表"]')!);
    await waitFor(() => expect(mounted!.container.textContent).not.toContain("a.txt"));

    expect(mounted!.container.querySelectorAll('[aria-selected="true"]')).toHaveLength(0);
    expect(mocks.pickSavePath).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces).toHaveLength(0);
  });
});

describe("disconnected-session feedback for terminal entry points", () => {
  it("FileTree: opening in a new terminal reports a clear error when the session is disconnected", async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/sub")).toBeTruthy());

    openRowMenu(mounted!.container, "~/sub");
    clickMenuItem(mounted!.container, "在新终端打开");
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("error", "当前会话已断开，无法打开终端");
  });

});

describe("operations preserve existing state", () => {
  it("preserves unrelated dirty editors after an operation", async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());

    setFileEditorDirty(SID, "~/other.txt", true);
    mocks.promptText.mockResolvedValue("600");
    openRowMenu(mounted!.container, "~/a.txt");
    clickMenuItem(mounted!.container, "权限…");
    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("权限已更新")),
    );

    expect(
      isDirtyFileEditor({
        id: "x",
        kind: "files",
        title: "other.txt",
        sessionId: SID,
        path: "~/other.txt",
        closable: true,
      }),
    ).toBe(true);
    setFileEditorDirty(SID, "~/other.txt", false);
  });
});

describe("operations under React StrictMode (review P1 regression)", () => {
  function mountTreeStrict(): MountedView {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return mount(
      createElement(
        StrictMode,
        null,
        createElement(
          QueryClientProvider,
          { client },
          createElement(FileTree, { sessionId: SID }),
        ),
      ),
    );
  }

  beforeEach(async () => {
    mounted = mountTreeStrict();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("rename actually issues an RPC and shows a result toast under StrictMode", async () => {
    mocks.promptText.mockResolvedValue("c.txt");
    openRowMenu(mounted!.container, "~/a.txt");
    clickMenuItem(mounted!.container, "重命名");
    await waitFor(() => expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/c.txt"));
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已重命名"));
  });

  it("chmod actually issues an RPC under StrictMode", async () => {
    mocks.promptText.mockResolvedValue("600");
    openRowMenu(mounted!.container, "~/a.txt");
    clickMenuItem(mounted!.container, "权限…");
    await waitFor(() => expect(mocks.chmod).toHaveBeenCalledWith(SID, "~/a.txt", 0o600));
  });

  it("checksum actually issues an RPC and presents the result under StrictMode", async () => {
    mocks.askChoice.mockResolvedValue("sha256");
    openRowMenu(mounted!.container, "~/a.txt");
    clickMenuItem(mounted!.container, "校验值…");
    await waitFor(() => expect(mocks.checksum).toHaveBeenCalledWith(SID, "~/a.txt", "sha256"));
    expect(mocks.promptText).toHaveBeenCalledWith(
      expect.stringContaining("SHA-256"),
      "deadbeef",
      expect.anything(),
    );
  });
});

describe("rename open-editor directory boundaries", () => {
  beforeEach(async () => {
    mocks.list.mockImplementation((_s: string, p: string) =>
      Promise.resolve(
        p === "~"
          ? [entry("a", "dir", { path: "/a" }), entry("abc", "dir", { path: "/abc" })]
          : [],
      ),
    );
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "/a")).toBeTruthy());
  });

  function seedOpenEditor(path: string): AppTab {
    const tab = editorTab(path);
    act(() => {
      useUi.setState({
        workspaces: [
          {
            id: "ws",
            kind: "session",
            title: "w",
            panes: [{ id: "p", tabs: [tab], activeTabId: tab.id }],
            activePaneId: "p",
            closable: true,
          },
        ],
        activeWorkspaceId: "ws",
      });
    });
    return tab;
  }

  async function triggerDirectoryRename(): Promise<void> {
    mocks.promptText.mockResolvedValue("renamed");
    openRowMenu(mounted!.container, "/a");
    clickMenuItem(mounted!.container, "重命名");
    await flush();
  }

  it("migrates a dirty directory descendant and preserves its dirty state", async () => {
    const tab = seedOpenEditor("/a/file.txt");
    setFileEditorDirty(SID, "/a/file.txt", true);
    await triggerDirectoryRename();
    expect(mocks.rename).toHaveBeenCalledWith(SID, "/a", "/renamed");
    const migrated = useUi.getState().workspaces[0]?.panes[0]?.tabs[0] ?? tab;
    expect(migrated).toMatchObject({ id: tab.id, path: "/renamed/file.txt", title: "file.txt" });
    expect(isDirtyFileEditor(migrated)).toBe(true);
    expect(isDirtyFileEditor(tab)).toBe(false);
    setFileEditorDirty(SID, "/renamed/file.txt", false);
  });

  it("migrates a clean directory descendant", async () => {
    const tab = seedOpenEditor("/a/file.txt");
    await triggerDirectoryRename();
    expect(mocks.rename).toHaveBeenCalledWith(SID, "/a", "/renamed");
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs[0]).toMatchObject({
      id: tab.id,
      path: "/renamed/file.txt",
      title: "file.txt",
    });
  });

  it("does not treat /abc as a descendant of /a", async () => {
    seedOpenEditor("/abc/file.txt");
    await triggerDirectoryRename();
    expect(mocks.rename).toHaveBeenCalledWith(SID, "/a", "/renamed");
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已重命名"));
  });
});

describe("Windows backslash paths (review P2-2 / R2 regression)", () => {
  beforeEach(async () => {
    let subRenamed = false;
    mocks.list.mockImplementation((_s: string, p: string) => {
      if (p === "~") {
        const sub = subRenamed
          ? entry("sub2", "dir", { path: "~\\sub2" })
          : entry("sub", "dir", { path: "~\\sub" });
        return Promise.resolve(
          [
            sub,
            entry("other", "dir", { path: "~\\other" }),
            entry("a.txt", "file", { path: "~\\a.txt" }),
            entry("b.txt", "file", { path: "~\\b.txt" }),
          ].map((e) => ({ ...e })),
        );
      }
      if (p === "~\\sub") {
        return Promise.resolve([
          entry("inner", "dir", { path: "~\\sub\\inner" }),
          entry("a.txt", "file", { path: "~\\sub\\a.txt" }),
          entry("b.txt", "file", { path: "~\\sub\\b.txt" }),
        ]);
      }
      if (p === "~\\other") {
        return Promise.resolve([entry("o.txt", "file", { path: "~\\other\\o.txt" })]);
      }
      if (p === "~\\sub2") {
        return Promise.resolve([
          entry("inner", "dir", { path: "~\\sub2\\inner" }),
          entry("a.txt", "file", { path: "~\\sub2\\a.txt" }),
        ]);
      }
      return Promise.resolve([]);
    });
    mocks.rename.mockImplementation(() => {
      subRenamed = true;
      return Promise.resolve();
    });
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~\\sub")).toBeTruthy());
    click(rowByPath(mounted!.container, "~\\sub"));
    await waitFor(() =>
      expect(rowByPath(mounted!.container, "~\\sub\\a.txt")).toBeTruthy(),
    );
    click(rowByPath(mounted!.container, "~\\sub\\inner"));
    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "~\\sub\\inner"));
  });

  it("shows the usual overwrite confirmation for name conflicts in nested backslash directories", async () => {
    mocks.promptText.mockResolvedValue("b.txt");
    openRowMenu(mounted!.container, "~\\sub\\a.txt");
    clickMenuItem(mounted!.container, "重命名");
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("已存在"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~\\sub\\a.txt", "~/sub/b.txt");
  });

  it("migrates expansion and selection using the original separator style after directory rename while the backend still returns backslashes", async () => {
    mocks.promptText.mockResolvedValue("sub2");
    openRowMenu(mounted!.container, "~\\sub");
    clickMenuItem(mounted!.container, "重命名");
    await flush();
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~\\sub", "~/sub2");
    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "~\\sub2"));
    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "~\\sub2\\inner"));
    await waitFor(() =>
      expect(rowByPath(mounted!.container, "~\\sub2").className).toContain("is-selected"),
    );
  });

  it("does not affect expansion state of directories not involved in the rename", async () => {
    click(rowByPath(mounted!.container, "~\\other"));
    await waitFor(() =>
      expect(rowByPath(mounted!.container, "~\\other\\o.txt")).toBeTruthy(),
    );
    mocks.promptText.mockResolvedValue("sub2");
    openRowMenu(mounted!.container, "~\\sub");
    clickMenuItem(mounted!.container, "重命名");
    await flush();
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~\\sub", "~/sub2");
    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "~\\sub2"));
    expect(rowByPath(mounted!.container, "~\\other\\o.txt")).toBeTruthy();
  });
});

describe("real backend path shapes (~ list keys + absolute entry.path, R1 review P1/P2 regression)", () => {
  const HOME_ABS = "/home/u";
  let renamedRoot = false;
  let renamedSub = false;

  const homeEntries = (): FileEntryDto[] => [
    entry("sub", "dir", { path: `${HOME_ABS}/sub` }),
    renamedRoot
      ? entry("c.txt", "file", { path: `${HOME_ABS}/c.txt` })
      : entry("a.txt", "file", { path: `${HOME_ABS}/a.txt` }),
    entry("b.txt", "file", { path: `${HOME_ABS}/b.txt` }),
  ];
  const subEntries = (): FileEntryDto[] => [
    renamedSub
      ? entry("c.txt", "file", { path: `${HOME_ABS}/sub/c.txt` })
      : entry("a.txt", "file", { path: `${HOME_ABS}/sub/a.txt` }),
    entry("b.txt", "file", { path: `${HOME_ABS}/sub/b.txt` }),
  ];

  const listCalls = (key: string): number =>
    mocks.list.mock.calls.filter((c) => c[1] === key).length;

  beforeEach(async () => {
    renamedRoot = false;
    renamedSub = false;
    mocks.list.mockImplementation((_s: string, p: string) => {
      if (p === "~" || p === HOME_ABS) return Promise.resolve(homeEntries().map((e) => ({ ...e })));
      if (p === "~/sub" || p === `${HOME_ABS}/sub`)
        return Promise.resolve(subEntries().map((e) => ({ ...e })));
      return Promise.resolve([]);
    });
    mocks.rename.mockImplementation((_s: string, from: string) => {
      if (from === `${HOME_ABS}/a.txt`) renamedRoot = true;
      if (from === `${HOME_ABS}/sub/a.txt`) renamedSub = true;
      return Promise.resolve();
    });
  });

  it("FileTree root: shows the usual overwrite confirmation for duplicate names and aborts on cancel (P1-1)", async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, `${HOME_ABS}/a.txt`)).toBeTruthy());

    mocks.promptText.mockResolvedValue("b.txt");
    mocks.ask.mockResolvedValueOnce(false);
    openRowMenu(mounted!.container, `${HOME_ABS}/a.txt`);
    clickMenuItem(mounted!.container, "重命名");
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("已存在"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.rename).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    openRowMenu(mounted!.container, `${HOME_ABS}/a.txt`);
    clickMenuItem(mounted!.container, "重命名");
    await flush();
    expect(mocks.rename).toHaveBeenCalledWith(SID, `${HOME_ABS}/a.txt`, `${HOME_ABS}/b.txt`);
  });

  it("FileTree root: actually refreshes the `~`-keyed list after rename (P2-1)", async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, `${HOME_ABS}/a.txt`)).toBeTruthy());
    const before = listCalls("~");

    mocks.promptText.mockResolvedValue("c.txt");
    openRowMenu(mounted!.container, `${HOME_ABS}/a.txt`);
    clickMenuItem(mounted!.container, "重命名");
    await flush();
    expect(mocks.rename).toHaveBeenCalledWith(SID, `${HOME_ABS}/a.txt`, `${HOME_ABS}/c.txt`);
    await waitFor(() => expect(listCalls("~")).toBeGreaterThan(before));
    await waitFor(() =>
      expect(rowByPath(mounted!.container, `${HOME_ABS}/c.txt`)).toBeTruthy(),
    );
    expect(
      [...mounted!.container.querySelectorAll("div[title]")].some((d) =>
        (d.getAttribute("title") ?? "").startsWith(`${HOME_ABS}/a.txt`),
      ),
    ).toBe(false);
  });

  it("FileTree nested level: refreshes the corresponding list after rename/chmod", async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, `${HOME_ABS}/sub`)).toBeTruthy());
    click(rowByPath(mounted!.container, `${HOME_ABS}/sub`));
    await waitFor(() =>
      expect(rowByPath(mounted!.container, `${HOME_ABS}/sub/a.txt`)).toBeTruthy(),
    );

    mocks.promptText.mockResolvedValue("c.txt");
    openRowMenu(mounted!.container, `${HOME_ABS}/sub/a.txt`);
    clickMenuItem(mounted!.container, "重命名");
    await flush();
    await waitFor(() =>
      expect(rowByPath(mounted!.container, `${HOME_ABS}/sub/c.txt`)).toBeTruthy(),
    );

    const beforeChmod = listCalls(`${HOME_ABS}/sub`);
    mocks.promptText.mockResolvedValue("600");
    openRowMenu(mounted!.container, `${HOME_ABS}/sub/b.txt`);
    clickMenuItem(mounted!.container, "权限…");
    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("权限已更新")),
    );
    await waitFor(() => expect(listCalls(`${HOME_ABS}/sub`)).toBeGreaterThan(beforeChmod));
  });

});

describe("delete and discard confirmations (R42 destructive dialogs)", () => {
  beforeEach(() => {
    editors.views = [];
    mocks.read.mockResolvedValue({
      path: "~/a.txt",
      size: 8,
      contentBase64: btoa("original"),
    });
    useUi.setState({ appDialog: null });
    useRealAsk();
    installRealDialogs();
  });

  function mountWithDialogHost(element: ReactElement): MountedView {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return mount(
      createElement(
        QueryClientProvider,
        { client },
        createElement("div", null, element, createElement(DialogHost)),
      ),
    );
  }

  async function openModal(): Promise<HTMLElement> {
    await waitFor(() =>
      expect(mounted!.container.querySelector(".nx-modal")).not.toBeNull(),
    );
    return mounted!.container.querySelector<HTMLElement>(".nx-modal")!;
  }

  async function closeModal(modal: HTMLElement, button: "取消" | "确定"): Promise<void> {
    clickButton(modal, button);
    await waitFor(() =>
      expect(mounted!.container.querySelector(".nx-modal")).toBeNull(),
    );
  }

  it("FileTree file deletion: shows a warning dialog (alertdialog) and aborts on cancel", async () => {
    mounted = mountWithDialogHost(createElement(FileTree, { sessionId: SID }));
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());

    openRowMenu(mounted!.container, "~/a.txt");
    clickMenuItem(mounted!.container, "删除");
    const modal = await openModal();
    expect(modal.getAttribute("role")).toBe("alertdialog");
    expect(modal.textContent).toContain("删除 ~/a.txt");
    await closeModal(modal, "取消");
    expect(mocks.remove).not.toHaveBeenCalled();

    openRowMenu(mounted!.container, "~/a.txt");
    clickMenuItem(mounted!.container, "删除");
    const modal2 = await openModal();
    expect(modal2.getAttribute("role")).toBe("alertdialog");
    await closeModal(modal2, "确定");
    await waitFor(() => expect(mocks.remove).toHaveBeenCalledWith(SID, "~/a.txt", false));
  });

  it("FileTree directory deletion: warning dialog explains that recursive deletion is irreversible", async () => {
    mounted = mountWithDialogHost(createElement(FileTree, { sessionId: SID }));
    await waitFor(() => expect(rowByPath(mounted!.container, "~/sub")).toBeTruthy());

    openRowMenu(mounted!.container, "~/sub");
    clickMenuItem(mounted!.container, "删除");
    const modal = await openModal();
    expect(modal.getAttribute("role")).toBe("alertdialog");
    expect(modal.textContent).toContain("递归删除，不可恢复");
    await closeModal(modal, "取消");
    expect(mocks.remove).not.toHaveBeenCalled();

    openRowMenu(mounted!.container, "~/sub");
    clickMenuItem(mounted!.container, "删除");
    const modal2 = await openModal();
    await closeModal(modal2, "确定");
    await waitFor(() => expect(mocks.remove).toHaveBeenCalledWith(SID, "~/sub", true));
  });

  it("FileEditor encoding switch: warns about dirty documents first and does not reread from disk on cancel", async () => {
    mounted = mountWithDialogHost(createElement(FileEditor, { sessionId: SID, path: "~/a.txt" }));
    await waitFor(() => expect(editors.views).toHaveLength(1));

    const view = editors.views[0];
    act(() => {
      view.dispatch({ changes: { from: 0, to: 8, insert: "changed!" } });
    });
    await flush();

    chooseSelectOption(mounted.container, "解码方式", "GBK");
    const modal = await openModal();
    expect(modal.getAttribute("role")).toBe("alertdialog");
    expect(modal.textContent).toContain("未保存的改动会丢失");
    await closeModal(modal, "取消");
    expect(mocks.read).toHaveBeenCalledTimes(1);

    // The auto entry also reads UTF-8 once detected, so pick the explicit one, which is listed after it.
    click(mounted.container.querySelector<HTMLElement>('button[aria-label="解码方式"]')!);
    const explicit = [...mounted.container.querySelectorAll<HTMLElement>('[role="option"]')]
      .filter((o) => o.textContent?.includes("UTF-8"))
      .at(-1);
    click(explicit!);
    const modal2 = await openModal();
    await closeModal(modal2, "确定");
    await flush();
    await waitFor(() => expect(mocks.read).toHaveBeenCalledTimes(2));
  });

  it("FileEditor large-file prompt: uses an explicit info dialog for the performance notice and closes the editor on cancel", async () => {
    mocks.read.mockResolvedValue({
      path: "~/big.log",
      size: 6 * 1024 * 1024,
      contentBase64: "",
    });
    const onClose = vi.fn();
    mounted = mountWithDialogHost(
      createElement(FileEditor, { sessionId: SID, path: "~/big.log", onClose }),
    );

    const modal = await openModal();
    expect(modal.getAttribute("role")).toBe("dialog");
    expect(modal.textContent).toContain("文件超过 5MB");
    await closeModal(modal, "取消");
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
  });

  it("FileEditor large-file preflight: read-only chunked view reuses a log tab without reading the whole file", async () => {
    mocks.readRange.mockResolvedValue({
      path: "~/big.log",
      offset: 0,
      size: 6 * 1024 * 1024,
      contentBase64: "",
      truncated: true,
    });
    mocks.askChoice.mockResolvedValue("view");
    act(() => {
      useUi.setState({
        workspaces: [
          {
            id: "ws",
            kind: "session",
            title: "w",
            sessionId: SID,
            panes: [{ id: "p", tabs: [], activeTabId: null }],
            activePaneId: "p",
            closable: true,
          },
        ],
        activeWorkspaceId: "ws",
      });
    });
    const onClose = vi.fn();
    mounted = mountWithDialogHost(
      createElement(FileEditor, { sessionId: SID, path: "~/big.log", onClose }),
    );

    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
    expect(mocks.readRange).toHaveBeenCalledWith(SID, "~/big.log", 0, 1);
    expect(mocks.read).not.toHaveBeenCalled();
    expect(
      mocks.askChoice.mock.calls[0]?.[1]?.choices.map((choice: { label: string }) => choice.label),
    ).toEqual([
      "只读分块查看",
      "编辑整个文件",
    ]);
    const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
    expect(tabs.find((tab) => tab.kind === "log")).toMatchObject({
      sessionId: SID,
      path: "~/big.log",
    });
  });

  it("FileEditor large-file preflight: cancel avoids reading the whole file", async () => {
    mocks.readRange.mockResolvedValue({
      path: "~/big.log",
      offset: 0,
      size: 6 * 1024 * 1024,
      contentBase64: "",
      truncated: true,
    });
    mocks.askChoice.mockResolvedValue(null);
    const onClose = vi.fn();
    mounted = mountWithDialogHost(
      createElement(FileEditor, { sessionId: SID, path: "~/big.log", onClose }),
    );

    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
    expect(mocks.read).not.toHaveBeenCalled();
  });

  it("FileEditor large-file preflight: prompts only once when editing the whole file", async () => {
    mocks.readRange.mockResolvedValue({
      path: "~/big.log",
      offset: 0,
      size: 6 * 1024 * 1024,
      contentBase64: "",
      truncated: true,
    });
    mocks.read.mockResolvedValue({
      path: "~/big.log",
      size: 6 * 1024 * 1024,
      contentBase64: "",
    });
    mocks.askChoice.mockResolvedValue("edit");
    mocks.ask.mockResolvedValue(true);
    mounted = mountWithDialogHost(createElement(FileEditor, { sessionId: SID, path: "~/big.log" }));

    await waitFor(() => expect(editors.views).toHaveLength(1));
    expect(mocks.read).toHaveBeenCalledOnce();
    expect(mocks.ask).not.toHaveBeenCalled();
  });
});

describe("context menus: blank-area entries and wiring", () => {
  function openTreeBlankMenu(): void {
    const tree = mounted!.container.querySelector('[role="tree"]');
    if (!tree) throw new Error("tree not found");
    act(() => {
      tree.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    });
  }

  function menuLabels(): string[] {
    return [...mounted!.container.querySelectorAll('[role="menuitem"]')].map(
      (b) => b.textContent ?? "",
    );
  }

  function seedSessionKind(kind: string): void {
    useUi.setState({
      sessions: [
        { id: SID, assetId: null, name: "h", kind, status: "connected", tabs: [], createdAt: 1 },
      ],
    });
  }

  beforeEach(() => {
    mocks.pickFolderUpload.mockResolvedValue(undefined);
    mocks.dropFolderUpload.mockResolvedValue(undefined);
  });

  it("FileTree blank-area upload and refresh flows", async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());

    mocks.pickLocalFile.mockResolvedValue(null);
    openTreeBlankMenu();
    clickMenuItem(mounted!.container, "上传到当前目录");
    await flush();
    expect(mocks.pickLocalFile).toHaveBeenCalledTimes(1);

    openTreeBlankMenu();
    clickMenuItem(mounted!.container, "上传文件夹到当前目录");
    await flush();
    expect(mocks.pickFolderUpload).toHaveBeenCalledWith(
      expect.objectContaining({ sessionId: SID, dir: "~" }),
    );

    const before = mocks.list.mock.calls.length;
    openTreeBlankMenu();
    clickMenuItem(mounted!.container, "刷新");
    await waitFor(() => expect(mocks.list.mock.calls.length).toBeGreaterThan(before));
  });

  it("directory row menu pack download follows the pickDownloadTarget -> packDownload -> finishSave flow", async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/sub")).toBeTruthy());
    mocks.pickSavePath.mockResolvedValue("/local/sub.tar.gz");
    mocks.packDownload.mockResolvedValue(1234);
    mocks.finishSave.mockResolvedValue("/local/sub.tar.gz");
    mocks.discardStaged.mockResolvedValue(undefined);
    openRowMenu(mounted!.container, "~/sub");
    clickMenuItem(mounted!.container, "打包下载当前目录");
    await waitFor(() =>
      expect(mocks.packDownload).toHaveBeenCalledWith(
        SID,
        "~/sub",
        "/local/sub.tar.gz",
        expect.any(String),
        expect.any(String),
      ),
    );
    expect(mocks.pickSavePath).toHaveBeenCalledWith("sub.tar.gz");
    await waitFor(() =>
      expect(mocks.finishSave).toHaveBeenCalledWith("/local/sub.tar.gz", "sub.tar.gz"),
    );
    // The pack runs as a queued transfer task, so the transfer panel shows its progress.
    await waitFor(() =>
      expect(
        useTransferStore.getState().tasks.find((t) => t.pack && t.status === "done"),
      ).toMatchObject({ remotePath: "~/sub", resultText: "已下载到 /local/sub.tar.gz" }),
    );
  });

  it("hides pack download for winrm sessions and keeps it for ssh sessions", async () => {
    seedSessionKind("winrm");
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/sub")).toBeTruthy());
    openRowMenu(mounted!.container, "~/sub");
    expect(menuLabels().some((t) => t.includes("打包下载"))).toBe(false);
    openTreeBlankMenu();
    const labels = menuLabels();
    expect(labels.some((t) => t.includes("打包下载"))).toBe(false);
    expect(labels.some((t) => t.includes("新建空白文件"))).toBe(true);
    mounted.unmount();

    seedSessionKind("ssh");
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/sub")).toBeTruthy());
    openRowMenu(mounted!.container, "~/sub");
    expect(menuLabels().some((t) => t.includes("打包下载当前目录"))).toBe(true);
  });

  it("FileTree directory row menu also hides pack download for winrm sessions", async () => {
    seedSessionKind("winrm");
    mocks.list.mockImplementation((_s: string, p: string) =>
      Promise.resolve(p === "/" ? [entry("sub", "dir", { path: "/sub" })] : []),
    );
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "/sub")).toBeTruthy());
    openRowMenu(mounted!.container, "/sub");
    expect(menuLabels().some((t) => t.includes("打包下载"))).toBe(false);
  });

  it("FileTree blank-area menu has all entries; new blank file writes to the tree root", async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
    openTreeBlankMenu();
    const labels = menuLabels();
    for (const label of [
      "上传到当前目录",
      "上传文件夹到当前目录",
      "新建目录",
      "新建空白文件",
      "刷新",
    ]) {
      expect(labels.some((t) => t.includes(label)), label).toBe(true);
    }
    mocks.promptText.mockResolvedValue("blank.txt");
    clickMenuItem(mounted!.container, "新建空白文件");
    await waitFor(() => expect(mocks.write).toHaveBeenCalledWith(SID, "~/blank.txt", "", false));
    await waitFor(() => expect(mocks.list.mock.calls.length).toBeGreaterThan(1));
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已创建"));

    openTreeBlankMenu();
    clickMenuItem(mounted!.container, "上传文件夹到当前目录");
    await flush();
    expect(mocks.pickFolderUpload).toHaveBeenCalledWith(
      expect.objectContaining({ sessionId: SID, dir: "~" }),
    );
  });
});
