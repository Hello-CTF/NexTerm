// Device management view (FLEET149): the real device management UI in account mode.
// All data comes from the real /fleet/* HTTP contracts (see internal/fleet/server/http.go):
// the server filters the device list by role (superadmins see all + owner, regular users only their own);
// the desktop proxies the same contracts (remote_fleet_*) through the "account sync" link server session;
// logged out, a "log in to a server account to use this" guide view points to "Settings -> Account sync", and no request is issued.
// The metrics panel lives in ./MetricsPanel (with the CPU 24h trend line in ./MetricsChart), reused directly here;
// no separate chart logic is implemented.

import { useCallback, useEffect, useLayoutEffect, useRef, useState, type MouseEvent as ReactMouseEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useQueryClient } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import { type FleetBaseURLEntry, type FleetDevice } from "../../ipc/fleetApi";
import { useAuth } from "../auth/store";
import { connectDevice, deviceAssetId, useUi } from "../../app/store";
import { openHostDetail } from "../hosts/hostDetail";
import { WEB } from "../../ipc/env";
import { fleetClient } from "./clients";
import { useFleetAccount } from "./useFleetAccount";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { copyText as copyToClipboard } from "../../ui/clipboard";
import { EVENTS, listenEvent, type DeviceStatusEvent } from "../../ipc/events";
import { onEventsResync } from "../../ipc/webTransport";
import { ContextMenu, type ContextMenuState, type MenuItem } from "../../ui/ContextMenu";
import {
  IconCheckCircle,
  IconChevronDown,
  IconChevronUp,
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
import { showSettingsSection } from "../settings/settingsNavigation";

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
        <div className="flex w-full max-w-[340px] flex-col items-center gap-3 rounded-xl border border-neutral-800/70 bg-neutral-950/45 px-6 py-7 text-center">
          <div className="nx-empty-icon">
            <IconMonitor size={18} />
          </div>
          <div>
            <div className="text-[13.5px] font-semibold tracking-tight text-neutral-100">
              登录服务端账号后可用
            </div>
            <div className="mt-1 text-[11.5px] leading-relaxed text-neutral-500">
              设备接入与远程终端依赖服务端账号体系。在「设置 → 账号同步」登录服务端账号后，即可管理接入的设备、生成接入码并打开远程终端。
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

// openAccountSyncSettings opens the settings tab on the "account sync" section.
function openAccountSyncSettings(): void {
  useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
  showSettingsSection("settings-account");
}

const PLATFORM_LABELS: Record<string, string> = { darwin: "macOS", linux: "Linux", windows: "Windows" };

function platformLabel(platform: string): string {
  return PLATFORM_LABELS[platform.toLowerCase()] ?? platform;
}

function IconMoreDots({ size = 14 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
      <circle cx="3" cy="8" r="1.3" />
      <circle cx="8" cy="8" r="1.3" />
      <circle cx="13" cy="8" r="1.3" />
    </svg>
  );
}

function DetailRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[7.5rem_minmax(0,1fr)] items-start gap-x-3 gap-y-0.5 text-[12px]">
      <dt className="nx-hint">{label}</dt>
      <dd className="m-0 min-w-0 break-words text-neutral-200">{children}</dd>
    </div>
  );
}

// A version is comparable only when it looks like a release (x.y.z); "dev", "0.0.0" and empty values are not.
function releaseVersion(raw: string | undefined | null): string | null {
  const v = (raw ?? "").trim().replace(/^v/i, "");
  if (!/^\d+\.\d+\.\d+/.test(v) || /^0\.0\.0(?![\d.])/.test(v)) return null;
  return v;
}

interface DeviceCardProps {
  device: FleetDevice;
  serverVersion: string | null;
  now: number;
  online: boolean;
  isAdmin: boolean;
  onPatch: (id: string, patch: Partial<FleetDevice>) => void;
  onRevoked: (id: string) => void;
}

