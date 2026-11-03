// Chinese labels for fleet device audit kinds (audit_log.asset_id holds the device ID).
export const KIND_LABELS: Record<string, string> = {
  device_enroll: "设备接入",
  device_online: "设备上线",
  device_offline: "设备离线",
  device_revoke: "设备吊销",
  device_autostart: "设备开机自启",
  device_terminal_toggle: "设备远程终端开关",
  device_failover: "设备切换连接",
  device_terminal: "设备终端",
  device_alert_fired: "设备告警触发",
  device_alert_resolved: "设备告警恢复",
};
