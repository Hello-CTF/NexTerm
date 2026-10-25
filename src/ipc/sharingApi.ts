// Browser-side HTTP client for /share/* admin routes; shares the same session
// cookie and CSRF header with authApi/fleetApi (request is reused directly, 401 also goes through SESSION_EXPIRED_EVENT).
// WEB transport only: the desktop has no account system, so the view layer shows an explicit unavailable state.
// Contract aligned with internal/fleet/server/sharing_http.go: list/revoke responses carry no token;

import { request } from "./authApi";

export type SharePermission = "read" | "read_write";

export interface HostShareView {
  id: string;
  owner_id: string;
  owner_username: string;
  device_id: string;
  recipient_id: string;
  recipient_username: string;
  permission: SharePermission;
  created_at: number;
  expires_at: number;
  revoked_at?: number;
}

export const sharingApi = {
  hostShares: () => request<{ shares: HostShareView[] }>("GET", "/share/host-shares"),

  createHostShare: (input: { deviceId: string; recipientUsername: string; write: boolean; ttlMs: number }) =>
    request<HostShareView>(
      "POST",
      "/share/host-shares",
      { device_id: input.deviceId, recipient_username: input.recipientUsername, write: input.write, ttl_ms: input.ttlMs },
      { csrf: true },
    ),

  revokeHostShare: (id: string) =>
    request<{ ok: boolean }>("POST", `/share/host-shares/${encodeURIComponent(id)}/revoke`, {}, { csrf: true }),
};
