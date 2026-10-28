import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { syncApi, terminalApi, type SessionAttentionState } from "../../ipc/commands";
import {
  aggregateBackgroundTabs,
  aggregateTerminatePath,
  backgroundTabKey,
  backgroundTargets,
  groupBackgroundTabs,
  listBackgroundTabs,
  openTerminalTabIds,
  resolveHostGate,
  takeOverAggregateSession,
  takeOverBackgroundTab,
  terminateAggregateSession,
  unknownHostText,
  visibleBackgroundTabs,
  workspaceHostKey,
  workspaceSessionMatches,
  type BackgroundHostGroup,
  type BackgroundScope,
  type BackgroundTab,
  type KnownDurableTab,
} from "./backgroundSessionsAccess";
import { TabAttentionBadge } from "../../app/workspace/tabStrip";
import type { AwarenessBadge } from "../../app/sessionAwarenessStore";
import type { AggregateHostStatus } from "../../ipc/generated/types";
import type { SessionInfo } from "../../ipc/commands";
import {
  reconnectDeadHost,
  reconnectWorkspaceSession,
  sanitizeRemoteTabTitle,
  useSessionList,
  useUi,
  type Workspace,
} from "../../app/store";
import { aggregateRows, forgetAggregateSession, useSessionAggregate } from "../../app/sessionAggregate";
import { apiBase, TRANSPORT } from "../../ipc/env";
import { listenEvent } from "../../ipc/events";
import { useAuth } from "../auth/store";
import { ask } from "../../ui/dialogs";
import { startPolling } from "../../app/usePolling";
import { describeError } from "../../ui/errorText";
import "../../ui/skeleton.css";
import "./backgroundSessions.css";
import { localDurableEnabled, useLocalHelperStatus } from "./localHelperStatus";
import {
  IconActivity,
  IconGamepad,
  IconRefresh,
  IconTerminal,
  IconTrash,
} from "../../ui/icons";

const POLL_MS = 5000;
const COLUMN_COUNT = 5;
const NO_WORKSPACES: Workspace[] = [];
const NO_HOSTS: ReadonlyMap<string, AggregateHostStatus> = new Map();

interface PendingTab {
  key: string;
  workspaceId?: string;
  hostName: string;
  title: string;
  waitingForConnection?: boolean;
}

const TAKEOVER_HELP = "「接管」会回到原来的终端，并补上你离开期间的输出，不会新开终端。";

function persistenceFootnote(scope: "remote" | "local" | "all"): string {
  if (scope === "local") {
    return `本机的后台终端只在应用运行期间保留，退出应用后会结束。${TAKEOVER_HELP}`;
  }
  if (TRANSPORT === "web") {
    return `持久终端在目标主机上运行，关闭页面或换一台设备后仍能找到。${TAKEOVER_HELP}`;
  }
  if (scope === "all") {
    return `SSH 和受管设备的持久终端由目标主机保持运行，退出应用后仍在；本机的后台终端会随应用退出而结束。${TAKEOVER_HELP}`;
  }
  return `持久终端由目标主机保持运行，退出应用或换一台设备后仍能找到。${TAKEOVER_HELP}`;
}

function summaryCopy(count: number): string {
  return `${count} 个可恢复`;
}

