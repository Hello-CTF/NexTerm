// Superadmin card: user management (create / disable / reset) and the open-registration switch. Visible to superadmins only.
// The desktop identity comes from the "account sync" link session (../auth/desktopAccount.ts); user management goes through the
// remoteAccountApi proxy; the registration switch and MFA policy are not covered by the proxy, so they are honestly hidden with a browser-management note.

import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { adminApi, authApi, type AccountUser, type AdminSettings } from "../../ipc/authApi";
import { remoteAccountApi } from "../../ipc/remoteApi";
import { DESKTOP } from "../../ipc/env";
import { useAuth } from "../auth/store";
import { useDesktopAccount, useDesktopAccountScope } from "../auth/desktopAccount";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { formatTime } from "../../ui/format";
import { ServerSecurityLink } from "./ServerSecurityLink";
import {
  IconCheckCircle,
  IconInfo,
  IconPlus,
  IconRefresh,
  IconShield,
  IconXCircle,
} from "../../ui/icons";

function stateBadge(user: AccountUser): { text: string; tone: string } {
  if (user.state === "disabled") return { text: "已禁用", tone: "nx-badge-red" };
  if (user.state === "reset_required") return { text: "待重置密码", tone: "nx-badge-amber" };
  return { text: "正常", tone: "nx-badge-green" };
}

