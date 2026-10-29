// Production wiring between the reviewed agent UI contracts and the real
// generated IPC surface (ai_roster_*).
// Every value crossing the seam is validated instead of cast; what the
// backend cannot evidence stays unknown instead of being invented. Rename is
// the M131 alias-only command under the exact durable identity CAS — the
// permanent handle never changes — and host/cwd are the backend's verifiable
// target evidence (store-asset host, OSC 7 directory), empty when unknown.

import { assetApi, call, type AppError } from "../../ipc/commands";
import type {
  RosterEntry,
  RosterListEntry,
} from "../../ipc/generated/types";
import { assetEnvironment } from "../production/assetEnvironment";
import {
  isProductionEnvironment,
  productionSettingsApi,
} from "../production/productionSettings";
import type {
  AgentRosterApi,
} from "./agentApi";
import type {
  AgentApprovalDecision,
  AgentEvidence,
  AgentPresence,
  AgentRosterEntry,
} from "./agentTypes";

export interface AgentProductionIdentity {
  ownerId: string;
  ownerName: string;
}

export interface AgentRosterProductionDeps {
  conversationId: string;
  identity: AgentProductionIdentity;
  // activeJobId reports the conversation's currently bound AI job ("" when
  // idle): roster attach re-binds the attaching job through the real IPC.
  activeJobId: () => string;
  // resolveApproval is supplied by the AiSidebar production flow: it maps the
  // reviewed approval decisions onto the pending HITL interaction (ai_confirm).
  resolveApproval: (requestId: string, decision: AgentApprovalDecision) => Promise<void>;
}

function unsupported(message: string): AppError {
  return { code: "unsupported", message };
}

// The roster contract's own fail-closed fallback (roster_ipc.go): any
// resolution the backend cannot classify authoritatively is unverifiable,
// never invented presence.
const ROSTER_PRESENCE: Record<string, AgentPresence> = {
  connected: "attached",
  disconnected: "disconnected",
  gone: "gone",
  replaced: "replaced",
  exited: "exited",
  unverifiable: "unverifiable",
  nonpersistent: "nonpersistent",
};

function rosterPresenceOf(value: string): AgentPresence {
  return ROSTER_PRESENCE[value] ?? "unverifiable";
}

const PERSISTED_TERMINAL_STATES = new Set(["released", "gone", "exited", "replaced"]);

// rosterEntryEvidence surfaces the backend's authoritative terminal verdicts
// as evidence with provenance; non-terminal presences carry none. A persisted
// terminal state is evidenced by the row's own write timestamps; a probe
// verdict on a live row is evidenced by when the listing observed it.
function rosterEntryEvidence(
  entry: RosterEntry,
  presence: AgentPresence,
  observedAt: number,
): AgentEvidence | null {
  if (presence === "gone" && entry.state === "released" && entry.releasedAt !== null) {
    return { kind: "release", observedAt: entry.releasedAt, summary: "已确认释放" };
  }
  const label =
    presence === "gone" ? "已不存在" : presence === "replaced" ? "已被替换" : presence === "exited" ? "已退出" : null;
  if (label === null) return null;
  if (PERSISTED_TERMINAL_STATES.has(entry.state)) {
    return { kind: "reconcile", observedAt: entry.updatedAt, summary: `${label}（已记录）` };
  }
  return { kind: "reconcile", observedAt, summary: `${label}（已检测确认）` };
}

interface RosterMappingContext {
  observedAt: number;
  environmentByAsset: Map<string, string>;
  configured: readonly string[];
  identity: AgentProductionIdentity;
}

function toRosterEntryView(row: RosterListEntry, ctx: RosterMappingContext): AgentRosterEntry {
  const { entry } = row;
  const presence = rosterPresenceOf(row.presence);
  const environment = row.assetId === "" ? "" : ctx.environmentByAsset.get(row.assetId) ?? "";
  return {
    handle: entry.handle,
    alias: entry.alias,
    // Verifiable target evidence from the backend (store-asset host, OSC 7
    // cwd); empty stays honestly unknown and is never substituted.
    host: row.host,
    cwd: row.cwd,
    ownerId: ctx.identity.ownerId,
    ownerName: ctx.identity.ownerName,
    // The roster IPC surface is owner-isolated: cross-owner rows fail closed
    // server-side, so every listed row belongs to the caller.
    isMine: true,
    production: environment !== "" && isProductionEnvironment(environment, ctx.configured),
    persistent: true,
    incarnation: entry.incarnation,
    presence,
    evidence: rosterEntryEvidence(entry, presence, ctx.observedAt),
    releasePending: entry.state === "release_pending",
    tabId: entry.tabId === "" ? null : entry.tabId,
    lastRun: null,
  };
}

