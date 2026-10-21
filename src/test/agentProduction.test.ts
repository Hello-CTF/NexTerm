import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ call: vi.fn(), assetList: vi.fn() }));
vi.mock("../ipc/commands", async () => {
  const { createFakeApi } = await import("./helpers/createFakeApi");
  return createFakeApi({ call: mocks.call, assetApi: { list: mocks.assetList } });
});

import type {
  ConversationRecoverySummary,
  RosterAttachmentDTO,
  RosterEntry,
  RosterListEntry,
  RunHistoryPage,
} from "../ipc/generated/types";
import {
  createAgentRosterApi,
  createAgentRunHistoryApi,
  type AgentRosterProductionDeps,
} from "../features/ai/agentProduction";
import type { AgentRosterApi, AgentRunHistoryApi } from "../features/ai/agentApi";

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
    createdAt: 1_760_000_000_000,
    updatedAt: 1_760_000_100_000,
    releasedAt: null,
    ...partial,
  };
}

function rosterRow(partial: Partial<RosterListEntry> = {}): RosterListEntry {
  return { entry: rosterEntry(), presence: "connected", assetId: "asset-1", ...partial };
}

function deps(partial: Partial<AgentRosterProductionDeps> = {}): AgentRosterProductionDeps {
  return {
    conversationId: "conv-1",
    identity: { ownerId: "u-1", ownerName: "alice" },
    activeJobId: () => "job-live",
    resolveApproval: vi.fn(async () => undefined),
    ...partial,
  };
}

// Compile-time conformance: the production factories implement the reviewed
// contracts through typed IPC only (no unsafeCall, no mock as production).
const rosterConformance: AgentRosterApi = createAgentRosterApi(deps());
const historyConformance: AgentRunHistoryApi = createAgentRunHistoryApi();

