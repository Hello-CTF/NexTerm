// 端口转发面板（§5.2 / M3-T8）：本地静态转发 + SOCKS5 动态转发。
//
// 为什么要有这个面板：内核的 `forward_create` / `forward_create_socks` /
// `forward_list` / `forward_remove` 四条命令一直在，但前端从来没有入口 ——
// 于是"能建转发"这件事实际上不可达（`docs/acceptance.md` §7.4 说的那种缺陷）。
//
// 两种形态放在同一张表里，因为对用户来说都是"本地开了个口子"；
// 差别只在「目标是谁定的」：静态转发建的时候就定死，动态转发由客户端当场指定。
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { forwardApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import {
  IconGlobe,
  IconNetwork,
  IconPlug,
  IconRefresh,
  IconTrash,
} from "../../ui/icons";

type Kind = "local" | "socks";

/** 本地端口默认值：静态转发习惯给 13306（MySQL 的 3306 加一万），动态给 1080。 */
const DEFAULT_PORT: Record<Kind, string> = { local: "13306", socks: "1080" };

export function ForwardPanel({ sessionId }: { sessionId?: string }) {
  const qc = useQueryClient();
  const { pushToast } = useUi();
  const sessions = useUi((s) => s.sessions);
  const [kind, setKind] = useState<Kind>("local");
  const [listenPort, setListenPort] = useState(DEFAULT_PORT.local);
  const [targetHost, setTargetHost] = useState("127.0.0.1");
  const [targetPort, setTargetPort] = useState("3306");
  const [busy, setBusy] = useState(false);

  // 转发是全局的（不跟着标签走），所以列表不带 sessionId 过滤 ——
  // 用户最需要知道的恰恰是"我总共开了哪些口子"，漏掉别的工作区建的那条最危险。
  const forwards = useQuery({
    queryKey: ["forwards"],
    queryFn: () => forwardApi.list(),
    refetchInterval: 5000,
  });

  const refresh = () => void qc.invalidateQueries({ queryKey: ["forwards"] });

  const switchKind = (k: Kind) => {
    setKind(k);
    setListenPort(DEFAULT_PORT[k]);
  };

  const create = async () => {
    if (!sessionId) {
      pushToast("info", "先连接一台机器 —— 转发要挂在某条 SSH 会话上");
      return;
    }
    const port = Number(listenPort.trim());
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      pushToast("error", "本地端口要填 1–65535 之间的整数");
      return;
    }
    if (kind === "local") {
      if (!targetHost.trim()) {
        pushToast("error", "目标主机不能为空");
        return;
      }
      const tp = Number(targetPort.trim());
      if (!Number.isInteger(tp) || tp < 1 || tp > 65535) {
        pushToast("error", "目标端口要填 1–65535 之间的整数");
        return;
      }
    }

    setBusy(true);
    try {
      if (kind === "socks") {
        await forwardApi.createSocks(sessionId, port);
        pushToast("success", `SOCKS5 代理已就绪 → 127.0.0.1:${port}`);
      } else {
        await forwardApi.create(
          sessionId,
          port,
          targetHost.trim(),
          Number(targetPort.trim()),
        );
        pushToast("success", `本地转发已就绪 → 127.0.0.1:${port}`);
      }
      refresh();
    } catch (e) {
      // 端口被占用是这里最常见的失败，内核的原话已经说清楚了，直接透传
      pushToast("error", `创建失败：${describeError(e)}`);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (id: string, label: string) => {
    if (!(await ask(`停止转发 ${label}？\n\n已经建立的连接会被断开。`))) return;
    try {
      await forwardApi.remove(id);
      refresh();
      pushToast("success", "已停止");
    } catch (e) {
      pushToast("error", `停止失败：${describeError(e)}`);
    }
  };

  const sessionName = (id: string) => sessions.find((s) => s.id === id)?.name ?? id.slice(0, 8);
  const list = forwards.data ?? [];
  const localCount = list.filter((f) => f.kind !== "socks").length;
  const socksCount = list.length - localCount;

  return (
    <div className="nx-pane">
      <div className="nx-toolbar">
        <IconNetwork size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">端口转发</span>
        <span className="nx-hint">
          {localCount} 条本地转发 · {socksCount} 条 SOCKS5 · 5s 自动刷新
        </span>
        <div className="nx-spacer" />
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={refresh}>
          <IconRefresh size={13} />
          刷新
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        <table className="nx-table">
          <thead>
            <tr>
              <th style={{ width: 132 }}>类型</th>
              <th style={{ width: 130 }}>本地监听</th>
              <th>目标</th>
              <th style={{ width: 150 }}>所属会话</th>
              <th style={{ width: 96 }} />
            </tr>
          </thead>
          <tbody>
            {list.map((f) => {
              const isSocks = f.kind === "socks";
              const label = `127.0.0.1:${f.listenPort}`;
              return (
                <tr key={f.id}>
                  <td>
                    <span className={`nx-badge ${isSocks ? "nx-badge-purple" : "nx-badge-green"}`}>
                      {isSocks ? (
                        <>
                          <IconGlobe size={11} />
                          SOCKS5 动态
                        </>
                      ) : (
                        <>
                          <IconPlug size={11} />
                          本地静态
                        </>
                      )}
                    </span>
                  </td>
                  <td className="nx-mono font-semibold text-neutral-100">{label}</td>
                  <td className="nx-mono">
                    {isSocks ? (
                      <span className="text-neutral-500">
                        由客户端指定（浏览器 / curl 里填 <span className="nx-code">{label}</span>）
                      </span>
                    ) : (
                      <span>
                        {f.targetHost}
                        <span className="text-neutral-600">:</span>
                        {f.targetPort}
                      </span>
                    )}
                  </td>
                  <td className="truncate text-neutral-400" title={f.sessionId}>
                    {sessionName(f.sessionId)}
                  </td>
                  <td className="nx-right">
                    <button
                      className="nx-btn nx-btn-danger nx-btn-xs"
                      onClick={() => void remove(f.id, label)}
                    >
                      <IconTrash size={11} />
                      停止
                    </button>
                  </td>
                </tr>
              );
            })}
            {list.length === 0 && (
              <tr>
                <td colSpan={5} className="nx-table-empty">
                  还没有任何转发
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="shrink-0 border-t border-neutral-800/60 bg-neutral-950/40 p-3">
        {/* 类型切换：两种转发要填的字段不一样，所以做成显式的两个 tab 而不是一堆可选输入框 */}
        <div className="mb-2.5 flex items-center gap-1.5">
          <button
            className={`nx-btn nx-btn-sm ${kind === "local" ? "nx-btn-primary" : "nx-btn-ghost"}`}
            onClick={() => switchKind("local")}
          >
            <IconPlug size={13} />
            本地静态转发
          </button>
          <button
            className={`nx-btn nx-btn-sm ${kind === "socks" ? "nx-btn-primary" : "nx-btn-ghost"}`}
            onClick={() => switchKind("socks")}
          >
            <IconGlobe size={13} />
            SOCKS5 动态转发
          </button>
          <span className="ml-1 text-[11px] text-neutral-500">
            {kind === "local"
              ? "把远端某个固定端口映射到本机（连 MySQL / Redis 用这个）"
              : "在本机开一个 SOCKS5 代理，目标由客户端当场指定（浏览器 / curl / proxychains 用这个）"}
          </span>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <label className="text-[11.5px] text-neutral-400">本地端口</label>
          <input
            className="nx-input nx-input-sm w-[92px] font-mono"
            value={listenPort}
            onChange={(e) => setListenPort(e.target.value)}
            placeholder={DEFAULT_PORT[kind]}
          />
          {kind === "local" && (
            <>
              <span className="text-neutral-600">→</span>
              <input
                className="nx-input nx-input-sm min-w-[200px] flex-1 font-mono"
                value={targetHost}
                onChange={(e) => setTargetHost(e.target.value)}
                placeholder="目标主机（相对远端解析，如 172.17.0.5 / db.internal）"
              />
              <span className="text-neutral-600">:</span>
              <input
                className="nx-input nx-input-sm w-[92px] font-mono"
                value={targetPort}
                onChange={(e) => setTargetPort(e.target.value)}
                placeholder="3306"
                onKeyDown={(e) => e.key === "Enter" && void create()}
              />
            </>
          )}
          <button
            className="nx-btn nx-btn-primary nx-btn-sm"
            disabled={busy || !sessionId}
            onClick={() => void create()}
          >
            {busy ? <IconRefresh size={13} className="animate-spin" /> : null}
            创建
          </button>
        </div>

        <div className="nx-hint mt-2">
          {sessionId ? (
            <>
              出口走当前会话（{sessionName(sessionId)}）的 SSH 连接；监听地址固定为{" "}
              <span className="nx-code">127.0.0.1</span> —— 内置代理无认证，不对外暴露。
            </>
          ) : (
            "当前没有可用的 SSH 会话，先连一台机器再来建转发。"
          )}
        </div>
      </div>
    </div>
  );
}