export function createAgentRosterApi(deps: AgentRosterProductionDeps): AgentRosterApi {
  // Raw rows behind the latest delivered listing: attach/rename/release need
  // the exact durable identity (createdAt + incarnation) the reviewed UI
  // contract deliberately does not carry, and open needs the preassigned tab.
  let rawByHandle = new Map<string, RosterListEntry>();
  // The latest listing's mapping context lets post-mutation responses be
  // re-projected with the same owner/environment classification.
  let lastCtx: RosterMappingContext | null = null;

  const rawEntry = (handle: string): RosterListEntry => {
    const row = rawByHandle.get(handle);
    if (!row) {
      throw unsupported(`找不到子代理 ${handle}，请先刷新列表`);
    }
    return row;
  };

  return {
    async list(): Promise<AgentRosterEntry[]> {
      const observedAt = Date.now();
      // Asset and settings lookups only enrich the production badge: when
      // they are unreachable the roster still renders with the badge off
      // rather than the whole panel failing on secondary data.
      const [rows, assets, settings] = await Promise.all([
        call("ai_roster_list", { conversationId: deps.conversationId }),
        assetApi.list().catch(() => null),
        productionSettingsApi.get().catch(() => null),
      ]);
      const environmentByAsset = new Map<string, string>();
      for (const asset of assets ?? []) {
        const environment = assetEnvironment(typeof asset.tags === "string" ? asset.tags : "");
        if (environment !== "") environmentByAsset.set(asset.id, environment);
      }
      const configured =
        settings && Array.isArray(settings.productionEnvironments) ? settings.productionEnvironments : [];
      const ctx: RosterMappingContext = {
        observedAt,
        environmentByAsset,
        configured,
        identity: deps.identity,
      };
      const nextRaw = new Map<string, RosterListEntry>();
      const entries = rows.map((row) => {
        nextRaw.set(row.entry.handle, row);
        return toRosterEntryView(row, ctx);
      });
      rawByHandle = nextRaw;
      lastCtx = ctx;
      return entries;
    },

    async open(handle: string): Promise<{ tabId: string }> {
      const row = rawEntry(handle);
      if (row.entry.tabId === "") {
        throw unsupported(`子代理 ${handle} 无可打开的终端标签页`);
      }
      return { tabId: row.entry.tabId };
    },
    async attach(handle: string): Promise<{ tabId: string }> {
      rawEntry(handle);
      const attachment = await call("ai_roster_attach", { handle, jobId: deps.activeJobId() });
      const resolution = attachment.resolution;
      if (resolution === "connected") {
        const current = rawByHandle.get(handle);
        if (current) {
          rawByHandle.set(handle, {
            ...current,
            entry: attachment.entry,
            assetId: attachment.assetId,
            host: attachment.host,
            cwd: attachment.cwd,
          });
        }
        if (attachment.entry.tabId === "") {
          throw unsupported(`子代理 ${handle} 已连接，但无可打开的终端标签页`);
        }
        return { tabId: attachment.entry.tabId };
      }
      if (resolution === "nonpersistent") {
        throw unsupported(`子代理 ${handle} 不在持久终端中，无法附加`);
      }
      const text =
        resolution === "disconnected"
          ? "终端已断开"
          : resolution === "gone"
            ? "终端已不存在"
            : resolution === "replaced"
              ? "终端已被新会话替换"
              : resolution === "exited"
                ? "终端已退出"
                : "无法确认终端状态";
      throw unsupported(`未能附加子代理 ${handle}：${text}`);
    },

    // Alias-only rename through the real command: the permanent handle and
    // incarnation survive, and the exact durable identity (createdAt +
    // incarnation) guards the CAS so a stale listing conflicts instead of
    // renaming a replacement row.
    async rename(handle: string, nextAlias: string): Promise<AgentRosterEntry> {
      const row = rawEntry(handle);
      const updated = await call("ai_roster_rename", {
        handle,
        alias: nextAlias,
        createdAt: row.entry.createdAt,
        incarnation: row.entry.incarnation,
      });
      const merged: RosterListEntry = { ...row, entry: updated };
      rawByHandle.set(handle, merged);
      const ctx = lastCtx;
      if (!ctx) {
        throw unsupported("列表已过期，请刷新后重试");
      }
      return toRosterEntryView(merged, ctx);
    },

    async release(handle: string): Promise<void> {
      const row = rawEntry(handle);
      await call("ai_roster_release", {
        handle,
        createdAt: row.entry.createdAt,
        incarnation: row.entry.incarnation,
      });
    },

    resolveApproval(requestId: string, decision: AgentApprovalDecision): Promise<void> {
      return deps.resolveApproval(requestId, decision);
    },
  };
}
