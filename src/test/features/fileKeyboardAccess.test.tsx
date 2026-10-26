/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  promptText: vi.fn(),
  askChoice: vi.fn(),
  pickLocalFile: vi.fn(),
  pickSavePath: vi.fn(),
  finishSave: vi.fn(),
  discardStaged: vi.fn(),
  list: vi.fn(),
  read: vi.fn(),
  rename: vi.fn(),
  chmod: vi.fn(),
  checksum: vi.fn(),
  mkdir: vi.fn(),
  remove: vi.fn(),
  upload: vi.fn(),
  download: vi.fn(),
  packDownload: vi.fn(),
  extract: vi.fn(),
  termWrite: vi.fn(),
  toast: vi.fn(),
  listenEvent: vi.fn(),
}));
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return {
    ...actual,
    ask: mocks.ask,
    askChoice: mocks.askChoice,
    promptText: mocks.promptText,
    pickLocalFile: mocks.pickLocalFile,
    pickSavePath: mocks.pickSavePath,
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
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, i) => ({ index: i, start: i * 30, size: 30, key: i })),
    getTotalSize: () => count * 30,
  }),
}));

import { openFileTab, useUi, type AppTab } from "../../app/store";
import { setWindowsPlatform } from "../../app/platform";
import type { FileEntryDto } from "../../ipc/types";
import { FileBrowser } from "../../features/files/FileBrowser";

const SID = "s1";

function entry(name: string, kind: string, extra: Partial<FileEntryDto> = {}): FileEntryDto {
  return {
    name,
    path: `~/${name}`,
    kind,
    size: kind === "dir" ? 4096 : 10,
    mode: kind === "symlink" ? "" : kind === "dir" ? "755" : "644",
    owner: "u",
    group: "u",
    mtime: 1,
    symlinkTarget: null,
    ...extra,
  };
}

const HOME_ENTRIES: FileEntryDto[] = [
  entry("a.txt", "file"),
  entry("sub", "dir"),
  entry("link", "symlink", { symlinkTarget: "~/a.txt" }),
];

function mountBrowser(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(FileBrowser, { sessionId: SID })));
}

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function treeitems(container: ParentNode): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>('[role="treeitem"]')];
}

function itemByName(container: ParentNode, name: string): HTMLElement {
  const row = treeitems(container).find((candidate) =>
    candidate.querySelector(".nx-row-name")?.textContent?.includes(name),
  );
  if (!row) throw new Error(`treeitem not found: ${name}`);
  return row;
}

function rowByPath(container: HTMLElement, path: string): HTMLElement {
  const row = [...container.querySelectorAll<HTMLElement>("div[title]")].find((d) =>
    (d.getAttribute("title") ?? "").startsWith(path),
  );
  if (!row) throw new Error(`Row not found: ${path}`);
  return row;
}

