import { useEffect, useState } from "react";
import { localHelperApi, type LocalTerminalMode } from "../../ipc/commands";
import { forgetLocalDurableReadiness, useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { refreshLocalHelperStatus, useLocalHelperStatus } from "../terminal/localHelperStatus";
import { SyncScopeBadge } from "./SyncScopeBadge";
import { t } from "../../i18n";

const LOCAL_MODE_OPTIONS: { value: LocalTerminalMode; label: string }[] = [
  { value: "durable", label: t("持久终端") },
  { value: "regular", label: t("临时终端") },
];

const STATE_TEXT: Record<string, string> = {
  idle: t("未运行"),
  starting: t("正在启动"),
  ready: t("运行中"),
  outdated: t("旧版本"),
  failed: t("启动失败"),
};

function sessionCount(activeSessions: number | undefined, activityKnown: boolean | undefined): string {
  return activityKnown === false ? t("数量未知的会话") : t("{value} 个会话", { value: activeSessions ?? 0 });
}

// LocalHelperSettings shows the local helper that keeps local terminals running after the app exits, lets this
// device keep local terminals regular, and asks for consent before a helper of another build, and every session
// in it, is restarted.
export function LocalHelperSettings() {
  const status = useLocalHelperStatus();
  const pushToast = useUi((s) => s.pushToast);
  const [busy, setBusy] = useState(false);
  const [mode, setMode] = useState<LocalTerminalMode | null>(null);
  const enabled = Boolean(status && status.state !== "disabled");

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    void Promise.resolve()
      .then(() => localHelperApi.getMode())
      .then((value) => { if (!cancelled) setMode(value); })
      .catch((error) => {
        if (!cancelled) pushToast("error", t("读取本机终端模式失败：{value}", { value: describeError(error) }));
      });
    return () => { cancelled = true; };
  }, [enabled, pushToast]);

  if (!status || !enabled) return null;

  const chooseMode = async (value: LocalTerminalMode) => {
    if (busy || value === mode) return;
    setBusy(true);
    try {
      setMode(await localHelperApi.setMode(value));
      forgetLocalDurableReadiness();
    } catch (error) {
      pushToast("error", t("保存本机终端模式失败：{value}", { value: describeError(error) }));
    } finally {
      setBusy(false);
    }
  };

  const restart = async () => {
    if (busy || !status.consentToken) return;
    const sessions = sessionCount(status.activeSessions, status.activityKnown);
    const ok = await ask(
      t("重启本机持久终端组件将结束其中正在运行的 {sessions}，未保存的工作将丢失。\n重启后新建的本机终端将使用当前版本的组件。确定要重启吗？", { sessions }),
      { title: t("重启本机持久终端组件"), kind: "warning" },
    );
    if (!ok) return;
    setBusy(true);
    try {
      await localHelperApi.restart(status.consentToken);
      pushToast("info", t("本机持久终端组件已重启"));
    } catch (error) {
      pushToast("error", describeError(error));
    } finally {
      await refreshLocalHelperStatus();
      setBusy(false);
    }
  };

  const retry = async () => {
    if (busy) return;
    setBusy(true);
    try {
      await localHelperApi.retry();
    } catch (error) {
      pushToast("error", describeError(error));
    } finally {
      await refreshLocalHelperStatus();
      setBusy(false);
    }
  };

  const badge = status.state === "ready" ? "nx-badge nx-badge-green" : status.state === "starting" || status.state === "idle" ? "nx-badge" : "nx-badge nx-badge-amber";
  const restartable = status.consentToken && (status.state === "outdated" || status.updateAvailable);
  return (
    <div data-local-helper-state={status.state}>
      <div className="nx-setting-label mb-1.5 flex flex-wrap items-center gap-2">
        {t("本机终端模式")}
        <SyncScopeBadge scope="local" />
        <span className={badge}>{t("持久终端组件")}{STATE_TEXT[status.state] ?? status.state}</span>
        {status.updateAvailable && <span className="nx-badge nx-badge-blue">{t("可更新")}</span>}
      </div>
      <div className="nx-segment" role="group" aria-label={t("本机终端模式")} aria-busy={busy}>
        {LOCAL_MODE_OPTIONS.map((option) => (
          <button
            key={option.value}
            type="button"
            className={`nx-segment-item ${mode === option.value ? "is-active" : ""}`}
            aria-pressed={mode === option.value}
            disabled={busy || mode === null}
            onClick={() => void chooseMode(option.value)}
          >
            {option.label}
          </button>
        ))}
      </div>
      <p className="nx-hint mt-1.5">
        {mode === "regular"
          ? t("本机新建的终端随 NexTerm 退出而结束，含 AI 和定时任务打开的。")
          : t("本机新建的终端在退出 NexTerm 后继续运行，含 AI 和定时任务打开的。")}
      </p>
      {status.reason && status.state !== "idle" && <p className="nx-hint mt-1.5 text-amber-300">{status.reason}</p>}
      {status.warning && <p className="nx-hint mt-1.5 text-amber-300">{status.warning}</p>}
      {restartable && (
        <button type="button" className="nx-btn nx-btn-danger nx-btn-sm mt-2" disabled={busy} onClick={() => void restart()}>
          {t("重启持久终端组件（结束")} {sessionCount(status.activeSessions, status.activityKnown)}）
        </button>
      )}
      {status.state === "failed" && (
        <button type="button" className="nx-btn nx-btn-outline nx-btn-sm mt-2" disabled={busy} onClick={() => void retry()}>
          {t("重试启动持久终端组件")}
        </button>
      )}
    </div>
  );
}
