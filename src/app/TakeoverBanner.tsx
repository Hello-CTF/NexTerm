import { useEffect, useState } from "react";
import { consumeRestoredTakeover, useUi, type TakeoverState } from "./store";
import { formatBinding, formatBindingAria, getKeybinding, matchKeybinding, useKeybindings } from "./keybindings";
import { aiApi, type TakeoverStatus } from "../ipc/commands";
import { IconAlert, IconGamepad, IconPlay, IconShield } from "../ui/icons";
import { describeError, describeErrorSource } from "../ui/errorText";
import { hasActiveOverlay, isImeKeyEvent } from "../ui/DialogHost";
import { t as tr } from "../i18n";

export type TakeoverViewState = TakeoverState;

export function takeoverPaused(takeover: TakeoverState): boolean {
  return takeover.paused === true;
}

/** Paused (or not) as the backend reported it; any pending pause/resume request is over. */
export function withTakeoverPaused(takeover: TakeoverState, paused: boolean): TakeoverState {
  return { ...takeover, paused, control: undefined };
}

function withControl(takeover: TakeoverState, control: TakeoverState["control"]): TakeoverState {
  return { ...takeover, control };
}

function sameTakeover(a: TakeoverState | null, b: TakeoverState): a is TakeoverState {
  return a !== null && a.tabId === b.tabId && a.jobId === b.jobId && a.startedAt === b.startedAt;
}

/** Applies update to the banner only while it still shows the same takeover. */
function updateSame(takeover: TakeoverState, update: (current: TakeoverState) => TakeoverState | null) {
  const current = useUi.getState().takeover;
  if (sameTakeover(current, takeover)) useUi.getState().setTakeover(update(current));
}

const resumeHandlers = new Map<string, () => Promise<void>>();

export function registerTakeoverResumeHandler(jobId: string, handler: () => Promise<void>): () => void {
  resumeHandlers.set(jobId, handler);
  return () => {
    if (resumeHandlers.get(jobId) === handler) resumeHandlers.delete(jobId);
  };
}

function alreadyEnded(message: string): boolean {
  return message.includes("接管令牌已过期") || message.includes("接管任务不存在或已结束");
}

function endedOutside(takeover: TakeoverState) {
  updateSame(takeover, () => null);
  useUi.getState().pushToast("info", tr("接管已结束"));
}

// pauseTakeover asks the AI to stop at its next safe point. The banner shows
// "pausing" until the run reports it paused; the run's own paused event (or
// the pause reply) flips it to paused.
export async function pauseTakeover() {
  const { takeover, pushToast } = useUi.getState();
  if (!takeover || takeoverPaused(takeover) || takeover.control) return;
  updateSame(takeover, (current) => withControl(current, "pausing"));
  try {
    const phase = (await aiApi.takeoverPause(takeover.tabId, takeover.token))?.phase;
    if (phase === "pausing") return;
    if (phase === "paused") {
      updateSame(takeover, (current) => withTakeoverPaused(current, true));
      return;
    }
    updateSame(takeover, (current) => (current.control === "pausing" ? withControl(current, undefined) : current));
    if (phase === "confirm") pushToast("info", tr("AI 正在等待你确认操作，当前没有可暂停的步骤"));
  } catch (e) {
    if (alreadyEnded(describeErrorSource(e))) {
      endedOutside(takeover);
      return;
    }
    updateSame(takeover, (current) => (current.control === "pausing" ? withControl(current, undefined) : current));
    pushToast("error", tr("暂停接管失败：{value}", { value: describeError(e) }));
  }
}

