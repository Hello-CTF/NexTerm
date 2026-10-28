import { useCallback, useEffect, useState } from "react";
import { describeError } from "../../ui/errorText";
import { setNotificationPrefs, useNotificationPrefs, type NotificationPrefs } from "./notificationPrefs";
import { refreshNotificationStatus } from "./inboxNotifications";
import { systemNotifier, type SystemNotificationStatus } from "./systemNotifications";

const CATEGORIES: readonly { key: Exclude<keyof NotificationPrefs, "system">; label: string; hint: string }[] = [
  { key: "needsMe", label: "需要处理", hint: "终端响铃，或程序发出通知" },
  { key: "finished", label: "长命令结束", hint: "运行 10 秒以上的命令结束（需要 shell 集成）" },
  { key: "ai", label: "AI 通知", hint: "AI 助手的通知，仅在窗口不在前台时显示" },
];

function statusText(status: SystemNotificationStatus | null): string {
  if (!status) return "正在检查系统通知…";
  if (!status.available) return status.reason ?? "此端不支持系统通知";
  if (status.permission === "denied") return status.reason ?? "系统不允许 NexTerm 发送通知";
  if (status.permission === "prompt") return "尚未允许系统通知";
  return "系统通知可用";
}

// NotificationSettings decides, for this device only, which attention states pop an operating system notification.
// The inbox in the header lists them either way.
export function NotificationSettings() {
  const prefs = useNotificationPrefs();
  // A browser knows its permission at once; the desktop asks the backend.
  const [status, setStatus] = useState<SystemNotificationStatus | null>(() => systemNotifier().knownStatus?.() ?? null);
  const [error, setError] = useState<string | null>(null);
  const [requesting, setRequesting] = useState(false);

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
      .catch((e: unknown) => setError(`未能开启系统通知：${describeError(e)}。请在系统设置中允许 NexTerm 发送通知。`))
      .finally(() => setRequesting(false));
  }, [requesting]);

  const usable = status?.available === true;
  return (
    <div className="mt-3.5" data-setting="system-notifications">
      <label className="flex cursor-pointer items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="text-[12.5px] text-neutral-200">在本机显示系统通知</div>
          <p className="nx-hint mt-0.5">
            其他工作区或主机的会话需要处理时显示系统通知，单击通知可返回该终端；正在查看的终端不显示通知。关闭或系统不支持时改为应用内提示，顶栏铃铛始终列出这些会话。仅影响本机。
          </p>
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
          {requesting ? "正在请求权限…" : "允许系统通知"}
        </button>
      )}
      <div className="mt-2 flex flex-col gap-1.5 pl-1" aria-disabled={!prefs.system}>
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
    </div>
  );
}
