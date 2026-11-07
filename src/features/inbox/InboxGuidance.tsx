import { useCallback, useEffect, useState } from "react";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { showSettingsSection } from "../settings/settingsNavigation";
import { dismissInboxGuidance } from "./firstRunGuidance";
import { refreshNotificationStatus } from "./inboxNotifications";
import { systemNotifier, type SystemNotificationStatus } from "./systemNotifications";
import { t } from "../../i18n";

// InboxGuidance is the one-time note shown the first time a session needs the user or finishes: what the
// badges and the bell mean, a way to allow system notifications, and a link to the notification settings.
export function InboxGuidance({ onNavigate }: { onNavigate: () => void }) {
  const [status, setStatus] = useState<SystemNotificationStatus | null>(() => systemNotifier().knownStatus?.() ?? null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (systemNotifier().knownStatus) return;
    let alive = true;
    systemNotifier()
      .status()
      .then((next) => alive && setStatus(next))
      .catch(() => alive && setStatus(null));
    return () => {
      alive = false;
    };
  }, []);

  const enable = useCallback(() => {
    setError(null);
    systemNotifier()
      .request()
      .then((next) => {
        refreshNotificationStatus();
        setStatus(next);
        if (next.permission === "granted") dismissInboxGuidance();
      })
      .catch((e: unknown) => setError(describeError(e)));
  }, []);

  const openSettings = useCallback(() => {
    useUi.getState().addTab({ id: "settings", kind: "settings", title: t("设置"), closable: true });
    showSettingsSection("settings-notifications");
    dismissInboxGuidance();
    onNavigate();
  }, [onNavigate]);

  const canEnable = status?.available === true && status.permission !== "granted";
  return (
    <section
      className="mb-1.5 rounded-md border border-neutral-800 bg-neutral-950/60 px-2.5 py-2 text-[12px] text-neutral-300"
      data-inbox-guidance
      aria-label={t("收件箱说明")}
    >
      <p className="text-neutral-200">{t("收件箱说明")}</p>
      <p className="nx-hint mt-1">{t("标签页圆点表示会话需要处理或长命令已结束，铃铛汇总全部。")}</p>
      {canEnable && <p className="nx-hint mt-1">{t("开启系统通知，窗口在后台也能提醒。")}</p>}
      {error && <p className="mt-1 text-[11.5px] text-red-400">{error}</p>}
      <div className="mt-2 flex flex-wrap gap-1.5">
        {canEnable && (
          <button type="button" className="nx-btn nx-btn-outline nx-btn-xs" data-guidance="enable" onClick={enable}>
            {t("允许系统通知")}
          </button>
        )}
        <button type="button" className="nx-btn nx-btn-outline nx-btn-xs" data-guidance="settings" onClick={openSettings}>
          {t("通知设置")}
        </button>
        <button type="button" className="nx-btn nx-btn-outline nx-btn-xs" data-guidance="dismiss" onClick={dismissInboxGuidance}>
          {t("确定")}
        </button>
      </div>
    </section>
  );
}