describe("agent production roster adapter", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.assetList.mockResolvedValue([]);
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
  });

  it("implements the reviewed contracts", () => {
    expect(typeof rosterConformance.list).toBe("function");
    expect(typeof historyConformance.listRuns).toBe("function");
  });

  it("lists through ai_roster_list and maps the real DTO with honest unknown host/cwd", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") return Promise.resolve([rosterRow()]);
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    const api = createAgentRosterApi(deps());
    const rows = await api.list();
    expect(mocks.call).toHaveBeenCalledWith("ai_roster_list", { conversationId: "conv-1" });
    expect(rows).toHaveLength(1);
    const row = rows[0];
    expect(row.handle).toBe("build");
    // M131 lands real host/cwd; the adapter never substitutes the asset name.
    expect(row.host).toBe("");
    expect(row.cwd).toBe("");
    expect(row.presence).toBe("attached");
    expect(row.isMine).toBe(true);
    expect(row.ownerId).toBe("u-1");
    expect(row.persistent).toBe(true);
    expect(row.incarnation).toBe("inc-1");
    expect(row.tabId).toBe("tab-1");
    expect(row.releasePending).toBe(false);
    expect(row.lastRun).toBeNull();
  });

  it("marks production rows from the real target asset environment", async () => {
    mocks.assetList.mockResolvedValue([
      { id: "asset-1", tags: "env:production" },
      { id: "asset-2", tags: "env:staging" },
    ]);
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") {
        return Promise.resolve([
          rosterRow({ entry: rosterEntry({ handle: "prod", id: "row-p" }), assetId: "asset-1" }),
          rosterRow({ entry: rosterEntry({ handle: "stage", id: "row-s" }), assetId: "asset-2" }),
          rosterRow({ entry: rosterEntry({ handle: "unknown", id: "row-u" }), assetId: "" }),
        ]);
      }
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    const api = createAgentRosterApi(deps());
    const rows = await api.list();
    expect(rows.find((row) => row.handle === "prod")?.production).toBe(true);
    expect(rows.find((row) => row.handle === "stage")?.production).toBe(false);
    expect(rows.find((row) => row.handle === "unknown")?.production).toBe(false);
  });

  it("keeps listing when the asset or settings enrichment is unreachable", async () => {
    mocks.assetList.mockRejectedValue(new Error("assets down"));
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") return Promise.resolve([rosterRow()]);
      if (cmd === "production_settings_get") return Promise.reject(new Error("settings down"));
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    const api = createAgentRosterApi(deps());
    const rows = await api.list();
    expect(rows).toHaveLength(1);
    expect(rows[0].production).toBe(false);
  });

  it("maps release-pending state and evidences a confirmed release", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") {
        return Promise.resolve([
          rosterRow({ entry: rosterEntry({ state: "release_pending" }), presence: "connected" }),
          rosterRow({
            entry: rosterEntry({
              id: "row-r",
              handle: "done",
              state: "released",
              releasedAt: 1_760_000_200_000,
              tabId: "",
            }),
            presence: "gone",
          }),
        ]);
      }
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    const api = createAgentRosterApi(deps());
    const rows = await api.list();
    expect(rows[0].releasePending).toBe(true);
    expect(rows[1].presence).toBe("gone");
    expect(rows[1].evidence).toEqual({
      kind: "release",
      observedAt: 1_760_000_200_000,
      summary: expect.stringContaining("释放"),
    });
    expect(rows[1].tabId).toBeNull();
  });

  it("evidences probe verdicts on live rows at the listing observation time", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") {
        return Promise.resolve([
          rosterRow({ presence: "gone" }),
          rosterRow({ entry: rosterEntry({ id: "row-x", handle: "mystery" }), presence: "bogus-value" }),
        ]);
      }
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    const before = Date.now();
    const api = createAgentRosterApi(deps());
    const rows = await api.list();
    const after = Date.now();
    expect(rows[0].presence).toBe("gone");
    expect(rows[0].evidence?.kind).toBe("reconcile");
    expect(rows[0].evidence?.observedAt).toBeGreaterThanOrEqual(before);
    expect(rows[0].evidence?.observedAt).toBeLessThanOrEqual(after);
    // An unrecognized backend resolution fails closed to unverifiable.
    expect(rows[1].presence).toBe("unverifiable");
    expect(rows[1].evidence).toBeNull();
  });

  it("evidences persisted terminal states with the row write timestamp", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") {
        return Promise.resolve([
          rosterRow({ entry: rosterEntry({ state: "gone", updatedAt: 1_760_000_300_000 }), presence: "gone" }),
        ]);
      }
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    const api = createAgentRosterApi(deps());
    const rows = await api.list();
    expect(rows[0].evidence).toEqual({
      kind: "reconcile",
      observedAt: 1_760_000_300_000,
      summary: expect.stringContaining("持久状态"),
    });
  });

  it("opens the cached preassigned tab and rejects unknown or tabless handles", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") {
        return Promise.resolve([
          rosterRow(),
          rosterRow({ entry: rosterEntry({ id: "row-t", handle: "notab", tabId: "" }) }),
        ]);
      }
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    const api = createAgentRosterApi(deps());
    await api.list();
    await expect(api.open("build")).resolves.toEqual({ tabId: "tab-1" });
    await expect(api.open("ghost")).rejects.toMatchObject({ code: "unsupported" });
    await expect(api.open("notab")).rejects.toMatchObject({ code: "unsupported" });
  });

  it("attaches through ai_roster_attach with the active job and returns the tab", async () => {
    const attachment: RosterAttachmentDTO = {
      entry: rosterEntry({ jobId: "job-live" }),
      resolution: "connected",
      assetId: "asset-1",
    };
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") return Promise.resolve([rosterRow()]);
      if (cmd === "ai_roster_attach") return Promise.resolve(attachment);
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    const api = createAgentRosterApi(deps());
    await api.list();
    await expect(api.attach("build")).resolves.toEqual({ tabId: "tab-1" });
    expect(mocks.call).toHaveBeenCalledWith("ai_roster_attach", { handle: "build", jobId: "job-live" });
  });

  it("rejects attach with the honest resolution for non-connected outcomes", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") return Promise.resolve([rosterRow()]);
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    const api = createAgentRosterApi(deps());
    await api.list();
    for (const [resolution, text] of [
      ["gone", "已消失"],
      ["replaced", "已被新化身替换"],
      ["exited", "已退出"],
      ["disconnected", "已断开"],
      ["unverifiable", "无法验证"],
      ["nonpersistent", "不是持久 roster 目标"],
    ] as const) {
      mocks.call.mockImplementation((cmd: string) => {
        if (cmd === "ai_roster_attach") {
          return Promise.resolve({ entry: rosterEntry(), resolution, assetId: "" } satisfies RosterAttachmentDTO);
        }
        if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
        return Promise.reject(new Error(`unexpected call: ${cmd}`));
      });
      await expect(api.attach("build")).rejects.toMatchObject({ message: expect.stringContaining(text) });
    }
  });

  it("releases with the exact durable identity from the latest listing", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") {
        return Promise.resolve([
          rosterRow({ entry: rosterEntry({ createdAt: 1_760_000_111_000, incarnation: "inc-exact" }) }),
        ]);
      }
      if (cmd === "ai_roster_release") return Promise.resolve(rosterEntry({ state: "release_pending" }));
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    const api = createAgentRosterApi(deps());
    await api.list();
    await api.release("build");
    expect(mocks.call).toHaveBeenCalledWith("ai_roster_release", {
      handle: "build",
      createdAt: 1_760_000_111_000,
      incarnation: "inc-exact",
    });
  });

  it("answers rename as honestly unsupported until the M131 command lands", async () => {
    const api = createAgentRosterApi(deps());
    await expect(api.rename("build", "deploy")).rejects.toMatchObject({ code: "unsupported" });
    expect(mocks.call).not.toHaveBeenCalledWith("ai_roster_rename", expect.anything());
  });

  it("delegates approval resolution to the production flow", async () => {
    const resolveApproval = vi.fn(async () => undefined);
    const api = createAgentRosterApi(deps({ resolveApproval }));
    await api.resolveApproval("req-1", "approve_session");
    expect(resolveApproval).toHaveBeenCalledWith("req-1", "approve_session");
  });
});

