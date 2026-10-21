/** @vitest-environment jsdom */

// Deterministic production integration: the reviewed panels run against the
// real production adapter (typed IPC only) with the transport mocked at the
// commands seam. Backend-owned paging is proven by exact cursor arguments —
// the UI never slices accumulated results in memory.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, flush, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  call: vi.fn(),
  assetList: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
  resolveApproval: vi.fn(),
}));

vi.mock("../ipc/commands", async () => {
  const { createFakeApi } = await import("./helpers/createFakeApi");
  return createFakeApi({ call: mocks.call, assetApi: { list: mocks.assetList } });
});
vi.mock("../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});

import type {
  ConversationRecoverySummary,
  RosterEntry,
  RunHistoryEntry,
  RunHistoryPage,
} from "../ipc/generated/types";
import { AgentRosterPanel } from "../features/ai/AgentRosterPanel";
import { AgentRunHistoryPanel } from "../features/ai/AgentRunHistoryPanel";
import { createAgentRosterApi, createAgentRunHistoryApi } from "../features/ai/agentProduction";
import { useUi } from "../app/store";

function runEntry(index: number): RunHistoryEntry {
  const finished = index % 3 !== 0;
  return {
    runId: `run-${String(index).padStart(3, "0")}`,
    state: finished ? "done" : "idle",
    evidence: finished
      ? { kind: "output", observedAt: 1_760_000_000_000 + index, summary: `run ${index} 完成` }
      : null,
    startedAt: 1_760_000_000_000 + index,
    finishedAt: finished ? 1_760_000_000_000 + index : null,
    summary: `run ${index}`,
  };
}

// historyBackend emulates the real ai_run_history contract: keyset pages of
// `size`, an opaque cursor the caller passes back verbatim, and server-side
// status filtering.
function historyBackend(total: number, size: number) {
  const calls: { filter: string; cursor: string; limit: number }[] = [];
  const handler = (args: { conversationId: string; filter: string; cursor: string; limit: number }): RunHistoryPage => {
    calls.push({ filter: args.filter, cursor: args.cursor, limit: args.limit });
    const offset = args.cursor === "" ? 0 : Number(args.cursor.replace("cursor-", ""));
    const all = Array.from({ length: total }, (_, index) => runEntry(index));
    const filtered = args.filter === "all" ? all : all.filter((entry) => entry.state === args.filter);
    const pageRows = filtered.slice(offset, offset + args.limit);
    const consumed = offset + pageRows.length;
    return {
      entries: pageRows,
      nextCursor: consumed < filtered.length ? `cursor-${consumed}` : null,
    };
  };
  return { calls, handler, pageSize: size };
}

let view: MountedView | null = null;

afterEach(() => {
  view?.unmount();
  view = null;
});

describe("production integration: AI-05 run history", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("restores more than 50 runs through verbatim opaque cursors only", async () => {
    const backend = historyBackend(120, 50);
    const summary: ConversationRecoverySummary = {
      conversationId: "conv-1",
      restoredRuns: 120,
      resumableRunId: "run-001",
      interruptedRunIds: ["run-001", "run-004"],
      truncated: false,
    };
    mocks.call.mockImplementation((cmd: string, args?: Record<string, unknown>) => {
      if (cmd === "ai_run_history") return Promise.resolve(backend.handler(args as never));
      if (cmd === "ai_run_recovery_summary") return Promise.resolve(summary);
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });

    const api = createAgentRunHistoryApi();
    view = mount(
      createElement(AgentRunHistoryPanel, {
        api,
        scope: { ownerId: "u-1", conversationId: "conv-1" },
      }),
    );
    await flush();

    // First page: 50 rows, fetched with an empty cursor and the page size.
    expect(backend.calls).toEqual([{ filter: "all", cursor: "", limit: 50 }]);
    expect(view.container.querySelectorAll("[data-run-id]")).toHaveLength(50);
    // The backend recovery summary renders as reported (restored > 50).
    const text = view.container.textContent ?? "";
    expect(text).toContain("已恢复 120 个 run");
    expect(text).toContain("可继续运行 run-001");
    expect(text).toContain("2 个 run 中断");

    clickButton(view.container, "加载更早");
    await flush();
    expect(backend.calls[1]).toEqual({ filter: "all", cursor: "cursor-50", limit: 50 });
    expect(view.container.querySelectorAll("[data-run-id]")).toHaveLength(100);

    clickButton(view.container, "加载更早");
    await flush();
    expect(backend.calls[2]).toEqual({ filter: "all", cursor: "cursor-100", limit: 50 });
    const rows = [...view.container.querySelectorAll("[data-run-id]")];
    expect(rows).toHaveLength(120);
    // Every row the backend returned is present exactly once.
    expect(new Set(rows.map((row) => row.getAttribute("data-run-id"))).size).toBe(120);
    expect(view.container.textContent).not.toContain("加载更早");
  });

  it("round-trips the server-side status filter and resets to the first page", async () => {
    const backend = historyBackend(120, 50);
    mocks.call.mockImplementation((cmd: string, args?: Record<string, unknown>) => {
      if (cmd === "ai_run_history") return Promise.resolve(backend.handler(args as never));
      if (cmd === "ai_run_recovery_summary") return Promise.resolve(null);
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    const api = createAgentRunHistoryApi();
    view = mount(
      createElement(AgentRunHistoryPanel, {
        api,
        scope: { ownerId: "", conversationId: "conv-1" },
      }),
    );
    await flush();

    clickButton(view.container, "失败");
    await flush();
    expect(backend.calls.at(-1)).toEqual({ filter: "error", cursor: "", limit: 50 });
    // The mock backend has no error rows: the filter is honestly server-side.
    expect(view.container.querySelectorAll("[data-run-id]")).toHaveLength(0);
    expect(view.container.textContent).toContain("暂无 run 记录");

    clickButton(view.container, "空闲");
    await flush();
    expect(backend.calls.at(-1)).toEqual({ filter: "idle", cursor: "", limit: 50 });
    expect(view.container.querySelectorAll("[data-run-id]")).toHaveLength(40);
  });
});

describe("production integration: roster panel", () => {
  function rosterEntry(partial: Partial<RosterEntry> = {}): RosterEntry {
    return {
      id: "row-1",
      conversationId: "conv-1",
      handle: "build",
      alias: "build",
      tabId: "tab-1",
      state: "active",
      incarnation: "inc-1",
      jobId: "job-1",
      createdAt: 1_760_000_111_000,
      updatedAt: 1_760_000_222_000,
      releasedAt: null,
      ...partial,
    };
  }

  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
    useUi.setState({ pushToast: mocks.toast });
    mocks.assetList.mockResolvedValue([{ id: "asset-1", tags: "env:production" }]);
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") {
        return Promise.resolve([
          { entry: rosterEntry(), presence: "connected", assetId: "asset-1" },
          {
            entry: rosterEntry({ id: "row-2", handle: "deploy", tabId: "tab-2" }),
            presence: "disconnected",
            assetId: "",
          },
        ]);
      }
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
  });

  function mountRoster(callbacks: { onMention?: (token: string) => void; onOpened?: (tabId: string) => void } = {}) {
    const api = createAgentRosterApi({
      conversationId: "conv-1",
      identity: { ownerId: "u-1", ownerName: "alice" },
      activeJobId: () => "job-live",
      resolveApproval: mocks.resolveApproval,
    });
    view = mount(
      createElement(AgentRosterPanel, {
        api,
        onMention: callbacks.onMention,
        onOpened: callbacks.onOpened,
      }),
    );
    return api;
  }

  it("renders real rows with honest unknown host/cwd and the production badge", async () => {
    mountRoster();
    await flush();
    const build = view!.container.querySelector('[data-handle="build"]')!;
    const text = build.textContent ?? "";
    expect(text).toContain("build");
    // No fabricated target identity: handle@asset-name must never appear.
    expect(text).not.toContain("build@");
    expect(text).toContain("已附加");
    expect(text).toContain("生产环境");
    const deploy = view!.container.querySelector('[data-handle="deploy"]')!;
    expect(deploy.textContent).toContain("已断开");
    expect(deploy.textContent).not.toContain("生产环境");
  });

  it("attaches through the real IPC with the active job and reports the tab", async () => {
    const onOpened = vi.fn();
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") {
        return Promise.resolve([{ entry: rosterEntry(), presence: "connected", assetId: "asset-1" }]);
      }
      if (cmd === "ai_roster_attach") {
        return Promise.resolve({ entry: rosterEntry(), resolution: "connected", assetId: "asset-1" });
      }
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    mountRoster({ onOpened });
    await flush();
    const build = view!.container.querySelector('[data-handle="build"]')!;
    clickButton(build as HTMLElement, "附加");
    await flush();
    expect(mocks.call).toHaveBeenCalledWith("ai_roster_attach", { handle: "build", jobId: "job-live" });
    expect(onOpened).toHaveBeenCalledWith("tab-1", expect.objectContaining({ handle: "build" }));
  });

  it("releases with the exact durable identity and keeps the row release-pending", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") {
        return Promise.resolve([{ entry: rosterEntry(), presence: "connected", assetId: "asset-1" }]);
      }
      if (cmd === "ai_roster_release") {
        return Promise.resolve(rosterEntry({ state: "release_pending" }));
      }
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    mountRoster();
    await flush();
    const build = view!.container.querySelector('[data-handle="build"]')!;
    clickButton(build as HTMLElement, "释放");
    await flush();
    // Production target: the confirmation names the production impact.
    const prompt = mocks.ask.mock.calls[0][0] as string;
    expect(prompt).toContain("生产环境");
    expect(mocks.call).toHaveBeenCalledWith("ai_roster_release", {
      handle: "build",
      createdAt: 1_760_000_111_000,
      incarnation: "inc-1",
    });
    expect(build.textContent).toContain("释放待确认");
  });

  it("mentions through the panel callback with the durable handle token", async () => {
    const onMention = vi.fn();
    mountRoster({ onMention });
    await flush();
    const build = view!.container.querySelector('[data-handle="build"]')!;
    clickButton(build as HTMLElement, "提及");
    expect(onMention).toHaveBeenCalledWith("@build", expect.objectContaining({ handle: "build" }));
  });
});
