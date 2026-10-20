// Device management view (FLEET149): the real device management UI in account mode.
// All data comes from the real /fleet/* HTTP contracts (see internal/fleet/server/http.go):
// the server filters the device list by role (superadmins see all + owner, regular users only their own);
// the desktop proxies the same contracts (remote_fleet_*) through the "account sync" link server session;
// logged out, a "log in to a server account to use this" guide view points to "Settings -> Account sync", and no request is issued.
// The metrics panel lives in ./MetricsPanel (with the CPU 24h trend line in ./MetricsChart), reused directly here;
// no separate chart logic is implemented.

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import { type FleetBaseURLEntry, type FleetDevice } from "../../ipc/fleetApi";
import { useAuth } from "../auth/store";
import { connectDevice, useUi } from "../../app/store";
import { WEB } from "../../ipc/env";
import { fleetClient } from "./clients";
import { useFleetAccount } from "./useFleetAccount";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { copyText as copyToClipboard } from "../../ui/clipboard";
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
import { formatTime, isOnline, ONLINE_THRESHOLD_MS, relativeTime } from "./format";
import { BaseUrlsSection } from "./BaseUrlsSection";
import { MetricsPanel } from "./MetricsPanel";
import { oneLineInstallCommand, oneLineInstallCommandWindows, shellQuote } from "./installCommand";

const ENROLL_TTL_OPTIONS = [
  { label: "5 分钟", ms: 5 * 60_000 },
  { label: "15 分钟", ms: 15 * 60_000 },
  { label: "1 小时", ms: 60 * 60_000 },
];

// The data directory matches the repo's per-user convention (platform.desktopDataDir: XDG_DATA_HOME
// or ~/.local/share), writable by regular users, fitting systemd --user / launchd per-user.
// Double quotes let the shell expand before invocation: NEXTERM_DATA_DIR takes top priority (matching the copy's override promise;
// in the CLI flags beat env, so the override must be embedded into the flag value), then XDG_DATA_HOME, finally $HOME.
const AGENT_DATA_DIR = '"${NEXTERM_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/NexTerm}"';

function enrollCommand(baseUrl: string, insecure: boolean, code: string): string {
  return `nexterm-agent enroll --server ${shellQuote(baseUrl)} --code ${shellQuote(code)}${insecure ? " --insecure" : ""} --data-dir ${AGENT_DATA_DIR}`;
}

function installCommand(): string {
  return `nexterm-agent install --data-dir ${AGENT_DATA_DIR}`;
}

function copyText(text: string, pushToast: (kind: "info" | "error" | "success", text: string) => void, what: string): void {
  void copyToClipboard(text)
    .then(() => pushToast("success", `${what}已复制`))
    .catch(() => pushToast("error", "复制失败，请手动复制"));
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
              登录服务端账号后可用
            </div>
            <div className="mt-1 text-[11.5px] leading-relaxed text-neutral-500">
              设备接入与远程终端依赖服务端账号体系。在「设置 → 账号同步」登录服务端账号后，即可管理接入设备、签发接入码并打开远程终端。
            </div>
          </div>
          <button
            type="button"
            className="nx-btn nx-btn-outline nx-btn-sm"
            onClick={() => openAccountSyncSettings()}
          >
            去设置 → 账号同步登录
          </button>
        </div>
      </div>
    </div>
  );
}

// openAccountSyncSettings opens the settings tab and scrolls to the "account sync" section; the settings page mounts asynchronously,
// so poll for the target node and give up after about 1 second.
function openAccountSyncSettings(attempt = 0): void {
  if (attempt === 0) {
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
  }
  const target = document.getElementById("settings-account");
  if (target) {
    target.scrollIntoView({ behavior: "smooth", block: "start" });
    return;
  }
  if (attempt < 20) window.setTimeout(() => openAccountSyncSettings(attempt + 1), 50);
}

interface DeviceCardProps {
  device: FleetDevice;
  now: number;
  online: boolean;
  isAdmin: boolean;
  onPatch: (id: string, patch: Partial<FleetDevice>) => void;
  onRevoked: (id: string) => void;
}

