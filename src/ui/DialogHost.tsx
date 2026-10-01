// 应用内确认 / 提示弹框宿主（替代 plugin-dialog / window.confirm 的原生框）。
//
// 为什么要有它：系统弹框的字体、按钮、图标都是 Windows 的，在整套暗色
// 自绘 UI 里弹出来非常违和；且图标用的是系统图标而非本应用图标集。
// dialogs.ts 通过注册制把 ask / confirm / message 接到这里，全部调用方
// 一行不改就换成应用内样式。
import { useEffect } from "react";
import { useUi } from "../app/store";
import { IconAlert, IconInfo } from "./icons";

/** 标题缺省值：与原生框的「确认 / 提示」对齐，但样式是我们自己的。 */
const DEFAULT_TITLE: Record<AppDialogKind, string> = {
  ask: "确认",
  confirm: "确认",
  message: "提示",
};
type AppDialogKind = "ask" | "confirm" | "message";

export function DialogHost() {
  const dialog = useUi((s) => s.appDialog);
  const close = useUi((s) => s.closeAppDialog);
  const choice = useUi((s) => s.appChoice);
  const closeChoice = useUi((s) => s.closeAppChoice);

  useEffect(() => {
    if (!dialog) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        close(dialog.kind === "message");
      } else if (e.key === "Enter") {
        e.preventDefault();
        close(true);
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [dialog, close]);

  // 多选一弹框的键盘语义：Esc = 取消；Enter = 选「主选项」（没有主选项就选第一个）。
  // 不加这一层的话，习惯性按 Esc / Enter 的用户会觉得弹框"卡住了"。
  useEffect(() => {
    if (!choice) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        closeChoice(null);
      } else if (e.key === "Enter") {
        e.preventDefault();
        const pick = choice.options.find((o) => o.primary) ?? choice.options[0];
        closeChoice(pick ? pick.key : null);
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [choice, closeChoice]);

  if (choice) {
    const warning = choice.level === "warning";
    return (
      <div className="nx-overlay z-[91]" onClick={() => closeChoice(null)}>
        <div
          className="nx-modal max-w-[440px]"
          role="alertdialog"
          onClick={(e) => e.stopPropagation()}
        >
          <div className="nx-modal-header">
            <span className={`flex shrink-0 ${warning ? "text-amber-400" : "text-blue-400"}`}>
              {warning ? <IconAlert size={14} /> : <IconInfo size={14} />}
            </span>
            <span className="truncate">{choice.title}</span>
          </div>
          <div className="nx-modal-body">
            <div className="whitespace-pre-wrap text-[12.5px] leading-relaxed text-neutral-200">
              {choice.message}
            </div>
            {/* 每个选项一个整行按钮：标签 + 一行解释。不做下拉或单选圈 ——
                这是一个需要用户看清后果再决定的动作，选项就该直接摊开。 */}
            <div className="mt-3.5 flex flex-col gap-2">
              {choice.options.map((o) => (
                <button
                  key={o.key}
                  className={`w-full rounded-lg border px-3 py-2 text-left transition-colors ${
                    o.danger
                      ? "border-red-500/35 bg-red-950/30 hover:bg-red-950/55"
                      : "border-neutral-700/70 bg-neutral-900/60 hover:bg-neutral-800/80"
                  }`}
                  onClick={() => closeChoice(o.key)}
                >
                  <span
                    className={`block text-[12.5px] font-semibold ${
                      o.danger ? "text-red-200" : "text-neutral-100"
                    }`}
                  >
                    {o.label}
                  </span>
                  {o.hint && (
                    <span className="mt-0.5 block text-[11px] leading-relaxed text-neutral-400">
                      {o.hint}
                    </span>
                  )}
                </button>
              ))}
            </div>
          </div>
          <div className="nx-modal-footer">
            <button className="nx-btn nx-btn-ghost" onClick={() => closeChoice(null)}>
              取消
            </button>
          </div>
        </div>
      </div>
    );
  }

  if (!dialog) return null;

  const warning = dialog.level === "warning";
  const Icon = warning ? IconAlert : IconInfo;
  const iconColor = warning ? "text-amber-400" : "text-blue-400";

  return (
    // 全局系统弹框要压过一切应用内浮层（模型配置面板 z-[70] 等）
    <div className="nx-overlay z-[90]" onClick={() => close(dialog.kind === "message")}>
      <div
        className="nx-modal max-w-[420px]"
        role="alertdialog"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="nx-modal-header">
          <span className={`flex shrink-0 ${iconColor}`}>
            <Icon size={14} />
          </span>
          <span className="truncate">{dialog.title ?? DEFAULT_TITLE[dialog.kind]}</span>
        </div>
        <div className="nx-modal-body">
          <div className="whitespace-pre-wrap text-[12.5px] leading-relaxed text-neutral-200">
            {dialog.message}
          </div>
        </div>
        <div className="nx-modal-footer">
          {dialog.kind !== "message" && (
            <button className="nx-btn nx-btn-ghost" onClick={() => close(false)}>
              取消
            </button>
          )}
          <button
            // 危险确认用实心红；普通确认 / 提示保持主按钮
            className={`nx-btn ${warning ? "nx-btn-danger-solid" : "nx-btn-primary"}`}
            autoFocus
            onClick={() => close(true)}
          >
            确定
          </button>
        </div>
      </div>
    </div>
  );
}
