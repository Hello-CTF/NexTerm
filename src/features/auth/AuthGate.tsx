// 账号门:首次初始化 / 登录 / 注册 / reset_required 强制改密 / 恢复密钥一次性展示。
// 仅在 WEB 且门状态非 ready 时渲染全屏覆盖。

import { useEffect, useState, type FormEvent } from "react";
import { useAuth, type RecoveryKeyIssue } from "./store";
import { describeError } from "../../ui/errorText";
import { authApi, type AccountDevice, type TotpSetup } from "../../ipc/authApi";
import {
  IconCheckCircle,
  IconCopy,
  IconInfo,
  IconKey,
  IconLock,
  IconRefresh,
  IconShield,
  IconXCircle,
} from "../../ui/icons";

type Screen = "setup" | "login" | "register" | "reset_required" | "recovery" | "enroll" | "mfa_enroll";

export function AuthGate() {
  const gate = useAuth((s) => s.gate);
  const status = useAuth((s) => s.status);
  const pendingRecoveryKey = useAuth((s) => s.pendingRecoveryKey);
  const clearPendingRecoveryKey = useAuth((s) => s.clearPendingRecoveryKey);
  const pendingMfa = useAuth((s) => s.pendingMfa);

  const [screen, setScreen] = useState<Screen | null>(null);
  const [recoveryIssue, setRecoveryIssue] = useState<RecoveryKeyIssue | null>(null);

  useEffect(() => {
    void useAuth.getState().refresh();
  }, []);

  useEffect(() => {
    if (gate === "setup") setScreen("setup");
    else if (gate === "login") setScreen("login");
    else if (gate === "reset_required") setScreen("reset_required");
    else if (gate === "mfa_enroll") setScreen("mfa_enroll");
    else setScreen(null);
  }, [gate]);

  // 初始化/注册/改密成功后签发的一次性恢复密钥优先于门状态展示
  const issue = recoveryIssue ?? pendingRecoveryKey;
  if (issue) {
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center bg-neutral-950/85 p-4 backdrop-blur-sm">
        <div className="nx-card w-full max-w-[420px]">
          <RecoveryKeyScreen
            issue={issue}
            onDone={() => {
              setRecoveryIssue(null);
              clearPendingRecoveryKey();
            }}
          />
        </div>
      </div>
    );
  }

  if (gate === "loading") {
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center bg-neutral-950/85 p-4 backdrop-blur-sm">
        <div className="nx-card flex w-full max-w-[420px] items-center justify-center">
          <IconRefresh size={18} className="animate-spin text-neutral-400" />
        </div>
      </div>
    );
  }
  if (gate === "ready" || screen === null) return null;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-neutral-950/85 p-4 backdrop-blur-sm">
      <div className="nx-card w-full max-w-[420px]">
        {screen === "setup" && <SetupForm />}
        {screen === "login" && (pendingMfa ? <TotpChallengeForm /> : <LoginForm onNavigate={setScreen} registrationOpen={status?.registration_open ?? false} />)}
        {screen === "register" && <RegisterForm onBack={() => setScreen("login")} />}
        {screen === "reset_required" && <ResetRequiredForm />}
        {screen === "recovery" && <RecoveryForm onBack={() => setScreen("login")} />}
        {screen === "enroll" && <EnrollForm onBack={() => setScreen("login")} />}
        {screen === "mfa_enroll" && <MfaEnrollForm />}
      </div>
    </div>
  );
}

function GateError({ error }: { error: { code: string; message: string } | null }) {
  if (!error) return null;
  return (
    <div className="nx-alert nx-alert-danger mt-3 flex items-start gap-2" role="alert">
      <IconXCircle size={13} className="mt-0.5 shrink-0" />
      <span className="min-w-0 flex-1 break-words">{describeError(error)}</span>
    </div>
  );
}

function Field(props: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  type?: string;
  placeholder?: string;
  autoComplete?: string;
  mono?: boolean;
}) {
  return (
    <label className="mt-3 block">
      <span className="mb-1 block text-[12px] text-neutral-400">{props.label}</span>
      <input
        className={`nx-input w-full ${props.mono ? "font-mono" : ""}`}
        type={props.type ?? "text"}
        value={props.value}
        placeholder={props.placeholder}
        autoComplete={props.autoComplete}
        onChange={(e) => props.onChange(e.target.value)}
      />
    </label>
  );
}