function ago(ms: number): string {
  if (!Number.isFinite(ms) || ms < 1000) return "刚刚";
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s} 秒前`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} 分钟前`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h} 小时前`;
  return `${Math.floor(h / 24)} 天前`;
}

const STATE_LABELS: Record<SessionAttentionState, string> = {
  running: "运行中",
  needs_me: "需要我",
  just_finished: "刚完成",
  idle: "空闲",
  exited: "已退出",
};

const STATE_HINTS: Record<SessionAttentionState, string> = {
  running: "终端最近一直有输出",
  needs_me: "程序在等你回应：响铃、发出通知或显示了提示",
  just_finished: "刚有命令运行结束",
  idle: "没有新的输出，也没有在等你回应",
  exited: "终端里的程序已经退出",
};

interface StatusView {
  label: string;
  hint: string;
  tone: "accent" | "muted" | "normal";
}

function statusView(tab: BackgroundTab): StatusView {
  const state = tab.state;
  if (state && Object.hasOwn(STATE_LABELS, state)) {
    return {
      label: STATE_LABELS[state],
      hint: STATE_HINTS[state],
      tone: state === "needs_me" ? "accent" : state === "exited" ? "muted" : "normal",
    };
  }
  // A state a newer helper added is reported, just not understood here.
  if (state) return { label: "—", hint: "未知状态", tone: "muted" };
  if (tab.exited) return { label: STATE_LABELS.exited, hint: STATE_HINTS.exited, tone: "muted" };
  return {
    label: "—",
    hint: tab.durable || tab.remote
      ? "这台主机上的 NexTerm 组件未开启状态检测"
      : "普通终端不会报告状态",
    tone: "muted",
  };
}

// backgroundBadge is the tab strip's badge for a row's state, so a session reads the same in both places. Unlike a
// tab, a row keeps its needs_me or just_finished badge after the state was seen: the list shows the state itself.
export function backgroundBadge(tab: Pick<BackgroundTab, "state" | "exitCode">): AwarenessBadge | null {
  switch (tab.state) {
    case "needs_me":
      return { kind: "needs_me", label: "需要你处理" };
    case "just_finished": {
      const code = tab.exitCode;
      const failed = typeof code === "number" && code !== 0;
      return { kind: "just_finished", label: failed ? `命令刚结束，退出码 ${code}` : "命令刚结束", outcome: failed ? "failed" : "ok" };
    }
    case "running":
      return { kind: "running", label: "运行中" };
    default:
      return null;
  }
}

// hostStatusText describes a host whose sessions the cross-host listing could not list just now.
function hostStatusText(status: AggregateHostStatus | undefined): string | null {
  switch (status?.status) {
    case "offline":
    case "failed":
      return unknownHostText(status);
    case "pending":
      return "正在查询…";
    case "ok":
      return status.stale ? "列表可能已过时" : null;
    default:
      return null;
  }
}

function terminateTitle(tab: BackgroundTab, sessions: readonly SessionInfo[]): string {
  if (!tab.aggregateHost) return tab.sessionId ? "结束这个终端里运行的程序，无法恢复" : "接管后才能在这里结束这个终端";
  switch (aggregateTerminatePath(tab.aggregateHost, sessions)) {
    case "direct":
      return "结束这个终端里运行的程序，无法恢复";
    case "connect_device":
      return "先连接这台设备，再结束这个终端里运行的程序，无法恢复";
    default:
      return "主机未连接：请先从资产树连接这台主机再结束";
  }
}

// HostGroupHeader names a host in the all-hosts view, with its listing status when its sessions are not all known.
function HostGroupHeader({ group }: { group: BackgroundHostGroup }) {
  const detail = [group.tabs.length > 0 ? `${group.tabs.length} 个` : null, hostStatusText(group.status)]
    .filter(Boolean)
    .join(" · ");
  return (
    <tr className="nx-bg-group" data-background-group={group.key} data-host-status={group.status?.status ?? "listed"}>
      <th scope="colgroup" colSpan={COLUMN_COUNT}>
        <div className="flex min-w-0 items-baseline gap-2">
          <span className="truncate font-medium text-[var(--nx-fg-strong)]" title={group.name} data-testid="background-host-name">
            {group.name}
          </span>
          <span className="truncate text-[11px] font-normal text-[var(--nx-fg-soft)]" data-testid="background-host-status">
            {detail}
          </span>
        </div>
      </th>
    </tr>
  );
}

// Hover tooltips do not reach touch screens, so the gaps a tooltip explains are also stated once below the table.
const NEEDS_ME_NOTE = "「需要我」表示程序在等你回应：响铃、发出通知或显示了需要选择的提示";

const STATUS_TONE_CLASS: Record<StatusView["tone"], string> = {
  accent: "font-medium text-blue-300",
  muted: "text-[var(--nx-fg-soft)]",
  normal: "text-[var(--nx-fg)]",
};

// Short, stable fallback name: the tail of a tab ID is its random part.
function shortTabId(tabId: string): string {
  const compact = tabId.replace(/[^0-9a-z]/gi, "");
  return (compact || tabId).slice(-6);
}

// The tab strip's rule: the backend reports the custom title when there is one, otherwise the
// program's title; either is sanitized the way the strip shows it.
export function backgroundTabName(tab: Pick<BackgroundTab, "tabId" | "title">): string {
  return sanitizeRemoteTabTitle(tab.title ?? "") || `终端 ${shortTabId(tab.tabId)}`;
}

function pad(value: number): string {
  return String(value).padStart(2, "0");
}

function absoluteTime(ms: number): string {
  const at = new Date(ms);
  return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())} ${pad(at.getHours())}:${pad(at.getMinutes())}:${pad(at.getSeconds())}`;
}

interface TimeView {
  output: string;
  detached: string;
  tooltip: string;
}

interface MomentView {
  // How long ago, on this device's clock.
  age: number;
  // When, on this device's clock unless hostClock.
  at: number;
}

// moment prefers the backend's age, measured on the clock that recorded the time, and rebases it
// to this device's clock; without an age the raw time is used as is.
function moment(at: number | undefined, msAgo: number | undefined, fetchedAt: number, now: number): MomentView | null {
  if (msAgo !== undefined && msAgo >= 0) {
    return { age: msAgo + Math.max(0, now - fetchedAt), at: fetchedAt - msAgo };
  }
  if (at !== undefined && at > 0) return { age: now - at, at };
  return null;
}

