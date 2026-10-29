// Guided device enrollment panel: pick a platform, copy one command, then watch for the new device to appear.
// State that must survive closing the panel (the issued code and the device list baseline) is owned by DevicesView;
// this component only renders it and drives the 3s polling while the code is still waiting to be used.

import { useEffect, useRef, useState, type ReactNode } from "react";
import type { FleetBaseURLEntry, FleetDevice } from "../../ipc/fleetApi";
import { isWindows } from "../../app/platform";
import { copyText as copyToClipboard } from "../../ui/clipboard";
import { Select } from "../../ui/Select";
import { IconCheckCircle, IconCopy, IconPlus, IconRefresh, IconTerminal, IconXCircle } from "../../ui/icons";
import { oneLineInstallCommand, oneLineInstallCommandWindows } from "./installCommand";

export interface IssuedEnrollCode {
  code: string;
  expiresAt: number;
  // Device IDs that already existed when the code was issued; any other active device counts as newly enrolled.
  baselineIds: string[];
}

export const ENROLL_TTL_OPTIONS = [
  { label: "5 分钟", ms: 5 * 60_000 },
  { label: "15 分钟", ms: 15 * 60_000 },
  { label: "1 小时", ms: 60 * 60_000 },
];

const POLL_INTERVAL_MS = 3000;
const CLOCK_INTERVAL_MS = 5000;
const COPIED_FEEDBACK_MS = 2000;

type Platform = "unix" | "windows";

interface CommandRow {
  key: string;
  command: string;
  label: string;
  what: string;
}

export interface EnrollPanelProps {
  baseUrls: FleetBaseURLEntry[];
  baseUrlsStatus: "loading" | "error" | "ready";
  isAdmin: boolean;
  // null when /healthz gave no usable release version: one-line commands are then unavailable.
  serverVersion: string | null;
  devices: FleetDevice[] | null;
  issued: IssuedEnrollCode | null;
  issuing: boolean;
  issueError: string | null;
  ttlMs: number;
  onTtlChange: (ms: number) => void;
  onIssue: () => void;
  onClose: () => void;
  onPoll: () => void;
  onOpenWorkspace: (device: FleetDevice) => void;
  onJumpBaseUrls: () => void;
  onLayoutChange: () => void;
  isOnline: (device: FleetDevice) => boolean;
  outdatedNote: (device: FleetDevice) => string | null;
  manualRows: (code: string) => { enroll: CommandRow[]; install: CommandRow };
}

export function expiryText(expiresAt: number, now: number): string {
  const ms = expiresAt - now;
  if (ms <= 0) return "接入码已失效";
  const minutes = Math.ceil(ms / 60_000);
  if (minutes <= 1) return "接入码不足 1 分钟后失效";
  return `接入码 ${minutes} 分钟后失效`;
}

function defaultPlatform(): Platform {
  return isWindows() ? "windows" : "unix";
}

