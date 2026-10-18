import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { sessionApi, syncApi, terminalApi, type LiveTabInfo } from "../../ipc/commands";
import { nextTabId, reconnectDeadHost, reconnectSessionAndWait, useUi, type Workspace } from "../../app/store";
import { apiBase, TRANSPORT } from "../../ipc/env";
import { listenEvent } from "../../ipc/events";
import { useAuth } from "../auth/store";
import { ask } from "../../ui/dialogs";
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

type BackgroundTab = Omit<LiveTabInfo, "lastOutputMsAgo"> & {
  lastOutputMsAgo?: number;
  remote?: boolean;
};

interface PendingTab {
  key: string;
  workspaceId?: string;
  hostName: string;
  title: string;
  waitingForConnection?: boolean;
}

interface KnownDurableTab {
  id: string;
  title?: string;
}

function durableHostKey(kind: string, assetId?: string, quickTargetId?: string): string | null {
  if (kind !== "ssh" && kind !== "device") return null;
  const target = assetId || (kind === "ssh" ? quickTargetId : undefined);
  return target ? JSON.stringify([kind, target]) : null;
}

function workspaceHostKey(workspace: Workspace): string | null {
  return durableHostKey(workspace.assetKind ?? "", workspace.assetId, workspace.quickTarget?.id);
}

function workspaceSessionMatches(
  workspace: Workspace,
  session: { id: string; assetId: string | null; kind: string },
  verifiedQuickSessions: Record<string, string>,
): boolean {
  if (session.kind !== workspace.assetKind) return false;
  if (workspace.assetId) return session.assetId === workspace.assetId;
  const key = workspaceHostKey(workspace);
  return Boolean(key && workspace.quickTarget && !session.assetId &&
    (workspace.sessionId === session.id || verifiedQuickSessions[key] === session.id));
}

function backgroundTabKey(tab: Pick<BackgroundTab, "sessionId" | "tabId">): string {
  return JSON.stringify([tab.sessionId, tab.tabId]);
}

function persistenceFootnote(): string {
  if (TRANSPORT === "web") {
    return "SSH 和受管设备的持久终端在目标主机运行，关闭页面或更换客户端后仍可发现。「接管」将恢复原终端并回放离开期间的输出，不会新建 shell";
  }
  return "SSH 和受管设备的持久终端由目标主机上的后台服务保持运行，退出应用或更换客户端后仍可发现；本机（local）等其他分离终端会随应用退出而结束。「接管」将恢复原终端并回放离开期间的输出，不会新建 shell";
}

