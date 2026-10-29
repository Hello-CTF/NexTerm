/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, waitForRendered, type MountedView } from "./reactTestUtils";

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
  enqueueDownload: vi.fn(),
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
vi.mock("../../features/files/transferStore", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../features/files/transferStore")>();
  return { ...actual, enqueueDownload: mocks.enqueueDownload };
});

import { useUi } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";
import { FileTree } from "../../features/files/FileTree";

const SID = "s1";

function entry(name: string, kind: string): FileEntryDto {
  return {
    name,
    path: `~/${name}`,
    kind,
    size: 10,
    mode: "644",
    owner: "u",
    group: "u",
    mtime: 1,
    symlinkTarget: null,
  };
}

const ENTRIES = [entry("a.txt", "file"), entry("b.txt", "file"), entry("c.txt", "file"), entry("sub", "dir")];

function stubPointer(coarse: boolean): void {
  vi.stubGlobal(
    "matchMedia",
    (query: string) =>
      ({
        matches: coarse && query.includes("pointer: coarse"),
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
      }) as MediaQueryList,
  );
}

function mountTree(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(FileTree, { sessionId: SID })));
}

function row(name: string): HTMLElement {
  const found = [...mounted!.container.querySelectorAll<HTMLElement>('[role="treeitem"]')].find((r) =>
    r.querySelector(".nx-row-name")?.textContent === name,
  );
  if (!found) throw new Error(`row not found: ${name}`);
  return found;
}

function selectedNames(): string[] {
  return [...mounted!.container.querySelectorAll<HTMLElement>('[role="treeitem"][aria-selected="true"]')].map(
    (r) => r.querySelector(".nx-row-name")?.textContent ?? "",
  );
}

function clickWith(el: Element, init: MouseEventInit): void {
  act(() => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, ...init }));
  });
}

function keyDown(target: Element, key: string, init: KeyboardEventInit = {}): void {
  act(() => {
    target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init }));
  });
}

function contextMenu(el: Element): void {
  act(() => {
    el.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 5, clientY: 5 }));
  });
}

function pointer(el: Element, type: string, x = 5, y = 5): void {
  const ev = new MouseEvent(type, { bubbles: true, cancelable: true, clientX: x, clientY: y });
  Object.defineProperty(ev, "pointerType", { value: "touch" });
  act(() => {
    el.dispatchEvent(ev);
  });
}

function menuLabels(): string[] {
  return [...document.querySelectorAll('[role="menuitem"]')].map((b) => b.textContent ?? "");
}

function menuItem(label: string): HTMLButtonElement {
  const b = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((x) =>
    x.textContent?.includes(label),
  );
  if (!b) throw new Error(`menu item not found: ${label}`);
  return b;
}

let mounted: MountedView | undefined;

