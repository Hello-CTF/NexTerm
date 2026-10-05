/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  fsList: vi.fn(),
  listenEvent: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return {
    ...actual,
    promptText: vi.fn(),
    pickLocalFile: vi.fn(),
    pickSavePath: vi.fn(),
    finishSave: vi.fn(),
    discardStaged: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    fsApi: {
      list: mocks.fsList,
      read: vi.fn(),
      write: vi.fn(),
      rename: vi.fn(),
      chmod: vi.fn(),
      checksum: vi.fn(),
      mkdir: vi.fn(),
      delete: vi.fn(),
      upload: vi.fn(),
      download: vi.fn(),
      packDownload: vi.fn(),
      extract: vi.fn(),
    },
    terminalApi: { write: vi.fn() },
    sessionApi: { probe: vi.fn(), list: vi.fn().mockResolvedValue([]) },
  };
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return { ...actual, listenEvent: mocks.listenEvent };
});

vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, i) => ({ index: i, start: i * 30, size: 30, key: i })),
    getTotalSize: () => count * 30,
  }),
}));

import { FileBrowser } from "../../features/files/FileBrowser";
import { FileTree } from "../../features/files/FileTree";
import { useUi } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";

const HOME_ENTRIES: FileEntryDto[] = [
  {
    name: "logs",
    path: "~/logs",
    kind: "dir",
    size: 0,
    mode: "drwxr-xr-x",
    owner: "root",
    group: "root",
    mtime: 1,
    symlinkTarget: null,
  },
  {
    name: "notes.txt",
    path: "~/notes.txt",
    kind: "file",
    size: 12,
    mode: "-rw-r--r--",
    owner: "root",
    group: "root",
    mtime: 1,
    symlinkTarget: null,
  },
];

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

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function rowByName(container: HTMLElement, name: string): HTMLElement {
  const row = [...container.querySelectorAll<HTMLElement>('[role="treeitem"]')].find((r) =>
    r.textContent?.includes(name),
  );
  if (!row) throw new Error(`row not found: ${name}`);
  return row;
}

function menuLabels(): string[] {
  return [...document.querySelectorAll('[role="menuitem"]')].map(
    (b) => b.querySelector(".nx-menu-label")?.textContent ?? b.textContent ?? "",
  );
}

