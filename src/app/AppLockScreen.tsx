import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useQueryClient } from "@tanstack/react-query";
import { biometricApi, systemApi } from "../ipc/commands";
import { WEB } from "../ipc/env";
import { useAuth } from "../features/auth/store";
import { systemNotifier } from "../features/inbox/systemNotifications";
import { describeError } from "../ui/errorText";
import { IconLock, IconXCircle } from "../ui/icons";
import {
  APP_LOCK_ROOT_ID,
  LOCKED_NOTIFICATION_ID,
  resolveAppLockMethod,
  startAppLockIdleTimer,
  unlockApp,
  unlockAppWithPassword,
  useAppLocked,
  type AppLockMethod,
} from "./appLock";
import { t } from "../i18n";

const HIDDEN_MARK = "data-nx-app-locked";

// hideAppContent keeps every top-level node except the lock screen invisible (visibility: hidden in base.css) and
// inert while locked, including dialogs and menus portaled to the body later. Content is hidden, not just covered,
// so terminal canvases, previews and remote desktop frames never show through or take focus.
function hideAppContent(root: HTMLElement): () => void {
  const marked = new Map<Element, { inert: boolean; ariaHidden: string | null }>();
  const mark = (node: Element) => {
    if (node === root || marked.has(node) || node.tagName === "SCRIPT" || node.tagName === "STYLE") return;
    marked.set(node, { inert: node.hasAttribute("inert"), ariaHidden: node.getAttribute("aria-hidden") });
    node.setAttribute(HIDDEN_MARK, "");
    node.setAttribute("inert", "");
    node.setAttribute("aria-hidden", "true");
  };
  for (const child of Array.from(document.body.children)) mark(child);
  const observer = new MutationObserver((records) => {
    for (const record of records) for (const node of Array.from(record.addedNodes)) if (node instanceof Element) mark(node);
  });
  observer.observe(document.body, { childList: true });
  return () => {
    observer.disconnect();
    for (const [node, previous] of marked) {
      node.removeAttribute(HIDDEN_MARK);
      if (!previous.inert) node.removeAttribute("inert");
      if (previous.ariaHidden === null) node.removeAttribute("aria-hidden");
      else node.setAttribute("aria-hidden", previous.ariaHidden);
    }
  };
}

function lockRoot(): HTMLElement {
  let root = document.getElementById(APP_LOCK_ROOT_ID);
  if (!root) {
    root = document.createElement("div");
    root.id = APP_LOCK_ROOT_ID;
    document.body.appendChild(root);
  }
  return root;
}

/** Mounted once by the app shell: runs the idle auto-lock and shows the lock screen while NexTerm is locked. */
export function AppLockLayer() {
  const locked = useAppLocked();
  useEffect(() => startAppLockIdleTimer(), []);
  const [root, setRoot] = useState<HTMLElement | null>(null);
  useLayoutEffect(() => {
    if (!locked) return;
    const node = lockRoot();
    setRoot(node);
    const restore = hideAppContent(node);
    return () => {
      restore();
      setRoot(null);
      // The content-free "locked" notification has served its purpose once the user is back.
      systemNotifier().remove(LOCKED_NOTIFICATION_ID);
    };
  }, [locked]);
  if (!locked || !root) return null;
  return createPortal(<AppLockScreen />, root);
}

type MethodState =
  | { kind: "loading" }
  | { kind: "ready"; method: AppLockMethod }
  | { kind: "none"; reason: string }
  | { kind: "error"; message: string };

