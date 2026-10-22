/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  deferred,
  flush,
  flushUntil,
  mount,
  setInputValue,
  setSelectValue,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  fsList: vi.fn(),
  readRange: vi.fn(),
  assetList: vi.fn(),
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
      readRange: mocks.readRange,
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
    assetApi: { list: mocks.assetList },
  };
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return { ...actual, listenEvent: mocks.listenEvent };
});

import { LogViewer } from "../../features/files/LogViewer";
import { FileBrowser } from "../../features/files/FileBrowser";
import { FileTree } from "../../features/files/FileTree";
import { useUi } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";

const CHUNK = 262144;
const SESSION = "s1";
const LOG_PATH = "/var/log/app.log";

const FILE_LINES = Array.from({ length: 700 }, (_, i) =>
  i % 10 === 0 ? `line ${i} ERROR boom ${"x".repeat(800)}` : `line ${i} ok ${"y".repeat(800)}`,
);
const FILE_TEXT = `${FILE_LINES.join("\n")}\n`;
const FILE_BYTES = new TextEncoder().encode(FILE_TEXT);
const PAGES = Math.ceil(FILE_BYTES.length / CHUNK);

function toBase64(bytes: Uint8Array): string {
  let binary = "";
  for (let i = 0; i < bytes.length; i += 8192) {
    binary += String.fromCharCode(...bytes.subarray(i, i + 8192));
  }
  return btoa(binary);
}

function rangeResult(offset: number, maxBytes: number) {
  const slice = FILE_BYTES.subarray(offset, offset + maxBytes);
  return {
    path: LOG_PATH,
    offset,
    size: FILE_BYTES.length,
    contentBase64: toBase64(slice),
    truncated: offset + slice.length < FILE_BYTES.length,
  };
}

const GBK_BYTES = new Uint8Array([0x45, 0x52, 0x52, 0x4f, 0x52, 0x20, 0xd6, 0xd0, 0xce, 0xc4, 0x0a]);

function gbkRangeResult() {
  return {
    path: LOG_PATH,
    offset: 0,
    size: GBK_BYTES.length,
    contentBase64: toBase64(GBK_BYTES),
    truncated: false,
  };
}

