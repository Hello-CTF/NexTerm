import { useCallback, useEffect, useState } from "react";
import { sessionApi, terminalApi, type LiveTabInfo } from "../../ipc/commands";
import { nextTabId, useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import {
  IconActivity,
  IconGamepad,
  IconRefresh,
  IconTerminal,
  IconTrash,
} from "../../ui/icons";

const POLL_MS = 5000;

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

export function BackgroundSessions({ visible = true }: { visible?: boolean }) {
  const pushToast = useUi((s) => s.pushToast);
  const [items, setItems] = useState<LiveTabInfo[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState<Record<string, "takeover" | "kill">>({});

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const list = await terminalApi.listLive();
      setItems(list.filter((t) => t.subscribers === 0 && !t.exited));
      setError(null);
    } catch (e) {
      setError(describeError(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!visible) return;
    void load();
    const timer = window.setInterval(() => void load(), POLL_MS);
    return () => window.clearInterval(timer);
  }, [visible, load]);

  const takeOver = async (info: LiveTabInfo) => {
    setBusy((b) => ({ ...b, [info.tabId]: "takeover" }));
    try {
      const list = await sessionApi.list();
      const st = useUi.getState();
      st.setSessions(list);
      const session = list.find((s) => s.id === info.sessionId);
      st.addTab({
        id: nextTabId(`term-${info.sessionId}`),
        kind: "terminal",
        title: session?.name ?? info.sessionName,
        sessionId: info.sessionId,
        tabId: info.tabId,
        closable: true,
      });
      setItems((prev) => (prev ? prev.filter((t) => t.tabId !== info.tabId) : prev));
      pushToast("info", `已接回「${session?.name ?? info.sessionName}」的终端`);
    } catch (e) {
      pushToast("error", `接管失败：${describeError(e)}`);
    } finally {
      setBusy((b) => {
        const next = { ...b };
        delete next[info.tabId];
        return next;
      });
    }
  };

  const kill = async (info: LiveTabInfo) => {
    const ok = await ask(
      `结束「${info.sessionName}」的这个后台终端？\n\n正在里面跑的进程会被终止，无法恢复。`,
      { kind: "warning" },
    );
    if (!ok) return;
    setBusy((b) => ({ ...b, [info.tabId]: "kill" }));
    const snapshot = items;
    setItems((prev) => (prev ? prev.filter((t) => t.tabId !== info.tabId) : prev));
    try {
      await terminalApi.closeTab(info.tabId, "kill");
      pushToast("success", "已结束进程");
    } catch (e) {
      setItems(snapshot);
      pushToast("error", `结束进程失败：${describeError(e)}`);
    } finally {
      setBusy((b) => {
        const next = { ...b };
        delete next[info.tabId];
        return next;
      });
    }
  };

  const list = items ?? [];
  const count = list.length;

  return (
    <div className="nx-pane">
      <div className="nx-toolbar">
        <IconActivity size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">后台会话</span>
        <span className="nx-hint">
          {items === null ? "读取中…" : `${count} 个任务在服务端运行 · 5s 自动刷新`}
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
              <th>最近输出</th>
              <th style={{ width: 96 }}>状态</th>
              <th style={{ width: 208 }} />
            </tr>
          </thead>
          <tbody>
            {list.map((t) => {
              const action = busy[t.tabId];
              const rowBusy = action !== undefined;
              return (
                <tr key={t.tabId}>
                  <td className="truncate font-medium text-neutral-200" title={t.sessionName}>
                    {t.sessionName}
                  </td>
                  <td className="nx-mono text-neutral-400">
                    {t.cols}×{t.rows}
                  </td>
                  <td className="text-neutral-400">{ago(t.lastOutputMsAgo)}</td>
                  <td>
                    <span className="nx-badge nx-badge-green">
                      <span className="nx-dot" />
                      运行中
                    </span>
                  </td>
                  <td className="nx-right">
                    <div className="flex items-center justify-end gap-1.5">
                      <button
                        className="nx-btn nx-btn-primary nx-btn-xs"
                        disabled={rowBusy}
                        title="在界面上接回这个终端（会回放已有的输出，不新开 shell）"
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
                <td colSpan={5} className="nx-table-empty text-red-300">
                  读取后台会话失败：{error}
                </td>
              </tr>
            )}
            {!error && items !== null && list.length === 0 && (
              <tr>
                <td colSpan={5} className="nx-table-empty">
                  没有在后台运行的终端
                  <div className="mt-1.5 text-[11px] text-neutral-500">
                    关闭终端标签时选「后台继续运行」，进程就会留在这里等你回来接管。
                  </div>
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="shrink-0 border-t border-neutral-800/60 px-3 py-2 text-[11px] leading-relaxed text-neutral-500">
        <IconTerminal size={11} className="mr-1 inline align-[-1px]" />
        这里的终端进程都跑在服务端：关掉本页面、换一台设备打开，它们都还在。
        「接管」不会新开 shell，会把你离开期间产生的输出一起回放回来。
      </div>
    </div>
  );
}