function clickMenuItem(container: ParentNode, label: string): void {
  const button = [...container.querySelectorAll('[role="menuitem"]')].find((b) =>
    b.textContent?.includes(label),
  );
  if (!button) throw new Error(`Menu item not found: ${label}`);
  click(button as HTMLButtonElement);
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.ask.mockResolvedValue(true);
  mocks.promptText.mockResolvedValue(null);
  mocks.askChoice.mockResolvedValue(null);
  mocks.list.mockImplementation((_s: string, p: string) =>
    Promise.resolve(p === "~" ? HOME_ENTRIES.map((e) => ({ ...e })) : []),
  );
  mocks.remove.mockResolvedValue(undefined);
  mocks.chmod.mockResolvedValue(undefined);
  mocks.listenEvent.mockReturnValue(Promise.resolve(() => {}));
  useUi.setState({
    pushToast: mocks.toast,
    leftOpen: true,
    leftWidth: 260,
    workspaces: [],
    activeWorkspaceId: null,
    sessions: [],
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("FileBrowser keyboard accessibility", () => {
  beforeEach(async () => {
    mounted = mountBrowser();
    await waitFor(() => expect(itemByName(mounted!.container, "a.txt")).toBeTruthy());
  });

  it("exposes tree/treeitem roles, levels, selection state, and tabIndex on rows while keeping action buttons semantic", () => {
    expect(mounted!.container.querySelector('[role="tree"][aria-label="文件"]')).not.toBeNull();
    expect(mounted!.container.querySelector('[role="option"]')).toBeNull();
    const rows = treeitems(mounted!.container);
    expect(rows.length).toBeGreaterThanOrEqual(3);
    for (const row of rows) {
      expect(row.getAttribute("aria-level")).toBe("1");
      expect(row.getAttribute("aria-selected")).toBe("false");
      expect(row.tabIndex).toBe(0);
      const more = row.querySelector("button");
      expect(more?.getAttribute("aria-label") ?? "").toContain("更多操作");
    }
  });

  it("selects the focused row with Space and keeps selection exclusive across rows", () => {
    const a = itemByName(mounted!.container, "a.txt");
    const sub = itemByName(mounted!.container, "sub");
    act(() => a.focus());
    keyDown(a, " ");
    expect(a.getAttribute("aria-selected")).toBe("true");

    act(() => sub.focus());
    keyDown(sub, " ");
    expect(sub.getAttribute("aria-selected")).toBe("true");
    expect(a.getAttribute("aria-selected")).toBe("false");
  });

  it("opens a file in an editor tab with Enter", async () => {
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
    const a = itemByName(mounted!.container, "a.txt");
    act(() => a.focus());
    keyDown(a, "Enter");
    await waitFor(() => {
      const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
      expect(tabs.some((t) => t.kind === "files" && t.path === "~/a.txt")).toBe(true);
    });
  });

  it("keeps the editor instance on rename and deduplicates the new normalized locator", async () => {
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
    const a = itemByName(mounted!.container, "a.txt");
    act(() => a.focus());
    keyDown(a, "Enter");
    await waitFor(() => {
      const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
      expect(tabs.some((t) => t.kind === "files" && t.path === "~/a.txt")).toBe(true);
    });
    const editorId = useUi.getState().workspaces[0]?.panes[0]?.tabs[0]?.id;

    mocks.promptText.mockResolvedValue("c.txt");
    act(() => {
      a.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    });
    clickMenuItem(mounted!.container, "重命名");
    await flush();

    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/c.txt");
    expect(useUi.getState().workspaces[0]?.panes[0]).toMatchObject({ activeTabId: editorId });
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs[0]).toMatchObject({
      id: editorId,
      path: "~/c.txt",
      title: "c.txt",
    });

    act(() => openFileTab(SID, "~\\c.txt"));
    const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
    expect(tabs).toHaveLength(1);
    expect(tabs[0]?.id).toBe(editorId);
  });

  it("enters a directory and loads its listing with Enter", async () => {
    const sub = itemByName(mounted!.container, "sub");
    act(() => sub.focus());
    keyDown(sub, "Enter");
    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "~/sub"));
  });

  it("moves focus between rows with ArrowDown/ArrowUp", () => {
    const a = itemByName(mounted!.container, "a.txt");
    const sub = itemByName(mounted!.container, "sub");
    act(() => a.focus());
    keyDown(a, "ArrowDown");
    expect(document.activeElement).toBe(sub);
    keyDown(sub, "ArrowUp");
    expect(document.activeElement).toBe(a);
    keyDown(a, "ArrowUp");
    expect(document.activeElement).toBe(a);
  });

  it("uses the existing warning confirmation for Delete: cancel keeps the file and confirm deletes it", async () => {
    const a = itemByName(mounted!.container, "a.txt");
    act(() => a.focus());

    mocks.ask.mockResolvedValueOnce(false);
    keyDown(a, "Delete");
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("删除 ~/a.txt"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.remove).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    keyDown(a, "Delete");
    await waitFor(() => expect(mocks.remove).toHaveBeenCalledWith(SID, "~/a.txt", false));
  });

  it("explains recursive deletion for Backspace on a directory and deletes it with directory semantics after confirmation", async () => {
    const sub = itemByName(mounted!.container, "sub");
    act(() => sub.focus());

    mocks.ask.mockResolvedValueOnce(true);
    keyDown(sub, "Backspace");
    await waitFor(() => expect(mocks.remove).toHaveBeenCalledWith(SID, "~/sub", true));
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("递归删除，不可恢复"),
      expect.objectContaining({ kind: "warning" }),
    );
  });

  it("opens the row menu with ContextMenu or Shift+F10 and closes it with Escape", async () => {
    const sub = itemByName(mounted!.container, "sub");
    act(() => sub.focus());
    keyDown(sub, "ContextMenu");
    await waitFor(() =>
      expect(mounted!.container.querySelector('[role="menu"]')).not.toBeNull(),
    );
    expect(sub.getAttribute("aria-selected")).toBe("true");
    expect(
      [...mounted!.container.querySelectorAll('[role="menuitem"]')].some((b) =>
        b.textContent?.includes("打包下载当前目录"),
      ),
    ).toBe(true);

    keyDown(document.activeElement as HTMLElement, "Escape");
    await waitFor(() => expect(mounted!.container.querySelector('[role="menu"]')).toBeNull());

    act(() => sub.focus());
    keyDown(sub, "F10", { shiftKey: true });
    await waitFor(() =>
      expect(mounted!.container.querySelector('[role="menu"]')).not.toBeNull(),
    );
    expect(sub.getAttribute("aria-selected")).toBe("true");
  });

  it("does not open the row when Enter is pressed on its inline ⋯ button", () => {
    const a = itemByName(mounted!.container, "a.txt");
    const more = a.querySelector<HTMLButtonElement>('button[aria-label="更多操作 a.txt"]');
    if (!more) throw new Error("more button not found");
    act(() => more.focus());

    const event = keyDown(more, "Enter");
    expect(event.defaultPrevented).toBe(false);
    expect(mounted!.container.querySelector('[role="menu"]')).toBeNull();
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs ?? []).toHaveLength(0);
  });

  it("does not let row-consumed keys bubble to window-level bare-key shortcuts, while unconsumed keys still bubble", async () => {
    const globalHits: string[] = [];
    const onWindowKey = (e: KeyboardEvent) => {
      globalHits.push(e.key);
    };
    window.addEventListener("keydown", onWindowKey);
    try {
      const a = itemByName(mounted!.container, "a.txt");
      act(() => a.focus());
      keyDown(a, " ");
      expect(a.getAttribute("aria-selected")).toBe("true");

      keyDown(a, "ArrowDown");
      expect(document.activeElement).toBe(itemByName(mounted!.container, "sub"));

      keyDown(document.activeElement as HTMLElement, "Delete");
      await flush();
      expect(mocks.ask).toHaveBeenCalledWith(
        expect.stringContaining("删除 ~/sub"),
        expect.objectContaining({ kind: "warning" }),
      );
      expect(globalHits).toEqual([]);

      keyDown(itemByName(mounted!.container, "a.txt"), "x");
      expect(globalHits).toEqual(["x"]);
    } finally {
      window.removeEventListener("keydown", onWindowKey);
    }
  });
});

