import { useQuery } from "@tanstack/react-query";
import { fleetAlertsClient, type FleetAlert, type FleetAlertEvent, type FleetAlerts } from "../../ipc/fleetAlertsApi";
import { fleetClient } from "./clients";
import { useFleetAccount } from "./useFleetAccount";
import { t } from "../../i18n";

export const ALERTS_REFETCH_MS = 60_000;

export function alertsQueryKey(userId: string | null) {
  return ["fleet-alerts", userId] as const;
}

// useFleetAlerts polls the owner's alerts once a minute. It stays idle until the account is signed in, and a
// failure is left to the callers (no toast): the next poll retries.
export function useFleetAlerts() {
  const { userId } = useFleetAccount();
  return useQuery<FleetAlerts>({
    queryKey: alertsQueryKey(userId),
    queryFn: () => fleetAlertsClient().list(),
    enabled: Boolean(userId),
    refetchInterval: ALERTS_REFETCH_MS,
    refetchOnWindowFocus: false,
    retry: false,
  });
}

const RULE_NAMES: Record<string, string> = { disk: t("磁盘"), mem: t("内存"), cpu: "CPU", offline: t("离线") };
const RULE_METRIC_NAMES: Record<string, string> = { disk: t("磁盘使用率"), mem: t("内存使用率"), cpu: t("CPU 使用率") };
// Fixed sustain windows, mirroring the server rules.
export const RULE_SUSTAIN_MINUTES: Record<string, number> = { disk: 10, mem: 10, cpu: 15 };

export function ruleName(rule: string): string {
  return RULE_NAMES[rule] ?? rule;
}

function percent(value: number): string {
  return `${Math.round(value * 10) / 10}%`;
}

function minutesOf(durationMs: number): number {
  return Math.max(1, Math.round(durationMs / 60_000));
}

export function alertEventText(event: FleetAlertEvent): string {
  if (event.rule === "offline") {
    const minutes = minutesOf(event.duration_ms);
    if (event.kind === "fired") return t("已离线 {minutes} 分钟", { minutes });
    return event.muted ? t("静音期间离线 {minutes} 分钟，已恢复", { minutes }) : t("已恢复，离线 {minutes} 分钟", { minutes });
  }
  if (event.kind !== "fired") return t("已恢复");
  const metric = RULE_METRIC_NAMES[event.rule] ?? ruleName(event.rule);
  const sustain = RULE_SUSTAIN_MINUTES[event.rule];
  const parts = [event.value !== undefined ? `${metric} ${percent(event.value)}` : metric];
  if (event.threshold !== undefined) parts.push(t("超过 {value}", { value: percent(event.threshold) }));
  return sustain ? t("{join} 已持续 {sustain} 分钟", { join: parts.join("，"), sustain }) : parts.join("，");
}

export function alertEventTitle(deviceName: string, event: FleetAlertEvent): string {
  const rule = ruleName(event.rule);
  return event.kind === "fired" ? t("{deviceName} · {rule}告警", { deviceName, rule }) : t("{deviceName} · {rule}已恢复", { deviceName, rule });
}

export function activeAlertCounts(alerts: FleetAlerts | undefined): Map<string, number> {
  const counts = new Map<string, number>();
  for (const alert of alerts?.active ?? []) counts.set(alert.device_id, (counts.get(alert.device_id) ?? 0) + 1);
  return counts;
}

export function deviceActiveAlerts(alerts: FleetAlerts | undefined, deviceId: string): FleetAlert[] {
  return (alerts?.active ?? []).filter((alert) => alert.device_id === deviceId);
}

// The server lists unacknowledged events only; muted ones are the quiet-hours offline records.
export function visibleAlertEvents(alerts: FleetAlerts | undefined): FleetAlertEvent[] {
  return (alerts?.events ?? []).filter((event) => !event.muted);
}

// bellAlertCount: the server acknowledges a fired event when its rule resolves, so an unacknowledged unmuted
// fired event is an alert that has not recovered. Resolved events stay listed but do not count.
export function bellAlertCount(alerts: FleetAlerts | undefined): number {
  return visibleAlertEvents(alerts).filter((event) => event.kind === "fired").length;
}

// clockSkewText: empty without a skew; positive skew means the device clock runs ahead of the server.
export function clockSkewText(skewMs: number | undefined): string {
  if (!skewMs) return "";
  const abs = Math.abs(skewMs);
  let amount: string;
  if (abs < 60_000) amount = t("{max} 秒", { max: Math.max(1, Math.round(abs / 1000)) });
  else if (abs < 3_600_000) amount = t("{round} 分钟", { round: Math.round(abs / 60_000) });
  else amount = t("{round} 小时", { round: Math.round(abs / 3_600_000) });
  return t("设备时钟偏差约 {amount}（{value}），指标按服务端接收时间记录", { amount, value: skewMs > 0 ? t("快") : t("慢") });
}

export function mutedAlertEvents(alerts: FleetAlerts | undefined): FleetAlertEvent[] {
  return (alerts?.events ?? []).filter((event) => event.muted);
}

// useDeviceNames observes the shared device list cache without fetching it; names fall back to the caller.
export function useDeviceNames(userId: string | null): (deviceId: string) => string | undefined {
  const query = useQuery({
    queryKey: ["fleet-devices", userId],
    queryFn: () => fleetClient().devices(),
    enabled: false,
  });
  const devices = query.data?.devices;
  return (deviceId) => devices?.find((d) => d.id === deviceId)?.name;
}
