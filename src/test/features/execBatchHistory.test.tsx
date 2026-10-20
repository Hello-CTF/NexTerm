/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { AccountUser } from "../../ipc/authApi";
import type { ExecBatchHistory, ExecBatchHistoryPage, ExecBatchHistoryQuery } from "../../ipc/execBatchApi";
import { click, clickButton, deferred, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({ historyList: vi.fn(), historyGet: vi.fn() }));
vi.mock("../../ipc/execBatchApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/execBatchApi")>();
  return {
    ...actual,
    execBatchApi: { ...actual.execBatchApi, historyList: mocks.historyList, historyGet: mocks.historyGet },
  };
});
vi.mock("../../ipc/env", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/env")>();
  return { ...actual, WEB: true, DESKTOP: false };
});

import { ExecBatchHistory as ExecBatchHistoryPanel } from "../../features/execbatch/ExecBatchHistory";
import { useAuth } from "../../features/auth/store";

const accountA = { id: "account-a" } as AccountUser;
const accountB = { id: "account-b" } as AccountUser;
const batch = {
  id: "history-batch",
  commandHash: "a".repeat(64),
  targetSetHash: "b".repeat(64),
  status: "failed",
  targetCount: 1,
  production: true,
  startedAt: 1000,
  finishedAt: 2000,
  syncOptIn: false,
  ownerScope: "self",
};

const detail: ExecBatchHistory = {
  batch,
  results: [
    {
      sessionId: "session-1",
      position: 0,
      sessionName: "生产主机",
      assetId: "asset-1",
      environment: "production",
      status: "exit_error",
      exitCode: 17,
      signal: "",
      errorKind: "remote_exit",
      startedAt: 1000,
      finishedAt: 2000,
      durationMs: 1000,
      truncated: false,
    },
  ],
};

function detailWithTarget(sessionName: string): ExecBatchHistory {
  return { ...detail, results: [{ ...detail.results[0]!, sessionName }] };
}

function page(offset: number): ExecBatchHistoryPage {
  return { items: offset === 0 ? [batch] : [], total: 21, limit: 20, offset };
}

let mounted: MountedView | undefined;
let client: QueryClient;

function text(): string {
  return mounted?.container.textContent ?? "";
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useAuth.setState({ user: accountA });
  mocks.historyList.mockImplementation((query: ExecBatchHistoryQuery) => Promise.resolve(page(query.offset ?? 0)));
  mocks.historyGet.mockResolvedValue(detail);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(createElement(QueryClientProvider, { client }, createElement(ExecBatchHistoryPanel)));
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  client.clear();
  useAuth.setState({ user: null });
});

async function openHistory(): Promise<void> {
  clickButton(mounted!.container, "执行历史");
  await flushUntil(() => text().includes("共 21 批"));
}

