// 桌面端经已登录服务端账号调远程 API 的绑定: 方法签名与 WEB 版 fleetApi.ts /
// sharingApi.ts / authApi.ts 的对应方法同形同返回类型, 视图层按 TRANSPORT 切换。
// 命令在 internal/app/production/remote_ipc.go 注册; 会话与 401 重登由
// internal/sync 的 RemoteProxy 代理面供应(桌面端内存持有 cookie+CSRF)。

import { call } from "./commands";
import type { AccountLink } from "./types";
import type { FleetBaseURLEntry, FleetDevice, FleetEnrollCode, FleetMetricsSample } from "./fleetApi";
import type { HostShareView, ShareLinkView } from "./sharingApi";
import type { AccountDevice, AccountUser } from "./authApi";

// RemoteLinkResult 是 sync_link_set / sync_link_mfa / sync_link_reset_password 的返回:
// AccountLink 视图之外附带连接三态, MFA 分支携带票据, 强制改密完成携带一次性恢复密钥。
export interface RemoteLinkResult extends AccountLink {
  connect: "" | "connected" | "mfa_required" | "reset_required";
  mfaTicket?: string;
  mfaExpiresAt?: number;
  recoveryKey?: string;
}

// remoteLinkApi 是账号连接登录流程的入口: linkSet 保存并探测登录,
// mfa / resetPassword 完成两步验证与强制改密入职(会话建立后内存持有 cookie+CSRF)。
export const remoteLinkApi = {
  linkSet: (patch: { url?: string; username?: string; password?: string; insecure?: boolean }) =>
    call<RemoteLinkResult>("sync_link_set", { args: patch }),

  mfa: (code: string) => call<RemoteLinkResult>("sync_link_mfa", { args: { code } }),

  resetPassword: (newPassword: string) =>
    call<RemoteLinkResult>("sync_link_reset_password", { args: { newPassword } }),
};

// RemotePreviewLinkView 对齐 src/features/sharing/previewApi.ts 的 PreviewLinkView
// (此处独立声明, 避免 ipc 层反向依赖 features 层)。
export interface RemotePreviewLinkView {
  id: string;
  owner_id: string;
  session_id: string;
  created_at: number;
  expires_at: number;
  revoked_at?: number;
  last_accessed_at?: number;
}

// RemoteShareLinkCreateResult 是创建公开链接的响应: 唯一携带一次性 token 的入口,
// token 只用于当场拼公开 URL, 不写日志、不持久化、不进 web storage。
export interface RemoteShareLinkCreateResult extends ShareLinkView {
  token: string;
}

// RemotePreviewCreateResult 是 preview_share_create 的返回: 桌面中继已用账号连接的
// 服务端地址拼好公开 url; token 只在创建当场持有, 不落盘; sessionId 是远程壳 id,
// 「进行中的预览」列表 (remoteSharingApi.previewLinks) 按它过滤当前 tab 的链接。
export interface RemotePreviewCreateResult {
  token: string;
  url: string;
  sessionId: string;
}

// remotePreviewApi 是桌面端只读预览的中继入口: create 建远程壳+预览链接并启动
// 输出中继 (单 tab 单条活跃中继, 重复创建返回既有链接); active 查 tab 当前中继的
// 壳 id。列表/吊销复用 remoteSharingApi.previewLinks / revokePreviewLink。
export const remotePreviewApi = {
  create: (tabId: string, ttlMs: number) =>
    call<RemotePreviewCreateResult>("preview_share_create", { tabId, ttlMs }),

  active: (tabId: string) => call<{ sessionId?: string }>("preview_share_active", { tabId }),
};

export const remoteFleetApi = {
  devices: () => call<{ devices: FleetDevice[] }>("remote_fleet_devices"),

  issueEnrollCode: (ttlMs: number, userId?: string) =>
    call<FleetEnrollCode>("remote_fleet_enroll_code", { ttlMs, ...(userId ? { userId } : {}) }),

  revoke: (id: string) => call<{ ok: boolean }>("remote_fleet_device_revoke", { deviceId: id }),

  approve: (id: string) => call<{ ok: boolean }>("remote_fleet_device_approve", { deviceId: id }),

  reject: (id: string) => call<{ ok: boolean }>("remote_fleet_device_reject", { deviceId: id }),

  setAutostart: (id: string, desired: boolean) =>
    call<{ ok: boolean; desired_autostart: boolean }>("remote_fleet_device_autostart", { deviceId: id, desired }),

  metrics: (id: string, sinceMs?: number) =>
    call<{ samples: FleetMetricsSample[] }>("remote_fleet_device_metrics", {
      deviceId: id,
      ...(sinceMs ? { sinceMs } : {}),
    }),

  baseUrls: () => call<{ base_urls: FleetBaseURLEntry[] }>("remote_fleet_base_urls_get"),

  putBaseUrls: (entries: FleetBaseURLEntry[]) =>
    call<{ base_urls: FleetBaseURLEntry[] }>("remote_fleet_base_urls_put", { entries }),
};

export const remoteSharingApi = {
  hostShares: () => call<{ shares: HostShareView[] }>("remote_share_host_list"),

  createHostShare: (input: { deviceId: string; recipientUsername: string; write: boolean; ttlMs: number }) =>
    call<HostShareView>("remote_share_host_create", input),

  revokeHostShare: (id: string) => call<{ ok: boolean }>("remote_share_host_revoke", { id }),

  links: () => call<{ links: ShareLinkView[] }>("remote_share_link_list"),

  createLink: (input: { deviceId: string; sessionId: string; write: boolean; ttlMs: number }) =>
    call<RemoteShareLinkCreateResult>("remote_share_link_create", input),

  revokeLink: (id: string) => call<{ ok: boolean }>("remote_share_link_revoke", { id }),

  previewLinks: () => call<{ links: RemotePreviewLinkView[] }>("remote_share_preview_list"),

  revokePreviewLink: (id: string) => call<{ ok: boolean }>("remote_share_preview_revoke", { id }),
};

export const remoteAccountApi = {
  me: () => call<{ user: AccountUser; mfa_required: boolean }>("remote_account_me"),

  devices: () => call<{ devices: AccountDevice[] }>("remote_account_devices"),

  deviceRevoke: (id: string) => call<{ ok: boolean }>("remote_account_device_revoke", { deviceId: id }),

  adminUsers: () => call<{ users: AccountUser[] }>("remote_account_admin_users"),

  adminCreateUser: (username: string, password: string, displayName: string) =>
    call<{ user: AccountUser }>("remote_account_admin_create_user", { username, password, displayName }),

  // state 仅支持 disabled(禁用) 与 reset_required(强制改密), 与服务端仅有的两个状态动作对应。
  adminSetUserState: (userId: string, state: "disabled" | "reset_required") =>
    call<{ ok: boolean }>("remote_account_admin_set_user_state", { userId, state }),

  // changePassword 返回一次性恢复密钥(仅本次响应下发), 视图层必须当场展示。
  changePassword: (oldPassword: string, newPassword: string) =>
    call<{ ok: boolean; recoveryKey?: string }>("remote_account_change_password", { oldPassword, newPassword }),
};

export const remoteHealthApi = {
  // serverVersion 读 /healthz 的真实服务端版本。返回空串表示版本不可用:
  // 调用方必须隐藏依赖版本的 UI, 不得伪造版本。
  serverVersion: () => call<string>("remote_healthz"),
};
