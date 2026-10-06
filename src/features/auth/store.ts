// 账号会话与同步 DEK 的内存态。DEK 绝不持久化、绝不出内存;
// 会话只存 HttpOnly cookie(浏览器管理),CSRF 令牌与 DEK 仅内存持有,
// 页面刷新后经 /auth/me 重取 CSRF 令牌,DEK 需重新用密码解锁。

import { create } from "zustand";
import {
  authApi,
  setCsrfToken,
  SESSION_EXPIRED_EVENT,
  type AccountStatus,
  type AccountUser,
} from "../../ipc/authApi";
import {
  base64ToBytes,
  generateDEK,
  generateRecoveryKey,
  recoveryKeyHash,
  unwrapDEKWithPassword,
  wrapDEKWithPassword,
  wrapDEKWithRecovery,
  type DekEnvelopes,
} from "./crypto";
import { DEMO, WEB } from "../../demo";
import { createHttpPreferenceStore, registerAccountPreferenceStore } from "../../app/preferences";
import type { AppError } from "../../ipc/commands";

// syncPreferenceStore 在登录/刷新后注册账号偏好存储(M140 /auth/preferences),登出时注销。
function syncPreferenceStore(loggedIn: boolean): void {
  if (loggedIn && (WEB || DEMO)) {
    registerAccountPreferenceStore(createHttpPreferenceStore());
  } else {
    registerAccountPreferenceStore(null);
  }
}

export type AuthGateKind = "loading" | "ready" | "setup" | "login" | "reset_required";

export interface RecoveryKeyIssue {
  formatted: string;
  canonical: string;
}

interface AuthState {
  status: AccountStatus | null;
  user: AccountUser | null;
  dek: Uint8Array | null;
  gate: AuthGateKind;
  /** 最近一次初始化/注册/改密签发的恢复密钥,展示一次后由界面清除。 */
  pendingRecoveryKey: RecoveryKeyIssue | null;
  error: AppError | null;

  refresh: () => Promise<void>;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  initSuperadmin: (code: string, username: string, password: string) => Promise<void>;
  register: (username: string, password: string, displayName: string) => Promise<void>;
  completeResetRequired: (tempPassword: string, newPassword: string) => Promise<void>;
  changePassword: (oldPassword: string, newPassword: string) => Promise<RecoveryKeyIssue>;
  recoveryReset: (username: string, recoveryKey: string, newPassword: string) => Promise<void>;
  unlockDEK: (password: string) => Promise<void>;
  lockDEK: () => void;
  clearPendingRecoveryKey: () => void;
  clearError: () => void;
}

function zeroize(data: Uint8Array | null): void {
  if (data) data.fill(0);
}

function isNotFoundError(e: unknown): boolean {
  if (e && typeof e === "object" && "code" in e) {
    const code = (e as { code: string }).code;
    const status = (e as { status?: number }).status;
    return code === "not_found" || status === 404;
  }
  return false;
}

function toAppError(e: unknown): AppError {
  if (e && typeof e === "object" && "code" in e) return e as AppError;
  return { code: "internal", message: e instanceof Error ? e.message : String(e) };
}

/** 用口令与(可选)指定 DEK 产出完整信封五元组;dek 缺省时新生成。 */
async function buildEnvelopes(
  password: string,
  dek: Uint8Array,
): Promise<{ fields: DekEnvelopes; recovery: RecoveryKeyIssue }> {
  const canonical = generateRecoveryKey();
  if (DEMO) {
    // 演示模式不做真实 KDF;信封为形状合法的假数据。
    return {
      fields: {
        dekEnvelope: new Uint8Array(60),
        kdfSalt: new Uint8Array(16),
        kdfParams: '{"t":3,"m":65536,"p":4}',
        recoveryEnvelope: new Uint8Array(60),
        recoveryHash: "0".repeat(64),
      },
      recovery: { canonical, formatted: formatGroups(canonical) },
    };
  }
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
  if (DEMO) {
    // 演示模式不做真实 KDF(避免每次登录跑 64MiB argon2);同步数据同样是假数据。
    return new Uint8Array(32).fill(0x5a);
  }
  const view = await authApi.dekGet();
  const r = await unwrapDEKWithPassword(
    password,
    base64ToBytes(view.dek_envelope),
    base64ToBytes(view.kdf_salt),
    view.kdf_params,
  );
  return r;
}