export function AccountCard() {
  const { pushToast } = useUi();
  const sessionUser = useAuth((s) => s.user);
  // The desktop identity comes from the "account sync" link server session (useAuth holds no user on desktop).
  const desktopAccount = useDesktopAccount();
  const user = DESKTOP ? desktopAccount : sessionUser;
  const desktopScope = useDesktopAccountScope(desktopAccount);
  const identity = user?.role === "superadmin" && (!DESKTOP || desktopScope)
    ? (DESKTOP ? desktopScope : JSON.stringify(["web", user.id, user.username]))
    : null;
  const currentIdentity = useRef(identity);
  currentIdentity.current = identity;
  const identityEpoch = useRef(0);
  const loadGeneration = useRef(0);
  const [contentIdentity, setContentIdentity] = useState<string | null>(null);

  const [users, setUsers] = useState<AccountUser[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [settings, setSettings] = useState<AdminSettings | null>(null);
  const [registrationDraft, setRegistrationDraft] = useState(false);
  const [mfaDraft, setMfaDraft] = useState(false);
  const [settingsBusy, setSettingsBusy] = useState(false);

  const [createOpen, setCreateOpen] = useState(false);
  const [username, setUsername] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [createBusy, setCreateBusy] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);

  const load = useCallback(() => {
    if (!identity || currentIdentity.current !== identity) return Promise.resolve();
    const generation = ++loadGeneration.current;
    const stillCurrent = () => currentIdentity.current === identity && loadGeneration.current === generation;
    setListError(null);
    // Desktop: the registration switch and MFA policy have no proxy channel; only the user list is fetched.
    if (DESKTOP) {
      return remoteAccountApi
        .adminUsers()
        .then((r) => { if (stillCurrent()) setUsers(r.users); })
        .catch((e: unknown) => { if (stillCurrent()) setListError(describeError(e)); });
    }
    return Promise.all([
      adminApi.users(),
      adminApi.settingsGet(),
    ]).then(([r, s]) => {
      if (stillCurrent()) {
        setUsers(r.users);
        setSettings(s);
        setRegistrationDraft(s.registration_open);
        setMfaDraft(s.mfa_required);
      }
    }).catch((e: unknown) => { if (stillCurrent()) setListError(describeError(e)); });
  }, [identity]);

  useEffect(() => {
    identityEpoch.current++;
    loadGeneration.current++;
    setUsers(null);
    setListError(null);
    setSettings(null);
    setRegistrationDraft(false);
    setMfaDraft(false);
    setSettingsBusy(false);
    setCreateOpen(false);
    setUsername("");
    setDisplayName("");
    setPassword("");
    setCreateBusy(false);
    setCreateError(null);
    setContentIdentity(identity);
    if (identity) void load();
  }, [identity, load]);

  if (!user || user.role !== "superadmin" || !identity || contentIdentity !== identity) return null;

  const submitCreate = async (e: FormEvent) => {
    e.preventDefault();
    const actionIdentity = identity;
    const actionEpoch = identityEpoch.current;
    const stillCurrent = () => currentIdentity.current === actionIdentity && identityEpoch.current === actionEpoch;
    setCreateError(null);
    if (password.length < 8) {
      setCreateError("密码至少 8 位");
      return;
    }
    setCreateBusy(true);
    try {
      if (DESKTOP) {
        await remoteAccountApi.adminCreateUser(username.trim(), password, displayName.trim());
      } else {
        await adminApi.createUser(username.trim(), password, displayName.trim());
      }
      if (!stillCurrent()) return;
      pushToast("success", `已创建用户「${username.trim()}」，请将初始密码告知对方`);
      setUsername("");
      setDisplayName("");
      setPassword("");
      setCreateOpen(false);
      await load();
    } catch (err) {
      if (stillCurrent()) setCreateError(describeError(err));
    } finally {
      if (stillCurrent()) setCreateBusy(false);
    }
  };

  const disableUser = async (target: AccountUser) => {
    const actionIdentity = identity;
    const actionEpoch = identityEpoch.current;
    const stillCurrent = () => currentIdentity.current === actionIdentity && identityEpoch.current === actionEpoch;
    const ok = await ask(
      `禁用用户「${target.username}」？\n\n禁用后其所有会话立即失效，且无法再登录（可随后重置）。`,
      { title: "禁用用户", kind: "warning" },
    );
    if (!ok || !stillCurrent()) return;
    try {
      if (DESKTOP) {
        await remoteAccountApi.adminSetUserState(target.id, "disabled");
      } else {
        await adminApi.disableUser(target.id);
      }
      if (!stillCurrent()) return;
      pushToast("success", `已禁用「${target.username}」`);
      await load();
    } catch (e) {
      if (stillCurrent()) pushToast("error", describeError(e));
    }
  };

  const resetUser = async (target: AccountUser) => {
    const actionIdentity = identity;
    const actionEpoch = identityEpoch.current;
    const stillCurrent = () => currentIdentity.current === actionIdentity && identityEpoch.current === actionEpoch;
    const ok = await ask(
      `重置用户「${target.username}」？\n\n对方的数据密钥、所有会话和两步验证绑定将被清除；\n对方下次登录须设置新密码，服务端密文需从仍持有数据的设备重新同步。`,
      { title: "重置用户", kind: "warning" },
    );
    if (!ok || !stillCurrent()) return;
    try {
      if (DESKTOP) {
        await remoteAccountApi.adminSetUserState(target.id, "reset_required");
      } else {
        await adminApi.resetUser(target.id);
      }
      if (!stillCurrent()) return;
      pushToast("success", `已重置「${target.username}」`);
      await load();
    } catch (e) {
      if (stillCurrent()) pushToast("error", describeError(e));
    }
  };

  const saveSettings = async () => {
    if (!settings) return;
    const actionIdentity = identity;
    const actionEpoch = identityEpoch.current;
    const stillCurrent = () => currentIdentity.current === actionIdentity && identityEpoch.current === actionEpoch;
    setSettingsBusy(true);
    try {
      const next = await adminApi.settingsPut({ registrationOpen: registrationDraft, mfaRequired: mfaDraft });
      if (!stillCurrent()) return;
      setSettings(next);
      setRegistrationDraft(next.registration_open);
      setMfaDraft(next.mfa_required);
      pushToast("success", "设置已保存");
      // The registration switch in /auth/status changed; refresh the gate status cache
      void authApi.status().then((s) => {
        if (stillCurrent()) useAuth.setState({ status: s });
      }).catch(() => undefined);
    } catch (e) {
      if (stillCurrent()) {
        pushToast("error", describeError(e));
        setRegistrationDraft(settings.registration_open);
        setMfaDraft(settings.mfa_required);
      }
    } finally {
      if (stillCurrent()) setSettingsBusy(false);
    }
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconShield size={15} className="text-neutral-400" />
        <span className="nx-card-title">用户管理</span>
        <span className="nx-badge nx-badge-blue">超级管理员</span>
        <div className="nx-spacer" />
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void load()}>
          <IconRefresh size={12} />
          刷新
        </button>
        <button className="nx-btn nx-btn-primary nx-btn-sm" onClick={() => setCreateOpen((v) => !v)}>
          <IconPlus size={12} />
          添加用户
        </button>
      </div>

      <p className="nx-hint mb-3">
        管理员创建的用户可使用初始密码登录。禁用会使用户的所有会话立即失效；重置会清除数据密钥、所有会话和两步验证绑定，用户下次登录须设置新密码，并重新同步服务端密文。管理员无法获取或解密用户数据。
      </p>

      {createOpen && (
        <form onSubmit={(e) => void submitCreate(e)} className="mb-3 flex flex-col gap-2 border-b border-neutral-800/60 pb-3">
          <div className="flex flex-wrap items-center gap-2">
            <input
              className="nx-input min-w-0 flex-1"
              placeholder="用户名"
              value={username}
              autoComplete="off"
              onChange={(e) => setUsername(e.target.value)}
            />
            <input
              className="nx-input min-w-0 flex-1"
              placeholder="显示名（可选）"
              value={displayName}
              autoComplete="off"
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <input
              className="nx-input min-w-0 flex-1 font-mono"
              type="password"
              placeholder="初始密码（至少 8 位）"
              value={password}
              autoComplete="new-password"
              onChange={(e) => setPassword(e.target.value)}
            />
            <button className="nx-btn nx-btn-primary" disabled={createBusy || !username.trim() || !password}>
              {createBusy ? "创建中…" : "创建"}
            </button>
            <button type="button" className="nx-btn nx-btn-ghost" onClick={() => setCreateOpen(false)}>
              取消
            </button>
          </div>
          {createError && (
            <div className="nx-alert nx-alert-danger flex items-start gap-2">
              <IconXCircle size={13} className="mt-0.5 shrink-0" />
              <span>{createError}</span>
            </div>
          )}
        </form>
      )}

      {listError && (
        <div className="nx-alert nx-alert-danger flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">用户列表读取失败 · {listError}</span>
          <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={() => void load()}>
            <IconRefresh size={12} />
            重试
          </button>
        </div>
      )}

      {users && users.length > 0 && (
        <div className="max-h-[280px] overflow-y-auto rounded border border-neutral-800/60">
          {users.map((u) => {
            const st = stateBadge(u);
            const isSelf = u.id === user.id;
            return (
              <div
                key={u.id}
                className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-neutral-800/40 px-2.5 py-1.5 last:border-b-0"
              >
                <span className="min-w-0 flex-1 truncate text-[12.5px] text-neutral-200" title={u.username}>
                  {u.username}
                  {isSelf && <span className="nx-hint ml-1.5">（我）</span>}
                </span>
                {u.role === "superadmin" && <span className="nx-badge shrink-0 nx-badge-blue">超级管理员</span>}
                <span className={`nx-badge shrink-0 ${st.tone}`}>{st.text}</span>
                <span className={`nx-badge shrink-0 ${u.mfa_enabled ? "nx-badge-green" : "nx-badge-amber"}`}>
                  {u.mfa_enabled ? "MFA 已开启" : "MFA 未绑定"}
                </span>
                <span className="nx-hint shrink-0 text-[11px]">最近登录 {formatTime(u.last_login_at)}</span>
                {!isSelf && u.state === "active" && (
                  <button
                    className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
                    onClick={() => void disableUser(u)}
                  >
                    禁用
                  </button>
                )}
                {!isSelf && (
                  <button
                    className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
                    title="清除数据密钥、所有会话和两步验证绑定，对方下次登录须设置新密码"
                    onClick={() => void resetUser(u)}
                  >
                    重置
                  </button>
                )}
              </div>
            );
          })}
        </div>
      )}

      {!DESKTOP && (
      <div className="mt-3 border-t border-neutral-800/60 pt-3">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-[12.5px] text-neutral-200">开放注册</span>
          <input
            type="checkbox"
            className="h-4 w-4"
            checked={registrationDraft}
            onChange={(e) => setRegistrationDraft(e.target.checked)}
          />
          <span className="text-[12.5px] text-neutral-200">要求两步验证（MFA）</span>
          <input
            type="checkbox"
            className="h-4 w-4"
            checked={mfaDraft}
            onChange={(e) => setMfaDraft(e.target.checked)}
          />
          <button
            className="nx-btn nx-btn-outline nx-btn-sm"
            disabled={settingsBusy || !settings || (registrationDraft === settings.registration_open && mfaDraft === settings.mfa_required)}
            onClick={() => void saveSettings()}
          >
            {settingsBusy ? "保存中…" : "保存"}
          </button>
        </div>
        <p className="nx-hint mt-1.5">
          {registrationDraft
            ? "任何知道这台服务器地址的人都能注册普通用户账号；建议仅在可信网络中临时开放。"
            : "默认关闭。新用户只能由管理员手动添加。"}
        </p>
        <p className="nx-hint mt-1.5">
          {mfaDraft
            ? "强制启用两步验证：未绑定 TOTP 的账号登录后只能进入绑定页，完成前无法使用其他功能（包括管理功能）；已绑定的账号登录时必须输入动态码。开启前请确认所有用户都能完成绑定。"
            : "默认不强制。用户可在账号设置中开启 TOTP 两步验证。"}
        </p>
      </div>
      )}

      {DESKTOP && (
        <div className="mt-3 border-t border-neutral-800/60 pt-3">
          <p className="nx-hint">开放注册与两步验证（MFA）策略在服务端 Web 界面管理。</p>
          <ServerSecurityLink />
        </div>
      )}

      <div className="nx-alert nx-alert-info mt-3 flex items-start gap-2">
        <IconInfo size={14} className="mt-0.5 shrink-0" />
        <div>
          <IconCheckCircle size={11} className="mr-1 inline align-[-1px]" />
          注册默认关闭；管理员无法通过任何接口获取用户的数据密钥明文。
        </div>
      </div>
    </section>
  );
}