function SetupForm() {
  const initSuperadmin = useAuth((s) => s.initSuperadmin);
  const status = useAuth((s) => s.status);
  const error = useAuth((s) => s.error);
  const clearError = useAuth((s) => s.clearError);
  const [code, setCode] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setFormError(null);
    clearError();
    if (password.length < 8) {
      setFormError("密码至少 8 位");
      return;
    }
    if (password !== confirm) {
      setFormError("两次输入的密码不一致");
      return;
    }
    setBusy(true);
    try {
      await initSuperadmin(code.trim(), username.trim(), password);
    } catch {
      // 错误已在 store 里
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={(e) => void submit(e)}>
      <div className="mb-1 flex items-center gap-2">
        <IconShield size={15} className="text-neutral-400" />
        <span className="nx-card-title">初始化 NexTerm</span>
      </div>
      <p className="nx-hint">
        这台服务器还没有任何账号。输入服务器控制台打印的一次性初始化码，创建超级管理员。
        初始化码只能使用一次，成功后即失效。
      </p>
      <Field label="初始化码" value={code} onChange={setCode} mono placeholder="控制台输出的初始化码" autoComplete="off" />
      <Field label="用户名" value={username} onChange={setUsername} placeholder="超级管理员用户名" autoComplete="username" />
      <Field label="密码" value={password} onChange={setPassword} type="password" placeholder="至少 8 位" autoComplete="new-password" />
      <Field label="确认密码" value={confirm} onChange={setConfirm} type="password" placeholder="再输入一次" autoComplete="new-password" />
      {formError && <GateError error={{ code: "bad_param", message: formError }} />}
      <GateError error={error} />
      <button className="nx-btn nx-btn-primary mt-4 w-full" disabled={busy || !code.trim() || !username.trim()}>
        {busy ? "创建中…" : "创建超级管理员"}
      </button>
      {status?.auth === "loopback" && (
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm mt-3 w-full"
          onClick={() => useAuth.setState({ gate: "ready" })}
        >
          暂不初始化，匿名使用
        </button>
      )}
      {status?.auth === "off" && (
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm mt-3 w-full"
          onClick={() => useAuth.setState({ gate: "ready" })}
        >
          账号功能已关闭，返回应用
        </button>
      )}
    </form>
  );
}

function LoginForm({ onNavigate, registrationOpen }: { onNavigate: (s: Screen) => void; registrationOpen: boolean }) {
  const login = useAuth((s) => s.login);
  const error = useAuth((s) => s.error);
  const clearError = useAuth((s) => s.clearError);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    clearError();
    setBusy(true);
    try {
      await login(username.trim(), password);
    } catch {
      // 错误已在 store 里
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={(e) => void submit(e)}>
      <div className="mb-1 flex items-center gap-2">
        <IconLock size={15} className="text-neutral-400" />
        <span className="nx-card-title">登录</span>
      </div>
      <Field label="用户名" value={username} onChange={setUsername} autoComplete="username" />
      <Field label="密码" value={password} onChange={setPassword} type="password" autoComplete="current-password" />
      <GateError error={error} />
      <button className="nx-btn nx-btn-primary mt-4 w-full" disabled={busy || !username.trim() || !password}>
        {busy ? "正在登录并解锁数据…" : "登录"}
      </button>
      <div className="mt-3 flex flex-wrap items-center justify-between gap-x-2 gap-y-1 text-[12px]">
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => onNavigate("recovery")}>
          使用恢复密钥重置密码
        </button>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => onNavigate("enroll")}>
          用配对码添加设备
        </button>
        {registrationOpen && (
          <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => onNavigate("register")}>
            注册新账号
          </button>
        )}
      </div>
    </form>
  );
}

function TotpChallengeForm() {
  const verifyMfa = useAuth((s) => s.verifyMfa);
  const cancelMfa = useAuth((s) => s.cancelMfa);
  const error = useAuth((s) => s.error);
  const clearError = useAuth((s) => s.clearError);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    clearError();
    setBusy(true);
    try {
      await verifyMfa(code.trim());
    } catch {
      // 错误已在 store 里
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={(e) => void submit(e)}>
      <div className="mb-1 flex items-center gap-2">
        <IconShield size={15} className="text-neutral-400" />
        <span className="nx-card-title">两步验证</span>
      </div>
      <p className="nx-hint">
        此账号已开启 TOTP 两步验证。请输入认证器 App 中的 6 位动态码；手机丢失时，也可输入未使用的恢复码。
      </p>
      <Field label="动态码 / 恢复码" value={code} onChange={setCode} mono placeholder="6 位数字或恢复码" autoComplete="one-time-code" />
      <GateError error={error} />
      <button className="nx-btn nx-btn-primary mt-4 w-full" disabled={busy || !code.trim()}>
        {busy ? "验证中…" : "验证并登录"}
      </button>
      <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm mt-3 w-full" onClick={cancelMfa}>
        返回登录
      </button>
    </form>
  );
}

