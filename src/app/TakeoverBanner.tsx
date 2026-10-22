import { useEffect, useState } from "react";
import { consumeRestoredTakeover, useUi, type TakeoverState } from "./store";
import { formatBinding, formatBindingAria, getKeybinding, matchKeybinding, useKeybindings } from "./keybindings";
import { aiApi, terminalApi } from "../ipc/commands";
import { IconAlert, IconGamepad, IconShield } from "../ui/icons";
import { describeError } from "../ui/errorText";
import { hasActiveOverlay, isImeKeyEvent } from "../ui/DialogHost";

export type TakeoverViewState = TakeoverState & { paused?: boolean };

export function takeoverPaused(takeover: TakeoverState): boolean {
  return (takeover as TakeoverViewState).paused === true;
}

export function withTakeoverPaused(takeover: TakeoverState, paused: boolean): TakeoverState {
  return { ...takeover, paused } as TakeoverState;
}

const resumeHandlers = new Map<string, () => Promise<void>>();

export function registerTakeoverResumeHandler(jobId: string, handler: () => Promise<void>): () => void {
  resumeHandlers.set(jobId, handler);
  return () => {
    if (resumeHandlers.get(jobId) === handler) resumeHandlers.delete(jobId);
  };
}

let stealBackInFlight: TakeoverState | null = null;
let controlInFlight: TakeoverState | null = null;

export async function pauseTakeover() {
  const { takeover, setTakeover, pushToast } = useUi.getState();
  if (!takeover || takeoverPaused(takeover) || controlInFlight === takeover) return;
  controlInFlight = takeover;
  try {
    await aiApi.takeoverPause(takeover.tabId, takeover.token);
    if (useUi.getState().takeover === takeover) setTakeover(withTakeoverPaused(takeover, true));
  } catch (e) {
    pushToast("error", `暂停接管失败：${describeError(e)}`);
  } finally {
    if (controlInFlight === takeover) controlInFlight = null;
  }
}

export async function resumeTakeover() {
  const { takeover, setTakeover, pushToast } = useUi.getState();
  if (!takeover || !takeoverPaused(takeover) || controlInFlight === takeover) return;
  controlInFlight = takeover;
  try {
    if (!takeover.jobId) throw new Error("接管任务 ID 缺失");
    const handler = resumeHandlers.get(takeover.jobId);
    if (!handler) throw new Error("接管会话已断开，请打开对应 AI 会话后重试");
    await handler();
    if (useUi.getState().takeover === takeover) setTakeover(withTakeoverPaused(takeover, false));
  } catch (e) {
    pushToast("error", `恢复接管失败：${describeError(e)}`);
  } finally {
    if (controlInFlight === takeover) controlInFlight = null;
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

function alreadyEnded(message: string): boolean {
  return message.includes("接管令牌已过期") || message.includes("接管任务不存在或已结束");
}

export async function stealBack(reason = "用户收回控制权") {
  const { takeover, setTakeover, pushToast } = useUi.getState();
  if (!takeover || stealBackInFlight === takeover) return;
  stealBackInFlight = takeover;
  try {
    if (takeover.jobId) {
      await aiApi.takeoverCancel(takeover.jobId).catch(() => undefined);
    }
    await aiApi.takeoverExit(takeover.tabId, takeover.token, reason);
    if (useUi.getState().takeover === takeover) setTakeover(null);
    pushToast("info", "已收回终端控制权，AI 已停止");
  } catch (e) {
    const message = describeError(e);
    if (alreadyEnded(message)) {
      if (useUi.getState().takeover === takeover) setTakeover(null);
      pushToast("info", "接管已结束，终端控制权已恢复。");
      return;
    }
    pushToast("error", `收回控制权失败，AI 可能仍在操作终端：${message}`);
  } finally {
    if (stealBackInFlight === takeover) stealBackInFlight = null;
  }
}

export function takeoverEndedIn(text: string): boolean {
  const lastEnd = text.lastIndexOf("接管结束");
  if (lastEnd < 0) return false;
  const lastEnter = text.lastIndexOf("AI 正在操作此终端");
  return lastEnd > lastEnter;
}

export async function restoredTakeoverAlive(
  restored: TakeoverState,
  snapshot: (tabId: string) => Promise<{ text: string }>,
): Promise<boolean> {
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      const snap = await snapshot(restored.tabId);
      return !takeoverEndedIn(snap.text);
    } catch {
      await new Promise((resolve) => window.setTimeout(resolve, 800));
    }
  }
  return true;
}

