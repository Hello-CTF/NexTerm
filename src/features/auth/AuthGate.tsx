// 账号门:首次初始化 / 登录 / 注册 / reset_required 强制改密 / 恢复密钥一次性展示。
// 仅在 WEB/DEMO 且门状态非 ready 时渲染全屏覆盖。

import { useEffect, useState, type FormEvent } from "react";
import { useAuth, type RecoveryKeyIssue } from "./store";
import { DEMO } from "../../demo";
import { describeError } from "../../ui/errorText";
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

type Screen = "setup" | "login" | "register" | "reset_required" | "recovery";

export function AuthGate() {
  const gate = useAuth((s) => s.gate);
  const status = useAuth((s) => s.status);
  const pendingRecoveryKey = useAuth((s) => s.pendingRecoveryKey);
  const clearPendingRecoveryKey = useAuth((s) => s.clearPendingRecoveryKey);

  const [screen, setScreen] = useState<Screen | null>(null);
  const [recoveryIssue, setRecoveryIssue] = useState<RecoveryKeyIssue | null>(null);

  useEffect(() => {
    void useAuth.getState().refresh();
  }, []);

  useEffect(() => {
    if (gate === "setup") setScreen("setup");
    else if (gate === "login") setScreen("login");
    else if (gate === "reset_required") setScreen("reset_required");
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
        {screen === "login" && <LoginForm onNavigate={setScreen} registrationOpen={status?.registration_open ?? false} />}
        {screen === "register" && <RegisterForm onBack={() => setScreen("login")} />}
        {screen === "reset_required" && <ResetRequiredForm />}
        {screen === "recovery" && <RecoveryForm onBack={() => setScreen("login")} />}
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
        这台服务器还没有任何账号。输入服务器控制台打印的一次性初始化码,创建超级管理员。
        初始化码只使用一次;用完即焚,服务器不再接受第二次初始化。
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
        {busy ? "登录中…(正在解锁加密数据)" : "登录"}
      </button>
      <div className="mt-3 flex items-center justify-between text-[12px]">
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => onNavigate("recovery")}>
          忘记密码?用恢复密钥重置
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
      <p className="nx-hint">这台服务器已由管理员开放注册(注册默认关闭)。注册即可创建普通用户账号。</p>
      <Field label="用户名" value={username} onChange={setUsername} autoComplete="username" />
      <Field label="显示名(可选)" value={displayName} onChange={setDisplayName} />
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
        管理员已重置这个账号。旧密码与加密数据已被清除;设置新密码后会签发新的数据密钥,
        云端保存的加密内容需要从仍持有数据的设备重新同步。
      </p>
      <Field label="当前(临时)密码" value={tempPassword} onChange={setTempPassword} type="password" autoComplete="current-password" />
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
        输入你保存的恢复密钥(注册或修改密码时签发)。重置后旧密码与全部会话立即失效。
        注意:服务端旧数据密钥已被清除,云端正文需要从仍持有数据的设备重新同步。
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

function RecoveryKeyScreen({ issue, onDone }: { issue: RecoveryKeyIssue; onDone: () => void }) {
  const [revealed, setRevealed] = useState(false);
  const [copied, setCopied] = useState(false);

  const copy = () => {
    // 点击即视为已复制(剪贴板尽力而为;失败也可手动选中已显示的密钥)
    setCopied(true);
    void navigator.clipboard?.writeText(issue.formatted).catch(() => undefined);
  };

  return (
    <div>
      <div className="mb-1 flex items-center gap-2">
        <IconShield size={15} className="text-amber-300" />
        <span className="nx-card-title">恢复密钥(只显示这一次)</span>
      </div>
      <p className="nx-hint">
        这是找回账号的唯一凭证。服务端只保存它的散列,关闭后<b>无法再次查看</b>。
        请立即复制并保存在安全的地方(密码管理器或离线介质)。
      </p>
      <div className="nx-alert nx-alert-danger mt-3 flex flex-col gap-2">
        <code className="nx-code break-all font-mono text-[13px]">{revealed ? issue.formatted : "•".repeat(39)}</code>
        <div className="flex flex-wrap items-center gap-2">
          <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setRevealed((v) => !v)}>
            {revealed ? "隐藏" : "显示"}
          </button>
          <button className="nx-btn nx-btn-outline nx-btn-sm" onClick={copy}>
            <IconCopy size={12} />
            {copied ? "已复制" : "复制"}
          </button>
        </div>
      </div>
      <div className="nx-alert nx-alert-info mt-3 flex items-start gap-2">
        <IconInfo size={14} className="mt-0.5 shrink-0" />
        <div>
          恢复密钥用于在忘记密码时重置账号。它和数据密钥是两回事:日常解密靠密码,
          恢复密钥只在重置时使用,同样需要保密。
        </div>
      </div>
      <button className="nx-btn nx-btn-primary mt-4 w-full" disabled={!copied} onClick={onDone}>
        <IconCheckCircle size={12} />
        我已安全保存
      </button>
      {!copied && <p className="nx-hint mt-2 text-center text-[11px]">先复制密钥,再进入应用</p>}
      {DEMO && <p className="nx-hint mt-2 text-center text-[11px]">演示模式:这是一枚假密钥</p>}
    </div>
  );
}
