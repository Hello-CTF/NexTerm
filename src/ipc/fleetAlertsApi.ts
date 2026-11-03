// Device alerts: browser HTTP (/fleet/alerts*) and the desktop remote_fleet_alert* proxies behind one client.

import { call } from "./commands";
import { WEB } from "./env";
import { request } from "./authApi";

export type FleetAlertRule = "disk" | "mem" | "cpu" | "offline";

export interface FleetAlert {
  device_id: string;
  rule: string;
  status: string;
  since: number;
  value?: number;
}

export interface FleetAlertEvent {
  id: string;
  device_id: string;
  rule: string;
  kind: "fired" | "resolved" | string;
  ts: number;
  value?: number;
  threshold?: number;
  duration_ms: number;
  muted: boolean;
}

export interface FleetAlerts {
  active: FleetAlert[];
  events: FleetAlertEvent[];
}

// FleetAlertSettingsInput is the PUT body; the server requires every field.
export interface FleetAlertSettingsInput {
  disk_enabled: boolean;
  disk_threshold: number;
  mem_enabled: boolean;
  mem_threshold: number;
  cpu_enabled: boolean;
  cpu_threshold: number;
  offline_enabled: boolean;
  offline_minutes: number;
  expected_offline: boolean;
  quiet_enabled: boolean;
  // Minute of the day, 0-1439.
  quiet_start: number;
  quiet_end: number;
  quiet_tz: string;
}

export interface FleetAlertSettings extends FleetAlertSettingsInput {
  device_id: string;
  kind: string;
  server_tz: string;
  suspect_after_minutes: number;
  // Device clock minus server clock in ms (signed); 0 means no skew.
  clock_skew_ms?: number;
}

export interface FleetAlertsClient {
  list: () => Promise<FleetAlerts>;
  ack: (ids: string[]) => Promise<{ ok: boolean; acked: number }>;
  getSettings: (deviceId: string) => Promise<FleetAlertSettings>;
  putSettings: (deviceId: string, settings: FleetAlertSettingsInput) => Promise<FleetAlertSettings>;
}

const webClient: FleetAlertsClient = {
  list: () => request<FleetAlerts>("GET", "/fleet/alerts"),
  ack: (ids) => request<{ ok: boolean; acked: number }>("POST", "/fleet/alerts/ack", { ids }, { csrf: true }),
  getSettings: (deviceId) =>
    request<FleetAlertSettings>("GET", `/fleet/devices/${encodeURIComponent(deviceId)}/alert-settings`),
  putSettings: (deviceId, settings) =>
    request<FleetAlertSettings>("PUT", `/fleet/devices/${encodeURIComponent(deviceId)}/alert-settings`, settings, { csrf: true }),
};

const remoteClient: FleetAlertsClient = {
  list: async () => (await call("remote_fleet_alerts")) as FleetAlerts,
  ack: async (ids) => (await call("remote_fleet_alerts_ack", { args: { ids } })) as { ok: boolean; acked: number },
  getSettings: async (deviceId) =>
    (await call("remote_fleet_alert_settings_get", { args: { deviceId } })) as FleetAlertSettings,
  putSettings: async (deviceId, settings) =>
    (await call("remote_fleet_alert_settings_put", { args: { deviceId, settings } })) as FleetAlertSettings,
};

export function fleetAlertsClient(): FleetAlertsClient {
  return WEB ? webClient : remoteClient;
}

// isForbiddenError: the caller is not the device owner.
export function isForbiddenError(error: unknown): boolean {
  if (!error || typeof error !== "object") return false;
  const e = error as { status?: unknown; code?: unknown };
  return e.status === 403 || e.code === "forbidden";
}
