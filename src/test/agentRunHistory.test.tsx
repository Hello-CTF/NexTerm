/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, clickButton, deferred, flush, mount, type MountedView } from "./features/reactTestUtils";

import { AgentRunHistoryPanel } from "../features/ai/AgentRunHistoryPanel";
import type { AgentRunHistoryApi, AgentRunHistoryScope } from "../features/ai/agentApi";
import type { AgentRunHistoryEntry, AgentRunHistoryPage } from "../features/ai/agentTypes";

function runEntry(partial: Partial<AgentRunHistoryEntry> = {}): AgentRunHistoryEntry {
  return {
    runId: "run-1",
    state: "done",
    evidence: { kind: "output", observedAt: 1_760_000_000_000, summary: "退出码 0" },
    startedAt: 1_760_000_000_000,
    finishedAt: 1_760_000_006_000,
    summary: "部署完成",
    ...partial,
  };
}

function page(entries: AgentRunHistoryEntry[], nextCursor: string | null): AgentRunHistoryPage {
  return { entries, nextCursor };
}

function makeApi() {
  return {
    listRuns: vi.fn(),
    recoverySummary: vi.fn(),
  } satisfies AgentRunHistoryApi;
}

const scope: AgentRunHistoryScope = { ownerId: "u-1", conversationId: "conv-1" };

let view: MountedView | null = null;

async function mountPanel(api: AgentRunHistoryApi, s: AgentRunHistoryScope = scope) {
  view = mount(createElement(AgentRunHistoryPanel, { api, scope: s, pageSize: 50 }));
  await flush();
  return view;
}

function rows(): HTMLElement[] {
  return [...view!.container.querySelectorAll("[data-run-id]")] as HTMLElement[];
}

function rowIds(): string[] {
  return rows().map((row) => row.getAttribute("data-run-id") ?? "");
}

function refreshButton(): HTMLButtonElement {
  const b = view!.container.querySelector('button[aria-label="刷新 run 历史"]');
  if (!b) throw new Error("refresh button not found");
  return b as HTMLButtonElement;
}