function openRowMore(row: HTMLElement): void {
  const more = row.querySelector<HTMLButtonElement>(".nx-row-more");
  if (!more) throw new Error("overflow button not found");
  click(more);
}

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.resetAllMocks();
  document.body.replaceChildren();
  mocks.fsList.mockResolvedValue(HOME_ENTRIES.map((e) => ({ ...e })));
  mocks.listenEvent.mockResolvedValue(() => {});
  useUi.setState({ leftOpen: true, pushToast: vi.fn() });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("FileTree row actions", () => {
  it("keeps the hover action strip on fine pointers", async () => {
    stubPointer(false);
    mounted = mountWithClient(createElement(FileTree, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));

    const row = rowByName(mounted!.container, "notes.txt");
    const labels = [...row.querySelectorAll<HTMLButtonElement>(".nx-row-actions button")].map(
      (b) => b.getAttribute("aria-label"),
    );
    expect(labels).toEqual(["下载 notes.txt", "删除 notes.txt", "更多操作 notes.txt"]);
    expect(row.querySelector(".nx-row-more")).toBeNull();
    expect(row.querySelector(".nx-row-name")?.textContent).toBe("notes.txt");
  });

  it("collapses row actions into one overflow button on coarse pointers", async () => {
    stubPointer(true);
    mounted = mountWithClient(createElement(FileTree, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));

    const row = rowByName(mounted!.container, "notes.txt");
    expect(row.querySelector(".nx-row-actions")).toBeNull();
    expect(row.querySelector(".nx-row-more")?.getAttribute("aria-label")).toBe(
      "更多操作 notes.txt",
    );
    expect(row.querySelector(".nx-row-name")?.textContent).toBe("notes.txt");
  });

  it("reaches every file action through the coarse overflow menu", async () => {
    stubPointer(true);
    mounted = mountWithClient(createElement(FileTree, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));

    openRowMore(rowByName(mounted!.container, "notes.txt"));
    await flush();
    const labels = menuLabels();
    for (const label of ["在下方编辑", "下载当前文件", "重命名", "权限…", "校验值…", "删除"]) {
      expect(labels).toContain(label);
    }

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    await flush();
    expect(document.querySelector('[role="menu"]')).toBeNull();
  });

  it("does not leak double-clicks from the overflow trigger into openFile/enterDir", async () => {
    stubPointer(true);
    mounted = mountWithClient(createElement(FileTree, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));

    const tabsBefore = (useUi.getState().workspaces[0]?.panes[0]?.tabs ?? []).length;
    const callsBefore = mocks.fsList.mock.calls.length;

    const fileMore = rowByName(mounted!.container, "notes.txt").querySelector<HTMLButtonElement>(".nx-row-more");
    if (!fileMore) throw new Error("overflow button not found");
    act(() => {
      fileMore.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    });
    await flush();
    expect((useUi.getState().workspaces[0]?.panes[0]?.tabs ?? []).length).toBe(tabsBefore);

    const dirMore = rowByName(mounted!.container, "logs").querySelector<HTMLButtonElement>(".nx-row-more");
    if (!dirMore) throw new Error("overflow button not found");
    act(() => {
      dirMore.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    });
    await flush();
    const dirCalls = mocks.fsList.mock.calls.slice(callsBefore).map((call) => call[1]);
    expect(dirCalls).not.toContain("~/logs");
  });

  it("offers directory actions without file-only entries on coarse pointers", async () => {
    stubPointer(true);
    mounted = mountWithClient(createElement(FileTree, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("logs"));

    openRowMore(rowByName(mounted!.container, "logs"));
    await flush();
    const labels = menuLabels();
    expect(labels).toContain("打包下载当前文件夹");
    expect(labels).toContain("删除");
    expect(labels).not.toContain("下载当前文件");
    expect(labels).not.toContain("校验值…");
  });
});

describe("FileBrowser row actions", () => {
  it("keeps the hover overflow button and truncatable columns on fine pointers", async () => {
    stubPointer(false);
    mounted = mountWithClient(createElement(FileBrowser, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));

    const row = [...mounted!.container.querySelectorAll<HTMLElement>(".cursor-pointer")].find((r) =>
      r.textContent?.includes("notes.txt"),
    );
    if (!row) throw new Error("browser row not found");
    expect(row.querySelector(".nx-row-actions button")?.getAttribute("aria-label")).toBe(
      "更多操作 notes.txt",
    );
    expect(row.querySelector(".nx-row-more")).toBeNull();
    const name = row.querySelector(".nx-row-name");
    expect(name?.textContent).toBe("notes.txt");
    expect(name?.className).toContain("flex-auto");
    const cells = [...row.querySelectorAll<HTMLElement>("span")].filter((s) =>
      s.className.includes("text-right"),
    );
    expect(cells.length).toBeGreaterThanOrEqual(2);
    for (const cell of cells) {
      expect(cell.className).toContain("truncate");
      expect(cell.className).toContain("shrink");
    }
  });

  it("shows a persistent overflow button on coarse pointers", async () => {
    stubPointer(true);
    mounted = mountWithClient(createElement(FileBrowser, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));

    const row = [...mounted!.container.querySelectorAll<HTMLElement>(".cursor-pointer")].find((r) =>
      r.textContent?.includes("notes.txt"),
    );
    if (!row) throw new Error("browser row not found");
    expect(row.querySelector(".nx-row-actions")).toBeNull();
    const more = row.querySelector<HTMLButtonElement>(".nx-row-more");
    expect(more?.getAttribute("aria-label")).toBe("更多操作 notes.txt");

    click(more as HTMLButtonElement);
    await flush();
    const labels = menuLabels();
    for (const label of ["在下方编辑", "下载当前文件", "复制路径", "重命名", "权限…", "校验值…", "删除"]) {
      expect(labels).toContain(label);
    }
  });
});
