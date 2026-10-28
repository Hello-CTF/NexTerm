import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ call: vi.fn(), assetList: vi.fn() }));
vi.mock("../ipc/commands", async () => {
  const { createFakeApi } = await import("./helpers/createFakeApi");
  return createFakeApi({ call: mocks.call, assetApi: { list: mocks.assetList } });
});

import type {
  RosterAttachmentDTO,
  RosterEntry,
  RosterListEntry,
} from "../ipc/generated/types";
import {
  createAgentRosterApi,
  type AgentRosterProductionDeps,
} from "../features/ai/agentProduction";
import type { AgentRosterApi } from "../features/ai/agentApi";

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
  return {
    entry: rosterEntry(),
    presence: "connected",
    assetId: "asset-1",
    host: "web-01",
    cwd: "/srv/app",
    ...partial,
  };
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
  });

  it("lists through ai_roster_list and maps the real DTO including verifiable host/cwd", async () => {
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
    expect(row.alias).toBe("build");
    // The backend's verifiable target evidence passes through verbatim.
    expect(row.host).toBe("web-01");
    expect(row.cwd).toBe("/srv/app");
    expect(row.presence).toBe("attached");
    expect(row.isMine).toBe(true);
    expect(row.ownerId).toBe("u-1");
    expect(row.persistent).toBe(true);
    expect(row.incarnation).toBe("inc-1");
    expect(row.tabId).toBe("tab-1");
    expect(row.releasePending).toBe(false);
    expect(row.lastRun).toBeNull();
  });

  it("keeps unverifiable target evidence honestly unknown", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") return Promise.resolve([rosterRow({ host: "", cwd: "" })]);
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    const api = createAgentRosterApi(deps());
    const rows = await api.list();
    expect(rows[0].host).toBe("");
    expect(rows[0].cwd).toBe("");
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
      summary: expect.stringContaining("已记录"),
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
      host: "web-01",
      cwd: "/srv/app",
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
      ["gone", "已不存在"],
      ["replaced", "已被新会话替换"],
      ["exited", "已退出"],
      ["disconnected", "已断开"],
      ["unverifiable", "无法确认"],
      ["nonpersistent", "不在持久终端中"],
    ] as const) {
      mocks.call.mockImplementation((cmd: string) => {
        if (cmd === "ai_roster_attach") {
          return Promise.resolve({
            entry: rosterEntry(),
            resolution,
            assetId: "",
            host: "",
            cwd: "",
          } satisfies RosterAttachmentDTO);
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

  it("renames the alias through ai_roster_rename with the exact durable identity", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") return Promise.resolve([rosterRow()]);
      if (cmd === "ai_roster_rename") return Promise.resolve(rosterEntry({ alias: "deploy 构建机" }));
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    const api = createAgentRosterApi(deps());
    await api.list();
    const updated = await api.rename("build", "deploy 构建机");
    expect(mocks.call).toHaveBeenCalledWith("ai_roster_rename", {
      handle: "build",
      alias: "deploy 构建机",
      createdAt: 1_760_000_000_000,
      incarnation: "inc-1",
    });
    // The permanent handle, incarnation and the cached target evidence
    // survive; only the alias moved.
    expect(updated.handle).toBe("build");
    expect(updated.alias).toBe("deploy 构建机");
    expect(updated.incarnation).toBe("inc-1");
    expect(updated.host).toBe("web-01");
    expect(updated.cwd).toBe("/srv/app");
  });

  it("propagates a stale-identity rename conflict and rejects unknown handles", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") return Promise.resolve([rosterRow()]);
      if (cmd === "ai_roster_rename") {
        return Promise.reject({ code: "conflict", message: "roster 精确身份不匹配" });
      }
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    const api = createAgentRosterApi(deps());
    await api.list();
    await expect(api.rename("build", "deploy")).rejects.toMatchObject({ code: "conflict" });
    await expect(api.rename("ghost", "deploy")).rejects.toMatchObject({ code: "unsupported" });
  });

  it("keeps the attachment target evidence for later mutations", async () => {
    const attachment: RosterAttachmentDTO = {
      entry: rosterEntry({ jobId: "job-live" }),
      resolution: "connected",
      assetId: "asset-1",
      host: "prod.example",
      cwd: "/var/lib/app",
    };
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") return Promise.resolve([rosterRow({ host: "", cwd: "" })]);
      if (cmd === "ai_roster_attach") return Promise.resolve(attachment);
      if (cmd === "ai_roster_rename") return Promise.resolve(rosterEntry({ alias: "renamed" }));
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    const api = createAgentRosterApi(deps());
    await api.list();
    await api.attach("build");
    const updated = await api.rename("build", "renamed");
    expect(updated.host).toBe("prod.example");
    expect(updated.cwd).toBe("/var/lib/app");
  });

  it("delegates approval resolution to the production flow", async () => {
    const resolveApproval = vi.fn(async () => undefined);
    const api = createAgentRosterApi(deps({ resolveApproval }));
    await api.resolveApproval("req-1", "approve_session");
    expect(resolveApproval).toHaveBeenCalledWith("req-1", "approve_session");
  });
});
