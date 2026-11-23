import { useEffect } from "react";
import { DESKTOP } from "../ipc/env";
import { ProgressBar } from "../ui/DownloadProgress";
import { IconClose, IconDownload, IconLoader } from "../ui/icons";
import { useUi } from "./store";
import { useUpdate } from "./useUpdate";
import { installPhaseLabel, RESTART_NOTE, restartErrorText } from "./updateText";
import { t } from "../i18n";

const BANNER_CLASS =
  "nx-banner flex min-h-[30px] shrink-0 items-center gap-2 border-b border-neutral-800/60 bg-neutral-900 px-3 text-xs text-neutral-300";

export function UpdateBanner({ onOpenSettings }: { onOpenSettings: () => void }) {
  const {
    status, readyVersion, skippedVersion, dismissedVersion, restartDeferred, install,
    dismissVersion, deferRestart, restart, restarting, restartError, restartFailure, readyApplied,
  } = useUpdate();
  const installError = install.error;
  useEffect(() => {
    if (installError) useUi.getState().pushToast("error", t("更新安装失败：{installError}", { installError }));
  }, [installError]);
  // A restart that discards the package also drops the restart offer, so the reason would otherwise vanish with it.
  useEffect(() => {
    if (restartError) useUi.getState().pushToast("error", restartErrorText(restartFailure, restartError));
  }, [restartError, restartFailure]);
  if (!DESKTOP) return null;
  if (install.active) {
    const percent =
      install.total > 0
        ? Math.min(100, Math.round((install.transferred / install.total) * 100))
        : 0;
    // Verifying and installing have no measurable progress: the bar is indeterminate.
    const measured = install.phase === "download" && install.total > 0;
    return (
      <div className={BANNER_CLASS} role="region" aria-label={t("软件更新")}>
        <IconLoader size={13} className="shrink-0 animate-spin text-blue-300" />
        <span className="shrink-0">
          {installPhaseLabel(install.phase)}{" "}
          <span className="font-medium text-neutral-100">v{status?.version || ""}</span>
        </span>
        <ProgressBar className="w-32 shrink-0" label={install.phase === "download" ? t("下载进度") : t("安装进度")}
          transferred={measured ? install.transferred : 0} total={measured ? install.total : 0} />
        <span className="min-w-0 truncate text-neutral-400">
          {measured ? `${percent}%` : t("请稍候")}
        </span>
      </div>
    );
  }
  const version = status?.available ? status.version : "";
  if (readyVersion) {
    if (restartDeferred) return null;
    return (
      <div className={BANNER_CLASS} role="region" aria-label={t("软件更新")}>
        <IconDownload size={13} className="shrink-0 text-green-300" />
        <span className="min-w-0 truncate" title={RESTART_NOTE}>
          {readyApplied
            ? t("v{readyVersion} 已安装，重启应用后生效", { readyVersion })
            : t("v{readyVersion} 下载完成，重启时安装", { readyVersion })}
        </span>
        {restartError && <span className="min-w-0 truncate text-red-300" title={restartError}>{restartErrorText(restartFailure, restartError)}</span>}
        <div className="nx-spacer" />
        <button
          type="button"
          className="nx-btn nx-btn-success nx-btn-sm shrink-0"
          disabled={restarting}
          title={RESTART_NOTE}
          onClick={() => void restart()}
        >
          {restarting ? (readyApplied ? t("正在重启…") : t("正在安装并重启…")) : readyApplied ? t("立即重启") : t("立即重启并安装")}
        </button>
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
          disabled={restarting}
          onClick={deferRestart}
        >
          {t("稍后")}
        </button>
      </div>
    );
  }
  if (!version || skippedVersion === version || dismissedVersion === version) return null;
  return (
    <div className={BANNER_CLASS} role="region" aria-label={t("软件更新")}>
      <IconDownload size={13} className="shrink-0 text-blue-300" />
      <span className="min-w-0 truncate">
        {t("发现新版本")} <span className="font-medium text-neutral-100">v{version}</span>
        {status?.prerelease ? t(" (RC 候选版本)") : ""}
        {status?.currentVersion ? t(" · 当前 v{currentVersion}", { currentVersion: status.currentVersion }) : ""}
      </span>
      <div className="nx-spacer" />
      <button
        type="button"
        className="nx-btn nx-btn-success nx-btn-sm shrink-0"
        onClick={onOpenSettings}
      >
        {t("查看更新")}
      </button>
      <button
        type="button"
        className="nx-icon-btn nx-icon-btn-sm shrink-0"
        aria-label={t("本次启动内不再提示")}
        title={t("本次启动内不再提示")}
        onClick={() => dismissVersion(version)}
      >
        <IconClose size={12} />
      </button>
    </div>
  );
}
