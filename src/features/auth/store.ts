// In-memory state of the account session and sync DEK. The DEK is never persisted and never leaves memory;
// the session stores only the HttpOnly cookie (browser-managed); the CSRF token and DEK are memory-only,
// the CSRF token is refetched via /auth/me after a page refresh, and the DEK must be unlocked again with the password.

import { create } from "zustand";
import {
  authApi,
  AuthApiError,
  isMfaChallenge,
  setCsrfToken,
  MFA_ENROLLMENT_REQUIRED_EVENT,
  SESSION_EXPIRED_EVENT,
  SESSION_IDENTITY_CHANGED_EVENT,
  type AccountSession,
  type AccountStatus,
  type AccountUser,
} from "../../ipc/authApi";
import {
  base64ToBytes,
  bytesToBase64,
  generateDEK,
  generateRecoveryKey,
  recoveryKeyHash,
  unwrapDEKWithPassword,
  wrapDEKWithPassword,
  wrapDEKWithRecovery,
  type DekEnvelopes,
} from "./crypto";
import { DESKTOP, WEB } from "../../ipc/env";
import { currentAuthSessionEpoch } from "../../ipc/authSessionEpoch";
import { createHttpPreferenceStore, registerAccountPreferenceStore } from "../../app/preferences";
import type { AppError } from "../../ipc/commands";
import {
  cleanupDeviceSessions,
  deviceSessionCleanupMessage,
  reportDeviceSessionCleanup,
  type DeviceSessionCleanupFailure,
} from "../fleet/deviceSessionCleanup";

// syncPreferenceStore registers the account preference store (M140 /auth/preferences) after login/refresh, and unregisters on logout.
function syncPreferenceStore(loggedIn: boolean): void {
  if (loggedIn || DESKTOP) {
    registerAccountPreferenceStore(createHttpPreferenceStore());
  } else {
    registerAccountPreferenceStore(null);
  }
}

export type AuthGateKind = "loading" | "ready" | "setup" | "login" | "reset_required" | "mfa_enroll";

export interface RecoveryKeyIssue {
  formatted: string;
  canonical: string;
}

export interface PendingMfa {
  ticket: string;
  username: string;
  password: string;
  deviceId?: string;
}

interface AuthState {
  status: AccountStatus | null;
  user: AccountUser | null;
  dek: Uint8Array | null;
  gate: AuthGateKind;
  /** Recovery key issued by the latest init/register/password change; cleared by the UI after one display. */
  pendingRecoveryKey: RecoveryKeyIssue | null;
  /** Intermediate state after the password passes, awaiting the TOTP second factor; the password stays in memory only and is cleared on completion or cancel. */
  pendingMfa: PendingMfa | null;
  /**
   * Under the mfa_required policy, an unenrolled session is locked by the server to enrollment only;
   * the login password is stashed here, used to decrypt the DEK after enrollment completes, and cleared on completion or logout.
   */
  mfaEnrollmentPassword: string | null;
  error: AppError | null;
  /** Device cleanup evidence retained across account transitions; login error resets do not clear it. */
  cleanupError: AppError | null;

  refresh: () => Promise<void>;
  login: (username: string, password: string, deviceId?: string) => Promise<void>;
  verifyMfa: (code: string) => Promise<void>;
  cancelMfa: () => void;
  finishMfaEnrollment: () => Promise<void>;
  logout: (options?: { all?: boolean }) => Promise<void>;
  initSuperadmin: (code: string, username: string, password: string) => Promise<void>;
  register: (username: string, password: string, displayName: string) => Promise<void>;
  completeResetRequired: (tempPassword: string, newPassword: string) => Promise<void>;
  changePassword: (oldPassword: string, newPassword: string) => Promise<RecoveryKeyIssue>;
  recoveryReset: (username: string, recoveryKey: string, newPassword: string) => Promise<void>;
  unlockDEK: (password: string) => Promise<void>;
  lockDEK: () => void;
  clearPendingRecoveryKey: () => void;
  clearError: () => void;
  clearCleanupError: () => void;
}

function zeroize(data: Uint8Array | null): void {
  if (data) data.fill(0);
}

// The DEK persists with the browser tab session (sessionStorage): unlock auto-restores after a page refresh,
// and it is gone when the tab/browser closes, avoiding a password prompt on every refresh.
const DEK_SESSION_PREFIX = "nexterm.sessionDek.v1.";

