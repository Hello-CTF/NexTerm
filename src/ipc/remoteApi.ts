// Desktop bindings that call remote APIs through a logged-in server account: method signatures mirror the WEB
// fleetApi.ts / sharingApi.ts / authApi.ts counterparts with the same return types; the view layer switches on TRANSPORT.
// Commands are registered in internal/app/production/remote_ipc.go; the session and 401 re-login are supplied by
// the RemoteProxy facade in internal/sync (cookie+CSRF held in desktop memory).

import { call } from "./commands";
import type { AccountLink } from "./types";
import type { FleetBaseURLEntry, FleetDevice, FleetEnrollCode, FleetMetricsResponse } from "./fleetApi";
import type { HostShareView, ShareLinkView } from "./sharingApi";
import type { AccountDevice, AccountUser } from "./authApi";

// RemoteLinkResult is the return of sync_link_set / sync_link_mfa / sync_link_reset_password:
// beyond the AccountLink view it carries the three-state connect status; the MFA branch carries a ticket, forced password change completion carries a one-time recovery key.
export interface RemoteLinkResult extends AccountLink {
  connect: "" | "connected" | "mfa_required" | "reset_required";
  mfaTicket?: string;
  mfaExpiresAt?: number;
  recoveryKey?: string;
}

// remoteLinkApi is the entry to the account link login flow: linkSet saves and probes the login,
// mfa / resetPassword complete two-factor verification and forced password change onboarding (cookie+CSRF held in memory once the session is established).
export const remoteLinkApi = {
  linkSet: (patch: { url?: string; username?: string; password?: string; insecure?: boolean }) =>
    call<RemoteLinkResult>("sync_link_set", { args: patch }),

  mfa: (code: string) => call<RemoteLinkResult>("sync_link_mfa", { args: { code } }),

  resetPassword: (newPassword: string) =>
    call<RemoteLinkResult>("sync_link_reset_password", { args: { newPassword } }),
};

// SyncCompareRow is a per-row comparison of sync_compare: state values match the web compare detail;
// remote-undecryptable means the cloud object cannot be decrypted with the current data key (shown as-is, not an error).
export interface SyncCompareRow {
  id: string;
  kind: string;
  name: string;
  state: "local-only" | "remote-only" | "same" | "local-newer" | "remote-newer" |
    "local-deleted" | "remote-deleted" | "remote-undecryptable";
  updatedAtLocal?: number;
  updatedAtRemote?: number;
  remoteSeq?: number;
  remoteBlobHash?: string;
  localPayloadHash?: string;
}

export interface SyncCompareResult {
  rows: SyncCompareRow[];
  warnings?: string[];
  remote: number;
  userId: string;
  compareToken: string;
}

export interface SyncRepairUndecryptableRequest {
  id: string;
  action: "restore-local" | "clear-cloud";
  userId: string;
  compareToken: string;
  remoteSeq: number;
  remoteBlobHash: string;
  localKind?: string;
  localPayloadHash?: string;
}

// syncCompareApi is the desktop account sync per-row comparison entry: the Go side decrypts cloud objects with the engine session DEK,
// then judges states against the local collection using the same LWW rules as the web (see internal/sync/compare.go).
export const syncCompareApi = {
  compare: () => call<SyncCompareResult>("sync_compare"),
  repairUndecryptable: (args: SyncRepairUndecryptableRequest) =>
    call<{ repaired: boolean }>("sync_repair_undecryptable", { args }),
};

// RemotePreviewLinkView mirrors PreviewLinkView in src/features/sharing/previewApi.ts
// (declared independently here to avoid the ipc layer depending on the features layer).
export interface RemotePreviewLinkView {
  id: string;
  owner_id: string;
  session_id: string;
  created_at: number;
  expires_at: number;
  revoked_at?: number;
  last_accessed_at?: number;
}

// RemoteShareLinkCreateResult is the create-public-link response: the only entry that carries the one-time token;
// the token is used only to assemble the public URL on the spot - never logged, persisted, or put into web storage.
export interface RemoteShareLinkCreateResult extends ShareLinkView {
  token: string;
}

// RemotePreviewCreateResult is the return of preview_share_create: the desktop relay has already assembled the public url
// from the account link's server address; the token is held only at creation time, never persisted; sessionId is the remote shell id,
// which the "active previews" list (remoteSharingApi.previewLinks) uses to filter the current tab's link.
export interface RemotePreviewCreateResult {
  token: string;
  url: string;
  sessionId: string;
}

