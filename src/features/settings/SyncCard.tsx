import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { assetApi, syncApi, transcriptApi, type TranscriptChunk, type TranscriptSummary } from "../../ipc/commands";
import type { AccountLink, SyncCollectAsset, SyncCollectCredential, SyncCollectTombstone, SyncKindOptIn, SyncStatus } from "../../ipc/types";
import { AuthApiError, syncV2Api } from "../../ipc/authApi";
import { remoteLinkApi, syncCompareApi, type SyncCompareRow } from "../../ipc/remoteApi";
import { useAuth } from "../auth/store";
import {
  base64ToBytes,
  bytesToBase64,
  CryptoError,
  objectPayloadHash,
  sealSyncObject,
  tryOpenSyncObject,
  utf8Bytes,
  type SyncObjectKind,
} from "../auth/crypto";
import {
  aiGrantDevicePayload,
  aiGrantModePayload,
  aiGrantRulePayload,
  aiPermissionPayload,
  aiProfilePayload,
  collectAssetPayload,
  credentialPayload,
  groupPayload,
  knownHostPayload,
  marshalSyncPayload,
  preferencePayload,
  snippetPayload,
  tombstonePayload,
  transcriptPayload,
} from "../auth/syncPayload";
import { useUi } from "../../app/store";
import { WEB_SYNC_ERROR_EVENT, WEB_SYNC_REQUEST_EVENT, WEB_SYNC_RESULT_EVENT } from "../../app/syncEvents";
import { WEB } from "../../ipc/env";
import { listenEvent } from "../../ipc/events";
import { describeError } from "../../ui/errorText";
import { formatTime } from "../../ui/format";
import { AuthCard } from "./AuthCard";
import { AccountCard } from "./AccountCard";
import { SYNC_AUTO_BADGE, SYNC_CONSOLE_TITLE, SYNC_INTERVAL_LABEL, SYNC_MERGE_GUIDE, SyncOptInSection } from "./syncOptIns";
import { COMPARE_KIND_LABELS, CompareDetails, type UndecryptableAction } from "./syncCompareRows";
import {
  IconChevronDown,
  IconChevronRight,
  IconCopy,
  IconInfo,
  IconLock,
  IconRefresh,
  IconServer,
  IconShield,
  IconXCircle,
} from "../../ui/icons";
import { copyText } from "../../ui/clipboard";
import { ask } from "../../ui/dialogs";

export function SyncCard() {
  return (
    <>
      <AuthCard />
      <AccountCard />
      <SyncBody />
    </>
  );
}

function SyncBody() {
  if (WEB) return <WebSyncConsole />;
  return <DesktopLinkCard />;
}

// ---------- Desktop: account login and sync ----------

// LinkStep is the desktop login step state machine: when linkSet returns mfa_required / reset_required
// instead of connected, enter the corresponding onboarding step; after the forced password change completes,
// show the recovery key once (recovery) before entering the logged-in state.
type LinkStep =
  | { kind: "form" }
  | { kind: "mfa" }
  | { kind: "reset" }
  | { kind: "recovery"; recoveryKey: string };

type DesktopSyncStatus = SyncStatus & { paused?: boolean; vaultLocked?: boolean };

