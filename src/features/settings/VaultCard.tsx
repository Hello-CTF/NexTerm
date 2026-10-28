import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { biometricApi, vaultApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { DESKTOP } from "../../ipc/env";
import { isMobileShell } from "../../app/shell";
import { ask, promptText } from "../../ui/dialogs";
import { PasswordChangeForm } from "../../ui/PasswordChangeForm";
import { describeError } from "../../ui/errorText";
import { IconLock } from "../../ui/icons";

// VaultCard protects the local credential vault with an optional password.
// Every action here is an explicit button press with a busy state, so a second
// click cannot submit the same change twice.
export function VaultCard() {
  const pushToast = useUi((s) => s.pushToast);
  const qc = useQueryClient();
  const [protPwd, setProtPwd] = useState("");
  const [pendingEnable, setPendingEnable] = useState(false);
  const [vaultPasswordOpen, setVaultPasswordOpen] = useState(false);
  const [autoLockDraft, setAutoLockDraft] = useState("");
  const [busy, setBusy] = useState(false);

  // refetchOnWindowFocus off: a refetch on window focus would overwrite the auto-lock draft being edited.
  const vault =
    useQuery({
      queryKey: ["vault-status"],
      queryFn: () => vaultApi.status(),
      refetchOnWindowFocus: false,
    }).data ?? null;
  const vaultPasswordProtected = vault?.mode === "master" && !vault.passwordless;
  const vaultPasswordless = vault?.mode === "master" && !!vault?.passwordless;
  const systemProtection = DESKTOP && !!vault?.systemProtection;
  const refreshVault = () => void qc.invalidateQueries({ queryKey: ["vault-status"] });
  useEffect(() => {
    setAutoLockDraft(vault ? String(vault.autoLockMinutes) : "");
  }, [vault]);

  // run keeps one vault change in flight and reports its outcome in words.
  const run = async (action: () => Promise<unknown>, success: string) => {
    if (busy) return false;
    setBusy(true);
    try {
      await action();
      pushToast("success", success);
      refreshVault();
      return true;
    } catch (e) {
      pushToast("error", `${describeError(e)}。凭据没有改动，请检查后重试。`);
      return false;
    } finally {
      setBusy(false);
    }
  };

  const autoLockUnchanged = !!vault && autoLockDraft.trim() === String(vault.autoLockMinutes);
  const saveAutoLock = () => {
    const trimmed = autoLockDraft.trim();
    const minutes = Number(trimmed);
    if (trimmed === "" || !Number.isInteger(minutes) || minutes < 0 || minutes > 1440) {
      pushToast("error", "自动锁定时长须为 0 至 1440 的整数分钟，0 表示不自动锁定");
      return;
    }
    void run(
      () => vaultApi.setAutoLock(minutes),
      minutes === 0 ? "已关闭闲置自动锁定" : `闲置 ${minutes} 分钟后自动锁定`,
    );
  };

  // changeVaultPassword runs from PasswordChangeForm: it verifies the current
  // password, then sets the new one. An empty new password removes password
  // protection; errors are thrown so the form shows them inline.
  const changeVaultPassword = async (old: string, next: string) => {
    await vaultApi.unlock(old);
    if (next === "" && systemProtection) {
      await vaultApi.initDpapi(old);
      pushToast("success", "已关闭密码保护 · 凭据改由系统级密钥保护");
    } else {
      await vaultApi.changePassword(old, next);
      // On iOS/Android with biometric material enrolled, silently re-store it with the new password after a change; otherwise biometric unlock would consistently fail
      if (next !== "" && isMobileShell()) {
        try {
          const bio = await biometricApi.status();
          if (bio.canEvaluate && bio.hasMaterial) await biometricApi.enable(next);
        } catch {
          // A failed re-store only means the password unlock is needed again next time
        }
      }
      pushToast(
        "success",
        next === ""
          ? "已关闭密码保护 · 凭据不受影响"
          : vaultPasswordless
            ? "已开启密码保护 · 凭据不受影响"
            : "密码已修改 · 凭据不受影响",
      );
    }
    setVaultPasswordOpen(false);
    refreshVault();
  };

  const disableVaultEncryption = async () => {
    const ok = await ask("关闭加密？\n关闭后凭据改由系统级密钥保护，无需密码即可使用。");
    if (!ok) return;
    await run(() => vaultApi.initDpapi(""), "已关闭加密 · 凭据改由系统级密钥保护");
  };

  const disablePasswordProtection = async () => {
    const confirmText = systemProtection
      ? "关闭密码保护？\n关闭后凭据改由系统级密钥保护，无需密码即可使用。"
      : "关闭密码保护？\n关闭后凭据无需密码即可使用，但任何拿到数据目录的人都能解密。";
    if (!(await ask(confirmText))) return;
    const old = await promptText("输入当前保护密码：", "", { secret: true });
    if (old === null) return;
    await run(
      () => (systemProtection ? vaultApi.initDpapi(old) : vaultApi.changePassword(old, "")),
      "已关闭密码保护",
    );
  };

  return (
    <section id="settings-vault" className="nx-card scroll-mt-12">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconLock size={15} className="text-neutral-400" />
        <span className="nx-card-title">凭据保护</span>
        {vault &&
          (vaultPasswordProtected ? (
            <span className="nx-badge nx-badge-green">已开启</span>
          ) : vault.mode === "not_init" ? (
            <span className="nx-badge nx-badge-amber">未初始化</span>
          ) : vaultPasswordless ? (
            <span className="nx-badge nx-badge-amber">加密已启用 · 未设密码</span>
          ) : (
            <span className="nx-badge">未开启</span>
          ))}
        {busy && (
          <span className="nx-hint" role="status">
            正在处理…
          </span>
        )}
      </div>

      <label className="flex cursor-pointer items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="text-[12.5px] text-neutral-200" data-setting="vault-password">用密码保护凭据</div>
          <p className="nx-hint mt-0.5">
            {vaultPasswordProtected
              ? "开启：每次启动需输入密码后方可使用凭据。"
              : vault?.mode === "not_init"
                ? "尚未初始化：启用保护后才能保存密码或私钥。"
                : vaultPasswordless
                  ? "已启用：凭据加密保存、无需密码即可使用，但任何拿到数据目录的人都能解密；设置密码后，每次启动需输入密码。"
                  : "关闭：无需密码，凭据由系统级密钥保护，启动后直接使用。"}
          </p>
        </div>
        <input
          type="checkbox"
          className="mt-0.5 h-4 w-4 shrink-0"
          checked={vaultPasswordProtected}
          disabled={busy}
          onChange={() => {
            if (vaultPasswordProtected) {
              void disablePasswordProtection();
            } else if (vaultPasswordless) {
              setVaultPasswordOpen(true);
            } else {
              setProtPwd("");
              setPendingEnable(true);
            }
          }}
        />
      </label>

      {pendingEnable && !vaultPasswordProtected && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <input
            type="password"
            className="nx-input max-w-[240px]"
            aria-label="保护密码"
            placeholder="设置保护密码（至少 8 位，留空则不设置）"
            value={protPwd}
            autoComplete="off"
            disabled={busy}
            onChange={(e) => setProtPwd(e.target.value)}
          />
          <button
            type="button"
            className="nx-btn nx-btn-primary"
            disabled={busy || (protPwd.length > 0 && protPwd.length < 8)}
            title={protPwd.length > 0 && protPwd.length < 8 ? "密码至少 8 位" : undefined}
            onClick={() =>
              void run(
                () => vaultApi.initMaster(protPwd),
                protPwd === "" ? "已完成初始化，未设置密码" : "已开启密码保护",
              ).then((ok) => {
                if (ok) setPendingEnable(false);
              })
            }
          >
            {busy ? "正在启用…" : "启用保护"}
          </button>
          <button type="button" className="nx-btn nx-btn-ghost" disabled={busy} onClick={() => setPendingEnable(false)}>
            取消
          </button>
          <p className="nx-hint w-full text-[11px]">
            保护密码仅用于此处凭据库，不会随账号同步。留空则不设密码，任何拿到数据目录的人都能解密；忘记密码后，已保存的凭据将无法恢复。
          </p>
        </div>
      )}

      {vault?.mode === "master" && vaultPasswordOpen && (
        <div className="mt-3">
          <PasswordChangeForm
            requireCurrent={!vaultPasswordless}
            submitLabel={vaultPasswordless ? "设置密码" : "确认修改"}
            allowEmpty={!vaultPasswordless}
            emptyLabel="关闭密码保护"
            onSubmit={changeVaultPassword}
            onCancel={() => setVaultPasswordOpen(false)}
          />
          <p className="nx-hint mt-1.5 text-[11px]">保护密码只用于这台设备上的凭据库，忘记后已保存的凭据无法恢复。</p>
        </div>
      )}

      {vault?.mode === "master" && !vaultPasswordOpen && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          {!vaultPasswordless && <span className="nx-hint">锁定与解锁在「凭据」页</span>}
          <button
            type="button"
            className={`nx-btn nx-btn-sm ${vaultPasswordless ? "nx-btn-primary" : "nx-btn-outline"}`}
            disabled={busy}
            onClick={() => setVaultPasswordOpen(true)}
          >
            {vaultPasswordless ? "设置密码" : "修改密码"}
          </button>
          {vaultPasswordless && systemProtection && (
            <button
              type="button"
              className="nx-btn nx-btn-outline nx-btn-sm"
              disabled={busy}
              onClick={() => void disableVaultEncryption()}
            >
              关闭加密
            </button>
          )}
        </div>
      )}

      {vaultPasswordProtected && (
        <div className="mt-3 border-t border-neutral-800/60 pt-3">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-[12.5px] text-neutral-200" data-setting="vault-auto-lock">闲置自动锁定</span>
            <input
              type="number"
              className="nx-input nx-input-sm w-[96px]"
              aria-label="闲置自动锁定分钟数"
              min={0}
              max={1440}
              step={1}
              value={autoLockDraft}
              disabled={busy}
              onChange={(e) => setAutoLockDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  if (!autoLockUnchanged) saveAutoLock();
                }
              }}
            />
            <span className="nx-hint">分钟，0 为不自动锁定</span>
            <button
              type="button"
              className="nx-btn nx-btn-outline nx-btn-sm"
              disabled={busy || autoLockUnchanged}
              title={autoLockUnchanged ? "没有改动" : undefined}
              onClick={saveAutoLock}
            >
              保存
            </button>
          </div>
          <p className="nx-hint mt-1.5">
            闲置超过该时长后凭据库自动锁定，再次使用需输入保护密码。
          </p>
        </div>
      )}
    </section>
  );
}
