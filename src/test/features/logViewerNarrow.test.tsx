/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  readRange: vi.fn(),
  assetList: vi.fn(),
  copyText: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    fsApi: { readRange: mocks.readRange },
    assetApi: { list: mocks.assetList },
  };
});
vi.mock("../../ui/clipboard", () => ({ copyText: mocks.copyText }));

import { LogViewer } from "../../features/files/LogViewer";

const TEXT = "alpha\nbeta\ngamma\n";
const BYTES = new TextEncoder().encode(TEXT);

function setNarrow(narrow: boolean) {
  window.matchMedia = ((query: string) => ({
    matches: narrow && query.includes("max-width: 560px"),
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
}

let mounted: MountedView | undefined;
async function open(narrow: boolean): Promise<HTMLElement> {
  setNarrow(narrow);
  mounted = mount(
    createElement(QueryClientProvider, { client: new QueryClient() }, createElement(LogViewer, { sessionId: "s", path: "/a.log" })),
  );
  await flushUntil(() => (mounted!.container.textContent ?? "").includes("alpha"));
  return mounted.container;
}

beforeEach(() => {
  vi.resetAllMocks();
  mocks.assetList.mockResolvedValue([]);
  mocks.copyText.mockResolvedValue(undefined);
  mocks.readRange.mockResolvedValue({
    offset: 0,
    size: BYTES.length,
    truncated: false,
    contentBase64: btoa(TEXT),
  });
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, get: () => 400 });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
  delete (HTMLElement.prototype as unknown as Record<string, unknown>).offsetHeight;
});

describe("LogViewer on narrow screens", () => {
  it("keeps the search input reachable and wraps lines by default", async () => {
    const c = await open(true);
    const search = c.querySelector('input[placeholder="过滤当前分块…"]');
    expect(search).not.toBeNull();
    expect(search!.closest(".max-\\[560px\\]\\:hidden")).toBeNull();
    expect(c.querySelector('[role="log"]')!.getAttribute("data-wrap")).toBe("true");
    expect(c.querySelector('[role="log"] span.whitespace-pre-wrap')).not.toBeNull();
  });

  it("puts the pager in its own bottom row with the safe-area inset", async () => {
    const c = await open(true);
    const pager = c.querySelector<HTMLElement>('[data-testid="log-pager"]')!;
    expect(pager.className).toContain("--nx-safe-bottom");
    expect(pager.className).toContain("--nx-kb-inset");
    expect(c.querySelector('[data-testid="log-subbar"]')!.contains(pager)).toBe(false);
    expect(pager.closest(".nx-toolbar")).toBeNull();
  });

  it("toggles wrap off and copies the displayed lines", async () => {
    const c = await open(true);
    const toggle = c.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
    click(toggle);
    await flush();
    expect(c.querySelector('[role="log"]')!.getAttribute("data-wrap")).toBe("false");
    const copy = [...c.querySelectorAll("button")].find((b) => b.textContent === "复制日志")!;
    click(copy);
    await flush();
    expect(mocks.copyText).toHaveBeenCalledWith("alpha\nbeta\ngamma\n");
  });
});

describe("LogViewer on wide screens", () => {
  it("does not wrap by default and keeps the pager in the toolbar", async () => {
    const c = await open(false);
    expect(c.querySelector('[role="log"]')!.getAttribute("data-wrap")).toBe("false");
    expect(c.querySelector(".nx-toolbar")!.contains(c.querySelector('[data-testid="log-pager"]'))).toBe(true);
  });
});