describe("ExecBatchHistory", () => {
  it("loads on demand and shows a metadata-only paginated result matrix", async () => {
    expect(mocks.historyList).not.toHaveBeenCalled();
    expect(mocks.historyGet).not.toHaveBeenCalled();
    await openHistory();
    expect(mocks.historyList).toHaveBeenCalledWith({ limit: 20, offset: 0 });
    expect(text()).toContain("生产");

    clickButton(mounted!.container, "查看矩阵");
    await flushUntil(() => text().includes("历史结果矩阵"));
    expect(mocks.historyGet).toHaveBeenCalledWith("history-batch", "self");
    expect(text()).toContain("生产主机");
    expect(text()).toContain("远端非零退出");
    expect(text()).toContain("17");

    clickButton(mounted!.container, "下一页");
    await flushUntil(() => text().includes("暂无批量执行历史"));
    expect(mocks.historyList).toHaveBeenLastCalledWith({ limit: 20, offset: 20 });
    expect(text()).not.toContain("历史结果矩阵");
  });

  it("selects self and ownerless rows independently when batch IDs match", async () => {
    mocks.historyList.mockResolvedValue({
      items: [
        { ...batch, id: "shared-id", ownerScope: "self" },
        { ...batch, id: "shared-id", ownerScope: "ownerless" },
      ],
      total: 2,
      limit: 20,
      offset: 0,
    });
    mocks.historyGet.mockImplementation((_id: string, scope: string) =>
      Promise.resolve(scope === "ownerless" ? detailWithTarget("ownerless-target") : detailWithTarget("self-target")),
    );
    clickButton(mounted!.container, "执行历史");
    await flushUntil(() => text().includes("共 2 批"));
    const buttons = [...mounted!.container.querySelectorAll("button")].filter(
      (button) => button.textContent?.trim() === "查看矩阵",
    );
    expect(buttons).toHaveLength(2);

    click(buttons[1]!);
    await flushUntil(() => text().includes("ownerless-target"));
    expect(mocks.historyGet).toHaveBeenLastCalledWith("shared-id", "ownerless");
    click(buttons[0]!);
    await flushUntil(() => text().includes("self-target"));
    expect(mocks.historyGet).toHaveBeenLastCalledWith("shared-id", "self");
    expect(text()).not.toContain("ownerless-target");
    expect(client.getQueryData(["exec-batch-history-detail", "account-a", "shared-id", "self"])).toBeDefined();
    expect(client.getQueryData(["exec-batch-history-detail", "account-a", "shared-id", "ownerless"])).toBeDefined();
  });

  it("does not show or retain account A data while account B is pending", async () => {
    mocks.historyGet.mockResolvedValue(detailWithTarget("account-a-target"));
    await openHistory();
    clickButton(mounted!.container, "查看矩阵");
    await flushUntil(() => text().includes("account-a-target"));
    const listKey = ["exec-batch-history", "account-a", 0] as const;
    const detailKey = ["exec-batch-history-detail", "account-a", "history-batch", "self"] as const;
    expect(client.getQueryData(listKey)).toBeDefined();
    expect(client.getQueryData(detailKey)).toBeDefined();

    const pending = deferred<ExecBatchHistoryPage>();
    mocks.historyList.mockReturnValue(pending.promise);
    act(() => useAuth.setState({ user: accountB }));
    await flushUntil(() =>
      !text().includes("account-a-target") &&
      client.getQueryData(listKey) === undefined &&
      client.getQueryData(detailKey) === undefined,
    );
    expect(text()).not.toContain("历史结果矩阵");
    expect(mocks.historyGet).toHaveBeenCalledTimes(1);
    expect(mocks.historyList).toHaveBeenCalledTimes(2);
    await act(async () => {
      pending.resolve({ items: [], total: 0, limit: 20, offset: 0 });
      await pending.promise;
    });
  });

  it("does not show or retain account A data when account B fails", async () => {
    mocks.historyGet.mockResolvedValue(detailWithTarget("account-a-target"));
    await openHistory();
    clickButton(mounted!.container, "查看矩阵");
    await flushUntil(() => text().includes("account-a-target"));
    const listKey = ["exec-batch-history", "account-a", 0] as const;
    const detailKey = ["exec-batch-history-detail", "account-a", "history-batch", "self"] as const;

    mocks.historyList.mockRejectedValue(new Error("account-b-unavailable"));
    act(() => useAuth.setState({ user: accountB }));
    await flushUntil(() => text().includes("历史加载失败"));
    expect(text()).not.toContain("account-a-target");
    expect(text()).not.toContain("历史结果矩阵");
    expect(client.getQueryData(listKey)).toBeUndefined();
    expect(client.getQueryData(detailKey)).toBeUndefined();
    expect(client.getQueryData(["exec-batch-history", "account-b", 0])).toBeUndefined();
    expect(mocks.historyGet).toHaveBeenCalledTimes(1);
  });

  it("hides stale list and matrix data when a refresh fails", async () => {
    mocks.historyGet.mockResolvedValue(detailWithTarget("account-a-target"));
    await openHistory();
    clickButton(mounted!.container, "查看矩阵");
    await flushUntil(() => text().includes("account-a-target"));
    mocks.historyList.mockRejectedValue(new Error("list-refresh-failed"));
    mocks.historyGet.mockRejectedValue(new Error("detail-refresh-failed"));

    clickButton(mounted!.container, "刷新");
    await flushUntil(() => text().includes("历史加载失败") && text().includes("结果矩阵加载失败"));
    expect(text()).not.toContain("共 21 批");
    expect(text()).not.toContain("account-a-target");
    expect(text()).not.toContain("历史结果矩阵");
  });
});
