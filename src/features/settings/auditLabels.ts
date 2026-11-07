import { t } from "../../i18n";
// Chinese labels for fleet device audit kinds (audit_log.asset_id holds the device ID).
export const KIND_LABELS: Record<string, string> = {
  device_enroll: t("设备接入"),
  device_online: t("设备上线"),
  device_offline: t("设备离线"),
  device_revoke: t("设备吊销"),
  device_autostart: t("设备开机自启"),
  device_terminal_toggle: t("设备远程终端开关"),
  device_failover: t("设备切换连接"),
  device_terminal: t("设备终端"),
  device_alert_fired: t("设备告警触发"),
  device_alert_resolved: t("设备告警恢复"),
};