function timeView(tab: BackgroundTab, now: number): TimeView {
  const createdLabel = tab.createdOnHost ? "创建（主机时间）" : "创建";
  const created = tab.createdAt && tab.createdAt > 0 ? `${createdLabel}：${absoluteTime(tab.createdAt)}` : null;
  const noActivity =
    tab.lastOutputAt === undefined && tab.lastOutputMsAgo === undefined && !tab.attachedElsewhere &&
    tab.detachedAt === undefined && tab.detachedMsAgo === undefined;
  if (noActivity) return { output: "—", detached: "—", tooltip: created ?? "" };
  const fetchedAt = tab.fetchedAt ?? now;
  const clock = tab.hostClock ? "（主机时间）" : "";
  const lines: string[] = [];
  let output: string;
  if (tab.lastOutputAt !== undefined && tab.lastOutputAt <= 0) {
    output = "暂无输出";
    lines.push("最近输出：暂无");
  } else {
    const last = moment(tab.lastOutputAt, tab.lastOutputMsAgo, fetchedAt, now);
    output = `${ago(last?.age ?? 0)}有输出`;
    if (last) lines.push(`最近输出${clock}：${absoluteTime(last.at)}`);
  }
  let detached: string;
  const left = moment(tab.detachedAt, tab.detachedAt ? tab.detachedMsAgo : undefined, fetchedAt, now);
  if (tab.attachedElsewhere) {
    detached = "仍有设备连接着";
    lines.push("转入后台：仍有设备连接着这个终端");
  } else if (left && tab.detachedSinceOpen) {
    detached = `至少 ${ago(left.age)}转入后台`;
    lines.push(`转入后台：不晚于 ${absoluteTime(left.at)}（自本应用重新打开此终端起）`);
  } else if (left) {
    detached = `${ago(left.age)}转入后台`;
    lines.push(`转入后台${clock}：${absoluteTime(left.at)}`);
  } else {
    detached = "—";
    lines.push("转入后台：时间未知");
  }
  if (created) lines.push(created);
  return { output, detached, tooltip: lines.join("\n") };
}

