import { useEffect, useState } from "react";
import { sessionApi, terminalApi, type LiveTabInfo } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { nextTabId, useUi, type Workspace } from "../store";
import {
  backgroundTargets,
  collectBackgroundTabs,
  openTerminalTabIds,
  visibleBackgroundTabs,
  type BackgroundScope,
  type BackgroundTab,
} from "../../features/terminal/backgroundSessionsAccess";
import { getLocalHelperStatus, localDurableEnabled } from "../../features/terminal/localHelperStatus";
import { tabRecency } from "./tabRecency";

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
  error: string | null;
}

// useBackgroundJumpSessions lists takeable background terminals of every connected host once per open.
export function useBackgroundJumpSessions(enabled: boolean): BackgroundJumpState {
  const [state, setState] = useState<BackgroundJumpState>({ tabs: [], error: null });
  useEffect(() => {
    if (!enabled) return;
    const st = useUi.getState();
    if (!st.sessions.some((session) => session.status === "connected")) return;
    const scope: BackgroundScope = {
      sessions: st.sessions,
      workspaces: st.workspaces,
      verified: {},
      localDurable: localDurableEnabled(getLocalHelperStatus()),
    };
    const targets = backgroundTargets(scope, { showAll: true, hostSessionId: null, remoteOwner: false });
    let cancelled = false;
    void Promise.allSettled([
      Promise.resolve().then(() => terminalApi.listLive()),
      ...targets.map((session) => Promise.resolve().then(() => terminalApi.listDurable(session.id))),
    ]).then(([live, ...remote]) => {
      if (cancelled) return;
      const collected = collectBackgroundTabs(scope, {
        live: live.status === "fulfilled" ? (live.value as LiveTabInfo[]) : null,
        remote: targets.map((session, index) => {
          const result = remote[index];
          return {
            session,
            tabs: result.status === "fulfilled" ? result.value : null,
            reason: result.status === "rejected" ? result.reason : undefined,
          };
        }),
        openTabIds: openTerminalTabIds(useUi.getState().workspaces),
      });
      const errors = [
        ...(live.status === "rejected" ? [describeError(live.reason)] : []),
        ...collected.failedRemote.map((failure) => `${failure.session.name}: ${describeError(failure.reason)}`),
      ];
      setState({
        tabs: visibleBackgroundTabs(collected.list, scope, { hostSessionId: null, showAll: true, blocked: false }),
        error: errors.length > 0 ? errors.join("; ") : null,
      });
    });
    return () => {
      cancelled = true;
    };
  }, [enabled]);
  return state;
}

// jumpToSession brings an open tab to the front or takes a background terminal over into its host workspace.
export async function jumpToSession(target: JumpSessionTarget): Promise<void> {
  if (target.source === "tab") {
    const st = useUi.getState();
    if (!st.workspaces.some((workspace) => workspace.id === target.workspaceId)) return;
    st.setActiveTab(target.tabId);
    return;
  }
  const info = target.tab;
  const { pushToast } = useUi.getState();
  try {
    const list = await sessionApi.list();
    const session = list.find((item) => item.id === info.sessionId);
    if (!session || session.status !== "connected") throw new Error("主机未连接，请先重新连接再接管");
    const st = useUi.getState();
    st.setSessions(list);
    st.addTab({
      id: nextTabId(`term-${info.sessionId}`),
      kind: "terminal",
      title: info.title || session.name || info.sessionName,
      ...(info.title && info.titleCustom && info.titleChangedAt
        ? { renamedByUser: true, customTitleAt: info.titleChangedAt }
        : {}),
      sessionId: info.sessionId,
      tabId: info.tabId,
      closable: true,
    });
    pushToast("info", `已接管「${session.name}」的终端`);
  } catch (error) {
    pushToast("error", `接管失败：${describeError(error)}`);
  }
}