export const useAuth = create<AuthState>((set, get) => ({
  status: null,
  user: null,
  dek: null,
  gate: "loading",
  pendingRecoveryKey: null,
  error: null,

  refresh: async () => {
    if (!WEB && !DEMO) {
      set({ gate: "ready", status: null, user: null });
      return;
    }
    try {
      const status = await authApi.status();
      set({ status });
      if (status.auth === "off") {
        set({ gate: "ready", user: null });
        return;
      }
      if (!status.initialized) {
        set({ gate: "setup", user: null });
        return;
      }
      try {
        const session = await authApi.me();
        set({ user: session.user });
        set({ gate: session.user.state === "reset_required" ? "reset_required" : "ready" });
        syncPreferenceStore(true);
      } catch {
        // loopback 部署允许匿名继续用;auth=on 必须登录
        set({ user: null, gate: status.auth === "on" ? "login" : "ready" });
        syncPreferenceStore(false);
      }
    } catch (e) {
      set({ gate: "ready", error: toAppError(e) });
    }
  },

  login: async (username, password) => {
    set({ error: null });
    try {
      const session = await authApi.login(username, password);
      set({ user: session.user });
      if (session.user.state === "reset_required") {
        set({ gate: "reset_required" });
        return;
      }
      // 管理员创建的用户可能还没有 DEK 信封:用登录密码本地生成并上传(insert-only)。
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
      syncPreferenceStore(true);
    } catch (e) {
      set({ error: toAppError(e) });
      throw e;
    }
  },

  logout: async () => {
    try {
      await authApi.logout();
    } catch {
      // 会话可能已失效;本地状态照样清除
    }
    zeroize(get().dek);
    setCsrfToken(null);
    set({ user: null, dek: null, gate: get().status?.auth === "on" ? "login" : "ready", pendingRecoveryKey: null });
    syncPreferenceStore(false);
  },

  initSuperadmin: async (code, username, password) => {
    set({ error: null });
    const dek = generateDEK();
    try {
      const { fields, recovery } = await buildEnvelopes(password, dek);
      const session = await authApi.init({ code, username, password, ...fields });
      set({ user: session.user, dek, gate: "ready", pendingRecoveryKey: recovery });
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
      set({ user: session.user, dek, gate: "ready", pendingRecoveryKey: recovery });
    } catch (e) {
      zeroize(dek);
      set({ error: toAppError(e) });
      throw e;
    }
  },

  // reset_required(管理员重置):信封已被服务端删除,用新密码签发全新 DEK。
  // changePassword 在事务内校验临时密码并 upsert 信封;不能先 dekUpload(insert-only),否则重试因信封已存在 409 卡死。
  completeResetRequired: async (tempPassword, newPassword) => {
    set({ error: null });
    const dek = generateDEK();
    try {
      const { fields, recovery } = await buildEnvelopes(newPassword, dek);
      await authApi.changePassword({ oldPassword: tempPassword, newPassword, ...fields });
      const session = await authApi.me();
      set({ user: session.user, dek, gate: "ready", pendingRecoveryKey: recovery });
    } catch (e) {
      zeroize(dek);
      set({ error: toAppError(e) });
      throw e;
    }
  },

  // 主动改密:用旧密码解出 DEK,再用新密码与新恢复密钥重包裹;密文不动。
  changePassword: async (oldPassword, newPassword) => {
    set({ error: null });
    try {
      const dek = get().dek ?? (await fetchAndUnwrapDEK(oldPassword));
      const { fields, recovery } = await buildEnvelopes(newPassword, dek);
      await authApi.changePassword({ oldPassword, newPassword, ...fields });
      set({ dek, pendingRecoveryKey: recovery });
      return recovery;
    } catch (e) {
      set({ error: toAppError(e) });
      throw e;
    }
  },

  // 恢复密钥重置(公开路由):面向忘记密码的登出用户。客户端签发全新 DEK,
  // 服务端校验恢复密钥散列后写入新信封并吊销全部会话;旧密文因旧 DEK 不可恢复,
  // 界面须明确提示云端正文需从仍持有数据的设备重新同步。
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
}));

// 会话过期(401)时通知门重新登录;由 main.tsx 的 AuthGate 挂载监听。
if (typeof window !== "undefined") {
  window.addEventListener(SESSION_EXPIRED_EVENT, () => {
    const state = useAuth.getState();
    if (state.user) {
      zeroize(state.dek);
      setCsrfToken(null);
      useAuth.setState({ user: null, dek: null, gate: state.status?.auth === "on" ? "login" : "ready" });
    }
  });
}
