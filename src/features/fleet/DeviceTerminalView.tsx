// 设备远程终端视图 (FLEET156): 经 /fleet/devices/{id}/bridge 出站桥接与设备端
// supervisor 对话 (真实 v2 二进制合同, 见 src/ipc/deviceTerminalApi.ts)。
// 安全边界: 输入只转发终端按键字节, create 不带命令 (设备默认 shell), 绝不发送
// 宿主密码或私钥; state digest 只在内存中用于 hello, 不展示不持久化。
// 关闭标签 = detach (设备端会话保留), 「结束终端」是唯一销毁路径且需显式确认。

import { useCallback, useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebglAddon } from "@xterm/addon-webgl";
import "@xterm/xterm/css/xterm.css";

import { fleetApi, type FleetDevice } from "../../ipc/fleetApi";
import {
  DeviceTerminalError,
  killDeviceTerminal,
  openDeviceTerminal,
  resumeDeviceTerminal,
  type DeviceBridge,
  type SupervisorSessionIdentity,
} from "../../ipc/deviceTerminalApi";
import type { SharePermission } from "../../ipc/sharingApi";
import { createShareLink, sharePublicUrl } from "./shareLinkCreate";
import { formatTime } from "./format";
import { getAppearancePrefs, getResolvedTerminalTheme, subscribeAppearancePrefs } from "../../app/preferences";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { IconGlobe, IconPlay, IconRefresh, IconStop, IconTerminal } from "../../ui/icons";

const TERM_THEME = {
  dark: {
    background: "#101217",
    foreground: "#c6cbd6",
    cursorAccent: "#101217",
    selectionBackground: "#2c3e5d",
    scrollbarSliderBackground: "#31363f",
    scrollbarSliderHoverBackground: "#3e444f",
    scrollbarSliderActiveBackground: "#4d5563",
    black: "#101217",
    brightBlack: "#5c6472",
    red: "#e87b7b",
    brightRed: "#f0a0a0",
    green: "#7fd6a4",
    brightGreen: "#a3e6bd",
    yellow: "#e9c489",
    brightYellow: "#f0d6a4",
    blue: "#79a8f8",
    brightBlue: "#9dc0fb",
    magenta: "#b79ce8",
    brightMagenta: "#cdb9f0",
    cyan: "#7fc7d6",
    brightCyan: "#a3dbe6",
    white: "#c6cbd6",
    brightWhite: "#eef1f6",
  },
  light: {
    background: "#f4f6fa",
    foreground: "#2f3642",
    cursorAccent: "#f4f6fa",
    selectionBackground: "#c9d6f2",
    scrollbarSliderBackground: "#ccd3de",
    scrollbarSliderHoverBackground: "#aeb7c5",
    scrollbarSliderActiveBackground: "#8a94a6",
    black: "#2f3642",
    brightBlack: "#5d6572",
    red: "#c23636",
    brightRed: "#a12222",
    green: "#1a6b41",
    brightGreen: "#226e46",
    yellow: "#8a5a00",
    brightYellow: "#96660a",
    blue: "#3c59cc",
    brightBlue: "#324a9e",
    magenta: "#6440b8",
    brightMagenta: "#4c2f96",
    cyan: "#0e5a9e",
    brightCyan: "#0b4a80",
    white: "#5d6572",
    brightWhite: "#10141b",
  },
};

// 公开链接有效期选项 (FLEET168): 全部落在服务端 minShareTTL(1 分钟)-
// maxShareTTL(30 天) 边界内; 默认 1 小时与服务端 defaultLinkTTL 对齐,
// 创建时总是显式带 ttl_ms。默认只读, 勾选「允许读写」才发 read_write。
const SHARE_LINK_TTL_OPTIONS = [
  { label: "15 分钟", ms: 15 * 60_000 },
  { label: "1 小时", ms: 60 * 60_000 },
  { label: "6 小时", ms: 6 * 60 * 60_000 },
  { label: "24 小时", ms: 24 * 60 * 60_000 },
  { label: "7 天", ms: 7 * 24 * 60 * 60_000 },
];

type Phase =
  | { kind: "loading" }
  | { kind: "connecting" }
  | { kind: "ready" }
  | { kind: "exited"; detail: string }
  | { kind: "disconnected"; message: string }
  | { kind: "failed"; message: string; canRestart: boolean };

