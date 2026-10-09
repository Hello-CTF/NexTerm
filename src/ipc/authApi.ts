// /auth/* 与 /admin/* 的浏览器端 HTTP 客户端:HttpOnly cookie 会话 + 每会话 CSRF 头。
// 仅 WEB 传输可用;桌面端无账号体系(本地优先)。
// 错误一律规整为 AppError 形状({ code, message }),与 /rpc 的 toAppError 对齐。

import { WEB } from "./env";
import { httpUrl } from "./env";
import type { AppError } from "./commands";

export type AccountRole = "superadmin" | "user";
export type AccountState = "active" | "disabled" | "reset_required";

export interface AccountUser {
  id: string;
  username: string;
  display_name: string;
  role: AccountRole;
  state: AccountState;
  must_change_password: boolean;
  mfa_enabled: boolean;
  created_at: number;
  updated_at: number;
  last_login_at: number;
}

export interface AccountStatus {
  initialized: boolean;
  registration_open: boolean;
  auth: string;
}

export interface AccountSession {
  user: AccountUser;
  csrf_token: string;
  /** mfa_required 策略: 开启且 user.mfa_enabled 为 false 时会话被锁到只能完成 TOTP 绑定。 */
  mfa_required: boolean;
}

// MfaChallenge 是已绑定 TOTP 的账号在密码通过后拿到的中间态: 凭 ticket 换会话。
export interface MfaChallenge {
  mfa_required: true;
  ticket: string;
  expires_at: number;
}

export function isMfaChallenge(payload: AccountSession | MfaChallenge): payload is MfaChallenge {
  return typeof (payload as MfaChallenge).ticket === "string";
}

export interface TotpStatus {
  enabled: boolean;
  pending: boolean;
  mfa_required: boolean;
  recovery_codes_left: number;
}

export interface TotpSetup {
  secret: string;
  otpauth_uri: string;
}

export interface DekEnvelopesView {
  dek_envelope: string;
  kdf_salt: string;
  kdf_params: string;
  recovery_envelope: string;
  recovery_hash: string;
}

export interface AccountDevice {
  id: string;
  name: string;
  kind: string;
  created_at: number;
  last_seen_at: number;
  revoked_at: number;
}

export interface AdminSettings {
  registration_open: boolean;
  mfa_required: boolean;
}

export interface SyncIdEntry {
  id: string;
  seq: number;
  blob_hash: string;
}

export interface SyncIdsResponse {
  protocol: number;
  entries: SyncIdEntry[];
  head: string;
  max_seq: number;
}

export interface SyncWireObject {
  id: string;
  seq?: number;
  blob: string;
}

export interface SyncPullResponse {
  protocol: number;
  objects: SyncWireObject[];
  head: string;
  max_seq: number;
  next_seq: number;
  cursor_done: boolean;
}

export interface SyncPushResponse {
  protocol: number;
  head: string;
  max_seq: number;
  applied: number;
  skipped: number;
}

export const SYNC_PROTOCOL_VERSION = 2;
export const SESSION_EXPIRED_EVENT = "nexterm:session-expired";
// 服务端在 mfa_required 策略下拒绝未绑定会话时广播, 门据此切入强制绑定页(覆盖策略中途开启的场景)。
export const MFA_ENROLLMENT_REQUIRED_EVENT = "nexterm:mfa-enrollment-required";

let csrfToken: string | null = null;

export function setCsrfToken(token: string | null): void {
  csrfToken = token;
}

export function getCsrfToken(): string | null {
  return csrfToken;
}

export class AuthApiError extends Error {
  constructor(
    readonly code: string,
    message: string,
    readonly status: number,
  ) {
    super(message);
    this.name = "AuthApiError";
  }
}

export function toAuthError(e: unknown): AppError {
  if (e instanceof AuthApiError) return { code: e.code, message: e.message };
  if (e && typeof e === "object" && "code" in e) return e as AppError;
  return { code: "internal", message: e instanceof Error ? e.message : String(e) };
}

