// 端口转发面板（§5.2 / M3-T8）：静态转发 + SOCKS5 动态转发。
//
// 为什么要有这个面板：内核的 `forward_create` / `forward_create_socks` /
// `forward_list` / `forward_remove` 四条命令一直在，但前端从来没有入口 ——
// 于是"能建转发"这件事实际上不可达（`docs/acceptance.md` §7.4 说的那种缺陷）。
//
// 两种形态放在同一张表里，因为对用户来说都是"开了个口子"；
// 差别只在「目标是谁定的」：静态转发建的时候就定死，动态转发由客户端当场指定。
//
// ⚠️ 监听地址与「本平台能不能用」都不是硬编码的，而是问内核（`forward_env`）：
// 桌面形态绑 `127.0.0.1`（只有本机连得上），服务端形态绑 `0.0.0.0`
// （转发到服务端的端口上，外部客户端才够得到），懒猫微服上整个功能不可用。
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

/** 静态转发默认端口：习惯给 13306（MySQL 的 3306 加一万）；动态给 1080。 */
const DEFAULT_PORT: Record<Kind, string> = { local: "13306", socks: "1080" };

/**
 * 能力自述还没回来时的兜底监听地址。
 *
 * 用桌面形态的值（`127.0.0.1`）而不是 `0.0.0.0`：它只影响**这一瞬间界面上显示的字符串**
 * —— 真正绑哪个地址由内核按 `ForwardPolicy` 决定，前端说了不算。所以这里偏保守取小，
 * 不会出现「界面说对外、实际没对外」这种反向误导。
 */
const FALLBACK_LISTEN_HOST = "127.0.0.1";

/**
 * 懒猫微服上的提示文案。
 *
 * 纯文本、不加任何装饰：toast 与提示条都是纯文本渲染，写 `**强调**` 只会把星号原样打给用户看。
 * 也说「为什么不可用」的下一步（去微服平台用它自带的转发），而不只是一个「不支持」。
 */
const LAZYCAT_UNAVAILABLE =
  "检测到懒猫微服，端口转发在此平台上暂不可用，您可以前往微服平台使用更强大的原生转发功能";

export function ForwardPanel({ sessionId }: { sessionId?: string }) {
  const qc = useQueryClient();
  const { pushToast } = useUi();
  const sessions = useUi((s) => s.sessions);
  /**
   * 本机会话上转发没有意义：它存在的理由是把**远端**的口子搬到本机，
   * 而本机自己就已经在 127.0.0.1 上。内核同样会拒（`AppError::Unsupported`），
   * 这里把按钮禁掉并说清楚，不做「点了才失败」的入口。
   */
  const localSession = sessions.find((s) => s.id === sessionId)?.kind === "local";
  const [kind, setKind] = useState<Kind>("local");
  const [listenPort, setListenPort] = useState(DEFAULT_PORT.local);
  const [targetHost, setTargetHost] = useState("127.0.0.1");
  const [targetPort, setTargetPort] = useState("3306");
  const [busy, setBusy] = useState(false);

  /**
   * 转发能力自述：本平台允不允许转发、监听地址是哪个。
   *
   * 不设 `refetchInterval`、`staleTime` 拉满：它由内核在启动时探测一次就定死
   * （编译形态 + `NEXTERM_PLATFORM`），进程活着期间不会变，反复问只是白花请求。
   */
  const env = useQuery({
    queryKey: ["forwardEnv"],
    queryFn: () => forwardApi.env(),
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  });
  /** 本平台是否**明确**说了「不可用」。查询失败时不判不可用 —— 让内核的错误去说话。 */
  const unavailable = env.data?.available === false;
  const listenHost = env.data?.listenHost ?? FALLBACK_LISTEN_HOST;
  /** 静态转发在服务端形态下就是「把远端服务搬到服务端端口上」，文案要跟着变。 */
  const exposed = !listenHost.startsWith("127.");

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
    // 本平台不可用就到此为止：内核同样会拒（`AppError::Unsupported`），但这里先说清楚
    // 「为什么」，比让用户对着一个禁用按钮猜强。入口也已经被藏起来了，这是兜底。
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
        // 桌面形态说「本地」是有意义的（确实只在本机）；服务端形态下它是对外口子，
        // 说「本地」会让人以为没暴露 —— 所以文案跟着监听地址走。
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
          /* 本平台不支持就**不摆控件** —— 一排禁用的输入框比一句话更难懂，
             而且会让人以为「是不是我哪里没填对」。 */
          <div className="nx-alert nx-alert-info flex items-start gap-2">
            <IconInfo size={14} className="mt-0.5 shrink-0" />
            <span>{LAZYCAT_UNAVAILABLE}</span>
          </div>
        ) : (
          <>
            {/* 类型切换：两种转发要填的字段不一样，所以做成显式的两个 tab 而不是一堆可选输入框 */}
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