function DesktopLinkCard() {
  const pushToast = useUi((s) => s.pushToast);
  const qc = useQueryClient();

  const [link, setLink] = useState<AccountLink | null>(null);
  const [status, setStatus] = useState<DesktopSyncStatus | null>(null);
  const [draft, setDraft] = useState({ url: "", username: "", password: "", insecure: false });
  const draftEdited = useRef(false);
  const [step, setStep] = useState<LinkStep>({ kind: "form" });
  const [busy, setBusy] = useState<null | "login" | "mfa" | "reset" | "logout">(null);
  const [loaded, setLoaded] = useState(false);
  const active = useRef(true);
  const loadSequence = useRef(0);
  const logoutPending = useRef(false);
  const currentAccount = useRef("");
  currentAccount.current = JSON.stringify([link?.url, status?.userId, status?.username, status?.loggedIn]);
  const lastIdentity = useRef("");
  const [error, setError] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [mfaCode, setMfaCode] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [keyRevealed, setKeyRevealed] = useState(false);
  const [keyCopied, setKeyCopied] = useState(false);
  const [keyConfirmed, setKeyConfirmed] = useState(false);
  const [keyCopyError, setKeyCopyError] = useState<string | null>(null);
  const insecurePanelId = useId();
  const [insecureOpen, setInsecureOpen] = useState(false);
  const savedInsecure = link?.insecure ?? false;
  useEffect(() => {
    if (savedInsecure) setInsecureOpen(true);
  }, [savedInsecure]);

  const updateDraft = (patch: Partial<{ url: string; username: string; password: string; insecure: boolean }>) => {
    draftEdited.current = true;
    setDraft((d) => ({ ...d, ...patch }));
  };

  const load = useCallback(() => {
    const sequence = ++loadSequence.current;
    setLoadError(null);
    return Promise.all([syncApi.linkGet(), syncApi.status()])
      .then(([l, s]) => {
        if (sequence !== loadSequence.current) return;
        setLink(l);
        setStatus(s);
        setLoaded(true);
        // On first load, backfill URL/username/insecure from the saved link; the password stays blank; unsaved edits are not overwritten
        setDraft((d) => (draftEdited.current ? d : { ...d, url: l.url, username: l.username, insecure: l.insecure }));
        const identity = JSON.stringify([l.url, s.loggedIn, s.userId, s.username]);
        if (lastIdentity.current !== identity) {
          lastIdentity.current = identity;
          void qc.invalidateQueries({ queryKey: ["sync-link"] });
        }
      })
      .catch((e: unknown) => {
        if (sequence === loadSequence.current) setLoadError(describeError(e));
      });
  }, [qc]);

  useEffect(() => {
    active.current = true;
    void load();
    // When background auto-sync establishes the session or records a probe failure, it broadcasts via sync://status and the card auto-refreshes.
    let disposed = false;
    let unlisten: (() => void) | undefined;
    void listenEvent("sync://status", () => {
      if (!disposed) void load();
    }).then((off) => {
      if (disposed) off();
      else unlisten = off;
    });
    return () => {
      disposed = true;
      active.current = false;
      loadSequence.current += 1;
      unlisten?.();
    };
  }, [load]);

  const finishLogin = async () => {
    setStep({ kind: "form" });
    setLoaded(false);
    void qc.invalidateQueries({ queryKey: ["sync-link"] });
    await load();
    pushToast("success", "登录信息已保存，自动同步已开启");
  };

  const login = async () => {
    setBusy("login");
    setError(null);
    try {
      const r = await remoteLinkApi.linkSet({
        url: draft.url.trim(),
        username: draft.username.trim(),
        ...(draft.password ? { password: draft.password } : {}),
        insecure: draft.insecure,
      });
      setDraft((d) => ({ ...d, password: "" }));
      if (r.connect === "mfa_required") {
        setMfaCode("");
        setStep({ kind: "mfa" });
        return;
      }
      if (r.connect === "reset_required") {
        setNewPassword("");
        setConfirmPassword("");
        setStep({ kind: "reset" });
        return;
      }
      await finishLogin();
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(null);
    }
  };

  const submitMfa = async () => {
    const code = mfaCode.trim();
    if (!code) return;
    setBusy("mfa");
    setError(null);
    try {
      await remoteLinkApi.mfa(code);
      setMfaCode("");
      await finishLogin();
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(null);
    }
  };

  const submitReset = async () => {
    if (newPassword.length < 8) {
      setError("新密码至少 8 位");
      return;
    }
    if (newPassword !== confirmPassword) {
      setError("两次输入的密码不一致");
      return;
    }
    setBusy("reset");
    setError(null);
    try {
      const r = await remoteLinkApi.resetPassword(newPassword);
      setNewPassword("");
      setConfirmPassword("");
      if (r.recoveryKey) {
        setKeyRevealed(false);
        setKeyCopied(false);
        setKeyConfirmed(false);
        setKeyCopyError(null);
        setStep({ kind: "recovery", recoveryKey: r.recoveryKey });
        return;
      }
      await finishLogin();
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(null);
    }
  };

  const backToForm = () => {
    setError(null);
    setStep({ kind: "form" });
  };

  const copyRecoveryKey = async (recoveryKey: string) => {
    setKeyCopied(false);
    setKeyCopyError(null);
    try {
      if (!navigator.clipboard) throw new Error("Clipboard API unavailable");
      await copyText(recoveryKey);
      setKeyCopied(true);
      setKeyConfirmed(true);
    } catch {
      setKeyRevealed(true);
      setKeyConfirmed(true);
      setKeyCopyError("复制失败，请手动复制恢复密钥");
    }
  };

  const logout = async () => {
    if (logoutPending.current || busy !== null || !status?.loggedIn) return;
    const account = currentAccount.current;
    logoutPending.current = true;
    setBusy("logout");
    setError(null);
    try {
      if (!(await ask("登出当前账号？本机将停止自动登录和同步；本机和服务端的数据都会保留。", {
        title: "登出账号", kind: "warning",
      })) || !active.current || currentAccount.current !== account) return;
      const [latestLink, latestStatus] = await Promise.all([syncApi.linkGet(), syncApi.status()]);
      if (!active.current || JSON.stringify([latestLink.url, latestStatus.userId, latestStatus.username, latestStatus.loggedIn]) !== account) return;
      loadSequence.current += 1;
      const cleared = await remoteLinkApi.linkSet({ username: "", password: "" });
      if (!active.current) return;
      setLink(cleared);
      setStatus(null);
      setLoaded(false);
      draftEdited.current = false;
      setDraft((d) => ({ ...d, username: "", password: "" }));
      void qc.invalidateQueries({ queryKey: ["sync-link"] });
      await load();
      pushToast("success", "已登出账号");
    } catch (e) {
      if (active.current) setError(describeError(e));
    } finally {
      logoutPending.current = false;
      if (active.current) setBusy(null);
    }
  };

  const syncPaused = !!status?.paused || !!status?.vaultLocked;
  const connected = !!status?.loggedIn && !status.lastError && !syncPaused;

  return (
    <>
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconServer size={15} className="text-neutral-400" />
        <span className="nx-card-title">{status?.loggedIn ? "服务端连接" : "账号同步"}</span>
        {status?.loggedIn ? (
          <span className={`nx-badge ${connected ? "nx-badge-green" : "nx-badge-amber"}`}>已登录 {status.username}</span>
        ) : !loaded ? (
          <span className="nx-badge">正在读取账号…</span>
        ) : status?.configured ? (
          <span className="nx-badge nx-badge-amber">待同步</span>
        ) : (
          <span className="nx-badge">未配置</span>
        )}
      </div>

      <p className="nx-hint mb-3.5">
        {status?.loggedIn ? "数据会在本机与服务端之间自动同步。" : "登录后，数据会自动在本机与服务端之间同步；不登录也能继续本地使用。"}
      </p>

      {loadError && (
        <div className="nx-alert nx-alert-danger mb-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">同步配置读取失败 · {loadError}</span>
          <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={() => void load()}>
            <IconRefresh size={12} />
            重试
          </button>
        </div>
      )}

      {!loaded && !loadError && <p role="status" className="nx-hint">正在检查登录状态…</p>}

      {step.kind === "form" && status?.loggedIn && (
        <div className="flex flex-wrap items-center gap-2">
          <span className="nx-hint min-w-0 flex-1 break-all">{link?.url}</span>
          <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" disabled={busy !== null} onClick={() => void logout()}>
            {busy === "logout" ? "正在登出…" : "登出"}
          </button>
          {status.lastError && !syncPaused && <span className="nx-hint text-amber-300">上次同步失败：{status.lastError}</span>}
        </div>
      )}

      {step.kind === "form" && loaded && !status?.loggedIn && (
        <>
          <div className="flex flex-col gap-2">
            <div className="flex flex-col gap-1 sm:flex-row sm:items-center sm:gap-2">
              <label className="sm:w-[76px] sm:shrink-0 text-[12px] text-neutral-400" htmlFor="sync-url">
                服务端
              </label>
              <input
                id="sync-url"
                className="nx-input min-w-0 flex-1 font-mono"
                placeholder="https://nexterm.example.com"
                value={draft.url}
                autoComplete="url"
                onChange={(e) => updateDraft({ url: e.target.value })}
              />
            </div>
            <div className="flex flex-col gap-1 sm:flex-row sm:items-center sm:gap-2">
              <label className="sm:w-[76px] sm:shrink-0 text-[12px] text-neutral-400" htmlFor="sync-user">
                账号
              </label>
              <input
                id="sync-user"
                className="nx-input min-w-0 flex-1"
                placeholder="用户名"
                value={draft.username}
                autoComplete="username"
                onChange={(e) => updateDraft({ username: e.target.value })}
              />
            </div>
            <div className="flex flex-col gap-1 sm:flex-row sm:items-center sm:gap-2">
              <label className="sm:w-[76px] sm:shrink-0 text-[12px] text-neutral-400" htmlFor="sync-pass">
                密码
              </label>
              <input
                id="sync-pass"
                type="password"
                className="nx-input min-w-0 flex-1"
                placeholder={link?.hasPassword ? "留空则不修改已保存的密码" : "账号密码"}
                value={draft.password}
                autoComplete="new-password"
                onChange={(e) => updateDraft({ password: e.target.value })}
              />
            </div>
            <div className="mt-1 border-t border-neutral-800/60 pt-2">
              <button
                type="button"
                className="flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
                aria-expanded={insecureOpen}
                aria-controls={insecurePanelId}
                onClick={() => setInsecureOpen((v) => !v)}
              >
                {insecureOpen ? (
                  <IconChevronDown size={12} className="shrink-0" />
                ) : (
                  <IconChevronRight size={12} className="shrink-0" />
                )}
                高级选项
              </button>
              {insecureOpen && (
                <div id={insecurePanelId} className="mt-2">
                  <label className="flex items-start gap-2">
                    <input
                      type="checkbox"
                      className="mt-0.5 h-4 w-4 shrink-0"
                      checked={draft.insecure}
                      onChange={(e) => updateDraft({ insecure: e.target.checked })}
                    />
                    <span className="text-[12px] text-neutral-300">
                      跳过证书校验
                      <span className="nx-hint block">勾选后不再验证服务端身份，可能被中间人冒充；仅在自签名证书的内网或自建服务中使用。</span>
                    </span>
                  </label>
                </div>
              )}
            </div>
          </div>

          <div className="mt-3 flex flex-wrap items-center gap-2">
            <button
              className="nx-btn nx-btn-primary nx-btn-sm"
              disabled={busy !== null || !draft.url.trim() || !draft.username.trim() || (!draft.password && !link?.hasPassword)}
              onClick={() => void login()}
            >
              {busy === "login" ? <IconRefresh size={12} className="animate-spin" /> : <IconLock size={12} />}
              {busy === "login" ? "登录中…" : "登录"}
            </button>
            {status?.lastError && !syncPaused && <span className="nx-hint text-amber-300">上次失败：{status.lastError}</span>}
          </div>
        </>
      )}

      {step.kind === "mfa" && (
        <div className="flex flex-col gap-2">
          <p className="nx-hint">
            此账号已开启 TOTP 两步验证。请输入认证器 App 中的 6 位动态码；手机丢失时，也可输入未使用的恢复码。
          </p>
          <div className="flex flex-col gap-1 sm:flex-row sm:items-center sm:gap-2">
            <label className="sm:w-[76px] sm:shrink-0 text-[12px] text-neutral-400" htmlFor="sync-mfa-code">
              动态码
            </label>
            <input
              id="sync-mfa-code"
              className="nx-input min-w-0 flex-1 font-mono"
              placeholder="6 位数字或恢复码"
              value={mfaCode}
              autoComplete="one-time-code"
              onChange={(e) => setMfaCode(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && mfaCode.trim() && busy === null) void submitMfa();
              }}
            />
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            <button
              className="nx-btn nx-btn-primary nx-btn-sm"
              disabled={busy !== null || !mfaCode.trim()}
              onClick={() => void submitMfa()}
            >
              {busy === "mfa" ? <IconRefresh size={12} className="animate-spin" /> : <IconShield size={12} />}
              {busy === "mfa" ? "验证中…" : "验证并登录"}
            </button>
            <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" disabled={busy !== null} onClick={backToForm}>
              返回
            </button>
          </div>
        </div>
      )}

      {step.kind === "reset" && (
        <div className="flex flex-col gap-2">
          <p className="nx-hint">
            账号被管理员重置，必须先设置新密码。数据密钥、所有会话和两步验证绑定已清除；设置新密码后会签发新的数据密钥与一次性恢复密钥，服务端密文需从仍持有数据的设备重新同步。
          </p>
          <div className="flex flex-col gap-1 sm:flex-row sm:items-center sm:gap-2">
            <label className="sm:w-[76px] sm:shrink-0 text-[12px] text-neutral-400" htmlFor="sync-new-pass">
              新密码
            </label>
            <input
              id="sync-new-pass"
              type="password"
              className="nx-input min-w-0 flex-1"
              placeholder="至少 8 位"
              value={newPassword}
              autoComplete="new-password"
              onChange={(e) => setNewPassword(e.target.value)}
            />
          </div>
          <div className="flex flex-col gap-1 sm:flex-row sm:items-center sm:gap-2">
            <label className="sm:w-[76px] sm:shrink-0 text-[12px] text-neutral-400" htmlFor="sync-new-pass2">
              确认密码
            </label>
            <input
              id="sync-new-pass2"
              type="password"
              className="nx-input min-w-0 flex-1"
              placeholder="再输入一次"
              value={confirmPassword}
              autoComplete="new-password"
              onChange={(e) => setConfirmPassword(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && newPassword && confirmPassword && busy === null) void submitReset();
              }}
            />
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            <button
              className="nx-btn nx-btn-primary nx-btn-sm"
              disabled={busy !== null || !newPassword || !confirmPassword}
              onClick={() => void submitReset()}
            >
              {busy === "reset" ? <IconRefresh size={12} className="animate-spin" /> : <IconLock size={12} />}
              {busy === "reset" ? "设置中…" : "设置新密码"}
            </button>
            <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" disabled={busy !== null} onClick={backToForm}>
              返回
            </button>
          </div>
        </div>
      )}

      {step.kind === "recovery" && (
        <div className="flex flex-col gap-2">
          <div className="flex items-center gap-2">
            <IconShield size={15} className="text-amber-300" />
            <span className="text-[13px] font-semibold text-neutral-100">恢复密钥（仅显示一次）</span>
          </div>
          <p className="nx-hint">
            这是自行找回账号的唯一凭证，服务端只保存散列，关闭后无法再次查看。请复制并妥善保存（如密码管理器或离线介质）。
          </p>
          <div className="nx-alert nx-alert-danger flex flex-col gap-2">
            <code className="nx-code break-all font-mono text-[13px]">
              {keyRevealed ? step.recoveryKey : "•".repeat(step.recoveryKey.length)}
            </code>
            <div className="flex flex-wrap items-center gap-2">
              <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setKeyRevealed((v) => !v)}>
                {keyRevealed ? "隐藏" : "显示"}
              </button>
              <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => void copyRecoveryKey(step.recoveryKey)}>
                <IconCopy size={12} />
                {keyCopied ? "已复制" : "复制"}
              </button>
            </div>
          </div>
          {keyCopyError && (
            <div className="nx-alert nx-alert-danger flex items-start gap-2" role="alert">
              <IconXCircle size={13} className="mt-0.5 shrink-0" />
              <span>{keyCopyError}</span>
            </div>
          )}
          <div className="nx-alert nx-alert-info flex items-start gap-2">
            <IconInfo size={14} className="mt-0.5 shrink-0" />
            <div>恢复密钥仅用于忘记密码时重置账号；日常解密使用密码。两者都需要保密。</div>
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            <button className="nx-btn nx-btn-primary nx-btn-sm" disabled={!keyConfirmed} onClick={() => void finishLogin()}>
              已保存，进入同步
            </button>
            {!keyConfirmed && <span className="nx-hint text-[11px]">请先复制或手动保存恢复密钥</span>}
          </div>
        </div>
      )}

      {error && (
        <div className="nx-alert nx-alert-danger mt-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">{error}</span>
        </div>
      )}

      {step.kind === "form" && (
        <div className="nx-alert nx-alert-info mt-3 flex items-start gap-2">
          <IconInfo size={14} className="mt-0.5 shrink-0" />
          <div>
            终端录像默认不同步，可在「终端历史」中单独开启。
          </div>
        </div>
      )}
    </section>
    {status?.loggedIn && <DesktopSyncConsole />}
    </>
  );
}

