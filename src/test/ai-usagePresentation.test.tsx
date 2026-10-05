/** @vitest-environment jsdom */

import { describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import type { AiRunDto, AiUsageSummaryRow } from "../ipc/types";
import { aggregateUsageRows, runErrorStats, UsageRing } from "../features/ai/UsageRing";
import { click, flush, mount } from "./features/reactTestUtils";

const summaryRows: AiUsageSummaryRow[] = [
  { source: "chat", profileId: "p-a", runs: 2, tokensIn: 100, tokensOut: 50, cacheCreationTokens: 10, averageLatencyMs: 1000 },
  { source: "chat", profileId: "p-b", runs: 2, tokensIn: 300, tokensOut: 150, cacheCreationTokens: 0, averageLatencyMs: 3000 },
  { source: "title", profileId: "p-a", runs: 8, tokensIn: 80, tokensOut: 8, cacheCreationTokens: 0, averageLatencyMs: 500 },
];

function run(overrides: Partial<AiRunDto>): AiRunDto {
  return {
    id: "r",
    conversationId: "c",
    status: "completed",
    attempt: 1,
    seq: 1,
    planMode: false,
    source: "chat",
    answer: "",
    turns: 1,
    tokensIn: 0,
    tokensOut: 0,
    cacheCreationTokens: 0,
    latencyMs: 0,
    retries: 0,
    failures: 0,
    createdAt: 0,
    updatedAt: 0,
    finishedAt: 1,
    ...overrides,
  };
}

describe("aggregateUsageRows", () => {
  it("keeps title usage out of chat totals and averages latency weighted by runs", () => {
    const { chat, title } = aggregateUsageRows(summaryRows);
    expect(chat.runs).toBe(4);
    expect(chat.tokensIn).toBe(400);
    expect(chat.tokensOut).toBe(200);
    expect(chat.cacheCreationTokens).toBe(10);
    expect(chat.averageLatencyMs).toBe(2000);
    expect(title.runs).toBe(8);
    expect(title.tokensIn).toBe(80);
    expect(title.tokensOut).toBe(8);
  });

  it("tolerates empty and malformed rows", () => {
    const { chat, title } = aggregateUsageRows([
      { source: "chat", profileId: "p", runs: Number.NaN, tokensIn: -5, tokensOut: 0, cacheCreationTokens: 0, averageLatencyMs: Number.NaN },
    ]);
    expect(chat.runs).toBe(0);
    expect(chat.tokensIn).toBe(0);
    expect(title.runs).toBe(0);
    expect(aggregateUsageRows([]).chat.runs).toBe(0);
  });
});

describe("runErrorStats", () => {
  it("counts retries/failures over finished chat runs only", () => {
    const stats = runErrorStats([
      run({ id: "a", retries: 1 }),
      run({ id: "b", failures: 1 }),
      run({ id: "c", source: "title", failures: 5 }),
      run({ id: "d", status: "running", finishedAt: undefined, retries: 3 }),
      run({ id: "e", status: "interrupted", finishedAt: undefined, failures: 2 }),
    ]);
    expect(stats.runs).toBe(2);
    expect(stats.retries).toBe(1);
    expect(stats.failures).toBe(1);
    expect(stats.retryRate).toBe(0.5);
    expect(stats.failureRate).toBe(0.5);
  });

  it("yields zero rates without finished runs", () => {
    const stats = runErrorStats([run({ id: "a", status: "running", finishedAt: undefined, retries: 2 })]);
    expect(stats).toEqual({ runs: 0, retries: 0, failures: 0, retryRate: 0, failureRate: 0 });
  });
});

describe("UsageRing details", () => {
  it("shows title usage separately plus retry/failure rates, loading the summary lazily", async () => {
    const loadSummary = vi.fn().mockResolvedValue(summaryRows);
    const view = mount(
      createElement(UsageRing, {
        runs: [run({ id: "a", retries: 1 }), run({ id: "b", failures: 1 })],
        loadSummary,
      }),
    );
    const button = view.container.querySelector("button");
    expect(button).not.toBeNull();
    expect(loadSummary).not.toHaveBeenCalled();

    click(button!);
    await flush();
    const text = view.container.textContent ?? "";
    expect(text).toContain("用量分账");
    expect(text).toContain("本会话 2 轮");
    expect(text).toContain("重试 1");
    expect(text).toContain("失败 1");
    expect(text).toContain("对话 4 次");
    expect(text).toContain("输入 400");
    expect(text).toContain("标题 8 次");
    expect(text).toContain("单独计，不入对话总量");
    expect(loadSummary).toHaveBeenCalledTimes(1);

    click(button!);
    await flush();
    expect(view.container.textContent ?? "").not.toContain("用量分账");
    view.unmount();
  });

  it("reports summary load failure without retry spam", async () => {
    const loadSummary = vi.fn().mockRejectedValue(new Error("boom"));
    const view = mount(createElement(UsageRing, { loadSummary }));
    click(view.container.querySelector("button")!);
    await flush();
    expect(view.container.textContent ?? "").toContain("用量汇总加载失败");
    expect(loadSummary).toHaveBeenCalledTimes(1);
    view.unmount();
  });
});
