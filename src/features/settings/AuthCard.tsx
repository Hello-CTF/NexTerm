// Personal account card: current user, change password (re-wrap DEK), two-factor auth (TOTP), recovery key, device management, logout.
// Logged out, it is the settings page's only login entry (WEB); the desktop identity comes from the "account sync" link server session
// (see ../auth/desktopAccount.ts); after login it renders account info/password change/device management (all via the remoteAccountApi proxy);
// TOTP is not covered by the proxy, so it is honestly hidden with a browser-management note; renders nothing when logged out - the login entry is in SyncCard.

import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { authApi, authAvailable, type AccountDevice, type AccountLoginSession, type TotpSetup, type TotpStatus } from "../../ipc/authApi";
import { remoteAccountApi } from "../../ipc/remoteApi";
import { DESKTOP } from "../../ipc/env";
import { useAuth } from "../auth/store";
import { useDesktopAccount, useDesktopAccountScope } from "../auth/desktopAccount";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { copyText as copyToClipboard } from "../../ui/clipboard";
import { describeError } from "../../ui/errorText";
import { formatTime } from "../../ui/format";
import { ServerSecurityLink } from "./ServerSecurityLink";
import { ListPager, usePagedList } from "./ListPager";
import { PasswordChangeForm } from "../../ui/PasswordChangeForm";
import {
  IconCheckCircle,
  IconCopy,
  IconInfo,
  IconKey,
  IconLock,
  IconRefresh,
  IconShield,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";

const desktopPendingRecoveryKeys = new Map<string, { formatted: string; canonical: string }>();

export function AuthCard() {
  const pushToast = useUi((s) => s.pushToast);
  const qc = useQueryClient();
  const sessionUser = useAuth((s) => s.user);
  // The desktop identity comes from the "account sync" link server session (useAuth holds no user on desktop).
  const desktopAccount = useDesktopAccount();
  const user = DESKTOP ? desktopAccount : sessionUser;
  const desktopScope = useDesktopAccountScope(user);
  const accountKey = DESKTOP ? desktopScope : user ? `${user.id}:${user.username}` : null;
  const accountKeyRef = useRef(accountKey);
  accountKeyRef.current = accountKey;
  const devicesRequest = useRef(0);
  const sessionsRequest = useRef(0);
  const passwordRequest = useRef(0);
  const status = useAuth((s) => s.status);
  const gate = useAuth((s) => s.gate);
  const changePassword = useAuth((s) => s.changePassword);
  const logout = useAuth((s) => s.logout);
  const pendingRecoveryKey = useAuth((s) => s.pendingRecoveryKey);
  const clearPendingRecoveryKey = useAuth((s) => s.clearPendingRecoveryKey);
  const authError = useAuth((s) => s.error);
  const cleanupError = useAuth((s) => s.cleanupError);
  const clearError = useAuth((s) => s.clearError);
  const clearCleanupError = useAuth((s) => s.clearCleanupError);
  const visibleAccountError = authError ?? cleanupError;

  // Platform-hosted mode (Lazycat): the account is auto-established with the platform login; the user holds no password and should not have a "logout".
  // The next refresh after logout would be taken over by the platform session again; keeping the button only makes people think "logout" worked.
  const platformManaged = status?.auth === "platform";

  const [deviceResult, setDeviceResult] = useState<{ accountKey: string; devices: AccountDevice[] } | null>(null);
  const [deviceFailure, setDeviceFailure] = useState<{ accountKey: string; error: string } | null>(null);
  const [sessionResult, setSessionResult] = useState<{ accountKey: string; sessions: AccountLoginSession[] } | null>(null);
  const [sessionFailure, setSessionFailure] = useState<{ accountKey: string; error: string } | null>(null);
  const [desktopRecoveryState, setDesktopRecoveryState] = useState<{
    accountKey: string;
    recovery: { formatted: string; canonical: string };
  } | null>(null);
  const devices = deviceResult?.accountKey === accountKey ? deviceResult.devices : null;
  const [showRevoked, setShowRevoked] = useState(false);
  const revokedCount = devices?.filter((d) => d.revoked_at !== 0).length ?? 0;
  const visibleDevices = (devices ?? []).filter((d) => showRevoked || d.revoked_at === 0);
  const devicePager = usePagedList(visibleDevices);
  const devicesError = deviceFailure?.accountKey === accountKey ? deviceFailure.error : null;
  const sessions = sessionResult?.accountKey === accountKey ? sessionResult.sessions : null;
  const sessionPager = usePagedList(sessions ?? []);
  const sessionsError = sessionFailure?.accountKey === accountKey ? sessionFailure.error : null;
  const visibleRecoveryKey = DESKTOP
    ? accountKey
      ? desktopRecoveryState?.accountKey === accountKey
        ? desktopRecoveryState.recovery
        : desktopPendingRecoveryKeys.get(accountKey) ?? null
      : null
    : pendingRecoveryKey;

  const [pwdOpen, setPwdOpen] = useState(false);

  useEffect(() => {
    passwordRequest.current += 1;
    setPwdOpen(false);
  }, [accountKey]);

  const loadDevices = useCallback(() => {
    if (!accountKey) return Promise.resolve();
    const request = ++devicesRequest.current;
    setDeviceFailure(null);
    const fetchDevices = DESKTOP ? remoteAccountApi.devices : authApi.devices;
    return fetchDevices()
      .then((r) => {
        if (accountKeyRef.current === accountKey && devicesRequest.current === request) {
          setDeviceResult({ accountKey, devices: r.devices });
        }
      })
      .catch((e: unknown) => {
        if (accountKeyRef.current === accountKey && devicesRequest.current === request) {
          setDeviceFailure({ accountKey, error: describeError(e) });
        }
      });
  }, [accountKey]);

  useEffect(() => {
    devicesRequest.current += 1;
    setDeviceResult(null);
    setDeviceFailure(null);
    void loadDevices();
  }, [loadDevices]);

  const loadSessions = useCallback(async () => {
    if (!accountKey || DESKTOP) return;
    const request = ++sessionsRequest.current;
    setSessionFailure(null);
    try {
      const result = await authApi.sessions();
      if (accountKeyRef.current === accountKey && sessionsRequest.current === request) {
        setSessionResult({ accountKey, sessions: result.sessions });
      }
    } catch (e) {
      if (accountKeyRef.current === accountKey && sessionsRequest.current === request) {
        setSessionFailure({ accountKey, error: describeError(e) });
      }
    }
  }, [accountKey]);

  useEffect(() => {
    sessionsRequest.current += 1;
    setSessionResult(null);
    setSessionFailure(null);
    void loadSessions();
  }, [loadSessions]);

  // submitPassword runs from PasswordChangeForm, which validates the pair and
  // shows a thrown error inline. A completion after an account switch is ignored.
  const submitPassword = async (oldPassword: string, newPassword: string) => {
    if (!accountKey) return;
    const actionAccount = accountKey;
    const request = ++passwordRequest.current;
    const stillCurrent = () => accountKeyRef.current === actionAccount && passwordRequest.current === request;
    if (DESKTOP) {
      // Desktop: the DEK re-wrap happens inside the Go engine session; the recovery key is issued only in this response, so show it immediately.
      const r = await remoteAccountApi.changePassword(oldPassword, newPassword);
      if (r.recoveryKey) {
        const recovery = { formatted: r.recoveryKey, canonical: r.recoveryKey };
        desktopPendingRecoveryKeys.set(actionAccount, recovery);
        if (stillCurrent()) setDesktopRecoveryState({ accountKey: actionAccount, recovery });
      }
    } else {
      await changePassword(oldPassword, newPassword);
    }
    if (!stillCurrent()) return;
    setPwdOpen(false);
    pushToast("success", "密码已修改，其他设备上的会话已退出");
  };

  const revokeDevice = async (device: AccountDevice) => {
    const actionAccount = accountKey;
    const ok = await ask(`吊销设备「${device.name}」？\n\n吊销后该设备立即退出登录，不可恢复。`, {
      title: "吊销设备",
      kind: "warning",
    });
    if (!ok || !actionAccount || accountKeyRef.current !== actionAccount) return;
    try {
      if (DESKTOP) {
        await remoteAccountApi.deviceRevoke(device.id);
      } else {
        await authApi.deviceRevoke(device.id);
      }
      if (accountKeyRef.current !== actionAccount) return;
      pushToast("success", `已吊销「${device.name}」`);
      void loadDevices();
    } catch (e) {
      if (accountKeyRef.current === actionAccount) pushToast("error", describeError(e));
    }
  };

  const revokeSession = async (session: AccountLoginSession) => {
    const actionAccount = accountKey;
    const ok = await ask(
      `退出登录会话「${session.name}」？${session.current ? "\n\n当前浏览器将立即退出登录。" : "\n\n该会话将立即失效。"}`,
      { title: "退出登录会话", kind: "warning" },
    );
    if (!ok || !actionAccount || accountKeyRef.current !== actionAccount) return;
    try {
      if (session.current) await logout();
      else await authApi.sessionRevoke(session.id);
      if (accountKeyRef.current !== actionAccount) return;
      if (session.current) {
        void qc.invalidateQueries();
      } else {
        setSessionResult((current) => current?.accountKey === actionAccount
          ? { accountKey: actionAccount, sessions: current.sessions.filter((item) => item.id !== session.id) }
          : current);
        pushToast("success", `已退出「${session.name}」`);
      }
    } catch (e) {
      if (accountKeyRef.current === actionAccount) pushToast("error", describeError(e));
    }
  };

  if (!user || (DESKTOP && !desktopScope)) {
    // The desktop has no account gate; a login button would only open a gate that does not exist: the whole card does not render, and the login entry is in SyncCard.
    if (!authAvailable()) return null;
    if (gate === "loading") {
      return <section className="nx-card"><p role="status" className="nx-hint">正在检查登录状态…</p></section>;
    }
    if (status?.auth === "platform") {
      // Nothing to fill in under platform-hosted mode: the account is auto-established with the platform login. Reaching here means the platform session was not established
      // (typically direct access to the server port, bypassing the platform entry); give the reason instead of a dead button.
      return (
        <section className="nx-card">
          <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
            <IconKey size={15} className="text-neutral-400" />
            <span className="nx-card-title">账号</span>
          </div>
          <p className="nx-hint mb-3">
            本实例部署在懒猫平台上，账号随平台登录自动建立，这里没有需要填写的初始化码或密码。
            若看到此提示，请改用平台访问地址重新打开。
          </p>
        </section>
      );
    }
    if (status?.auth === "off") {
      // With auth=off, all /auth/* calls 403 (store.refresh synthesizes the auth=off state); any login/init entry would be a dead end.
      return (
        <section className="nx-card">
          <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
            <IconKey size={15} className="text-neutral-400" />
            <span className="nx-card-title">账号</span>
          </div>
          <p className="nx-hint mb-3">
            这台服务器关闭了账号功能（--auth=off），不提供登录、注册与账号同步；其他本地功能不受影响。
          </p>
        </section>
      );
    }
    if (status && !status.initialized) {
      // Uninitialized (anonymous users of a loopback passwordless instance can reach here directly): explicit init entry for users who want an account.
      return (
        <section className="nx-card">
          <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
            <IconKey size={15} className="text-neutral-400" />
            <span className="nx-card-title">账号</span>
          </div>
          <p className="nx-hint mb-3">
            这台服务器还没有任何账号{status.auth === "loopback" ? "，当前允许匿名使用" : ""}。
            初始化后创建超级管理员，资产、分组、片段会端到端加密同步到你的账号，在其他设备上可用；
            也可以继续匿名本地使用。
          </p>
          <button
            className="nx-btn nx-btn-primary nx-btn-sm"
            onClick={() => useAuth.setState({ gate: "setup" })}
          >
            <IconShield size={12} />
            初始化账号
          </button>
        </section>
      );
    }
    return (
      <section className="nx-card">
        <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
          <IconKey size={15} className="text-neutral-400" />
          <span className="nx-card-title">账号</span>
        </div>
        <p className="nx-hint mb-3">
          当前未登录{status?.auth === "loopback" ? "(这台服务器允许匿名使用)" : ""}。
          登录后，数据会自动在本机与服务端之间同步；不登录也能继续本地使用。
        </p>
        <button
          className="nx-btn nx-btn-primary nx-btn-sm"
          onClick={() => useAuth.setState({ gate: "login" })}
        >
          <IconLock size={12} />
          登录
        </button>
      </section>
    );
  }

  return (
    <>
      <section className="nx-card">
        <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
          <IconKey size={15} className="text-neutral-400" />
          <span className="nx-card-title">账号</span>
          <span className={`nx-badge ${user.role === "superadmin" ? "nx-badge-blue" : ""}`}>
            {user.role === "superadmin" ? "超级管理员" : "普通用户"}
          </span>
          {user.state === "reset_required" && <span className="nx-badge nx-badge-amber">待重置密码</span>}
        </div>

        <div className="flex flex-wrap items-center gap-x-6 gap-y-1 text-[12.5px] text-neutral-200">
          <span>
            用户名 <b>{user.username}</b>
          </span>
          {user.display_name && <span>显示名 {user.display_name}</span>}
          {user.last_login_at > 0 && <span className="nx-hint">最近登录 {formatTime(user.last_login_at)}</span>}
        </div>

        {visibleAccountError && (
          <div className="nx-alert nx-alert-danger mt-3 flex items-start gap-2" role="alert">
            <IconXCircle size={13} className="mt-0.5 shrink-0" />
            <span className="min-w-0 flex-1 break-words">{describeError(visibleAccountError)}</span>
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
              onClick={() => {
                if (cleanupError && authError === cleanupError) clearCleanupError();
                else if (authError) clearError();
                else clearCleanupError();
              }}
            >
              知道了
            </button>
          </div>
        )}

        {platformManaged && (
          <p className="nx-hint mt-2">
            本实例部署在懒猫平台上，登录由平台负责：打开地址即为当前账号，不需要在这里登录或退出。
            账号随平台登录自动建立，用于承载设备、分享等需要归属的功能。
          </p>
        )}

        {DESKTOP && (
          <p className="nx-hint mt-2">
            本账号由「服务端连接」管理，登出后可切换账号。
            两步验证管理请在浏览器中打开服务端 Web 界面。
          </p>
        )}

        {!platformManaged && !DESKTOP && (
          <div className="mt-3 flex flex-wrap items-center gap-2">
            <button
              className="nx-btn nx-btn-outline nx-btn-sm"
              onClick={() =>
                void (async () => {
                  const ok = await ask("退出当前账号？\n\n本机浏览器会话立即失效。", { title: "退出登录" });
                  if (!ok) return;
                  await logout();
                  void qc.invalidateQueries();
                })()
              }
            >
              退出登录
            </button>
            <button
              className="nx-btn nx-btn-ghost nx-btn-sm"
              onClick={() =>
                void (async () => {
                  const ok = await ask("退出所有设备上的会话？\n\n包括这台浏览器，退出后需重新登录。", {
                    title: "退出全部会话",
                    kind: "warning",
                  });
                  if (!ok) return;
                  try {
                    await logout({ all: true });
                    void qc.invalidateQueries();
                  } catch (e) {
                    pushToast("error", describeError(e));
                  }
                })()
              }
            >
              退出全部会话
            </button>
          </div>
        )}
      </section>

      {!platformManaged && (
        <section className="nx-card">
          <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
            <IconLock size={15} className="text-neutral-400" />
            <span className="nx-card-title">修改密码</span>
          </div>
          <p className="nx-hint mb-3">
            修改密码会用新密码重新加密数据密钥，服务端已保存的加密内容不受影响。修改成功后会签发新的恢复密钥（旧的立即作废），并退出其他设备上的会话。
          </p>
          {pwdOpen ? (
            <PasswordChangeForm
              requireCurrent
              submitLabel="确认修改"
              onSubmit={submitPassword}
              onCancel={() => setPwdOpen(false)}
            />
          ) : (
            <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => setPwdOpen(true)}>
              修改密码
            </button>
          )}
        </section>
      )}

      {DESKTOP ? (
        <section className="nx-card">
          <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
            <IconShield size={15} className="text-neutral-400" />
            <span className="nx-card-title">两步验证（TOTP）</span>
          </div>
          <p className="nx-hint">
            两步验证的绑定、换绑与关闭暂未接入桌面端，请在浏览器中打开服务端 Web 界面管理；开启后对桌面端登录同样生效。
          </p>
          <ServerSecurityLink />
        </section>
      ) : (
        <TotpCard />
      )}

      {visibleRecoveryKey && (
        <section className="nx-card">
          <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
            <IconShield size={15} className="text-amber-300" />
            <span className="nx-card-title">新的恢复密钥（仅显示一次）</span>
          </div>
          <div className="nx-alert nx-alert-danger flex flex-col gap-2">
            <code className="nx-code break-all font-mono text-[12.5px]">{visibleRecoveryKey.formatted}</code>
            <div className="flex flex-wrap items-center gap-2">
              <button
                className="nx-btn nx-btn-outline nx-btn-sm"
                onClick={() =>
                  void (async () => {
                    try {
                      await copyToClipboard(visibleRecoveryKey.formatted);
                      pushToast("success", "恢复密钥已复制");
                    } catch {
                      pushToast("error", "复制失败，请手动复制");
                    }
                  })()
                }
              >
                <IconCopy size={12} />
                复制
              </button>
              <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => {
                if (DESKTOP && accountKey) {
                  desktopPendingRecoveryKeys.delete(accountKey);
                  setDesktopRecoveryState(null);
                } else {
                  clearPendingRecoveryKey();
                }
              }}>
                我已保存
              </button>
            </div>
          </div>
        </section>
      )}

      {!DESKTOP && (
        <section className="nx-card">
          <div className="mb-1 flex flex-wrap items-center gap-2">
            <IconShield size={15} className="text-neutral-400" />
            <span className="nx-card-title">登录会话</span>
            <div className="nx-spacer" />
            <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void loadSessions()}>
              <IconRefresh size={12} />
              刷新
            </button>
          </div>
          <p className="nx-hint mb-3">这里列出当前有效的服务端登录会话，包含浏览器和桌面客户端。退出其他会话后，对方需重新登录。</p>
          {sessionsError && (
            <div className="nx-alert nx-alert-danger flex items-start gap-2">
              <IconXCircle size={13} className="mt-0.5 shrink-0" />
              <span className="min-w-0 flex-1 break-words">会话列表读取失败 · {sessionsError}</span>
              <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={() => void loadSessions()}>
                <IconRefresh size={12} />重试
              </button>
            </div>
          )}
          {sessions && sessions.length === 0 && <div className="nx-hint py-1 text-[12px]">当前没有有效登录会话。</div>}
          {sessions && sessions.length > 0 && (
            <div className="max-h-[240px] overflow-y-auto rounded border border-neutral-800/60">
              {sessionPager.pageItems.map((session) => (
                <div key={session.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-neutral-800/40 px-2.5 py-1.5 last:border-b-0">
                  <span className="min-w-0 flex-1 truncate text-[12.5px] text-neutral-200" title={session.name}>{session.name}</span>
                  <span className="nx-hint text-[11px]">{session.origin}</span>
                  {session.current && <span className="nx-badge nx-badge-green">当前会话</span>}
                  <span className="nx-hint text-[11px]">最近活动 {formatTime(session.last_seen_at)}</span>
                  <span className="nx-hint text-[11px]">到期 {formatTime(session.expires_at)}</span>
                  <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void revokeSession(session)}>
                    <IconTrash size={11} />退出会话
                  </button>
                </div>
              ))}
              {sessionPager.paged && <ListPager currentPage={sessionPager.currentPage} pageCount={sessionPager.pageCount} setPage={sessionPager.setPage} />}
            </div>
          )}
          <p className="nx-hint mt-2 border-t border-neutral-800/60 pt-2 text-[11px]">
            <IconCheckCircle size={11} className="mr-1 inline align-[-1px]" />
            会话有效期 12 小时（滑动续期），最长 7 天；修改密码或管理员重置会使会话立即失效。
          </p>
        </section>
      )}

      <section className="nx-card">
        <div className="mb-1 flex flex-wrap items-center gap-2">
          <IconShield size={15} className="text-neutral-400" />
          <span className="nx-card-title">已登记设备</span>
          <div className="nx-spacer" />
          <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void loadDevices()}>
            <IconRefresh size={12} />
            刷新
          </button>
        </div>
        <p className="nx-hint mb-3">
          这里列出已登记的受管设备。吊销设备会使关联的登录会话失效。
        </p>

        {devicesError && (
          <div className="nx-alert nx-alert-danger flex items-start gap-2">
            <IconXCircle size={13} className="mt-0.5 shrink-0" />
            <span className="min-w-0 flex-1 break-words">设备列表读取失败 · {devicesError}</span>
            <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={() => void loadDevices()}>
              <IconRefresh size={12} />
              重试
            </button>
          </div>
        )}

        {devices && visibleDevices.length === 0 && (
          <div className="nx-hint py-1 text-[12px]">{devices.length === 0 ? "还没有登记设备。" : "没有生效中的设备。"}</div>
        )}

        {devices && revokedCount > 0 && (
          <label className="nx-hint mb-2 flex items-center gap-1.5 text-[11px]">
            <input type="checkbox" checked={showRevoked} onChange={(e) => setShowRevoked(e.target.checked)} />
            显示已吊销（{revokedCount}）
          </label>
        )}

        {devices && visibleDevices.length > 0 && (
          <div className="max-h-[240px] overflow-y-auto rounded border border-neutral-800/60">
            {devicePager.pageItems.map((d) => {
              const revoked = d.revoked_at !== 0;
              return (
                <div
                  key={d.id}
                  className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-neutral-800/40 px-2.5 py-1.5 last:border-b-0"
                >
                  <span className="min-w-0 flex-1 truncate text-[12.5px] text-neutral-200" title={d.name}>
                    {d.name}
                  </span>
                  <span className="nx-hint shrink-0 text-[11px]">{d.kind}</span>
                  <span className={`nx-badge shrink-0 ${revoked ? "nx-badge-red" : "nx-badge-green"}`}>
                    {revoked ? "已吊销" : "生效中"}
                  </span>
                  <span className="nx-hint shrink-0 text-[11px]">最近活动 {formatTime(d.last_seen_at)}</span>
                  <button
                    className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
                    disabled={revoked}
                    title={revoked ? "已吊销" : "吊销后该设备立即退出登录"}
                    onClick={() => void revokeDevice(d)}
                  >
                    <IconTrash size={11} />
                    吊销
                  </button>
                </div>
              );
            })}
            {devicePager.paged && <ListPager currentPage={devicePager.currentPage} pageCount={devicePager.pageCount} setPage={devicePager.setPage} />}
          </div>
        )}

      </section>
    </>
  );
}

