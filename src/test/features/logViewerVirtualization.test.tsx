/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, flushUntil, mount, setInputValue, waitFor, type MountedView } from "./reactTestUtils";

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

// NOTE: @tanstack/react-virtual is deliberately NOT mocked here. The viewport
// is made real by giving the scroll element a nonzero offsetHeight/offsetWidth.
import { LogViewer } from "../../features/files/LogViewer";
import { useUi } from "../../app/store";

const CHUNK = 262144;
const SESSION = "s1";
const LOG_PATH = "/var/log/app.log";
const VIEWPORT_HEIGHT = 600;
const ROW_HEIGHT = 18.6;

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

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function logScrollElement(view: MountedView): HTMLElement {
  const el = view.container.querySelector<HTMLElement>('[role="log"]');
  if (!el) throw new Error("log scroll element not found");
  return el;
}

function renderedRows(view: MountedView): HTMLElement[] {
  return [...view.container.querySelectorAll<HTMLElement>('[role="log"] > div')];
}

function rowIndex(row: HTMLElement): number {
  const text = row.querySelector("span")?.textContent ?? "";
  const value = Number.parseInt(text, 10);
  if (!Number.isFinite(value)) throw new Error(`row index not parseable: ${text}`);
  return value - 1;
}

async function waitForText(view: MountedView, text: string): Promise<void> {
  await flushUntil(() => (view.container.textContent ?? "").includes(text));
}

let mounted: MountedView | undefined;
let offsetHeightBackup: PropertyDescriptor | undefined;
let offsetWidthBackup: PropertyDescriptor | undefined;

beforeEach(() => {
  vi.resetAllMocks();
  document.body.replaceChildren();
  mocks.fsList.mockResolvedValue([]);
  mocks.listenEvent.mockResolvedValue(() => {});
  mocks.assetList.mockResolvedValue([]);
  mocks.readRange.mockImplementation(async (_s: string, _p: string, offset: number, maxBytes?: number) =>
    rangeResult(offset, maxBytes ?? CHUNK),
  );
  useUi.setState({ leftOpen: true, pushToast: vi.fn(), sessions: [] });

  offsetHeightBackup = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  offsetWidthBackup = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetWidth");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      if (this.getAttribute("role") === "log") return VIEWPORT_HEIGHT;
      return 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
    configurable: true,
    get(this: HTMLElement) {
      if (this.getAttribute("role") === "log") return 800;
      return 0;
    },
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  if (offsetHeightBackup) Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightBackup);
  if (offsetWidthBackup) Object.defineProperty(HTMLElement.prototype, "offsetWidth", offsetWidthBackup);
});

describe("LogViewer viewport virtualization (real virtualizer)", () => {
  it("renders only a small window of the first chunk with correct content", async () => {
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await waitForText(mounted, "line 0 ERROR boom");

    const rows = renderedRows(mounted);
    expect(rows.length).toBeGreaterThan(10);
    expect(rows.length).toBeLessThan(200);
    expect(rows.length).toBeLessThan(FILE_LINES.length / 100);

    const first = rows[0];
    expect(rowIndex(first)).toBe(0);
    expect(first.textContent).toContain("1");
    expect(first.textContent).toContain("line 0 ERROR boom");
    const lastIndex = rowIndex(rows[rows.length - 1]);
    expect(lastIndex).toBeLessThan(1000);
    expect(rows[rows.length - 1].textContent).toContain(`line ${lastIndex} `);
    expect(mounted.container.textContent).not.toContain(`line ${FILE_LINES.length - 1} `);
  }, 15000);

  it("renders the window around a scrolled position instead of the whole chunk", async () => {
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await waitForText(mounted, "line 0 ERROR boom");

    const scrollEl = logScrollElement(mounted);
    let scrollTop = 0;
    Object.defineProperty(scrollEl, "scrollTop", {
      configurable: true,
      get: () => scrollTop,
      set: (value: number) => {
        scrollTop = value;
      },
    });
    act(() => {
      scrollTop = ROW_HEIGHT * 5000;
      scrollEl.dispatchEvent(new Event("scroll"));
    });
    await flush();
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("line 5000 "));

    expect(mounted.container.textContent).not.toContain("line 0 ERROR boom");
    const rows = renderedRows(mounted);
    expect(rows.length).toBeGreaterThan(10);
    expect(rows.length).toBeLessThan(200);
    const indexes = rows.map(rowIndex);
    expect(Math.min(...indexes)).toBeLessThan(5000);
    expect(Math.max(...indexes)).toBeGreaterThan(5000);
    expect(indexes).toContain(5000);
    expect(mounted.container.textContent).not.toContain(`line ${FILE_LINES.length - 1} `);
  }, 15000);

  it("virtualizes filtered rows while keeping original line numbers", async () => {
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await waitForText(mounted, "line 0 ERROR boom");

    const search = mounted.container.querySelector<HTMLInputElement>(
      'input[placeholder="过滤当前分块…"]',
    );
    if (!search) throw new Error("search input not found");
    setInputValue(search, "error boom");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("行匹配"));

    const chunkLines = FILE_TEXT.slice(0, CHUNK).split("\n");
    const expected = chunkLines.filter((l) => l.toLowerCase().includes("error boom")).length;
    expect(mounted.container.textContent).toContain(`${expected} 行匹配`);

    const rows = renderedRows(mounted);
    expect(rows.length).toBeGreaterThan(10);
    expect(rows.length).toBeLessThan(200);
    expect(rows.length).toBeLessThan(expected);
    for (const row of rows) {
      expect(row.textContent?.toLowerCase()).toContain("error boom");
    }
    expect(rowIndex(rows[0])).toBe(0);
    expect(rows[0].textContent).toContain("line 0 ERROR boom");
    expect(mounted.container.querySelector("mark")).not.toBeNull();
  }, 15000);

  it("keeps the rendered window small after paging", async () => {
    mounted = mountWithClient(createElement(LogViewer, { sessionId: SESSION, path: LOG_PATH }));
    await waitForText(mounted, "line 0 ERROR boom");

    const next = [...mounted.container.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.title === "下一页",
    );
    if (!next) throw new Error("next page button not found");
    click(next);
    await waitFor(() =>
      expect(mocks.readRange).toHaveBeenLastCalledWith(SESSION, LOG_PATH, CHUNK, CHUNK),
    );
    await flushUntil(() => (mounted!.container.textContent ?? "").includes(`分块 2 / ${PAGES}`));

    const rows = renderedRows(mounted);
    expect(rows.length).toBeGreaterThan(0);
    expect(rows.length).toBeLessThan(200);
  }, 15000);
});
