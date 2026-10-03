// 「后台会话」面板：服务端还在跑、但没人在看的终端标签。
//
// # 它解决什么
//
// 用户在外面用手机/另一台电脑开了个打日志的长任务，关掉网页走了。没有这个面板，
// 那些进程在服务端一直跑着，但界面里**看不见、也回不去** —— 唯一的办法是记住
// 它属于哪台机器，再开一个终端去 `ps`。
//
// # 数据源不是「会话」，是「内核标签」
//
// `terminal_list` 返回的是所有存活的终端标签（`subscribers === 0 && !exited`
// 就是后台在跑的那些）。用标签而不是会话做粒度，是因为同一个会话上可能有好几个
// 终端，用户要接回的是**具体某一个**（能看到它原来的输出），不是"随便开一个新终端"。
//
// # 「接管」走的是 resumeTabId 路径
//
// 新标签带上 `tabId`，上层会以 `resumeTabId` 交给 XtermView，走 `terminal_attach_tab`：
// 不新开 shell，把服务端那条连接上的回滚内容回放回来。这正是"中间的输出也在"的来源。
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

/** 轮询间隔：面板可见时每 5 秒对一次账（后台任务的状态会自己变）。 */
const POLL_MS = 5000;

/** 距最近一次输出多久 → 人话。 */
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
  /** tabId → 正在进行的动作。行级 pending：一行在忙不该让整个列表都不能点。 */
  const [busy, setBusy] = useState<Record<string, "takeover" | "kill">>({});

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const list = await terminalApi.listLive();
      // 「后台」= 没人在看、进程还活着。有人正看着的标签不用在这里出现；
      // 已经退出的进程也不归这个面板管（它已经不能"继续跑"了）。
      setItems(list.filter((t) => t.subscribers === 0 && !t.exited));
      setError(null);
    } catch (e) {
      setError(describeError(e));
    } finally {
      setLoading(false);
    }
  }, []);

  // 只在面板被激活时轮询：App 里所有标签都保持挂载，不判 visible 的话
  // 每开一个后台面板就多一个 5 秒定时器在后台空转。
  useEffect(() => {
    if (!visible) return;
    void load();
    const timer = window.setInterval(() => void load(), POLL_MS);
    return () => window.clearInterval(timer);
  }, [visible, load]);

  const takeOver = async (info: LiveTabInfo) => {
    setBusy((b) => ({ ...b, [info.tabId]: "takeover" }));
    try {
      // 先把会话列表刷到最新：这个会话可能是**别的设备**建的，本机 sessions 里没有它。
      // 不刷的话新标签会挂在一个标题为「会话」的空工作区下（工作区名是原子的导航依据）。
      const list = await sessionApi.list();
      const st = useUi.getState();
      st.setSessions(list);
      const session = list.find((s) => s.id === info.sessionId);
      // 固定走"接管"：带上内核 tabId，上层以 resumeTabId 交给 XtermView。
      st.addTab({
        id: nextTabId(`term-${info.sessionId}`),
        kind: "terminal",
        title: session?.name ?? info.sessionName,
        sessionId: info.sessionId,
        tabId: info.tabId,
        closable: true,
      });
      // 乐观地从列表摘掉：新标签 attach 后 subscribers 会变成 1，
      // 它已经不再"后台"了（下次轮询本来也会摘掉，这里先摘免得闪一下）。
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
      // 终止进程不可撤销：按共享对话框约定给 warning 级别（警示图标 + alertdialog 语义）。
      { kind: "warning" },
    );
    if (!ok) return;
    setBusy((b) => ({ ...b, [info.tabId]: "kill" }));
    // 乐观更新：先从列表移除，失败再放回去（否则用户会盯着一个"没反应的按钮"连点）。
    const snapshot = items;
    setItems((prev) => (prev ? prev.filter((t) => t.tabId !== info.tabId) : prev));
    try {
      await terminalApi.closeTab(info.tabId, "kill");
      pushToast("success", "已结束进程");
    } catch (e) {
      setItems(snapshot); // 回滚
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
