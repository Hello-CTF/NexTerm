import { vaultApi } from "../ipc/commands";
import { WEB } from "../ipc/env";
import { useAuth } from "../features/auth/store";
import { describeError } from "../ui/errorText";
import { useUi } from "./store";
import { isAppLocked, lockApp, subscribeAppLock, subscribeAppLockIdle, appLockIdleMinutes, unlockApp, type AppLockMethod } from "./appLockState";
import { t } from "../i18n";

export * from "./appLockState";

/** The unlock method available now, or null with the reason none is. */
export async function resolveAppLockMethod(): Promise<{ method: AppLockMethod | null; reason: string }> {
  if (WEB) {
    const auth = useAuth.getState();
    if (auth.user && auth.gate === "ready") return { method: "account", reason: "" };
    return { method: null, reason: t("未登录服务端账号，无法验证解锁密码，因此不能锁定 NexTerm") };
  }
  const vault = await vaultApi.status();
  if (vault.initialized && vault.mode === "master" && !vault.passwordless && !vault.managed) {
    return { method: "vault", reason: "" };
  }
  return { method: null, reason: t("请先在「设置 → 凭据保护」中设置保护密码，解锁 NexTerm 时需要输入该密码") };
}

/** Locks NexTerm when an unlock method exists; otherwise explains why it cannot (unless quiet). */
export async function requestAppLock(options: { quiet?: boolean } = {}): Promise<boolean> {
  if (isAppLocked()) return true;
  let resolved: { method: AppLockMethod | null; reason: string };
  try {
    resolved = await resolveAppLockMethod();
  } catch (e) {
    if (!options.quiet) useUi.getState().pushToast("error", t("无法锁定 NexTerm：{value}", { value: describeError(e) }));
    return false;
  }
  if (!resolved.method) {
    if (!options.quiet) useUi.getState().pushToast("info", resolved.reason);
    return false;
  }
  lockApp();
  return true;
}

/** Verifies the unlock password and unlocks on success; throws the verification error otherwise. */
export async function unlockAppWithPassword(method: AppLockMethod, password: string): Promise<void> {
  if (!password) throw new Error(t("请输入密码"));
  if (method === "vault") await vaultApi.unlock(password);
  else await useAuth.getState().verifyPassword(password);
  unlockApp();
}

const ACTIVITY_EVENTS = ["keydown", "pointerdown", "pointermove", "wheel", "touchstart"] as const;

/**
 * Starts the idle auto-lock: after the configured minutes without keyboard, pointer or touch input in this window,
 * NexTerm locks itself (quietly skipped when no unlock method is set up). Terminal output is not activity.
 */
export function startAppLockIdleTimer(target: Window = window): () => void {
  let lastActivity = Date.now();
  let timer: ReturnType<typeof setTimeout> | null = null;
  const clear = () => {
    if (timer !== null) clearTimeout(timer);
    timer = null;
  };
  const schedule = () => {
    clear();
    if (appLockIdleMinutes() <= 0 || isAppLocked()) return;
    const wait = Math.max(0, lastActivity + appLockIdleMinutes() * 60_000 - Date.now());
    timer = setTimeout(() => {
      timer = null;
      if (appLockIdleMinutes() <= 0 || isAppLocked()) return;
      if (Date.now() - lastActivity >= appLockIdleMinutes() * 60_000) void requestAppLock({ quiet: true });
      else schedule();
    }, wait);
  };
  const onActivity = () => {
    lastActivity = Date.now();
    if (timer === null) schedule();
  };
  for (const type of ACTIVITY_EVENTS) target.addEventListener(type, onActivity, { capture: true, passive: true });
  const offIdle = subscribeAppLockIdle(() => {
    lastActivity = Date.now();
    schedule();
  });
  const offLock = subscribeAppLock(() => {
    lastActivity = Date.now();
    schedule();
  });
  schedule();
  return () => {
    clear();
    offIdle();
    offLock();
    for (const type of ACTIVITY_EVENTS) target.removeEventListener(type, onActivity, { capture: true });
  };
}

