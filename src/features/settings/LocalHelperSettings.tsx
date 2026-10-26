import { useState } from "react";
import { localHelperApi } from "../../ipc/commands";
import { setTerminalLocalMode, useTerminalPrefs, type TerminalLocalMode } from "../../app/preferences";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { refreshLocalHelperStatus, useLocalHelperStatus } from "../terminal/localHelperStatus";

const LOCAL_MODE_OPTIONS: { value: TerminalLocalMode; label: string }[] = [
  { value: "durable", label: "持久" },
  { value: "regular", label: "普通" },
];

const STATE_TEXT: Record<string, string> = {
  starting: "正在启动",
  ready: "运行中",
  outdated: "旧版本",
  failed: "未运行",
};

// LocalHelperSettings shows the local helper that keeps local terminals running after the app exits,
// and asks for consent before an outdated helper, and every session in it, is restarted.
export function LocalHelperSettings() {
  const status = useLocalHelperStatus();
  const prefs = useTerminalPrefs();
  const pushToast = useUi((s) => s.pushToast);
  const [busy, setBusy] = useState(false);
  if (!status || status.state === "disabled") return null;

  const restart = async () => {
    if (busy || !status.consentToken) return;
    const count = status.activeSessions ?? 0;
    const ok = await ask(
      `重启本机持久组件会结束其中正在运行的 ${count} 个会话，未保存的工作会丢失。\n重启后新开的本机终端将使用持久模式。仍要重启？`,
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

  const badge = status.state === "ready" ? "nx-badge nx-badge-green" : status.state === "starting" ? "nx-badge nx-badge-blue" : "nx-badge nx-badge-amber";
  return (
    <div className="mb-3.5" data-local-helper-state={status.state}>
      <div className="nx-setting-label mb-1.5 flex items-center gap-2">
        本机终端
        <span className={badge}>持久组件{STATE_TEXT[status.state] ?? status.state}</span>
      </div>
      <div className="nx-segment" role="group" aria-label="本机终端模式">
        {LOCAL_MODE_OPTIONS.map((option) => (
          <button
            key={option.value}
            type="button"
            className={`nx-segment-item ${prefs.localMode === option.value ? "is-active" : ""}`}
            aria-pressed={prefs.localMode === option.value}
            onClick={() => setTerminalLocalMode(option.value)}
          >
            {option.label}
          </button>
        ))}
      </div>
      <p className="nx-hint mt-1.5">
        {prefs.localMode === "durable"
          ? "本机终端在独立的持久组件中运行，关闭标签或退出 NexTerm 后继续运行，可在「后台会话」接管或终止。"
          : "本机终端随 NexTerm 退出而结束。"}
        只影响这台设备新开的终端。
      </p>
      {status.reason && status.state !== "ready" && <p className="nx-hint mt-1.5 text-amber-300">{status.reason}</p>}
      {status.warning && <p className="nx-hint mt-1.5 text-amber-300">{status.warning}</p>}
      {status.state === "outdated" && status.consentToken && (
        <button type="button" className="nx-btn nx-btn-danger nx-btn-sm mt-2" disabled={busy} onClick={() => void restart()}>
          重启持久组件（结束 {status.activeSessions ?? 0} 个会话）
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
