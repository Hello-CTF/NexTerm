import { useCallback, useEffect, useState } from "react";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { showSettingsSection } from "../settings/settingsNavigation";
import { dismissInboxGuidance } from "./firstRunGuidance";
import { refreshNotificationStatus } from "./inboxNotifications";
import { systemNotifier, type SystemNotificationStatus } from "./systemNotifications";

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
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
    showSettingsSection("settings-terminal");
    dismissInboxGuidance();
    onNavigate();
  }, [onNavigate]);

  const canEnable = status?.available === true && status.permission !== "granted";
  return (
    <section
      className="mb-1.5 rounded-md border border-neutral-800 bg-neutral-950/60 px-2.5 py-2 text-[12px] text-neutral-300"
      data-inbox-guidance
      aria-label="收件箱说明"
    >
      <p className="text-neutral-200">收件箱说明</p>
      <p className="nx-hint mt-1">
        终端标签页上的圆点表示该会话需要处理（响铃、程序通知或停在提问处），或刚结束了一条长命令。顶栏铃铛汇总所有工作区的此类会话，选择即可跳转。
      </p>
      <p className="nx-hint mt-1">允许系统通知后，窗口不在前台时也会收到提醒；仅影响本机。</p>
      {error && <p className="mt-1 text-[11.5px] text-red-400">{error}</p>}
      <div className="mt-2 flex flex-wrap gap-1.5">
        {canEnable && (
          <button type="button" className="nx-btn nx-btn-outline nx-btn-xs" data-guidance="enable" onClick={enable}>
            允许系统通知
          </button>
        )}
        <button type="button" className="nx-btn nx-btn-outline nx-btn-xs" data-guidance="settings" onClick={openSettings}>
          通知设置
        </button>
        <button type="button" className="nx-btn nx-btn-outline nx-btn-xs" data-guidance="dismiss" onClick={dismissInboxGuidance}>
          确定
        </button>
      </div>
    </section>
  );
}
