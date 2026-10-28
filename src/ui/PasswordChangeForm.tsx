import { useId, useState, type FormEvent } from "react";
import { describeError } from "./errorText";
import { IconCheck, IconEye, IconEyeOff, IconXCircle } from "./icons";

export const MIN_PASSWORD_LENGTH = 8;

export interface PasswordChangeFormProps {
  // requireCurrent shows the current-password field; setting a first password skips it.
  requireCurrent: boolean;
  submitLabel: string;
  // allowEmpty lets both new fields stay empty to remove the password; emptyLabel names that action.
  allowEmpty?: boolean;
  emptyLabel?: string;
  onSubmit: (current: string, next: string) => Promise<void>;
  onCancel: () => void;
}

function Check({ ok, failed, children }: { ok: boolean; failed?: boolean; children: string }) {
  const tone = ok ? "text-green-400" : failed ? "text-red-400" : "text-neutral-500";
  return (
    <li className={`flex items-center gap-1 ${tone}`} aria-label={`${children}：${ok ? "已满足" : "未满足"}`}>
      {failed ? <IconXCircle size={11} /> : <IconCheck size={11} className={ok ? "" : "opacity-40"} />}
      {children}
    </li>
  );
}

// PasswordChangeForm collects the current and new password with live checks:
// the length rule and the confirmation match turn green as soon as they hold,
// and a mismatch turns red, so the submit button only enables on a valid pair.
export function PasswordChangeForm({ requireCurrent, submitLabel, allowEmpty = false, emptyLabel, onSubmit, onCancel }: PasswordChangeFormProps) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [visible, setVisible] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const checksId = useId();

  const removing = allowEmpty && next === "" && confirm === "";
  const longEnough = next.length >= MIN_PASSWORD_LENGTH;
  const matches = next !== "" && next === confirm;
  const mismatch = confirm !== "" && next !== confirm;
  const valid = (!requireCurrent || current !== "") && (removing || (longEnough && matches));

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!valid || busy) return;
    setBusy(true);
    setError(null);
    try {
      await onSubmit(current, removing ? "" : next);
    } catch (cause) {
      setError(describeError(cause));
    } finally {
      setBusy(false);
    }
  };

  const type = visible ? "text" : "password";
  return (
    <form onSubmit={(event) => void submit(event)} className="flex max-w-[320px] flex-col gap-2">
      {requireCurrent && (
        <input
          className="nx-input"
          type={type}
          placeholder="当前密码"
          aria-label="当前密码"
          value={current}
          autoComplete="current-password"
          autoFocus
          onChange={(event) => setCurrent(event.target.value)}
        />
      )}
      <div className="relative">
        <input
          className="nx-input w-full pr-8"
          type={type}
          placeholder={`新密码（至少 ${MIN_PASSWORD_LENGTH} 位）`}
          aria-label="新密码"
          aria-describedby={checksId}
          value={next}
          autoComplete="new-password"
          autoFocus={!requireCurrent}
          onChange={(event) => setNext(event.target.value)}
        />
        <button
          type="button"
          className="nx-icon-btn nx-icon-btn-sm absolute right-1 top-1/2 -translate-y-1/2"
          title={visible ? "隐藏密码" : "显示密码"}
          aria-label={visible ? "隐藏密码" : "显示密码"}
          onClick={() => setVisible((value) => !value)}
        >
          {visible ? <IconEyeOff size={12} /> : <IconEye size={12} />}
        </button>
      </div>
      <input
        className={`nx-input ${mismatch ? "border-red-500/60" : matches ? "border-green-500/50" : ""}`}
        type={type}
        placeholder="再输入一次新密码"
        aria-label="确认新密码"
        aria-invalid={mismatch}
        value={confirm}
        autoComplete="new-password"
        onChange={(event) => setConfirm(event.target.value)}
      />
      {removing ? (
        <div id={checksId} className="text-[11px] text-amber-300">
          两个新密码都留空，将{emptyLabel ?? "移除密码"}。
        </div>
      ) : (
        <ul id={checksId} className="flex flex-wrap gap-x-3 gap-y-0.5 text-[11px]">
          <Check ok={longEnough}>{`至少 ${MIN_PASSWORD_LENGTH} 位`}</Check>
          <Check ok={matches} failed={mismatch}>{mismatch ? "两次输入不一致" : "两次输入一致"}</Check>
        </ul>
      )}
      {error && (
        <div className="nx-alert nx-alert-danger flex items-start gap-2" role="alert">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span>{error}</span>
        </div>
      )}
      <div className="flex gap-1.5">
        <button type="submit" className={`nx-btn nx-btn-sm ${removing ? "nx-btn-danger" : "nx-btn-primary"}`} disabled={!valid || busy}>
          {busy ? "正在处理…" : removing ? (emptyLabel ?? "移除密码") : submitLabel}
        </button>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" disabled={busy} onClick={onCancel}>
          取消
        </button>
      </div>
    </form>
  );
}