async function mountReady(): Promise<void> {
  mounted = mountTree();
  await waitForRendered(() => expect(row("a.txt")).toBeTruthy());
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  stubPointer(false);
  mocks.ask.mockResolvedValue(true);
  mocks.list.mockImplementation((_s: string, p: string) =>
    Promise.resolve(p === "~" ? ENTRIES.map((e) => ({ ...e })) : []),
  );
  mocks.remove.mockResolvedValue(undefined);
  mocks.pickSavePath.mockImplementation((name: string) => Promise.resolve(`/tmp/${name}`));
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
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("FileTree desktop multi-select", () => {
  it("has no toolbar download or delete buttons", async () => {
    await mountReady();
    const labels = [...mounted!.container.querySelectorAll(".nx-files-bar button")].map((b) => b.getAttribute("aria-label"));
    expect(labels.length).toBeGreaterThan(0);
    expect(labels).not.toContain("下载选中项");
    expect(labels).not.toContain("删除选中项");
  });

  it("supports ctrl toggle, shift range and plain click reset", async () => {
    await mountReady();
    click(row("a.txt"));
    expect(selectedNames()).toEqual(["a.txt"]);
    clickWith(row("c.txt"), { ctrlKey: true });
    expect(selectedNames()).toEqual(["a.txt", "c.txt"]);
    clickWith(row("c.txt"), { ctrlKey: true });
    expect(selectedNames()).toEqual(["a.txt"]);
    click(row("a.txt"));
    clickWith(row("c.txt"), { shiftKey: true });
    expect(selectedNames()).toEqual(["a.txt", "b.txt", "c.txt"]);
    clickWith(row("a.txt"), { metaKey: true });
    expect(selectedNames()).toEqual(["b.txt", "c.txt"]);
    click(row("sub"));
    expect(selectedNames()).toEqual(["sub"]);
    expect(mocks.list).toHaveBeenCalledWith(SID, "~/sub");
  });

  it("does not expand a directory on modifier clicks", async () => {
    await mountReady();
    click(row("a.txt"));
    clickWith(row("sub"), { ctrlKey: true });
    expect(selectedNames()).toEqual(["sub", "a.txt"]);
    expect(mocks.list).not.toHaveBeenCalledWith(SID, "~/sub");
  });

  it("selects everything with Ctrl+A and clears with Escape", async () => {
    await mountReady();
    click(row("a.txt"));
    keyDown(row("a.txt"), "a", { ctrlKey: true });
    expect(selectedNames()).toHaveLength(4);
    expect(mounted!.container.textContent).toContain("已选 4 项");
    keyDown(row("a.txt"), "Escape");
    expect(selectedNames()).toEqual([]);
  });

  it("shows a batch menu on a multi-selected row and selects only the row otherwise", async () => {
    await mountReady();
    click(row("a.txt"));
    clickWith(row("b.txt"), { ctrlKey: true });
    contextMenu(row("b.txt"));
    expect(menuLabels().join("|")).toContain("下载 2 个文件");
    expect(menuLabels().join("|")).toContain("删除 2 项…");
    expect(menuLabels().join("|")).toContain("取消选择");
    expect(menuItem("下载 2 个文件").disabled).toBe(false);

    click(menuItem("取消选择"));
    expect(selectedNames()).toEqual([]);
    click(row("a.txt"));
    clickWith(row("sub"), { ctrlKey: true });
    contextMenu(row("sub"));
    expect(menuItem("下载 2 个文件").disabled).toBe(true);
    expect(menuItem("下载 2 个文件").textContent).toContain("目录请单独打包下载");

    contextMenu(row("c.txt"));
    expect(selectedNames()).toEqual(["c.txt"]);
    expect(menuLabels().join("|")).toContain("下载当前文件");
  });

  it("deletes the batch after one confirmation and summarises partial failure", async () => {
    mocks.remove.mockImplementation((_s: string, p: string) =>
      p === "~/b.txt" ? Promise.reject(new Error("permission denied")) : Promise.resolve(undefined),
    );
    await mountReady();
    click(row("sub"));
    clickWith(row("c.txt"), { shiftKey: true });
    keyDown(row("a.txt"), "Delete");
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.ask.mock.calls[0][0]).toContain("4 项");
    expect(mocks.ask.mock.calls[0][0]).toContain("递归删除");
    expect(mocks.remove.mock.calls.map((c) => [c[1], c[2]])).toEqual([
      ["~/sub", true],
      ["~/a.txt", false],
      ["~/b.txt", false],
      ["~/c.txt", false],
    ]);
    const toast = mocks.toast.mock.calls.find((c) => c[0] === "error");
    expect(toast?.[1]).toContain("已删除 3 项");
    expect(toast?.[1]).toContain("b.txt");
    expect(toast?.[1]).toContain("permission denied");
    expect(selectedNames()).toEqual(["b.txt"]);
  });

  it("forgets selected rows that are gone after a refresh", async () => {
    await mountReady();
    click(row("a.txt"));
    clickWith(row("b.txt"), { ctrlKey: true });
    expect(selectedNames()).toEqual(["a.txt", "b.txt"]);
    mocks.list.mockImplementation((_s: string, p: string) =>
      Promise.resolve(p === "~" ? ENTRIES.filter((e) => e.name !== "a.txt").map((e) => ({ ...e })) : []),
    );
    click(mounted!.container.querySelector<HTMLButtonElement>('button[aria-label="刷新文件列表"]')!);
    await waitForRendered(() => expect(mounted!.container.textContent).not.toContain("a.txt"));
    expect(selectedNames()).toEqual(["b.txt"]);
  });

  it("does not delete when the confirmation is declined", async () => {
    mocks.ask.mockResolvedValue(false);
    await mountReady();
    click(row("a.txt"));
    clickWith(row("b.txt"), { shiftKey: true });
    keyDown(row("a.txt"), "Delete");
    await flush();
    expect(mocks.remove).not.toHaveBeenCalled();
    expect(selectedNames()).toEqual(["a.txt", "b.txt"]);
  });

  it("downloads each selected file in order and reports one summary", async () => {
    await mountReady();
    click(row("a.txt"));
    clickWith(row("c.txt"), { shiftKey: true });
    contextMenu(row("b.txt"));
    click(menuItem("下载 3 个文件"));
    await flush();
    expect(mocks.enqueueDownload.mock.calls.map((c) => c[1].remotePath)).toEqual([
      "~/a.txt",
      "~/b.txt",
      "~/c.txt",
    ]);
    expect(mocks.toast).toHaveBeenCalledWith("success", "已加入 3 个下载任务");
  });
});