function DeviceCard({ device, now, online, isAdmin, onPatch, onRevoked }: DeviceCardProps) {
  const { pushToast } = useUi();
  const [metricsOpen, setMetricsOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const revoked = device.revoked_at !== 0;
  const agent = device.agent;
  // Heartbeat-determined online state feeds the badge hint text: event-driven online state defers to the control channel.
  const heartbeatOnline = agent ? isOnline(agent.last_seen_at || device.last_seen_at, now) : false;

  const revoke = async () => {
    const ok = await ask(`吊销设备「${device.name}」？\n\n吊销后设备凭证立即失效、控制通道断开，且不可恢复。`, {
      title: "吊销设备",
      kind: "warning",
    });
    if (!ok) return;
    setBusy(true);
    try {
      await fleetClient().revoke(device.id);
      pushToast("success", `已吊销「${device.name}」`);
      onRevoked(device.id);
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
        `设备「${device.name}」当前离线。\n\n此次修改仅保存为期望状态，设备下次连接服务器后生效。`,
        { title: "设备离线", kind: "info" },
      );
      if (!ok) return;
    }
    setBusy(true);
    try {
      const r = await fleetClient().setAutostart(device.id, desired);
      onPatch(device.id, { agent: { ...agent, desired_autostart: r.desired_autostart } });
      pushToast("success", `已将「${device.name}」的期望自启动设为${r.desired_autostart ? "开启" : "关闭"}`);
    } catch (e) {
      pushToast("error", describeError(e));
    } finally {
      setBusy(false);
    }
  };

  // reconcile semantics (internal/fleet/agent/runtime.go): desired=true aligns with installed&&enabled,
  // active is only whether the process is running right now, shown separately and not part of drift judgment.
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
        {!revoked && agent?.terminal_enabled && (
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
            disabled={!online}
            title={online ? "打开这台设备的工作区" : "设备离线，无法打开工作区"}
            onClick={() => void connectDevice(device)}
          >
            <IconTerminal size={11} />
            工作区
          </button>
        )}
        {!revoked && agent && (
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
            title="吊销后设备凭证立即失效，不可恢复"
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
            <label className="flex items-center gap-1.5" title="期望自启动：服务端保存的目标状态，设备代理下次连接时应用">
              <input
                type="checkbox"
                checked={agent.desired_autostart}
                disabled={busy || revoked}
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
                服务错误：{serviceState.last_error}
              </span>
            )}
            {!online && !revoked && (
              <span className="nx-hint text-[11px]">设备离线：修改只会保存为期望状态，上线后生效</span>
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
          该设备未安装设备代理，没有运行状态和指标。
        </div>
      )}
    </section>
  );
}