function persistDek(userId: string, dek: Uint8Array): void {
  try {
    sessionStorage.setItem(DEK_SESSION_PREFIX + userId, bytesToBase64(dek));
  } catch {
    // Memory-only when sessionStorage is unavailable
  }
}

function restoreDek(userId: string): Uint8Array | null {
  try {
    const raw = sessionStorage.getItem(DEK_SESSION_PREFIX + userId);
    return raw ? base64ToBytes(raw) : null;
  } catch {
    return null;
  }
}

function clearPersistedDek(userId: string): void {
  try {
    sessionStorage.removeItem(DEK_SESSION_PREFIX + userId);
  } catch {
    // Ignore
  }
}

function isNotFoundError(e: unknown): boolean {
  if (e && typeof e === "object" && "code" in e) {
    const code = (e as { code: string }).code;
    const status = (e as { status?: number }).status;
    return code === "not_found" || status === 404;
  }
  return false;
}

function isAccountChangedError(e: unknown): boolean {
  return e instanceof AuthApiError && e.code === "account_changed";
}

function toAppError(e: unknown): AppError {
  if (e && typeof e === "object" && "code" in e) return e as AppError;
  return { code: "internal", message: e instanceof Error ? e.message : String(e) };
}

function logoutFailure(cleanupFailures: DeviceSessionCleanupFailure[], revocationError: unknown): AppError | null {
  const cleanupMessage = cleanupFailures.length > 0 ? deviceSessionCleanupMessage(cleanupFailures) : "";
  if (revocationError) {
    return {
      code: "logout_failed",
      message: `本地已退出登录，但服务端退出请求失败：${toAppError(revocationError).message}${cleanupMessage ? `；${cleanupMessage}` : ""}`,
    };
  }
  if (cleanupMessage) {
    return {
      code: "cleanup_failed",
      message: `已退出登录；${cleanupMessage}。服务端已收到退出请求，并将按原会话补充清理。`,
    };
  }
  return null;
}

/** Build the full five-part envelope set from a password and (optional) given DEK; generates a fresh DEK when omitted. */
async function buildEnvelopes(
  password: string,
  dek: Uint8Array,
): Promise<{ fields: DekEnvelopes; recovery: RecoveryKeyIssue }> {
  const canonical = generateRecoveryKey();
  const { envelope, salt, params } = await wrapDEKWithPassword(password, dek);
  const recoveryEnvelope = await wrapDEKWithRecovery(canonical, dek);
  const hash = await recoveryKeyHash(canonical);
  return {
    fields: {
      dekEnvelope: envelope,
      kdfSalt: salt,
      kdfParams: params,
      recoveryEnvelope,
      recoveryHash: hash,
    },
    recovery: { canonical, formatted: formatGroups(canonical) },
  };
}

function formatGroups(canonical: string): string {
  return canonical.replace(/(.{4})/g, "$1-").replace(/-$/, "");
}

async function fetchAndUnwrapDEK(password: string): Promise<Uint8Array> {
  const view = await authApi.dekGet();
  const r = await unwrapDEKWithPassword(
    password,
    base64ToBytes(view.dek_envelope),
    base64ToBytes(view.kdf_salt),
    view.kdf_params,
  );
  return r;
}

type AuthSet = (partial: Partial<AuthState>) => void;

// finishPasswordLogin is the shared wrap-up after password authentication: record the CSRF token, fetch/init the DEK envelope, and enter the app.
// An unenrolled session under the mfa_required policy enters the forced enrollment gate (the server has already locked the session to enrollment only).
async function finishPasswordLogin(set: AuthSet, session: AccountSession, password: string): Promise<void> {
  setCsrfToken(session.csrf_token);
  set({ user: session.user });
  if (session.user.state === "reset_required") {
    set({ gate: "reset_required" });
    return;
  }
  if (!session.user.mfa_enabled && session.mfa_required) {
    set({ gate: "mfa_enroll", mfaEnrollmentPassword: password });
    return;
  }
  // Users created by an admin may not have a DEK envelope yet: generate one locally with the login password and upload it (insert-only).
  let dek: Uint8Array;
  let recovery: RecoveryKeyIssue | null = null;
  try {
    dek = await fetchAndUnwrapDEK(password);
  } catch (e) {
    if (!isNotFoundError(e)) throw e;
    dek = generateDEK();
    const built = await buildEnvelopes(password, dek);
    await authApi.dekUpload(built.fields);
    recovery = built.recovery;
  }
  set({ dek, gate: "ready", ...(recovery ? { pendingRecoveryKey: recovery } : {}) });
  persistDek(session.user.id, dek);
  syncPreferenceStore(true);
}

