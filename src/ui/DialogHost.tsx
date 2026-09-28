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