export function DevicesView() {
  const qc = useQueryClient();
  const { pushToast } = useUi();
  const account = useFleetAccount();
  const authStatus = useAuth((s) => s.status);
  const [devices, setDevices] = useState<FleetDevice[] | null>(null);
  const [deviceQuery, setDeviceQuery] = useState("");
  const [deviceFilter, setDeviceFilter] = useState<"all" | "online" | "offline" | "revoked">("all");
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
  // serverVersion is the real server version from /healthz (used for one-click install --version);
  // null means unavailable, and the whole one-click install section hides - never fabricate a version.
  const [serverVersion, setServerVersion] = useState<string | null>(null);
  // presence is the realtime signal from device://status events (device ID -> online state + receive time).
  // Past the heartbeat threshold it falls back to the heartbeat determination (polling fallback), consistent with existing display semantics.
  const [presence, setPresence] = useState<Record<string, { online: boolean; at: number }>>({});
  const loadSeq = useRef(0);
  const baseUrlsSeq = useRef(0);
  const versionSeq = useRef(0);
  // accountEpoch bumps only on account switch (manual refresh leaves it alone), isolating in-flight issuance
  // then/catch/finally: late completions from an old account never write into the new account's view.
  const accountEpoch = useRef(0);
  const deviceScrollRef = useRef<HTMLDivElement>(null);
  const deviceListRef = useRef<HTMLDivElement>(null);
  const [deviceListScrollMargin, setDeviceListScrollMargin] = useState(0);

  const isAdmin = account.isAdmin;
  const userId = account.userId;

  // Account-scope isolation: when user.id changes, reset all account state (devices/base URLs/one-time enroll codes) during render,
  // and bump loadSeq to invalidate late responses from the old account; AuthGate is only an overlay, so logout/login does not unmount this pane.
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
    const seq = ++baseUrlsSeq.current;
    setBaseUrlsStatus("loading");
    setBaseUrlsError(null);
    void fleetClient()
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
    const seq = ++loadSeq.current;
    setLoading(true);
    setError(null);
    loadBaseUrls();
    fleetClient()
      .devices()
      .then((r) => {
        if (seq === loadSeq.current) {
          setDevices(r.devices);
          qc.setQueryData(["fleet-devices", userId], r);
        }
      })
      .catch((e: unknown) => {
        if (seq === loadSeq.current) setError(describeError(e));
      })
      .finally(() => {
        if (seq === loadSeq.current) setLoading(false);
      });
  }, [loadBaseUrls, qc, userId]);

  useEffect(() => {
    if (!userId) return;
    load();
  }, [userId, load]);

  // Polling fallback: WEB stays realtime via the event stream and silently refreshes every 60s to converge losses/disconnects;
  // the desktop has no /ws/events channel, so online state relies on polling alone, tightened to 30 seconds.
  // Silent refreshes do not touch the loading state, avoiding periodic UI disturbance.
  const poll = useCallback(() => {
    const seq = ++loadSeq.current;
    fleetClient()
      .devices()
      .then((r) => {
        if (seq === loadSeq.current) {
          setDevices(r.devices);
          qc.setQueryData(["fleet-devices", userId], r);
        }
      })
      .catch(() => {});
  }, [qc, userId]);

  useEffect(() => {
    if (!userId) return;
    const timer = setInterval(poll, WEB ? 60_000 : 30_000);
    return () => clearInterval(timer);
  }, [userId, poll]);

  // device://status realtime online/offline: only writes presence; badges and terminal buttons update on render;
  // IDs not in the current account's device list (other users' devices) are never rendered and are naturally ignored.
  // Only WEB has this event channel; the desktop relies on the polling above.
  useEffect(() => {
    if (!WEB || !userId) return;
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
  }, [userId]);

  // Refresh immediately when the event WS reconnects and may have missed events (resync), without waiting for the next poll round.
  useEffect(() => {
    if (!WEB || !userId) return;
    return onEventsResync(() => load());
  }, [userId, load]);

  // Effective online = realtime event first (within the heartbeat threshold), otherwise fall back to the heartbeat determination; non-agent devices are always offline.
  const onlineOf = (d: FleetDevice): boolean => {
    if (!d.agent) return false;
    const p = presence[d.id];
    if (p && now - p.at <= ONLINE_THRESHOLD_MS) return p.online;
    return isOnline(d.agent.last_seen_at || d.last_seen_at, now);
  };

  // Read the real server version from /healthz when opening the enroll panel (for one-click install --version);
  // failure or an empty string both map to null: the one-click install section hides entirely, never fabricating a version.
  useEffect(() => {
    if (!enrollOpen || !userId) return;
    const seq = ++versionSeq.current;
    fleetClient()
      .serverVersion()
      .then((v) => {
        if (seq === versionSeq.current) setServerVersion(v || null);
      })
      .catch(() => {
        if (seq === versionSeq.current) setServerVersion(null);
      });
  }, [enrollOpen, userId]);

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(timer);
  }, []);

  const normalizedDeviceQuery = deviceQuery.trim().toLowerCase();
  const matchesDeviceQuery = (d: FleetDevice) => {
    if (!normalizedDeviceQuery) return true;
    return [d.name, d.id, d.kind, d.owner?.username, d.agent?.platform, d.agent?.current_url]
      .map((value) => value?.toLowerCase() ?? "")
      .some((value) => value.includes(normalizedDeviceQuery));
  };
  const visibleDevices = devices?.filter((d) => {
    const revoked = d.revoked_at !== 0;
    if (deviceFilter === "revoked") return revoked && matchesDeviceQuery(d);
    if (revoked) return false;
    if (deviceFilter === "online" && !onlineOf(d)) return false;
    if (deviceFilter === "offline" && onlineOf(d)) return false;
    return matchesDeviceQuery(d);
  });
  const devicesVirtualized = (visibleDevices?.length ?? 0) > 40;
  const deviceVirtualizer = useVirtualizer({
    count: visibleDevices?.length ?? 0,
    getScrollElement: () => deviceScrollRef.current,
    estimateSize: () => 152,
    overscan: 4,
    gap: 12,
    enabled: devicesVirtualized,
    getItemKey: (index) => visibleDevices?.[index]?.id ?? index,
    scrollMargin: deviceListScrollMargin,
    initialRect: { width: 800, height: 600 },
  });

  useLayoutEffect(() => {
    const list = deviceListRef.current;
    const scroller = deviceScrollRef.current;
    if (!list || !scroller) return;
    const next = list.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop;
    setDeviceListScrollMargin((current) => (Math.abs(current - next) < 1 ? current : next));
  }, []);

  if (account.loading) {
    return (
      <div className="nx-pane">
        <div className="nx-toolbar flex-wrap">
          <IconMonitor size={14} className="text-neutral-500" />
          <span className="nx-toolbar-title">设备管理</span>
          <span className="nx-hint">账号状态读取中…</span>
        </div>
      </div>
    );
  }

  if (!userId && !WEB) {
    return <FleetUnsupported />;
  }

  if (!userId) {
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
                {authOff ? "这台服务器关闭了账号功能，设备管理不可用。" : "登录后才能查看与管理接入的设备。"}
              </div>
              {!authOff && <div className="mt-1 text-[11.5px] text-neutral-500">登录入口在「设置 → 账号同步」里的「账号」卡。</div>}
            </div>
          </div>
        </div>
      </div>
    );
  }

  const issueEnrollCode = async () => {
    if (!userId || baseUrlsStatus !== "ready") return;
    const epoch = accountEpoch.current;
    setIssuing(true);
    try {
      const r = await fleetClient().issueEnrollCode(ttlMs);
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
  const windowsRows =
    issued && serverVersion
      ? baseUrls.map((entry) =>
          copyCommandRow(
            `one-line-windows:${entry.url}`,
            oneLineInstallCommandWindows(entry.url, Boolean(entry.insecure), issued.code, serverVersion),
            "Windows 一键安装命令",
          ),
        )
      : null;
  const installRow = issued ? copyCommandRow("install", installCommand(), "安装命令") : null;
  const hiddenRevokedCount = devices?.filter((d) => d.revoked_at !== 0).length ?? 0;
  const renderDeviceCard = (d: FleetDevice) => (
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
    />
  );

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

      <div ref={deviceScrollRef} className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto p-3">
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
              <option value="revoked">已吊销</option>
            </select>
            {(deviceQuery.trim() !== "" || deviceFilter !== "all") && (
              <span className="nx-hint text-[11px]">
                显示 {visibleDevices?.length ?? 0} / {devices.length} 台
              </span>
            )}
            {deviceFilter !== "revoked" && hiddenRevokedCount > 0 && (
              <span className="nx-hint text-[11px]">已隐藏 {hiddenRevokedCount} 台已吊销设备（选「已吊销」可见）</span>
            )}
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
                  签发一次性接入码，并在设备上执行接入命令。接入码只能使用一次，到期自动作废。
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
                      {baseUrlsStatus === "loading" ? "接入地址读取中，成功后才能签发。" : "接入地址读取失败，请在下方重试后签发。"}
                    </span>
                  )}
                </div>
              </>
            ) : (
              <div className="nx-alert nx-alert-info flex flex-col gap-2">
                <div className="flex items-start gap-2">
                  <IconInfo size={14} className="mt-0.5 shrink-0" />
                  <div className="min-w-0">
                    接入码只能使用一次，有效期至 {formatTime(issued.expiresAt)}；完成后不再显示。设备凭证原文仅保存在设备本地，服务端只保存散列。
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
                          <span>还没有配置接入地址，设备无法接入，也无法生成接入命令。</span>
                          <button type="button" className="nx-btn nx-btn-outline nx-btn-xs shrink-0" onClick={jumpToBaseUrls}>
                            去配置接入地址
                          </button>
                        </>
                      ) : (
                        <span>管理员还没有配置接入地址，请联系管理员配置后再接入。</span>
                      )}
                    </div>
                  ) : (
                    <>
                      {serverVersion ? (
                        <>
                          <div className="text-[12px] text-neutral-200">
                            在设备上用<b>普通用户</b>执行一键接入命令（Linux / macOS）：
                          </div>
                          {oneLineRows?.[0]}
                          <div className="text-[12px] text-neutral-200">Windows 设备请用 PowerShell 版：</div>
                          {windowsRows?.[0]}
                        </>
                      ) : (
                        <>
                          <div className="text-[12px] text-neutral-200">
                            在设备上用<b>普通用户</b>依次执行两条命令：第一条兑换设备凭证，第二条安装后台服务。
                          </div>
                          {enrollRows?.[0]}
                          {installRow}
                        </>
                      )}
                      <div className="text-[11.5px] text-neutral-400">
                        本页面不会执行安装，也无法确认安装结果；请以设备上的输出为准。
                      </div>
                      <details className="rounded border border-red-500/20 px-2 py-1.5">
                        <summary className="cursor-pointer text-[11.5px] text-neutral-300">高级说明：多地址、手动接入与服务管理</summary>
                        <div className="mt-2 flex flex-col gap-1.5">
                          {baseUrls.length > 1 && (
                            <>
                              <div className="text-[11.5px] text-neutral-400">其他接入地址按配置顺序作为备用，第一个可达的即接入点：</div>
                              {serverVersion ? [...(oneLineRows?.slice(1) ?? []), ...(windowsRows?.slice(1) ?? [])] : enrollRows?.slice(1)}
                            </>
                          )}
                          {serverVersion && (
                            <>
                              <div className="text-[11.5px] text-neutral-400">
                                设备已安装 nexterm-agent 时，也可手动执行 enroll 与 install：
                              </div>
                              {enrollRows}
                              {installRow}
                            </>
                          )}
                          <div className="text-[11.5px] text-neutral-400">
                            agent install 会安装随用户登录自动启动的后台服务（systemd --user / launchd）；数据目录默认使用用户数据目录，可通过 NEXTERM_DATA_DIR 同时覆盖 enroll 和 install。
                          </div>
                        </div>
                      </details>
                    </>
                  )}
                  <div className="flex flex-wrap items-center gap-2 border-t border-red-500/20 pt-2">
                    <span className="text-[12px] text-neutral-200">
                      执行完成后刷新列表：设备接入即上线，从设备卡片打开「终端」。
                    </span>
                    <button type="button" className="nx-btn nx-btn-primary nx-btn-sm shrink-0" disabled={loading} onClick={() => void load()}>
                      <IconRefresh size={12} className={loading ? "animate-spin" : ""} />
                      刷新设备列表
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
          <div className="nx-hint py-2 text-center text-[12px]">还没有接入设备，请选择右上角「接入新设备」并按提示操作。</div>
        )}
        {devices !== null && devices.length > 0 && visibleDevices?.length === 0 && (
          <div className="nx-hint py-2 text-center text-[12px]">没有符合当前搜索或过滤条件的设备。</div>
        )}

        {visibleDevices && visibleDevices.length > 0 && (devicesVirtualized ? (
          <div
            ref={deviceListRef}
            className="relative shrink-0"
            style={{ height: deviceVirtualizer.getTotalSize() }}
          >
            {deviceVirtualizer.getVirtualItems().map((virtualItem) => (
              <div
                key={virtualItem.key}
                ref={deviceVirtualizer.measureElement}
                data-index={virtualItem.index}
                className="absolute left-0 right-0 top-0"
                style={{ transform: `translateY(${virtualItem.start - deviceListScrollMargin}px)` }}
              >
                {renderDeviceCard(visibleDevices[virtualItem.index])}
              </div>
            ))}
          </div>
        ) : (
          visibleDevices.map(renderDeviceCard)
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
          设备列表由服务端按角色过滤：普通用户只看自己的设备，超级管理员看全部。
        </span>
      </div>
    </div>
  );
}
