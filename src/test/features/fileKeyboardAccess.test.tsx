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

import { useUi } from "../../app/store";
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

function keyDown(target: EventTarget, key: string): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true });
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

describe("FileBrowser 键盘可达性", () => {
  beforeEach(async () => {
    mounted = mountBrowser();
    await waitFor(() => expect(itemByName(mounted!.container, "a.txt")).toBeTruthy());
  });

  it("行暴露 tree/treeitem 角色、层级、选中态与 tabIndex，交互按钮保持语义暴露", () => {
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

  it("空格选中焦点行，行间互斥", () => {
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

  it("回车把文件打开为编辑器标签", async () => {
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
            splitRatio: 0.5,
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

  it("回车进入目录并加载其列表", async () => {
    const sub = itemByName(mounted!.container, "sub");
    act(() => sub.focus());
    keyDown(sub, "Enter");
    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "~/sub"));
  });

  it("ArrowDown/ArrowUp 在行间移动焦点", () => {
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

  it("Delete 走既有警示确认：取消不删，确认才删", async () => {
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

  it("目录 Backspace 确认讲清递归删除，确认后按目录语义删除", async () => {
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

  it("行内 ⋯ 按钮的 Enter 不触发行打开", () => {
    const a = itemByName(mounted!.container, "a.txt");
    const more = a.querySelector<HTMLButtonElement>('button[aria-label="更多操作 a.txt"]');
    if (!more) throw new Error("more button not found");
    act(() => more.focus());

    const event = keyDown(more, "Enter");
    expect(event.defaultPrevented).toBe(false);
    expect(mounted!.container.querySelector('[role="menu"]')).toBeNull();
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs ?? []).toHaveLength(0);
  });

  it("行消费的按键不再冒泡到 window 级裸键快捷键，未消费的键照常冒泡", async () => {
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

describe("useFileOps 对话框文案（纯文本）", () => {
  beforeEach(async () => {
    mounted = mountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("chmod 符号链接确认无 markdown 星号且保留目标含义", async () => {
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
    expect(message).toContain("「目标」");
    expect(message).toContain("~/a.txt");
    expect(notice![1]).toEqual(expect.objectContaining({ kind: "warning" }));
  });

  it("chmod 危险确认无装饰符号且保留所有者警告", async () => {
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
    expect(message).toContain("新权限移除了所有者的读取或写入");
    expect(confirm![1]).toEqual(expect.objectContaining({ kind: "warning" }));
  });
});
