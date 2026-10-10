// 个人账号卡片:当前用户、修改密码(重包裹 DEK)、两步验证(TOTP)、恢复密钥、设备管理、退出登录。
// 未登录时是设置页唯一的登录入口(WEB);桌面端本地优先,账号体系在同步服务端
// (见 SyncCard 桌面端登录卡),未登录不渲染。

import { useCallback, useEffect, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { authApi, authAvailable, type AccountDevice, type TotpSetup, type TotpStatus } from "../../ipc/authApi";
import { useAuth } from "../auth/store";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { formatTime } from "../../ui/format";
import {
  IconCheckCircle,
  IconCopy,
  IconInfo,
  IconKey,
  IconLock,
  IconPlus,
  IconRefresh,
  IconShield,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";

export function AuthCard() {
  const { pushToast } = useUi();
  const qc = useQueryClient();
  const user = useAuth((s) => s.user);
  const status = useAuth((s) => s.status);
  const changePassword = useAuth((s) => s.changePassword);
  const logout = useAuth((s) => s.logout);
  const pendingRecoveryKey = useAuth((s) => s.pendingRecoveryKey);
  const clearPendingRecoveryKey = useAuth((s) => s.clearPendingRecoveryKey);

  // 平台托管模式(懒猫): 账号随平台登录自动建立, 用户手里没有口令, 也不该有"退出"。
  // 退出后下一次刷新会被平台会话重新接管, 留着只会让人以为"登出"生效了。
  const platformManaged = status?.auth === "platform";

  const [devices, setDevices] = useState<AccountDevice[] | null>(null);
  const [devicesError, setDevicesError] = useState<string | null>(null);
  const [enrollCode, setEnrollCode] = useState<{ code: string; expiresAt: number } | null>(null);

  const [oldPassword, setOldPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [pwdBusy, setPwdBusy] = useState(false);
  const [pwdError, setPwdError] = useState<string | null>(null);

  const loadDevices = useCallback(() => {
    if (!user) return Promise.resolve();
    setDevicesError(null);
    return authApi
      .devices()
      .then((r) => setDevices(r.devices))
      .catch((e: unknown) => setDevicesError(describeError(e)));
  }, [user]);

  useEffect(() => {
    void loadDevices();
  }, [loadDevices]);

  const submitPassword = async (e: FormEvent) => {
    e.preventDefault();
    setPwdError(null);
    if (newPassword.length < 8) {
      setPwdError("新密码至少 8 位");
      return;
    }
    if (newPassword !== confirmPassword) {
      setPwdError("两次输入的密码不一致");
      return;
    }
    setPwdBusy(true);
    try {
      await changePassword(oldPassword, newPassword);
      setOldPassword("");
      setNewPassword("");
      setConfirmPassword("");
      pushToast("success", "密码已修改,其他设备上的会话已退出");
    } catch (err) {
      setPwdError(describeError(err));
    } finally {
      setPwdBusy(false);
    }
  };

  const revokeDevice = async (device: AccountDevice) => {
    const ok = await ask(`吊销设备「${device.name}」?\n\n吊销后该设备立即退出登录,不可恢复。`, {
      title: "吊销设备",
      kind: "warning",
    });
    if (!ok) return;
    try {
      await authApi.deviceRevoke(device.id);
      pushToast("success", `已吊销「${device.name}」`);
      void loadDevices();
    } catch (e) {
      pushToast("error", describeError(e));
    }
  };

  const issueEnrollCode = async () => {
    try {
      const r = await authApi.enrollCode();
      setEnrollCode({ code: r.code, expiresAt: r.expires_at });
    } catch (e) {
      pushToast("error", describeError(e));
    }
  };

  if (!user) {
    // 桌面端没有账号门,登录按钮只会打开一个不存在的门:整卡不渲染,登录入口在 SyncCard。
    if (!authAvailable()) return null;
    if (status?.auth === "platform") {
      // 平台托管模式下没有可填的东西: 账号随平台登录自动建立。走到这里说明平台会话没建立
      // (典型是从服务端口直连、绕开了平台入口), 给出原因而不是死按钮。
      return (
        <section className="nx-card">
          <div className="mb-1 flex items-center gap-2">
            <IconKey size={15} className="text-neutral-400" />
            <span className="nx-card-title">账号</span>
          </div>
          <p className="nx-hint mb-3">
            本实例部署在懒猫平台上，账号随平台登录自动建立，这里没有需要填写的初始化码或密码。
            看到这段文字通常意味着没有经过平台入口访问 —— 请改用平台的访问地址重新打开。
          </p>
        </section>
      );
    }
    if (status?.auth === "off") {
      // auth=off 下 /auth/* 全部 403(store.refresh 合成 auth=off 状态),任何登录/初始化入口都是死路。
      return (
        <section className="nx-card">
          <div className="mb-1 flex items-center gap-2">
            <IconKey size={15} className="text-neutral-400" />
            <span className="nx-card-title">账号</span>
          </div>
          <p className="nx-hint mb-3">
            这台服务器关闭了账号功能(--auth=off),不提供登录、注册与账号同步;其他本地功能不受影响。
          </p>
        </section>
      );
    }
    if (status && !status.initialized) {
      // 未初始化(loopback 免登录实例匿名可直达这里): 显式初始化入口,供想要账号的用户使用。
      return (
        <section className="nx-card">
          <div className="mb-1 flex items-center gap-2">
            <IconKey size={15} className="text-neutral-400" />
            <span className="nx-card-title">账号</span>
          </div>
          <p className="nx-hint mb-3">
            这台服务器还没有任何账号{status.auth === "loopback" ? ",当前允许匿名使用" : ""}。
            初始化后创建超级管理员,资产、分组、片段会端到端加密同步到你的账号,在其他设备上可用;
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
        <div className="mb-1 flex items-center gap-2">
          <IconKey size={15} className="text-neutral-400" />
          <span className="nx-card-title">账号</span>
        </div>
        <p className="nx-hint mb-3">
          当前未登录{status?.auth === "loopback" ? "(这台服务器允许匿名使用)" : ""}。
          登录只建立连接并完成配置;下方「账号同步」台会读取云端副本进行对比,但不会自动推送或应用,
          推送到云端 / 拉取并应用由你手动触发。不登录也能继续本地使用。
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
        <div className="mb-1 flex items-center gap-2">
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
          <span className="nx-hint">最近登录 {formatTime(user.last_login_at)}</span>
        </div>

        {platformManaged && (
          <p className="nx-hint mt-2">
            本实例部署在懒猫平台上，登录由平台负责：打开地址即为当前账号，不需要在这里登录或退出。
            账号随平台登录自动建立，用于承载设备、分享等需要归属的功能。
          </p>
        )}

        {!platformManaged && (
          <div className="mt-3 flex flex-wrap items-center gap-2">
            <button
              className="nx-btn nx-btn-outline nx-btn-sm"
              onClick={() =>
                void (async () => {
                  const ok = await ask("退出当前账号?\n\n本机浏览器会话立即失效。", { title: "退出登录" });
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
                  const ok = await ask("退出所有设备上的会话?\n\n包括这台浏览器,退出后需重新登录。", {
                    title: "退出全部会话",
                    kind: "warning",
                  });
                  if (!ok) return;
                  try {
                    await authApi.logoutAll();
                    await logout();
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
          <div className="mb-1 flex items-center gap-2">
            <IconLock size={15} className="text-neutral-400" />
            <span className="nx-card-title">修改密码</span>
          </div>
          <p className="nx-hint mb-3">
            修改密码会用新密码重新加密数据密钥,云端已保存的加密内容不受影响。修改成功后会签发
            新的恢复密钥(旧的立即作废),并退出其他设备上的会话。
          </p>
          <form onSubmit={(e) => void submitPassword(e)} className="flex flex-col gap-2">
            <input
              className="nx-input max-w-[280px]"
              type="password"
              placeholder="当前密码"
              value={oldPassword}
              autoComplete="current-password"
              onChange={(e) => setOldPassword(e.target.value)}
            />
            <input
              className="nx-input max-w-[280px]"
              type="password"
              placeholder="新密码(至少 8 位)"
              value={newPassword}
              autoComplete="new-password"
              onChange={(e) => setNewPassword(e.target.value)}
            />
            <input
              className="nx-input max-w-[280px]"
              type="password"
              placeholder="确认新密码"
              value={confirmPassword}
              autoComplete="new-password"
              onChange={(e) => setConfirmPassword(e.target.value)}
            />
            {pwdError && (
              <div className="nx-alert nx-alert-danger flex items-start gap-2">
                <IconXCircle size={13} className="mt-0.5 shrink-0" />
                <span>{pwdError}</span>
              </div>
            )}
            <div>
              <button className="nx-btn nx-btn-primary nx-btn-sm" disabled={pwdBusy || !oldPassword || !newPassword}>
                {pwdBusy ? "修改中…" : "修改密码"}
              </button>
            </div>
          </form>
        </section>
      )}

      <TotpCard />

      {pendingRecoveryKey && (
        <section className="nx-card">
          <div className="mb-1 flex items-center gap-2">
            <IconShield size={15} className="text-amber-300" />
            <span className="nx-card-title">新的恢复密钥(只显示这一次)</span>
          </div>
          <div className="nx-alert nx-alert-danger flex flex-col gap-2">
            <code className="nx-code break-all font-mono text-[12.5px]">{pendingRecoveryKey.formatted}</code>
            <div className="flex flex-wrap items-center gap-2">
              <button
                className="nx-btn nx-btn-outline nx-btn-sm"
                onClick={() =>
                  void (async () => {
                    try {
                      if (!navigator.clipboard) throw new Error("Clipboard API unavailable");
                      await navigator.clipboard.writeText(pendingRecoveryKey.formatted);
                      pushToast("success", "恢复密钥已复制");
                    } catch {
                      pushToast("error", "复制失败,请手动选中复制");
                    }
                  })()
                }
              >
                <IconCopy size={12} />
                复制
              </button>
              <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={clearPendingRecoveryKey}>
                我已保存
              </button>
            </div>
          </div>
        </section>
      )}

      <section className="nx-card">
        <div className="mb-1 flex flex-wrap items-center gap-2">
          <IconShield size={15} className="text-neutral-400" />
          <span className="nx-card-title">登录设备</span>
          <div className="nx-spacer" />
          <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void loadDevices()}>
            <IconRefresh size={12} />
            刷新
          </button>
          <button className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => void issueEnrollCode()}>
            <IconPlus size={12} />
            添加设备
          </button>
        </div>
        <p className="nx-hint mb-3">
          已登录的设备可以随时吊销,吊销后该设备立即退出登录。
        </p>

        {enrollCode && (
          <div className="nx-alert nx-alert-info mb-3 flex flex-col gap-2">
            <div className="flex items-start gap-2">
              <IconInfo size={14} className="mt-0.5 shrink-0" />
              <div>
                配对码 <b>15 分钟内有效</b>,只显示这一次。在新设备的登录页点「用配对码添加设备」输入它,
                即可把账号添加到那台设备:
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <code className="nx-code font-mono text-[13px]">{enrollCode.code}</code>
              <button
                className="nx-btn nx-btn-outline nx-btn-sm"
                onClick={() => {
                  void navigator.clipboard
                    ?.writeText(enrollCode.code)
                    .then(() => pushToast("success", "配对码已复制"))
                    .catch(() => pushToast("error", "复制失败,请手动选中复制"));
                }}
              >
                <IconCopy size={12} />
                复制
              </button>
              <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setEnrollCode(null)}>
                完成
              </button>
            </div>
          </div>
        )}

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

        {devices && devices.length === 0 && (
          <div className="nx-hint py-1 text-[12px]">还没有登记设备。</div>
        )}

        {devices && devices.length > 0 && (
          <div className="max-h-[240px] overflow-y-auto rounded border border-neutral-800/60">
            {devices.map((d) => {
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
          </div>
        )}

        <p className="nx-hint mt-2 border-t border-neutral-800/60 pt-2 text-[11px]">
          <IconCheckCircle size={11} className="mr-1 inline align-[-1px]" />
          会话有效期 12 小时(滑动续期),最长 7 天;改密、吊销设备或管理员重置会使会话立即失效。
        </p>
      </section>
    </>
  );
}

// TotpCard 管理当前账号的 TOTP 两步验证: 开启(密钥/otpauth URI → 确认码 → 一次性恢复码)、
// 关闭(需当前动态码或恢复码)。服务端只存加密密钥与恢复码散列, 恢复码明文只展示一次。
function TotpCard() {
  const { pushToast } = useUi();
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
      if (!navigator.clipboard) throw new Error("Clipboard API unavailable");
      await navigator.clipboard.writeText(text);
      pushToast("success", `${label}已复制`);
      return true;
    } catch {
      pushToast("error", "复制失败,请手动选中复制");
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
        <span className="nx-card-title">两步验证 (TOTP)</span>
        {status?.enabled && <span className="nx-badge nx-badge-green">已开启</span>}
        <div className="nx-spacer" />
        {status && !loadError && (
          <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void load()}>
            <IconRefresh size={12} />
            刷新
          </button>
        )}
      </div>

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
            管理员已要求所有账号启用两步验证。完成绑定前,这个账号只能使用绑定相关功能;
            绑定完成后一切恢复正常。
          </div>
        </div>
      )}

      {recoveryCodes && (
        <div className="nx-alert nx-alert-danger flex flex-col gap-2">
          <div className="flex items-start gap-2">
            <IconInfo size={14} className="mt-0.5 shrink-0" />
            <div>
              恢复码(只显示这一次)。手机丢失时,可用任意一枚恢复码代替动态码登录;
              每枚只能用一次。服务端只保存它们的散列,关闭后<b>无法再次查看</b>。
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
          {!confirmed && <p className="nx-hint text-[11px]">先复制保存,再关闭本页</p>}
        </div>
      )}

      {!recoveryCodes && setup && (
        <div>
          <p className="nx-hint mb-3">
            用认证器 App(如 Google Authenticator、1Password 等)扫描或录入以下密钥绑定。
            otpauth 链接可整体复制到支持 URI 导入的认证器;也可以把链接自行编码成二维码扫描。
          </p>
          <div className="nx-alert nx-alert-info mb-3 flex flex-col gap-2">
            <div className="flex flex-wrap items-center gap-2">
              <code className="nx-code break-all font-mono text-[12.5px]">{setup.otpauth_uri}</code>
              <button className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => copyText(setup.otpauth_uri, "绑定链接")}>
                <IconCopy size={12} />
                复制
              </button>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <code className="nx-code font-mono text-[12.5px]">{setup.secret.replace(/(.{4})/g, "$1 ").trim()}</code>
              <button className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => copyText(setup.secret, "密钥")}>
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
                登录时在密码后需要输入认证器中的 6 位动态码。剩余恢复码 <b>{status.recovery_codes_left}</b> 枚。
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
                开启后,登录时除密码外还需输入认证器 App 中的 6 位动态码;即使密码泄漏,账号也多一道防线。
                服务端以加密形式保存密钥,恢复码只保存散列。
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
