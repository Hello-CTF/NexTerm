import { useState } from "react";
import { WEB } from "../ipc/env";
import { useAuth } from "../features/auth/store";
import { useUi } from "./store";
import { describeError } from "../ui/errorText";
import { IconLock, IconXCircle } from "../ui/icons";

// 浏览器刷新后会话经 cookie 恢复但 DEK 已随内存清空,自动同步静默停摆;
// 这条常驻横幅是设置页 SyncCard 之外的主界面解锁入口,解锁成功或 dismiss 前不消失。
export function DekUnlockBanner() {
  const user = useAuth((s) => s.user);
  const dek = useAuth((s) => s.dek);
  const gate = useAuth((s) => s.gate);
  const unlockDEK = useAuth((s) => s.unlockDEK);
  const [dismissedFor, setDismissedFor] = useState<string | null>(null);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (!WEB || gate !== "ready" || !user || dek !== null || dismissedFor === user.id) return null;

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