describe("FileTree touch multi-select", () => {
  it("enters multi-select on long press, toggles rows with checkboxes and exits on cancel", async () => {
    stubPointer(true);
    await mountReady();
    vi.useFakeTimers();
    pointer(row("a.txt"), "pointerdown");
    act(() => {
      vi.advanceTimersByTime(500);
    });
    pointer(row("a.txt"), "pointerup");
    vi.useRealTimers();
    expect(mounted!.container.textContent).toContain("已选 1 项");
    expect(row("a.txt").querySelector('input[type="checkbox"]')).not.toBeNull();
    click(row("a.txt")); // swallowed: the click that ends the long press
    expect(selectedNames()).toEqual(["a.txt"]);
    click(row("b.txt"));
    expect(selectedNames()).toEqual(["a.txt", "b.txt"]);
    expect(mounted!.container.textContent).toContain("已选 2 项");
    click(row("sub"));
    expect(mocks.list).not.toHaveBeenCalledWith(SID, "~/sub");
    expect(selectedNames()).toEqual(["sub", "a.txt", "b.txt"]);

    const bar = mounted!.container.querySelector('[aria-label="多选操作"]')!;
    const barBtn = (t: string) =>
      [...bar.querySelectorAll("button")].find((b) => b.textContent?.trim() === t) as HTMLButtonElement;
    expect(barBtn("下载").disabled).toBe(true);
    click(barBtn("全选"));
    expect(selectedNames()).toHaveLength(4);
    click(barBtn("取消"));
    expect(selectedNames()).toEqual([]);
    expect(mounted!.container.querySelector('[aria-label="多选操作"]')).toBeNull();
  });

  it("cancels the long press when the finger moves and ignores contextmenu", async () => {
    stubPointer(true);
    await mountReady();
    vi.useFakeTimers();
    pointer(row("a.txt"), "pointerdown", 5, 5);
    pointer(row("a.txt"), "pointermove", 5, 30);
    act(() => {
      vi.advanceTimersByTime(800);
    });
    vi.useRealTimers();
    expect(mounted!.container.querySelector('[aria-label="多选操作"]')).toBeNull();
    contextMenu(row("a.txt"));
    expect(document.querySelector('[role="menuitem"]')).toBeNull();
  });

  it("deletes the checked rows from the selection bar", async () => {
    stubPointer(true);
    await mountReady();
    vi.useFakeTimers();
    pointer(row("a.txt"), "pointerdown");
    act(() => {
      vi.advanceTimersByTime(500);
    });
    vi.useRealTimers();
    click(row("a.txt"));
    click(row("b.txt"));
    const bar = mounted!.container.querySelector('[aria-label="多选操作"]')!;
    click([...bar.querySelectorAll("button")].find((b) => b.textContent?.trim() === "删除")!);
    await flush();
    expect(mocks.ask.mock.calls[0][0]).toContain("2 项");
    expect(mocks.remove).toHaveBeenCalledTimes(2);
    expect(mocks.toast).toHaveBeenCalledWith("success", "已删除 2 项");
  });
});