export const useAuth = create<AuthState>((set, get) => ({
  status: null,
  user: null,
  dek: null,
  gate: "loading",
  pendingRecoveryKey: null,
  pendingMfa: null,
  mfaEnrollmentPassword: null,
  error: null,
  cleanupError: null,

  refresh: async () => {
    if (!WEB) {
      set({ gate: "ready", status: null, user: null });
      return;
    }
    try {
      const status = await authApi.status();
      set({ status });
      if (status.auth === "off") {
        set({ gate: "ready", user: null });
        syncPreferenceStore(false);
        return;
      }
      if (!status.initialized) {
        // With auth=on, superadmin init must complete first; loopback passwordless deployments allow continuing anonymously,
        // and users who want an account have an explicit init entry in the settings account card.
        set({ user: null, gate: status.auth === "on" ? "setup" : "ready" });
        syncPreferenceStore(false);
        return;
      }
      try {
        const sessionEpoch = currentAuthSessionEpoch();
        const session = await authApi.me();
        if (currentAuthSessionEpoch() !== sessionEpoch) return;
        set({ user: session.user });
        if (session.user.state === "reset_required") {
          set({ gate: "reset_required" });
        } else if (!session.user.mfa_enabled && session.mfa_required) {
          set({ gate: "mfa_enroll" });
        } else {
          const savedDek = restoreDek(session.user.id);
          set({ gate: "ready", ...(savedDek ? { dek: savedDek } : {}) });
        }
        syncPreferenceStore(true);
      } catch (e) {
        if (isAccountChangedError(e)) return;
        // Platform-hosted mode (Lazycat): the gateway has already authenticated via the platform; exchange the "platform owner" session here.
        // Users see neither login nor the init code - opening the app means being the owner.
        if (status.auth === "platform") {
          try {
            const sessionEpoch = currentAuthSessionEpoch();
            const session = await authApi.platformSession();
            if (currentAuthSessionEpoch() !== sessionEpoch) return;
            set({
              user: session.user,
              gate: session.user.state === "reset_required" ? "reset_required" : "ready",
            });
            syncPreferenceStore(true);
            return;
          } catch (e) {
            if (isAccountChangedError(e)) return;
            // Gateway header missing etc. (e.g. direct port access instead of the platform entry): fall back to anonymous availability; the account card explains why.
          }
        }
        // Loopback deployments allow continuing anonymously; auth=on requires login
        set({ user: null, gate: status.auth === "on" ? "login" : "ready" });
        syncPreferenceStore(false);
      }
    } catch (e) {
      if (isAccountChangedError(e)) return;
      // With auth=off, /auth/status is also 403'd by accountGuard (the message text is a server contract);
      // normalize to the auth=off state so the UI shows "account features disabled" instead of a dead login entry.
      if (e instanceof AuthApiError && e.status === 403 && e.message.includes("--auth=off")) {
        set({ status: { initialized: false, registration_open: false, auth: "off" }, user: null, gate: "ready", error: null });
        syncPreferenceStore(false);
        return;
      }
      set({ gate: "ready", error: toAppError(e) });
    }
  },

  login: async (username, password, deviceId) => {
    if (get().user) await get().logout();
    set({ error: null });
    try {
      const result = await authApi.login(username, password, deviceId);
      if (isMfaChallenge(result)) {
        set({
          pendingMfa: { ticket: result.ticket, username, password, ...(deviceId ? { deviceId } : {}) },
          error: get().cleanupError,
        });
        return;
      }
      await finishPasswordLogin(set, result, password);
      set({ error: get().cleanupError });
    } catch (e) {
      set({ error: toAppError(e) });
      throw e;
    }
  },

  // verifyMfa completes the second step with a dynamic code or recovery code; the success path mirrors password login (decrypt DEK, enter the gate).
  verifyMfa: async (code) => {
    const pending = get().pendingMfa;
    if (!pending) return;
    set({ error: null });
    try {
      const session = await authApi.totpLogin(pending.ticket, code);
      await finishPasswordLogin(set, session, pending.password);
      set({ pendingMfa: null, error: get().cleanupError });
    } catch (e) {
      set({ error: toAppError(e) });
      throw e;
    }
  },

  cancelMfa: () => set({ pendingMfa: null, error: null }),

  // finishMfaEnrollment enters the app after forced enrollment completes: refresh the session (now bound), and decrypt the DEK when a stashed password exists.
  finishMfaEnrollment: async () => {
    set({ error: null });
    try {
      const session = await authApi.me();
      const password = get().mfaEnrollmentPassword;
      if (password) {
        await finishPasswordLogin(set, session, password);
      } else {
        setCsrfToken(session.csrf_token);
        set({ user: session.user, gate: "ready" });
        syncPreferenceStore(true);
      }
      set({ mfaEnrollmentPassword: null, error: get().cleanupError });
    } catch (e) {
      set({ error: toAppError(e) });
      throw e;
    }
  },

  logout: async (options) => {
    const logoutEpoch = currentAuthSessionEpoch();
    const logoutUserId = get().user?.id;
    const logoutDek = get().dek;
    const cleanupFailures = WEB ? await cleanupDeviceSessions() : [];
    if (currentAuthSessionEpoch() !== logoutEpoch) return;
    let revocationError: unknown = null;
    try {
      if (options?.all) await authApi.logoutAll();
      else await authApi.logout();
    } catch (e) {
      revocationError = e;
    }
    if (currentAuthSessionEpoch() !== logoutEpoch) return;
    if (logoutUserId) clearPersistedDek(logoutUserId);
    zeroize(logoutDek);
    setCsrfToken(null);
    const error = logoutFailure(cleanupFailures, revocationError);
    set({
      user: null,
      dek: null,
      gate: get().status?.auth === "on" ? "login" : "ready",
      pendingRecoveryKey: null,
      pendingMfa: null,
      mfaEnrollmentPassword: null,
      error,
      cleanupError: cleanupFailures.length > 0 ? error : null,
    });
    reportDeviceSessionCleanup(cleanupFailures);
    syncPreferenceStore(false);
  },

  initSuperadmin: async (code, username, password) => {
    set({ error: null });
    const dek = generateDEK();
    try {
      const { fields, recovery } = await buildEnvelopes(password, dek);
      const session = await authApi.init({ code, username, password, ...fields });
      const status = get().status;
      set({
        user: session.user,
        dek,
        pendingRecoveryKey: recovery,
        ...(status ? { status: { ...status, initialized: true } } : {}),
      });
      if (!session.user.mfa_enabled && session.mfa_required) {
        set({ gate: "mfa_enroll", mfaEnrollmentPassword: password });
      } else {
        set({ gate: "ready" });
      }
      syncPreferenceStore(true);
    } catch (e) {
      zeroize(dek);
      set({ error: toAppError(e) });
      throw e;
    }
  },

  register: async (username, password, displayName) => {
    set({ error: null });
    const dek = generateDEK();
    try {
      const { fields, recovery } = await buildEnvelopes(password, dek);
      const session = await authApi.register({ username, password, displayName, ...fields });
      set({ user: session.user, dek, pendingRecoveryKey: recovery });
      if (!session.user.mfa_enabled && session.mfa_required) {
        set({ gate: "mfa_enroll", mfaEnrollmentPassword: password });
      } else {
        set({ gate: "ready" });
      }
      syncPreferenceStore(true);
    } catch (e) {
      zeroize(dek);
      set({ error: toAppError(e) });
      throw e;
    }
  },

  // reset_required (admin reset): the envelope was deleted by the server; issue a brand-new DEK with the new password.
  // changePassword validates the temporary password and upserts the envelope in one transaction; do not dekUpload first (insert-only), or a retry deadlocks on 409 because the envelope already exists.
  completeResetRequired: async (tempPassword, newPassword) => {
    set({ error: null });
    const dek = generateDEK();
    try {
      const { fields, recovery } = await buildEnvelopes(newPassword, dek);
      await authApi.changePassword({ oldPassword: tempPassword, newPassword, ...fields });
      const session = await authApi.me();
      set({ user: session.user, dek, pendingRecoveryKey: recovery });
      if (!session.user.mfa_enabled && session.mfa_required) {
        set({ gate: "mfa_enroll", mfaEnrollmentPassword: newPassword });
      } else {
        set({ gate: "ready" });
      }
      syncPreferenceStore(true);
    } catch (e) {
      zeroize(dek);
      set({ error: toAppError(e) });
      throw e;
    }
  },

  // Proactive password change: decrypt the DEK with the old password, re-wrap it with the new password and a new recovery key; ciphertext untouched.
  changePassword: async (oldPassword, newPassword) => {
    set({ error: null });
    try {
      const dek = get().dek ?? (await fetchAndUnwrapDEK(oldPassword));
      const { fields, recovery } = await buildEnvelopes(newPassword, dek);
      await authApi.changePassword({ oldPassword, newPassword, ...fields });
      set({ dek, pendingRecoveryKey: recovery });
      const userId = get().user?.id;
      if (userId) persistDek(userId, dek);
      return recovery;
    } catch (e) {
      set({ error: toAppError(e) });
      throw e;
    }
  },

  // Recovery key reset (public route): for logged-out users who forgot the password. The client issues a brand-new DEK,
  // the server verifies the recovery key hash, writes the new envelope and revokes all sessions; old ciphertext is unrecoverable with the old DEK,
  // so the UI must clearly state that cloud content needs to re-sync from a device that still holds the data.
  recoveryReset: async (username, recoveryKey, newPassword) => {
    set({ error: null });
    const dek = generateDEK();
    try {
      const { fields, recovery } = await buildEnvelopes(newPassword, dek);
      await authApi.recoveryReset({ username, recoveryKey, newPassword, ...fields });
      await get().login(username, newPassword);
      set({ pendingRecoveryKey: recovery });
    } catch (e) {
      zeroize(dek);
      set({ error: toAppError(e) });
      throw e;
    }
  },

  unlockDEK: async (password) => {
    set({ error: null });
    try {
      const dek = await fetchAndUnwrapDEK(password);
      set({ dek });
      const userId = get().user?.id;
      if (userId) persistDek(userId, dek);
    } catch (e) {
      set({ error: toAppError(e) });
      throw e;
    }
  },

  lockDEK: () => {
    zeroize(get().dek);
    set({ dek: null });
  },

  clearPendingRecoveryKey: () => set({ pendingRecoveryKey: null }),
  clearError: () => set({ error: null }),
  clearCleanupError: () => {
    const cleanupError = get().cleanupError;
    set({ cleanupError: null, ...(get().error === cleanupError ? { error: null } : {}) });
  },
}));