describe("AgentRunHistoryPanel", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    view?.unmount();
    view = null;
  });

  it("renders the first backend page with evidence-based run states", async () => {
    const api = makeApi();
    api.listRuns.mockResolvedValue(
      page(
        [
          runEntry({ runId: "run-idle", state: "idle", evidence: null, finishedAt: null }),
          runEntry({ runId: "run-no-evidence", state: "done", evidence: null }),
          runEntry({ runId: "run-done" }),
        ],
        null,
      ),
    );
    api.recoverySummary.mockResolvedValue(null);
    await mountPanel(api);

    expect(api.listRuns).toHaveBeenCalledWith(scope, "all", null, 50);
    expect(rowIds()).toEqual(["run-idle", "run-no-evidence", "run-done"]);
    const text = view!.container.textContent ?? "";
    expect(text).toContain("空闲");
    expect(text).toContain("未知");
    expect(text).toContain("已完成");
    const idleRow = view!.container.querySelector('[data-run-id="run-idle"]')!;
    expect(idleRow.textContent).not.toContain("已完成");
    expect(view!.container.textContent).not.toContain("加载更早");
  });

  it("restores more than 50 runs as multiple backend pages via opaque cursors", async () => {
    const api = makeApi();
    const make = (prefix: string, count: number) =>
      Array.from({ length: count }, (_, i) => runEntry({ runId: `${prefix}-${i}` }));
    api.listRuns
      .mockResolvedValueOnce(page(make("a", 50), "cursor-2"))
      .mockResolvedValueOnce(page(make("b", 50), "cursor-3"))
      .mockResolvedValueOnce(page(make("c", 20), null));
    api.recoverySummary.mockResolvedValue(null);
    await mountPanel(api);

    // Only the first backend page is rendered; nothing is sliced in memory.
    expect(rows()).toHaveLength(50);
    expect(view!.container.textContent).toContain("加载更早");

    clickButton(view!.container, "加载更早");
    await flush();
    expect(api.listRuns).toHaveBeenLastCalledWith(scope, "all", "cursor-2", 50);
    expect(rows()).toHaveLength(100);

    clickButton(view!.container, "加载更早");
    await flush();
    expect(api.listRuns).toHaveBeenLastCalledWith(scope, "all", "cursor-3", 50);
    expect(rows()).toHaveLength(120);
    expect(view!.container.textContent).not.toContain("加载更早");
  });

  it("dedupes overlapping run ids across pages", async () => {
    const api = makeApi();
    api.listRuns
      .mockResolvedValueOnce(page([runEntry({ runId: "run-1" }), runEntry({ runId: "run-2" })], "c2"))
      .mockResolvedValueOnce(
        page([runEntry({ runId: "run-2" }), runEntry({ runId: "run-3" })], null),
      );
    api.recoverySummary.mockResolvedValue(null);
    await mountPanel(api);
    clickButton(view!.container, "加载更早");
    await flush();
    expect(rowIds()).toEqual(["run-1", "run-2", "run-3"]);
    expect(view!.container.querySelectorAll('[data-run-id="run-2"]')).toHaveLength(1);
  });

  it("changing the status filter resets to a server-filtered first page", async () => {
    const api = makeApi();
    api.listRuns
      .mockResolvedValueOnce(page([runEntry({ runId: "run-any" })], null))
      .mockResolvedValueOnce(page([runEntry({ runId: "run-done" })], null));
    api.recoverySummary.mockResolvedValue(null);
    await mountPanel(api);
    clickButton(view!.container, "已完成");
    await flush();
    expect(api.listRuns).toHaveBeenLastCalledWith(scope, "done", null, 50);
    expect(rowIds()).toEqual(["run-done"]);
  });

  it("switching the owner scope reloads and discards the old scope's late response", async () => {
    const stale = deferred<AgentRunHistoryPage>();
    const api = makeApi();
    api.listRuns
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValueOnce(page([runEntry({ runId: "run-new" })], null));
    api.recoverySummary.mockResolvedValue(null);
    await mountPanel(api);

    const nextScope: AgentRunHistoryScope = { ownerId: "u-2", conversationId: "conv-2" };
    act(() => {
      view!.root.render(createElement(AgentRunHistoryPanel, { api, scope: nextScope, pageSize: 50 }));
    });
    await flush();
    expect(api.listRuns).toHaveBeenLastCalledWith(nextScope, "all", null, 50);
    expect(rowIds()).toEqual(["run-new"]);

    stale.resolve(page([runEntry({ runId: "run-old" })], null));
    await flush();
    expect(rowIds()).toEqual(["run-new"]);
  });

  it("a stale refresh response never overwrites a newer first page", async () => {
    const stale = deferred<AgentRunHistoryPage>();
    const api = makeApi();
    api.listRuns
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValueOnce(page([runEntry({ runId: "run-fresh" })], null));
    api.recoverySummary.mockResolvedValue(null);
    await mountPanel(api);

    click(refreshButton());
    await flush();
    expect(rowIds()).toEqual(["run-fresh"]);

    stale.resolve(page([runEntry({ runId: "run-stale" })], null));
    await flush();
    expect(rowIds()).toEqual(["run-fresh"]);
  });

  it("a failed older-page load keeps the rendered entries and recovers via retry", async () => {
    const api = makeApi();
    api.listRuns
      .mockResolvedValueOnce(page([runEntry({ runId: "run-1" })], "c2"))
      .mockRejectedValueOnce(new Error("page offline"))
      .mockResolvedValueOnce(page([runEntry({ runId: "run-2" })], null));
    api.recoverySummary.mockResolvedValue(null);
    await mountPanel(api);

    clickButton(view!.container, "加载更早");
    await flush();
    expect(view!.container.textContent).toContain("加载失败");
    expect(rowIds()).toEqual(["run-1"]);

    clickButton(view!.container, "重试");
    await flush();
    expect(rowIds()).toEqual(["run-1", "run-2"]);
    expect(view!.container.textContent).not.toContain("加载失败");
  });

  it("an initial load failure offers a retry that reloads the first page", async () => {
    const api = makeApi();
    api.listRuns
      .mockRejectedValueOnce(new Error("history offline"))
      .mockResolvedValueOnce(page([runEntry({ runId: "run-1" })], null));
    api.recoverySummary.mockResolvedValue(null);
    await mountPanel(api);
    expect(view!.container.textContent).toContain("加载失败");
    clickButton(view!.container, "重试");
    await flush();
    expect(rowIds()).toEqual(["run-1"]);
  });

  it("renders the recovery summary with resume, interruption and truncation evidence", async () => {
    const api = makeApi();
    api.listRuns.mockResolvedValue(page([], null));
    api.recoverySummary.mockResolvedValue({
      conversationId: "conv-1",
      restoredRuns: 120,
      resumableRunId: "run-7",
      interruptedRunIds: ["run-3", "run-9"],
      truncated: true,
    });
    await mountPanel(api);
    const text = view!.container.textContent ?? "";
    expect(text).toContain("已恢复 120 个 run");
    expect(text).toContain("run-7");
    expect(text).toContain("2 个 run 中断");
    expect(text).toContain("截断");
  });
});