describe("agent production run history adapter", () => {
  beforeEach(() => vi.clearAllMocks());

  function historyPage(partial: Partial<RunHistoryPage> = {}): RunHistoryPage {
    return {
      entries: [
        {
          runId: "run-1",
          state: "done",
          evidence: { kind: "output", observedAt: 1_760_000_500_000, summary: "完成" },
          startedAt: 1_760_000_400_000,
          finishedAt: 1_760_000_500_000,
          summary: "完成",
        },
      ],
      nextCursor: "cursor-2",
      ...partial,
    };
  }

  it("forwards filter, opaque cursor and limit verbatim and maps one page per call", async () => {
    mocks.call.mockResolvedValue(historyPage());
    const api = createAgentRunHistoryApi();
    const page = await api.listRuns({ ownerId: "u-1", conversationId: "conv-1" }, "error", "opaque-1", 50);
    expect(mocks.call).toHaveBeenCalledWith("ai_run_history", {
      conversationId: "conv-1",
      filter: "error",
      cursor: "opaque-1",
      limit: 50,
    });
    expect(page.nextCursor).toBe("cursor-2");
    expect(page.entries[0]).toEqual({
      runId: "run-1",
      state: "done",
      evidence: { kind: "output", observedAt: 1_760_000_500_000, summary: "完成" },
      startedAt: 1_760_000_400_000,
      finishedAt: 1_760_000_500_000,
      summary: "完成",
    });
  });

  it("passes an empty cursor for the newest page and a null nextCursor through", async () => {
    mocks.call.mockResolvedValue(historyPage({ entries: [], nextCursor: null }));
    const api = createAgentRunHistoryApi();
    const page = await api.listRuns({ ownerId: "", conversationId: "conv-2" }, "all", null, 20);
    expect(mocks.call).toHaveBeenCalledWith("ai_run_history", {
      conversationId: "conv-2",
      filter: "all",
      cursor: "",
      limit: 20,
    });
    expect(page.entries).toEqual([]);
    expect(page.nextCursor).toBeNull();
  });

  it("rejects unrecognized backend run states and evidence kinds instead of casting", async () => {
    mocks.call.mockResolvedValue(historyPage({ entries: [{ ...historyPage().entries[0], state: "mystery" }] }));
    const api = createAgentRunHistoryApi();
    await expect(
      api.listRuns({ ownerId: "", conversationId: "conv-1" }, "all", null, 10),
    ).rejects.toMatchObject({ message: expect.stringContaining("mystery") });

    mocks.call.mockResolvedValue(
      historyPage({
        entries: [
          { ...historyPage().entries[0], evidence: { kind: "mystery", observedAt: 1, summary: "x" } },
        ],
      }),
    );
    await expect(
      api.listRuns({ ownerId: "", conversationId: "conv-1" }, "all", null, 10),
    ).rejects.toMatchObject({ message: expect.stringContaining("mystery") });
  });

  it("maps the recovery summary and passes null through", async () => {
    const summary: ConversationRecoverySummary = {
      conversationId: "conv-1",
      restoredRuns: 57,
      resumableRunId: "run-56",
      interruptedRunIds: ["run-56", "run-12"],
      truncated: true,
    };
    mocks.call.mockResolvedValue(summary);
    const api = createAgentRunHistoryApi();
    await expect(api.recoverySummary({ ownerId: "", conversationId: "conv-1" })).resolves.toEqual(summary);
    expect(mocks.call).toHaveBeenCalledWith("ai_run_recovery_summary", { conversationId: "conv-1" });

    mocks.call.mockResolvedValue(null);
    await expect(api.recoverySummary({ ownerId: "", conversationId: "conv-1" })).resolves.toBeNull();
  });
});