// TotpCard manages TOTP two-factor auth for the current account: enable (secret/otpauth URI → confirm code → one-time recovery codes),
// disable (requires the current dynamic code or a recovery code). The server stores only the encrypted secret and recovery-code hashes; recovery-code plaintext is shown once.
function TotpCard() {
  const pushToast = useUi((s) => s.pushToast);
  const [status, setStatus] = useState<TotpStatus | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [setup, setSetup] = useState<TotpSetup | null>(null);
  const [confirmCode, setConfirmCode] = useState("");
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null);
  const [copied, setCopied] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [disableOpen, setDisableOpen] = useState(false);
  const [disableCode, setDisableCode] = useState("");
  const [rebindOpen, setRebindOpen] = useState(false);
  const [reverify, setReverify] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoadError(null);
    return authApi
      .totpStatus()
      .then(setStatus)
      .catch((e: unknown) => setLoadError(describeError(e)));
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const copyText = async (text: string, label: string): Promise<boolean> => {
    try {
      await copyToClipboard(text);
      pushToast("success", `${label}已复制`);
      return true;
    } catch {
      pushToast("error", "复制失败，请手动复制");
      return false;
    }
  };

  const startSetup = async (reverifyCredential?: string) => {
    setBusy(true);
    setError(null);
    try {
      setSetup(await authApi.totpSetup(reverifyCredential));
      setConfirmCode("");
      setRebindOpen(false);
      setReverify("");
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(false);
    }
  };

  const submitConfirm = async (e: FormEvent) => {
    e.preventDefault();
    if (confirmCode.trim().length !== 6) {
      setError("请输入认证器中的 6 位动态码");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const r = await authApi.totpConfirm(confirmCode.trim());
      setRecoveryCodes(r.recovery_codes);
      setCopied(false);
      setSetup(null);
      setConfirmCode("");
      await load();
      pushToast("success", "两步验证已开启");
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

  const submitDisable = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await authApi.totpDisable(disableCode.trim());
      setDisableOpen(false);
      setDisableCode("");
      await load();
      pushToast("success", "两步验证已关闭");
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

  const submitRebind = async (e: FormEvent) => {
    e.preventDefault();
    if (!reverify.trim()) {
      setError("请输入当前动态码、恢复码或登录密码");
      return;
    }
    await startSetup(reverify.trim());
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconShield size={15} className="text-neutral-400" />
        <span className="nx-card-title">两步验证（TOTP）</span>
        {status?.enabled && <span className="nx-badge nx-badge-green">已开启</span>}
        <div className="nx-spacer" />
        {status && !loadError && (
          <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void load()}>
            <IconRefresh size={12} />
            刷新
          </button>
        )}
      </div>

      <p className="nx-hint mb-3">两步验证绑定在服务端账号上，换设备登录仍使用同一认证器，无需重新绑定。</p>

      {loadError && (
        <div className="nx-alert nx-alert-danger flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">两步验证状态读取失败 · {loadError}</span>
          <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={() => void load()}>
            <IconRefresh size={12} />
            重试
          </button>
        </div>
      )}

      {status?.mfa_required && !status.enabled && (
        <div className="nx-alert nx-alert-info mb-3 flex items-start gap-2">
          <IconInfo size={14} className="mt-0.5 shrink-0" />
          <div>
            管理员已要求所有账号启用两步验证，完成绑定前只能使用绑定功能。
          </div>
        </div>
      )}

      {recoveryCodes && (
        <div className="nx-alert nx-alert-danger flex flex-col gap-2">
          <div className="flex items-start gap-2">
            <IconInfo size={14} className="mt-0.5 shrink-0" />
            <div>
              恢复码仅显示一次。手机丢失时，可用任一恢复码代替动态码登录，每个只能使用一次。服务端只保存散列，关闭后无法再次查看。
            </div>
          </div>
          <div className="grid grid-cols-2 gap-x-4 gap-y-0.5 font-mono text-[12.5px]">
            {recoveryCodes.map((code) => (
              <code key={code}>{code}</code>
            ))}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <button
              className="nx-btn nx-btn-outline nx-btn-sm"
              onClick={() =>
                void (async () => {
                  setCopied(false);
                  setCopied(await copyText(recoveryCodes.join("\n"), "恢复码"));
                  setConfirmed(true);
                })()
              }
            >
              <IconCopy size={12} />
              {copied ? "已复制" : "复制全部"}
            </button>
            <button
              className="nx-btn nx-btn-ghost nx-btn-sm"
              disabled={!confirmed}
              onClick={() => setRecoveryCodes(null)}
            >
              <IconCheckCircle size={12} />
              {copied ? "我已安全保存" : "已手动保存"}
            </button>
          </div>
          {!confirmed && <p className="nx-hint text-[11px]">请先复制或手动保存恢复码，再关闭此面板</p>}
        </div>
      )}

      {!recoveryCodes && setup && (
        <div>
          <p className="nx-hint mb-3">
            用认证器 App（如 Google Authenticator、1Password）导入绑定链接，或手动输入密钥。
          </p>
          <div className="nx-alert nx-alert-info mb-3 flex flex-col gap-2">
            <div className="flex flex-wrap items-center gap-2">
              <code className="nx-code break-all font-mono text-[12.5px]">{setup.otpauth_uri}</code>
              <button className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => void copyText(setup.otpauth_uri, "绑定链接")}>
                <IconCopy size={12} />
                复制
              </button>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <code className="nx-code font-mono text-[12.5px]">{setup.secret.replace(/(.{4})/g, "$1 ").trim()}</code>
              <button className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => void copyText(setup.secret, "密钥")}>
                <IconCopy size={12} />
                复制
              </button>
            </div>
          </div>
          <form onSubmit={(e) => void submitConfirm(e)} className="flex flex-col gap-2">
            <input
              className="nx-input max-w-[280px] font-mono"
              placeholder="输入 6 位动态码完成绑定"
              value={confirmCode}
              autoComplete="one-time-code"
              onChange={(e) => setConfirmCode(e.target.value)}
            />
            {error && (
              <div className="nx-alert nx-alert-danger flex items-start gap-2">
                <IconXCircle size={13} className="mt-0.5 shrink-0" />
                <span>{error}</span>
              </div>
            )}
            <div className="flex flex-wrap items-center gap-2">
              <button className="nx-btn nx-btn-primary nx-btn-sm" disabled={busy || confirmCode.trim().length === 0}>
                {busy ? "确认中…" : "确认绑定"}
              </button>
              <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setSetup(null)}>
                取消
              </button>
            </div>
          </form>
        </div>
      )}

      {!recoveryCodes && !setup && status && (
        <div>
          {status.enabled ? (
            <div className="flex flex-col gap-2">
              <p className="nx-hint">
                登录时除密码外，还需输入认证器中的 6 位动态码。剩余恢复码 <b>{status.recovery_codes_left}</b> 个。
              </p>
              {disableOpen ? (
                <form onSubmit={(e) => void submitDisable(e)} className="flex flex-col gap-2">
                  <input
                    className="nx-input max-w-[280px] font-mono"
                    placeholder="当前动态码或恢复码"
                    value={disableCode}
                    autoComplete="one-time-code"
                    onChange={(e) => setDisableCode(e.target.value)}
                  />
                  {error && (
                    <div className="nx-alert nx-alert-danger flex items-center gap-2">
                      <IconXCircle size={13} className="shrink-0" />
                      <span>{error}</span>
                    </div>
                  )}
                  <div className="flex flex-wrap items-center gap-2">
                    <button className="nx-btn nx-btn-primary nx-btn-sm" disabled={busy || !disableCode.trim()}>
                      {busy ? "关闭中…" : "确认关闭"}
                    </button>
                    <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setDisableOpen(false)}>
                      取消
                    </button>
                  </div>
                </form>
              ) : rebindOpen ? (
                <form onSubmit={(e) => void submitRebind(e)} className="flex flex-col gap-2">
                  <input
                    className="nx-input max-w-[280px]"
                    placeholder="当前动态码、恢复码或登录密码"
                    value={reverify}
                    autoComplete="off"
                    onChange={(e) => setReverify(e.target.value)}
                  />
                  {error && (
                    <div className="nx-alert nx-alert-danger flex items-center gap-2">
                      <IconXCircle size={13} className="shrink-0" />
                      <span>{error}</span>
                    </div>
                  )}
                  <div className="flex flex-wrap items-center gap-2">
                    <button className="nx-btn nx-btn-primary nx-btn-sm" disabled={busy || !reverify.trim()}>
                      {busy ? "验证中…" : "验证并换绑"}
                    </button>
                    <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setRebindOpen(false)}>
                      取消
                    </button>
                  </div>
                </form>
              ) : (
                <div>
                  <button
                    className="nx-btn nx-btn-outline nx-btn-sm"
                    title="换绑需要当前动态码、恢复码或登录密码验证"
                    onClick={() => { setError(null); setRebindOpen(true); }}
                  >
                    换绑认证器
                  </button>
                  <button
                    className="nx-btn nx-btn-outline nx-btn-sm ml-2"
                    onClick={() => { setError(null); setDisableOpen(true); }}
                  >
                    关闭两步验证
                  </button>
                  {error && (
                    <div className="nx-alert nx-alert-danger mt-2 flex items-center gap-2">
                      <IconXCircle size={13} className="shrink-0" />
                      <span>{error}</span>
                    </div>
                  )}
                </div>
              )}
            </div>
          ) : (
            <div>
              <p className="nx-hint mb-2">
                开启后，登录时除密码外还需输入认证器 App 中的 6 位动态码。即使密码泄露，账号也多一层保护。服务端加密保存密钥，只保存恢复码散列。
              </p>
              <button className="nx-btn nx-btn-primary nx-btn-sm" disabled={busy} onClick={() => void startSetup()}>
                {busy ? "生成中…" : status.pending ? "继续绑定" : "开启两步验证"}
              </button>
              {error && (
                <div className="nx-alert nx-alert-danger mt-2 flex items-start gap-2">
                  <IconXCircle size={13} className="mt-0.5 shrink-0" />
                  <span>{error}</span>
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </section>
  );
}
