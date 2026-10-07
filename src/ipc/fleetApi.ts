// /fleet/* 与 /device/enroll-codes 的浏览器端 HTTP 客户端, 与 authApi 共用
// 同一会话 cookie 与 CSRF 头 (request 直接复用, 401 同样走 SESSION_EXPIRED_EVENT)。
// 仅 WEB 传输可用: 桌面端无账号体系, DEMO 没有 fleet 假后端, 由视图层给出显式不可用态。

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
  // 设备端 supervisor 状态摘要 (控制通道上报, 不含凭据材料), 仅供远程终端
  // hello 协商; 不得在 UI 展示、写日志或持久化到 web storage。设备离线时缺省。
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
    request<{ samples: FleetMetricsSample[] }>(
      "GET",
      `/fleet/devices/${encodeURIComponent(id)}/metrics${sinceMs ? `?since_ms=${sinceMs}` : ""}`,
    ),

  baseUrls: () => request<{ base_urls: FleetBaseURLEntry[] }>("GET", "/fleet/base-urls"),

  putBaseUrls: (entries: FleetBaseURLEntry[]) =>
    request<{ base_urls: FleetBaseURLEntry[] }>("PUT", "/fleet/base-urls", { base_urls: entries }, { csrf: true }),
};