// MfaEnrollForm 是 mfa_required 策略下未绑定会话的强制绑定门:
// 服务端把这类会话锁到只能绑定, 完成 TOTP 绑定(并保存恢复码)后才能进入应用。
function MfaEnrollForm() {
  const finishMfaEnrollment = useAuth((s) => s.finishMfaEnrollment);
  const logout = useAuth((s) => s.logout);
  const [setup, setSetup] = useState<TotpSetup | null>(null);
  const [code, setCode] = useState("");
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null);
  const [copied, setCopied] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const startSetup = async () => {
    setBusy(true);
    setError(null);
    try {
      setSetup(await authApi.totpSetup());
      setCode("");
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    void startSetup();
  }, []);

  const submitConfirm = async (e: FormEvent) => {
    e.preventDefault();
    if (code.trim().length !== 6) {
      setError("请输入认证器中的 6 位动态码");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const r = await authApi.totpConfirm(code.trim());
      setRecoveryCodes(r.recovery_codes);
      setCopied(false);
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

  const finish = async () => {
    setBusy(true);
    setError(null);
    try {
      await finishMfaEnrollment();
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(false);
    }
  };

  const copyRecoveryCodes = async () => {
    setCopied(false);
    setError(null);
    try {
      if (!navigator.clipboard) throw new Error("Clipboard API unavailable");
      await navigator.clipboard.writeText(recoveryCodes?.join("\n") ?? "");
      setCopied(true);
      setConfirmed(true);
    } catch {
      setConfirmed(true);
      setError("复制失败，请手动复制恢复码");
    }
  };

  if (recoveryCodes) {
    return (
      <div>
        <div className="mb-1 flex items-center gap-2">
          <IconShield size={15} className="text-amber-300" />
          <span className="nx-card-title">保存恢复码</span>
        </div>
        <p className="nx-hint">
          恢复码可在无法使用认证器时代替动态码，每个只能使用一次。服务端只保存散列，关闭后无法再次查看，请妥善保存。
        </p>
        <div className="nx-alert nx-alert-danger mt-3 flex flex-col gap-2">
          <div className="grid grid-cols-2 gap-x-4 gap-y-0.5 font-mono text-[12.5px]">
            {recoveryCodes.map((recoveryCode) => (
              <code key={recoveryCode}>{recoveryCode}</code>
            ))}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              className="nx-btn nx-btn-outline nx-btn-sm"
              onClick={() => void copyRecoveryCodes()}
            >
              <IconCopy size={12} />
              {copied ? "已复制" : "复制全部"}
            </button>
          </div>
        </div>
        {error && <GateError error={{ code: "internal", message: error }} />}
        <button className="nx-btn nx-btn-primary mt-4 w-full" disabled={busy || !confirmed} onClick={() => void finish()}>
          <IconCheckCircle size={12} />
          已保存，进入应用
        </button>
        {!confirmed && <p className="nx-hint mt-2 text-center text-[11px]">请先复制或手动保存恢复码</p>}
      </div>
    );
  }

  if (!setup) {
    return (
      <div>
        <div className="mb-1 flex items-center gap-2">
          <IconShield size={15} className="text-amber-300" />
          <span className="nx-card-title">必须启用两步验证</span>
        </div>
        <p className="nx-hint">管理员已要求所有账号启用 TOTP 两步验证，完成绑定前无法使用其他功能。</p>
        {error && <GateError error={{ code: "internal", message: error }} />}
        <button className="nx-btn nx-btn-primary mt-4 w-full" disabled={busy} onClick={() => void startSetup()}>
          {busy ? "生成中…" : "重试"}
        </button>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm mt-3 w-full" onClick={() => void logout()}>
          退出登录
        </button>
      </div>
    );
  }

  return (
    <form onSubmit={(e) => void submitConfirm(e)}>
      <div className="mb-1 flex items-center gap-2">
        <IconShield size={15} className="text-amber-300" />
        <span className="nx-card-title">必须启用两步验证</span>
      </div>
      <p className="nx-hint">
        管理员已要求所有账号启用 TOTP 两步验证，完成绑定前无法使用其他功能。
        用认证器 App（如 Google Authenticator、1Password）导入绑定链接，或手动输入密钥。
      </p>
      <div className="nx-alert nx-alert-info mt-3 flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <code className="nx-code break-all font-mono text-[12.5px]">{setup.otpauth_uri}</code>
          <button
            type="button"
            className="nx-btn nx-btn-outline nx-btn-sm"
            onClick={() => void navigator.clipboard?.writeText(setup.otpauth_uri).catch(() => undefined)}
          >
            <IconCopy size={12} />
            复制
          </button>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <code className="nx-code font-mono text-[12.5px]">{setup.secret.replace(/(.{4})/g, "$1 ").trim()}</code>
          <button
            type="button"
            className="nx-btn nx-btn-outline nx-btn-sm"
            onClick={() => void navigator.clipboard?.writeText(setup.secret).catch(() => undefined)}
          >
            <IconCopy size={12} />
            复制
          </button>
        </div>
      </div>
      <Field label="动态码" value={code} onChange={setCode} mono placeholder="输入 6 位动态码完成绑定" autoComplete="one-time-code" />
      <GateError error={error ? { code: "internal", message: error } : null} />
      <button className="nx-btn nx-btn-primary mt-4 w-full" disabled={busy || code.trim().length === 0}>
        {busy ? "确认中…" : "确认绑定"}
      </button>
      <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm mt-3 w-full" onClick={() => void logout()}>
        退出登录
      </button>
    </form>
  );
}

function RegisterForm({ onBack }: { onBack: () => void }) {
  const register = useAuth((s) => s.register);
  const error = useAuth((s) => s.error);
  const clearError = useAuth((s) => s.clearError);
  const [username, setUsername] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setFormError(null);
    clearError();
    if (password.length < 8) {
      setFormError("密码至少 8 位");
      return;
    }
    if (password !== confirm) {
      setFormError("两次输入的密码不一致");
      return;
    }
    setBusy(true);
    try {
      await register(username.trim(), password, displayName.trim());
    } catch {
      // 错误已在 store 里
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={(e) => void submit(e)}>
      <div className="mb-1 flex items-center gap-2">
        <IconKey size={15} className="text-neutral-400" />
        <span className="nx-card-title">注册</span>
      </div>
      <p className="nx-hint">管理员已开放注册，可创建普通用户账号。</p>
      <Field label="用户名" value={username} onChange={setUsername} autoComplete="username" />
      <Field label="显示名（可选）" value={displayName} onChange={setDisplayName} />
      <Field label="密码" value={password} onChange={setPassword} type="password" placeholder="至少 8 位" autoComplete="new-password" />
      <Field label="确认密码" value={confirm} onChange={setConfirm} type="password" placeholder="再输入一次" autoComplete="new-password" />
      {formError && <GateError error={{ code: "bad_param", message: formError }} />}
      <GateError error={error} />
      <button className="nx-btn nx-btn-primary mt-4 w-full" disabled={busy || !username.trim()}>
        {busy ? "注册中…" : "注册并登录"}
      </button>
      <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm mt-3 w-full" onClick={onBack}>
        返回登录
      </button>
    </form>
  );
}

function ResetRequiredForm() {
  const completeResetRequired = useAuth((s) => s.completeResetRequired);
  const error = useAuth((s) => s.error);
  const clearError = useAuth((s) => s.clearError);
  const [tempPassword, setTempPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setFormError(null);
    clearError();
    if (newPassword.length < 8) {
      setFormError("新密码至少 8 位");
      return;
    }
    if (newPassword !== confirm) {
      setFormError("两次输入的密码不一致");
      return;
    }
    setBusy(true);
    try {
      await completeResetRequired(tempPassword, newPassword);
    } catch {
      // 错误已在 store 里
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={(e) => void submit(e)}>
      <div className="mb-1 flex items-center gap-2">
        <IconShield size={15} className="text-amber-300" />
        <span className="nx-card-title">必须先设置新密码</span>
      </div>
      <p className="nx-hint">
        当前临时密码仅用于本次验证；数据密钥、所有会话和两步验证绑定已清除。设置新密码后会签发新的数据密钥，云端密文需从仍持有数据的设备重新同步。
      </p>
      <Field label="当前临时密码" value={tempPassword} onChange={setTempPassword} type="password" autoComplete="current-password" />
      <Field label="新密码" value={newPassword} onChange={setNewPassword} type="password" placeholder="至少 8 位" autoComplete="new-password" />
      <Field label="确认新密码" value={confirm} onChange={setConfirm} type="password" placeholder="再输入一次" autoComplete="new-password" />
      {formError && <GateError error={{ code: "bad_param", message: formError }} />}
      <GateError error={error} />
      <button className="nx-btn nx-btn-primary mt-4 w-full" disabled={busy || !tempPassword || !newPassword}>
        {busy ? "设置中…" : "设置新密码"}
      </button>
    </form>
  );
}

function RecoveryForm({ onBack }: { onBack: () => void }) {
  const recoveryReset = useAuth((s) => s.recoveryReset);
  const error = useAuth((s) => s.error);
  const clearError = useAuth((s) => s.clearError);
  const [username, setUsername] = useState("");
  const [recoveryKey, setRecoveryKey] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setFormError(null);
    clearError();
    if (newPassword.length < 8) {
      setFormError("新密码至少 8 位");
      return;
    }
    if (newPassword !== confirm) {
      setFormError("两次输入的密码不一致");
      return;
    }
    setBusy(true);
    try {
      await recoveryReset(username.trim(), recoveryKey, newPassword);
    } catch {
      // 错误已在 store 里
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={(e) => void submit(e)}>
      <div className="mb-1 flex items-center gap-2">
        <IconKey size={15} className="text-neutral-400" />
        <span className="nx-card-title">用恢复密钥重置</span>
      </div>
      <p className="nx-hint">
        重置后，旧密码、数据密钥、所有会话和两步验证绑定将失效；请从仍持有数据的设备重新同步云端密文。
      </p>
      <Field label="用户名" value={username} onChange={setUsername} autoComplete="username" />
      <Field label="恢复密钥" value={recoveryKey} onChange={setRecoveryKey} mono placeholder="XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX" autoComplete="off" />
      <Field label="新密码" value={newPassword} onChange={setNewPassword} type="password" placeholder="至少 8 位" autoComplete="new-password" />
      <Field label="确认新密码" value={confirm} onChange={setConfirm} type="password" placeholder="再输入一次" autoComplete="new-password" />
      {formError && <GateError error={{ code: "bad_param", message: formError }} />}
      <GateError error={error} />
      <button className="nx-btn nx-btn-primary mt-4 w-full" disabled={busy || !username.trim() || !recoveryKey.trim() || !newPassword}>
        {busy ? "重置中…" : "重置密码并登录"}
      </button>
      <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm mt-3 w-full" onClick={onBack}>
        返回登录
      </button>
    </form>
  );
}

function EnrollForm({ onBack }: { onBack: () => void }) {
  const enrollDevice = useAuth((s) => s.enrollDevice);
  const login = useAuth((s) => s.login);
  const error = useAuth((s) => s.error);
  const clearError = useAuth((s) => s.clearError);
  const [code, setCode] = useState("");
  const [deviceName, setDeviceName] = useState("这台浏览器");
  const [device, setDevice] = useState<AccountDevice | null>(null);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  // 第一步: 配对码登记设备(公共路由, 只登记不建会话)。
  const submitEnroll = async (e: FormEvent) => {
    e.preventDefault();
    setFormError(null);
    clearError();
    const trimmedCode = code.trim();
    const name = deviceName.trim();
    if (!trimmedCode) {
      setFormError("请输入配对码");
      return;
    }
    if (!name) {
      setFormError("请输入设备名");
      return;
    }
    setBusy(true);
    try {
      setDevice(await enrollDevice(trimmedCode, name));
    } catch {
      // 错误已在 store 里
    } finally {
      setBusy(false);
    }
  };

  // 第二步: 走统一登录, 会话绑定到刚登记的设备。
  const submitLogin = async (e: FormEvent) => {
    e.preventDefault();
    clearError();
    setBusy(true);
    try {
      await login(username.trim(), password, device?.id);
    } catch {
      // 错误已在 store 里
    } finally {
      setBusy(false);
    }
  };

  if (device) {
    return (
      <form onSubmit={(e) => void submitLogin(e)}>
        <div className="mb-1 flex items-center gap-2">
          <IconCheckCircle size={15} className="text-green-400" />
          <span className="nx-card-title">设备已登记</span>
        </div>
        <p className="nx-hint">
          设备「{device.name}」已添加到账号。请输入账号密码完成登录；之后可在「设置 → 账号同步 → 账号 → 登录设备」中查看或吊销。
        </p>
        <Field label="用户名" value={username} onChange={setUsername} autoComplete="username" />
        <Field label="密码" value={password} onChange={setPassword} type="password" autoComplete="current-password" />
        <GateError error={error} />
        <button className="nx-btn nx-btn-primary mt-4 w-full" disabled={busy || !username.trim() || !password}>
          {busy ? "正在登录并解锁数据…" : "登录"}
        </button>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm mt-3 w-full" onClick={onBack}>
          返回登录
        </button>
      </form>
    );
  }

  return (
    <form onSubmit={(e) => void submitEnroll(e)}>
      <div className="mb-1 flex items-center gap-2">
        <IconKey size={15} className="text-neutral-400" />
        <span className="nx-card-title">用配对码添加设备</span>
      </div>
      <p className="nx-hint">
        在另一台已登录设备的「设置 → 账号同步 → 账号」中选择「添加设备」，生成一次性配对码（15 分钟内有效），然后在此输入配对码完成添加。
      </p>
      <Field label="配对码" value={code} onChange={setCode} mono placeholder="另一台设备上显示的配对码" autoComplete="off" />
      <Field label="设备名" value={deviceName} onChange={setDeviceName} placeholder="这台浏览器" autoComplete="off" />
      {formError && <GateError error={{ code: "bad_param", message: formError }} />}
      <GateError error={error} />
      <button className="nx-btn nx-btn-primary mt-4 w-full" disabled={busy || !code.trim() || !deviceName.trim()}>
        {busy ? "添加中…" : "添加并继续"}
      </button>
      <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm mt-3 w-full" onClick={onBack}>
        返回登录
      </button>
    </form>
  );
}

function RecoveryKeyScreen({ issue, onDone }: { issue: RecoveryKeyIssue; onDone: () => void }) {
  const [revealed, setRevealed] = useState(false);
  const [copied, setCopied] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [copyError, setCopyError] = useState<string | null>(null);

  const copy = async () => {
    setCopied(false);
    setCopyError(null);
    try {
      if (!navigator.clipboard) throw new Error("Clipboard API unavailable");
      await navigator.clipboard.writeText(issue.formatted);
      setCopied(true);
      setConfirmed(true);
    } catch {
      setRevealed(true);
      setConfirmed(true);
      setCopyError("复制失败，请手动复制恢复密钥");
    }
  };

  return (
    <div>
      <div className="mb-1 flex items-center gap-2">
        <IconShield size={15} className="text-amber-300" />
        <span className="nx-card-title">恢复密钥（仅显示一次）</span>
      </div>
      <p className="nx-hint">
        这是找回账号的唯一凭证，服务端只保存散列，关闭后无法再次查看。请复制并妥善保存（如密码管理器或离线介质）。
      </p>
      <div className="nx-alert nx-alert-danger mt-3 flex flex-col gap-2">
        <code className="nx-code break-all font-mono text-[13px]">{revealed ? issue.formatted : "•".repeat(39)}</code>
        <div className="flex flex-wrap items-center gap-2">
          <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setRevealed((v) => !v)}>
            {revealed ? "隐藏" : "显示"}
          </button>
          <button className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => void copy()}>
            <IconCopy size={12} />
            {copied ? "已复制" : "复制"}
          </button>
        </div>
      </div>
      {copyError && (
        <div className="nx-alert nx-alert-danger mt-3 flex items-start gap-2" role="alert">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span>{copyError}</span>
        </div>
      )}
      <div className="nx-alert nx-alert-info mt-3 flex items-start gap-2">
        <IconInfo size={14} className="mt-0.5 shrink-0" />
        <div>
          恢复密钥仅用于忘记密码时重置账号；日常解密使用密码。两者都需要保密。
        </div>
      </div>
      <button className="nx-btn nx-btn-primary mt-4 w-full" disabled={!confirmed} onClick={onDone}>
        <IconCheckCircle size={12} />
        已保存，进入应用
      </button>
      {!confirmed && <p className="nx-hint mt-2 text-center text-[11px]">请先复制或手动保存恢复密钥</p>}
    </div>
  );
}
