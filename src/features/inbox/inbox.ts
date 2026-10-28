import { useMemo } from "react";
import { useUi, useWorkspaceList, type AppTab, type Workspace } from "../../app/store";
import { takeOverAggregateSession } from "../terminal/backgroundSessionsAccess";
import {
  awarenessBadge,
  markAwarenessSeenLocally,
  needsAttention,
  toAwareness,
  useAwarenessStore,
  type AwarenessBadge,
  type SessionAwareness,
} from "../../app/sessionAwarenessStore";
import { isSeenUnsupported } from "../../app/sessionAwareness";
import { aggregateRows, useSessionAggregate, type AggregateRow } from "../../app/sessionAggregate";
import { terminalApi } from "../../ipc/commands";
import { hideMobileHome } from "../../app/shell/mobileHome";
import type { AggregateHost } from "../../ipc/generated/types";

// An inbox entry is a terminal session whose needs_me or just_finished state nobody has seen yet, on any device. The
// inbox is derived from the attention states the backend keeps, so it survives reloads and empties on every device
// once the state is seen anywhere.
export interface InboxEntry {
  // The backend terminal tab id.
  tabId: string;
  // The UI tab showing the session on this client; absent when no workspace here holds it.
  uiTabId?: string;
  workspaceId?: string;
  title: string;
  place: string;
  awareness: SessionAwareness;
  badge: AwarenessBadge;
  detail: string;
  // The session's host from the cross-host listing; a session no tab here shows is opened through it.
  host?: AggregateHost;
  // No tab in this app reports on the session; only the cross-host listing knows it.
  listedOnly?: boolean;
}

interface TabLocation {
  tab: AppTab;
  workspace: Workspace;
}

function locateTerminalTabs(workspaces: readonly Workspace[]): Map<string, TabLocation> {
  const byTabId = new Map<string, TabLocation>();
  for (const workspace of workspaces) {
    for (const pane of workspace.panes) {
      for (const tab of pane.tabs) {
        if (tab.kind === "terminal" && tab.tabId && !byTabId.has(tab.tabId)) byTabId.set(tab.tabId, { tab, workspace });
      }
    }
  }
  return byTabId;
}

const NEEDS_ME_REASON: Record<string, string> = {
  bell: "终端响铃",
  notify: "程序发出通知",
};

// attentionDetail describes why the session is in the inbox, with the program's own notification text when it sent one.
export function attentionDetail(awareness: SessionAwareness): string {
  if (awareness.state === "just_finished") {
    const code = awareness.exitCode;
    if (typeof code !== "number") return "命令已结束";
    return code === 0 ? "命令已成功结束" : `命令已结束，退出码 ${code}`;
  }
  const text = [awareness.title, awareness.body].filter(Boolean).join("：");
  if (text) return text;
  return NEEDS_ME_REASON[awareness.reason ?? ""] ?? "需要你处理";
}

function sinceTime(awareness: SessionAwareness): number {
  const time = awareness.since ? Date.parse(awareness.since) : Number.NaN;
  return Number.isNaN(time) ? 0 : time;
}

// buildInbox lists the unseen needs_me and just_finished sessions, needs_me first and the newest first within a kind.
export function buildInbox(
  byTabId: ReadonlyMap<string, SessionAwareness>,
  workspaces: readonly Workspace[],
  aggregate: readonly AggregateRow[] = [],
): InboxEntry[] {
  const located = locateTerminalTabs(workspaces);
  const entries: InboxEntry[] = [];
  // The cross-host listing names the host of a session this client holds no tab for, so it can be opened there.
  const listed = new Map(aggregate.map((row) => [row.session.tabId, row]));
  for (const [tabId, awareness] of byTabId) {
    if (!needsAttention(awareness)) continue;
    const badge = awarenessBadge(awareness);
    if (!badge) continue;
    const where = located.get(tabId);
    if (where?.tab.exited) continue;
    const row = listed.get(tabId);
    entries.push({
      tabId,
      uiTabId: where?.tab.id,
      workspaceId: where?.workspace.id,
      title: where?.tab.title ?? (row?.session.title || "未在此端打开的终端"),
      place: where?.workspace.title ?? row?.host.name ?? "其他窗口或设备",
      awareness,
      badge,
      detail: attentionDetail(awareness),
      ...(row ? { host: row.host } : {}),
    });
  }
  // A session no tab here reports on comes from the cross-host listing; a tab's own state wins whenever it has one.
  for (const { session, host } of aggregate) {
    const known = byTabId.get(session.tabId);
    if (known && known.state !== "unknown") continue;
    const awareness = toAwareness(session.attention);
    if (!needsAttention(awareness)) continue;
    const badge = awarenessBadge(awareness);
    if (!badge) continue;
    const where = located.get(session.tabId);
    if (where?.tab.exited) continue;
    entries.push({
      tabId: session.tabId,
      uiTabId: where?.tab.id,
      workspaceId: where?.workspace.id,
      title: where?.tab.title ?? (session.title || "持久终端"),
      place: where?.workspace.title ?? host.name,
      awareness,
      badge,
      detail: attentionDetail(awareness),
      host,
      listedOnly: true,
    });
  }
  const rank = (entry: InboxEntry) => (entry.awareness.state === "needs_me" ? 0 : 1);
  return entries.sort((a, b) => rank(a) - rank(b) || sinceTime(b.awareness) - sinceTime(a.awareness));
}

export function useInboxEntries(): InboxEntry[] {
  const byTabId = useAwarenessStore((state) => state.byTabId);
  const workspaces = useWorkspaceList();
  const hosts = useSessionAggregate((state) => state.hosts);
  const sessionsByHost = useSessionAggregate((state) => state.sessionsByHost);
  return useMemo(
    () => buildInbox(byTabId, workspaces, aggregateRows({ hosts, sessionsByHost })),
    [byTabId, workspaces, hosts, sessionsByHost],
  );
}

// uiTabForBackendTab finds the UI tab that shows the backend tab on this client.
export function uiTabForBackendTab(tabId: string): string | undefined {
  return locateTerminalTabs(useUi.getState().workspaces).get(tabId)?.tab.id;
}

// openInboxEntry shows the session's tab, switching workspace and pane. It reports false when no tab here shows it.
export function openInboxTab(tabId: string): boolean {
  const uiTabId = uiTabForBackendTab(tabId);
  if (!uiTabId) return false;
  hideMobileHome();
  useUi.getState().setActiveTab(uiTabId);
  return true;
}

// openAggregateEntry opens a session this client knows only from the cross-host listing as a tab: through its host's
// connected session, or, for a device no workspace here holds, by opening the device first. It resolves to null once
// the tab is open, or to the reason it could not be.
export async function openAggregateEntry(entry: Pick<InboxEntry, "tabId" | "title" | "host">): Promise<string | null> {
  if (!entry.host) return "此端没有打开这个终端";
  const result = await takeOverAggregateSession(entry.host, entry.tabId, entry.title);
  if (result.ok) return null;
  return "canceled" in result ? null : result.error;
}

// markInboxEntrySeen clears the entry on every device through the backend's seen mark. A helper that cannot share
// the mark (state detection is off) clears it on this device only; other errors are transient and thrown for a retry.
export async function markInboxEntrySeen(entry: Pick<InboxEntry, "tabId" | "awareness">): Promise<void> {
  const since = entry.awareness.since;
  if (!since) return;
  try {
    await terminalApi.attentionSeen(entry.tabId, since);
  } catch (error) {
    if (!isSeenUnsupported(error)) throw error;
    markAwarenessSeenLocally(entry.tabId, since);
  }
}
