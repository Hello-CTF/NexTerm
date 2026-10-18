import { useCallback, useEffect, useRef, useState } from "react";
import { sessionApi, terminalApi, type LiveTabInfo } from "../../ipc/commands";
import { nextTabId, reconnectDeadHost, reconnectSessionAndWait, useUi } from "../../app/store";
import { TRANSPORT } from "../../ipc/env";
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
    Boolean(ownerWorkspace.assetId);
  const hostIdentityMismatch =
    remoteOwner && Boolean(hostSession && (hostSession.assetId !== ownerWorkspace.assetId || hostSession.kind !== ownerWorkspace.assetKind));
  const needsReconnect =
    remoteOwner &&
    (!hostSession || hostIdentityMismatch || hostSession.status === "disconnected" || hostSession.status === "failed");
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

  const load = useCallback(async () => {
    const generation = ++loadGenerationRef.current;
    if (!hostSessionId && !showAll) {
      setItems(null);
      setError(null);
      setLoading(false);
      return;
    }
    setLoading(true);
    try {
      const targets = sessions.filter((session) =>
        (showAll || session.id === hostSessionId) &&
        (!remoteOwner || showAll || (session.assetId === ownerWorkspace?.assetId && session.kind === ownerWorkspace.assetKind)) &&
        session.status === "connected" &&
        (session.kind === "ssh" || session.kind === "device"),
      );
      const [live, ...remote] = await Promise.allSettled([
        terminalApi.listLive(),
        ...targets.map((session) => terminalApi.listDurable(session.id)),
      ]);
      if (generation !== loadGenerationRef.current) return;
      const list: BackgroundTab[] = [];
      const known = new Set<string>();
      const errors: string[] = [];
      if (live.status === "fulfilled") {
        for (const tab of live.value as LiveTabInfo[]) {
          const key = backgroundTabKey(tab);
          if (known.has(key)) continue;
          known.add(key);
          if (tab.subscribers === 0 && !tab.exited) list.push(tab);
        }
      } else {
        errors.push(describeError(live.reason));
      }
      for (const [index, result] of remote.entries()) {
        const session = targets[index];
        if (result.status === "rejected") {
          errors.push(`${session.name}: ${describeError(result.reason)}`);
          continue;
        }
        for (const tab of result.value as Awaited<ReturnType<typeof terminalApi.listDurable>>) {
          const key = backgroundTabKey({ sessionId: session.id, tabId: tab.id });
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
      setItems(list);
      setError(errors.length > 0 ? errors.join("; ") : null);
    } catch (e) {
      if (generation === loadGenerationRef.current) setError(describeError(e));
    } finally {
      if (generation === loadGenerationRef.current) setLoading(false);
    }
  }, [hostSessionId, showAll, sessions, remoteOwner, ownerWorkspace?.assetId, ownerWorkspace?.assetKind]);

  useEffect(() => {
    ownerGenerationRef.current += 1;
    setItems(null);
    setError(null);
    setBusy({});
    setReconnecting(false);
    return () => {
      ownerGenerationRef.current += 1;
    };
  }, [workspaceId, hostSessionId, showAll, ownerWorkspace?.assetId, ownerWorkspace?.assetKind]);

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
      if (remoteOwner && (session.assetId !== ownerWorkspace?.assetId || session.kind !== ownerWorkspace.assetKind)) {
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

  const reconnectHost = async () => {
    if (!needsReconnect || !ownerWorkspace?.assetId || reconnecting) return;
    const ownerGeneration = ownerGenerationRef.current;
    setReconnecting(true);
    setError(null);
    try {
      const fresh = hostSession && !hostIdentityMismatch
        ? await reconnectSessionAndWait(hostSession)
        : await reconnectDeadHost(ownerWorkspace);
      if (ownerGeneration !== ownerGenerationRef.current) return;
      if (!fresh) {
        pushToast("info", "已取消重连");
        return;
      }
      if (fresh.kind !== ownerWorkspace.assetKind || fresh.assetId !== ownerWorkspace.assetId) {
        throw new Error("重连结果与当前主机不匹配");
      }
      const st = useUi.getState();
      st.setSessions([...st.sessions.filter((session) => session.id !== fresh.id), fresh]);
      if (ownerWorkspace.sessionId !== fresh.id) st.updateWorkspaceSession(ownerWorkspace.id, fresh.id);
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
  const all = items ?? [];
  const list = effectiveScope && !hostIdentityMismatch
    ? all.filter((t) => t.sessionId === effectiveScope)
    : effectiveScope
      ? []
      : all;
  const count = list.length;

  if (!hostSessionId && needsReconnect) {
    return (
      <div className="nx-pane" data-background-view="local">
        <div className="nx-toolbar">
          <IconActivity size={14} className="text-neutral-500" />
          <span className="nx-toolbar-title">后台会话</span>
          <span className="nx-hint">主机：{hostName}</span>
        </div>
        <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 px-6 text-center text-[12px] text-neutral-400">
          <span>这台主机尚未连接，重连后才能查找持久终端</span>
          <button className="nx-btn nx-btn-primary nx-btn-sm" disabled={reconnecting} onClick={() => void reconnectHost()}>
            {reconnecting ? "正在重连…" : "重连并查找"}
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="nx-pane" data-background-view="local">
      <div className="nx-toolbar">
        <IconActivity size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">后台会话</span>
        {hostSessionId && (
          <span className="nx-hint" data-testid="background-host">
            主机：{hostName}
          </span>
        )}
        <span className="nx-hint">
          {items === null ? (error ? "读取失败" : "加载中…") : runningCopy(count)}
        </span>
        <div className="nx-spacer" />
        {needsReconnect && (
          <button className="nx-btn nx-btn-primary nx-btn-sm" disabled={reconnecting} onClick={() => void reconnectHost()}>
            {reconnecting ? "正在重连…" : "重连并查找"}
          </button>
        )}
        <button className="nx-btn nx-btn-ghost nx-btn-sm" disabled={loading} onClick={() => void load()}>
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
            {items === null && !error && (
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
            {error && (
              <tr>
                <td colSpan={4} className="nx-table-empty text-red-300">
                  读取后台会话失败：{error}
                </td>
              </tr>
            )}
            {!error && items !== null && list.length === 0 && (
              <tr>
                <td colSpan={4} className="nx-table-empty">
                  {needsReconnect
                    ? "主机未连接，重连后才能查找远端持久终端"
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
