// /share/* 管理路由的浏览器端 HTTP 客户端, 与 authApi/fleetApi 共用同一会话
// cookie 与 CSRF 头 (request 直接复用, 401 同样走 SESSION_EXPIRED_EVENT)。
// 仅 WEB 传输可用: 桌面端无账号体系, DEMO 没有分享假后端, 由视图层给出显式不可用态。
// 合同对齐 internal/fleet/server/sharing_http.go: 列表/吊销响应不含任何 token;
// 公开链接的创建入口不在本切片 (由实时会话侧提供), 这里只有列表与吊销。

import { request } from "./authApi";

export type SharePermission = "read" | "read_write";

export interface ShareLinkView {
  id: string;
  owner_id: string;
  device_id: string;
  session_id: string;
  permission: SharePermission;
  created_at: number;
  expires_at: number;
  revoked_at?: number;
  last_accessed_at?: number;
}

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

  links: () => request<{ links: ShareLinkView[] }>("GET", "/share/links"),

  revokeLink: (id: string) =>
    request<{ ok: boolean }>("POST", `/share/links/${encodeURIComponent(id)}/revoke`, {}, { csrf: true }),
};
