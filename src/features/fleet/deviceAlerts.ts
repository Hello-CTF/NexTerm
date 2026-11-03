import { useQuery } from "@tanstack/react-query";
import { fleetAlertsClient, type FleetAlert, type FleetAlertEvent, type FleetAlerts } from "../../ipc/fleetAlertsApi";
import { fleetClient } from "./clients";
import { useFleetAccount } from "./useFleetAccount";

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

const RULE_NAMES: Record<string, string> = { disk: "磁盘", mem: "内存", cpu: "CPU", offline: "离线" };
const RULE_METRIC_NAMES: Record<string, string> = { disk: "磁盘使用率", mem: "内存使用率", cpu: "CPU 使用率" };
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
    if (event.kind === "fired") return `已离线 ${minutes} 分钟`;
    return event.muted ? `静音期间离线 ${minutes} 分钟，已恢复` : `已恢复，离线 ${minutes} 分钟`;
  }
  if (event.kind !== "fired") return "已恢复";
  const metric = RULE_METRIC_NAMES[event.rule] ?? ruleName(event.rule);
  const sustain = RULE_SUSTAIN_MINUTES[event.rule];
  const parts = [event.value !== undefined ? `${metric} ${percent(event.value)}` : metric];
  if (event.threshold !== undefined) parts.push(`超过 ${percent(event.threshold)}`);
  return sustain ? `${parts.join("，")} 已持续 ${sustain} 分钟` : parts.join("，");
}

export function alertEventTitle(deviceName: string, event: FleetAlertEvent): string {
  const rule = ruleName(event.rule);
  return event.kind === "fired" ? `${deviceName} · ${rule}告警` : `${deviceName} · ${rule}已恢复`;
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
