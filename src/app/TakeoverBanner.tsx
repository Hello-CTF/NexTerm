import { useEffect, useState } from "react";
import { consumeRestoredTakeover, useUi, type TakeoverState } from "./store";
import { formatBinding, formatBindingAria, getKeybinding, matchKeybinding, useKeybindings } from "./keybindings";
import { aiApi, terminalApi } from "../ipc/commands";
import { IconAlert, IconGamepad, IconShield } from "../ui/icons";
import { describeError } from "../ui/errorText";
import { hasActiveOverlay, isImeKeyEvent } from "../ui/DialogHost";

let stealBackInFlight: TakeoverState | null = null;

function alreadyEnded(message: string): boolean {
  return message.includes("接管令牌已过期") || message.includes("接管任务不存在或已结束");
}

export async function stealBack(reason = "用户夺回控制权") {
  const { takeover, setTakeover, pushToast } = useUi.getState();
  if (!takeover || stealBackInFlight === takeover) return;
  stealBackInFlight = takeover;
  try {
    if (takeover.jobId) {
      await aiApi.cancel(takeover.jobId).catch(() => undefined);
    }
    await aiApi.takeoverExit(takeover.tabId, takeover.token, reason);
    if (useUi.getState().takeover === takeover) setTakeover(null);
    pushToast("info", "已夺回终端控制权，AI 已停止");
  } catch (e) {
    const message = describeError(e);
    if (alreadyEnded(message)) {
      if (useUi.getState().takeover === takeover) setTakeover(null);
      pushToast("info", "接管已结束，终端已在你手中");
      return;
    }
    pushToast("error", `夺回失败，AI 可能仍在操作终端：${message}`);
  } finally {
    if (stealBackInFlight === takeover) stealBackInFlight = null;
  }
}

export async function restoredTakeoverAlive(
  restored: TakeoverState,
  snapshot: (tabId: string) => Promise<{ text: string }>,
): Promise<boolean> {
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      const snap = await snapshot(restored.tabId);
      return !snap.text.includes("接管结束");
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
      void stealBack(`用户按 ${formatBinding(getKeybinding("reclaimTakeover"))} 夺回`);
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [takeover]);

  useEffect(() => {
    const restored = consumeRestoredTakeover();
    if (!restored) return;
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

  return (
    <div
      className="nx-takeover-banner flex h-[34px] shrink-0 items-center gap-2.5 border-b border-red-500/40 bg-red-950 px-3 text-xs text-red-100"
      role="region"
      aria-label="AI 终端接管状态"
    >
      <span className="nx-sr-only" role="status" aria-atomic="true">
        AI 正在操作此终端，{mode}。
        {bindings.reclaimTakeover ? `按 ${reclaimLabel} 可立即夺回控制权。` : "可立即夺回控制权。"}
      </span>
      <span className="flex items-center gap-1.5 font-semibold text-red-100">
        <IconAlert size={14} className="text-red-300" />
        AI 正在操作此终端
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
      <span className="shrink-0 text-red-300/70">
        {bindings.reclaimTakeover ? (
          <>
            按 <span className="nx-kbd border-red-500/40 bg-red-900/60 text-red-200">{reclaimLabel}</span> 随时夺回
          </>
        ) : (
          "可随时夺回"
        )}
      </span>
      <button
        type="button"
        className="nx-btn nx-btn-danger-solid nx-btn-sm shrink-0"
        aria-keyshortcuts={formatBindingAria(bindings.reclaimTakeover) ?? undefined}
        onClick={() => void stealBack()}
      >
        <IconGamepad size={12} />
        立即夺回
      </button>
    </div>
  );
}
