/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  flush,
  mount,
  setInputValue,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    list: vi.fn(),
    snippetList: vi.fn(),
    probeBatch: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: { list: mocks.list, snippetList: mocks.snippetList, probeBatch: mocks.probeBatch },
    sessionApi: {},
    dbApi: {},
    vaultApi: {},
    terminalApi: {},
  };
});

import { CommandPalette } from "../../app/CommandPalette";
import { useUi } from "../../app/store";
import { useAssetReachability } from "../../features/explorer/assetReachability";
import type { Asset } from "../../ipc/commands";

function assetOf(extra: Partial<Asset> & { id: string; name: string }): Asset {
  return {
    groupId: null,
    kind: "ssh",
    host: "10.0.0.8",
    port: 22,
    username: "root",
    authKind: "password",
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: 1,
    updatedAt: 1,
    deletedAt: null,
    builtin: false,
    ...extra,
  };
}

const WEB = assetOf({ id: "a-web", name: "web-01" });
const DB = assetOf({ id: "a-db", name: "db-01", kind: "mysql", host: "10.0.0.9", port: 3306 });

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function optionByText(container: HTMLElement, text: string): HTMLButtonElement {
  const option = [...container.querySelectorAll<HTMLButtonElement>('[role="option"]')].find(
    (o) => o.textContent?.includes(text),
  );
  if (!option) throw new Error(`Palette option not found: ${text}`);
  return option;
}

function searchInput(container: HTMLElement): HTMLInputElement {
  const input = container.querySelector<HTMLInputElement>('[role="combobox"]');
  if (!input) throw new Error("Palette combobox not found");
  return input;
}

const mountedViews: MountedView[] = [];

async function mountPalette(props: {
  onClose: () => void;
  onQuickConnect?: () => void;
}): Promise<MountedView> {
  const view = mountWithClient(createElement(CommandPalette, props));
  mountedViews.push(view);
  await flush();
  await waitFor(() => expect(optionByText(view.container, "连接 web-01")).toBeTruthy());
  return view;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  useAssetReachability.setState({ entries: {}, batchError: null });
  useUi.setState({
    sessions: [],
    workspaces: [],
    activeWorkspaceId: null,
    connectingAssetIds: [],
    pushToast: mocks.toast,
  });
  mocks.list.mockResolvedValue([{ ...WEB }, { ...DB }]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.probeBatch.mockResolvedValue({ results: [] });
});

afterEach(() => {
  for (const view of mountedViews.splice(0)) view.unmount();
  useAssetReachability.setState({ entries: {}, batchError: null });
});

describe("命令面板可达性与快速连接", () => {
  it("打开时对有主机的资产发起有界批量探测", async () => {
    await mountPalette({ onClose: vi.fn() });
    await waitFor(() => expect(mocks.probeBatch).toHaveBeenCalledTimes(1));
    const [ids, timeoutMs, maxConcurrent] = mocks.probeBatch.mock.calls[0] as [
      string[],
      number,
      number,
    ];
    expect(ids).toEqual(["a-web", "a-db"]);
    expect(timeoutMs).toBe(2500);
    expect(maxConcurrent).toBe(8);
  });

  it("连接条目展示可达性圆点", async () => {
    mocks.probeBatch.mockResolvedValue({
      results: [
        { assetId: "a-web", reachable: true, durationMs: 12 },
        { assetId: "a-db", reachable: false, error: "dial tcp 超时", durationMs: 2500 },
      ],
    });
    const view = await mountPalette({ onClose: vi.fn() });
    await waitFor(() => {
      const webRow = optionByText(view.container, "连接 web-01");
      expect(webRow.querySelector('[aria-label="可达"]')).toBeTruthy();
      const dbRow = optionByText(view.container, "连接 db-01");
      expect(dbRow.querySelector('[aria-label="不可达"]')).toBeTruthy();
    });
  });

  it("探测动作单独重探并 toast 结果", async () => {
    const view = await mountPalette({ onClose: vi.fn() });
    await waitFor(() => expect(mocks.probeBatch).toHaveBeenCalledTimes(1));
    mocks.probeBatch.mockResolvedValueOnce({
      results: [{ assetId: "a-web", reachable: true, durationMs: 31 }],
    });
    setInputValue(searchInput(view.container), "探测 web-01");
    click(optionByText(view.container, "探测 web-01"));
    await waitFor(() => expect(mocks.probeBatch).toHaveBeenCalledTimes(2));
    expect(mocks.probeBatch).toHaveBeenLastCalledWith(["a-web"], 2500, 8);
    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("31ms")),
    );
  });

  it("探测不可达时 toast 错误原因", async () => {
    const view = await mountPalette({ onClose: vi.fn() });
    await waitFor(() => expect(mocks.probeBatch).toHaveBeenCalledTimes(1));
    mocks.probeBatch.mockResolvedValueOnce({
      results: [{ assetId: "a-web", reachable: false, error: "connection refused", durationMs: 5 }],
    });
    setInputValue(searchInput(view.container), "探测 web-01");
    click(optionByText(view.container, "探测 web-01"));
    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("connection refused")),
    );
  });

  it("快速连接动作交给调用方打开 overlay", async () => {
    const onQuickConnect = vi.fn();
    const view = await mountPalette({ onClose: vi.fn(), onQuickConnect });
    setInputValue(searchInput(view.container), "快速连接");
    click(optionByText(view.container, "快速连接"));
    expect(onQuickConnect).toHaveBeenCalledTimes(1);
  });

  it("连接在途时条目提示连接中", async () => {
    const view = await mountPalette({ onClose: vi.fn() });
    useUi.setState({ connectingAssetIds: ["a-web"] });
    await flush();
    expect(optionByText(view.container, "连接 web-01").textContent).toContain("连接中…");
  });
});
