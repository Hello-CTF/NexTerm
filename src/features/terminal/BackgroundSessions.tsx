import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { sessionApi, syncApi, terminalApi, type LiveTabInfo } from "../../ipc/commands";
import {
  backgroundTabKey,
  backgroundTargets,
  collectBackgroundTabs,
  openTerminalTabIds,
  resolveHostGate,
  visibleBackgroundTabs,
  workspaceHostKey,
  workspaceSessionMatches,
  type BackgroundScope,
  type BackgroundTab,
  type KnownDurableTab,
} from "./backgroundSessionsAccess";
import { nextTabId, reconnectDeadHost, reconnectWorkspaceSession, useUi, type Workspace } from "../../app/store";
import { apiBase, TRANSPORT } from "../../ipc/env";
import { listenEvent } from "../../ipc/events";
import { useAuth } from "../auth/store";
import { ask } from "../../ui/dialogs";
import { startPolling } from "../../app/usePolling";
import { describeError } from "../../ui/errorText";
import "../../ui/skeleton.css";
import {
  IconActivity,
  IconGamepad,
  IconRefresh,
  IconTerminal,
  IconTrash,
} from "../../ui/icons";

const POLL_MS = 5000;

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

function lastOutputLabel(tab: BackgroundTab): string {
  if (tab.lastOutputAt !== undefined && tab.lastOutputAt <= 0) return "暂无输出";
  return ago(tab.lastOutputMsAgo ?? 0);
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
  const sessions = useUi((s) => s.sessions);
  const workspaces = useUi((s) => s.workspaces);
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
    void listenEvent("sync://status", () => refresh(true)).then((off) => {
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
  const workspaceSessionId =
    ownerWorkspace?.kind === "session" ? (ownerWorkspace.sessionId ?? null) : null;
  const hostSessionId = workspaceSessionId ?? sessionId ?? null;
  const showAll = hostSessionId === null && ownerWorkspace?.kind === "tools";
  const hostSession = sessions.find((session) => session.id === hostSessionId);
  const { remoteOwner, identityMismatch: hostIdentityMismatch, needsReconnect } =
    resolveHostGate(ownerWorkspace, hostSession, verifiedQuickSessions);
  const hostName = hostSessionId
    ? (hostSession?.name ?? ownerWorkspace?.title ?? hostSessionId)
    : (ownerWorkspace?.title ?? null);
  const [items, setItems] = useState<BackgroundTab[] | null>(null);
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
      const scope: BackgroundScope = { sessions, workspaces: st.workspaces, verified: verifiedQuickSessions };
      const targets = backgroundTargets(scope, { showAll, hostSessionId, owner: ownerWorkspace, remoteOwner });
      const [live, ...remote] = await Promise.allSettled([
        terminalApi.listLive(),
        ...targets.map((session) => terminalApi.listDurable(session.id)),
      ]);
      if (generation !== loadGenerationRef.current) return;
      const errors: string[] = [];
      if (live.status === "rejected") errors.push(describeError(live.reason));
      const collected = collectBackgroundTabs(scope, {
        live: live.status === "fulfilled" ? (live.value as LiveTabInfo[]) : null,
        remote: targets.map((session, index) => {
          const result = remote[index];
          return {
            session,
            tabs: result.status === "fulfilled" ? (result.value as Awaited<ReturnType<typeof terminalApi.listDurable>>) : null,
            reason: result.status === "rejected" ? result.reason : undefined,
          };
        }),
        openTabIds: openTerminalTabIds(showAll ? st.workspaces : ownerWorkspace ? [ownerWorkspace] : []),
      });
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
      setError(errors.length > 0 ? errors.join("; ") : null);
    } catch (e) {
      if (generation === loadGenerationRef.current) setError(describeError(e));
    } finally {
      if (generation === loadGenerationRef.current) setLoading(false);
    }
  }, [hostSessionId, showAll, sessions, remoteOwner, ownerWorkspace, accountScope, runtimeEpoch, desktopChecking, verifiedQuickSessions]);

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

  useEffect(() => {
    if (!visible || (!hostSessionId && !showAll)) return;
    const stopPoll = startPolling(() => load(), { intervalMs: POLL_MS });
    return () => {
      stopPoll();
      loadGenerationRef.current += 1;
    };
  }, [visible, hostSessionId, showAll, load]);

  const takeOver = async (info: BackgroundTab) => {
    const ownerGeneration = ownerGenerationRef.current;
    const key = backgroundTabKey(info);
    setBusy((b) => ({ ...b, [key]: "takeover" }));
    try {
      const list = await sessionApi.list();
      if (ownerGeneration !== ownerGenerationRef.current) return;
      const session = list.find((s) => s.id === info.sessionId);
      if (!session || session.status !== "connected") {
        throw new Error("主机未连接，请先重新连接再接管");
      }
      if (remoteOwner && (!ownerWorkspace || !workspaceSessionMatches(ownerWorkspace, session, verifiedQuickSessions))) {
        throw new Error("这个终端不属于当前主机，请重新连接后再接管");
      }
      const st = useUi.getState();
      st.setSessions(list);
      st.addTab({
        id: nextTabId(`term-${info.sessionId}`),
        kind: "terminal",
        title: info.title || session?.name || info.sessionName,
        renamedByUser: info.title ? true : undefined,
        sessionId: info.sessionId,
        tabId: info.tabId,
        closable: true,
      });
      setItems((prev) => (prev ? prev.filter((t) => backgroundTabKey(t) !== key) : prev));
      pushToast("info", `已接管「${session?.name ?? info.sessionName}」的终端`);
    } catch (e) {
      if (ownerGeneration === ownerGenerationRef.current) {
        pushToast("error", `接管失败：${describeError(e)}`);
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
    const currentSession = sessions.find((session) => session.id === target.sessionId);
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
    const ok = await ask(
      `结束「${info.sessionName}」上的这个后台终端？\n\n其中运行的程序会被终止，无法恢复`,
      { title: "结束进程", kind: "warning" },
    );
    if (!ok || ownerGeneration !== ownerGenerationRef.current) return;
    setBusy((b) => ({ ...b, [key]: "kill" }));
    setItems((prev) => (prev ? prev.filter((t) => backgroundTabKey(t) !== key) : prev));
    try {
      await terminalApi.closeTab(info.tabId, "kill", info.sessionId);
      if (ownerGeneration === ownerGenerationRef.current) pushToast("success", "已结束进程");
    } catch (e) {
      if (ownerGeneration === ownerGenerationRef.current) {
        setItems((prev) => {
          if (prev?.some((t) => backgroundTabKey(t) === key)) return prev;
          return [...(prev ?? []), info];
        });
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
    scopedItems ?? [],
    { sessions, workspaces, verified: verifiedQuickSessions },
    { hostSessionId, showAll, blocked: hostIdentityMismatch || (remoteOwner && needsReconnect) },
  );
  const count = list.length;
  const hostLabel = showAll ? "全部主机" : (hostName ?? "未知主机");
  const hostScope = showAll ? "all" : hostSession?.kind === "local" || (!remoteOwner && ownerWorkspace?.assetKind === "local") ? "local" : "remote";

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
        <table className="nx-table nx-table-cards">
          <thead>
            <tr>
              <th style={{ width: 190 }}>主机</th>
              <th style={{ width: 130 }}>终端</th>
              <th>最近输出</th>
              <th style={{ width: 208 }} />
            </tr>
          </thead>
          <tbody>
            {scopedItems === null && !scopedError && pendingTabs.length === 0 && (
              <>
                {Array.from({ length: 4 }, (_, i) => (
                  <tr key={`skeleton-${i}`} aria-hidden="true">
                    <td>
                      <span className="nx-skeleton w-4/5" />
                    </td>
                    <td>
                      <span className="nx-skeleton w-3/5" />
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
            {list.map((t) => {
              const key = backgroundTabKey(t);
              const action = busy[key];
              const rowBusy = action !== undefined;
              return (
                <tr key={key}>
                  <td className="truncate font-medium text-neutral-200" title={t.sessionName}>
                    {t.sessionName}
                  </td>
                  <td className="nx-mono text-neutral-400" title={t.title}>
                    {t.title ?? `${t.cols}×${t.rows}`}
                  </td>
                  <td className="text-neutral-400">{t.remote ? "在主机上运行" : lastOutputLabel(t)}</td>
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
                        disabled={rowBusy}
                        title="结束这个终端里运行的程序，无法恢复"
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
            })}
            {pendingTabs.map((tab) => (
              <tr key={`pending-${tab.key}`}>
                <td className="truncate font-medium text-neutral-200" title={tab.hostName}>{tab.hostName}</td>
                <td className="nx-mono text-neutral-400" title={tab.title}>{tab.title}</td>
                <td className="text-amber-300">待核实 · 连接主机后才能确认是否仍在运行</td>
                <td className="nx-right">
                  {tab.workspaceId ? (
                    <button className="nx-btn nx-btn-primary nx-btn-xs" disabled={desktopChecking || reconnecting || tab.waitingForConnection} onClick={() => void reconnectHost(tab.workspaceId as string)}>
                      <IconRefresh size={11} className={reconnecting ? "animate-spin" : ""} />
                      {reconnecting || tab.waitingForConnection ? "正在连接…" : "连接并查找"}
                    </button>
                  ) : <span className="text-[11px] text-neutral-400">请先从资产树打开这台主机</span>}
                </td>
              </tr>
            ))}
            {scopedError && (
              <tr>
                <td colSpan={4} className="nx-table-empty text-red-300">
                  无法读取后台会话：{scopedError}
                </td>
              </tr>
            )}
            {!scopedError && scopedItems !== null && list.length === 0 && pendingTabs.length === 0 && (
              <tr>
                <td colSpan={4} className="nx-table-empty">
                  {needsReconnect
                    ? "主机未连接，连接后才能查看后台终端"
                    : showAll
                      ? "没有可恢复的后台终端"
                      : hostScope === "local"
                        ? "本机没有在后台运行的终端"
                        : "这台主机上没有在后台运行的终端"}
                  <div className="mt-1.5 text-[11px] text-neutral-500">
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

      <div className="shrink-0 border-t border-neutral-800/60 px-3 py-2 text-[11px] leading-relaxed text-neutral-500">
        <IconTerminal size={11} className="mr-1 inline align-[-1px]" />
        {persistenceFootnote(hostScope)}
      </div>
    </div>
  );
}