// ---------- Desktop: account sync ----------

// DesktopSyncConsole is shown after desktop login (link established): sync content switches (kindOptInGet/Set),
// sync status (last successful sync/latest error), manual trigger (sync_wake wakes the background sync loop),
// and per-row comparison details (sync_compare: the Go side decrypts cloud objects with the engine session DEK, same judgment as the web).
function DesktopSyncConsole() {
  const pushToast = useUi((s) => s.pushToast);
  const [status, setStatus] = useState<DesktopSyncStatus | null>(null);
  const [kindOptIn, setKindOptIn] = useState<SyncKindOptIn | null>(null);
  const [optInBusy, setOptInBusy] = useState(false);
  const [syncBusy, setSyncBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [compareRows, setCompareRows] = useState<SyncCompareRow[] | null>(null);
  const [compareRemote, setCompareRemote] = useState<number | null>(null);
  const [compareWarnings, setCompareWarnings] = useState<string[]>([]);
  const [compareBusy, setCompareBusy] = useState(false);
  const [compareError, setCompareError] = useState<string | null>(null);
  const [compareContext, setCompareContext] = useState<{ userId: string; compareToken: string } | null>(null);
  const [repairBusy, setRepairBusy] = useState(false);
  const [repairError, setRepairError] = useState<string | null>(null);
  const repairBusyRef = useRef(false);
  const compareSeqRef = useRef(0);

  const load = useCallback(() => {
    return Promise.all([
      syncApi.status().then((s) => setStatus(s)),
      syncApi.kindOptInGet().then((o) => setKindOptIn(o)),
    ]).catch((e: unknown) => setError(describeError(e)));
  }, []);

  const loadCompare = useCallback(() => {
    const seq = ++compareSeqRef.current;
    setCompareBusy(true);
    setCompareError(null);
    setCompareContext(null);
    return syncCompareApi
      .compare()
      .then((result) => {
        if (seq !== compareSeqRef.current) return;
        setCompareRows(result.rows.map((row) => ({
          ...row,
          hasLocalRepairCopy: Boolean(row.localPayloadHash && row.kind),
        })));
        setCompareRemote(result.remote);
        setCompareWarnings(result.warnings ?? []);
        setCompareContext({ userId: result.userId, compareToken: result.compareToken });
      })
      .catch((e: unknown) => {
        if (seq === compareSeqRef.current) setCompareError(describeError(e));
      })
      .finally(() => {
        if (seq === compareSeqRef.current) setCompareBusy(false);
      });
  }, []);

  useEffect(() => {
    void load();
    void loadCompare();
  }, [load, loadCompare]);

  useEffect(() => {
    let disposed = false;
    let unlisten: (() => void) | undefined;
    void listenEvent("sync://round-complete", () => {
      if (disposed) return;
      void load();
      void loadCompare();
    }).then((off) => {
      if (disposed) off();
      else unlisten = off;
    });
    return () => {
      disposed = true;
      unlisten?.();
    };
  }, [load, loadCompare]);

  const updateKindOptIn = async (patch: Partial<SyncKindOptIn>) => {
    setOptInBusy(true);
    setError(null);
    try {
      setKindOptIn(await syncApi.kindOptInSet(patch));
    } catch (e) {
      setError(describeError(e));
    } finally {
      setOptInBusy(false);
    }
  };

  const syncNow = async () => {
    setSyncBusy(true);
    setError(null);
    try {
      await syncApi.wake();
      pushToast("success", "已触发一轮后台同步");
    } catch (e) {
      setError(describeError(e));
    } finally {
      setSyncBusy(false);
    }
  };

  const repairUndecryptable = async (id: string, action: UndecryptableAction) => {
    if (repairBusyRef.current || compareBusy) return;
    if (action === "retry") {
      await loadCompare();
      return;
    }
    const row = compareRows?.find((candidate) => candidate.id === id && candidate.state === "remote-undecryptable");
    const context = compareContext;
    if (!row || !context || !row.remoteSeq || !row.remoteBlobHash) {
      setRepairError("对比结果已变化，请刷新后再操作");
      return;
    }
    const hasLocal = Boolean(row.localPayloadHash && row.kind);
    if ((action === "restore-local") !== hasLocal) {
      setRepairError("本机副本状态已变化，请刷新对比结果");
      return;
    }
    repairBusyRef.current = true;
    setRepairBusy(true);
    setRepairError(null);
    const seq = compareSeqRef.current;
    try {
      const { ask } = await import("../../ui/dialogs");
      const confirmed = action === "restore-local"
        ? await ask(`将本机「${row.name}」覆盖服务端无法解密的对象？此操作会替换服务端原有密文。`, {
          title: "用本机副本修复服务端对象", kind: "warning",
        })
        : await ask(`本次对比没有可用的本机对象 ${id}。确认清除这条无法解密、类型未知的服务端副本？`, {
          title: "清除无法解密的服务端副本", kind: "warning",
        });
      if (!confirmed) return;
      if (seq !== compareSeqRef.current) throw new Error("对比结果已变化，请刷新后再操作");
      const response = await syncCompareApi.repairUndecryptable({
        id, action, ...context,
        remoteSeq: row.remoteSeq,
        remoteBlobHash: row.remoteBlobHash,
        ...(hasLocal ? { localKind: row.kind, localPayloadHash: row.localPayloadHash } : {}),
      });
      if (!response.repaired) throw new Error("服务端未确认操作，请刷新对比结果");
      pushToast("success", action === "restore-local" ? "服务端对象已用本机副本修复" : "服务端副本已清除");
      await loadCompare();
      await load();
    } catch (cause) {
      setRepairError(describeError(cause));
    } finally {
      repairBusyRef.current = false;
      setRepairBusy(false);
    }
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconServer size={15} className="text-neutral-400" />
        <span className="nx-card-title">{SYNC_CONSOLE_TITLE}</span>
        <span className="nx-badge nx-badge-green">{SYNC_AUTO_BADGE}</span>
        {compareRemote !== null && <span className="nx-hint">服务端 {compareRemote} 个对象</span>}
        <span className="nx-hint">{SYNC_INTERVAL_LABEL}</span>
        <div className="nx-spacer" />
        <button
          className="nx-btn nx-btn-outline nx-btn-sm"
          disabled={compareBusy}
          onClick={() => void loadCompare()}
        >
          <IconRefresh size={12} className={compareBusy ? "animate-spin" : ""} />
          刷新对比
        </button>
        <button
          className="nx-btn nx-btn-outline nx-btn-sm"
          disabled={syncBusy}
          onClick={() => void syncNow()}
        >
          <IconRefresh size={12} className={syncBusy ? "animate-spin" : ""} />
          立即同步
        </button>
      </div>

      <p className="nx-hint mb-3">
        {SYNC_MERGE_GUIDE}
        {status?.lastSuccessAt && Date.parse(status.lastSuccessAt) > 0 &&
          `上次同步：${formatTime(Date.parse(status.lastSuccessAt))}。`}
      </p>

      {kindOptIn && <SyncOptInSection value={kindOptIn} busy={optInBusy} onChange={(patch) => void updateKindOptIn(patch)} />}

      {status?.lastError && !status.paused && !status.vaultLocked && (
        <div className="nx-alert nx-alert-danger mb-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">上次同步失败：{status.lastError}，将在下个自动同步周期重试。</span>
        </div>
      )}

      {error && (
        <div className="nx-alert nx-alert-danger mb-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">{error}</span>
        </div>
      )}

      {compareError && (
        <div className="nx-alert nx-alert-danger mb-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">对比读取失败：{compareError}</span>
          <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={() => void loadCompare()}>
            <IconRefresh size={12} />
            重试
          </button>
        </div>
      )}

      {repairError && (
        <div className="nx-alert nx-alert-danger mb-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">操作失败：{repairError}</span>
        </div>
      )}

      {compareWarnings.length > 0 && (
        <div className="nx-alert nx-alert-info mb-3 flex items-start gap-2">
          <IconInfo size={13} className="mt-0.5 shrink-0" />
          <div className="min-w-0 flex-1 text-amber-300">
            {compareWarnings.map((warning, index) => (
              <div key={index} className="break-words">
                {warning}
              </div>
            ))}
          </div>
        </div>
      )}

      <CompareDetails rows={compareRows} error={compareError !== null}
        onUndecryptableAction={compareContext ? (id, action) => void repairUndecryptable(id, action) : undefined}
        actionsBusy={repairBusy || compareBusy} />

      <div className="nx-alert nx-alert-info mt-3 flex items-start gap-2">
        <IconInfo size={14} className="mt-0.5 shrink-0" />
        <div>
          关闭上方内容类型开关不会删除服务端已有副本；在「终端历史」关闭单条录像同步会删除其服务端副本，本机记录保留。冲突按最后修改时间裁决，请保持各设备时钟准确，否则「较新」判定可能不符合预期。
        </div>
      </div>
    </section>
  );
}

// ---------- Web: account sync ----------

interface LocalEntity {
  id: string;
  kind: SyncObjectKind;
  name: string;
  updatedAt: number;
  deletedAt: number | null;
  payload: unknown;
  payloadHash: string;
  payloadText?: string;
}

interface RemoteObject {
  id: string;
  kind: SyncObjectKind;
  name: string;
  updatedAt: number;
  deletedAt: number | null;
  seq: number;
  payloadHash: string;
  blobHash: string;
  plaintext: string;
}

interface RemoteState {
  objects: RemoteObject[];
  undecryptableIds: string[];
  undecryptableEntries: UndecryptableEntry[];
  head: string;
  maxSeq: number;
}

interface UndecryptableEntry {
  id: string;
  seq: number;
  blobHash: string;
}

const TRANSCRIPT_MAX_CONTENT_BYTES = 64 << 20;

// readTranscriptAll reads all content paginated by done/nextSeq; read failures throw explicitly and are never swallowed into an empty array.
async function readTranscriptAll(id: string): Promise<TranscriptChunk[]> {
  const chunks: TranscriptChunk[] = [];
  let afterSeq = 0;
  for (let page = 0; page < 10000; page++) {
    const read = await transcriptApi.read(id, afterSeq);
    chunks.push(...read.chunks);
    if (read.done) return chunks;
    if (read.nextSeq <= afterSeq) {
      throw new Error(`会话记录 ${id} 分页游标未推进(done=false 且 nextSeq 未前进)`);
    }
    afterSeq = read.nextSeq;
  }
  throw new Error(`会话记录 ${id} 分页读取超出上限`);
}

interface TranscriptCacheEntry {
  fingerprint: string;
  entity?: LocalEntity;
  pending?: Promise<LocalEntity>;
}

const transcriptCaches = new WeakMap<Uint8Array, Map<string, TranscriptCacheEntry>>();

function transcriptFingerprint(t: TranscriptSummary): string {
  return JSON.stringify([
    t.id, t.sessionId, t.assetId, t.assetName, t.assetKind, t.startedAt, t.endedAt,
    t.bytes, t.chunks, t.truncated, t.contentOmitted === true,
  ]);
}

async function cachedTranscriptEntity(dek: Uint8Array, t: TranscriptSummary): Promise<LocalEntity> {
  let cache = transcriptCaches.get(dek);
  if (!cache) {
    cache = new Map<string, TranscriptCacheEntry>();
    transcriptCaches.set(dek, cache);
  }
  const fingerprint = transcriptFingerprint(t);
  const existing = cache.get(t.id);
  if (existing?.fingerprint === fingerprint) {
    if (existing.entity) return existing.entity;
    if (existing.pending) return existing.pending;
  }

  const pending: Promise<LocalEntity> = (async () => {
    let content: TranscriptChunk[] | null = null;
    // contentOmitted is used only with the real over-limit semantics (>64MiB is metadata-only); under the limit, everything must be paged in - multi-page content must not be marked complete.
    if (!t.contentOmitted && t.bytes > TRANSCRIPT_MAX_CONTENT_BYTES) {
      content = null;
    } else if (!t.contentOmitted) {
      content = await readTranscriptAll(t.id);
    }
    const payload = transcriptPayload(t, content);
    const payloadText = marshalSyncPayload(payload);
    return {
      id: t.id,
      kind: "transcript",
      name: `${t.assetName} 的会话记录`,
      updatedAt: t.endedAt ?? t.startedAt,
      deletedAt: null,
      payload,
      payloadText,
      payloadHash: await objectPayloadHash(utf8Bytes(payloadText)),
    };
  })();
  cache.set(t.id, { fingerprint, pending });
  try {
    const entity = await pending;
    if (cache.get(t.id)?.pending === pending) cache.set(t.id, { fingerprint, entity });
    return entity;
  } catch (error) {
    if (cache.get(t.id)?.pending === pending) cache.delete(t.id);
    throw error;
  }
}

async function loadOptedInTranscripts(dek: Uint8Array): Promise<LocalEntity[]> {
  const hosts = await transcriptApi.hosts();
  const out: LocalEntity[] = [];
  for (const host of hosts) {
    const summaries = await transcriptApi.list(host.assetId);
    for (const t of summaries) {
      if (!t.syncOptIn || t.active || t.endedAt === null) continue;
      out.push(await cachedTranscriptEntity(dek, t));
    }
  }
  return out;
}

async function localTranscriptExists(id: string): Promise<boolean> {
  const hosts = await transcriptApi.hosts();
  for (const host of hosts) {
    if ((await transcriptApi.list(host.assetId)).some((transcript) => transcript.id === id)) return true;
  }
  return false;
}

interface LocalCollection {
  entities: LocalEntity[];
  warnings: string[];
}

const COLLECT_PAGE_LIMIT = 512;

async function collectAssetsAll(): Promise<SyncCollectAsset[]> {
  const out: SyncCollectAsset[] = [];
  let afterId: string | undefined;
  for (let page = 0; page < 10000; page++) {
    const result = await syncApi.collectAssets(true, afterId, COLLECT_PAGE_LIMIT);
    out.push(...result.assets);
    if (!result.hasMore) return out;
    if (!result.nextAfterId) throw new Error("资产收集分页游标缺失");
    afterId = result.nextAfterId;
  }
  throw new Error("资产收集分页超出上限");
}

async function collectTombstonesAll(): Promise<SyncCollectTombstone[]> {
  const out: SyncCollectTombstone[] = [];
  let afterId: string | undefined;
  for (let page = 0; page < 10000; page++) {
    const result = await syncApi.collectTombstones(afterId, COLLECT_PAGE_LIMIT);
    out.push(...result.tombstones);
    if (!result.hasMore) return out;
    if (!result.nextAfterId) throw new Error("墓碑收集分页游标缺失");
    afterId = result.nextAfterId;
  }
  throw new Error("墓碑收集分页超出上限");
}

async function collectCredentialsAll(): Promise<SyncCollectCredential[]> {
  const out: SyncCollectCredential[] = [];
  let afterId: string | undefined;
  for (let page = 0; page < 10000; page++) {
    const result = await syncApi.collectCredentials(true, afterId, COLLECT_PAGE_LIMIT);
    out.push(...result.credentials);
    if (!result.hasMore) return out;
    if (!result.nextAfterId) throw new Error("凭据收集分页游标缺失");
    afterId = result.nextAfterId;
  }
  throw new Error("凭据收集分页超出上限");
}

async function collectKnownHostsAll(): Promise<import("../../ipc/types").SyncCollectKnownHost[]> {
  const out: import("../../ipc/types").SyncCollectKnownHost[] = [];
  let afterId: string | undefined;
  for (let page = 0; page < 10000; page++) {
    const result = await syncApi.collectKnownHosts(afterId, COLLECT_PAGE_LIMIT);
    out.push(...result.knownHosts);
    if (!result.hasMore) return out;
    if (!result.nextAfterId) throw new Error("已知主机收集分页游标缺失");
    afterId = result.nextAfterId;
  }
  throw new Error("已知主机收集分页超出上限");
}

async function collectAIProfilesAll(): Promise<import("../../ipc/types").SyncCollectAIProfile[]> {
  const out: import("../../ipc/types").SyncCollectAIProfile[] = [];
  let afterId: string | undefined;
  for (let page = 0; page < 10000; page++) {
    const result = await syncApi.collectAIProfiles(true, afterId, COLLECT_PAGE_LIMIT);
    out.push(...result.profiles);
    if (!result.hasMore) return out;
    if (!result.nextAfterId) throw new Error("AI 模型档案收集分页游标缺失");
    afterId = result.nextAfterId;
  }
  throw new Error("AI 模型档案收集分页超出上限");
}

function credentialStateLabel(state: string): string {
  switch (state) {
    case "locked":
      return "凭据库未解锁";
    case "unavailable":
      return "凭据库不可用";
    case "error":
      return "解密失败";
    case "withheld":
      return "未请求明文";
    default:
      return state;
  }
}

async function localEntity(
  id: string,
  kind: SyncObjectKind,
  name: string,
  updatedAt: number,
  deletedAt: number | null,
  payload: unknown,
): Promise<LocalEntity> {
  const payloadText = marshalSyncPayload(payload);
  return { id, kind, name, updatedAt, deletedAt, payload, payloadText, payloadHash: await objectPayloadHash(utf8Bytes(payloadText)) };
}

// loadLocalEntities collects the full local copy via the M141 collect RPC: soft-deleted assets, both tombstone kinds and credentials all enter the comparison,
// in the same order and keys as internal/sync collectLocalObjects (same ID: later write wins; newer live objects beat old tombstones; surviving transcripts go last).
// Credentials whose plaintext cannot be read (locked/unavailable/error) are explicitly recorded in warnings, never silently dropped.
// Optional sync kinds are collected per the account user's switches (default on): only enabled ones enter comparison and push;
// a profile apiKey withheld (locked/unavailable/error) likewise only records a warning; keyless profiles (apiKeySet=false) are unaffected by vault lock.
async function loadLocalEntities(optIn: SyncKindOptIn, dek: Uint8Array): Promise<LocalCollection> {
  const warnings: string[] = [];
  const [groups, snippets, transcripts, assets, tombstones, credentials, knownHosts, aiProfiles, aiPermission, aiGrants, preferences] = await Promise.all([
    assetApi.groupList(),
    assetApi.snippetList(),
    loadOptedInTranscripts(dek),
    collectAssetsAll(),
    collectTombstonesAll(),
    collectCredentialsAll(),
    optIn.knownHost ? collectKnownHostsAll() : Promise.resolve([]),
    optIn.aiProfile ? collectAIProfilesAll() : Promise.resolve([]),
    optIn.aiPermission ? syncApi.collectAIPermission() : Promise.resolve({ id: "", mode: "", dangerRules: [], updatedAt: 0, found: false }),
    optIn.aiGrant ? syncApi.collectAIGrants() : Promise.resolve({ grants: [], rules: [], modes: [] }),
    optIn.preferences ? syncApi.collectPreferences() : Promise.resolve([]),
  ]);
  const byId = new Map<string, LocalEntity>();
  for (const g of groups) {
    byId.set(g.id, await localEntity(g.id, "group", g.name, g.updatedAt, null, groupPayload(g)));
  }
  for (const c of credentials) {
    if (c.secretState !== "revealed" || c.secret === undefined) {
      warnings.push(`凭据「${c.name}」${credentialStateLabel(c.secretState)}，未进入本次对比与推送`);
      continue;
    }
    byId.set(c.id, await localEntity(c.id, "credential", c.name, c.updatedAt, null, credentialPayload(c)));
  }
  for (const s of snippets) {
    byId.set(s.id, await localEntity(s.id, "snippet", s.name, s.updatedAt, null, snippetPayload(s)));
  }
  for (const a of assets) {
    byId.set(a.id, await localEntity(a.id, "asset", a.name, a.updatedAt, a.deletedAt ?? null, collectAssetPayload(a)));
  }
  for (const k of knownHosts) {
    byId.set(k.id, await localEntity(k.id, "known_host", `${k.host}:${k.port}`, k.addedAt, null, knownHostPayload(k)));
  }
  for (const p of aiProfiles) {
    if (p.apiKeySet && (p.apiKeyState !== "revealed" || p.apiKey === undefined)) {
      warnings.push(`AI 模型档案「${p.name}」${credentialStateLabel(p.apiKeyState)}，未进入本次对比与推送`);
      continue;
    }
    byId.set(p.id, await localEntity(p.id, "ai_profile", p.name, p.updatedAt, null, aiProfilePayload(p)));
  }
  if (aiPermission.found) {
    byId.set(aiPermission.id, await localEntity(aiPermission.id, "ai_permission", "全局 AI 权限", aiPermission.updatedAt, null, aiPermissionPayload(aiPermission)));
  }
  for (const g of aiGrants.grants) {
    byId.set(g.id, await localEntity(g.id, "ai_grant", g.deviceId, g.updatedAt, null, aiGrantDevicePayload(g)));
  }
  for (const r of aiGrants.rules) {
    const name = r.path ? `${r.deviceId} ${r.action} ${r.path}` : `${r.deviceId} ${r.action}`;
    byId.set(r.id, await localEntity(r.id, "ai_grant", name, r.updatedAt, null, aiGrantRulePayload(r)));
  }
  for (const m of aiGrants.modes) {
    byId.set(m.id, await localEntity(m.id, "ai_grant", `${m.deviceId} ${m.mode}`, m.updatedAt, null, aiGrantModePayload(m)));
  }
  for (const p of preferences) {
    byId.set(p.id, await localEntity(p.id, "preference", p.key, p.updatedAt, null, preferencePayload(p)));
  }
  for (const t of tombstones) {
    // Tombstones of disabled kinds stay out of comparison and push: disabled sync must not delete remote objects
    if (t.targetKind === "known_host" && !optIn.knownHost) continue;
    if (t.targetKind === "ai_profile" && !optIn.aiProfile) continue;
    if (t.targetKind === "ai_permission" && !optIn.aiPermission) continue;
    if (t.targetKind === "ai_grant" && !optIn.aiGrant) continue;
    if (t.targetKind === "preference" && !optIn.preferences) continue;
    const live = byId.get(t.id);
    if (live && revisionOf(live.updatedAt, live.deletedAt) > t.deletedAt) continue;
    const label = COMPARE_KIND_LABELS[t.targetKind] ?? t.targetKind;
    byId.set(t.id, await localEntity(t.id, "tombstone", `${label} ${t.id}`, t.deletedAt, t.deletedAt, tombstonePayload(t)));
  }
  for (const tr of transcripts) {
    byId.set(tr.id, tr);
  }
  return { entities: [...byId.values()], warnings };
}

function revisionOf(updatedAt: number, deletedAt: number | null): number {
  return deletedAt !== null && deletedAt > updatedAt ? deletedAt : updatedAt;
}

// remoteKindVisible filters remote objects by opt-in: disabled kinds are neither applied nor compared;
// remote tombstones of optional kinds are likewise governed by their targetKind switch (disabled sync must not delete remote/local objects).
function remoteKindVisible(kind: SyncObjectKind, plaintext: string, optIn: SyncKindOptIn): boolean {
  if (kind === "known_host") return optIn.knownHost;
  if (kind === "ai_profile") return optIn.aiProfile;
  if (kind === "ai_permission") return !!optIn.aiPermission;
  if (kind === "ai_grant") return !!optIn.aiGrant;
  if (kind === "preference") return !!optIn.preferences;
  if (kind === "tombstone") {
    const target = (JSON.parse(plaintext) as { targetKind?: string }).targetKind;
    if (target === "known_host") return optIn.knownHost;
    if (target === "ai_profile") return optIn.aiProfile;
    if (target === "ai_permission") return !!optIn.aiPermission;
    if (target === "ai_grant") return !!optIn.aiGrant;
    if (target === "preference") return !!optIn.preferences;
  }
  return true;
}

interface RemoteCache {
  head: string;
  objects: Map<string, RemoteObject>;
  undecryptableIds: string[];
  undecryptableEntries: UndecryptableEntry[];
}

const remoteCaches = new WeakMap<Uint8Array, RemoteCache>();

async function decodeRemoteObject(entry: { id: string; seq: number; blob_hash: string }, blob: string, dek: Uint8Array): Promise<RemoteObject> {
  const { kind, plaintext } = await tryOpenSyncObject(dek, base64ToBytes(blob), entry.id);
  const text = new TextDecoder().decode(plaintext);
  const payloadHash = await objectPayloadHash(plaintext);
  const base = {
    id: entry.id,
    kind,
    updatedAt: 0,
    deletedAt: null,
    seq: entry.seq,
    payloadHash,
    blobHash: entry.blob_hash,
    plaintext: text,
  };
  if (kind === "tombstone") {
    const t = JSON.parse(text) as { targetKind?: string; deletedAt?: number };
    return { ...base, name: entry.id, updatedAt: t.deletedAt ?? 0, deletedAt: t.deletedAt ?? 0 };
  }
  if (kind === "transcript") {
    const t = JSON.parse(text) as { assetName?: string; endedAt?: number };
    return { ...base, name: t.assetName ? `${t.assetName} 的会话记录` : entry.id, updatedAt: t.endedAt ?? 0 };
  }
  if (kind === "known_host") {
    const k = JSON.parse(text) as { host?: string; port?: number; addedAt?: number };
    return { ...base, name: k.host ? `${k.host}:${k.port}` : entry.id, updatedAt: k.addedAt ?? 0 };
  }
  if (kind === "ai_grant") {
    const g = JSON.parse(text) as { deviceId?: string; action?: string; path?: string; mode?: string; updatedAt?: number };
    const name = g.deviceId
      ? g.action
        ? `${g.deviceId} ${g.action}${g.path ? ` ${g.path}` : ""}`
        : g.mode
          ? `${g.deviceId} ${g.mode}`
          : g.deviceId
      : entry.id;
    return { ...base, name, updatedAt: g.updatedAt ?? 0 };
  }
  const p = JSON.parse(text) as { name?: string; updatedAt?: number; deletedAt?: number };
  return { ...base, name: p.name ?? entry.id, updatedAt: p.updatedAt ?? 0, deletedAt: p.deletedAt ?? null };
}

type SyncIds = Awaited<ReturnType<typeof syncV2Api.ids>>;

async function loadRemoteObjects(dek: Uint8Array, optIn: SyncKindOptIn, idsOverride?: SyncIds): Promise<RemoteState> {
  const cache = remoteCaches.get(dek);
  const previous = cache?.objects ?? new Map<string, RemoteObject>();
  let ids = idsOverride ?? await syncV2Api.ids(cache?.head || undefined);
  // A failed object has no cached plaintext. Fetch the full index so it can be retried on refresh.
  if (ids.unchanged && cache?.undecryptableIds.length) ids = await syncV2Api.ids();
  if (ids.unchanged && cache) {
    const objects = [...cache.objects.values()].filter((object) => remoteKindVisible(object.kind, object.plaintext, optIn));
    return { objects, undecryptableIds: cache.undecryptableIds, undecryptableEntries: cache.undecryptableEntries, head: ids.head, maxSeq: ids.max_seq };
  }

  const next = new Map<string, RemoteObject>();
  const pending: { id: string; seq: number; blob_hash: string }[] = [];
  for (const entry of ids.entries ?? []) {
    const existing = previous.get(entry.id);
    if (existing && existing.blobHash === entry.blob_hash) {
      next.set(entry.id, existing);
    } else {
      pending.push(entry);
    }
  }

  const undecryptableIds: string[] = [];
  const undecryptableEntries: UndecryptableEntry[] = [];
  for (let offset = 0; offset < pending.length; offset += 256) {
    const batch = pending.slice(offset, offset + 256);
    const page = await syncV2Api.pull(ids.max_seq, batch.map((entry) => entry.id));
    if (page.head !== ids.head) throw new Error("服务端对象在读取期间发生变化，请重试读取对比结果");
    const pulled = new Map(page.objects.map((object) => [object.id, object]));
    for (const entry of batch) {
      let wire = pulled.get(entry.id);
      if (!wire) {
        // A batch may exceed the response byte budget. A single ID is always
        // returned even when its blob is larger than that budget.
        const single = await syncV2Api.pull(ids.max_seq, [entry.id]);
        if (single.head !== ids.head) throw new Error("服务端对象在读取期间发生变化，请重试读取对比结果");
        wire = single.objects.find((object) => object.id === entry.id);
      }
      if (!wire || wire.seq !== entry.seq) {
        throw new Error(`服务端对象 ${entry.id} 未按同步清单返回，请重试读取对比结果`);
      }
      try {
        const object = await decodeRemoteObject(entry, wire.blob, dek);
        next.set(entry.id, object);
      } catch (e) {
        if (!(e instanceof CryptoError && e.code === "decrypt")) throw e;
        undecryptableIds.push(entry.id);
        undecryptableEntries.push({ id: entry.id, seq: entry.seq, blobHash: entry.blob_hash });
      }
    }
  }
  remoteCaches.set(dek, { head: ids.head, objects: next, undecryptableIds, undecryptableEntries });
  const objects = [...next.values()].filter((object) => remoteKindVisible(object.kind, object.plaintext, optIn));
  return { objects, undecryptableIds, undecryptableEntries, head: ids.head, maxSeq: ids.max_seq };
}

type RowState = "local-only" | "remote-only" | "same" | "local-newer" | "remote-newer" | "local-deleted" | "remote-deleted" | "remote-undecryptable";

interface Row {
  id: string;
  name: string;
  kind: SyncObjectKind | "";
  state: RowState;
  hasLocalRepairCopy?: boolean;
  undecryptableEntry?: UndecryptableEntry;
  local?: LocalEntity;
  remote?: RemoteObject;
}

// An unreadable cloud object needs explicit user confirmation; automatic sync must not treat it as absent.
// Other local-only objects and local winners (including revision ties broken by payload hash) can be pushed.
function computeWinners(local: LocalEntity[], remote: RemoteObject[], undecryptableIds: string[]): LocalEntity[] {
  const remoteById = new Map(remote.map((r) => [r.id, r]));
  const protectedIds = new Set(undecryptableIds);
  return local.filter((e) => {
    if (protectedIds.has(e.id)) return false;
    const r = remoteById.get(e.id);
    return r === undefined || localWins(e, r);
  });
}

// sealAll encrypts winner objects into wire form in kind dependency order (aligned with Go objectKindRank:
// AI permissions, AI grants, and preferences rank before plain tombstones, transcripts still go last).
async function sealAll(dek: Uint8Array, list: LocalEntity[]): Promise<{ id: string; blob: string }[]> {
  const rank = (k: SyncObjectKind) => {
    switch (k) {
      case "group": return 0;
      case "credential": return 1;
      case "snippet": return 2;
      case "asset": return 3;
      case "known_host": return 4;
      case "ai_profile": return 5;
      case "ai_permission": return 6;
      case "ai_grant": return 7;
      case "preference": return 8;
      case "tombstone": return 9;
      case "transcript": return 10;
      default: return 11;
    }
  };
  const ordered = [...list].sort((a, b) => rank(a.kind) - rank(b.kind) || a.id.localeCompare(b.id));
  const objects: { id: string; blob: string }[] = [];
  for (const e of ordered) {
    const blob = await sealSyncObject(dek, utf8Bytes(e.payloadText ?? marshalSyncPayload(e.payload)), e.id, e.kind);
    objects.push({ id: e.id, blob: bytesToBase64(blob) });
  }
  return objects;
}

// localWins replicates the LWW ruling of M117 merge.go: the higher revision wins; ties break by payload sha256 lexicographic order.
// With identical payloads (same hash), the local side needs no push (overwriting identical content).
function localWins(l: LocalEntity, r: RemoteObject): boolean {
  if (l.payloadHash === r.payloadHash) return false;
  if (l.kind === "transcript") return transcriptLocalWins(l, r);
  const lr = revisionOf(l.updatedAt, l.deletedAt);
  const rr = revisionOf(r.updatedAt, r.deletedAt);
  if (lr !== rr) return lr > rr;
  return l.payloadHash > r.payloadHash;
}

// transcriptLocalWins rules by transcript endedAt/immutability and content-completion semantics: complete content beats the omitted version
// (when local is omitted and the cloud is complete, the remote wins and application completes it); otherwise compare by endedAt, ties broken by payload hash.
function transcriptLocalWins(l: LocalEntity, r: RemoteObject): boolean {
  const lp = l.payload as { contentOmitted?: boolean; endedAt?: number };
  const rp = JSON.parse(r.plaintext) as { contentOmitted?: boolean; endedAt?: number };
  const lFull = lp.contentOmitted !== true;
  const rFull = rp.contentOmitted !== true;
  if (lFull !== rFull) return lFull;
  const le = lp.endedAt ?? 0;
  const re = rp.endedAt ?? 0;
  if (le !== re) return le > re;
  return l.payloadHash > r.payloadHash;
}

function buildRows(local: LocalEntity[], remote: RemoteObject[], undecryptableIds: string[], undecryptableEntries: UndecryptableEntry[]): Row[] {
  const remoteById = new Map(remote.map((r) => [r.id, r]));
  const undecryptable = new Set(undecryptableIds);
  const undecryptableById = new Map(undecryptableEntries.map((entry) => [entry.id, entry]));
  const rows: Row[] = [];
  for (const l of local) {
    if (undecryptable.has(l.id)) {
      rows.push({ id: l.id, name: l.name, kind: l.kind, state: "remote-undecryptable", local: l, hasLocalRepairCopy: true, undecryptableEntry: undecryptableById.get(l.id) });
      undecryptable.delete(l.id);
      continue;
    }
    const r = remoteById.get(l.id);
    if (!r) {
      rows.push({ id: l.id, name: l.name, kind: l.kind, state: l.deletedAt !== null ? "local-deleted" : "local-only", local: l });
      continue;
    }
    remoteById.delete(l.id);
    const wins = localWins(l, r);
    let state: RowState;
    if (l.payloadHash === r.payloadHash) {
      state = "same";
    } else if (r.kind === "tombstone" || r.deletedAt !== null) {
      state = wins ? "local-newer" : "remote-deleted";
    } else if (l.deletedAt !== null) {
      state = wins ? "local-deleted" : "remote-newer";
    } else {
      state = wins ? "local-newer" : "remote-newer";
    }
    rows.push({ id: l.id, name: l.name, kind: l.kind, state, local: l, remote: r });
  }
  for (const r of remoteById.values()) {
    rows.push({ id: r.id, name: r.name, kind: r.kind, state: r.kind === "tombstone" || r.deletedAt !== null ? "remote-deleted" : "remote-only", remote: r });
  }
  for (const id of undecryptable) {
    rows.push({ id, name: id, kind: "", state: "remote-undecryptable", undecryptableEntry: undecryptableById.get(id) });
  }
  const rank = (s: RowState) => (s === "same" ? 1 : 0);
  return rows.sort((a, b) => rank(a.state) - rank(b.state) || a.name.localeCompare(b.name));
}

export { WEB_SYNC_ERROR_EVENT, WEB_SYNC_REQUEST_EVENT, WEB_SYNC_RESULT_EVENT };

interface Snapshot {
  local: LocalEntity[];
  remote: RemoteObject[];
  undecryptableIds: string[];
  undecryptableEntries: UndecryptableEntry[];
  warnings: string[];
  head: string;
  maxSeq: number;
}

// Re-read the current cloud object before a manual repair. A stale compare row must never
// overwrite an object that another device has since repaired or replaced.
async function currentUndecryptableObject(expected: UndecryptableEntry, dek: Uint8Array): Promise<{ head: string; blobHash: string }> {
  const ids = await syncV2Api.ids();
  const entry = ids.entries?.find((candidate) => candidate.id === expected.id);
  if (!entry) throw new Error("服务端对象已不存在，请重试读取对比结果");
  if (entry.seq !== expected.seq || entry.blob_hash !== expected.blobHash) {
    throw new Error("服务端对象在对比后发生变化，请刷新对比结果");
  }
  const pulled = await syncV2Api.pull(ids.max_seq, [expected.id]);
  const wire = pulled.objects.find((candidate) => candidate.id === expected.id);
  if (pulled.head !== ids.head || !wire || wire.seq !== entry.seq) {
    throw new Error("服务端对象在检查期间发生变化，请重试读取对比结果");
  }
  try {
    await decodeRemoteObject(entry, wire.blob, dek);
  } catch (error) {
    if (!(error instanceof CryptoError && error.code === "decrypt")) throw error;
    return { head: ids.head, blobHash: entry.blob_hash };
  }
  throw new Error("服务端对象现已可读取，请刷新对比结果后再操作");
}

export interface WebSyncResult extends Snapshot {
  applied: number;
  pushed: number;
  lastSuccessAt: number;
}

function snapshotFromParts(local: LocalCollection, remote: RemoteState): Snapshot {
  return {
    local: local.entities,
    remote: remote.objects,
    undecryptableIds: remote.undecryptableIds,
    undecryptableEntries: remote.undecryptableEntries,
    warnings: [...local.warnings, ...remote.undecryptableIds.map((id) =>
      local.entities.some((entity) => entity.id === id)
        ? `服务端对象 ${id} 解密失败，已跳过自动覆盖；可在对比详情中手动确认用本机同 ID 副本修复`
        : `服务端对象 ${id} 解密失败，已跳过；本次对比没有可用的同 ID 本机副本，可在对比详情中重试读取或确认清除服务端副本；也可从持有正确数据密钥的设备重新同步`)],
    head: remote.head,
    maxSeq: remote.maxSeq,
  };
}

function remoteStateFromSnapshot(snapshot: Snapshot): RemoteState {
  return {
    objects: snapshot.remote,
    undecryptableIds: snapshot.undecryptableIds,
    undecryptableEntries: snapshot.undecryptableEntries,
    head: snapshot.head,
    maxSeq: snapshot.maxSeq,
  };
}

async function loadSnapshot(dek: Uint8Array, optIn: SyncKindOptIn, idsOverride?: SyncIds): Promise<Snapshot> {
  const [local, remote] = await Promise.all([loadLocalEntities(optIn, dek), loadRemoteObjects(dek, optIn, idsOverride)]);
  return snapshotFromParts(local, remote);
}

function computeApplySet(local: LocalEntity[], remote: RemoteObject[]): RemoteObject[] {
  return remote.filter((r) => {
    const l = local.find((entity) => entity.id === r.id);
    if (!l) return true;
    if (l.payloadHash === r.payloadHash) return false;
    return !localWins(l, r);
  });
}

async function optInMatches(optIn: SyncKindOptIn, shouldContinue: () => boolean): Promise<boolean> {
  if (!shouldContinue()) return false;
  const current = await syncApi.kindOptInGet();
  return current.knownHost === optIn.knownHost && current.aiProfile === optIn.aiProfile && !!current.aiPermission === !!optIn.aiPermission && !!current.aiGrant === !!optIn.aiGrant && !!current.preferences === !!optIn.preferences && shouldContinue();
}

export async function runWebSync(
  dek: Uint8Array,
  optIn: SyncKindOptIn,
  shouldContinue: () => boolean = () => true,
): Promise<WebSyncResult | null> {
  let localWarnings: string[] = [];
  const [initialLocal, initialRemote] = await Promise.all([
    loadLocalEntities(optIn, dek),
    loadRemoteObjects(dek, optIn),
  ]);
  localWarnings = initialLocal.warnings;
  let snapshot = snapshotFromParts(initialLocal, initialRemote);
  const operationWarnings: string[] = [];
  const reloadLocal = async () => {
    const local = await loadLocalEntities(optIn, dek);
    localWarnings = local.warnings;
    snapshot = snapshotFromParts(local, remoteStateFromSnapshot(snapshot));
  };
  const reloadRemote = async (idsOverride?: SyncIds) => {
    const remote = await loadRemoteObjects(dek, optIn, idsOverride);
    snapshot = snapshotFromParts({ entities: snapshot.local, warnings: localWarnings }, remote);
  };
  if (!(await optInMatches(optIn, shouldContinue))) return null;

  const applySet = computeApplySet(snapshot.local, snapshot.remote);
  let applied = 0;
  for (let i = 0; i < applySet.length; i += 256) {
    if (!(await optInMatches(optIn, shouldContinue))) return null;
    const result = await syncApi.applyObjects(
      applySet.slice(i, i + 256).map((r) => ({
        id: r.id,
        kind: r.kind,
        payload: JSON.parse(r.plaintext) as import("../../ipc/types").JsonValue,
      })),
    );
    applied += result.applied;
    for (const object of result.objects) {
      if (object.warning) operationWarnings.push(`${object.id}: ${object.warning}`);
    }
  }
  // Applying changes only the local replica; the cloud head and objects are unchanged.
  if (applySet.length > 0) await reloadLocal();
  if (!(await optInMatches(optIn, shouldContinue))) return null;

  let winners = computeWinners(snapshot.local, snapshot.remote, snapshot.undecryptableIds);
  let pushed = 0;
  if (winners.length > 0) {
    const fresh = await syncV2Api.ids();
    if (!(await optInMatches(optIn, shouldContinue))) return null;
    if (fresh.head !== snapshot.head) {
      await reloadRemote(fresh);
      winners = computeWinners(snapshot.local, snapshot.remote, snapshot.undecryptableIds);
    }
    if (winners.length > 0) {
      let response: Awaited<ReturnType<typeof syncV2Api.push>>;
      let remoteRefreshed = false;
      try {
        response = await syncV2Api.push(snapshot.head, await sealAll(dek, winners));
      } catch (e) {
        if (!(e instanceof AuthApiError && e.status === 409)) throw e;
        const latest = await syncV2Api.ids();
        await reloadRemote(latest);
        winners = computeWinners(snapshot.local, snapshot.remote, snapshot.undecryptableIds);
        if (winners.length === 0) return { ...snapshot, applied, pushed: 0, lastSuccessAt: Date.now() };
        if (!(await optInMatches(optIn, shouldContinue))) return null;
        response = await syncV2Api.push(snapshot.head, await sealAll(dek, winners));
      }
      if (response.applied === 0 && response.skipped === 0) {
        const latest = await syncV2Api.ids();
        await reloadRemote(latest);
        remoteRefreshed = true;
        winners = computeWinners(snapshot.local, snapshot.remote, snapshot.undecryptableIds);
        if (winners.length > 0) {
          if (!(await optInMatches(optIn, shouldContinue))) return null;
          response = await syncV2Api.push(snapshot.head, await sealAll(dek, winners));
          remoteRefreshed = false;
        }
      }
      pushed = response.applied;
      // A successful push changes only the cloud; retain the local collection and refresh remote state.
      if (!remoteRefreshed) await reloadRemote();
    }
  }
  return { ...snapshot, warnings: [...new Set([...snapshot.warnings, ...operationWarnings])], applied, pushed, lastSuccessAt: Date.now() };
}

function WebSyncConsole() {
  const user = useAuth((s) => s.user);
  const dek = useAuth((s) => s.dek);

  // The whole card renders nothing when logged out: the settings page has only one login entry in AuthCard "account"; no duplicate login button in the sync card.
  if (!user) return null;
  if (!dek) return <UnlockDEKCard />;
  return <CompareConsole />;
}

function UnlockDEKCard() {
  const unlockDEK = useAuth((s) => s.unlockDEK);
  const error = useAuth((s) => s.error);
  const clearError = useAuth((s) => s.clearError);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const submit = async () => {
    setFormError(null);
    clearError();
    setBusy(true);
    try {
      await unlockDEK(password);
      setPassword("");
    } catch (e) {
      setFormError(describeError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconLock size={15} className="text-neutral-400" />
        <span className="nx-card-title">解锁数据密钥</span>
      </div>
      <p className="nx-hint mb-3">
        输入账号密码在本机解锁数据密钥（约需几秒），密钥不会离开本机。
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <input
          className="nx-input min-w-0 flex-1 max-w-[280px]"
          type="password"
          placeholder="账号密码"
          value={password}
          autoComplete="current-password"
          onChange={(e) => setPassword(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && password) void submit();
          }}
        />
        <button className="nx-btn nx-btn-primary" disabled={busy || !password} onClick={() => void submit()}>
          {busy ? "解锁中…" : "解锁"}
        </button>
      </div>
      {formError && (
        <div className="nx-alert nx-alert-danger mt-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span>{formError}</span>
        </div>
      )}
      {!formError && error && (
        <div className="nx-alert nx-alert-danger mt-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span>{describeError(error)}</span>
        </div>
      )}
    </section>
  );
}

function CompareConsole() {
  const dek = useAuth((s) => s.dek)!;
  const [local, setLocal] = useState<LocalEntity[] | null>(null);
  const [remote, setRemote] = useState<RemoteObject[] | null>(null);
  const [undecryptableIds, setUndecryptableIds] = useState<string[]>([]);
  const [undecryptableEntries, setUndecryptableEntries] = useState<UndecryptableEntry[]>([]);
  const [collectWarnings, setCollectWarnings] = useState<string[]>([]);
  const [refreshBusy, setRefreshBusy] = useState(false);
  const [repairBusyId, setRepairBusyId] = useState<string | null>(null);
  const [optInBusy, setOptInBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [repairError, setRepairError] = useState<string | null>(null);
  const [kindOptIn, setKindOptIn] = useState<SyncKindOptIn | null>(null);
  const [lastSuccessAt, setLastSuccessAt] = useState<number | null>(null);
  const optInEpochRef = useRef(0);
  const loadSeqRef = useRef(0);
  const repairBusyRef = useRef(false);
  const kindOptInRef = useRef<SyncKindOptIn | null>(null);

  const load = useCallback((view: SyncKindOptIn, epoch: number) => {
    const seq = ++loadSeqRef.current;
    setRefreshBusy(true);
    setError(null);
    return loadSnapshot(dek, view)
      .then((snapshot) => {
        if (seq !== loadSeqRef.current || epoch !== optInEpochRef.current) return;
        setLocal(snapshot.local);
        setRemote(snapshot.remote);
        setUndecryptableIds(snapshot.undecryptableIds);
        setUndecryptableEntries(snapshot.undecryptableEntries);
        setCollectWarnings(snapshot.warnings);
      })
      .catch((e: unknown) => {
        if (seq === loadSeqRef.current && epoch === optInEpochRef.current) setError(describeError(e));
      })
      .finally(() => {
        if (seq === loadSeqRef.current) setRefreshBusy(false);
      });
  }, [dek]);

  useEffect(() => {
    let cancelled = false;
    const epoch = optInEpochRef.current;
    setRefreshBusy(true);
    Promise.resolve()
      .then(() => syncApi.kindOptInGet())
      .then((view) => {
        if (cancelled || epoch !== optInEpochRef.current) return;
        kindOptInRef.current = view;
        setKindOptIn(view);
        void load(view, epoch);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(describeError(e));
      });

    const onResult = (event: Event) => {
      const result = (event as CustomEvent<WebSyncResult>).detail;
      setLocal(result.local);
      setRemote(result.remote);
      setUndecryptableIds(result.undecryptableIds);
      setUndecryptableEntries(result.undecryptableEntries);
      setCollectWarnings(result.warnings);
      setLastSuccessAt(result.lastSuccessAt);
      setError(null);
      setRepairError(null);
    };
    const onError = (event: Event) => {
      setError(describeError((event as CustomEvent<unknown>).detail));
    };
    window.addEventListener(WEB_SYNC_RESULT_EVENT, onResult);
    window.addEventListener(WEB_SYNC_ERROR_EVENT, onError);
    return () => {
      cancelled = true;
      loadSeqRef.current += 1;
      window.removeEventListener(WEB_SYNC_RESULT_EVENT, onResult);
      window.removeEventListener(WEB_SYNC_ERROR_EVENT, onError);
    };
  }, [load]);

  const rows = useMemo(() => (local && remote ? buildRows(local, remote, undecryptableIds, undecryptableEntries) : null), [local, remote, undecryptableIds, undecryptableEntries]);

  const repairUndecryptable = async (id: string, action: UndecryptableAction) => {
    if (repairBusyRef.current || refreshBusy) return;
    const view = kindOptInRef.current;
    if (!view) return;
    const epoch = optInEpochRef.current;
    const userID = useAuth.getState().user?.id;
    const stillCurrent = () => epoch === optInEpochRef.current &&
      useAuth.getState().user?.id === userID && useAuth.getState().dek === dek;
    repairBusyRef.current = true;
    setRepairBusyId(id);
    setRepairError(null);
    try {
      if (action === "retry") {
        await load(view, epoch);
        return;
      }
      const row = rows?.find((candidate) => candidate.id === id && candidate.state === "remote-undecryptable");
      if (!row) throw new Error("对比结果已变化，请重试读取");
      if (!row.undecryptableEntry) throw new Error("对比结果缺少服务端版本，请刷新对比结果");
      if (action === "restore-local") {
        if (!row.local) throw new Error("本机已没有同 ID 副本，请重试读取");
        const { ask } = await import("../../ui/dialogs");
        if (!(await ask(`将本机「${row.name}」覆盖服务端无法解密的对象？此操作会替换服务端原有密文。`, {
          title: "用本机副本修复服务端对象", kind: "warning",
        }))) return;
        if (!stillCurrent()) return;
        const localNow = await loadLocalEntities(view, dek);
        const replacement = localNow.entities.find((entity) => entity.id === id);
        if (!replacement) throw new Error("本机同 ID 副本已不存在，请重试读取");
        if (replacement.id !== row.local.id || replacement.kind !== row.local.kind ||
          replacement.payloadHash !== row.local.payloadHash || replacement.deletedAt !== row.local.deletedAt ||
          replacement.updatedAt !== row.local.updatedAt) {
          throw new Error("本机副本在对比后发生变化，请刷新对比结果");
        }
        const cloud = await currentUndecryptableObject(row.undecryptableEntry, dek);
        if (!stillCurrent()) return;
        const objects = await sealAll(dek, [replacement]);
        if (!stillCurrent()) return;
        if (!(await optInMatches(view, stillCurrent))) throw new Error("内容类型设置已变化，请刷新对比结果");
        const response = await syncV2Api.push(cloud.head, objects);
        if (response.applied !== 1) throw new Error("服务端未确认修复，请重试读取对比结果");
        remoteCaches.delete(dek);
        await load(view, epoch);
        return;
      }
      if (row.local) throw new Error("本机有同 ID 副本，请先重试读取并选择本机修复");
      const { ask } = await import("../../ui/dialogs");
      if (!(await ask(`本次对比没有可用的本机对象 ${id}。确认清除这条无法解密、类型未知的服务端副本？其他设备若仍保存旧的本地副本，之后同步可能重新上传。`, {
        title: "清除无法解密的服务端副本", kind: "warning",
      }))) return;
      if (!stillCurrent()) return;
      const localNow = await loadLocalEntities({ knownHost: true, aiProfile: true, aiPermission: true, aiGrant: true, preferences: true }, dek);
      if (localNow.warnings.length > 0) {
        throw new Error("本机内容读取不完整，请先解锁凭据库或修复读取错误，再刷新对比结果");
      }
      if (localNow.entities.some((entity) => entity.id === id)) {
        throw new Error("本机现在已有同 ID 副本，请重试读取并选择本机修复");
      }
      if (await localTranscriptExists(id)) {
        throw new Error("本机现在已有同 ID 会话记录，请先核查该记录的同步设置");
      }
      const cloud = await currentUndecryptableObject(row.undecryptableEntry, dek);
      if (!stillCurrent()) return;
      if (!(await optInMatches(view, stillCurrent))) throw new Error("内容类型设置已变化，请刷新对比结果");
      const response = await syncV2Api.deleteObject(cloud.head, id, cloud.blobHash);
      if (!response.deleted) throw new Error("服务端未确认清除，请重试读取对比结果");
      remoteCaches.delete(dek);
      await load(view, epoch);
    } catch (cause) {
      if (stillCurrent()) {
        setRepairError(cause instanceof AuthApiError && cause.status === 409
          ? "服务端数据已变化，请重试读取后再次操作"
          : describeError(cause));
      }
    } finally {
      repairBusyRef.current = false;
      setRepairBusyId(null);
    }
  };

  const updateKindOptIn = async (patch: Partial<SyncKindOptIn>) => {
    const epoch = ++optInEpochRef.current;
    setOptInBusy(true);
    setRefreshBusy(true);
    setLocal(null);
    setRemote(null);
    setUndecryptableIds([]);
    setUndecryptableEntries([]);
    setCollectWarnings([]);
    setError(null);
    setRepairError(null);
    try {
      const next = await syncApi.kindOptInSet(patch);
      if (epoch !== optInEpochRef.current) return;
      kindOptInRef.current = next;
      setKindOptIn(next);
      await load(next, epoch);
      window.dispatchEvent(new Event(WEB_SYNC_REQUEST_EVENT));
    } catch (e) {
      if (epoch === optInEpochRef.current) {
        setError(describeError(e));
        const current = kindOptInRef.current;
        if (current) await load(current, epoch);
      }
    } finally {
      if (epoch === optInEpochRef.current) setOptInBusy(false);
    }
  };

  const winners = useMemo(() => {
    if (!local || !remote) return [];
    return computeWinners(local, remote, undecryptableIds);
  }, [local, remote, undecryptableIds]);

  const applySet = useMemo(() => {
    if (!local || !remote) return [];
    return computeApplySet(local, remote);
  }, [local, remote]);


  const pushCount = winners.length;

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconServer size={15} className="text-neutral-400" />
        <span className="nx-card-title">{SYNC_CONSOLE_TITLE}</span>
        <span className="nx-badge nx-badge-green">{SYNC_AUTO_BADGE}</span>
        <span className="nx-hint">服务端 {remote ? remote.length + undecryptableIds.length : "…"} 个对象</span>
        <div className="nx-spacer" />
        <span className="nx-hint">{SYNC_INTERVAL_LABEL}</span>
        <button className="nx-btn nx-btn-outline nx-btn-sm" disabled={refreshBusy || repairBusyId !== null || !kindOptIn}
          onClick={() => {
            const view = kindOptInRef.current;
            if (view) void load(view, optInEpochRef.current);
          }}>
          <IconRefresh size={12} className={refreshBusy ? "animate-spin" : ""} />
          刷新对比
        </button>
      </div>

      <p className="nx-hint mb-3">
        {SYNC_MERGE_GUIDE}
        {lastSuccessAt !== null && `上次同步：${formatTime(lastSuccessAt)}。`}
        {refreshBusy ? "正在读取本机与服务端数据…" : `当前待应用 ${applySet.length} 项，待推送 ${pushCount} 项。`}
      </p>

      {kindOptIn && <SyncOptInSection value={kindOptIn} busy={optInBusy} onChange={(patch) => void updateKindOptIn(patch)} />}

      {error && (
        <div className="nx-alert nx-alert-danger mb-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">同步失败：{error}，将在下个自动同步周期重试。</span>
        </div>
      )}

      {repairError && (
        <div className="nx-alert nx-alert-danger mb-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">操作失败：{repairError}</span>
        </div>
      )}

      {collectWarnings.length > 0 && (
        <div className="nx-alert nx-alert-info mb-3 flex items-start gap-2">
          <IconInfo size={13} className="mt-0.5 shrink-0" />
          <div className="min-w-0 flex-1 text-amber-300">
            {collectWarnings.map((warning, index) => (
              <div key={index} className="break-words">
                {warning}
              </div>
            ))}
          </div>
        </div>
      )}

      <CompareDetails rows={rows} error={error !== null} actionsBusy={repairBusyId !== null || refreshBusy}
        onUndecryptableAction={(id, action) => void repairUndecryptable(id, action)} />

      <div className="nx-alert nx-alert-info mt-3 flex items-start gap-2">
        <IconInfo size={14} className="mt-0.5 shrink-0" />
        <div>
          终端录像默认不同步，可在「终端历史」中单独开启。关闭上方内容类型开关不会删除服务端已有副本；在「终端历史」关闭单条录像同步会删除其服务端副本，本机记录保留。冲突按最后修改时间裁决，请保持各设备时钟准确，否则「较新」判定可能不符合预期。
        </div>
      </div>
    </section>
  );
}
