import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { WEB } from "../ipc/env";
import { useAuth } from "../features/auth/store";
import { useUi } from "./store";
import { describeError } from "../ui/errorText";
import { IconLock, IconXCircle } from "../ui/icons";
import { biometricApi, systemApi, vaultApi } from "../ipc/commands";

export function DekUnlockBanner() {
  if (WEB) return <WebDekUnlockBanner />;
  return <NativeVaultUnlockBanner />;
}

// After a browser refresh, the session is restored via cookie but the DEK is gone with the cleared memory, so auto sync stalls silently;
// this persistent banner is the main-UI unlock entry besides the settings SyncCard, and stays until unlocked or dismissed.
function WebDekUnlockBanner() {
  const user = useAuth((s) => s.user);
  const dek = useAuth((s) => s.dek);
  const gate = useAuth((s) => s.gate);
  const unlockDEK = useAuth((s) => s.unlockDEK);
  const [dismissedFor, setDismissedFor] = useState<string | null>(null);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (gate !== "ready" || !user || dek !== null || dismissedFor === user.id) return null;

  const submit = async () => {
    if (busy || !password) return;
    setError(null);
    setBusy(true);
    try {
      await unlockDEK(password);
      setPassword("");
      useUi.getState().pushToast("success", "已解锁，正在恢复同步");
    } catch (e) {
      setError(describeError(e));
      setPassword("");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      className="flex h-[34px] shrink-0 items-center gap-2 border-b border-amber-500/40 bg-amber-950 px-3 text-xs text-amber-100"
      role="region"
      aria-label="数据密钥未解锁"
    >
      <IconLock size={13} className="shrink-0 text-amber-300" />
      <span className="min-w-0 truncate">
        数据密钥未解锁，同步已暂停；输入账号密码即可恢复同步与加密内容
      </span>
      <div className="nx-spacer" />
      {error && (
        <span className="flex min-w-0 shrink items-center gap-1 text-red-300" role="alert">
          <IconXCircle size={12} className="shrink-0" />
          <span className="truncate" title={error}>
            {error}
          </span>
        </span>
      )}
      <input
        className="nx-input nx-input-sm w-40 shrink-0"
        type="password"
        placeholder="账号密码"
        aria-label="账号密码"
        autoComplete="current-password"
        value={password}
        disabled={busy}
        onChange={(e) => setPassword(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") void submit();
        }}
      />
      <button
        type="button"
        className="nx-btn nx-btn-primary nx-btn-sm shrink-0"
        disabled={busy || !password}
        onClick={() => void submit()}
      >
        {busy ? "解锁中…" : "解锁"}
      </button>
      <button
        type="button"
        className="nx-btn nx-btn-outline nx-btn-sm shrink-0"
        onClick={() => setDismissedFor(user.id)}
      >
        稍后
      </button>
    </div>
  );
}

// Native (desktop and mobile) vault-locked banner: the vault master password protects sync link secrets and credentials; locked means sync stalls;
// after a successful unlock, the existing fireUnlocked → wake chain resumes sync automatically.
// Biometric quick unlock is iOS/Android only: the unlock material is that master password (iOS stores it in the Keychain bound to the current Face ID set,
// Android in Keystore-encrypted EncryptedSharedPreferences), verification goes through the system biometric prompt,
// and success still takes the existing vault_unlock path; cancel/failure keeps the password input. Desktop has only the password unlock branch.
const DONT_ASK_KEY = "nexterm.biometricUnlock.dontAsk";

function dontAskBiometricAgain(): boolean {
  try {
    return window.localStorage.getItem(DONT_ASK_KEY) === "1";
  } catch {
    return false;
  }
}

function NativeVaultUnlockBanner() {
  const qc = useQueryClient();
  const { pushToast } = useUi();
  const platform = useQuery({ queryKey: ["app-platform"], queryFn: () => systemApi.platform() });
  const mobile = platform.data === "ios" || platform.data === "android";
  const supported =
    mobile || platform.data === "darwin" || platform.data === "windows" || platform.data === "linux";
  const bioLabel = platform.data === "ios" ? "面容 ID" : "生物识别";
  const vault = useQuery({ queryKey: ["vault-status"], queryFn: () => vaultApi.status(), enabled: supported });
  const bio = useQuery({
    queryKey: ["biometric-status"],
    queryFn: () => biometricApi.status(),
    enabled: mobile,
  });

  const [dismissed, setDismissed] = useState(false);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [offerEnable, setOfferEnable] = useState<string | null>(null);
  const [confirmDisable, setConfirmDisable] = useState(false);

  const st = vault.data;
  const locked = !!st?.initialized && !st.unlocked && st.mode === "master" && !st.passwordless;
  if (!supported || dismissed) return null;
  if (!locked && offerEnable === null) return null;

  const refreshUnlocked = async () => {
    await qc.invalidateQueries({ queryKey: ["vault-status"] });
    void qc.invalidateQueries({ queryKey: ["credentials"] });
  };

  const submit = async () => {
    if (busy || !password) return;
    const pw = password;
    setError(null);
    setNotice(null);
    setBusy(true);
    try {
      await vaultApi.unlock(pw);
      setPassword("");
      await refreshUnlocked();
      const b = bio.data;
      if (b?.canEvaluate && !b.hasMaterial && !dontAskBiometricAgain()) {
        setOfferEnable(pw);
      } else {
        pushToast("success", "已解锁，正在恢复同步");
      }
    } catch (e) {
      setError(describeError(e));
      setPassword("");
    } finally {
      setBusy(false);
    }
  };

  const faceUnlock = async () => {
    if (busy) return;
    setError(null);
    setNotice(null);
    setBusy(true);
    try {
      const r = await biometricApi.unlock();
      if (!r.ok || !r.password) {
        setNotice(r.message ?? "验证未通过，请使用密码解锁");
        return;
      }
      await vaultApi.unlock(r.password);
      await refreshUnlocked();
      pushToast("success", "已解锁，正在恢复同步");
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(false);
    }
  };

  const enableBiometric = async () => {
    if (busy || offerEnable === null) return;
    setBusy(true);
    setError(null);
    try {
      await biometricApi.enable(offerEnable);
      setOfferEnable(null);
      void qc.invalidateQueries({ queryKey: ["biometric-status"] });
      pushToast("success", `已启用${bioLabel}快捷解锁`);
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(false);
    }
  };

  const disableBiometric = async () => {
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      await biometricApi.disable();
      setConfirmDisable(false);
      void qc.invalidateQueries({ queryKey: ["biometric-status"] });
      pushToast("success", `已关闭${bioLabel}快捷解锁`);
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(false);
    }
  };

  if (offerEnable !== null) {
    return (
      <div
        className="flex h-[34px] shrink-0 items-center gap-2 border-b border-amber-500/40 bg-amber-950 px-3 text-xs text-amber-100"
        role="region"
        aria-label={`启用${bioLabel}快捷解锁`}
      >
        <IconLock size={13} className="shrink-0 text-amber-300" />
        <span className="min-w-0 truncate">已解锁。启用{bioLabel}快捷解锁？验证通过后即可免输密码</span>
        <div className="nx-spacer" />
        {error && (
          <span className="flex min-w-0 shrink items-center gap-1 text-red-300" role="alert">
            <IconXCircle size={12} className="shrink-0" />
            <span className="truncate" title={error}>
              {error}
            </span>
          </span>
        )}
        <button
          type="button"
          className="nx-btn nx-btn-primary nx-btn-sm shrink-0"
          disabled={busy}
          onClick={() => void enableBiometric()}
        >
          启用{bioLabel}
        </button>
        <button
          type="button"
          className="nx-btn nx-btn-outline nx-btn-sm shrink-0"
          disabled={busy}
          onClick={() => setOfferEnable(null)}
        >
          暂不
        </button>
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
          disabled={busy}
          onClick={() => {
            try {
              window.localStorage.setItem(DONT_ASK_KEY, "1");
            } catch {
              // Ignore
            }
            setOfferEnable(null);
          }}
        >
          不再提示
        </button>
      </div>
    );
  }

  const canFaceUnlock = !!bio.data?.canEvaluate && !!bio.data?.hasMaterial;

  return (
    <div
      className="flex h-[34px] shrink-0 items-center gap-2 border-b border-amber-500/40 bg-amber-950 px-3 text-xs text-amber-100"
      role="region"
      aria-label="凭据库已锁定"
    >
      <IconLock size={13} className="shrink-0 text-amber-300" />
      <span className="min-w-0 truncate">
        {mobile
          ? `数据密钥未解锁，同步已暂停；输入主密码或使用${bioLabel}解锁`
          : "数据密钥未解锁，同步已暂停；输入主密码即可恢复同步与加密内容"}
      </span>
      <div className="nx-spacer" />
      {error && (
        <span className="flex min-w-0 shrink items-center gap-1 text-red-300" role="alert">
          <IconXCircle size={12} className="shrink-0" />
          <span className="truncate" title={error}>
            {error}
          </span>
        </span>
      )}
      {notice && !error && (
        <span className="min-w-0 shrink truncate text-amber-200" role="status">
          {notice}
        </span>
      )}
      {canFaceUnlock &&
        (confirmDisable ? (
          <>
            <span className="min-w-0 shrink truncate text-amber-200">关闭后下次需输入主密码解锁</span>
            <button
              type="button"
              className="nx-btn nx-btn-outline nx-btn-sm shrink-0"
              disabled={busy}
              onClick={() => void disableBiometric()}
            >
              确认关闭
            </button>
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
              disabled={busy}
              onClick={() => setConfirmDisable(false)}
            >
              取消
            </button>
          </>
        ) : (
          <>
            <button
              type="button"
              className="nx-btn nx-btn-primary nx-btn-sm shrink-0"
              disabled={busy}
              onClick={() => void faceUnlock()}
            >
              {busy ? "验证中…" : `用${bioLabel}解锁`}
            </button>
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
              disabled={busy}
              title="删除本机保存的解锁材料"
              onClick={() => setConfirmDisable(true)}
            >
              关闭{bioLabel}
            </button>
          </>
        ))}
      <input
        className="nx-input nx-input-sm w-40 shrink-0"
        type="password"
        placeholder="主密码"
        aria-label="主密码"
        autoComplete="current-password"
        value={password}
        disabled={busy}
        onChange={(e) => setPassword(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") void submit();
        }}
      />
      <button
        type="button"
        className="nx-btn nx-btn-outline nx-btn-sm shrink-0"
        disabled={busy || !password}
        onClick={() => void submit()}
      >
        解锁
      </button>
      <button
        type="button"
        className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
        disabled={busy}
        onClick={() => setDismissed(true)}
      >
        稍后
      </button>
    </div>
  );
}
