import { useQuery } from "@tanstack/react-query";
import { useUi, useWorkspaceList, type Workspace } from "../store";
import {
  backgroundTargets,
  listBackgroundTabs,
  openTerminalTabIds,
  takeOverBackgroundTab,
  visibleBackgroundTabs,
  workspaceSessionMatches,
  type BackgroundScope,
  type BackgroundTab,
} from "../../features/terminal/backgroundSessionsAccess";
import { getLocalHelperStatus, localDurableEnabled } from "../../features/terminal/localHelperStatus";
import { tabRecency } from "./tabRecency";

const BACKGROUND_STALE_MS = 5_000;

export type JumpSessionTarget =
  | { source: "tab"; workspaceId: string; tabId: string; current: boolean; ended: boolean }
  | { source: "background"; tab: BackgroundTab };

export interface JumpSessionEntry {
  key: string;
  label: string;
  hint: string;
  keywords: string[];
  recency: number;
  // sessionId and terminalId identify the session for attention lookups.
  sessionId: string | null;
  terminalId: string | null;
  target: JumpSessionTarget;
}

// openTabSessions lists every open terminal tab across workspaces. The tab in front ranks last, since
// jumping to it does nothing; the others follow activation recency.
export function openTabSessions(workspaces: Workspace[], activeWorkspaceId: string | null): JumpSessionEntry[] {
  const entries: JumpSessionEntry[] = [];
  const activeWorkspace = workspaces.find((workspace) => workspace.id === activeWorkspaceId);
  const activePane = activeWorkspace?.panes.find((pane) => pane.id === activeWorkspace.activePaneId) ?? activeWorkspace?.panes[0];
  const currentTabId = activePane?.activeTabId ?? null;
  for (const workspace of workspaces) {
    for (const pane of workspace.panes) {
      for (const tab of pane.tabs) {
        if (tab.kind !== "terminal") continue;
        const current = tab.id === currentTabId;
        const ended = Boolean(tab.dead || tab.exited);
        entries.push({
          key: `tab-${tab.id}`,
          label: tab.title,
          hint: [workspace.title, current ? "当前" : null, ended ? "已结束" : null].filter(Boolean).join(" · "),
          keywords: [workspace.title],
          recency: current ? Number.MIN_SAFE_INTEGER : tabRecency(tab.id),
          sessionId: tab.sessionId ?? workspace.sessionId ?? null,
          terminalId: tab.tabId ?? null,
          target: { source: "tab", workspaceId: workspace.id, tabId: tab.id, current, ended },
        });
      }
    }
  }
  return entries;
}

function outputAgo(ms: number | undefined): string | null {
  if (ms === undefined || !Number.isFinite(ms) || ms < 0) return null;
  if (ms < 60_000) return "刚有输出";
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)} 分钟前有输出`;
  if (ms < 86_400_000) return `${Math.floor(ms / 3_600_000)} 小时前有输出`;
  return `${Math.floor(ms / 86_400_000)} 天前有输出`;
}

const UNKNOWN_OUTPUT_AGE = 1e12;

// backgroundSessionEntries ranks background terminals after open tabs that were used, newest output first.
export function backgroundSessionEntries(tabs: BackgroundTab[]): JumpSessionEntry[] {
  return tabs.map((tab) => ({
    key: `background-${JSON.stringify([tab.sessionId, tab.tabId])}`,
    label: tab.title || `${tab.sessionName} 的终端`,
    hint: ["后台", tab.sessionName, outputAgo(tab.lastOutputMsAgo)].filter(Boolean).join(" · "),
    keywords: [tab.sessionName, "后台"],
    recency: -1 - Math.min(tab.lastOutputMsAgo ?? UNKNOWN_OUTPUT_AGE, UNKNOWN_OUTPUT_AGE),
    sessionId: tab.sessionId,
    terminalId: tab.tabId,
    target: { source: "background", tab },
  }));
}

export interface BackgroundJumpState {
  tabs: BackgroundTab[];
  // unlisted counts the listings that failed; the background sessions page reports their details.
  unlisted: number;
}

const NO_BACKGROUND: BackgroundJumpState = { tabs: [], unlisted: 0 };

// useBackgroundJumpSessions lists takeable background terminals of every connected host. The listing is cached
// per runtime epoch and connected session set, so reopening the palette right away does not list again.
export function useBackgroundJumpSessions(enabled: boolean): BackgroundJumpState {
  const epoch = useUi((s) => s.workspaceRuntimeEpoch);
  const connected = useUi((s) =>
    s.sessions
      .filter((session) => session.status === "connected")
      .map((session) => session.id)
      .sort()
      .join("\n"),
  );
  const query = useQuery({
    queryKey: ["jump-background", epoch, connected],
    enabled: enabled && connected.length > 0,
    staleTime: BACKGROUND_STALE_MS,
    retry: false,
    refetchOnWindowFocus: false,
    queryFn: async (): Promise<BackgroundJumpState> => {
      const st = useUi.getState();
      const scope: BackgroundScope = {
        sessions: st.sessions,
        workspaces: st.workspaces,
        verified: {},
        localDurable: localDurableEnabled(getLocalHelperStatus()),
      };
      const targets = backgroundTargets(scope, { showAll: true, hostSessionId: null, remoteOwner: false });
      const { collected, liveFailed } = await listBackgroundTabs(scope, targets, openTerminalTabIds(st.workspaces));
      return {
        tabs: visibleBackgroundTabs(collected.list, scope, { hostSessionId: null, showAll: true, blocked: false }),
        unlisted: collected.failedRemote.length + (liveFailed ? 1 : 0),
      };
    },
  });
  const workspaces = useWorkspaceList();
  const data = enabled && connected.length > 0 ? (query.data ?? NO_BACKGROUND) : NO_BACKGROUND;
  // A cached listing may predate a takeover, so terminals opened since are left out.
  const open = openTerminalTabIds(workspaces);
  return data.tabs.some((tab) => open.has(tab.tabId)) ? { ...data, tabs: data.tabs.filter((tab) => !open.has(tab.tabId)) } : data;
}

// The palette lists every host, so a host qualifies while an asset or a matching open workspace still owns it.
function ownedByApp(session: { id: string; assetId: string | null; kind: string }): boolean {
  if (session.assetId) return true;
  return useUi.getState().workspaces.some((workspace) =>
    workspace.kind === "session" && workspace.sessionId === session.id && workspaceSessionMatches(workspace, session, {}));
}

// jumpToSession brings an open tab to the front or takes a background terminal over into its host workspace.
export async function jumpToSession(target: JumpSessionTarget): Promise<void> {
  if (target.source === "tab") {
    const st = useUi.getState();
    if (!st.workspaces.some((workspace) => workspace.id === target.workspaceId)) return;
    st.setActiveTab(target.tabId);
    return;
  }
  const result = await takeOverBackgroundTab(target.tab, {
    guard: (session) => {
      if (!ownedByApp(session)) throw new Error("这个终端的主机已不在工作区中，请重新连接后再接管");
    },
  });
  const { pushToast } = useUi.getState();
  if (result.ok) pushToast("info", `已接管「${result.sessionName}」的终端`);
  else if ("error" in result) pushToast("error", `接管失败：${result.error}`);
}