export async function request<T>(method: string, path: string, body?: unknown, options: { csrf?: boolean } = {}): Promise<T> {
  const headers: Record<string, string> = { accept: "application/json" };
  if (body !== undefined) headers["content-type"] = "application/json";
  if (options.csrf && csrfToken) headers["X-NexTerm-CSRF"] = csrfToken;
  let response: Response;
  try {
    response = await fetch(httpUrl(path), {
      method,
      headers,
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
  } catch (e) {
    throw new AuthApiError("disconnected", `无法连接服务器: ${e instanceof Error ? e.message : String(e)}`, 0);
  }
  const text = await response.text();
  let payload: unknown = null;
  if (text.length > 0) {
    try {
      payload = JSON.parse(text);
    } catch {
      throw new AuthApiError("internal", `服务器返回了非 JSON 响应（HTTP ${response.status}）`, response.status);
    }
  }
  if (!response.ok) {
    const failure = payload as { error?: { code?: string; message?: string } } | null;
    const code = failure?.error?.code ?? "internal";
    const message = failure?.error?.message ?? `HTTP ${response.status}`;
    if (response.status === 401) {
      window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
    }
    if (code === "mfa_enrollment_required") {
      window.dispatchEvent(new CustomEvent(MFA_ENROLLMENT_REQUIRED_EVENT));
    }
    throw new AuthApiError(code, message, response.status);
  }
  return payload as T;
}

function envelopesBody(fields: {
  dekEnvelope: Uint8Array;
  kdfSalt: Uint8Array;
  kdfParams: string;
  recoveryEnvelope: Uint8Array;
  recoveryHash: string;
}): Record<string, unknown> {
  return {
    dek_envelope: b64(fields.dekEnvelope),
    kdf_salt: b64(fields.kdfSalt),
    kdf_params: fields.kdfParams,
    recovery_envelope: b64(fields.recoveryEnvelope),
    recovery_hash: fields.recoveryHash,
  };
}

function b64(data: Uint8Array): string {
  let binary = "";
  for (const b of data) binary += String.fromCharCode(b);
  return btoa(binary);
}

function rememberSession(session: AccountSession): AccountSession {
  setCsrfToken(session.csrf_token);
  return session;
}

export const authApi = {
  status: () => request<AccountStatus>("GET", "/auth/status"),

  init: (input: { code: string; username: string; password: string } & Parameters<typeof envelopesBody>[0]) =>
    request<AccountSession>("POST", "/auth/init", { code: input.code, username: input.username, password: input.password, ...envelopesBody(input) }).then(rememberSession),

  login: (username: string, password: string, deviceId?: string) =>
    request<AccountSession | MfaChallenge>("POST", "/auth/login", {
      username,
      password,
      ...(deviceId ? { device_id: deviceId } : {}),
    }),

  // totpLogin 用登录票据 + 动态码(或一次性恢复码)换会话; 与 login 一样建立 cookie 会话。
  totpLogin: (ticket: string, code: string) =>
    request<AccountSession>("POST", "/auth/totp/login", { ticket, code }).then(rememberSession),

  totpStatus: () => request<TotpStatus>("GET", "/auth/totp"),

  // totpSetup 开启或换绑 TOTP; 已绑定用户必须带 reverify(当前动态码、恢复码或登录密码)。
  totpSetup: (reverify?: string) =>
    request<TotpSetup>("POST", "/auth/totp/setup", reverify ? { reverify } : {}, { csrf: true }),

  totpConfirm: (code: string) =>
    request<{ recovery_codes: string[] }>("POST", "/auth/totp/confirm", { code }, { csrf: true }),

  totpDisable: (code: string) =>
    request<{ ok: boolean }>("DELETE", "/auth/totp", { code }, { csrf: true }),

  register: (input: { username: string; password: string; displayName: string } & Parameters<typeof envelopesBody>[0]) =>
    request<AccountSession>("POST", "/auth/register", {
      username: input.username,
      password: input.password,
      display_name: input.displayName,
      ...envelopesBody(input),
    }).then(rememberSession),

  me: () => request<AccountSession>("GET", "/auth/me").then(rememberSession),

  // 平台托管模式（--auth=platform）下的握手。前置网关已经完成认证, 这里只需换取
  // 「平台所有者」会话; 没有这一步, 用户会看到初始化/登录界面, 而懒猫上两者都不该出现。
  platformSession: () => request<AccountSession>("POST", "/auth/platform-session").then(rememberSession),

  logout: () => request<{ ok: boolean }>("POST", "/auth/logout", {}, { csrf: true }),

  logoutAll: () => request<{ revoked: number }>("POST", "/auth/logout-all", {}, { csrf: true }),

  changePassword: (input: { oldPassword: string; newPassword: string } & Parameters<typeof envelopesBody>[0]) =>
    request<{ ok: boolean }>("POST", "/auth/password", {
      old_password: input.oldPassword,
      new_password: input.newPassword,
      ...envelopesBody(input),
    }, { csrf: true }),

  recoveryReset: (input: { username: string; recoveryKey: string; newPassword: string } & Parameters<typeof envelopesBody>[0]) =>
    request<{ ok: boolean }>("POST", "/auth/recovery/reset", {
      username: input.username,
      recovery_key: input.recoveryKey,
      new_password: input.newPassword,
      ...envelopesBody(input),
    }),

  dekGet: () => request<DekEnvelopesView>("GET", "/auth/dek"),

  dekUpload: (fields: Parameters<typeof envelopesBody>[0]) =>
    request<{ ok: boolean }>("POST", "/auth/dek", envelopesBody(fields), { csrf: true }),

  devices: () => request<{ devices: AccountDevice[] }>("GET", "/auth/devices"),

  enrollCode: () => request<{ code: string; expires_at: number }>("POST", "/auth/devices/enroll-code", {}, { csrf: true }),

  deviceRevoke: (id: string) => request<{ ok: boolean }>("DELETE", `/auth/devices/${encodeURIComponent(id)}`, undefined, { csrf: true }),

  enroll: (code: string, name: string, kind: string) =>
    request<{ device: AccountDevice }>("POST", "/auth/devices/enroll", { code, name, kind }),
};

export const adminApi = {
  users: () => request<{ users: AccountUser[] }>("GET", "/admin/users"),

  createUser: (username: string, password: string, displayName: string) =>
    request<{ user: AccountUser }>("POST", "/admin/users", { username, password, display_name: displayName }, { csrf: true }),

  disableUser: (id: string) =>
    request<{ ok: boolean }>("POST", `/admin/users/${encodeURIComponent(id)}/disable`, {}, { csrf: true }),

  resetUser: (id: string) =>
    request<{ ok: boolean }>("POST", `/admin/users/${encodeURIComponent(id)}/reset`, {}, { csrf: true }),

  settingsGet: () => request<AdminSettings>("GET", "/admin/settings"),

  settingsPut: (settings: { registrationOpen: boolean; mfaRequired?: boolean }) =>
    request<AdminSettings>("PUT", "/admin/settings", {
      registration_open: settings.registrationOpen,
      ...(settings.mfaRequired === undefined ? {} : { mfa_required: settings.mfaRequired }),
    }, { csrf: true }),
};

export const syncV2Api = {
  ids: () =>
    request<SyncIdsResponse>("POST", "/sync/v2/ids", { protocol: SYNC_PROTOCOL_VERSION }),

  pull: (sinceSeq: number, ids?: string[], maxBytes?: number) =>
    request<SyncPullResponse>("POST", "/sync/v2/pull", {
      protocol: SYNC_PROTOCOL_VERSION,
      since_seq: sinceSeq,
      ...(ids && ids.length > 0 ? { ids } : {}),
      ...(maxBytes ? { max_bytes: maxBytes } : {}),
    }),

  push: (knownHead: string, objects: { id: string; blob: string }[]) =>
    request<SyncPushResponse>("POST", "/sync/v2/push", {
      protocol: SYNC_PROTOCOL_VERSION,
      known_head: knownHead,
      objects,
    }, { csrf: true }),
};

// PreferenceScopeView 是 /auth/preferences 的三层视图(默认+覆盖+生效)。
export interface PreferenceScopeView {
  defaults: Record<string, unknown>;
  overrides: Record<string, unknown>;
  effective: Record<string, unknown>;
}

export interface PreferenceDefaultsView {
  defaults: Record<string, unknown>;
}

export const preferencesApi = {
  get: () => request<PreferenceScopeView>("GET", "/auth/preferences"),

  put: (update: { set?: Record<string, unknown>; clear?: string[] }) =>
    request<PreferenceScopeView>("PUT", "/auth/preferences", update, { csrf: true }),

  adminGet: () => request<PreferenceDefaultsView>("GET", "/admin/preferences"),

  adminPut: (update: { set?: Record<string, unknown>; clear?: string[] }) =>
    request<PreferenceDefaultsView>("PUT", "/admin/preferences", update, { csrf: true }),
};

export function authAvailable(): boolean {
  return WEB;
}