interface SessionRef {
  id: string;
  identity: SupervisorSessionIdentity;
}

type DeviceGate =
  | { ok: true; row: FleetDevice; digest: string }
  | { ok: false; phase: Phase };

// gateDevice 是 open 与 resume 共用的设备状态复核: 吊销/策略关闭/摘要缺失
// (离线) 都给出显式状态, 不落入 generic 断线。
function gateDevice(row: FleetDevice | null): DeviceGate {
  if (!row || !row.agent) {
    return { ok: false, phase: { kind: "failed", message: "设备不存在或无权访问", canRestart: false } };
  }
  if (row.revoked_at !== 0) {
    return { ok: false, phase: { kind: "failed", message: "设备已吊销, 无法打开终端", canRestart: false } };
  }
  if (!row.agent.terminal_enabled) {
    return { ok: false, phase: { kind: "failed", message: "设备已关闭终端访问", canRestart: false } };
  }
  const digest = row.agent.state_digest;
  if (!digest) {
    return { ok: false, phase: { kind: "failed", message: "设备代理未上报终端状态摘要 (设备离线?)", canRestart: true } };
  }
  return { ok: true, row, digest };
}

function describeSessionError(error: unknown): { message: string; canRestart: boolean } {
  if (error instanceof DeviceTerminalError) {
    switch (error.code) {
      case "version_mismatch":
        return { message: "设备端终端协议版本不兼容, 请升级设备上的 NexTerm", canRestart: false };
      case "state_mismatch":
        return { message: "设备端终端状态已变化, 请重试", canRestart: true };
      case "not_found":
        return { message: "会话在设备上已不存在", canRestart: true };
      case "identity":
        return { message: "会话已被替换, 无法恢复", canRestart: true };
      case "exited":
        return { message: "会话已结束", canRestart: true };
      default:
        return { message: describeError(error), canRestart: true };
    }
  }
  return { message: describeError(error), canRestart: true };
}

