// Browser-side HTTP client for /fleet/* and /device/enroll-codes; shares the same
// session cookie and CSRF header with authApi (request is reused directly, 401 also goes through SESSION_EXPIRED_EVENT).
// WEB transport only: the desktop has no account system, so the view layer shows an explicit unavailable state.

import { request } from "./authApi";

export interface FleetOwner {
  id: string;
  username: string;
}

export interface FleetServiceState {
  installed: boolean;
  enabled: boolean;
  active: boolean;
  last_reconcile_at?: number;
  last_error?: string;
}

export interface FleetAgentInfo {
  platform: string;
  app_version: string;
  desired_autostart: boolean;
  terminal_enabled: boolean;
  current_url: string;
  service_state: FleetServiceState;
  last_seen_at: number;
  // Device-side supervisor state digest (reported over the control channel, no credential material), for remote terminal
  // hello negotiation only; must not be shown in the UI, logged, or persisted to web storage. Absent when the device is offline.
  state_digest?: string;
}

export interface FleetDevice {
  id: string;
  name: string;
  kind: string;
  created_at: number;
  last_seen_at: number;
  revoked_at: number;
  owner?: FleetOwner;
  agent?: FleetAgentInfo;
}

export interface FleetMetricsSample {
  ts: number;
  cpu_pct: number;
  mem_used: number;
  mem_total: number;
  disk_used: number;
  disk_total: number;
  uptime_s: number;
}

export interface FleetMetricsResponse {
  samples: FleetMetricsSample[];
  latest?: FleetMetricsSample;
  windowStart?: number;
  windowEnd?: number;
  sampleCount?: number;
  downsampled?: boolean;
  seriesError?: string;
  sampleIntervalMs?: number;
}

export interface FleetBaseURLEntry {
  url: string;
  insecure?: boolean;
}

export interface FleetEnrollCode {
  code: string;
  expires_at: number;
}

export const fleetApi = {
  devices: () => request<{ devices: FleetDevice[] }>("GET", "/fleet/devices"),

  issueEnrollCode: (ttlMs: number, userId?: string) =>
    request<FleetEnrollCode>("POST", "/device/enroll-codes", {
      ttl_ms: ttlMs,
      ...(userId ? { user_id: userId } : {}),
    }, { csrf: true }),

  revoke: (id: string) =>
    request<{ ok: boolean }>("POST", `/fleet/devices/${encodeURIComponent(id)}/revoke`, {}, { csrf: true }),

  setAutostart: (id: string, desired: boolean) =>
    request<{ ok: boolean; desired_autostart: boolean }>(
      "POST",
      `/fleet/devices/${encodeURIComponent(id)}/autostart`,
      { desired },
      { csrf: true },
    ),

  metrics: (id: string, sinceMs?: number) =>
    request<FleetMetricsResponse>(
      "GET",
      `/fleet/devices/${encodeURIComponent(id)}/metrics${sinceMs ? `?since_ms=${sinceMs}` : ""}`,
    ),

  baseUrls: () => request<{ base_urls: FleetBaseURLEntry[] }>("GET", "/fleet/base-urls"),

  putBaseUrls: (entries: FleetBaseURLEntry[]) =>
    request<{ base_urls: FleetBaseURLEntry[] }>("PUT", "/fleet/base-urls", { base_urls: entries }, { csrf: true }),

  // serverVersion reads the real server version from /healthz (used for one-click install --version on fresh
  // machines). An empty string means the version is unavailable: callers must hide version-dependent UI, never fabricate a version.
  serverVersion: () => request<{ version?: string }>("GET", "/healthz").then((r) => r.version ?? ""),
};
