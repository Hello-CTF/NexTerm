import type { Workspace } from "../../app/store";
import {
  awarenessBadge,
  toAwareness,
  type AwarenessBadge,
  type AwarenessState,
  type SessionAwareness,
} from "../../app/sessionAwarenessStore";
import type { AggregateRow } from "../../app/sessionAggregate";
import type { AggregateHost, AggregateHostStatus } from "../../ipc/generated/types";
import { attentionDetail } from "../inbox/inbox";

// The status wall is the mobile home: every session this client knows, from its own tabs and the cross-host listing,
// grouped by what it asks of the user. The order of the groups is fixed.
export const WALL_GROUPS = ["needs_me", "running", "finished", "unknown"] as const;
export type WallGroup = (typeof WALL_GROUPS)[number];

export const WALL_GROUP_LABELS: Record<WallGroup, string> = {
  needs_me: "需要我",
  running: "运行中",
  finished: "已结束",
  unknown: "状态未知",
};

const STATE_LABELS: Record<AwarenessState, string> = {
  needs_me: "需要我",
  running: "运行中",
  idle: "空闲",
  just_finished: "刚完成",
  exited: "已退出",
  unknown: "状态未知",
};

export interface WallCard {
  // The backend terminal tab id; the card's key.
  tabId: string;
  // The UI tab showing the session on this client; absent when no workspace here holds it.
  uiTabId?: string;
  title: string;
  hostName: string;
  state: AwarenessState;
  group: WallGroup;
  stateLabel: string;
  badge: AwarenessBadge | null;
  // Why it needs the user or how it finished; absent for other states.
  detail?: string;
  // When the state began (or the last output, when only that is known), in ms since the epoch; 0 when unknown.
  at: number;
  awareness?: SessionAwareness;
  // The host of a session this client knows only from the cross-host listing; it is opened through that host.
  host?: AggregateHost;
  // The host's listing is older than the backend's cache allows, so the state may have moved on.
  stale: boolean;
}

export interface WallHostNote {
  key: string;
  name: string;
  status: "offline" | "failed" | "pending";
  error?: string;
  errorCode?: string;
}

export interface StatusWallModel {
  groups: Record<WallGroup, WallCard[]>;
  // Hosts whose sessions are not known right now.
  hosts: WallHostNote[];
}

export function wallGroupOf(state: AwarenessState): WallGroup {
  switch (state) {
    case "needs_me":
      return "needs_me";
    case "running":
    case "idle":
      return "running";
    case "just_finished":
    case "exited":
      return "finished";
    default:
      return "unknown";
  }
}

// wallBadge is the tab strip's badge for a state. A card keeps it after the state was seen: the wall shows the
// state itself, while the inbox and the tab strip show only what is new.
function wallBadge(awareness: SessionAwareness | undefined, state: AwarenessState): AwarenessBadge | null {
  if (state === "exited") {
    // A check or a cross only when the exit code says which.
    const code = awareness?.exitCode;
    if (typeof code !== "number") return null;
    return code === 0
      ? { kind: "just_finished", label: "已退出，成功", outcome: "ok" }
      : { kind: "just_finished", label: `已退出，退出码 ${code}`, outcome: "failed" };
  }
  if (!awareness) return state === "unknown" ? awarenessBadge({ state, seen: false, notable: false, version: 0 }) : null;
  return awarenessBadge({ ...awareness, state, seen: false });
}

function timeOf(awareness: SessionAwareness | undefined, fallback: number | undefined): number {
  const since = awareness?.since ? Date.parse(awareness.since) : Number.NaN;
  if (!Number.isNaN(since)) return since;
  return fallback && fallback > 0 ? fallback : 0;
}

function card(input: {
  tabId: string;
  uiTabId?: string;
  title: string;
  hostName: string;
  awareness?: SessionAwareness;
  // Set when the session is known to have ended.
  ended?: boolean;
  lastOutputAt?: number;
  host?: AggregateHost;
  stale?: boolean;
}): WallCard {
  const { awareness } = input;
  const state: AwarenessState = input.ended ? "exited" : (awareness?.state ?? "unknown");
  const detail = awareness && (state === "needs_me" || state === "just_finished") ? attentionDetail(awareness) : undefined;
  return {
    tabId: input.tabId,
    uiTabId: input.uiTabId,
    title: input.title,
    hostName: input.hostName,
    state,
    group: wallGroupOf(state),
    stateLabel: STATE_LABELS[state],
    badge: wallBadge(awareness, state),
    detail,
    at: timeOf(awareness, input.lastOutputAt),
    awareness,
    host: input.host,
    stale: input.stale === true,
  };
}

// buildStatusWall lists each session once: a tab here first, with its own state when it has one and the listing's
// otherwise; then the listing's sessions no tab here holds. Within a group the newest state comes first.
export function buildStatusWall(
  byTabId: ReadonlyMap<string, SessionAwareness>,
  workspaces: readonly Workspace[],
  aggregate: readonly AggregateRow[],
  hosts: ReadonlyMap<string, AggregateHostStatus>,
): StatusWallModel {
  const listed = new Map<string, AggregateRow>();
  for (const row of aggregate) listed.set(row.session.tabId, row);
  const known = (tabId: string): SessionAwareness | undefined => {
    const own = byTabId.get(tabId);
    if (own && own.state !== "unknown") return own;
    const row = listed.get(tabId);
    return row ? toAwareness(row.session.attention) : own;
  };

  const cards: WallCard[] = [];
  const seen = new Set<string>();
  for (const workspace of workspaces) {
    if (workspace.kind !== "session") continue;
    for (const pane of workspace.panes) {
      for (const tab of pane.tabs) {
        if (tab.kind !== "terminal" || !tab.tabId || seen.has(tab.tabId)) continue;
        seen.add(tab.tabId);
        const row = listed.get(tab.tabId);
        cards.push(card({
          tabId: tab.tabId,
          uiTabId: tab.id,
          title: tab.title,
          hostName: workspace.title,
          awareness: known(tab.tabId),
          ended: tab.exited === true || tab.dead === true || row?.session.dead === true,
          lastOutputAt: row?.session.lastOutputAt,
          stale: row?.host.stale === true,
        }));
      }
    }
  }
  for (const row of aggregate) {
    const { session, host } = row;
    if (seen.has(session.tabId)) continue;
    seen.add(session.tabId);
    cards.push(card({
      tabId: session.tabId,
      title: session.title || "持久终端",
      hostName: host.name,
      awareness: known(session.tabId),
      ended: session.dead === true,
      lastOutputAt: session.lastOutputAt,
      host,
      stale: host.stale === true,
    }));
  }

  const groups: Record<WallGroup, WallCard[]> = { needs_me: [], running: [], finished: [], unknown: [] };
  for (const entry of cards) groups[entry.group].push(entry);
  for (const group of WALL_GROUPS) groups[group].sort((a, b) => b.at - a.at);

  const notes: WallHostNote[] = [];
  for (const host of hosts.values()) {
    if (host.status === "offline" || host.status === "failed" || host.status === "pending") {
      notes.push({ key: host.key, name: host.name, status: host.status, error: host.error, errorCode: host.errorCode });
    }
  }
  return { groups, hosts: notes };
}