const HOME_ENTRIES: FileEntryDto[] = [
  {
    name: "app.log",
    path: "~/app.log",
    kind: "file",
    size: FILE_BYTES.length,
    mode: "-rw-r--r--",
    owner: "root",
    group: "root",
    mtime: 1,
    symlinkTarget: null,
  },
];

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function inputByLabel(container: HTMLElement, label: string): HTMLInputElement {
  const input = container.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`);
  if (!input) throw new Error(`input not found: ${label}`);
  return input;
}

async function waitForText(view: MountedView, text: string): Promise<void> {
  await flushUntil(() => (view.container.textContent ?? "").includes(text));
}

async function waitForSettled(view: MountedView, offset: number): Promise<void> {
  const marker = `字节 ${offset.toLocaleString()}–`;
  await flushUntil(() => (view.container.textContent ?? "").includes(marker));
}

function toolbarButton(container: HTMLElement, title: string): HTMLButtonElement {
  const button = container.querySelector<HTMLButtonElement>(`button[title="${title}"]`);
  if (!button) throw new Error(`button not found: ${title}`);
  return button;
}

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.resetAllMocks();
  document.body.replaceChildren();
  mocks.fsList.mockResolvedValue(HOME_ENTRIES.map((e) => ({ ...e })));
  mocks.listenEvent.mockResolvedValue(() => {});
  mocks.assetList.mockResolvedValue([]);
  mocks.readRange.mockImplementation(async (_s: string, _p: string, offset: number, maxBytes?: number) =>
    rangeResult(offset, maxBytes ?? CHUNK),
  );
  // Drive the real @tanstack/react-virtual: jsdom has no ResizeObserver and no
  // layout, so give every element a fixed viewport height; the virtualizer then
  // computes a bounded window (viewport/estimateSize + overscan) instead of
  // rendering every row.
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    unobserve() {}
    disconnect() {}
  });
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get: () => 400,
  });
  useUi.setState({ leftOpen: true, pushToast: vi.fn(), sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
  delete (HTMLElement.prototype as unknown as Record<string, unknown>).offsetHeight;
});

describe("LogViewer", () => {
  it("loads the first chunk instead of the whole file", async () => {
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await waitForText(mounted, "line 0 ERROR boom");

    expect(mocks.readRange).toHaveBeenCalledTimes(1);
    expect(mocks.readRange).toHaveBeenCalledWith(SESSION, LOG_PATH, 0, CHUNK);
    const text = mounted!.container.textContent ?? "";
    expect(text).toContain(`分块 1 / ${PAGES}`);
    expect(text).toContain("只读");
    expect(text).toContain("搜索仅过滤当前已加载分块");
    expect(text).not.toContain(`line ${FILE_LINES.length - 1}`);
  });

  it("pages forward, backward, and jumps to an explicit page", async () => {
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await waitForText(mounted, "line 0 ERROR boom");

    click(toolbarButton(mounted!.container, "下一页"));
    await waitForSettled(mounted, CHUNK);
    expect(mocks.readRange).toHaveBeenLastCalledWith(SESSION, LOG_PATH, CHUNK, CHUNK);
    expect(mounted!.container.textContent).toContain(`分块 2 / ${PAGES}`);

    click(toolbarButton(mounted!.container, "最后一页（日志末尾）"));
    const lastOffset = (PAGES - 1) * CHUNK;
    await waitForSettled(mounted, lastOffset);
    expect(mocks.readRange).toHaveBeenLastCalledWith(SESSION, LOG_PATH, lastOffset, CHUNK);
    expect(mounted!.container.textContent).toContain(`分块 ${PAGES} / ${PAGES}`);

    const pageInput = inputByLabel(mounted!.container, "页码");
    setInputValue(pageInput, "1");
    const form = pageInput.closest("form");
    if (!form) throw new Error("page form not found");
    act(() => form.requestSubmit());
    await waitForSettled(mounted, 0);
    expect(mocks.readRange).toHaveBeenLastCalledWith(SESSION, LOG_PATH, 0, CHUNK);

    click(toolbarButton(mounted!.container, "下一页"));
    await waitForSettled(mounted, CHUNK);
    click(toolbarButton(mounted!.container, "上一页"));
    await waitForSettled(mounted, 0);
    expect(mounted!.container.textContent).toContain("分块 1 /");
  }, 15000);

  it("filters the loaded chunk client-side and highlights matches", async () => {
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await waitForText(mounted, "line 0 ERROR boom");

    const search = mounted!.container.querySelector<HTMLInputElement>(
      'input[placeholder="过滤当前分块…"]',
    );
    if (!search) throw new Error("search input not found");
    setInputValue(search, "error boom");
    await flush();

    const chunkLines = FILE_TEXT.slice(0, CHUNK).split("\n");
    const expected = chunkLines.filter((l) => l.toLowerCase().includes("error boom")).length;
    expect(mounted!.container.textContent).toContain(`${expected} 行匹配`);
    const rows = [...mounted!.container.querySelectorAll('[role="log"] > div > div')];
    expect(rows.length).toBe(expected);
    for (const row of rows) {
      expect(row.textContent?.toLowerCase()).toContain("error boom");
    }
    expect(mounted!.container.querySelector("mark")).not.toBeNull();

    setInputValue(search, "");
    await flush();
    expect(mounted!.container.querySelectorAll('[role="log"] > div > div').length).toBeGreaterThan(
      expected,
    );
  });

  it("renders and filters 20k lines inside one production chunk through a bounded virtual window", async () => {
    // Fixed-width lines keep the byte total exact: 2,000 ERROR lines of 22
    // bytes and 18,000 plain lines of 11 bytes (newline included) sum to
    // 242,000 bytes, which fits inside one production CHUNK of 262,144. The
    // loaded chunk therefore holds every one of the 20,000 lines — nothing
    // hides behind pagination — so the match count and line numbers asserted
    // below cover the whole file, not a first-chunk prefix.
    const largeLines = Array.from({ length: 20000 }, (_, i) => {
      const n = String(i).padStart(5, "0");
      return i % 10 === 0 ? `line ${n} ERROR boom` : `line ${n}`;
    });
    const largeText = `${largeLines.join("\n")}\n`;
    const largeBytes = new TextEncoder().encode(largeText);
    expect(largeBytes.length).toBe(242_000);
    expect(largeBytes.length).toBeLessThanOrEqual(CHUNK);
    const totalRows = largeLines.length + 1; // split("\n") keeps a trailing empty row
    mocks.readRange.mockImplementation(async (_s: string, _p: string, offset: number, maxBytes?: number) => {
      const slice = largeBytes.subarray(offset, offset + (maxBytes ?? CHUNK));
      return {
        path: LOG_PATH,
        offset,
        size: largeBytes.length,
        contentBase64: toBase64(slice),
        truncated: offset + slice.length < largeBytes.length,
      };
    });
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await waitForText(mounted, "line 00000 ERROR boom");

    // One 256 KiB range read covers the whole file: a single chunk page
    // holds all 20,000 lines.
    expect(mocks.readRange).toHaveBeenCalledTimes(1);
    expect(mocks.readRange).toHaveBeenCalledWith(SESSION, LOG_PATH, 0, CHUNK);
    expect(mounted!.container.textContent).toContain("分块 1 / 1");

    const rowNumbers = () =>
      [...mounted!.container.querySelectorAll('[role="log"] > div > div')].map(
        (row) => Number(row.querySelector("span")?.textContent ?? 0),
      );
    const scrollEl = mounted!.container.querySelector<HTMLElement>('[role="log"]')!;
    const scrollTo = (top: number) => {
      act(() => {
        scrollEl.scrollTop = top;
        scrollEl.dispatchEvent(new Event("scroll"));
      });
    };

    // The window is bounded: only a small fraction of the 20,001 rows render.
    const topNumbers = rowNumbers();
    expect(topNumbers.length).toBeGreaterThan(0);
    expect(topNumbers.length).toBeLessThan(100);
    expect(topNumbers[0]).toBe(1);

    // Scrolling to an offset renders the rows at that offset.
    scrollTo(3000);
    await flush();
    expect(rowNumbers()[0]).toBeGreaterThan(100);

    // Scrolling far past the end clamps to the deepest rows: the last row of
    // the single chunk is the 20,001st (the trailing empty row), proving
    // every line is inside the loaded chunk, and the window stays bounded.
    scrollTo(1_000_000);
    await flushUntil(() => rowNumbers().at(-1) === totalRows);
    const deepNumbers = rowNumbers();
    expect(deepNumbers[0]).toBeGreaterThan(19_000);
    expect(deepNumbers.length).toBeLessThan(100);

    // Back at the top, filtering narrows the window to matching rows with
    // the full match count: every 10th of the 20,000 lines matches, so
    // exactly 2,000 matches across the whole chunk.
    scrollTo(0);
    await flush();
    const search = mounted!.container.querySelector<HTMLInputElement>(
      'input[placeholder="过滤当前分块…"]',
    );
    if (!search) throw new Error("search input not found");
    setInputValue(search, "error boom");
    await flush();
    expect(mounted!.container.textContent).toContain("2000 行匹配");
    const matchedRows = [...mounted!.container.querySelectorAll('[role="log"] > div > div')];
    expect(matchedRows.length).toBeGreaterThan(0);
    expect(matchedRows.length).toBeLessThan(100);
    for (const row of matchedRows) {
      expect(row.textContent?.toLowerCase()).toContain("error boom");
      // The production row label is the original line number {index + 1}, so
      // every visible match keeps its pre-filter numbering (1, 11, 21, ...).
      expect(Number(row.querySelector("span")?.textContent) % 10).toBe(1);
    }
    expect(Number(matchedRows[0].querySelector("span")?.textContent)).toBe(1);

    // Deep in the filtered list the visible rows still carry the original
    // line numbers, down to the last match at line 19,991.
    scrollTo(1_000_000);
    await flushUntil(() => rowNumbers().at(-1) === 19_991);
    const deepMatched = rowNumbers();
    expect(deepMatched[0]).toBeGreaterThan(15_000);
    expect(deepMatched.length).toBeLessThan(100);
    for (const n of deepMatched) {
      expect(n % 10).toBe(1);
    }
  }, 15000);

  it("refreshes the current chunk in place", async () => {
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await waitForText(mounted, "line 0 ERROR boom");

    click(toolbarButton(mounted!.container, "下一页"));
    await waitForSettled(mounted, CHUNK);

    click(toolbarButton(mounted!.container, "重新读取当前分块（日志文件可能已增长）"));
    await flush();
    await flushUntil(() => mocks.readRange.mock.calls.length === 3);
    expect(mocks.readRange).toHaveBeenLastCalledWith(SESSION, LOG_PATH, CHUNK, CHUNK);
    await waitForSettled(mounted, CHUNK);
    expect(mounted!.container.textContent).toContain(`分块 2 / ${PAGES}`);
  });

  it("honestly reports unsupported backends", async () => {
    mocks.readRange.mockRejectedValue({
      code: "unsupported",
      message: "transport capability unsupported",
    });
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await waitForText(mounted, "当前连接不支持远程日志查看");
    expect(mounted!.container.textContent).toContain("请使用内置编辑器或下载到本机查看");
  });

  it("offers retry on ordinary failures", async () => {
    mocks.readRange.mockRejectedValueOnce({ code: "sftp", message: "SFTP read failed" });
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await waitForText(mounted, "SFTP read failed");

    click(mounted!.container.querySelector("button.nx-link") as HTMLButtonElement);
    await waitForText(mounted, "line 0 ERROR boom");
  });

  it("decodes GBK through the encoding selector to avoid mojibake", async () => {
    mocks.readRange.mockImplementation(async () => gbkRangeResult());
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("�"));

    const select = mounted!.container.querySelector<HTMLSelectElement>(
      'select[aria-label="日志编码"]',
    );
    if (!select) throw new Error("encoding select not found");
    expect(select.value).toBe("utf-8");

    setSelectValue(select, "gbk");
    await waitForText(mounted, "ERROR 中文");
    expect(select.value).toBe("gbk");
  });

  it("uses the asset terminal encoding setting by default when decoding logs", async () => {
    mocks.assetList.mockResolvedValue([{ id: "a-gbk", options: { encoding: "gbk" } }]);
    useUi.setState({
      sessions: [
        {
          id: SESSION,
          assetId: "a-gbk",
          name: "web-01",
          kind: "ssh",
          status: "connected",
          tabs: [],
          createdAt: 0,
        },
      ],
    });
    mocks.readRange.mockImplementation(async () => gbkRangeResult());
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await waitForText(mounted, "ERROR 中文");

    const select = mounted!.container.querySelector<HTMLSelectElement>(
      'select[aria-label="日志编码"]',
    );
    expect(select?.value).toBe("gbk");
  });

  it("does not let an older page response replace the latest page", async () => {
    const previous = deferred<ReturnType<typeof rangeResult>>();
    const latest = deferred<ReturnType<typeof rangeResult>>();
    mocks.readRange.mockImplementation(
      async (_s: string, _p: string, offset: number, maxBytes?: number) => {
        if (offset === CHUNK) return previous.promise;
        if (offset === CHUNK * 2) return latest.promise;
        return rangeResult(offset, maxBytes ?? CHUNK);
      },
    );
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await waitForText(mounted, "line 0 ERROR boom");

    click(toolbarButton(mounted.container, "下一页"));
    await flushUntil(() => mocks.readRange.mock.calls.length === 2);
    const pageInput = inputByLabel(mounted.container, "页码");
    setInputValue(pageInput, "3");
    act(() => pageInput.closest("form")?.requestSubmit());
    await flushUntil(() => mocks.readRange.mock.calls.length === 3);

    latest.resolve(rangeResult(CHUNK * 2, CHUNK));
    await waitForSettled(mounted, CHUNK * 2);
    previous.resolve(rangeResult(CHUNK, CHUNK));
    await flush();

    expect(mounted.container.textContent).toContain(`字节 ${(CHUNK * 2).toLocaleString()}–`);
    expect(mounted.container.textContent).not.toContain(`字节 ${CHUNK.toLocaleString()}–`);
  });

  it("does not let an older encoding response replace the latest response and decodes with the encoding used at request time", async () => {
    const stale = deferred<ReturnType<typeof gbkRangeResult>>();
    const current = deferred<ReturnType<typeof gbkRangeResult>>();
    let request = 0;
    mocks.readRange.mockImplementation(async () => {
      request += 1;
      if (request === 1) return gbkRangeResult();
      if (request === 2) return stale.promise;
      return current.promise;
    });
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("�"));

    click(toolbarButton(mounted.container, "重新读取当前分块（日志文件可能已增长）"));
    await flushUntil(() => mocks.readRange.mock.calls.length === 2);
    const select = mounted.container.querySelector<HTMLSelectElement>(
      'select[aria-label="日志编码"]',
    );
    if (!select) throw new Error("encoding select not found");
    setSelectValue(select, "gbk");
    await flushUntil(() => mocks.readRange.mock.calls.length === 3);

    current.resolve(gbkRangeResult());
    await waitForText(mounted, "ERROR 中文");
    stale.resolve({
      ...gbkRangeResult(),
      size: 5,
      contentBase64: toBase64(new TextEncoder().encode("stale")),
    });
    await flush();

    expect(mounted.container.textContent).toContain("ERROR 中文");
    expect(mounted.container.textContent).not.toContain("stale");
  });
});

describe("Log viewer entries", () => {
  it("FileTree context menu opens a read-only log tab", async () => {
    mounted = mountWithClient(createElement(FileTree, { sessionId: SESSION }));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("app.log"));

    const row = [...mounted!.container.querySelectorAll<HTMLElement>('[role="treeitem"]')].find(
      (r) => r.textContent?.includes("app.log"),
    );
    if (!row) throw new Error("row not found");
    act(() => {
      row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    });
    await flush();

    const item = [...document.querySelectorAll('[role="menuitem"]')].find((el) =>
      el.textContent?.includes("查看日志"),
    );
    expect(item).toBeDefined();
    expect(item?.textContent).toContain("只读分块");
    click(item as Element);
    await flush();

    const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
    const logTab = tabs.find((t) => t.kind === "log");
    expect(logTab).toMatchObject({ sessionId: SESSION, path: "~/app.log" });
  });

  it("FileBrowser toolbar opens the log viewer for the selected file", async () => {
    mounted = mountWithClient(createElement(FileBrowser, { sessionId: SESSION }));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("app.log"));

    const row = [...mounted!.container.querySelectorAll<HTMLElement>(".cursor-pointer")].find((r) =>
      r.textContent?.includes("app.log"),
    );
    if (!row) throw new Error("browser row not found");
    click(row);

    const button = [...mounted!.container.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent?.trim() === "日志",
    );
    if (!button) throw new Error("toolbar log button not found");
    expect(button.disabled).toBe(false);
    click(button);
    await flush();

    const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
    expect(tabs.find((t) => t.kind === "log")).toMatchObject({
      sessionId: SESSION,
      path: "~/app.log",
    });
  });

  it("FileBrowser context menu offers the log viewer for files", async () => {
    mounted = mountWithClient(createElement(FileBrowser, { sessionId: SESSION }));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("app.log"));

    const row = [...mounted!.container.querySelectorAll<HTMLElement>(".cursor-pointer")].find((r) =>
      r.textContent?.includes("app.log"),
    );
    if (!row) throw new Error("browser row not found");
    act(() => {
      row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    });
    await flush();

    const labels = [...document.querySelectorAll('[role="menuitem"]')].map(
      (b) => b.querySelector(".nx-menu-label")?.textContent ?? b.textContent ?? "",
    );
    expect(labels).toContain("查看日志");
    expect(labels).toContain("在下方编辑");
  });
});