export function TakeoverBanner() {
  const takeover = useUi((s) => s.takeover);
  const bindings = useKeybindings();
  const reclaimLabel = formatBinding(bindings.reclaimTakeover);
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!takeover) return;
    setNow(Date.now());
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, [takeover]);

  useEffect(() => {
    if (!takeover) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.repeat || isImeKeyEvent(event) || hasActiveOverlay()) return;
      if (!matchKeybinding(event, "reclaimTakeover")) return;
      event.preventDefault();
      event.stopPropagation();
      void stealBack(`用户按 ${formatBinding(getKeybinding("reclaimTakeover"))} 收回控制权`);
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [takeover]);

  useEffect(() => {
    let restored = consumeRestoredTakeover();
    if (!restored) return;
    if (persistedTakeoverPaused(restored)) {
      restored = withTakeoverPaused(restored, true);
      useUi.getState().setTakeover(restored);
    }
    let disposed = false;
    void restoredTakeoverAlive(restored, (tabId) => terminalApi.snapshot(tabId)).then((alive) => {
      if (disposed || alive) return;
      const ui = useUi.getState();
      if (ui.takeover !== restored) return;
      ui.setTakeover(null);
      ui.pushToast("info", "接管已结束");
    });
    return () => {
      disposed = true;
    };
  }, []);

  if (!takeover) return null;

  const elapsed = Math.max(0, Math.floor((now - takeover.startedAt) / 1000));
  const mm = String(Math.floor(elapsed / 60)).padStart(2, "0");
  const ss = String(elapsed % 60).padStart(2, "0");
  const mode = takeover.allowWrite ? "可写" : "只读";
  const paused = takeoverPaused(takeover);

  return (
    <div
      className="nx-takeover-banner flex h-[34px] shrink-0 items-center gap-2.5 border-b border-red-500/40 bg-red-950 px-3 text-xs text-red-100"
      role="region"
      aria-label="AI 终端接管状态"
    >
      <span className="nx-sr-only" role="status" aria-atomic="true">
        {paused ? `AI 接管已暂停，${mode}。` : `AI 正在操作此终端，${mode}。`}
        {bindings.reclaimTakeover ? `按 ${reclaimLabel} 可收回控制权。` : "可收回控制权。"}
      </span>
      <span className="flex items-center gap-1.5 font-semibold text-red-100">
        <IconAlert size={14} className="text-red-300" />
        {paused ? "AI 接管已暂停" : "AI 正在操作此终端"}
      </span>
      <span className={`nx-badge ${takeover.allowWrite ? "nx-badge-red" : ""}`}>
        <IconShield size={10} />
        {mode}
      </span>
      <span className="min-w-0 flex-1 truncate text-red-200/80" title={takeover.task}>
        {takeover.task}
      </span>
      <span
        className="shrink-0 font-mono tabular-nums text-red-200"
        role="timer"
        aria-label={`已用时 ${mm} 分 ${ss} 秒`}
      >
        {mm}:{ss}
      </span>
      <span
        className="shrink-0 text-red-300"
        title="收回控制权将停止 AI 并退出接管；在终端内按任意键仅暂停接管，之后可恢复。"
      >
        {bindings.reclaimTakeover ? (
          <>
            按 <span className="nx-kbd border-red-500/40 bg-red-500/10 text-red-300">{reclaimLabel}</span> 收回控制权
          </>
        ) : (
          "可收回控制权"
        )}
      </span>
      <button
        type="button"
        className="nx-btn nx-btn-outline nx-btn-sm shrink-0"
        title={paused ? "使用同一接管任务 ID 继续" : "暂停 AI 接管，之后可继续"}
        onClick={() => void (paused ? resumeTakeover() : pauseTakeover())}
      >
        {paused ? "继续" : "暂停"}
      </button>
      <button
        type="button"
        className="nx-btn nx-btn-danger-solid nx-btn-sm shrink-0"
        aria-keyshortcuts={formatBindingAria(bindings.reclaimTakeover) ?? undefined}
        title="收回控制权将停止 AI 并退出接管；在终端内按任意键仅暂停接管，之后可恢复。"
        onClick={() => void stealBack()}
      >
        <IconGamepad size={12} />
        收回控制权
      </button>
    </div>
  );
}