// On session expiry (401), notify the gate to re-login; mounted and listened to by AuthGate in main.tsx.
if (typeof window !== "undefined") {
  window.addEventListener(SESSION_IDENTITY_CHANGED_EVENT, (event) => {
    const session = (event as CustomEvent<AccountSession>).detail;
    if (!session?.user) return;
    const state = useAuth.getState();
    if (!state.user || state.user.id === session.user.id) return;
    clearPersistedDek(state.user.id);
    zeroize(state.dek);
    const nextGate = session.user.state === "reset_required"
      ? "reset_required"
      : (!session.user.mfa_enabled && session.mfa_required ? "mfa_enroll" : "ready");
    useAuth.setState({
      user: session.user,
      dek: restoreDek(session.user.id),
      gate: nextGate,
      pendingRecoveryKey: null,
      pendingMfa: null,
      mfaEnrollmentPassword: null,
      error: state.cleanupError,
    });
    syncPreferenceStore(true);
  });
  window.addEventListener(SESSION_EXPIRED_EVENT, () => {
    const state = useAuth.getState();
    if (state.user) {
      clearPersistedDek(state.user.id);
      zeroize(state.dek);
      setCsrfToken(null);
      useAuth.setState({ user: null, dek: null, gate: state.status?.auth === "on" ? "login" : "ready", pendingMfa: null, mfaEnrollmentPassword: null });
      syncPreferenceStore(false);
    }
  });
  // The server rejects with mfa_enrollment_required (e.g. policy enabled mid-session): a logged-in unenrolled session switches to the forced enrollment gate.
  window.addEventListener(MFA_ENROLLMENT_REQUIRED_EVENT, () => {
    const state = useAuth.getState();
    if (state.user && !state.user.mfa_enabled) {
      useAuth.setState({ gate: "mfa_enroll" });
    }
  });
}