function DeviceCard({ device, serverVersion: listServerVersion, now, online, isAdmin, onPatch, onRevoked }: DeviceCardProps) {
  const pushToast = useUi((s) => s.pushToast);
  const [metricsOpen, setMetricsOpen] = useState(false);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [reasonOpen, setReasonOpen] = useState(false);
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  const [busy, setBusy] = useState(false);
  const revoked = device.revoked_at !== 0;
  const agent = device.agent;
  const lastSeenAt = agent?.last_seen_at || device.last_seen_at;
  // Heartbeat-determined online state feeds the status hint: event-driven online state defers to the control channel.
  const heartbeatOnline = agent ? isOnline(lastSeenAt, now) : false;

  const revoke = async () => {
    const ok = await ask(
      `撤销设备「${device.name}」的接入？\n\n撤销后，这台设备的登录凭据立即失效、远程连接会断开，且无法恢复。设备上的文件和程序不会被删除；要再次使用，需要重新生成接入码并重新接入。`,
      { title: "撤销接入", kind: "warning" },
    );
    if (!ok) return;
    setBusy(true);
    try {
      await fleetClient().revoke(device.id);
      pushToast("success", `已撤销「${device.name}」的接入`);
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
      pushToast("success", `已${r.desired_autostart ? "开启" : "关闭"}「${device.name}」的随登录自动启动`);
    } catch (e) {
      pushToast("error", describeError(e));
    } finally {
      setBusy(false);
    }
  };

  const toggleTerminal = async () => {
    if (!agent) return;
    const enabled = !agent.terminal_enabled;
    if (!enabled) {
      const ok = await ask(
        `关闭「${device.name}」的远程终端？\n\n关闭后，这台设备将拒绝新的远程终端、文件和端口转发连接，「打开工作区」也不可用。已经打开的连接不会被强制断开，会保持到你自己关闭为止。之后可以随时重新开启。${online ? "" : "\n\n设备当前离线，设备本身会在下次连接服务器后同步这项设置。"}`,
        { title: "关闭远程终端", kind: "warning" },
      );
      if (!ok) return;
    } else if (!online) {
      const ok = await ask(
        `设备「${device.name}」当前离线。\n\n此次修改会先保存在服务器上，设备下次连接服务器后生效。`,
        { title: "设备离线", kind: "info" },
      );
      if (!ok) return;
    }
    setBusy(true);
    try {
      const r = await fleetClient().setTerminalAccess(device.id, enabled);
      onPatch(device.id, { agent: { ...agent, terminal_enabled: r.terminal_enabled } });
      pushToast("success", `已${r.terminal_enabled ? "开启" : "关闭"}「${device.name}」的远程终端`);
    } catch (e) {
      pushToast("error", describeError(e));
    } finally {
      setBusy(false);
    }
  };

  // The agent only self-upgrades when the control protocol is incompatible (version_mismatch), not on every version
  // difference, so this note stays informational and makes no promise about upgrading.
  const agentRelease = releaseVersion(agent?.app_version);
  const serverRelease = releaseVersion(listServerVersion);
  const versionBehind = Boolean(!revoked && agent && online && agentRelease && serverRelease && agentRelease !== serverRelease);

  // reconcile semantics (internal/fleet/agent/runtime.go): desired=true aligns with installed&&enabled,
  // active is only whether the process is running right now, shown separately and not part of drift judgment.
  const serviceState = agent?.service_state;
  const hasServiceReport = Boolean(serviceState && (serviceState.installed || serviceState.enabled || serviceState.active || serviceState.last_reconcile_at));
  const autostartActual = Boolean(serviceState?.installed && serviceState?.enabled);
  const runningText = !hasServiceReport ? "未上报" : serviceState?.active ? "运行中" : "未运行";
  const autostartDrift = Boolean(agent && hasServiceReport && agent.desired_autostart !== autostartActual && !revoked);
  const serviceError = serviceState?.last_error;

  const openMenu = (e: ReactMouseEvent<HTMLButtonElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    const items: MenuItem[] = [];
    items.push({ kind: "item", label: "主机详情", hint: "会话、容器和指标", onSelect: () => openHostDetail(deviceAssetId(device.id)) });
    items.push({ kind: "item", label: "复制设备名称", onSelect: () => copyText(device.name, pushToast, "设备名称") });
    if (agent?.current_url) {
      const url = agent.current_url;
      items.push({ kind: "item", label: "复制接入地址", onSelect: () => copyText(url, pushToast, "接入地址") });
    }
    items.push({ kind: "separator" });
    items.push({ kind: "item", label: "撤销接入…", icon: <IconTrash size={12} />, danger: true, disabled: busy, onSelect: () => void revoke() });
    setMenu({ x: rect.left, y: rect.bottom + 4, items, title: device.name });
  };

  const statusLabel = revoked ? "已撤销接入" : agent ? (online ? "在线" : "离线") : "未安装设备代理";
  const statusTone = revoked ? "text-red-300" : online ? "text-green-300" : "text-neutral-300";
  const statusTitle = !agent || revoked
    ? undefined
    : online
      ? heartbeatOnline ? "最近 3 分钟内有心跳" : "控制通道实时在线"
      : `最近一次上报：${lastSeenAt ? formatTime(lastSeenAt) : "从未"}`;

  const lastSeenText = lastSeenAt ? (
    <time dateTime={new Date(lastSeenAt).toISOString()} title={formatTime(lastSeenAt)}>
      {relativeTime(lastSeenAt, now)}上报
    </time>
  ) : (
    <span>尚未上报</span>
  );

  return (
    <section className={`nx-card ${revoked ? "opacity-60" : ""}`}>
      <div className="flex flex-wrap items-center gap-2">
        <IconMonitor size={15} className="text-neutral-400" />
        <span className="nx-card-title min-w-0 break-all">{device.name}</span>
        <span className={`inline-flex shrink-0 items-center gap-1.5 text-[12px] ${statusTone}`} title={statusTitle}>
          <span className="nx-dot" aria-hidden="true" />
          {statusLabel}
        </span>
        {agent?.platform && <span className="nx-hint shrink-0">{platformLabel(agent.platform)}</span>}
        {isAdmin && device.owner && (
          <span className="nx-badge nx-badge-blue shrink-0" title={`所有者 ${device.owner.username}`}>
            {device.owner.username}
          </span>
        )}
        <div className="nx-spacer" />
        {!revoked && agent && online && agent.terminal_enabled && (
          <button
            type="button"
            className="nx-btn nx-btn-primary nx-btn-sm shrink-0"
            title="在这台设备上打开工作区"
            onClick={() => void connectDevice(device)}
          >
            <IconTerminal size={11} />
            打开工作区
          </button>
        )}
        {!revoked && agent && !online && (
          <button
            type="button"
            className="nx-btn nx-btn-outline nx-btn-sm shrink-0"
            aria-expanded={reasonOpen}
            onClick={() => setReasonOpen(!reasonOpen)}
          >
            离线原因
          </button>
        )}
        {!revoked && (
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
            aria-label="更多操作"
            aria-haspopup="menu"
            onClick={openMenu}
          >
            <IconMoreDots size={14} />
          </button>
        )}
      </div>

      <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[12px] text-neutral-400">
        {revoked ? (
          <span className="text-red-300">撤销于 {formatTime(device.revoked_at)}</span>
        ) : agent ? (
          <>
            {lastSeenText}
            {agent.app_version && <span>· v{agent.app_version.replace(/^v/i, "")}</span>}
            <span className={agent.terminal_enabled ? "" : "text-amber-300"}>
              · 远程终端{agent.terminal_enabled ? "已开启" : "已关闭"}
            </span>
          </>
        ) : (
          <span>{device.kind === "browser" ? "浏览器设备" : device.kind === "local-helper" ? "桌面本机终端" : device.kind} · 创建于 {formatTime(device.created_at)}</span>
        )}
      </div>

      {versionBehind && (
        <div className="nx-hint mt-1 text-[11px]" role="note">
          代理版本 {agentRelease}，服务端 {serverRelease}。版本不同不影响使用；仅当控制通道协议不兼容时，代理才会自动升级，否则需要在设备上手动更新。
        </div>
      )}

      {!revoked && agent && !online && reasonOpen && (
        <div className="nx-alert nx-alert-info mt-2 text-[12px]">
          <div>
            设备{lastSeenAt ? `在 ${formatTime(lastSeenAt)} 之后就没有再联系服务器` : "还没有联系过服务器"}。常见原因：设备关机或断网、后台服务没有运行。
          </div>
          <div className="mt-1 text-neutral-400">
            先确认设备已开机联网；若后台服务没有运行，可在设备上重新执行 <code className="nx-code font-mono">nexterm-agent install</code>。仍无法恢复时，用页面右上角的「接入新设备」重新接入。
          </div>
        </div>
      )}

      {!revoked && autostartDrift && (
        <div className="mt-2 flex flex-wrap items-center gap-2 text-[12px] text-amber-300" role="status">
          <span>
            {agent?.desired_autostart
              ? "已设置随用户登录自动启动，但当前没有生效"
              : "已设置不自动启动，但后台服务当前仍会随登录启动"}
          </span>
          {!online && <span className="nx-hint">设备离线，上线后才会调整</span>}
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
            onClick={() => setDetailsOpen(true)}
          >
            查看详情
          </button>
        </div>
      )}
      {!revoked && serviceError && (
        <div className="mt-1 min-w-0 break-all text-[12px] text-red-300" title={serviceError}>
          后台服务报错：{serviceError}
        </div>
      )}

      {!revoked && agent && (
        <div className="mt-2 flex flex-wrap items-center gap-1 border-t border-neutral-800/60 pt-2">
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
            onClick={() => setDetailsOpen(!detailsOpen)}
            aria-expanded={detailsOpen}
          >
            {detailsOpen ? <IconChevronUp size={11} /> : <IconChevronDown size={11} />}
            详情
          </button>
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
            onClick={() => setMetricsOpen(!metricsOpen)}
            aria-expanded={metricsOpen}
          >
            指标
          </button>
        </div>
      )}

      {!revoked && agent && detailsOpen && (
        <dl className="m-0 mt-2 flex flex-col gap-1.5">
          <DetailRow label="创建时间">{formatTime(device.created_at)}</DetailRow>
          <DetailRow label="代理版本">{agent.app_version ? `v${agent.app_version.replace(/^v/i, "")}` : "未上报"}</DetailRow>
          {agent.platform && <DetailRow label="系统平台">{platformLabel(agent.platform)}</DetailRow>}
          <DetailRow label="接入地址">
            {agent.current_url ? (
              <span className="flex flex-wrap items-center gap-2">
                <code className="nx-code min-w-0 break-all font-mono text-[11.5px]">{agent.current_url}</code>
                <button
                  type="button"
                  className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
                  aria-label="复制接入地址"
                  onClick={() => copyText(agent.current_url, pushToast, "接入地址")}
                >
                  <IconCopy size={11} />
                  复制
                </button>
              </span>
            ) : (
              <span className="nx-hint">未上报</span>
            )}
          </DetailRow>
          <DetailRow label="远程终端">
            <label className="flex flex-wrap items-center gap-1.5">
              <input
                type="checkbox"
                aria-label="远程终端"
                checked={agent.terminal_enabled}
                disabled={busy}
                onChange={() => void toggleTerminal()}
              />
              <span>{agent.terminal_enabled ? "已开启" : "已关闭"}</span>
              <span className="nx-hint">· {agent.terminal_enabled ? "允许新建终端、文件和端口转发连接" : "拒绝新建远程连接，已打开的连接不受影响"}</span>
            </label>
            {!online && <div className="nx-hint mt-0.5 text-[11px]">设备离线：服务器立即按新设置拒绝或放行，设备上线后同步</div>}
          </DetailRow>
          <DetailRow label="随登录自动启动">
            <label className="flex flex-wrap items-center gap-1.5">
              <input
                type="checkbox"
                aria-label="随登录自动启动"
                checked={agent.desired_autostart}
                disabled={busy}
                onChange={() => void toggleAutostart()}
              />
              <span>{agent.desired_autostart ? "已设置自动启动" : "未设置自动启动"}</span>
              <span className="nx-hint">
                · 当前{hasServiceReport ? (autostartActual ? "已生效" : "未生效") : "未上报"}
              </span>
            </label>
            {!online && <div className="nx-hint mt-0.5 text-[11px]">设备离线：修改只会保存为期望状态，上线后生效</div>}
          </DetailRow>
          <DetailRow label="后台服务">{runningText}</DetailRow>
        </dl>
      )}

      {!revoked && agent && metricsOpen && (
        <div className="mt-2 border-t border-neutral-800/60 pt-2">
          <MetricsPanel deviceId={device.id} online={online} />
        </div>
      )}

      {!agent && !revoked && (
        <div className="nx-hint mt-2 border-t border-neutral-800/60 pt-2 text-[11px]">
          该设备未安装设备代理，没有运行状态和指标。
        </div>
      )}
      {createPortal(<ContextMenu state={menu} onClose={() => setMenu(null)} />, document.body)}
    </section>
  );
}

