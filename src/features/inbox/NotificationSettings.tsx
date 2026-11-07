import { useCallback, useEffect, useId, useState } from "react";
import { IconChevronDown, IconChevronRight } from "../../ui/icons";
import { describeError } from "../../ui/errorText";
import { setNotificationPrefs, useNotificationPrefs, type NotificationPrefs } from "./notificationPrefs";
import { refreshNotificationStatus } from "./inboxNotifications";
import { systemNotifier, type SystemNotificationStatus } from "./systemNotifications";
import { t } from "../../i18n";

const CATEGORIES: readonly { key: Exclude<keyof NotificationPrefs, "system">; label: string; hint: string }[] = [
  { key: "needsMe", label: t("需要处理"), hint: t("终端响铃，或程序发出通知") },
  { key: "finished", label: t("长命令结束"), hint: t("运行 10 秒以上的命令结束（需要 shell 集成）") },
  { key: "ai", label: t("AI 通知"), hint: t("AI 助手的通知，仅在窗口不在前台时显示") },
  { key: "deviceAlerts", label: t("设备告警"), hint: t("受管设备的磁盘、内存、CPU 或离线告警及恢复") },
];

function statusText(status: SystemNotificationStatus | null): string {
  if (!status) return t("正在检查系统通知…");
  if (!status.available) return status.reason ?? t("当前客户端不支持系统通知");
  if (status.permission === "denied") return status.reason ?? t("系统不允许 NexTerm 发送通知");
  if (status.permission === "prompt") return t("尚未允许系统通知");
  return t("系统通知可用");
}

// NotificationSettings decides, for this device only, which attention states pop an operating system notification.
// The inbox in the header lists them either way.
export function NotificationSettings() {
  const prefs = useNotificationPrefs();
  // A browser knows its permission at once; the desktop asks the backend.
  const [status, setStatus] = useState<SystemNotificationStatus | null>(() => systemNotifier().knownStatus?.() ?? null);
  const [error, setError] = useState<string | null>(null);
  const [requesting, setRequesting] = useState(false);
  const categoriesId = useId();
  const [categoriesOpen, setCategoriesOpen] = useState(false);

  useEffect(() => {
    if (systemNotifier().knownStatus) return;
    let alive = true;
    systemNotifier()
      .status()
      .then((next) => alive && setStatus(next))
      .catch((e: unknown) => alive && setStatus({ available: false, reason: describeError(e) }));
    return () => {
      alive = false;
    };
  }, []);

  const request = useCallback(() => {
    if (requesting) return;
    setError(null);
    setRequesting(true);
    systemNotifier()
      .request()
      .then((next) => {
        refreshNotificationStatus();
        setStatus(next);
      })
      .catch((e: unknown) => setError(t("未能开启系统通知：{value}。请在系统设置中允许 NexTerm 发送通知。", { value: describeError(e) })))
      .finally(() => setRequesting(false));
  }, [requesting]);

  const usable = status?.available === true;
  return (
    <div data-setting="system-notifications">
      <label className="flex cursor-pointer items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="text-[12.5px] text-neutral-200">{t("在本机显示系统通知")}</div>
          <p className="nx-hint mt-0.5">{t("其他会话需要处理时弹出系统通知，单击可跳转。")}</p>
          <p className="nx-hint mt-0.5" role="status">
            {statusText(status)}
          </p>
          {error && <p className="mt-0.5 text-[11.5px] text-red-400">{error}</p>}
        </div>
        <input
          type="checkbox"
          className="mt-0.5 h-4 w-4 shrink-0"
          checked={prefs.system}
          onChange={(e) => setNotificationPrefs({ system: e.target.checked })}
        />
      </label>
      {usable && status?.permission !== "granted" && (
        <button type="button" className="nx-btn nx-btn-outline nx-btn-xs mt-1.5" disabled={requesting} aria-busy={requesting} onClick={request}>
          {requesting ? t("正在请求权限…") : t("允许系统通知")}
        </button>
      )}
      <button
        type="button"
        className="mt-3 flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
        aria-expanded={categoriesOpen}
        aria-controls={categoriesId}
        onClick={() => setCategoriesOpen((open) => !open)}
      >
        {categoriesOpen ? <IconChevronDown size={12} className="shrink-0" /> : <IconChevronRight size={12} className="shrink-0" />}
        {t("通知类别")}
      </button>
      {categoriesOpen && (
        <div id={categoriesId} className="mt-2 flex flex-col gap-1.5 pl-1" aria-disabled={!prefs.system}>
          {CATEGORIES.map((category) => (
            <label key={category.key} className="flex cursor-pointer items-start gap-2.5">
              <input
                type="checkbox"
                className="mt-0.5 h-3.5 w-3.5 shrink-0"
                disabled={!prefs.system}
                checked={prefs[category.key]}
                onChange={(e) => setNotificationPrefs({ [category.key]: e.target.checked })}
              />
              <span className="min-w-0">
                <span className="text-[12px] text-neutral-200">{category.label}</span>
                <span className="nx-hint ml-1.5">{category.hint}</span>
              </span>
            </label>
          ))}
        </div>
      )}
    </div>
  );
}
