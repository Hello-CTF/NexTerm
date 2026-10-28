// Browser-side HTTP client for /auth/* and /admin/*: HttpOnly cookie session + per-session CSRF header.
// WEB transport only; the desktop has no account system (local-first).
// Errors are always normalized to the AppError shape ({ code, message }), aligned with toAppError in /rpc.

import { WEB } from "./env";
import { httpUrl } from "./env";
import type { AppError } from "./commands";
import { advanceAuthSessionEpoch, currentAuthSessionEpoch } from "./authSessionEpoch";

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
  /** mfa_required policy: when enabled and user.mfa_enabled is false, the session is locked to completing TOTP enrollment only. */
  mfa_required: boolean;
}

// MfaChallenge is the intermediate state an account with TOTP bound gets after the password passes: exchange the ticket for a session.
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

export interface AccountLoginSession {
  id: string;
  name: string;
  origin: string;
  device_id?: string;
  device_name?: string;
  created_at: number;
  last_seen_at: number;
  expires_at: number;
  current: boolean;
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
  unchanged?: boolean;
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

export interface SyncDeleteResponse {
  protocol: number;
  head: string;
  max_seq: number;
  deleted: boolean;
}

export const SYNC_PROTOCOL_VERSION = 3;
export const SESSION_EXPIRED_EVENT = "nexterm:session-expired";
export const SESSION_IDENTITY_CHANGED_EVENT = "nexterm:auth-session-changed";
// Broadcast when the server rejects an unenrolled session under the mfa_required policy; the gate switches to the forced enrollment page (covers the policy being enabled mid-session).
export const MFA_ENROLLMENT_REQUIRED_EVENT = "nexterm:mfa-enrollment-required";

let csrfToken: string | null = null;
let hasHeldCsrfToken = false;
let sessionResponseSequence = 0;

export function setCsrfToken(token: string | null): void {
  // The CSRF token derives from the login session ID. A changed nonempty token is a new
  // cookie session even when its user ID is the same.
  if (csrfToken !== token && (csrfToken !== null || hasHeldCsrfToken)) advanceAuthSessionEpoch();
  csrfToken = token;
  if (token !== null) hasHeldCsrfToken = true;
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

function browserClientKind(): "browser" | "mobile" {
  return typeof navigator !== "undefined" && /iPhone|iPad|Android/i.test(navigator.userAgent) ? "mobile" : "browser";
}

function ensureAuthEpoch(startedEpoch: number): void {
  if (currentAuthSessionEpoch() !== startedEpoch) {
    throw new AuthApiError("account_changed", "账号已切换，旧请求已取消", 0);
  }
}

export async function request<T>(method: string, path: string, body?: unknown, options: { csrf?: boolean; client?: boolean } = {}): Promise<T> {
  const startedEpoch = currentAuthSessionEpoch();
  const headers: Record<string, string> = { accept: "application/json" };
  if (body !== undefined) headers["content-type"] = "application/json";
  if (options.csrf && csrfToken) headers["X-NexTerm-CSRF"] = csrfToken;
  if (options.client) headers["X-NexTerm-Client"] = browserClientKind();
  let response: Response;
  try {
    response = await fetch(httpUrl(path), {
      method,
      headers,
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
  } catch (e) {
    ensureAuthEpoch(startedEpoch);
    throw new AuthApiError("disconnected", `无法连接服务器: ${e instanceof Error ? e.message : String(e)}`, 0);
  }
  ensureAuthEpoch(startedEpoch);
  const text = await response.text();
  ensureAuthEpoch(startedEpoch);
  let payload: unknown = null;
  if (text.length > 0) {
    try {
      payload = JSON.parse(text);
    } catch {
      throw new AuthApiError("internal", "服务未返回有效结果，请稍后重试；若持续出现，请检查服务版本或网络代理", response.status);
    }
  }
  if (!response.ok) {
    const failure = payload as { error?: { code?: string; message?: string } } | null;
    const code = failure?.error?.code ?? "internal";
    const message = failure?.error?.message ?? "请求未成功，请稍后重试";
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

function rememberSession(session: AccountSession, sequence: number, startedEpoch: number): AccountSession {
  ensureAuthEpoch(startedEpoch);
  if (sequence !== sessionResponseSequence) return session;
  setCsrfToken(session.csrf_token);
  if (typeof window !== "undefined" && typeof window.dispatchEvent === "function") {
    window.dispatchEvent(new CustomEvent(SESSION_IDENTITY_CHANGED_EVENT, { detail: session }));
  }
  return session;
}

function rememberedSessionRequest(
  requestPromise: Promise<AccountSession>,
): Promise<AccountSession> {
  const startedEpoch = currentAuthSessionEpoch();
  const sequence = ++sessionResponseSequence;
  return requestPromise.then((session) => rememberSession(session, sequence, startedEpoch));
}

export const authApi = {
  status: () => request<AccountStatus>("GET", "/auth/status"),

  init: (input: { code: string; username: string; password: string } & Parameters<typeof envelopesBody>[0]) =>
    rememberedSessionRequest(request<AccountSession>("POST", "/auth/init", { code: input.code, username: input.username, password: input.password, ...envelopesBody(input) }, { client: true })),

  login: (username: string, password: string, deviceId?: string) =>
    request<AccountSession | MfaChallenge>("POST", "/auth/login", {
      username,
      password,
      ...(deviceId ? { device_id: deviceId } : {}),
    }, { client: true }),

  // totpLogin exchanges the login ticket + dynamic code (or a one-time recovery code) for a session; establishes a cookie session like login.
  totpLogin: (ticket: string, code: string) =>
    rememberedSessionRequest(request<AccountSession>("POST", "/auth/totp/login", { ticket, code }, { client: true })),

  totpStatus: () => request<TotpStatus>("GET", "/auth/totp"),

  // totpSetup enables or rebinds TOTP; users already bound must pass reverify (current dynamic code, recovery code, or login password).
  totpSetup: (reverify?: string) =>
    request<TotpSetup>("POST", "/auth/totp/setup", reverify ? { reverify } : {}, { csrf: true }),

  totpConfirm: (code: string) =>
    request<{ recovery_codes: string[] }>("POST", "/auth/totp/confirm", { code }, { csrf: true }),

  totpDisable: (code: string) =>
    request<{ ok: boolean }>("DELETE", "/auth/totp", { code }, { csrf: true }),

  register: (input: { username: string; password: string; displayName: string } & Parameters<typeof envelopesBody>[0]) =>
    rememberedSessionRequest(request<AccountSession>("POST", "/auth/register", {
      username: input.username,
      password: input.password,
      display_name: input.displayName,
      ...envelopesBody(input),
    })),

  me: () => rememberedSessionRequest(request<AccountSession>("GET", "/auth/me")),

  // Handshake under platform-hosted mode (--auth=platform). The front gateway has already authenticated; only the
  // "platform owner" session needs to be exchanged here. Without this, users would see the init/login screens, neither of which should appear on Lazycat.
  platformSession: () => rememberedSessionRequest(request<AccountSession>("POST", "/auth/platform-session", undefined, { client: true })),

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

  deviceRevoke: (id: string) => request<{ ok: boolean }>("DELETE", `/auth/devices/${encodeURIComponent(id)}`, undefined, { csrf: true }),

  sessions: () => request<{ sessions: AccountLoginSession[] }>("GET", "/auth/sessions"),

  sessionRevoke: (id: string) => request<{ ok: boolean }>("DELETE", `/auth/sessions/${encodeURIComponent(id)}`, undefined, { csrf: true }),
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
  ids: (knownHead?: string) =>
    request<SyncIdsResponse>("POST", "/sync/v2/ids", {
      protocol: SYNC_PROTOCOL_VERSION,
      ...(knownHead ? { known_head: knownHead } : {}),
    }),

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

  deleteObject: (knownHead: string, id: string, blobHash: string) =>
    request<SyncDeleteResponse>("POST", "/sync/v2/delete", {
      protocol: SYNC_PROTOCOL_VERSION,
      known_head: knownHead,
      id,
      blob_hash: blobHash,
    }, { csrf: true }),
};

// PreferenceScopeView is the three-layer view of /auth/preferences (defaults + overrides + effective).
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
