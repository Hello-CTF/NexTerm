// 接管横幅（§8.6）—— 硬性安全要求，不是可选装饰：
//   · 进入接管后必须在**顶部**显示醒目提示，让用户随时知道「AI 正在操作此终端」；
//   · 必须提供「立即夺回」按钮，一键中断；
//   · Esc 也能夺回（终端内敲任意键由内核的 steal_on_key 处理）。
import { useEffect, useState } from "react";
import { useUi } from "./store";
import { aiApi } from "../ipc/commands";
import { IconAlert, IconGamepad, IconShield } from "../ui/icons";

/** 夺回：取消模型请求 + 退出接管 + 清横幅。幂等，可被按钮与 Esc 同时触发。 */
async function stealBack(reason = "用户夺回控制权") {
  const { takeover, setTakeover, pushToast } = useUi.getState();
  if (!takeover) return;
  setTakeover(null); // 先清 UI，避免连点/连按重复触发
  try {
    if (takeover.jobId) {
      await aiApi.cancel(takeover.jobId).catch(() => undefined);
    }
    await aiApi.takeoverExit(takeover.tabId, reason);
    pushToast("info", "已夺回终端控制权，AI 已停止");
  } catch (e) {
    pushToast("error", `夺回时出错：${typeof e === "object" ? JSON.stringify(e) : String(e)}`);
  }
}

export function TakeoverBanner() {
  const takeover = useUi((s) => s.takeover);
  const [now, setNow] = useState(() => Date.now());

  // 每秒刷新「已用时」，让用户对 AI 持续操作有时长感知
  useEffect(() => {
    if (!takeover) return;
    setNow(Date.now());
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, [takeover]);

  // Esc 夺回（捕获阶段，避免被终端的 keydown 吞掉）
  useEffect(() => {
    if (!takeover) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        void stealBack("用户按 Esc 夺回");
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [takeover]);

  if (!takeover) return null;

  const elapsed = Math.max(0, Math.floor((now - takeover.startedAt) / 1000));
  const mm = String(Math.floor(elapsed / 60)).padStart(2, "0");
  const ss = String(elapsed % 60).padStart(2, "0");

  return (
    <div className="flex h-[34px] shrink-0 items-center gap-2.5 border-b border-red-500/40 bg-red-950 px-3 text-xs text-red-100">
      <span className="flex items-center gap-1.5 font-semibold text-red-100">
        <IconAlert size={14} className="text-red-300" />
        AI 正在操作此终端
      </span>
      <span className={`nx-badge ${takeover.allowWrite ? "nx-badge-red" : ""}`}>
        <IconShield size={10} />
        {takeover.allowWrite ? "可写" : "只读"}
      </span>
      <span className="min-w-0 flex-1 truncate text-red-200/80" title={takeover.task}>
        {takeover.task}
      </span>
      <span className="shrink-0 font-mono tabular-nums text-red-200">{mm}:{ss}</span>
      <span className="shrink-0 text-red-300/70">
        按 <span className="nx-kbd border-red-500/40 bg-red-900/60 text-red-200">Esc</span> 随时夺回
      </span>
      <button className="nx-btn nx-btn-danger-solid nx-btn-sm shrink-0" onClick={() => void stealBack()}>
        <IconGamepad size={12} />
        立即夺回
      </button>
    </div>
  );
}
