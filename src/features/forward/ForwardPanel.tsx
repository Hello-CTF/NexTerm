import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { forwardApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import {
  IconGlobe,
  IconInfo,
  IconNetwork,
  IconPlug,
  IconRefresh,
  IconTrash,
} from "../../ui/icons";

type Kind = "local" | "socks";

const DEFAULT_PORT: Record<Kind, string> = { local: "13306", socks: "1080" };

const FALLBACK_LISTEN_HOST = "127.0.0.1";

const LAZYCAT_UNAVAILABLE =
  "检测到懒猫微服，端口转发在此平台上暂不可用，您可以前往微服平台使用更强大的原生转发功能";

export function ForwardPanel({ sessionId }: { sessionId?: string }) {
  const qc = useQueryClient();
  const { pushToast } = useUi();
  const sessions = useUi((s) => s.sessions);
  const localSession = sessions.find((s) => s.id === sessionId)?.kind === "local";
  const [kind, setKind] = useState<Kind>("local");
  const [listenPort, setListenPort] = useState(DEFAULT_PORT.local);
  const [targetHost, setTargetHost] = useState("127.0.0.1");
  const [targetPort, setTargetPort] = useState("3306");
  const [busy, setBusy] = useState(false);

  const env = useQuery({
    queryKey: ["forwardEnv"],
    queryFn: () => forwardApi.env(),
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  });
  const unavailable = env.data?.available === false;
  const listenHost = env.data?.listenHost ?? FALLBACK_LISTEN_HOST;
  const exposed = !listenHost.startsWith("127.");

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
    if (unavailable) {
      pushToast("info", LAZYCAT_UNAVAILABLE);
      return;
    }
    if (!sessionId) {
      pushToast("info", "先连接一台机器 —— 转发要挂在某条 SSH 会话上");
      return;
    }
    if (localSession) {
      pushToast(
        "info",
        "当前是「当前设备」会话 —— 本机就是目标机器，转发没有意义；要访问内网其他主机请先连一台 SSH 资产",
      );
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
        pushToast("success", `SOCKS5 代理已就绪 → ${listenHost}:${port}`);
      } else {
        await forwardApi.create(
          sessionId,
          port,
          targetHost.trim(),
          Number(targetPort.trim()),
        );
        pushToast(
          "success",
          exposed
            ? `转发已就绪 → ${listenHost}:${port}（对外可访问）`
            : `本地转发已就绪 → ${listenHost}:${port}`,
        );
      }
      refresh();
    } catch (e) {
      if (kind === "socks" && (e as { code?: string } | null)?.code === "needs_confirm") {
        const detail = (e as { detail?: { listenHost?: unknown } } | null)?.detail;
        const confirmedListenHost =
          typeof detail?.listenHost === "string" && detail.listenHost ? detail.listenHost : listenHost;
        const ok = await ask(
          `监听 ${confirmedListenHost}:${port} 将创建一个无认证的开放 SOCKS5 代理。\n\n能连上这个端口的任何人都能借当前 SSH 会话访问远端网络。请确认你理解并愿意承担这个风险。`,
          { title: "确认开放代理风险", kind: "warning" },
        );
        if (ok) {
          try {
            await forwardApi.createSocks(sessionId, port, true);
            pushToast("success", `SOCKS5 代理已就绪 → ${confirmedListenHost}:${port}`);
            refresh();
          } catch (retryError) {
            pushToast("error", `创建失败：${describeError(retryError)}`);
          }
        }
        return;
      }
      pushToast("error", `创建失败：${describeError(e)}`);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (id: string, label: string) => {
    if (!(await ask(`停止转发 ${label}？\n\n已经建立的连接会被断开。`, { kind: "warning" }))) return;
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
          {localCount} 条转发 · {socksCount} 条 SOCKS5 · 5s 自动刷新
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
              <th style={{ width: 130 }}>监听地址</th>
              <th>目标</th>
              <th style={{ width: 150 }}>所属会话</th>
              <th style={{ width: 96 }} />
            </tr>
          </thead>
          <tbody>
            {list.map((f) => {
              const isSocks = f.kind === "socks";
              const label = `${listenHost}:${f.listenPort}`;
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
                          {exposed ? "静态转发" : "本地静态"}
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
        {unavailable ? (
          <div className="nx-alert nx-alert-info flex items-start gap-2">
            <IconInfo size={14} className="mt-0.5 shrink-0" />
            <span>{LAZYCAT_UNAVAILABLE}</span>
          </div>
        ) : (
          <>
            <div className="mb-2.5 flex items-center gap-1.5">
              <button
                className={`nx-btn nx-btn-sm ${kind === "local" ? "nx-btn-primary" : "nx-btn-ghost"}`}
                onClick={() => switchKind("local")}
              >
                <IconPlug size={13} />
                静态转发
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
                  ? exposed
                    ? "把远端某个固定端口映射到服务端（连 MySQL / Redis 用这个）"
                    : "把远端某个固定端口映射到本机（连 MySQL / Redis 用这个）"
                  : exposed
                    ? "在服务端开一个 SOCKS5 代理，目标由客户端当场指定（浏览器 / curl / proxychains 用这个）"
                    : "在本机开一个 SOCKS5 代理，目标由客户端当场指定（浏览器 / curl / proxychains 用这个）"}
              </span>
            </div>

            <div className="flex flex-wrap items-center gap-2">
              <label className="text-[11.5px] text-neutral-400">
                {exposed ? "监听端口" : "本地端口"}
              </label>
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
                disabled={busy || !sessionId || localSession}
                onClick={() => void create()}
              >
                {busy ? <IconRefresh size={13} className="animate-spin" /> : null}
                创建
              </button>
            </div>

            <div className="nx-hint mt-2">
              {localSession ? (
                <>
                  当前工作区是内置的「当前设备」：出口必须是另一台机器的 SSH 连接。
                  要访问内网服务（数据库 / 后台面板），先新建一台 SSH 资产并连上，再来建转发。
                </>
              ) : sessionId ? (
                <>
                  出口走当前会话（{sessionName(sessionId)}）的 SSH 连接；监听地址为{" "}
                  <span className="nx-code">
                    {listenHost}:{listenPort}
                  </span>
                  {exposed
                    ? " —— 这个端口对外可访问，能连上它的任何人都会到达目标服务。请确认目标服务自身有鉴权，不需要时及时停止。"
                    : " —— 只有本机能连，不对外暴露。"}
                  {exposed && kind === "socks" && (
                    <>
                      <br />
                      SOCKS5 代理本身没有认证，暴露在服务端上等于一个开放代理 ——
                      能连上这个端口的人都能借这条 SSH 会话访问远端网络。
                    </>
                  )}
                </>
              ) : (
                "当前没有可用的 SSH 会话，先连一台机器再来建转发。"
              )}
            </div>
          </>
        )}
      </div>
    </div>
  );
}