export async function resumeTakeover() {
  const { takeover, pushToast } = useUi.getState();
  if (!takeover || !takeoverPaused(takeover) || takeover.control) return;
  updateSame(takeover, (current) => withControl(current, "resuming"));
  try {
    if (!takeover.jobId) throw new Error(tr("接管任务 ID 缺失"));
    const handler = resumeHandlers.get(takeover.jobId);
    if (!handler) throw new Error(tr("接管会话已断开，请打开对应 AI 会话后重试"));
    await handler();
    updateSame(takeover, (current) => withTakeoverPaused(current, false));
  } catch (e) {
    if (alreadyEnded(describeErrorSource(e))) {
      endedOutside(takeover);
      return;
    }
    updateSame(takeover, (current) => (current.control === "resuming" ? withControl(current, undefined) : current));
    pushToast("error", tr("恢复接管失败：{value}", { value: describeError(e) }));
  }
}

function persistedTakeoverPaused(restored: TakeoverState): boolean {
  try {
    const raw = localStorage.getItem("nexterm.takeover.v1");
    if (!raw) return false;
    const value = JSON.parse(raw) as { tabId?: unknown; jobId?: unknown; paused?: unknown };
    return value.paused === true && value.tabId === restored.tabId && value.jobId === restored.jobId;
  } catch {
    return false;
  }
}

// stealBack ends the takeover: the backend cancels the AI with this reason,
// which the run reports once on its done event. Nothing is written to the terminal.
export async function stealBack(reason = tr("用户收回控制权")) {
  const { takeover, pushToast } = useUi.getState();
  if (!takeover || takeover.control === "reclaiming") return;
  updateSame(takeover, (current) => withControl(current, "reclaiming"));
  try {
    await aiApi.takeoverExit(takeover.tabId, takeover.token, reason);
    updateSame(takeover, () => null);
    pushToast("info", tr("已收回终端控制权，AI 已停止"));
  } catch (e) {
    if (alreadyEnded(describeErrorSource(e))) {
      // The ownership is gone; stop a job that might still outlive it.
      if (takeover.jobId) await aiApi.takeoverCancel(takeover.jobId).catch(() => undefined);
      updateSame(takeover, () => null);
      pushToast("info", tr("接管已结束，终端控制权已恢复。"));
      return;
    }
    updateSame(takeover, (current) => (current.control === "reclaiming" ? withControl(current, undefined) : current));
    pushToast("error", tr("收回控制权失败，AI 可能仍在操作终端：{message}", { message: describeError(e) }));
  }
}

/**
 * Asks the backend whether a takeover restored after a reload still owns its terminal. Persistent failures
 * keep the banner (null) rather than hiding a takeover that may still be typing.
 */
export async function restoredTakeoverStatus(
  restored: TakeoverState,
  status: (tabId: string, token: string) => Promise<TakeoverStatus>,
): Promise<TakeoverStatus | null> {
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      return await status(restored.tabId, restored.token);
    } catch {
      await new Promise((resolve) => window.setTimeout(resolve, 800));
    }
  }
  return null;
}