function AppLockScreen() {
  const qc = useQueryClient();
  const gate = useAuth((s) => s.gate);
  const username = useAuth((s) => s.user?.username ?? null);
  const logout = useAuth((s) => s.logout);
  const [method, setMethod] = useState<MethodState>({ kind: "loading" });
  const [attempt, setAttempt] = useState(0);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [biometric, setBiometric] = useState<{ label: string } | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    // In browser server mode the account gate decides: while it is still loading nothing is known yet.
    if (WEB && gate === "loading") return;
    let alive = true;
    resolveAppLockMethod().then(
      (resolved) => {
        if (!alive) return;
        setMethod(resolved.method ? { kind: "ready", method: resolved.method } : { kind: "none", reason: resolved.reason });
      },
      (e: unknown) => {
        if (alive) setMethod({ kind: "error", message: describeError(e) });
      },
    );
    return () => {
      alive = false;
    };
  }, [gate, attempt]);

  // A browser session that ended (sign-out here or in another tab, expiry, revocation) hands over to the account
  // gate: signing in again proves the password, so the lock is lifted under the gate rather than stacked over it.
  useEffect(() => {
    if (WEB && gate !== "loading" && gate !== "ready") unlockApp();
  }, [gate]);

  useEffect(() => {
    if (method.kind !== "ready" || method.method !== "vault") return;
    let alive = true;
    void (async () => {
      try {
        const platform = await systemApi.platform();
        if (platform !== "ios" && platform !== "android") return;
        const status = await biometricApi.status();
        if (alive && status.canEvaluate && status.hasMaterial) {
          setBiometric({ label: platform === "ios" ? t("面容 ID") : t("生物识别") });
        }
      } catch {
        // Password unlock stays available.
      }
    })();
    return () => {
      alive = false;
    };
  }, [method]);

  useEffect(() => {
    if (method.kind === "ready") inputRef.current?.focus();
  }, [method]);

  const afterUnlock = () => {
    void qc.invalidateQueries({ queryKey: ["vault-status"] });
    void qc.invalidateQueries({ queryKey: ["credentials"] });
  };

  const submit = async () => {
    if (busy || method.kind !== "ready" || !password) return;
    const value = password;
    setBusy(true);
    setError(null);
    try {
      await unlockAppWithPassword(method.method, value);
      afterUnlock();
    } catch (e) {
      setError(t("解锁失败：{value}", { value: describeError(e) }));
      setPassword("");
      inputRef.current?.focus();
    } finally {
      setBusy(false);
    }
  };

  const unlockWithBiometric = async () => {
    if (busy || !biometric) return;
    setBusy(true);
    setError(null);
    try {
      const result = await biometricApi.unlock();
      if (!result.ok || !result.password) {
        setError(result.message || t("验证未通过，请使用密码解锁"));
        return;
      }
      await unlockAppWithPassword("vault", result.password);
      afterUnlock();
    } catch (e) {
      setError(t("解锁失败：{value}", { value: describeError(e) }));
    } finally {
      setBusy(false);
    }
  };

  const hint =
    method.kind === "ready" && method.method === "account"
      ? username
        ? t("输入账号 {username} 的密码解锁", { username })
        : t("输入账号密码解锁")
      : t("输入凭据库保护密码解锁");

  return (
    <div
      className="nx-auth-gate nx-app-lock fixed inset-0 z-[1000] flex items-center justify-center bg-neutral-950 p-4"
      role="dialog"
      aria-modal="true"
      aria-labelledby="nx-app-lock-title"
    >
      <div className="nx-card w-full max-w-[380px]">
        <div className="mb-1 flex items-center gap-2">
          <IconLock size={15} className="text-neutral-400" />
          <span id="nx-app-lock-title" className="nx-card-title">
            {t("NexTerm 已锁定")}
          </span>
        </div>
        <p className="nx-hint">{t("终端与传输在后台继续运行；解锁前不显示任何内容，也不接收输入。")}</p>

        {method.kind === "loading" && (
          <p className="nx-hint mt-3" role="status">
            {t("正在检查解锁方式…")}
          </p>
        )}

        {method.kind === "error" && (
          <div className="mt-3">
            <div className="nx-alert nx-alert-danger flex items-start gap-2" role="alert">
              <IconXCircle size={13} className="mt-0.5 shrink-0" />
              <span className="min-w-0 flex-1 break-words">{t("无法确认解锁方式：{value}", { value: method.message })}</span>
            </div>
            <button type="button" className="nx-btn nx-btn-outline mt-3 w-full" onClick={() => setAttempt((n) => n + 1)}>
              {t("重试")}
            </button>
          </div>
        )}

        {method.kind === "none" && (
          <div className="mt-3">
            <p className="nx-hint">{t("已没有可验证的解锁密码：{value}", { value: method.reason })}</p>
            <button type="button" className="nx-btn nx-btn-primary mt-3 w-full" onClick={() => unlockApp()}>
              {t("继续使用")}
            </button>
          </div>
        )}

        {method.kind === "ready" && (
          <form
            className="mt-3"
            onSubmit={(e) => {
              e.preventDefault();
              void submit();
            }}
          >
            <label className="block">
              <span className="mb-1 block text-[12px] text-neutral-400">{hint}</span>
              <input
                ref={inputRef}
                className="nx-input w-full"
                type="password"
                aria-label={method.method === "account" ? t("账号密码") : t("保护密码")}
                autoComplete={method.method === "account" ? "current-password" : "off"}
                value={password}
                disabled={busy}
                onChange={(e) => setPassword(e.target.value)}
              />
            </label>
            {error && (
              <div className="nx-alert nx-alert-danger mt-3 flex items-start gap-2" role="alert">
                <IconXCircle size={13} className="mt-0.5 shrink-0" />
                <span className="min-w-0 flex-1 break-words">{error}</span>
              </div>
            )}
            <button type="submit" className="nx-btn nx-btn-primary mt-4 w-full" disabled={busy || !password}>
              {busy ? t("解锁中…") : t("解锁")}
            </button>
            {biometric && (
              <button
                type="button"
                className="nx-btn nx-btn-outline mt-2 w-full"
                disabled={busy}
                onClick={() => void unlockWithBiometric()}
              >
                {t("用{bioLabel}解锁", { bioLabel: biometric.label })}
              </button>
            )}
            {WEB && method.method === "account" && (
              <button
                type="button"
                className="nx-btn nx-btn-ghost nx-btn-sm mt-3 w-full"
                disabled={busy}
                onClick={() => void logout().catch(() => undefined)}
              >
                {t("退出登录")}
              </button>
            )}
          </form>
        )}
      </div>
    </div>
  );
}