export function EnrollPanel(props: EnrollPanelProps) {
  const {
    baseUrls,
    baseUrlsStatus,
    isAdmin,
    serverVersion,
    devices,
    issued,
    issuing,
    issueError,
    ttlMs,
    onTtlChange,
    onIssue,
    onClose,
    onPoll,
    onOpenWorkspace,
    onJumpBaseUrls,
    onLayoutChange,
    isOnline,
    outdatedNote,
    manualRows,
  } = props;
  const [platform, setPlatform] = useState<Platform>(defaultPlatform);
  const [now, setNow] = useState(() => Date.now());
  const [copied, setCopied] = useState<string | null>(null);
  const [copyFailed, setCopyFailed] = useState(false);
  // The raw code and manual commands are only mounted while the disclosure is open.
  const [manualOpen, setManualOpen] = useState(false);
  const copyTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const headingRef = useRef<HTMLHeadingElement>(null);

  const expired = issued !== null && issued.expiresAt <= now;
  const baseline = issued ? new Set(issued.baselineIds) : null;
  const joined =
    issued && baseline && devices ? (devices.find((d) => d.revoked_at === 0 && !baseline.has(d.id)) ?? null) : null;
  const waiting = issued !== null && !expired && joined === null;

  useEffect(() => {
    headingRef.current?.focus({ preventScroll: true });
  }, []);

  useEffect(() => () => clearTimeout(copyTimer.current), []);

  // Wall clock for the expiry countdown; it only runs while an unexpired code exists.
  useEffect(() => {
    if (!issued || issued.expiresAt <= Date.now()) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), CLOCK_INTERVAL_MS);
    return () => clearInterval(timer);
  }, [issued]);

  // Poll the device list while waiting; hidden tabs skip the request and refresh once when they become visible again.
  useEffect(() => {
    if (!waiting) return;
    const tick = () => {
      if (document.visibilityState === "visible") onPoll();
    };
    const timer = setInterval(tick, POLL_INTERVAL_MS);
    document.addEventListener("visibilitychange", tick);
    return () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", tick);
    };
  }, [waiting, onPoll]);

  const copy = (key: string, text: string) => {
    void copyToClipboard(text)
      .then(() => {
        setCopyFailed(false);
        setCopied(key);
        clearTimeout(copyTimer.current);
        copyTimer.current = setTimeout(() => setCopied(null), COPIED_FEEDBACK_MS);
      })
      .catch(() => {
        setCopied(null);
        setCopyFailed(true);
      });
  };

  const codeBox = (command: string) => (
    <code
      className="nx-code block max-w-full overflow-x-auto whitespace-pre px-2 py-1.5 font-mono text-[11.5px]"
      tabIndex={0}
      aria-label="安装命令"
    >
      {command}
    </code>
  );

  const smallRow = (row: CommandRow) => (
    <div key={row.key} className="flex flex-col gap-1">
      <div className="text-[11.5px] text-neutral-400">{row.label}</div>
      <div className="flex min-w-0 items-start gap-2">
        <code
          className="nx-code min-w-0 flex-1 overflow-x-auto whitespace-pre px-2 py-1 font-mono text-[11.5px]"
          tabIndex={0}
        >
          {row.command}
        </code>
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
          aria-label={`复制${row.what}`}
          onClick={() => copy(row.key, row.command)}
        >
          <IconCopy size={11} />
          {copied === row.key ? "已复制" : "复制"}
        </button>
      </div>
    </div>
  );

  const entryRows = (entry: FleetBaseURLEntry, code: string): CommandRow[] => {
    if (!serverVersion) return [];
    const insecure = Boolean(entry.insecure);
    return [
      {
        key: `unix:${entry.url}`,
        label: `${entry.url} - macOS / Linux`,
        what: `${entry.url} 的 macOS / Linux 安装命令`,
        command: oneLineInstallCommand(entry.url, insecure, code, serverVersion),
      },
      {
        key: `win:${entry.url}`,
        label: `${entry.url} - Windows`,
        what: `${entry.url} 的 Windows 安装命令`,
        command: oneLineInstallCommandWindows(entry.url, insecure, code, serverVersion),
      },
    ];
  };

  let body: ReactNode;
  if (baseUrlsStatus === "loading") {
    body = <p className="nx-hint">正在读取接入地址…</p>;
  } else if (baseUrlsStatus === "error") {
    body = <p className="nx-hint text-amber-300">无法读取接入地址，暂时不能生成接入码。请在页面下方的接入地址区域重试。</p>;
  } else if (baseUrls.length === 0) {
    body = (
      <div className="flex flex-wrap items-center gap-2 text-[12px] text-amber-300">
        {isAdmin ? (
          <>
            <span>尚未配置接入地址，设备无法接入。请先配置接入地址，再生成接入码。</span>
            <button type="button" className="nx-btn nx-btn-outline nx-btn-sm shrink-0" onClick={onJumpBaseUrls}>
              配置接入地址
            </button>
          </>
        ) : (
          <span>管理员尚未配置接入地址，设备无法接入。请联系管理员配置后再接入。</span>
        )}
      </div>
    );
  } else if (!issued) {
    body = issueError ? (
      <div className="nx-alert nx-alert-danger flex flex-wrap items-center gap-2" role="alert">
        <IconXCircle size={13} className="shrink-0" />
        <span className="min-w-0 flex-1 break-words">接入码生成失败：{issueError}。请检查网络后重试。</span>
        <button type="button" className="nx-btn nx-btn-outline nx-btn-sm shrink-0" disabled={issuing} onClick={onIssue}>
          重试
        </button>
      </div>
    ) : (
      <p className="nx-hint" role="status">
        正在生成接入码…
      </p>
    );
  } else if (joined) {
    const online = isOnline(joined);
    const canOpen = online && joined.agent?.terminal_enabled !== false;
    const note = outdatedNote(joined);
    body = (
      <div className="flex flex-col gap-2">
        <div className="flex items-center gap-2 text-[13px] text-green-300" role="status" aria-live="polite">
          <IconCheckCircle size={14} className="shrink-0" />
          <span className="min-w-0 break-all">已接入：{joined.name}</span>
        </div>
        {!canOpen && (
          <p className="nx-hint">{online ? "该设备已关闭远程终端，开启后才能打开工作区。" : "设备正在启动，上线后即可打开工作区。"}</p>
        )}
        {issueError && (
          <p className="text-[12px] text-red-300" role="alert">
            接入码生成失败：{issueError}。请重试。
          </p>
        )}
        {note && (
          <p className="nx-hint" role="note">
            {note}
          </p>
        )}
        <div className="flex flex-wrap items-center gap-2">
          <button type="button" className="nx-btn nx-btn-primary nx-btn-sm" disabled={!canOpen} onClick={() => onOpenWorkspace(joined)}>
            <IconTerminal size={12} />
            打开工作区
          </button>
          <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" disabled={issuing} onClick={onIssue}>
            <IconPlus size={12} />
            再接入一台
          </button>
        </div>
      </div>
    );
  } else if (expired) {
    body = (
      <div className="flex flex-col gap-2">
        <div className="flex items-center gap-2 text-[13px] text-amber-300" role="status" aria-live="polite">
          <IconXCircle size={14} className="shrink-0" />
          <span>接入码已失效</span>
        </div>
        <p className="nx-hint">接入码超过有效期后无法再使用。请重新生成，并使用新的命令。</p>
        {issueError && (
          <p className="text-[12px] text-red-300" role="alert">
            接入码生成失败：{issueError}。请重试。
          </p>
        )}
        <div>
          <button type="button" className="nx-btn nx-btn-primary nx-btn-sm" disabled={issuing} onClick={onIssue}>
            <IconRefresh size={12} className={issuing ? "animate-spin" : ""} />
            {issuing ? "正在生成…" : "重新生成"}
          </button>
        </div>
      </div>
    );
  } else {
    const main = entryRows(baseUrls[0], issued.code);
    const active = platform === "windows" ? main[1] : main[0];
    const manual = manualRows(issued.code);
    const others = baseUrls.slice(1).flatMap((entry) => entryRows(entry, issued.code));
    body = (
      <div className="flex flex-col gap-3">
        {active ? (
          <div className="flex flex-col gap-2">
            <div role="group" aria-label="目标设备系统" className="flex flex-wrap items-center gap-2">
              {(
                [
                  ["unix", "macOS / Linux"],
                  ["windows", "Windows"],
                ] as const
              ).map(([value, label]) => (
                <button
                  key={value}
                  type="button"
                  aria-pressed={platform === value}
                  className={`nx-btn nx-btn-sm ${platform === value ? "nx-btn-primary" : "nx-btn-outline"}`}
                  onClick={() => setPlatform(value)}
                >
                  {label}
                </button>
              ))}
            </div>
            {codeBox(active.command)}
            <div className="flex flex-wrap items-center gap-2">
              <button
                type="button"
                className="nx-btn nx-btn-outline nx-btn-sm"
                aria-label={copied === "main" ? "已复制安装命令" : "复制命令"}
                onClick={() => copy("main", active.command)}
              >
                <IconCopy size={12} />
                {copied === "main" ? "已复制" : "复制命令"}
              </button>
              <span className="text-[12px] text-neutral-300">
                {platform === "windows"
                  ? "在目标设备的 PowerShell 中运行，无需管理员权限。"
                  : "在目标设备上用普通用户运行，无需管理员权限；使用 root 将被拒绝。"}
              </span>
            </div>
          </div>
        ) : (
          <p className="nx-hint text-amber-300">
            无法读取服务器版本，暂不能提供一键命令。请展开「手动接入」，在已安装 nexterm-agent 的设备上执行命令。
          </p>
        )}
        <p className="nx-hint text-[11.5px]">接入码仅可使用一次，设备凭证只保存在设备上。</p>
        <div className="flex flex-col gap-1">
          <div className="flex flex-wrap items-center gap-2" role="status" aria-live="polite">
            <IconRefresh size={12} className="shrink-0 animate-spin text-neutral-400" aria-hidden="true" />
            <span className="text-[13px] text-neutral-100">等待设备接入…</span>
          </div>
          <div className="text-[11.5px] text-neutral-400">{expiryText(issued.expiresAt, now)}</div>
        </div>
        <details
          className="rounded border border-neutral-800/70 px-2 py-1.5"
          open={!active || undefined}
          onToggle={(e) => {
            setManualOpen(e.currentTarget.open);
            onLayoutChange();
          }}
        >
          <summary className="cursor-pointer text-[12px] text-neutral-300">手动接入</summary>
          {(manualOpen || !active) && (
          <div className="mt-2 flex flex-col gap-2">
            <div className="text-[11.5px] text-neutral-400">接入码</div>
            <div className="flex min-w-0 items-start gap-2">
              <code className="nx-code min-w-0 flex-1 overflow-x-auto whitespace-pre px-2 py-1 font-mono text-[12px]" tabIndex={0}>
                {issued.code}
              </code>
              <button
                type="button"
                className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
                aria-label="复制接入码"
                onClick={() => copy("code", issued.code)}
              >
                <IconCopy size={11} />
                {copied === "code" ? "已复制" : "复制"}
              </button>
            </div>
            <div className="text-[11.5px] text-neutral-400">服务器地址</div>
            <code className="nx-code block max-w-full overflow-x-auto whitespace-pre px-2 py-1 font-mono text-[12px]" tabIndex={0}>
              {baseUrls[0].url}
            </code>
            <div className="text-[11.5px] text-neutral-400">设备已安装 nexterm-agent 时，依次执行以下两条命令：</div>
            {[...manual.enroll.slice(0, 1), manual.install].map(smallRow)}
          </div>
          )}
        </details>
        <details className="rounded border border-neutral-800/70 px-2 py-1.5" onToggle={onLayoutChange}>
          <summary className="cursor-pointer text-[12px] text-neutral-300">更多选项</summary>
          <div className="mt-2 flex flex-col gap-2">
            <div className="flex flex-wrap items-center gap-2 text-[11.5px] text-neutral-400">
              <span>新接入码有效期</span>
              <Select
                className="w-[120px] shrink-0"
                value={String(ttlMs)}
                ariaLabel="接入码有效期"
                onChange={(value) => onTtlChange(Number(value))}
                options={ENROLL_TTL_OPTIONS.map((o) => ({ value: String(o.ms), label: o.label }))}
              />
              <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" disabled={issuing} onClick={onIssue}>
                重新生成
              </button>
            </div>
            {others.length > 0 && (
              <>
                <div className="text-[11.5px] text-neutral-400">备用接入地址按配置顺序排列，第一个可连通的地址即为接入点：</div>
                {others.map(smallRow)}
              </>
            )}
            {manual.enroll.length > 1 && (
              <>
                <div className="text-[11.5px] text-neutral-400">备用地址的手动接入命令：</div>
                {manual.enroll.slice(1).map(smallRow)}
              </>
            )}
            <div className="text-[11.5px] text-neutral-400">
              安装命令会注册随用户登录自动启动的后台服务（Linux 使用 systemd --user，macOS 使用 launchd，Windows 使用计划任务）。数据目录为可选项，默认 ~/.local/share/NexTerm（Windows 为 %LOCALAPPDATA%\NexTerm），可通过环境变量 NEXTERM_DATA_DIR 或 --data-dir 指定。
            </div>
          </div>
        </details>
      </div>
    );
  }

  return (
    <section
      className="nx-card"
      aria-labelledby="enroll-panel-title"
      onKeyDown={(e) => {
        if (e.key === "Escape") onClose();
      }}
    >
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <IconPlus size={15} className="text-neutral-400" />
        <h2 id="enroll-panel-title" ref={headingRef} tabIndex={-1} className="nx-card-title m-0 outline-none">
          接入新设备
        </h2>
        <div className="nx-spacer" />
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={onClose}>
          关闭
        </button>
      </div>
      {body}
      {copyFailed && (
        <p className="mt-2 text-[12px] text-red-300" role="alert">
          复制失败。请选中命令后手动复制。
        </p>
      )}
    </section>
  );
}
