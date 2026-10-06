// 个人账号卡片:当前用户、修改密码(重包裹 DEK)、恢复密钥、设备管理、退出登录。
// 仅 WEB/DEMO 渲染;桌面端本地优先,账号体系在同步服务端(见 SyncCard)。

import { useCallback, useEffect, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { authApi, type AccountDevice } from "../../ipc/authApi";
import { useAuth } from "../auth/store";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
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

function formatTime(ms: number): string {
  if (!ms) return "从未";
  return new Date(ms).toLocaleString();
}

export function AuthCard() {
  const { pushToast } = useUi();
  const qc = useQueryClient();
  const user = useAuth((s) => s.user);
  const status = useAuth((s) => s.status);
  const changePassword = useAuth((s) => s.changePassword);
  const logout = useAuth((s) => s.logout);
  const pendingRecoveryKey = useAuth((s) => s.pendingRecoveryKey);
  const clearPendingRecoveryKey = useAuth((s) => s.clearPendingRecoveryKey);

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
    return (
      <section className="nx-card">
        <div className="mb-1 flex items-center gap-2">
          <IconKey size={15} className="text-neutral-400" />
          <span className="nx-card-title">账号</span>
        </div>
        <p className="nx-hint mb-3">
          当前未登录{status?.auth === "loopback" ? "(这台服务器允许匿名使用)" : ""}。
          登录后可以跨设备同步资产;不登录也能继续本地使用。
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
      </section>

      <section className="nx-card">
        <div className="mb-1 flex items-center gap-2">
          <IconLock size={15} className="text-neutral-400" />
          <span className="nx-card-title">修改密码</span>
        </div>
        <p className="nx-hint mb-3">
          改密会用新密码重新包裹数据密钥,云端密文内容不受影响;同时会签发新的恢复密钥,
          其他设备上的会话会被退出。
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
                onClick={() => {
                  void navigator.clipboard
                    ?.writeText(pendingRecoveryKey.formatted)
                    .then(() => pushToast("success", "恢复密钥已复制"))
                    .catch(() => pushToast("error", "复制失败,请手动选中复制"));
                }}
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
          已登录的设备可以随时吊销。要把账号加到新设备,在已登录设备上生成一次性配对码,再到新设备上输入。
        </p>

        {enrollCode && (
          <div className="nx-alert nx-alert-info mb-3 flex flex-col gap-2">
            <div className="flex items-start gap-2">
              <IconInfo size={14} className="mt-0.5 shrink-0" />
              <div>
                配对码 <b>10 分钟内有效</b>,只显示这一次。在新设备的登录页选择「用配对码添加设备」并输入:
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
