/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  flush,
  flushUntil,
  mount,
  setInputValue,
  setSelectValue,
  waitFor,
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

vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, i) => ({ index: i, start: i * 30, size: 30, key: i })),
    getTotalSize: () => count * 30,
  }),
}));

import { LogViewer } from "../../features/files/LogViewer";
import { FileBrowser } from "../../features/files/FileBrowser";
import { FileTree } from "../../features/files/FileTree";
import { useUi } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";

const CHUNK = 262144;
const SESSION = "s1";
const LOG_PATH = "/var/log/app.log";

const FILE_LINES = Array.from({ length: 20000 }, (_, i) =>
  i % 10 === 0 ? `line ${i} ERROR boom ${"x".repeat(20)}` : `line ${i} ok ${"y".repeat(20)}`,
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
  useUi.setState({ leftOpen: true, pushToast: vi.fn(), sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
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
    form.requestSubmit();
    await waitForSettled(mounted, 0);
    expect(mocks.readRange).toHaveBeenLastCalledWith(SESSION, LOG_PATH, 0, CHUNK);

    click(toolbarButton(mounted!.container, "下一页"));
    await waitForSettled(mounted, CHUNK);
    click(toolbarButton(mounted!.container, "上一页"));
    await waitForSettled(mounted, 0);
    expect(mounted!.container.textContent).toContain("分块 1 /");
  });

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
    const rows = [...mounted!.container.querySelectorAll('[role="log"] > div')];
    expect(rows.length).toBe(expected);
    for (const row of rows) {
      expect(row.textContent?.toLowerCase()).toContain("error boom");
    }
    expect(mounted!.container.querySelector("mark")).not.toBeNull();

    setInputValue(search, "");
    await flush();
    expect(mounted!.container.querySelectorAll('[role="log"] > div').length).toBeGreaterThan(
      expected,
    );
  });

  it("refreshes the current chunk in place", async () => {
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await waitForText(mounted, "line 0 ERROR boom");

    click(toolbarButton(mounted!.container, "下一页"));
    await waitForSettled(mounted, CHUNK);

    click(toolbarButton(mounted!.container, "重新读取当前分块（日志文件可能已增长）"));
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
    await waitForText(mounted, "当前连接的后端不支持远程日志查看");
    expect(mounted!.container.textContent).toContain("WinRM 与本地后端暂不支持");
  });

  it("offers retry on ordinary failures", async () => {
    mocks.readRange.mockRejectedValueOnce({ code: "sftp", message: "SFTP read failed" });
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await waitForText(mounted, "SFTP read failed");

    click(mounted!.container.querySelector("button.nx-link") as HTMLButtonElement);
    await waitForText(mounted, "line 0 ERROR boom");
  });

  it("可通过编码选择按 GBK 解码，避免乱码", async () => {
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

  it("默认复用资产的终端编码设置解码日志", async () => {
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
});

describe("查看日志 entries", () => {
  it("FileTree context menu opens a read-only log tab", async () => {
    mounted = mountWithClient(createElement(FileTree, { sessionId: SESSION }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("app.log"));

    const row = [...mounted!.container.querySelectorAll<HTMLElement>('[role="treeitem"]')].find(
      (r) => r.textContent?.includes("app.log"),
    );
    if (!row) throw new Error("row not found");
    row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
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
    await waitFor(() => expect(mounted!.container.textContent).toContain("app.log"));

    const row = [...mounted!.container.querySelectorAll<HTMLElement>(".cursor-pointer")].find((r) =>
      r.textContent?.includes("app.log"),
    );
    if (!row) throw new Error("browser row not found");
    click(row);

    const button = [...mounted!.container.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent?.trim() === "日志",
    );
    if (!button) throw new Error("toolbar 日志 button not found");
    expect(button.disabled).toBe(false);
    click(button);
    await flush();

    const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
    expect(tabs.find((t) => t.kind === "log")).toMatchObject({
      sessionId: SESSION,
      path: "~/app.log",
    });
  });

  it("FileBrowser context menu offers 查看日志 for files", async () => {
    mounted = mountWithClient(createElement(FileBrowser, { sessionId: SESSION }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("app.log"));

    const row = [...mounted!.container.querySelectorAll<HTMLElement>(".cursor-pointer")].find((r) =>
      r.textContent?.includes("app.log"),
    );
    if (!row) throw new Error("browser row not found");
    row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    await flush();

    const labels = [...document.querySelectorAll('[role="menuitem"]')].map(
      (b) => b.querySelector(".nx-menu-label")?.textContent ?? b.textContent ?? "",
    );
    expect(labels).toContain("查看日志");
    expect(labels).toContain("在下方编辑");
  });
});
