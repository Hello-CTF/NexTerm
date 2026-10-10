// 设备管理视图 (FLEET149): WEB 账号模式下的真实设备管理界面。
// 数据全部来自 /fleet/* 真实 HTTP 合同 (见 internal/fleet/server/http.go):
// 设备列表由服务端按角色过滤 (超管看全部+owner, 普通用户只看自己的);
// 桌面端没有账号体系/fleet 后端, 一律给显式不可用态, 不发任何请求。

import { useCallback, useEffect, useRef, useState } from "react";
import { fleetApi, type FleetBaseURLEntry, type FleetDevice, type FleetMetricsSample } from "../../ipc/fleetApi";
import { useAuth } from "../auth/store";
import { openDeviceTerminalTab, useUi } from "../../app/store";
import { WEB } from "../../ipc/env";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { EVENTS, listenEvent, type DeviceStatusEvent } from "../../ipc/events";
import { onEventsResync } from "../../ipc/webTransport";
import {
  IconCheckCircle,
  IconCopy,
  IconInfo,
  IconMonitor,
  IconPlus,
  IconRefresh,
  IconTerminal,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";
import { formatBytes, formatTime, formatUptime, isOnline, ONLINE_THRESHOLD_MS, relativeTime } from "./format";
import { BaseUrlsSection } from "./BaseUrlsSection";
import { oneLineInstallCommand, shellQuote } from "./installCommand";

const ENROLL_TTL_OPTIONS = [
  { label: "5 分钟", ms: 5 * 60_000 },
  { label: "15 分钟", ms: 15 * 60_000 },
  { label: "1 小时", ms: 60 * 60_000 },
];

// 数据目录与仓库 per-user 约定一致 (platform.desktopDataDir: XDG_DATA_HOME
// 或 ~/.local/share), 普通用户可写, 适配 systemd --user / launchd per-user。
// 双引号让 shell 在调用前展开: NEXTERM_DATA_DIR 最优先 (与文案的覆盖承诺一致,
// CLI 中 flag 优先于 env, 必须把覆盖嵌进 flag 值), 其次 XDG_DATA_HOME, 最后 $HOME。
const AGENT_DATA_DIR = '"${NEXTERM_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/NexTerm}"';

function enrollCommand(baseUrl: string, insecure: boolean, code: string): string {
  return `nexterm-server agent enroll --server ${shellQuote(baseUrl)} --code ${shellQuote(code)}${insecure ? " --insecure" : ""} --data-dir ${AGENT_DATA_DIR}`;
}

function installCommand(): string {
  return `nexterm-server agent install --data-dir ${AGENT_DATA_DIR}`;
}

function copyText(text: string, pushToast: (kind: "info" | "error" | "success", text: string) => void, what: string): void {
  const clipboard = typeof navigator === "undefined" ? undefined : navigator.clipboard;
  if (!clipboard || typeof clipboard.writeText !== "function") {
    pushToast("error", "复制失败: 剪贴板不可用, 请手动选中复制");
    return;
  }
  void clipboard
    .writeText(text)
    .then(() => pushToast("success", `${what}已复制`))
    .catch(() => pushToast("error", "复制失败, 请手动选中复制"));
}

function FleetUnsupported() {
  return (
    <div className="nx-pane">
      <div className="nx-toolbar flex-wrap">
        <IconMonitor size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">设备管理</span>
      </div>
      <div className="flex min-h-0 flex-1 items-center justify-center px-6">
        <div className="flex w-[340px] flex-col items-center gap-3 rounded-xl border border-neutral-800/70 bg-neutral-950/45 px-6 py-7 text-center">
          <div className="nx-empty-icon">
            <IconMonitor size={18} />
          </div>
          <div>
            <div className="text-[13.5px] font-semibold tracking-tight text-neutral-100">
              桌面端暂无设备管理
            </div>
            <div className="mt-1 text-[11.5px] leading-relaxed text-neutral-500">
              桌面端是本地优先模式, 没有账号体系; 设备管理仅在浏览器模式连接服务器并登录后可用。
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

function MetricsPanel({ deviceId, online }: { deviceId: string; online: boolean }) {
  const [samples, setSamples] = useState<FleetMetricsSample[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const loadSeq = useRef(0);

  const load = useCallback(() => {
    const seq = ++loadSeq.current;
    setLoading(true);
    setError(null);
    fleetApi
      .metrics(deviceId)
      .then((r) => {
        if (seq === loadSeq.current) setSamples(r.samples);
      })
      .catch((e: unknown) => {
        if (seq === loadSeq.current) setError(describeError(e));
      })
      .finally(() => {
        if (seq === loadSeq.current) setLoading(false);
      });
  }, [deviceId]);

  useEffect(() => {
    load();
  }, [load]);

  const refreshButton = (
    <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" aria-label="刷新指标" onClick={load} disabled={loading}>
      <IconRefresh size={11} className={loading ? "animate-spin" : ""} />
      {error ? "重试" : "刷新"}
    </button>
  );

  if (loading && samples === null) {
    return (
      <div className="flex flex-wrap items-center gap-2 text-[11.5px]">
        <span className="nx-hint py-1">指标加载中…</span>
        {refreshButton}
      </div>
    );
  }
  if (error) {
    return (
      <div className="flex flex-wrap items-center gap-2 text-[11.5px]">
        <span className="text-red-300">指标加载失败 · {error}</span>
        {refreshButton}
      </div>
    );
  }
  if (!samples || samples.length === 0) {
    return (
      <div className="flex flex-wrap items-center gap-2 text-[11.5px]">
        <span className="nx-hint py-1">{online ? "设备还没有上报指标 (默认每 60 秒一次)。" : "设备离线, 没有近期指标。"}</span>
        {refreshButton}
      </div>
    );
  }
  const latest = samples[samples.length - 1];
  const memPct = latest.mem_total > 0 ? Math.min(100, (latest.mem_used / latest.mem_total) * 100) : 0;
  const diskPct = latest.disk_total > 0 ? Math.min(100, (latest.disk_used / latest.disk_total) * 100) : 0;
  return (
    <div className="flex flex-col gap-2">
      <div className="grid grid-cols-2 gap-x-4 gap-y-2 text-[12px] sm:grid-cols-4">
        <div>
          <div className="nx-hint text-[11px]">CPU</div>
          <div className="font-mono text-neutral-100">{latest.cpu_pct.toFixed(1)}%</div>
        </div>
        <div>
          <div className="nx-hint text-[11px]">内存</div>
          <div className="font-mono text-neutral-100">
            {formatBytes(latest.mem_used)} / {formatBytes(latest.mem_total)}
          </div>
          <div className="mt-1 h-1.5 rounded bg-neutral-800">
            <div className="h-1.5 rounded bg-blue-400/70" style={{ width: `${memPct}%` }} />
          </div>
        </div>
        <div>
          <div className="nx-hint text-[11px]">磁盘</div>
          <div className="font-mono text-neutral-100">
            {formatBytes(latest.disk_used)} / {formatBytes(latest.disk_total)}
          </div>
          <div className="mt-1 h-1.5 rounded bg-neutral-800">
            <div className="h-1.5 rounded bg-amber-400/70" style={{ width: `${diskPct}%` }} />
          </div>
        </div>
        <div>
          <div className="nx-hint text-[11px]">运行时长</div>
          <div className="font-mono text-neutral-100">{formatUptime(latest.uptime_s)}</div>
        </div>
      </div>
      <div className="flex items-center gap-2">
        <div className="nx-hint text-[11px]">
          近 24 小时共 {samples.length} 个采样点 · 最新 {formatTime(latest.ts)}
        </div>
        <div className="nx-spacer" />
        {refreshButton}
      </div>
    </div>
  );
}

interface DeviceCardProps {
  device: FleetDevice;
  now: number;
  online: boolean;
  isAdmin: boolean;
  onPatch: (id: string, patch: Partial<FleetDevice>) => void;
  onRevoked: (id: string) => void;
  onRejected: (id: string) => void;
}

function DeviceCard({ device, now, online, isAdmin, onPatch, onRevoked, onRejected }: DeviceCardProps) {
  const { pushToast } = useUi();
  const [metricsOpen, setMetricsOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const revoked = device.revoked_at !== 0;
  const pending = !revoked && device.state === "pending";
  const agent = device.agent;
  // 心跳判定的在线用于徽章提示文案: 事件驱动的在线以控制通道为准。
  const heartbeatOnline = agent ? isOnline(agent.last_seen_at || device.last_seen_at, now) : false;

  const revoke = async () => {
    const ok = await ask(`吊销设备「${device.name}」?\n\n吊销后设备凭证立即失效、控制通道断开, 且不可恢复。`, {
      title: "吊销设备",
      kind: "warning",
    });
    if (!ok) return;
    setBusy(true);
    try {
      await fleetApi.revoke(device.id);
      pushToast("success", `已吊销「${device.name}」`);
      onRevoked(device.id);
    } catch (e) {
      pushToast("error", describeError(e));
    } finally {
      setBusy(false);
    }
  };

  const approve = async () => {
    setBusy(true);
    try {
      await fleetApi.approve(device.id);
      pushToast("success", `已批准「${device.name}」, 设备上线后即可使用`);
      onPatch(device.id, { state: "active" });
    } catch (e) {
      pushToast("error", describeError(e));
    } finally {
      setBusy(false);
    }
  };

  const reject = async () => {
    const ok = await ask(`拒绝设备「${device.name}」的接入?\n\n拒绝后该设备立即被删除、凭证失效, 需要新接入码才能重新接入。`, {
      title: "拒绝接入",
      kind: "warning",
    });
    if (!ok) return;
    setBusy(true);
    try {
      await fleetApi.reject(device.id);
      pushToast("success", `已拒绝并删除「${device.name}」`);
      onRejected(device.id);
    } catch (e) {
      pushToast("error", describeError(e));
    } finally {
      setBusy(false);
    }
  };

  const toggleAutostart = async () => {
    if (!agent) return;
    const desired = !agent.desired_autostart;
    if (!online) {
      const ok = await ask(
        `设备「${device.name}」当前离线。\n\n这次修改只保存为期望状态, 不会立即生效; 设备下次连接服务器后才会实际应用。`,
        { title: "设备离线", kind: "info" },
      );
      if (!ok) return;
    }
    setBusy(true);
    try {
      const r = await fleetApi.setAutostart(device.id, desired);
      onPatch(device.id, { agent: { ...agent, desired_autostart: r.desired_autostart } });
      pushToast("success", `已把「${device.name}」的期望自启动设为${r.desired_autostart ? "开启" : "关闭"}`);
    } catch (e) {
      pushToast("error", describeError(e));
    } finally {
      setBusy(false);
    }
  };

  // reconcile 语义 (internal/fleet/agent/runtime.go): desired=true 对齐 installed&&enabled,
  // active 只是进程此刻是否运行, 单独展示, 不参与漂移判断。
  const serviceState = agent?.service_state;
  const hasServiceReport = Boolean(serviceState && (serviceState.installed || serviceState.enabled || serviceState.active || serviceState.last_reconcile_at));
  const autostartActual = Boolean(serviceState?.installed && serviceState?.enabled);
  const runningText = !hasServiceReport ? "未上报" : serviceState?.active ? "运行中" : "未运行";
  const autostartDrift = Boolean(agent && hasServiceReport && agent.desired_autostart !== autostartActual);

  return (
    <section className={`nx-card ${revoked ? "opacity-60" : ""}`}>
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconMonitor size={15} className="text-neutral-400" />
        <span className="nx-card-title min-w-0 break-all">{device.name}</span>
        <span className="nx-badge shrink-0">{device.kind}</span>
        {revoked ? (
          <span className="nx-badge nx-badge-red shrink-0">已吊销</span>
        ) : pending ? (
          <span className="nx-badge nx-badge-amber shrink-0" title="管理员批准前设备不会上线, 也不同步数据">
            待审批
          </span>
        ) : agent ? (
          <span
            className={`nx-badge shrink-0 ${online ? "nx-badge-green" : ""}`}
            title={online ? (heartbeatOnline ? "最近 3 分钟内有心跳" : "控制通道实时在线") : `最近心跳 ${relativeTime(agent.last_seen_at || device.last_seen_at, now)}`}
          >
            {online ? "在线" : "离线"}
          </span>
        ) : null}
        {isAdmin && device.owner && (
          <span className="nx-badge nx-badge-blue shrink-0" title={`所有者 ${device.owner.username}`}>
            {device.owner.username}
          </span>
        )}
        <div className="nx-spacer" />
        {pending && isAdmin && (
          <>
            <button
              type="button"
              className="nx-btn nx-btn-primary nx-btn-sm shrink-0"
              disabled={busy}
              title="批准后设备才能上线并同步数据"
              onClick={() => void approve()}
            >
              <IconCheckCircle size={11} />
              批准
            </button>
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
              disabled={busy}
              title="拒绝并删除该待审批设备, 凭证立即失效"
              onClick={() => void reject()}
            >
              <IconXCircle size={11} />
              拒绝
            </button>
          </>
        )}
        {!revoked && !pending && agent?.terminal_enabled && (
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
            disabled={!online}
            title={online ? "在这台设备上打开远程终端" : "设备离线, 无法打开终端"}
            onClick={() => openDeviceTerminalTab(device)}
          >
            <IconTerminal size={11} />
            终端
          </button>
        )}
        {!revoked && !pending && agent && (
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
            onClick={() => setMetricsOpen(!metricsOpen)}
            aria-expanded={metricsOpen}
          >
            指标
          </button>
        )}
        {!revoked && (
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
            disabled={busy}
            title="吊销后设备凭证立即失效, 不可恢复"
            onClick={() => void revoke()}
          >
            <IconTrash size={11} />
            吊销
          </button>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-x-5 gap-y-1 text-[12px] text-neutral-300">
        <span className="nx-hint">创建于 {formatTime(device.created_at)}</span>
        <span className="nx-hint">最近心跳 {relativeTime(agent?.last_seen_at || device.last_seen_at, now)}</span>
        {revoked && <span className="text-red-300">吊销于 {formatTime(device.revoked_at)}</span>}
      </div>

      {pending && (
        <div className="mt-2 border-t border-neutral-800/60 pt-2 text-[12px] text-amber-300">
          等待管理员批准: 批准前设备不会上线, 也不会同步任何数据。
        </div>
      )}

      {agent && (
        <div className="mt-2 flex flex-col gap-2 border-t border-neutral-800/60 pt-2">
          <div className="flex flex-wrap items-center gap-x-5 gap-y-1 text-[12px] text-neutral-300">
            <span>
              平台 <b className="font-mono">{agent.platform || "未知"}</b>
              {agent.app_version && <span className="nx-hint"> · v{agent.app_version}</span>}
            </span>
            <span className="min-w-0 break-all">
              当前接入{" "}
              {agent.current_url ? (
                <code className="nx-code font-mono text-[11.5px]">{agent.current_url}</code>
              ) : (
                <span className="nx-hint">未上报</span>
              )}
            </span>
            <span>
              远程终端{" "}
              <b>{agent.terminal_enabled ? "开启" : "关闭"}</b>
            </span>
          </div>

          <div className="flex flex-wrap items-center gap-x-5 gap-y-2 text-[12px] text-neutral-300">
            <label className="flex items-center gap-1.5" title="期望自启动: 服务端保存的期望状态, 设备 agent 下次连接时对齐">
              <input
                type="checkbox"
                checked={agent.desired_autostart}
                disabled={busy || revoked || pending}
                onChange={() => void toggleAutostart()}
              />
              期望自启动
            </label>
            <span>
              实际自启动{" "}
              <b>{hasServiceReport ? (autostartActual ? "已启用" : "未启用") : "未上报"}</b>
              {autostartDrift && !revoked && (
                <span className="nx-badge nx-badge-amber ml-2" title={serviceState?.last_error || "实际自启动与期望不一致"}>
                  未生效
                </span>
              )}
            </span>
            <span>
              运行状态{" "}
              <b>{runningText}</b>
            </span>
            {serviceState?.last_error && (
              <span className="min-w-0 break-all text-red-300" title={serviceState.last_error}>
                服务错误: {serviceState.last_error}
              </span>
            )}
            {!online && !revoked && (
              <span className="nx-hint text-[11px]">设备离线: 修改只会保存为期望状态, 上线后生效</span>
            )}
          </div>

          {metricsOpen && (
            <div className="border-t border-neutral-800/60 pt-2">
              <MetricsPanel deviceId={device.id} online={online} />
            </div>
          )}
        </div>
      )}

      {!agent && !revoked && (
        <div className="nx-hint mt-2 border-t border-neutral-800/60 pt-2 text-[11px]">
          该设备没有安装设备 agent, 没有运行状态与指标。
        </div>
      )}
    </section>
  );
}

export function DevicesView() {
  const { pushToast } = useUi();
  const user = useAuth((s) => s.user);
  const authStatus = useAuth((s) => s.status);
  const [devices, setDevices] = useState<FleetDevice[] | null>(null);
  const [deviceQuery, setDeviceQuery] = useState("");
  const [deviceFilter, setDeviceFilter] = useState<"all" | "online" | "offline" | "revoked" | "pending">("all");
  const [baseUrls, setBaseUrls] = useState<FleetBaseURLEntry[]>([]);
  const [baseUrlsStatus, setBaseUrlsStatus] = useState<"loading" | "error" | "ready">("loading");
  const [baseUrlsError, setBaseUrlsError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const [enrollOpen, setEnrollOpen] = useState(false);
  const [ttlMs, setTtlMs] = useState(ENROLL_TTL_OPTIONS[1].ms);
  const [issued, setIssued] = useState<{ code: string; expiresAt: number } | null>(null);
  const [issuing, setIssuing] = useState(false);
  // serverVersion 是 /healthz 的真实服务端版本 (一键安装 --version 用);
  // null 表示不可用, 一键安装指令整段隐藏, 绝不伪造版本。
  const [serverVersion, setServerVersion] = useState<string | null>(null);
  // presence 是 device://status 事件的实时信号 (设备 ID → 在线状态 + 收到时间)。
  // 超过心跳阈值后回落到心跳判定 (轮询兜底), 与既有展示语义一致。
  const [presence, setPresence] = useState<Record<string, { online: boolean; at: number }>>({});
  const loadSeq = useRef(0);
  const baseUrlsSeq = useRef(0);
  const versionSeq = useRef(0);
  // accountEpoch 只在账号切换时递增 (手动刷新不动它), 隔离签发在途的
  // then/catch/finally: 旧账号的晚到完成一律不写新账号视图。
  const accountEpoch = useRef(0);

  const isAdmin = user?.role === "superadmin";
  const userId = user?.id ?? null;

  // 账号作用域隔离: user.id 变化时在渲染期重置全部账号态 (设备/base URL/一次性接入码),
  // 并递增 loadSeq 使旧账号的晚到响应失效; AuthGate 只是 overlay, 登出再登录不会卸载本 pane。
  const [scopedUserId, setScopedUserId] = useState(userId);
  if (scopedUserId !== userId) {
    setScopedUserId(userId);
    loadSeq.current += 1;
    baseUrlsSeq.current += 1;
    accountEpoch.current += 1;
    versionSeq.current += 1;
    setDevices(null);
    setDeviceQuery("");
    setDeviceFilter("all");
    setBaseUrls([]);
    setBaseUrlsStatus("loading");
    setBaseUrlsError(null);
    setError(null);
    setEnrollOpen(false);
    setIssued(null);
    setIssuing(false);
    setLoading(false);
    setServerVersion(null);
    setPresence({});
  }

  const loadBaseUrls = useCallback(() => {
    if (!WEB) return;
    const seq = ++baseUrlsSeq.current;
    setBaseUrlsStatus("loading");
    setBaseUrlsError(null);
    void fleetApi
      .baseUrls()
      .then((r) => {
        if (seq !== baseUrlsSeq.current) return;
        setBaseUrls(r.base_urls);
        setBaseUrlsStatus("ready");
      })
      .catch((e: unknown) => {
        if (seq !== baseUrlsSeq.current) return;
        setBaseUrlsError(describeError(e));
        setBaseUrlsStatus("error");
      });
  }, []);

  const load = useCallback(() => {
    if (!WEB) return;
    const seq = ++loadSeq.current;
    setLoading(true);
    setError(null);
    loadBaseUrls();
    fleetApi
      .devices()
      .then((r) => {
        if (seq === loadSeq.current) setDevices(r.devices);
      })
      .catch((e: unknown) => {
        if (seq === loadSeq.current) setError(describeError(e));
      })
      .finally(() => {
        if (seq === loadSeq.current) setLoading(false);
      });
  }, [loadBaseUrls]);

  useEffect(() => {
    if (!WEB || !user) return;
    load();
  }, [user, load]);

  // 轮询兜底: 事件流之外每 60 秒静默刷新一次设备列表, 事件丢失/WS 断开时
  // 在线状态仍能收敛; 静默刷新不碰 loading 态, 避免周期性打扰界面。
  const poll = useCallback(() => {
    if (!WEB) return;
    const seq = ++loadSeq.current;
    fleetApi
      .devices()
      .then((r) => {
        if (seq === loadSeq.current) setDevices(r.devices);
      })
      .catch(() => {});
  }, []);

  useEffect(() => {
    if (!WEB || !user) return;
    const timer = setInterval(poll, 60_000);
    return () => clearInterval(timer);
  }, [user, poll]);

  // device://status 实时上下线: 只写入 presence, 徽章与终端按钮随渲染更新;
  // 不在当前账号设备清单内的 ID (他人设备) 不会被渲染, 自然忽略。
  useEffect(() => {
    if (!WEB || !user) return;
    let cancelled = false;
    let unlisten: (() => void) | undefined;
    void listenEvent<DeviceStatusEvent>(EVENTS.deviceStatus, (p) => {
      if (!p || typeof p.deviceId !== "string" || typeof p.online !== "boolean") return;
      setPresence((prev) => ({ ...prev, [p.deviceId]: { online: p.online, at: Date.now() } }));
    }).then((off) => {
      if (cancelled) off();
      else unlisten = off;
    });
    return () => {
      cancelled = true;
      unlisten?.();
    };
  }, [user]);

  // 事件 WS 重连并错过事件 (resync) 时立即刷新, 不等下一轮轮询。
  useEffect(() => {
    if (!WEB || !user) return;
    return onEventsResync(() => load());
  }, [user, load]);

  // 有效在线 = 实时事件优先 (心跳阈值内), 否则回落心跳判定; 非 agent 设备恒离线。
  const onlineOf = (d: FleetDevice): boolean => {
    if (!d.agent) return false;
    const p = presence[d.id];
    if (p && now - p.at <= ONLINE_THRESHOLD_MS) return p.online;
    return isOnline(d.agent.last_seen_at || d.last_seen_at, now);
  };

  // 打开接入面板时经 /healthz 读取真实服务端版本 (一键安装的 --version);
  // 失败或空串都归为 null: 一键安装指令整段隐藏, 绝不伪造版本。
  useEffect(() => {
    if (!enrollOpen || !WEB || !user) return;
    const seq = ++versionSeq.current;
    fleetApi
      .serverVersion()
      .then((v) => {
        if (seq === versionSeq.current) setServerVersion(v || null);
      })
      .catch(() => {
        if (seq === versionSeq.current) setServerVersion(null);
      });
  }, [enrollOpen, user]);

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(timer);
  }, []);

  if (!WEB) {
    return <FleetUnsupported />;
  }

  if (!user) {
    const authOff = authStatus?.auth === "off";
    return (
      <div className="nx-pane">
        <div className="nx-toolbar flex-wrap">
          <IconMonitor size={14} className="text-neutral-500" />
          <span className="nx-toolbar-title">设备管理</span>
        </div>
        <div className="flex min-h-0 flex-1 items-center justify-center px-6">
          <div className="flex w-[320px] flex-col items-center gap-3 rounded-xl border border-neutral-800/70 bg-neutral-950/45 px-6 py-7 text-center">
            <div className="nx-empty-icon">
              <IconMonitor size={18} />
            </div>
            <div>
              <div className="text-[13px] text-neutral-300">
                {authOff ? "这台服务器关闭了账号功能,设备管理不可用。" : "登录后才能查看与管理接入的设备。"}
              </div>
              {!authOff && <div className="mt-1 text-[11.5px] text-neutral-500">登录入口在「设置 → 账号同步」里的「账号」卡。</div>}
            </div>
          </div>
        </div>
      </div>
    );
  }

  const issueEnrollCode = async () => {
    if (!WEB || !user || baseUrlsStatus !== "ready") return;
    const epoch = accountEpoch.current;
    setIssuing(true);
    try {
      const r = await fleetApi.issueEnrollCode(ttlMs);
      if (epoch !== accountEpoch.current) return;
      setIssued({ code: r.code, expiresAt: r.expires_at });
    } catch (e) {
      if (epoch !== accountEpoch.current) return;
      pushToast("error", describeError(e));
    } finally {
      if (epoch === accountEpoch.current) setIssuing(false);
    }
  };

  const patchDevice = (id: string, patch: Partial<FleetDevice>) => {
    setDevices((prev) => (prev === null ? prev : prev.map((d) => (d.id === id ? { ...d, ...patch } : d))));
  };

  const removeDevice = (id: string) => {
    setDevices((prev) => (prev === null ? prev : prev.filter((d) => d.id !== id)));
  };

  const jumpToBaseUrls = () => {
    document.getElementById("fleet-base-urls")?.scrollIntoView({ behavior: "smooth", block: "start" });
    document.querySelector<HTMLInputElement>('input[aria-label="新接入地址"]')?.focus({ preventScroll: true });
  };

  const copyCommandRow = (key: string, command: string, what: string) => (
    <div key={key} className="flex flex-wrap items-center gap-2">
      <code className="nx-code min-w-0 flex-1 break-all font-mono text-[11.5px]">{command}</code>
      <button
        type="button"
        className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
        onClick={() => copyText(command, pushToast, what)}
      >
        <IconCopy size={11} />
        复制
      </button>
    </div>
  );

  const enrollRows = issued
    ? baseUrls.map((entry) => copyCommandRow(`enroll:${entry.url}`, enrollCommand(entry.url, Boolean(entry.insecure), issued.code), "接入命令"))
    : null;
  const oneLineRows =
    issued && serverVersion
      ? baseUrls.map((entry) =>
          copyCommandRow(
            `one-line:${entry.url}`,
            oneLineInstallCommand(entry.url, Boolean(entry.insecure), issued.code, serverVersion),
            "一键安装命令",
          ),
        )
      : null;
  const installRow = issued ? copyCommandRow("install", installCommand(), "安装命令") : null;
  const normalizedDeviceQuery = deviceQuery.trim().toLowerCase();
  const pendingCount = devices?.filter((d) => d.state === "pending" && d.revoked_at === 0).length ?? 0;
  const visibleDevices = devices?.filter((d) => {
    const revoked = d.revoked_at !== 0;
    if (deviceFilter === "revoked" && !revoked) return false;
    if (deviceFilter === "pending" && (revoked || d.state !== "pending")) return false;
    if (deviceFilter === "online" && (revoked || !onlineOf(d))) return false;
    if (deviceFilter === "offline" && (revoked || onlineOf(d))) return false;
    if (!normalizedDeviceQuery) return true;
    return [d.name, d.id, d.kind, d.owner?.username, d.agent?.platform, d.agent?.current_url]
      .map((value) => value?.toLowerCase() ?? "")
      .some((value) => value.includes(normalizedDeviceQuery));
  });

  return (
    <div className="nx-pane">
      <div className="nx-toolbar flex-wrap">
        <IconMonitor size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">设备管理</span>
        <span className="nx-hint">
          {error && devices === null ? "设备列表加载失败" : devices === null ? "加载中…" : `共 ${devices.length} 台`}
        </span>
        {error && devices !== null && <span className="nx-hint text-red-300">刷新失败 · {error}</span>}
        <div className="nx-spacer" />
        <button
          className={`nx-btn nx-btn-sm ${devices !== null && devices.length === 0 ? "nx-btn-primary" : "nx-btn-outline"}`}
          onClick={() => {
            setEnrollOpen(!enrollOpen);
          }}
          aria-expanded={enrollOpen}
        >
          <IconPlus size={12} />
          接入新设备
        </button>
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={load} disabled={loading}>
          <IconRefresh size={13} className={loading ? "animate-spin" : ""} />
          刷新
        </button>
      </div>

      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto p-3">
        {devices !== null && devices.length > 0 && (
          <div className="flex flex-wrap items-center gap-2">
            <input
              type="search"
              className="nx-input min-w-[180px] flex-1"
              style={{ maxWidth: 360 }}
              value={deviceQuery}
              aria-label="搜索设备"
              placeholder="搜索设备"
              onChange={(e) => setDeviceQuery(e.target.value)}
            />
            <select
              className="nx-select shrink-0"
              style={{ width: 112 }}
              value={deviceFilter}
              aria-label="设备状态过滤"
              onChange={(e) => setDeviceFilter(e.target.value as typeof deviceFilter)}
            >
              <option value="all">全部</option>
              <option value="online">在线</option>
              <option value="offline">离线</option>
              <option value="pending">待审批</option>
              <option value="revoked">已吊销</option>
            </select>
            {(deviceQuery.trim() !== "" || deviceFilter !== "all") && (
              <span className="nx-hint text-[11px]">
                显示 {visibleDevices?.length ?? 0} / {devices.length} 台
              </span>
            )}
          </div>
        )}

        {isAdmin && pendingCount > 0 && deviceFilter !== "pending" && (
          <div className="nx-alert nx-alert-info flex flex-wrap items-center gap-2">
            <IconInfo size={13} className="shrink-0" />
            <span className="min-w-0 flex-1">{pendingCount} 台设备待审批: 批准后才能上线并同步数据。</span>
            <button type="button" className="nx-btn nx-btn-outline nx-btn-xs shrink-0" onClick={() => setDeviceFilter("pending")}>
              查看待审批
            </button>
          </div>
        )}

        {enrollOpen && (
          <section className="nx-card">
            <div className="mb-1 flex flex-wrap items-center gap-2">
              <IconPlus size={15} className="text-neutral-400" />
              <span className="nx-card-title">接入新设备</span>
            </div>
            {!issued ? (
              <>
                <p className="nx-hint mb-3">
                  签发一次性接入码, 在设备上执行命令, 再回到本页刷新并打开终端。接入码单次使用, 到期自动作废。
                </p>
                <div className="flex flex-wrap items-center gap-2">
                  <select
                    className="nx-select shrink-0"
                    style={{ width: 120 }}
                    value={ttlMs}
                    aria-label="接入码有效期"
                    onChange={(e) => setTtlMs(Number(e.target.value))}
                  >
                    {ENROLL_TTL_OPTIONS.map((o) => (
                      <option key={o.ms} value={o.ms}>
                        {o.label}
                      </option>
                    ))}
                  </select>
                  <button
                    className="nx-btn nx-btn-primary"
                    disabled={issuing || baseUrlsStatus !== "ready"}
                    onClick={() => void issueEnrollCode()}
                  >
                    {issuing ? "签发中…" : "签发接入码"}
                  </button>
                  {baseUrlsStatus !== "ready" && (
                    <span className="nx-hint text-[11px]">
                      {baseUrlsStatus === "loading" ? "接入地址读取中, 成功后才能签发。" : "接入地址读取失败, 请在下方重试后签发。"}
                    </span>
                  )}
                </div>
              </>
            ) : (
              <div className="nx-alert nx-alert-danger flex flex-col gap-2">
                <div className="flex items-start gap-2">
                  <IconInfo size={14} className="mt-0.5 shrink-0" />
                  <div className="min-w-0">
                    接入码 <b>只显示这一次</b>, 单次使用, 有效期至 {formatTime(issued.expiresAt)}。
                    它经服务器中转兑换成设备凭证; 凭证只写入设备本地数据目录, 本页面不会再显示任何设备密钥。
                  </div>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <code className="nx-code break-all font-mono text-[13px]">{issued.code}</code>
                  <button
                    type="button"
                    className="nx-btn nx-btn-outline nx-btn-sm"
                    onClick={() => copyText(issued.code, pushToast, "接入码")}
                  >
                    <IconCopy size={12} />
                    复制接入码
                  </button>
                  <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setIssued(null)}>
                    完成
                  </button>
                </div>
                <div className="flex flex-col gap-1.5 border-t border-red-500/20 pt-2">
                  {baseUrls.length === 0 ? (
                    <div className="flex flex-wrap items-center gap-2 text-[12px] text-amber-300">
                      {isAdmin ? (
                        <>
                          <span>还没有配置接入地址, 设备无法接入, 也无法生成接入命令。</span>
                          <button type="button" className="nx-btn nx-btn-outline nx-btn-xs shrink-0" onClick={jumpToBaseUrls}>
                            去配置接入地址
                          </button>
                        </>
                      ) : (
                        <span>管理员还没有配置接入地址, 请联系管理员配置后再接入。</span>
                      )}
                    </div>
                  ) : (
                    <>
                      {serverVersion ? (
                        <>
                          <div className="text-[12px] text-neutral-200">在设备上用<b>普通用户</b>执行一键接入命令:</div>
                          {oneLineRows?.[0]}
                        </>
                      ) : (
                        <>
                          <div className="text-[12px] text-neutral-200">
                            在设备上用<b>普通用户</b>依次执行两条命令: 第一条兑换设备凭证, 第二条安装后台服务。
                          </div>
                          {enrollRows?.[0]}
                          {installRow}
                        </>
                      )}
                      <div className="text-[11.5px] text-neutral-400">
                        本页面不会替你安装, 也不会感知安装是否成功, 结果以设备上的输出为准。
                      </div>
                      <details className="rounded border border-red-500/20 px-2 py-1.5">
                        <summary className="cursor-pointer text-[11.5px] text-neutral-300">高级说明: 多地址、手动接入与服务管理</summary>
                        <div className="mt-2 flex flex-col gap-1.5">
                          {baseUrls.length > 1 && (
                            <>
                              <div className="text-[11.5px] text-neutral-400">其他接入地址按配置顺序作为备用, 第一个可达的即接入点:</div>
                              {serverVersion ? oneLineRows?.slice(1) : enrollRows?.slice(1)}
                            </>
                          )}
                          {serverVersion && (
                            <>
                              <div className="text-[11.5px] text-neutral-400">
                                设备已安装 nexterm-server 时, 也可手动执行 enroll 与 install:
                              </div>
                              {enrollRows}
                              {installRow}
                            </>
                          )}
                          <div className="text-[11.5px] text-neutral-400">
                            agent install 会安装随登录自动启动的 per-user 后台服务 (systemd --user / launchd); 数据目录默认取用户数据目录, 可用 NEXTERM_DATA_DIR 统一覆盖 enroll 与 install。
                          </div>
                        </div>
                      </details>
                    </>
                  )}
                  <div className="flex flex-wrap items-center gap-2 border-t border-red-500/20 pt-2">
                    <span className="text-[12px] text-neutral-200">
                      执行完成后刷新, 设备会出现在下方列表里: 首台直接「在线」; 之后接入的设备为「待审批」, 管理员批准上线后再点设备卡片上的「终端」。
                    </span>
                    <button type="button" className="nx-btn nx-btn-primary nx-btn-sm shrink-0" disabled={loading} onClick={() => void load()}>
                      <IconRefresh size={12} className={loading ? "animate-spin" : ""} />
                      我已完成, 刷新
                    </button>
                  </div>
                </div>
              </div>
            )}
          </section>
        )}

        {error && devices === null && (
          <div className="nx-alert nx-alert-danger flex items-start gap-2">
            <IconXCircle size={13} className="mt-0.5 shrink-0" />
            <span className="min-w-0 flex-1 break-words">设备列表加载失败 · {error}</span>
            <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={load} disabled={loading}>
              <IconRefresh size={12} />
              重试
            </button>
          </div>
        )}

        {devices !== null && devices.length === 0 && !error && (
          <div className="nx-hint py-2 text-center text-[12px]">还没有设备接入: 点右上角「接入新设备」, 按面板提示完成接入。</div>
        )}
        {devices !== null && devices.length > 0 && visibleDevices?.length === 0 && (
          <div className="nx-hint py-2 text-center text-[12px]">没有符合当前搜索或过滤条件的设备。</div>
        )}

        {visibleDevices?.map((d) => (
          <DeviceCard
            key={d.id}
            device={d}
            now={now}
            online={onlineOf(d)}
            isAdmin={Boolean(isAdmin)}
            onPatch={patchDevice}
            onRevoked={(id) => {
              patchDevice(id, { revoked_at: Date.now() });
              void load();
            }}
            onRejected={removeDevice}
          />
        ))}

        <div id="fleet-base-urls">
          <BaseUrlsSection
            entries={baseUrls}
            isAdmin={Boolean(isAdmin)}
            status={baseUrlsStatus}
            loadError={baseUrlsError}
            onRetry={loadBaseUrls}
            onSaved={(entries) => {
              setBaseUrls(entries);
              setBaseUrlsStatus("ready");
              setBaseUrlsError(null);
            }}
          />
        </div>
      </div>

      <div className="flex shrink-0 items-center gap-3 border-t border-neutral-800/60 bg-neutral-950/40 px-3 py-1.5 text-[11px] text-neutral-500">
        <span>
          <IconCheckCircle size={11} className="mr-1 inline align-[-1px]" />
          设备列表由服务端按角色过滤: 普通用户只看自己的设备, 超级管理员看全部。
        </span>
      </div>
    </div>
  );
}