export function DeviceTerminalView({ deviceId, visible }: { deviceId: string; visible?: boolean }) {
  const [device, setDevice] = useState<FleetDevice | null>(null);
  const [phase, setPhase] = useState<Phase>({ kind: "loading" });
  const hostRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const bridgeRef = useRef<DeviceBridge | null>(null);
  const sessionRef = useRef<SessionRef | null>(null);
  const deviceRef = useRef<FleetDevice | null>(null);
  // generationRef 是 open/resume 的代次: 新一次 open/resume 使旧代次的 wire/
  // 完成回调全部失效 (StrictMode 孤儿 open/并发重开), 配合 wire 里的 detach
  // 与 openDeviceTerminal 自带的回收, 设备端不留不可管理的会话。
  const generationRef = useRef(0);
  // unmountedRef 在组件真正卸载后保持 true, 供 resume/restart 的在途完成回调
  // 丢弃桥接 (按钮只在挂载期可点, 但 await 可能跨过卸载)。
  const unmountedRef = useRef(false);
  // openCancelRef 是在途 open/resume 的取消函数 (openDeviceTerminal /
  // resumeDeviceTerminal 经 onCancel 登记, settled 后自动注销): 卸载/账号
  // 边界关闭标签时取消在途连接, 回收走 creator 旧连接, 不依赖账号切换后的新 WS。
  const openCancelRef = useRef<(() => void) | null>(null);
  // shareEpochRef 是公开链接创建的代次: 卸载/账号边界关闭标签/会话离开 ready
  // 时递增, 在途创建的晚到完成一律丢弃 (token 不落地, 也不写已卸载视图)。
  const shareEpochRef = useRef(0);
  const [shareOpen, setShareOpen] = useState(false);
  const [shareTtlMs, setShareTtlMs] = useState(SHARE_LINK_TTL_OPTIONS[1].ms);
  const [shareWrite, setShareWrite] = useState(false);
  const [shareCreating, setShareCreating] = useState(false);
  const [shareError, setShareError] = useState<string | null>(null);
  // shareCreated 只在创建成功后当场持有公开 URL (含一次性 token); 面板关闭、
  // 会话离开 ready 或视图卸载即清除, 不写日志、不持久化、不进 web storage。
  const [shareCreated, setShareCreated] = useState<{ url: string; permission: SharePermission; expiresAt: number } | null>(null);
  const phaseRef = useRef<Phase>(phase);
  phaseRef.current = phase;
  deviceRef.current = device;
  const { pushToast } = useUi();

  const wireBridge = useCallback(
    (bridge: DeviceBridge) => {
      // 替换旧桥接时必定先 detach (自然 exit/失败重开路径下旧桥可能仍半开)
      if (bridgeRef.current && bridgeRef.current !== bridge) bridgeRef.current.detach();
      bridge.onOutput = (data) => termRef.current?.write(data);
      bridge.onExit = (exit) => {
        const detail =
          exit.signal !== "" ? `信号 ${exit.signal}` : exit.code !== null ? `退出码 ${exit.code}` : "已结束";
        setPhase({ kind: "exited", detail });
      };
      bridge.onClose = (error) => {
        // 旧桥接的迟到关闭不得清掉新桥接的引用
        if (bridgeRef.current === bridge) bridgeRef.current = null;
        if (error && phaseRef.current.kind === "ready") {
          setPhase({ kind: "disconnected", message: error.message });
        }
      };
      bridgeRef.current = bridge;
    },
    [],
  );

  const loadDevice = useCallback(async (): Promise<FleetDevice | null> => {
    const list = await fleetApi.devices();
    return list.devices.find((d) => d.id === deviceId) ?? null;
  }, [deviceId]);

  const open = useCallback(
    async (term: Terminal, fit: FitAddon, isDisposed?: () => boolean) => {
      const generation = ++generationRef.current;
      const stale = () => generation !== generationRef.current || Boolean(isDisposed?.());
      setPhase({ kind: "connecting" });
      let row: FleetDevice | null;
      try {
        row = await loadDevice();
      } catch (e) {
        if (stale()) return;
        setPhase({ kind: "failed", message: `加载设备信息失败: ${describeError(e)}`, canRestart: true });
        return;
      }
      if (row) setDevice(row);
      const gate = gateDevice(row);
      if (!gate.ok) {
        if (!stale()) setPhase(gate.phase);
        return;
      }
      if (stale()) return;
      try {
        fit.fit();
      } catch {
      }
      const cols = Math.max(2, term.cols);
      const rows = Math.max(1, term.rows);
      // wire 在 attach 前调用; 过期代次直接 detach, attach 随之中断,
      // openDeviceTerminal 的回收逻辑会 killSession, 设备端不留孤儿 shell。
      const wire = (bridge: DeviceBridge) => {
        if (stale()) {
          bridge.detach();
          return;
        }
        wireBridge(bridge);
      };
      try {
        const { info, identity, bridge } = await openDeviceTerminal({
          deviceId: gate.row.id,
          stateDigest: gate.digest,
          cols,
          rows,
          wire,
          onCancel: (cancel) => {
            openCancelRef.current = cancel;
          },
        });
        if (stale()) {
          // 已 attach 但未及交付 UI (代次被并发重开取代): 用这条已鉴权旧桥
          // kill 会话, 不新建依赖新账号/失效凭证的 WS。
          void bridge.kill().catch(() => undefined);
          bridge.detach();
          return;
        }
        sessionRef.current = { id: info.id, identity };
        setPhase({ kind: "ready" });
      } catch (e) {
        if (stale()) return;
        const { message, canRestart } = describeSessionError(e);
        setPhase({ kind: "failed", message, canRestart });
      }
    },
    [loadDevice, wireBridge],
  );

  const resume = useCallback(async () => {
    const session = sessionRef.current;
    if (!session) return;
    const generation = ++generationRef.current;
    const stale = () => generation !== generationRef.current || unmountedRef.current;
    setPhase({ kind: "connecting" });
    let digest: string | undefined;
    let gate: DeviceGate | null = null;
    try {
      const row = await loadDevice();
      if (row) setDevice(row);
      gate = gateDevice(row);
      if (gate.ok) digest = gate.digest;
    } catch {
      // fetch 失败才按明确降级策略使用旧摘要, hello 失败会给出显式状态
      digest = deviceRef.current?.agent?.state_digest;
    }
    if (gate && !gate.ok) {
      if (!stale()) setPhase(gate.phase);
      return;
    }
    if (!digest) {
      if (!stale()) setPhase({ kind: "failed", message: "设备代理未上报终端状态摘要 (设备离线?)", canRestart: true });
      return;
    }
    // 设备信息 await 后、发起连接前复核: 卸载/账号切换后不再新起连接
    if (stale()) return;
    // backlog 从 seq 0 全量重放, 先清屏再写回, 断开期间的内容不丢;
    // 清屏必须发生在 attach 之前 (attach 一批准对端立即开始重放)。
    termRef.current?.reset();
    const wire = (bridge: DeviceBridge) => {
      if (stale()) {
        bridge.detach();
        return;
      }
      wireBridge(bridge);
    };
    try {
      const { bridge } = await resumeDeviceTerminal({
        deviceId,
        stateDigest: digest,
        sessionId: session.id,
        identity: session.identity,
        wire,
        onCancel: (cancel) => {
          openCancelRef.current = cancel;
        },
      });
      if (stale()) {
        bridge.detach();
        return;
      }
      setPhase({ kind: "ready" });
    } catch (e) {
      if (stale()) return;
      const { message, canRestart } = describeSessionError(e);
      setPhase({ kind: "failed", message, canRestart });
    }
  }, [deviceId, loadDevice, wireBridge]);

  const kill = useCallback(async () => {
    const session = sessionRef.current;
    const row = deviceRef.current;
    if (!session || !row?.agent?.state_digest) return;
    const name = row.name;
    const ok = await ask(`结束设备「${name}」上的这个终端会话?\n\n设备端的 shell 进程会被终止, 未保存的工作会丢失。`, {
      title: "结束终端会话",
      kind: "warning",
    });
    if (!ok) return;
    try {
      await killDeviceTerminal({
        deviceId,
        stateDigest: row.agent.state_digest,
        sessionId: session.id,
        identity: session.identity,
      });
      bridgeRef.current?.detach();
      bridgeRef.current = null;
      setPhase({ kind: "exited", detail: "已手动结束" });
    } catch (e) {
      pushToast("error", `结束终端失败: ${describeError(e)}`);
    }
  }, [deviceId, pushToast]);

  const toggleShare = useCallback(() => {
    if (shareOpen) {
      // 关闭即作废在途创建: 递增代次使晚到响应 (成功/失败) 一律失效, 并复位
      // 创建中状态 — 重新打开是全新表单, 旧一次性 URL/token 不会重现。
      shareEpochRef.current += 1;
      setShareOpen(false);
      setShareCreating(false);
      setShareCreated(null);
      setShareError(null);
      return;
    }
    setShareOpen(true);
  }, [shareOpen]);

  const createShare = useCallback(async () => {
    const session = sessionRef.current;
    if (!session || shareCreating) return;
    const epoch = shareEpochRef.current;
    setShareCreating(true);
    setShareError(null);
    try {
      const link = await createShareLink({ deviceId, sessionId: session.id, write: shareWrite, ttlMs: shareTtlMs });
      if (epoch !== shareEpochRef.current || unmountedRef.current) return;
      setShareCreated({ url: sharePublicUrl(link.token), permission: link.permission, expiresAt: link.expires_at });
    } catch (e) {
      if (epoch !== shareEpochRef.current || unmountedRef.current) return;
      setShareError(describeError(e));
    } finally {
      if (epoch === shareEpochRef.current && !unmountedRef.current) setShareCreating(false);
    }
  }, [deviceId, shareCreating, shareTtlMs, shareWrite]);

  const copyShareUrl = useCallback(() => {
    if (!shareCreated) return;
    void navigator.clipboard
      ?.writeText(shareCreated.url)
      .then(() => pushToast("success", "公开链接已复制"))
      .catch(() => pushToast("error", "复制失败, 请手动选中复制"));
  }, [pushToast, shareCreated]);

  // 会话离开 ready (exit/断开/失败) 后分享面板关闭并清除一次性 URL; 在途创建
  // 的晚到完成经 shareEpochRef 失效, 不会把旧会话的链接写回视图。
  useEffect(() => {
    if (phase.kind === "ready") return;
    shareEpochRef.current += 1;
    setShareOpen(false);
    setShareCreating(false);
    setShareError(null);
    setShareCreated(null);
  }, [phase.kind]);

  useEffect(() => {
    const host = hostRef.current;
    if (!host || termRef.current) return;
    let disposed = false;
    unmountedRef.current = false;
    const term = new Terminal({
      scrollback: 100_000,
      fontFamily: "'Cascadia Mono', 'Cascadia Code', Consolas, 'Courier New', monospace",
      fontSize: getAppearancePrefs().terminalFontSize,
      cursorBlink: true,
      theme: TERM_THEME[getResolvedTerminalTheme()],
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    termRef.current = term;
    term.open(host);
    try {
      term.loadAddon(new WebglAddon());
    } catch {
    }
    fitRef.current = fit;

    const dataDisposable = term.onData((data) => {
      const bridge = bridgeRef.current;
      if (!bridge?.isAttached) return;
      void bridge.input(new TextEncoder().encode(data)).catch(() => undefined);
    });
    const ro = new ResizeObserver(() => {
      try {
        fit.fit();
      } catch {
      }
      const bridge = bridgeRef.current;
      if (bridge?.isAttached) {
        void bridge.resize(Math.max(2, term.cols), Math.max(1, term.rows)).catch(() => undefined);
      }
    });
    ro.observe(host);
    const appearanceDisposable = subscribeAppearancePrefs(() => {
      term.options.fontSize = getAppearancePrefs().terminalFontSize;
      term.options.theme = TERM_THEME[getResolvedTerminalTheme()];
    });

    void open(term, fit, () => disposed);

    return () => {
      disposed = true;
      unmountedRef.current = true;
      openCancelRef.current?.();
      openCancelRef.current = null;
      shareEpochRef.current += 1;
      dataDisposable.dispose();
      appearanceDisposable();
      ro.disconnect();
      bridgeRef.current?.detach();
      bridgeRef.current = null;
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
    };
  }, [open]);

  useEffect(() => {
    if (visible === false) return;
    const fit = fitRef.current;
    const term = termRef.current;
    if (!fit || !term) return;
    const raf = requestAnimationFrame(() => {
      try {
        fit.fit();
      } catch {
      }
    });
    return () => cancelAnimationFrame(raf);
  }, [visible]);

  const stateBadge =
    phase.kind === "ready" ? (
      <span className="nx-badge nx-badge-green">已连接</span>
    ) : phase.kind === "connecting" || phase.kind === "loading" ? (
      <span className="nx-badge">连接中</span>
    ) : phase.kind === "exited" ? (
      <span className="nx-badge">已结束</span>
    ) : (
      <span className="nx-badge nx-badge-red">已断开</span>
    );

  const canRestart = phase.kind === "exited" || (phase.kind === "failed" && phase.canRestart);
  const restart = () => {
    const term = termRef.current;
    const fit = fitRef.current;
    if (!term || !fit) return;
    term.reset();
    void open(term, fit, () => unmountedRef.current);
  };

  return (
    <div className="flex h-full min-h-0 flex-col bg-neutral-900">
      <div className="nx-toolbar flex-wrap">
        <IconTerminal size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title min-w-0 break-all">{device?.name ?? "设备终端"}</span>
        {stateBadge}
        <div className="nx-spacer" />
        {phase.kind === "ready" && (
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
            onClick={toggleShare}
            aria-expanded={shareOpen}
          >
            <IconGlobe size={11} />
            分享
          </button>
        )}
        {phase.kind === "ready" && (
          <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={() => void kill()}>
            <IconStop size={11} />
            结束终端
          </button>
        )}
        {phase.kind === "disconnected" && (
          <button type="button" className="nx-btn nx-btn-outline nx-btn-sm shrink-0" onClick={() => void resume()}>
            <IconRefresh size={11} />
            重新连接
          </button>
        )}
        {canRestart && (
          <button type="button" className="nx-btn nx-btn-outline nx-btn-sm shrink-0" onClick={restart}>
            <IconPlay size={11} />
            新开终端
          </button>
        )}
      </div>

      {shareOpen && phase.kind === "ready" && (
        <div className="shrink-0 border-b border-neutral-800/70 bg-neutral-950/60 px-3 py-2">
          {shareCreated ? (
            <div className="flex flex-col gap-1.5">
              <div className="text-[11.5px] text-neutral-300">
                公开链接已创建 ({shareCreated.permission === "read_write" ? "读写" : "只读"}, 有效期至{" "}
                {formatTime(shareCreated.expiresAt)})。链接<b>只显示这一次</b>, 关闭面板后本页面不再展示。
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <code className="nx-code min-w-0 flex-1 break-all font-mono text-[11.5px]">{shareCreated.url}</code>
                <button
                  type="button"
                  className="nx-btn nx-btn-outline nx-btn-xs shrink-0"
                  onClick={copyShareUrl}
                >
                  复制链接
                </button>
                <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs shrink-0" onClick={toggleShare}>
                  完成
                </button>
              </div>
              <div className="text-[11px] leading-relaxed text-neutral-500">
                有效期内任何拿到链接的人无需账号都能打开这个终端会话{shareCreated.permission === "read_write" ? "并输入" : " (不能输入)"}
                ; 可在「设置 → 分享」的公开链接列表中随时吊销。链接不接触主机密码或私钥。
              </div>
            </div>
          ) : (
            <div className="flex flex-col gap-2">
              <div className="flex flex-wrap items-center gap-2">
                <select
                  className="nx-select shrink-0"
                  style={{ width: 110 }}
                  value={shareTtlMs}
                  aria-label="链接有效期"
                  onChange={(e) => setShareTtlMs(Number(e.target.value))}
                >
                  {SHARE_LINK_TTL_OPTIONS.map((o) => (
                    <option key={o.ms} value={o.ms}>
                      {o.label}
                    </option>
                  ))}
                </select>
                <label className="flex items-center gap-1.5 text-[12px] text-neutral-300">
                  <input
                    type="checkbox"
                    className="h-4 w-4"
                    checked={shareWrite}
                    onChange={(e) => setShareWrite(e.target.checked)}
                  />
                  允许读写
                </label>
                <span className="nx-hint text-[11px]">默认只读; 勾选后持有链接的人才可以在终端里输入</span>
                <div className="nx-spacer" />
                <button
                  type="button"
                  className="nx-btn nx-btn-primary nx-btn-sm shrink-0"
                  disabled={shareCreating}
                  onClick={() => void createShare()}
                >
                  {shareCreating ? "创建中…" : "创建公开链接"}
                </button>
              </div>
              {shareError && <div className="text-[11.5px] break-words text-red-300">创建公开链接失败 · {shareError}</div>}
              <div className="text-[11px] leading-relaxed text-neutral-500">
                公开链接绑定当前终端会话: 有效期内任何拿到链接的人无需 NexTerm 账号就能打开这个终端, 请只把链接发给你信任的人。
                链接只能创建一次, 本页面不会保存链接; 创建后可随时在「设置 → 分享」吊销。
              </div>
            </div>
          )}
        </div>
      )}

      <div className="relative min-h-0 flex-1">
        <div ref={hostRef} className="absolute inset-0" />
        {phase.kind === "disconnected" && (
          <div className="absolute inset-0 flex items-center justify-center bg-neutral-950/70 px-4">
            <div className="flex w-[320px] flex-col items-center gap-3 rounded-xl border border-neutral-800/70 bg-neutral-950/85 px-6 py-6 text-center">
              <div className="text-[13px] font-semibold text-neutral-100">连接已断开</div>
              <div className="text-[11.5px] leading-relaxed text-neutral-400">{phase.message}</div>
              <div className="text-[11px] text-neutral-500">设备端会话仍在运行, 重新连接后可恢复全部输出。</div>
              <button type="button" className="nx-btn nx-btn-primary nx-btn-sm" onClick={() => void resume()}>
                <IconRefresh size={12} />
                重新连接
              </button>
            </div>
          </div>
        )}
        {phase.kind === "failed" && (
          <div className="absolute inset-0 flex items-center justify-center bg-neutral-950/70 px-4">
            <div className="flex w-[340px] flex-col items-center gap-3 rounded-xl border border-neutral-800/70 bg-neutral-950/85 px-6 py-6 text-center">
              <div className="text-[13px] font-semibold text-neutral-100">终端不可用</div>
              <div className="text-[11.5px] leading-relaxed text-neutral-400">{phase.message}</div>
              {phase.canRestart && (
                <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" onClick={restart}>
                  <IconPlay size={12} />
                  新开终端
                </button>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