describe("useFileOps dialog copy (plain text)", () => {
  beforeEach(async () => {
    mounted = mountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("omits Markdown asterisks from the chmod symlink confirmation and preserves the target meaning", async () => {
    mocks.promptText.mockResolvedValue("600");
    act(() => {
      rowByPath(mounted!.container, "~/link").dispatchEvent(
        new MouseEvent("contextmenu", { bubbles: true, cancelable: true }),
      );
    });
    clickMenuItem(mounted!.container, "权限…");
    await flush();

    const notice = mocks.ask.mock.calls.find((c) => String(c[0]).includes("符号链接"));
    expect(notice).toBeTruthy();
    const message = String(notice![0]);
    expect(message).not.toContain("**");
    expect(message).toContain("修改链接目标的权限");
    expect(message).toContain("~/a.txt");
    expect(notice![1]).toEqual(expect.objectContaining({ kind: "warning" }));
  });

  it("omits decorative symbols from the dangerous chmod confirmation and preserves the owner warning", async () => {
    mocks.promptText.mockResolvedValue("000");
    act(() => {
      rowByPath(mounted!.container, "~/a.txt").dispatchEvent(
        new MouseEvent("contextmenu", { bubbles: true, cancelable: true }),
      );
    });
    clickMenuItem(mounted!.container, "权限…");
    await flush();

    const confirm = mocks.ask.mock.calls.find((c) => String(c[0]).includes("所有者"));
    expect(confirm).toBeTruthy();
    const message = String(confirm![0]);
    expect(message).not.toContain("⚠");
    expect(message).toContain("~/a.txt");
    expect(message).toContain("新权限会移除所有者的读取或写入权限");
    expect(confirm![1]).toEqual(expect.objectContaining({ kind: "warning" }));
  });
});

describe("session filesystem locator semantics", () => {
  function seedWorkspace(kind: string, tabs: AppTab[] = []): void {
    act(() => {
      useUi.setState({
        sessions: [
          { id: SID, assetId: null, name: "h", kind, status: "connected", tabs: [], createdAt: 1 },
        ],
        workspaces: [
          {
            id: "ws",
            kind: "session",
            title: "w",
            sessionId: SID,
            assetKind: kind,
            panes: [{ id: "p", tabs, activeTabId: tabs[0]?.id ?? null }],
            activePaneId: "p",
            closable: true,
          },
        ],
        activeWorkspaceId: "ws",
      });
    });
  }

  function fileTabs(): AppTab[] {
    return (useUi.getState().workspaces[0]?.panes[0]?.tabs ?? []).filter(
      (tab) => tab.kind === "files",
    );
  }

  it("deduplicates WinRM open locators by case without lowercasing the displayed path", () => {
    seedWorkspace("winrm");
    act(() => {
      openFileTab(SID, "C:/Work/Draft.TXT");
      openFileTab(SID, "c:/work/draft.txt");
    });
    expect(fileTabs()).toHaveLength(1);
    expect(fileTabs()[0]?.path).toBe("c:/work/draft.txt");
  });

  it("deduplicates local Windows open locators by case", () => {
    setWindowsPlatform(true);
    try {
      seedWorkspace("local");
      act(() => {
        openFileTab(SID, "C:/Work/Draft.TXT");
        openFileTab(SID, "c:/work/draft.txt");
      });
      expect(fileTabs()).toHaveLength(1);
    } finally {
      setWindowsPlatform(false);
    }
  });

  it("keeps POSIX open locators case-sensitive", () => {
    seedWorkspace("ssh");
    act(() => {
      openFileTab(SID, "/tmp/Draft.TXT");
      openFileTab(SID, "/tmp/draft.txt");
    });
    expect(fileTabs()).toHaveLength(2);
  });

  it("migrates a WinRM locator whose case differs from the rename source", () => {
    const tab: AppTab = {
      id: "windows-editor",
      kind: "files",
      title: "Draft.TXT",
      sessionId: SID,
      path: "C:\\Work\\Draft.TXT",
      closable: true,
    };
    seedWorkspace("winrm", [tab]);
    act(() => {
      useUi.getState().migrateFileEditorLocators(
        SID,
        "c:/work/draft.txt",
        "D:/Next/Final.TxT",
        "file",
      );
    });
    expect(fileTabs()).toHaveLength(1);
    expect(fileTabs()[0]).toMatchObject({
      id: tab.id,
      path: "D:/Next/Final.TxT",
      title: "Final.TxT",
    });
  });
});