function runningCopy(count: number): string {
  return `${count} 个可接管终端 · 5 秒自动刷新`;
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
  const memoryKnown = !desktopChecking && knownState.scope === accountScope && knownState.epoch === runtimeEpoch ? knownState.tabs : {};
  const verifiedQuickSessions = quickState.scope === accountScope && quickState.epoch === runtimeEpoch ? quickState.sessions : {};
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
          setDesktopScope({ epoch: runtimeEpoch,
            value: status.loggedIn && status.userId && status.username && link.url && link.username === status.username
              ? JSON.stringify([link.url, status.userId, status.username]) : null });
          setDesktopChecking(false);
        }
      }).catch(() => {
        if (current && generation === request) {
          setDesktopScope({ epoch: runtimeEpoch, value: null });
          setDesktopChecking(false);
        }
      });
    };
    void listenEvent("sync://status", () => refresh(true)).then((off) => {
      if (!current) { off(); return; }
      unlisten = off;
      refresh(false);
    }).catch(() => { if (current) refresh(false); });
    const poll = window.setInterval(() => refresh(false), 5_000);
    return () => { current = false; request += 1; window.clearInterval(poll); unlisten?.(); };
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
  const remoteOwner =
    ownerWorkspace?.kind === "session" &&
    (ownerWorkspace.assetKind === "ssh" || ownerWorkspace.assetKind === "device") &&
    Boolean(ownerWorkspace.assetId || ownerWorkspace.quickTarget);
  const hostIdentityMismatch =
    remoteOwner && Boolean(ownerWorkspace && hostSession && !workspaceSessionMatches(ownerWorkspace, hostSession, verifiedQuickSessions));
  const needsReconnect =
    remoteOwner &&
    (!hostSession || hostIdentityMismatch || hostSession.status !== "connected");
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
      const targets = sessions.filter((session) =>
        (showAll || session.id === hostSessionId) &&
        (!remoteOwner || showAll || (ownerWorkspace && workspaceSessionMatches(ownerWorkspace, session, verifiedQuickSessions))) &&
        session.status === "connected" &&
        (session.kind === "ssh" || session.kind === "device") &&
        (Boolean(session.assetId) || useUi.getState().workspaces.some((workspace) =>
          workspace.kind === "session" && workspace.sessionId === session.id &&
          workspaceSessionMatches(workspace, session, verifiedQuickSessions))),
      );
      const [live, ...remote] = await Promise.allSettled([
        terminalApi.listLive(),
        ...targets.map((session) => terminalApi.listDurable(session.id)),
      ]);
      if (generation !== loadGenerationRef.current) return;
      const list: BackgroundTab[] = [];
      const known = new Set<string>();
      const knownTabsByHost = new Map<string, Map<string, KnownDurableTab>>();
      const incompleteHosts = new Set<string>();
      const errors: string[] = [];
      const hostKeyForSession = (session: (typeof sessions)[number]) => {
        const matchingWorkspace = useUi.getState().workspaces.find((workspace) =>
          workspace.kind === "session" && workspace.sessionId === session.id && workspaceSessionMatches(workspace, session, verifiedQuickSessions));
        return matchingWorkspace
          ? workspaceHostKey(matchingWorkspace)
          : durableHostKey(session.kind, session.assetId ?? undefined);
      };
      const keyForTab = (sessionId: string, tabId: string) => {
        const session = sessions.find((item) => item.id === sessionId && item.status === "connected");
        const hostKey = session ? hostKeyForSession(session) : null;
        return hostKey ? JSON.stringify([hostKey, tabId]) : backgroundTabKey({ sessionId, tabId });
      };
      if (live.status === "fulfilled") {
        for (const tab of live.value as LiveTabInfo[]) {
          if (tab.exited) continue;
          const key = keyForTab(tab.sessionId, tab.tabId);
          if (known.has(key)) continue;
          known.add(key);
          if (tab.subscribers === 0) list.push(tab);
        }
      } else {
        errors.push(describeError(live.reason));
      }
      for (const [index, result] of remote.entries()) {
        const session = targets[index];
        const hostKey = hostKeyForSession(session);
        if (result.status === "rejected") {
          if (hostKey) incompleteHosts.add(hostKey);
          errors.push(`${session.name}: ${describeError(result.reason)}`);
          continue;
        }
        const descriptors = result.value as Awaited<ReturnType<typeof terminalApi.listDurable>>;
        if (hostKey) {
          const knownTabs = knownTabsByHost.get(hostKey) ?? new Map<string, KnownDurableTab>();
          for (const tab of descriptors) {
            if (!tab.dead) knownTabs.set(tab.id, { id: tab.id, title: tab.title });
          }
          knownTabsByHost.set(hostKey, knownTabs);
        }
        for (const tab of descriptors) {
          const key = hostKey ? JSON.stringify([hostKey, tab.id]) : backgroundTabKey({ sessionId: session.id, tabId: tab.id });
          if (tab.dead || known.has(key)) continue;
          known.add(key);
          list.push({
            tabId: tab.id,
            sessionId: session.id,
            sessionName: session.name,
            sessionKind: session.kind,
            cols: tab.cols,
            rows: tab.rows,
            controller: null,
            subscribers: 0,
            viewers: 0,
            exited: false,
            title: tab.title,
            remote: true,
          });
        }
      }
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
  }, [hostSessionId, showAll, sessions, remoteOwner, ownerWorkspace?.assetId, ownerWorkspace?.assetKind, accountScope, runtimeEpoch, desktopChecking, verifiedQuickSessions]);

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
      if (known.size === 0) known.set("undiscovered", "待查找持久终端");
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
    void load();
    const timer = window.setInterval(() => void load(), POLL_MS);
    return () => {
      window.clearInterval(timer);
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
        throw new Error("目标主机会话未连接，请先重连后再接管");
      }
      if (remoteOwner && (!ownerWorkspace || !workspaceSessionMatches(ownerWorkspace, session, verifiedQuickSessions))) {
        throw new Error("目标会话不属于当前主机，请重新连接后再接管");
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
    try {
      const fresh = currentSession && workspaceSessionMatches(target, currentSession, verifiedQuickSessions)
        ? await reconnectSessionAndWait(currentSession)
        : await reconnectDeadHost(target);
      if (ownerGeneration !== ownerGenerationRef.current || connectionEpoch !== useUi.getState().workspaceRuntimeEpoch) return;
      if (!fresh) {
        pushToast("info", "已取消重连");
        return;
      }
      const currentTarget = useUi.getState().workspaces.find((workspace) => workspace.id === workspaceIdToReconnect);
      const verifiedFresh = currentTarget?.quickTarget && !currentTarget.assetId
        ? fresh.kind === "ssh" && !fresh.assetId
        : Boolean(currentTarget && workspaceSessionMatches(currentTarget, fresh, verifiedQuickSessions));
      if (!currentTarget || workspaceHostKey(currentTarget) !== identity || !verifiedFresh) {
        throw new Error("重连结果与当前主机不匹配");
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
      pushToast("success", `已连接「${fresh.name}」，正在查找持久终端`);
    } catch (cause) {
      if (ownerGeneration === ownerGenerationRef.current) {
        pushToast("error", `重连主机失败：${describeError(cause)}`);
      }
    } finally {
      if (ownerGeneration === ownerGenerationRef.current) setReconnecting(false);
    }
  };

  const kill = async (info: BackgroundTab) => {
    const ownerGeneration = ownerGenerationRef.current;
    const key = backgroundTabKey(info);
    const ok = await ask(
      `结束「${info.sessionName}」的后台终端？\n\n其中的进程将终止，且无法恢复`,
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
        <div className="nx-toolbar">
          <IconActivity size={14} className="text-neutral-500" />
          <span className="nx-toolbar-title">后台会话</span>
        </div>
        <div className="flex min-h-0 flex-1 items-center justify-center text-[12px] text-neutral-500">
          无法确定后台会话所属主机，请从目标主机重新打开
        </div>
      </div>
    );
  }

  const effectiveScope = hostSessionId ?? "";
  const all = scopedItems ?? [];
  const list = effectiveScope && !hostIdentityMismatch && !(remoteOwner && needsReconnect)
    ? all.filter((t) => t.sessionId === effectiveScope)
    : !showAll
      ? []
      : all.filter((tab) => {
        if (tab.sessionKind !== "ssh" && tab.sessionKind !== "device") return true;
        const session = sessions.find((candidate) => candidate.id === tab.sessionId);
        if (!session || session.status !== "connected") return false;
        return Boolean(session.assetId || workspaces.some((workspace) =>
          workspace.sessionId === session.id && workspaceSessionMatches(workspace, session, verifiedQuickSessions)));
      });
  const count = list.length;

  return (
    <div className="nx-pane" data-background-view="local">
      <div className="nx-toolbar">
        <IconActivity size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">后台会话</span>
        {(hostSessionId || remoteOwner) && (
          <span className="nx-hint" data-testid="background-host">
            主机：{hostName}
          </span>
        )}
        <span className="nx-hint">
          {scopedItems === null && pendingTabs.length === 0 ? (scopedError ? "读取失败" : "加载中…") : runningCopy(count)}
        </span>
        <div className="nx-spacer" />
        {needsReconnect && ownerWorkspace && (
          <button className="nx-btn nx-btn-primary nx-btn-sm" disabled={desktopChecking || reconnecting || hostSession?.status === "connecting" || hostSession?.status === "reconnecting"} onClick={() => void reconnectHost(ownerWorkspace.id)}>
            {reconnecting ? "正在重连…" : "重连并查找"}
          </button>
        )}
        <button className="nx-btn nx-btn-ghost nx-btn-sm" disabled={loading || (!hostSessionId && !showAll)} onClick={() => void load()}>
          <IconRefresh size={13} className={loading ? "animate-spin" : ""} />
          刷新
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        <table className="nx-table">
          <thead>
            <tr>
              <th style={{ width: 190 }}>所属会话</th>
              <th style={{ width: 130 }}>终端</th>
              <th>最近输出时间</th>
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
                  <td className="text-neutral-400">{t.remote ? "远端持久终端" : ago(t.lastOutputMsAgo ?? 0)}</td>
                  <td className="nx-right">
                    <div className="flex items-center justify-end gap-1.5">
                      <button
                        className="nx-btn nx-btn-primary nx-btn-xs"
                        disabled={rowBusy}
                        title="接管此终端（回放已有输出，不新建 shell）"
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
                        title="停止这个终端里的进程"
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
                <td className="text-amber-300">待核实 · 重连后才能查找远端持久终端</td>
                <td className="nx-right">
                  {tab.workspaceId ? (
                    <button className="nx-btn nx-btn-primary nx-btn-xs" disabled={desktopChecking || reconnecting || tab.waitingForConnection} onClick={() => void reconnectHost(tab.workspaceId as string)}>
                      <IconRefresh size={11} className={reconnecting ? "animate-spin" : ""} />
                      {reconnecting || tab.waitingForConnection ? "正在重连…" : "重连并查找"}
                    </button>
                  ) : <span className="text-[11px] text-neutral-400">从资产树打开主机后查找</span>}
                </td>
              </tr>
            ))}
            {scopedError && (
              <tr>
                <td colSpan={4} className="nx-table-empty text-red-300">
                  读取后台会话失败：{scopedError}
                </td>
              </tr>
            )}
            {!scopedError && scopedItems !== null && list.length === 0 && pendingTabs.length === 0 && (
              <tr>
                <td colSpan={4} className="nx-table-empty">
                  {needsReconnect
                    ? "主机未连接，重连后才能查找远端持久终端"
                    : showAll
                      ? "当前没有可接管的后台终端；请从资产树打开目标主机后查找持久终端"
                    : remoteOwner
                      ? "当前没有可查看的后台终端"
                      : "当前主机没有在后台运行的终端"}
                  <div className="mt-1.5 text-[11px] text-neutral-500">
                    {needsReconnect
                      ? "持久终端仍可能在目标主机运行，重连成功后会自动刷新列表"
                      : ownerWorkspace?.assetKind === "ssh" && remoteOwner
                        ? "如果该 SSH 资产已切换为普通终端，已有持久会话不会被删除；切回「自动」后可在这里查看并接管"
                        : "关闭运行中的 SSH 或受管设备终端标签不会终止进程；AI 以后台方式创建的终端（含本机）也会列在这里，之后可在此接管"}
                  </div>
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="shrink-0 border-t border-neutral-800/60 px-3 py-2 text-[11px] leading-relaxed text-neutral-500">
        <IconTerminal size={11} className="mr-1 inline align-[-1px]" />
        {persistenceFootnote()}
      </div>
    </div>
  );
}