export function TakeoverBanner() {
  const takeover = useUi((s) => s.takeover);
  const bindings = useKeybindings();
  const reclaimLabel = formatBinding(bindings.reclaimTakeover);
  const [now, setNow] = useState(() => Date.now());
  const active = takeover !== null;

  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, [active]);

  useEffect(() => {
    if (!active) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.repeat || isImeKeyEvent(event) || hasActiveOverlay()) return;
      if (!matchKeybinding(event, "reclaimTakeover")) return;
      event.preventDefault();
      event.stopPropagation();
      void stealBack(tr("用户按 {value} 收回控制权", { value: formatBinding(getKeybinding("reclaimTakeover")) }));
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [active]);

  useEffect(() => {
    let restored = consumeRestoredTakeover();
    if (!restored) return;
    if (persistedTakeoverPaused(restored)) {
      restored = withTakeoverPaused(restored, true);
      useUi.getState().setTakeover(restored);
    }
    const probed = restored;
    let disposed = false;
    void restoredTakeoverStatus(probed, (tabId, token) => aiApi.takeoverStatus(tabId, token)).then((status) => {
      if (disposed || !status) return;
      const current = useUi.getState().takeover;
      if (!sameTakeover(current, probed)) return;
      if (!status.active) {
        endedOutside(probed);
        return;
      }
      const paused = status.phase === "paused" || status.phase === "pausing";
      if (paused !== takeoverPaused(current)) useUi.getState().setTakeover(withTakeoverPaused(current, paused));
    });
    return () => {
      disposed = true;
    };
  }, []);

  if (!takeover) return null;

  const elapsed = Math.max(0, Math.floor((now - takeover.startedAt) / 1000));
  const mm = String(Math.floor(elapsed / 60)).padStart(2, "0");
  const ss = String(elapsed % 60).padStart(2, "0");
  const mode = takeover.allowWrite ? tr("可写") : tr("只读");
  const paused = takeoverPaused(takeover);
  const control = takeover.control;
  const status =
    control === "reclaiming"
      ? tr("正在收回控制权…")
      : control === "pausing"
        ? tr("正在暂停：AI 完成当前步骤后停下")
        : paused
          ? tr("AI 接管已暂停")
          : tr("AI 正在操作此终端");
  const toggleLabel = control === "pausing" ? tr("暂停中…") : control === "resuming" ? tr("继续中…") : paused ? tr("继续") : tr("暂停");
  const reclaimHint = bindings.reclaimTakeover ? tr("按 {reclaimLabel} 可收回控制权。", { reclaimLabel }) : tr("可收回控制权。");

  return (
    <div
      className="nx-takeover-banner flex h-[34px] shrink-0 items-center gap-2.5 border-b border-red-500/40 bg-red-950 px-3 text-xs text-red-100"
      role="region"
      aria-label={tr("AI 终端接管状态")}
      data-phase={control ?? (paused ? "paused" : "running")}
    >
      <span className="nx-sr-only" role="status" aria-atomic="true">
        {tr("{status}，{mode}。", { status, mode })}
        {reclaimHint}
      </span>
      <span className="flex shrink-0 items-center gap-1.5 font-semibold" aria-hidden="true">
        <IconAlert size={14} className="text-red-300" />
        {status}
      </span>
      <span className={`nx-badge ${takeover.allowWrite ? "nx-badge-red" : ""}`}>
        <IconShield size={10} />
        {mode}
      </span>
      <span className="min-w-0 flex-1 truncate opacity-80" title={takeover.task}>
        {takeover.task}
      </span>
      <span
        className="shrink-0 font-mono tabular-nums opacity-80"
        role="timer"
        title={tr("自接管开始的用时")}
        aria-label={tr("已用时 {mm} 分 {ss} 秒", { mm, ss })}
      >
        {mm}:{ss}
      </span>
      <button
        type="button"
        className="nx-btn nx-btn-outline nx-btn-sm shrink-0"
        disabled={control !== undefined}
        title={paused ? tr("从暂停处继续同一接管任务") : tr("暂停 AI 接管，之后可继续；在终端内输入也会暂停")}
        onClick={() => void (paused ? resumeTakeover() : pauseTakeover())}
      >
        {paused && <IconPlay size={12} />}
        {toggleLabel}
      </button>
      <button
        type="button"
        className="nx-btn nx-btn-danger-solid nx-btn-sm shrink-0"
        disabled={control === "reclaiming"}
        aria-keyshortcuts={formatBindingAria(bindings.reclaimTakeover) ?? undefined}
        title={tr("收回控制权将停止 AI 并退出接管；在终端内按任意键仅暂停接管，之后可恢复。")}
        onClick={() => void stealBack()}
      >
        <IconGamepad size={12} />
        {tr("收回控制权")}
        {bindings.reclaimTakeover && (
          <span className="nx-kbd ml-1 border-white/30 bg-white/10 text-current" aria-hidden="true">
            {reclaimLabel}
          </span>
        )}
      </button>
    </div>
  );
}
