// Desktop bindings that call remote APIs through a logged-in server account: method signatures mirror the WEB
// fleetApi.ts / sharingApi.ts / authApi.ts counterparts with the same return types; the view layer switches on TRANSPORT.
// Commands are registered in internal/app/production/remote_ipc.go; the session and 401 re-login are supplied by
// the RemoteProxy facade in internal/sync (cookie+CSRF held in desktop memory).

import { call } from "./commands";
import type { RemoteHostShareIdentity, RemoteMetricsRequest } from "./generated/types";
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
    call("sync_link_set", {
      args: patch as unknown as import("./generated/types").LinkPatch,
    }) as Promise<RemoteLinkResult>,

  mfa: (code: string) =>
    call("sync_link_mfa", { args: { code } }) as Promise<RemoteLinkResult>,

  resetPassword: (newPassword: string) =>
    call("sync_link_reset_password", { args: { newPassword } }) as Promise<RemoteLinkResult>,
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
  compare: () => call("sync_compare") as Promise<SyncCompareResult>,
  repairUndecryptable: (args: SyncRepairUndecryptableRequest) =>
    call("sync_repair_undecryptable", { args }) as Promise<{ repaired: boolean }>,
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
    call("preview_share_create", { args: { tabId, ttlMs } }) as Promise<RemotePreviewCreateResult>,

  active: (tabId: string) =>
    call("preview_share_active", { args: { tabId } }) as Promise<{ sessionId?: string }>,
};

export const remoteFleetApi = {
  devices: () => call("remote_fleet_devices") as Promise<{ devices: FleetDevice[] }>,

  issueEnrollCode: (ttlMs: number, userId?: string) =>
    call("remote_fleet_enroll_code", { args: { ttlMs, ...(userId ? { userId } : {}) } }) as Promise<FleetEnrollCode>,

  revoke: (id: string) =>
    call("remote_fleet_device_revoke", { args: { deviceId: id } }) as Promise<{ ok: boolean }>,

  setAutostart: (id: string, desired: boolean) =>
    call("remote_fleet_device_autostart", { args: { deviceId: id, desired } }) as Promise<{ ok: boolean; desired_autostart: boolean }>,

  metrics: (id: string, sinceMs?: number) =>
    call("remote_fleet_device_metrics", {
      args: { deviceId: id, ...(sinceMs ? { sinceMs } : {}) } as RemoteMetricsRequest,
    }) as Promise<FleetMetricsResponse>,

  baseUrls: () => call("remote_fleet_base_urls_get") as Promise<{ base_urls: FleetBaseURLEntry[] }>,

  putBaseUrls: (entries: FleetBaseURLEntry[]) =>
    call("remote_fleet_base_urls_put", { args: { entries } }) as Promise<{ base_urls: FleetBaseURLEntry[] }>,
};

export const remoteSharingApi = {
  hostShares: () => call("remote_share_host_list") as Promise<{ shares: HostShareView[] }>,

  createHostShare: (input: { deviceId: string; recipientUsername: string; write: boolean; ttlMs: number }) =>
    call("remote_share_host_create", { args: input }) as Promise<HostShareView>,

  revokeHostShare: (id: string) =>
    call("remote_share_host_revoke", { args: { id } }) as Promise<{ ok: boolean }>,

  links: () => call("remote_share_link_list") as Promise<{ links: ShareLinkView[] }>,

  revokeLink: (id: string) =>
    call("remote_share_link_revoke", { args: { id } }) as Promise<{ ok: boolean }>,

  previewLinks: () =>
    call("remote_share_preview_list") as Promise<{ links: RemotePreviewLinkView[] }>,

  revokePreviewLink: (id: string) =>
    call("remote_share_preview_revoke", { args: { id } }) as Promise<{ ok: boolean }>,
};

export const remoteAccountApi = {
  me: () => call("remote_account_me") as Promise<{ user: AccountUser; mfa_required: boolean }>,

  devices: () => call("remote_account_devices") as Promise<{ devices: AccountDevice[] }>,

  deviceRevoke: (id: string) =>
    call("remote_account_device_revoke", { args: { deviceId: id } }) as Promise<{ ok: boolean }>,

  adminUsers: () => call("remote_account_admin_users") as Promise<{ users: AccountUser[] }>,

  adminCreateUser: (username: string, password: string, displayName: string) =>
    call("remote_account_admin_create_user", { args: { username, password, displayName } }) as Promise<{ user: AccountUser }>,

  // state supports only disabled and reset_required, matching the server's only two state actions.
  adminSetUserState: (userId: string, state: "disabled" | "reset_required") =>
    call("remote_account_admin_set_user_state", { args: { userId, state } }) as Promise<{ ok: boolean }>,

  // changePassword returns a one-time recovery key (issued only in this response); the view layer must show it immediately.
  changePassword: (oldPassword: string, newPassword: string) =>
    call("remote_account_change_password", { args: { oldPassword, newPassword } }) as Promise<{ ok: boolean; recoveryKey?: string }>,
};

export const remoteHealthApi = {
  // serverVersion reads the real server version from /healthz. An empty string means the version is unavailable:
  // callers must hide version-dependent UI, never fabricate a version.
  serverVersion: () => call("remote_healthz") as Promise<string>,
};

export type { RemoteHostShareIdentity };

export const remoteHostShareApi = {
  identity: async (): Promise<RemoteHostShareIdentity> => {
    const identity = await call("host_share_identity");
    if (identity === null) throw new Error("主机分享身份信息不可用");
    return identity;
  },

  open: (deviceId: string, streamId: string, channel: { toJSON: () => string | null }) =>
    call("host_share_terminal_open", { args: { deviceId, streamId }, channel }),

  input: (streamId: string, data: Uint8Array) =>
    call("host_share_terminal_input", { args: { streamId, data: Array.from(data) } }),

  close: (streamId: string) => call("host_share_terminal_close", { args: { streamId } }),
};
