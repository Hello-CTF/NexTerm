import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { vaultApi } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { useUi } from "../../app/store";
import { useAuth } from "../auth/store";
import { formatBinding, useKeybindings } from "../../app/keybindings";
import {
  MAX_APP_LOCK_IDLE_MINUTES,
  requestAppLock,
  setAppLockIdleMinutes,
  useAppLockIdleMinutes,
} from "../../app/appLock";
import { IconLock } from "../../ui/icons";
import { t } from "../../i18n";

// AppLockCard configures the whole-app lock screen: lock now, and an optional idle auto-lock kept on this device.
export function AppLockCard() {
  const pushToast = useUi((s) => s.pushToast);
  const idleMinutes = useAppLockIdleMinutes();
  const [draft, setDraft] = useState(String(idleMinutes));
  useEffect(() => setDraft(String(idleMinutes)), [idleMinutes]);
  const binding = useKeybindings().lockApp;
  const user = useAuth((s) => s.user);
  const vault = useQuery({
    queryKey: ["vault-status"],
    queryFn: () => vaultApi.status(),
    enabled: !WEB,
    refetchOnWindowFocus: false,
  }).data;
  const available = WEB
    ? !!user
    : !!vault && vault.initialized && vault.mode === "master" && !vault.passwordless && !vault.managed;

  const unchanged = draft.trim() === String(idleMinutes);
  const save = () => {
    const trimmed = draft.trim();
    const minutes = Number(trimmed);
    if (trimmed === "" || !Number.isInteger(minutes) || minutes < 0 || minutes > MAX_APP_LOCK_IDLE_MINUTES) {
      pushToast("error", t("自动锁定时长须为 0 至 1440 的整数分钟，0 表示不自动锁定"));
      return;
    }
    setAppLockIdleMinutes(minutes);
    pushToast(
      "success",
      minutes === 0 ? t("已关闭闲置自动锁定 NexTerm") : t("闲置 {minutes} 分钟后自动锁定 NexTerm", { minutes }),
    );
  };

  return (
    <section id="settings-app-lock" className="nx-card scroll-mt-12">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconLock size={15} className="text-neutral-400" />
        <span className="nx-card-title">{t("锁定 NexTerm")}</span>
      </div>
      <p className="nx-hint mt-1">
        {WEB
          ? t("锁定后界面全部隐藏、不接收输入，终端与传输继续在后台运行；输入账号密码解锁。锁定期间的系统通知只显示“NexTerm 已锁定”和条数。")
          : t("锁定后界面全部隐藏、不接收输入，终端与传输继续在后台运行；输入凭据库保护密码解锁（移动端可用生物识别）。锁定期间的系统通知只显示“NexTerm 已锁定”和条数。")}
      </p>
      {!available && (
        <p className="nx-hint mt-1.5 text-amber-300" data-setting="app-lock-unavailable">
          {WEB
            ? t("需要登录服务端账号后才能锁定。")
            : t("需要先在上方「凭据保护」中设置保护密码后才能锁定。")}
        </p>
      )}
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <button
          type="button"
          className="nx-btn nx-btn-outline nx-btn-sm"
          disabled={!available}
          onClick={() => void requestAppLock()}
        >
          {t("立即锁定")}
        </button>
        <span className="nx-hint">
          {binding ? t("快捷键 {binding}", { binding: formatBinding(binding) }) : t("未绑定快捷键，可在「快捷键」中设置")}
        </span>
      </div>
      <div className="mt-3 border-t border-neutral-800/60 pt-3">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-[12.5px] text-neutral-200" data-setting="app-lock-idle">
            {t("闲置自动锁定 NexTerm")}
          </span>
          <input
            type="number"
            className="nx-input nx-input-sm w-[96px]"
            aria-label={t("闲置自动锁定 NexTerm 的分钟数")}
            min={0}
            max={MAX_APP_LOCK_IDLE_MINUTES}
            step={1}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                if (!unchanged) save();
              }
            }}
          />
          <span className="nx-hint">{t("分钟，0 为不自动锁定")}</span>
          <button
            type="button"
            className="nx-btn nx-btn-outline nx-btn-sm"
            disabled={unchanged}
            title={unchanged ? t("尚无改动") : undefined}
            onClick={save}
          >
            {t("保存")}
          </button>
        </div>
        <p className="nx-hint mt-1.5">
          {t("本机设置，不随账号同步。无键盘、鼠标或触摸操作超过该时长后自动锁定；终端输出不算操作。")}
        </p>
      </div>
    </section>
  );
}