export function BackgroundSessions({
  visible = true,
  workspaceId,
  sessionId,
}: {
  visible?: boolean;
  workspaceId?: string;
  sessionId?: string;
}) {
  const pushToast = useUi((s) => s.pushToast);
  const runtimeEpoch = useUi((s) => s.workspaceRuntimeEpoch);
  const webUserId = useAuth((s) => s.user?.id ?? null);
  const [desktopScope, setDesktopScope] = useState<{ epoch: number; value: string | null } | null>(null);
  const [desktopChecking, setDesktopChecking] = useState(false);
  const [knownState, setKnownState] = useState<{
    scope: string | null;
    epoch: number;
    tabs: Record<string, KnownDurableTab[]>;
  }>({ scope: null, epoch: runtimeEpoch, tabs: {} });
  const [quickState, setQuickState] = useState<{
    scope: string | null;
    epoch: number;
    sessions: Record<string, string>;
  }>({ scope: null, epoch: runtimeEpoch, sessions: {} });
  const [itemsScope, setItemsScope] = useState<string | null>(null);
  const accountScope = TRANSPORT === "web"
    ? (webUserId ? JSON.stringify([apiBase() || window.location.origin, webUserId]) : null)
    : desktopScope?.epoch === runtimeEpoch ? desktopScope.value : null;
  const memoryKnown = useMemo(
    () => (!desktopChecking && knownState.scope === accountScope && knownState.epoch === runtimeEpoch ? knownState.tabs : {}),
    [desktopChecking, knownState, accountScope, runtimeEpoch],
  );
  const verifiedQuickSessions = useMemo(
    () => (quickState.scope === accountScope && quickState.epoch === runtimeEpoch ? quickState.sessions : {}),
    [quickState, accountScope, runtimeEpoch],
  );
  useEffect(() => {
    if (TRANSPORT !== "desktop") return;
    let current = true;
    let request = 0;
    let unlisten: (() => void) | undefined;
    const refresh = (maskOldScope: boolean) => {
      const generation = ++request;
      if (maskOldScope) {
        loadGenerationRef.current += 1;
        ownerGenerationRef.current += 1;
        setDesktopChecking(true);
      }
      void Promise.all([syncApi.status(), syncApi.linkGet()]).then(([status, link]) => {
        if (current && generation === request) {
          const value = status.loggedIn && status.userId && status.username && link.url && link.username === status.username
            ? JSON.stringify([link.url, status.userId, status.username]) : null;
          // The poll usually finds the same scope; keep the state object so nothing re-renders.
          setDesktopScope((previous) => (previous?.epoch === runtimeEpoch && previous.value === value ? previous : { epoch: runtimeEpoch, value }));
          setDesktopChecking(false);
        }
      }).catch(() => {
        if (current && generation === request) {
          setDesktopScope((previous) => (previous?.epoch === runtimeEpoch && previous.value === null ? previous : { epoch: runtimeEpoch, value: null }));
          setDesktopChecking(false);
        }
      });
    };
    // A status event usually leaves the account unchanged; refresh(false) compares the scope first and only a real
    // change resets the list (via the accountScope effect), so ordinary events never blank or re-poll it.
    void listenEvent("sync://status", () => refresh(false)).then((off) => {
      if (!current) { off(); return; }
      unlisten = off;
      refresh(false);
    }).catch(() => { if (current) refresh(false); });
    // The first refresh is issued once the status listener is attached; polling only backs up missed events.
    const stopPoll = startPolling(() => refresh(false), { intervalMs: 10_000, immediate: false });
    return () => { current = false; request += 1; stopPoll(); unlisten?.(); };
  }, [runtimeEpoch]);
  const ownerWorkspace = useUi((s) => {
    const id = workspaceId ?? (sessionId === undefined ? s.activeWorkspaceId : undefined);
    return id === undefined ? undefined : s.workspaces.find((workspace) => workspace.id === id);
  });
  // Identity of the owner for loading: tab/title/split changes must not restart polling, so load() depends on this
  // primitive key and reads the live workspace from the store.
  const ownerKey = ownerWorkspace
    ? JSON.stringify([
      ownerWorkspace.id, ownerWorkspace.kind, ownerWorkspace.sessionId ?? null, ownerWorkspace.assetId ?? null,
      ownerWorkspace.assetKind ?? null, workspaceHostKey(ownerWorkspace) ?? null,
    ])
    : null;
  const ownerId = ownerWorkspace?.id;
  const workspaceSessionId =
    ownerWorkspace?.kind === "session" ? (ownerWorkspace.sessionId ?? null) : null;
  const hostSessionId = workspaceSessionId ?? sessionId ?? null;
  const showAll = hostSessionId === null && ownerWorkspace?.kind === "tools";
  // A single-host view follows only its own sessions; the all-hosts view follows every session and workspace.
  const sessions = useSessionList(showAll ? undefined : [hostSessionId, ownerWorkspace?.sessionId]);
  const workspaces = useUi((s) => (showAll ? s.workspaces : NO_WORKSPACES));
  const hostSession = sessions.find((session) => session.id === hostSessionId);
  const localHelper = useLocalHelperStatus(showAll || hostSession?.kind === "local");
  const { remoteOwner, identityMismatch: hostIdentityMismatch, needsReconnect } =
    resolveHostGate(ownerWorkspace, hostSession, verifiedQuickSessions);
  const hostName = hostSessionId
    ? (hostSession?.name ?? ownerWorkspace?.title ?? hostSessionId)
    : (ownerWorkspace?.title ?? null);
  const [items, setItems] = useState<BackgroundTab[] | null>(null);
  // What the direct listing covered, so the cross-host listing adds only the rest.
  const [covered, setCovered] = useState<{ listedSessionIds: Set<string>; coveredTabIds: Set<string> } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [reconnecting, setReconnecting] = useState(false);
  const [busy, setBusy] = useState<Record<string, "takeover" | "kill">>({});
  const loadGenerationRef = useRef(0);
  const ownerGenerationRef = useRef(0);
  const scopedItems = !desktopChecking && itemsScope === accountScope ? items : null;
  const scopedError = !desktopChecking && itemsScope === accountScope ? error : null;

  useEffect(() => {
    loadGenerationRef.current += 1;
    ownerGenerationRef.current += 1;
    setItems(null);
    setError(null);
    setBusy({});
    setReconnecting(false);
    setKnownState((previous) => previous.scope === accountScope && previous.epoch === runtimeEpoch
      ? previous : { scope: accountScope, epoch: runtimeEpoch, tabs: {} });
    setQuickState((previous) => previous.scope === accountScope && previous.epoch === runtimeEpoch
      ? previous : { scope: accountScope, epoch: runtimeEpoch, sessions: {} });
  }, [runtimeEpoch, accountScope]);

  const load = useCallback(async () => {
    const generation = ++loadGenerationRef.current;
    if (desktopChecking) {
      setItems(null);
      setError(null);
      setLoading(false);
      return;
    }
    if (!hostSessionId && !showAll) {
      setItems(null);
      setError(null);
      setLoading(false);
      return;
    }
    setLoading(true);
    setItemsScope(accountScope);
    try {
      const st = useUi.getState();
      // Fetches read the current session list: deduplication and host keys may involve other sessions.
      const scope: BackgroundScope = {
        sessions: st.sessions, workspaces: st.workspaces, verified: verifiedQuickSessions, localDurable: localDurableEnabled(localHelper),
      };
      const owner = ownerId === undefined ? undefined : st.workspaces.find((workspace) => workspace.id === ownerId);
      const targets = backgroundTargets(scope, { showAll, hostSessionId, owner, remoteOwner });
      const { collected, liveFailed, liveError } = await listBackgroundTabs(
        scope,
        targets,
        openTerminalTabIds(showAll ? st.workspaces : owner ? [owner] : []),
      );
      if (generation !== loadGenerationRef.current) return;
      const errors: string[] = [];
      if (liveFailed) errors.push(describeError(liveError));
      for (const failure of collected.failedRemote) errors.push(`${failure.session.name}: ${describeError(failure.reason)}`);
      const { list, knownTabsByHost, incompleteHosts } = collected;
      if (knownTabsByHost.size > 0) {
        setKnownState((previous) => {
          const previousTabs = previous.scope === accountScope && previous.epoch === runtimeEpoch ? previous.tabs : {};
          const updatedTabs: Record<string, KnownDurableTab[]> = {};
          for (const [hostKey, tabs] of knownTabsByHost) {
            const merged = new Map<string, KnownDurableTab>();
            if (incompleteHosts.has(hostKey)) {
              for (const tab of previousTabs[hostKey] ?? []) merged.set(tab.id, tab);
            }
            for (const tab of tabs.values()) merged.set(tab.id, tab);
            updatedTabs[hostKey] = [...merged.values()];
          }
          return { scope: accountScope, epoch: runtimeEpoch, tabs: { ...previousTabs, ...updatedTabs } };
        });
      }
      setItems(list);
      setCovered({ listedSessionIds: collected.listedSessionIds, coveredTabIds: collected.coveredTabIds });
      setError(errors.length > 0 ? errors.join("; ") : null);
    } catch (e) {
      if (generation === loadGenerationRef.current) setError(describeError(e));
    } finally {
      if (generation === loadGenerationRef.current) setLoading(false);
    }
    // ownerKey is a deliberate trigger: the owner's identity fields are read from the store inside load.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hostSessionId, showAll, remoteOwner, ownerKey, ownerId, accountScope, runtimeEpoch, desktopChecking, verifiedQuickSessions, localHelper]);

  useEffect(() => {
    ownerGenerationRef.current += 1;
    setItems(null);
    setError(null);
    setBusy({});
    setReconnecting(false);
    return () => {
      ownerGenerationRef.current += 1;
    };
  }, [workspaceId, showAll, ownerWorkspace?.assetId, ownerWorkspace?.assetKind, ownerWorkspace?.quickTarget?.id]);

  const pendingTabs = useMemo(() => {
    const selected = showAll
      ? workspaces.filter((workspace) => workspace.kind === "session")
      : ownerWorkspace ? [ownerWorkspace] : [];
    const rows: PendingTab[] = [];
    const hostWorkspaces = new Map<string, Workspace[]>();
    for (const workspace of selected) {
      const hostKey = workspaceHostKey(workspace);
      if (!hostKey || workspace.deletedTarget) continue;
      const group = hostWorkspaces.get(hostKey) ?? [];
      group.push(workspace);
      hostWorkspaces.set(hostKey, group);
    }
    for (const [hostKey, group] of hostWorkspaces) {
      const sessionFor = (workspace: Workspace) => sessions.find((item) =>
        item.id === workspace.sessionId && workspaceSessionMatches(workspace, item, verifiedQuickSessions));
      if (group.some((workspace) => sessionFor(workspace)?.status === "connected")) continue;
      const workspace = group.find((candidate) => {
        const status = sessionFor(candidate)?.status;
        return status === "connecting" || status === "reconnecting";
      }) ?? group[0];
      const session = sessionFor(workspace);
      const known = new Map<string, string>();
      for (const tab of memoryKnown[hostKey] ?? []) {
        known.set(tab.id, tab.title || "持久终端");
      }
      for (const candidate of group) {
        for (const pane of candidate.panes) {
          for (const tab of pane.tabs) {
            if (tab.kind !== "terminal" || tab.containerId || tab.dead || tab.exited || (!tab.tabId && !tab.restorePending)) continue;
            known.set(tab.tabId ?? `layout:${tab.id}`, tab.title);
          }
        }
      }
      if (known.size === 0) known.set("undiscovered", "尚未查找的终端");
      for (const [id, title] of known) {
        rows.push({
          key: JSON.stringify([hostKey, id]), workspaceId: workspace.id, hostName: workspace.title, title,
          waitingForConnection: session?.status === "connecting" || session?.status === "reconnecting",
        });
      }
    }
    return rows;
  }, [showAll, workspaces, ownerWorkspace, sessions, memoryKnown, verifiedQuickSessions]);

  // The all-hosts view adds the cross-host listing, whose hosts answer one by one; its updates only re-derive the rows.
  const aggregateHosts = useSessionAggregate((s) => (showAll ? s.hosts : null));
  const aggregateSessions = useSessionAggregate((s) => (showAll ? s.sessionsByHost : null));
  const shownItems = useMemo(() => {
    if (!scopedItems || !aggregateHosts || !aggregateSessions || !covered) return scopedItems;
    const extra = aggregateBackgroundTabs(
      aggregateRows({ hosts: aggregateHosts, sessionsByHost: aggregateSessions }),
      { ...covered, openTabIds: openTerminalTabIds(workspaces) },
    );
    return extra.length > 0 ? [...scopedItems, ...extra] : scopedItems;
  }, [scopedItems, aggregateHosts, aggregateSessions, covered, workspaces]);
  // A change to a followed session restarts polling, so the list reloads at once.
  useEffect(() => {
    if (!visible || (!hostSessionId && !showAll)) return;
    const stopPoll = startPolling(() => load(), { intervalMs: POLL_MS });
    return () => {
      stopPoll();
      loadGenerationRef.current += 1;
    };
  }, [visible, hostSessionId, showAll, load, sessions]);

  const takeOver = async (info: BackgroundTab) => {
    const ownerGeneration = ownerGenerationRef.current;
    const key = backgroundTabKey(info);
    setBusy((b) => ({ ...b, [key]: "takeover" }));
    try {
      // A row from the cross-host listing is taken over through its host, opening a device workspace when needed.
      const result = info.aggregateHost
        ? await takeOverAggregateSession(info.aggregateHost, info.tabId, info.title)
        : await takeOverBackgroundTab(info, {
          isCurrent: () => ownerGeneration === ownerGenerationRef.current,
          guard: (session) => {
            if (remoteOwner && (!ownerWorkspace || !workspaceSessionMatches(ownerWorkspace, session, verifiedQuickSessions))) {
              throw new Error("这个终端不属于当前主机，请重新连接后再接管");
            }
          },
        });
      if (result.ok) {
        setItems((prev) => (prev ? prev.filter((t) => backgroundTabKey(t) !== key) : prev));
        pushToast("info", `已接管「${result.sessionName}」的终端`);
      } else if ("error" in result) {
        pushToast("error", `接管失败：${result.error}`);
      }
    } finally {
      if (ownerGeneration === ownerGenerationRef.current) {
        setBusy((b) => {
          const next = { ...b };
          delete next[key];
          return next;
        });
      }
    }
  };

  const reconnectHost = async (workspaceIdToReconnect: string) => {
    const target = useUi.getState().workspaces.find((workspace) => workspace.id === workspaceIdToReconnect);
    if (!target || !workspaceHostKey(target) || target.deletedTarget || reconnecting || desktopChecking) return;
    const identity = workspaceHostKey(target);
    const currentSession = useUi.getState().sessions.find((session) => session.id === target.sessionId);
    if (currentSession && workspaceSessionMatches(target, currentSession, verifiedQuickSessions) && currentSession.status === "connected") return;
    if (currentSession?.status === "connecting" || currentSession?.status === "reconnecting") return;
    const ownerGeneration = ownerGenerationRef.current;
    const connectionEpoch = runtimeEpoch;
    setReconnecting(true);
    setError(null);
    const isCurrent = () =>
      ownerGeneration === ownerGenerationRef.current && connectionEpoch === useUi.getState().workspaceRuntimeEpoch;
    try {
      const fresh = currentSession && workspaceSessionMatches(target, currentSession, verifiedQuickSessions)
        ? await reconnectWorkspaceSession(target, currentSession, { isCurrent })
        : await reconnectDeadHost(target, { isCurrent });
      if (!isCurrent()) return;
      if (!fresh) {
        pushToast("info", "已取消重连");
        return;
      }
      const currentTarget = useUi.getState().workspaces.find((workspace) => workspace.id === workspaceIdToReconnect);
      const verifiedFresh = currentTarget?.quickTarget && !currentTarget.assetId
        ? fresh.kind === "ssh" && !fresh.assetId
        : Boolean(currentTarget && workspaceSessionMatches(currentTarget, fresh, verifiedQuickSessions));
      if (!currentTarget || workspaceHostKey(currentTarget) !== identity || !verifiedFresh) {
        throw new Error("重新连接到的不是当前主机");
      }
      if (currentTarget.quickTarget && identity) {
        setQuickState((previous) => ({
          scope: accountScope,
          epoch: runtimeEpoch,
          sessions: {
            ...(previous.scope === accountScope && previous.epoch === runtimeEpoch ? previous.sessions : {}),
            [identity]: fresh.id,
          },
        }));
      }
      const st = useUi.getState();
      st.setSessions([...st.sessions.filter((session) => session.id !== fresh.id), fresh]);
      if (currentTarget.sessionId !== fresh.id) st.updateWorkspaceSession(currentTarget.id, fresh.id);
      pushToast("success", `已连接「${fresh.name}」，正在查找后台终端`);
    } catch (cause) {
      if (ownerGeneration === ownerGenerationRef.current) {
        pushToast("error", `重新连接失败：${describeError(cause)}`);
      }
    } finally {
      if (ownerGeneration === ownerGenerationRef.current) setReconnecting(false);
    }
  };

  const kill = async (info: BackgroundTab) => {
    const ownerGeneration = ownerGenerationRef.current;
    const key = backgroundTabKey(info);
    // Ending always asks: the process ends, unlike closing a tab, which leaves it running in the background.
    const path = info.aggregateHost ? aggregateTerminatePath(info.aggregateHost, useUi.getState().sessions) : "direct";
    if (path === null) return;
    const connectFirst = path === "connect_device" ? `\n\n需要先连接设备「${info.sessionName}」，然后结束这个终端。` : "";
    const ok = await ask(
      `结束「${info.sessionName}」上的终端「${backgroundTabName(info)}」？\n\n终端里运行的程序会被终止，进程结束后无法恢复。只想离开时不必结束，它会在后台继续运行。${connectFirst}`,
      { title: "结束进程", kind: "warning" },
    );
    if (!ok || ownerGeneration !== ownerGenerationRef.current) return;
    setBusy((b) => ({ ...b, [key]: "kill" }));
    setItems((prev) => (prev ? prev.filter((t) => backgroundTabKey(t) !== key) : prev));
    // A row from the cross-host listing is derived from it, so it never left the list.
    const restore = () => {
      if (info.aggregateHost) return;
      setItems((prev) => {
        if (prev?.some((t) => backgroundTabKey(t) === key)) return prev;
        return [...(prev ?? []), info];
      });
    };
    try {
      if (info.aggregateHost) {
        const result = await terminateAggregateSession(info.aggregateHost, info.tabId, path);
        if (!result.ok) {
          if (ownerGeneration === ownerGenerationRef.current) {
            restore();
            if ("error" in result) pushToast("error", `结束进程失败：${result.error}`);
          }
          return;
        }
      } else {
        await terminalApi.closeTab(info.tabId, "kill", info.sessionId);
      }
      forgetAggregateSession(info.tabId);
      if (ownerGeneration === ownerGenerationRef.current) pushToast("success", "已结束进程");
    } catch (e) {
      if (ownerGeneration === ownerGenerationRef.current) {
        restore();
        pushToast("error", `结束进程失败：${describeError(e)}`);
      }
    } finally {
      void useUi.getState().resyncSessions({ silent: true });
      if (ownerGeneration === ownerGenerationRef.current) {
        setBusy((b) => {
          const next = { ...b };
          delete next[key];
          return next;
        });
      }
    }
  };

  if (!hostSessionId && !showAll && !needsReconnect) {
    return (
      <div className="nx-pane" data-background-view="local">
        <div className="nx-toolbar nx-wrap-toolbar">
          <IconActivity size={14} className="text-neutral-500" />
          <span className="nx-toolbar-title">后台会话</span>
        </div>
        <div className="flex min-h-0 flex-1 items-center justify-center text-[12px] text-neutral-500">
          不知道该查看哪台主机，请从目标主机重新打开后台会话
        </div>
      </div>
    );
  }

  const list = visibleBackgroundTabs(
    shownItems ?? [],
    { sessions, workspaces, verified: verifiedQuickSessions },
    { hostSessionId, showAll, blocked: hostIdentityMismatch || (remoteOwner && needsReconnect) },
  );
  const count = list.length;
  const hostLabel = showAll ? "全部主机" : (hostName ?? "当前主机");
  const now = Date.now();
  const hostScope = showAll ? "all" : hostSession?.kind === "local" || (!remoteOwner && ownerWorkspace?.assetKind === "local") ? "local" : "remote";
  // The all-hosts view groups rows by host; hosts the cross-host listing could not list show as unknown.
  const groups = showAll && scopedItems !== null ? groupBackgroundTabs(list, aggregateHosts ?? NO_HOSTS) : null;

  const renderRow = (t: BackgroundTab) => {
    const key = backgroundTabKey(t);
    const action = busy[key];
    const rowBusy = action !== undefined;
    const name = backgroundTabName(t);
    const status = statusView(t);
    const time = timeView(t, now);
    const terminable = t.aggregateHost ? aggregateTerminatePath(t.aggregateHost, sessions) !== null : Boolean(t.sessionId);
    return (
      <tr key={key} data-background-row={t.tabId}>
        <td className="nx-bg-name">
          <span className="block truncate font-medium text-[var(--nx-fg-strong)]" title={name} data-testid="background-name">
            {name}
          </span>
        </td>
        <td className={`nx-bg-status ${STATUS_TONE_CLASS[status.tone]}`} title={status.hint} data-testid="background-status" data-state={t.state ?? "none"}>
          <span className="inline-flex items-center gap-1.5">
            <TabAttentionBadge badge={backgroundBadge(t)} />
            <span data-testid="background-status-label">{status.label}</span>
          </span>
        </td>
        <td className="nx-bg-size font-mono text-[11.5px] text-[var(--nx-fg)]" data-label="大小" data-testid="background-size">
          {t.cols}×{t.rows}
        </td>
        <td className="nx-bg-time" title={time.tooltip} data-testid="background-time">
          <span className="block truncate text-[var(--nx-fg)]" data-testid="background-output">{time.output}</span>
          <span className="block truncate text-[11px] text-[var(--nx-fg-soft)]" data-testid="background-detached">{time.detached}</span>
        </td>
        <td className="nx-right">
          <div className="flex items-center justify-end gap-1.5 max-[640px]:justify-stretch">
            <button
              className="nx-btn nx-btn-primary nx-btn-xs"
              disabled={rowBusy}
              title="回到这个终端，并补上离开期间的输出"
              onClick={() => void takeOver(t)}
            >
              {action === "takeover" ? (
                <IconRefresh size={11} className="animate-spin" />
              ) : (
                <IconGamepad size={11} />
              )}
              接管
            </button>
            <button
              className="nx-btn nx-btn-danger nx-btn-xs"
              disabled={rowBusy || !terminable}
              title={terminateTitle(t, sessions)}
              onClick={() => void kill(t)}
            >
              {action === "kill" ? (
                <IconRefresh size={11} className="animate-spin" />
              ) : (
                <IconTrash size={11} />
              )}
              结束进程
            </button>
          </div>
        </td>
      </tr>
    );
  };

  return (
    <div className="nx-pane" data-background-view="local">
      <div className="nx-toolbar nx-wrap-toolbar">
        <IconActivity size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">后台会话</span>
        <span className="nx-hint" data-testid="background-host">
          {hostLabel}
        </span>
        <span className="nx-hint" data-testid="background-count">
          {scopedItems === null && pendingTabs.length === 0 ? (scopedError ? "读取失败" : "加载中…") : summaryCopy(count)}
        </span>
        <div className="nx-spacer" />
        {needsReconnect && ownerWorkspace && (
          <button className="nx-btn nx-btn-primary nx-btn-sm" disabled={desktopChecking || reconnecting || hostSession?.status === "connecting" || hostSession?.status === "reconnecting"} onClick={() => void reconnectHost(ownerWorkspace.id)}>
            {reconnecting ? "正在连接…" : "连接并查找"}
          </button>
        )}
        <button className="nx-btn nx-btn-ghost nx-btn-sm" title="列表会自动更新，也可以立即刷新" disabled={loading || (!hostSessionId && !showAll)} onClick={() => void load()}>
          <IconRefresh size={13} className={loading ? "animate-spin" : ""} />
          刷新
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        <table className="nx-table nx-table-cards nx-table-fixed nx-bg-sessions" data-testid="background-table">
          <colgroup>
            <col />
            <col style={{ width: 88 }} />
            <col style={{ width: 92 }} />
            <col style={{ width: 168 }} />
            <col style={{ width: 208 }} />
          </colgroup>
          <thead>
            <tr>
              <th scope="col">名称</th>
              <th scope="col">状态</th>
              <th scope="col">大小</th>
              <th scope="col">时间</th>
              <th scope="col"><span className="sr-only">操作</span></th>
            </tr>
          </thead>
          <tbody>
            {scopedItems === null && !scopedError && pendingTabs.length === 0 && (
              <>
                {Array.from({ length: 4 }, (_, i) => (
                  <tr key={`skeleton-${i}`} aria-hidden="true">
                    <td className="nx-bg-name">
                      <span className="nx-skeleton w-4/5" />
                    </td>
                    <td className="nx-bg-status">
                      <span className="nx-skeleton w-3/5" />
                    </td>
                    <td>
                      <span className="nx-skeleton w-1/2" />
                    </td>
                    <td>
                      <span className="nx-skeleton w-2/3" />
                    </td>
                    <td>
                      <span className="nx-skeleton ml-auto w-32" />
                    </td>
                  </tr>
                ))}
              </>
            )}
            {!groups && list.map(renderRow)}
          </tbody>
          {/* Each host is its own row group, headed by the host. */}
          {groups?.map((group) => (
            <tbody key={`group-${group.key}`} data-background-group-body={group.key}>
              <HostGroupHeader group={group} />
              {group.tabs.map(renderRow)}
            </tbody>
          ))}
          <tbody>
            {pendingTabs.map((tab) => (
              <tr key={`pending-${tab.key}`} data-background-pending="">
                <td className="nx-bg-name">
                  <span className="block truncate font-medium text-[var(--nx-fg-strong)]" title={tab.title} data-testid="background-name">
                    {tab.title}
                  </span>
                  {showAll && (
                    <span className="block truncate text-[11px] text-[var(--nx-fg-soft)]" title={tab.hostName} data-testid="background-host-name">
                      {tab.hostName}
                    </span>
                  )}
                </td>
                <td className="nx-bg-status text-[var(--nx-fg)]" title="连接主机后才能确认是否仍在运行" data-testid="background-status" data-state="pending">
                  待核实
                </td>
                <td className="nx-bg-size font-mono text-[11.5px] text-[var(--nx-fg-soft)]" data-label="大小">—</td>
                <td className="nx-bg-time text-[var(--nx-fg-soft)]">连接主机后才能确认是否仍在运行</td>
                <td className="nx-right">
                  {tab.workspaceId ? (
                    <button className="nx-btn nx-btn-primary nx-btn-xs" disabled={desktopChecking || reconnecting || tab.waitingForConnection} onClick={() => void reconnectHost(tab.workspaceId as string)}>
                      <IconRefresh size={11} className={reconnecting ? "animate-spin" : ""} />
                      {reconnecting || tab.waitingForConnection ? "正在连接…" : "连接并查找"}
                    </button>
                  ) : <span className="text-[11px] text-[var(--nx-fg-soft)]">请先从资产树打开这台主机</span>}
                </td>
              </tr>
            ))}
            {scopedError && (
              <tr>
                <td colSpan={COLUMN_COUNT} className="nx-table-empty text-red-300">
                  无法读取后台会话：{scopedError}
                </td>
              </tr>
            )}
            {!scopedError && scopedItems !== null && list.length === 0 && pendingTabs.length === 0 && (
              <tr>
                <td colSpan={COLUMN_COUNT} className="nx-table-empty text-[var(--nx-fg-soft)]">
                  {needsReconnect
                    ? "主机未连接，连接后才能查看后台终端"
                    : showAll
                      ? "没有可恢复的后台终端"
                      : hostScope === "local"
                        ? "本机没有在后台运行的终端"
                        : "这台主机上没有在后台运行的终端"}
                  <div className="mt-1.5 text-[11px] text-[var(--nx-fg-soft)]">
                    {needsReconnect
                      ? "终端可能仍在主机上运行，连接成功后列表会自动更新"
                      : showAll
                        ? "请先从资产树打开目标主机，才能查看它上面的后台终端"
                        : hostScope === "local"
                          ? "关闭标签时选择「转到后台运行」，或由 AI 在后台创建的本机终端，会列在这里；退出应用后它们会结束"
                          : ownerWorkspace?.assetKind === "ssh"
                            ? "持久终端关闭标签后会继续在主机上运行并出现在这里；普通模式的终端关闭后会结束"
                            : "关闭标签时选择「转到后台运行」，终端会继续在主机上运行并出现在这里"}
                  </div>
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="shrink-0 border-t border-neutral-800/60 px-3 py-2 text-[11px] leading-relaxed text-[var(--nx-fg-soft)]">
        {list.some((t) => t.state === "needs_me") && (
          <p className="mb-1" data-testid="background-needs-me-note">{NEEDS_ME_NOTE}</p>
        )}
        <IconTerminal size={11} className="mr-1 inline align-[-1px]" />
        {persistenceFootnote(hostScope)}
      </div>
    </div>
  );
}
