import { useCallback, useEffect, useRef, useState } from "react";
import { sessionApi, terminalApi, type LiveTabInfo } from "../../ipc/commands";
import { nextTabId, useUi } from "../../app/store";
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

function persistenceFootnote(): string {
  if (TRANSPORT === "web") {
    return "终端进程在服务端运行，关闭页面或更换设备后仍会保留。「接管」将恢复原终端并回放离开期间的输出，不会新建 shell";
  }
  return "SSH 持久终端由本机后台服务、设备终端由设备上的后台服务保持运行，退出应用后仍可接管；本机（local）等其他分离终端会随应用退出而结束。「接管」将恢复原终端并回放离开期间的输出，不会新建 shell";
}

function runningCopy(count: number): string {
  if (TRANSPORT === "web") return `${count} 个终端在服务端运行 · 5 秒自动刷新`;
  return `${count} 个终端在后台运行 · 5 秒自动刷新`;
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
  const missingOwner =
    workspaceId !== undefined && ownerWorkspace === undefined && sessionId === undefined;
  const showAll = hostSessionId === null && !missingOwner;
  const hostName = hostSessionId
    ? (sessions.find((s) => s.id === hostSessionId)?.name ?? ownerWorkspace?.title ?? hostSessionId)
    : null;
  const [items, setItems] = useState<LiveTabInfo[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
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
      const list = await terminalApi.listLive();
      if (generation !== loadGenerationRef.current) return;
      setItems(list.filter((t) => t.subscribers === 0 && !t.exited));
      setError(null);
    } catch (e) {
      if (generation === loadGenerationRef.current) setError(describeError(e));
    } finally {
      if (generation === loadGenerationRef.current) setLoading(false);
    }
  }, [hostSessionId, showAll]);

  useEffect(() => {
    ownerGenerationRef.current += 1;
    setItems(null);
    setError(null);
    setBusy({});
    return () => {
      ownerGenerationRef.current += 1;
    };
  }, [workspaceId, hostSessionId, showAll]);

  useEffect(() => {
    if (!visible || (!hostSessionId && !showAll)) return;
    void load();
    const timer = window.setInterval(() => void load(), POLL_MS);
    return () => {
      window.clearInterval(timer);
      loadGenerationRef.current += 1;
    };
  }, [visible, hostSessionId, showAll, load]);

  const takeOver = async (info: LiveTabInfo) => {
    const ownerGeneration = ownerGenerationRef.current;
    setBusy((b) => ({ ...b, [info.tabId]: "takeover" }));
    try {
      const list = await sessionApi.list();
      if (ownerGeneration !== ownerGenerationRef.current) return;
      const st = useUi.getState();
      st.setSessions(list);
      const session = list.find((s) => s.id === info.sessionId);
      st.addTab({
        id: nextTabId(`term-${info.sessionId}`),
        kind: "terminal",
        title: info.title || session?.name || info.sessionName,
        renamedByUser: info.title ? true : undefined,
        sessionId: info.sessionId,
        tabId: info.tabId,
        closable: true,
      });
      setItems((prev) => (prev ? prev.filter((t) => t.tabId !== info.tabId) : prev));
      pushToast("info", `已接管「${session?.name ?? info.sessionName}」的终端`);
    } catch (e) {
      if (ownerGeneration === ownerGenerationRef.current) {
        pushToast("error", `接管失败：${describeError(e)}`);
      }
    } finally {
      if (ownerGeneration === ownerGenerationRef.current) {
        setBusy((b) => {
          const next = { ...b };
          delete next[info.tabId];
          return next;
        });
      }
    }
  };

  const kill = async (info: LiveTabInfo) => {
    const ownerGeneration = ownerGenerationRef.current;
    const ok = await ask(
      `结束「${info.sessionName}」的后台终端？\n\n其中的进程将终止，且无法恢复`,
      { title: "结束进程", kind: "warning" },
    );
    if (!ok || ownerGeneration !== ownerGenerationRef.current) return;
    setBusy((b) => ({ ...b, [info.tabId]: "kill" }));
    setItems((prev) => (prev ? prev.filter((t) => t.tabId !== info.tabId) : prev));
    try {
      await terminalApi.closeTab(info.tabId, "kill");
      if (ownerGeneration === ownerGenerationRef.current) pushToast("success", "已结束进程");
    } catch (e) {
      if (ownerGeneration === ownerGenerationRef.current) {
        setItems((prev) => {
          if (prev?.some((t) => t.tabId === info.tabId)) return prev;
          return [...(prev ?? []), info];
        });
        pushToast("error", `结束进程失败：${describeError(e)}`);
      }
    } finally {
      void useUi.getState().resyncSessions({ silent: true });
      if (ownerGeneration === ownerGenerationRef.current) {
        setBusy((b) => {
          const next = { ...b };
          delete next[info.tabId];
          return next;
        });
      }
    }
  };

  if (!hostSessionId && !showAll) {
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
  const list = effectiveScope ? all.filter((t) => t.sessionId === effectiveScope) : all;
  const count = list.length;

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
              const action = busy[t.tabId];
              const rowBusy = action !== undefined;
              return (
                <tr key={t.tabId}>
                  <td className="truncate font-medium text-neutral-200" title={t.sessionName}>
                    {t.sessionName}
                  </td>
                  <td className="nx-mono text-neutral-400" title={t.title}>
                    {t.title ?? `${t.cols}×${t.rows}`}
                  </td>
                  <td className="text-neutral-400">{ago(t.lastOutputMsAgo)}</td>
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
                  当前主机没有在后台运行的终端
                  <div className="mt-1.5 text-[11px] text-neutral-500">
                    关闭运行中的 SSH 终端标签不会终止进程；AI 以后台方式创建的终端（含本机）也会列在这里，之后可在此接管
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