// remotePreviewApi is the desktop read-only preview relay entry: create builds a remote shell + preview link and starts
// output relaying (one active relay per tab; repeated creates return the existing link); active queries the shell id of the tab's
// current relay. List/revoke reuse remoteSharingApi.previewLinks / revokePreviewLink.
export const remotePreviewApi = {
  create: (tabId: string, ttlMs: number) =>
    call<RemotePreviewCreateResult>("preview_share_create", { args: { tabId, ttlMs } }),

  active: (tabId: string) => call<{ sessionId?: string }>("preview_share_active", { args: { tabId } }),
};

export const remoteFleetApi = {
  devices: () => call<{ devices: FleetDevice[] }>("remote_fleet_devices"),

  issueEnrollCode: (ttlMs: number, userId?: string) =>
    call<FleetEnrollCode>("remote_fleet_enroll_code", { args: { ttlMs, ...(userId ? { userId } : {}) } }),

  revoke: (id: string) => call<{ ok: boolean }>("remote_fleet_device_revoke", { args: { deviceId: id } }),

  setAutostart: (id: string, desired: boolean) =>
    call<{ ok: boolean; desired_autostart: boolean }>("remote_fleet_device_autostart", { args: { deviceId: id, desired } }),

  metrics: (id: string, sinceMs?: number) =>
    call<FleetMetricsResponse>("remote_fleet_device_metrics", {
      args: { deviceId: id, ...(sinceMs ? { sinceMs } : {}) },
    }),

  baseUrls: () => call<{ base_urls: FleetBaseURLEntry[] }>("remote_fleet_base_urls_get"),

  putBaseUrls: (entries: FleetBaseURLEntry[]) =>
    call<{ base_urls: FleetBaseURLEntry[] }>("remote_fleet_base_urls_put", { args: { entries } }),
};

export const remoteSharingApi = {
  hostShares: () => call<{ shares: HostShareView[] }>("remote_share_host_list"),

  createHostShare: (input: { deviceId: string; recipientUsername: string; write: boolean; ttlMs: number }) =>
    call<HostShareView>("remote_share_host_create", { args: input }),

  revokeHostShare: (id: string) => call<{ ok: boolean }>("remote_share_host_revoke", { args: { id } }),

  links: () => call<{ links: ShareLinkView[] }>("remote_share_link_list"),

  createLink: (input: { deviceId: string; sessionId: string; write: boolean; ttlMs: number }) =>
    call<RemoteShareLinkCreateResult>("remote_share_link_create", { args: input }),

  revokeLink: (id: string) => call<{ ok: boolean }>("remote_share_link_revoke", { args: { id } }),

  previewLinks: () => call<{ links: RemotePreviewLinkView[] }>("remote_share_preview_list"),

  revokePreviewLink: (id: string) => call<{ ok: boolean }>("remote_share_preview_revoke", { args: { id } }),
};

export const remoteAccountApi = {
  me: () => call<{ user: AccountUser; mfa_required: boolean }>("remote_account_me"),

  devices: () => call<{ devices: AccountDevice[] }>("remote_account_devices"),

  deviceRevoke: (id: string) => call<{ ok: boolean }>("remote_account_device_revoke", { args: { deviceId: id } }),

  adminUsers: () => call<{ users: AccountUser[] }>("remote_account_admin_users"),

  adminCreateUser: (username: string, password: string, displayName: string) =>
    call<{ user: AccountUser }>("remote_account_admin_create_user", { args: { username, password, displayName } }),

  // state supports only disabled and reset_required, matching the server's only two state actions.
  adminSetUserState: (userId: string, state: "disabled" | "reset_required") =>
    call<{ ok: boolean }>("remote_account_admin_set_user_state", { args: { userId, state } }),

  // changePassword returns a one-time recovery key (issued only in this response); the view layer must show it immediately.
  changePassword: (oldPassword: string, newPassword: string) =>
    call<{ ok: boolean; recoveryKey?: string }>("remote_account_change_password", { args: { oldPassword, newPassword } }),
};

export const remoteHealthApi = {
  // serverVersion reads the real server version from /healthz. An empty string means the version is unavailable:
  // callers must hide version-dependent UI, never fabricate a version.
  serverVersion: () => call<string>("remote_healthz"),
};