export function DevicesView() {
  const qc = useQueryClient();
  const pushToast = useUi((s) => s.pushToast);
  const account = useFleetAccount();
  const authStatus = useAuth((s) => s.status);
  const [devices, setDevices] = useState<FleetDevice[] | null>(null);
  const [listServerVersion, setListServerVersion] = useState<string | null>(null);
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
    setListServerVersion(null);
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
          setListServerVersion(r.server_version || null);
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
          setListServerVersion(r.server_version || null);
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

  const measureDeviceListScrollMargin = useCallback(() => {
    const list = deviceListRef.current;
    const scroller = deviceScrollRef.current;
    if (!list || !scroller) return;
    const next = list.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop;
    setDeviceListScrollMargin((current) => (Math.abs(current - next) < 1 ? current : next));
  }, []);

  // The list only renders once the asynchronous device response arrives, so a mount-only
  // effect misses it; and any state rendered above the list (the enrollment panel and its
  // issued-code content, filter hints) moves the list without remounting it or resizing the
  // window. Measure via a callback ref at mount, after every render, and on window resizes;
  // the setter no-ops when the offset is unchanged, so renders without layout changes are free.
  const deviceListCallbackRef = useCallback(
    (node: HTMLDivElement | null) => {
      deviceListRef.current = node;
      if (node) measureDeviceListScrollMargin();
    },
    [measureDeviceListScrollMargin],
  );

  useLayoutEffect(() => {
    measureDeviceListScrollMargin();
  });

  useEffect(() => {
    window.addEventListener("resize", measureDeviceListScrollMargin);
    return () => window.removeEventListener("resize", measureDeviceListScrollMargin);
  }, [measureDeviceListScrollMargin]);

  // The enrollment panel's native advanced <details> changes the list's offset without a
  // React render or window resize. Its toggle event fires after the open state applies, so
  // measuring here reads the settled layout (getBoundingClientRect forces reflow).
  const handleAdvancedDetailsToggle = useCallback(() => {
    measureDeviceListScrollMargin();
  }, [measureDeviceListScrollMargin]);

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
          <div className="flex w-full max-w-[320px] flex-col items-center gap-3 rounded-xl border border-neutral-800/70 bg-neutral-950/45 px-6 py-7 text-center">
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
      serverVersion={listServerVersion}
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
              placeholder="按名称、平台或地址搜索"
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
              <option value="revoked">已撤销接入</option>
            </select>
            {(deviceQuery.trim() !== "" || deviceFilter !== "all") && (
              <span className="nx-hint text-[11px]">
                显示 {visibleDevices?.length ?? 0} / {devices.length} 台
              </span>
            )}
            {deviceFilter !== "revoked" && hiddenRevokedCount > 0 && (
              <span className="nx-hint text-[11px]">已隐藏 {hiddenRevokedCount} 台已撤销接入的设备（过滤选「已撤销接入」可见）</span>
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
                  先生成一个一次性接入码，再到要接入的机器上执行命令，机器就会出现在这个列表里。接入码只能使用一次，到期自动作废。
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
                    {issuing ? "生成中…" : "生成接入码"}
                  </button>
                  {baseUrlsStatus !== "ready" && (
                    <span className="nx-hint text-[11px]">
                      {baseUrlsStatus === "loading" ? "接入地址读取中，读取成功后才能生成接入码。" : "接入地址读取失败，请在下方重试后再生成接入码。"}
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
                      <details className="rounded border border-red-500/20 px-2 py-1.5" onToggle={handleAdvancedDetailsToggle}>
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
                      命令执行完成后刷新列表，设备会出现在这里；在线后可点设备卡片上的「打开工作区」。
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
          <div className="flex flex-col items-center gap-2 py-4 text-center">
            <div className="nx-hint text-[12px]">还没有接入设备。生成接入码后，在目标机器上执行一条命令即可接入。</div>
            {!enrollOpen && (
              <button type="button" className="nx-btn nx-btn-primary nx-btn-sm" onClick={() => setEnrollOpen(true)}>
                <IconPlus size={12} />
                开始接入
              </button>
            )}
          </div>
        )}
        {devices !== null && devices.length > 0 && visibleDevices?.length === 0 && (
          <div className="nx-hint py-2 text-center text-[12px]">没有符合当前搜索或过滤条件的设备。</div>
        )}

        {visibleDevices && visibleDevices.length > 0 && (devicesVirtualized ? (
          <div
            ref={deviceListCallbackRef}
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
          你只能看到自己接入的设备；超级管理员可以看到所有人的设备。
        </span>
      </div>
    </div>
  );
}
