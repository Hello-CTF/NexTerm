/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  snippetList: vi.fn(),
  probeBatch: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      list: mocks.list,
      snippetList: mocks.snippetList,
      probeBatch: mocks.probeBatch,
    },
    sessionApi: {},
    dbApi: {},
    vaultApi: {},
    terminalApi: {},
  };
});

import { CommandPalette } from "../../app/CommandPalette";

let mounted: MountedView | undefined;

function mountPalette(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const node: ReactNode = createElement(CommandPalette, {
    onClose: () => {},
    onQuickConnect: () => {},
  });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function errorRows(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>(".nx-command-error")];
}

function retryButtonIn(row: HTMLElement): HTMLButtonElement {
  const btn = row.querySelector<HTMLButtonElement>("button");
  if (!btn) throw new Error("retry button not found in error row");
  return btn;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  mocks.list.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.probeBatch.mockResolvedValue([]);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("命令面板资产与片段加载失败互不覆盖", () => {
  it("两个请求都失败时各自展示错误, 后到的失败不覆盖先到的", async () => {
    mocks.list.mockRejectedValue(new Error("资产 boom"));
    mocks.snippetList.mockRejectedValue(new Error("片段 boom"));
    mounted = mountPalette();
    await flushUntil(() => errorRows().length === 2);

    const rows = errorRows();
    expect(rows[0].textContent).toContain("资产列表加载失败：资产 boom");
    expect(rows[1].textContent).toContain("片段列表加载失败：片段 boom");
  });

  it("资产重试只重新拉取资产, 成功后只清资产错误", async () => {
    mocks.list.mockRejectedValueOnce(new Error("资产 boom"));
    mocks.snippetList.mockRejectedValue(new Error("片段 boom"));
    mounted = mountPalette();
    await flushUntil(() => errorRows().length === 2);

    mocks.list.mockResolvedValue([]);
    click(retryButtonIn(errorRows()[0]));
    await flushUntil(() => errorRows().length === 1);

    expect(mocks.list).toHaveBeenCalledTimes(2);
    expect(mocks.snippetList).toHaveBeenCalledTimes(1);
    const rows = errorRows();
    expect(rows[0].textContent).toContain("片段列表加载失败：片段 boom");
  });

  it("片段重试成功后片段错误消失, 资产错误仍在", async () => {
    mocks.list.mockRejectedValue(new Error("资产 boom"));
    mocks.snippetList.mockRejectedValueOnce(new Error("片段 boom"));
    mounted = mountPalette();
    await flushUntil(() => errorRows().length === 2);

    mocks.snippetList.mockResolvedValue([]);
    click(retryButtonIn(errorRows()[1]));
    await flushUntil(() => errorRows().length === 1);

    expect(mocks.snippetList).toHaveBeenCalledTimes(2);
    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(errorRows()[0].textContent).toContain("资产列表加载失败：资产 boom");
  });
});
