import { useEffect, useState } from "react";
import { localHelperApi, type LocalTerminalMode } from "../../ipc/commands";
import { forgetDurableReadiness, useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { refreshLocalHelperStatus, useLocalHelperStatus } from "../terminal/localHelperStatus";

const LOCAL_MODE_OPTIONS: { value: LocalTerminalMode; label: string }[] = [
  { value: "durable", label: "持久" },
  { value: "regular", label: "普通" },
];

const STATE_TEXT: Record<string, string> = {
  idle: "未运行",
  starting: "正在启动",
  ready: "运行中",
  outdated: "旧版本",
  failed: "启动失败",
};

function sessionCount(activeSessions: number | undefined, activityKnown: boolean | undefined): string {
  return activityKnown === false ? "数量未知的会话" : `${activeSessions ?? 0} 个会话`;
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
        if (!cancelled) pushToast("error", `读取本机终端模式失败：${describeError(error)}`);
      });
    return () => { cancelled = true; };
  }, [enabled, pushToast]);

  if (!status || !enabled) return null;

  const chooseMode = async (value: LocalTerminalMode) => {
    if (busy || value === mode) return;
    setBusy(true);
    try {
      setMode(await localHelperApi.setMode(value));
      forgetDurableReadiness();
    } catch (error) {
      pushToast("error", `保存本机终端模式失败：${describeError(error)}`);
    } finally {
      setBusy(false);
    }
  };

  const restart = async () => {
    if (busy || !status.consentToken) return;
    const sessions = sessionCount(status.activeSessions, status.activityKnown);
    const ok = await ask(
      `重启本机持久组件会结束其中正在运行的 ${sessions}，未保存的工作会丢失。\n重启后新开的本机终端将使用当前版本的组件。仍要重启？`,
      { title: "重启本机持久组件", kind: "warning" },
    );
    if (!ok) return;
    setBusy(true);
    try {
      await localHelperApi.restart(status.consentToken);
      pushToast("info", "本机持久组件已重启");
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
    <div className="mb-3.5" data-local-helper-state={status.state}>
      <div className="nx-setting-label mb-1.5 flex items-center gap-2">
        本机终端
        <span className={badge}>持久组件{STATE_TEXT[status.state] ?? status.state}</span>
        {status.updateAvailable && <span className="nx-badge nx-badge-blue">可更新</span>}
      </div>
      <div className="nx-segment" role="group" aria-label="本机终端模式" aria-busy={busy}>
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
          ? "本机终端随 NexTerm 退出而结束；已在后台运行的本机终端仍可接管。"
          : "本机终端在独立的持久组件中运行，关闭标签或退出 NexTerm 后继续运行，可在「后台会话」接管或终止。"}
        只影响这台设备新开的终端，包括 AI 和定时任务打开的终端。
      </p>
      {status.reason && status.state !== "idle" && <p className="nx-hint mt-1.5 text-amber-300">{status.reason}</p>}
      {status.warning && <p className="nx-hint mt-1.5 text-amber-300">{status.warning}</p>}
      {restartable && (
        <button type="button" className="nx-btn nx-btn-danger nx-btn-sm mt-2" disabled={busy} onClick={() => void restart()}>
          重启持久组件（结束 {sessionCount(status.activeSessions, status.activityKnown)}）
        </button>
      )}
      {status.state === "failed" && (
        <button type="button" className="nx-btn nx-btn-outline nx-btn-sm mt-2" disabled={busy} onClick={() => void retry()}>
          重试启动持久组件
        </button>
      )}
    </div>
  );
}
