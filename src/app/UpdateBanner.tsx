import { useEffect } from "react";
import { DESKTOP } from "../ipc/env";
import { ProgressBar } from "../ui/DownloadProgress";
import { IconClose, IconDownload, IconLoader } from "../ui/icons";
import { useUi } from "./store";
import { useUpdate } from "./useUpdate";
import { t } from "../i18n";

export function UpdateBanner({ onOpenSettings }: { onOpenSettings: () => void }) {
  const { status, installedVersion, skippedVersion, install, skipVersion, restart, restarting, restartError } =
    useUpdate();
  const installError = install.error;
  useEffect(() => {
    if (installError) useUi.getState().pushToast("error", t("更新安装失败：{installError}", { installError }));
  }, [installError]);
  if (!DESKTOP) return null;
  if (install.active) {
    const percent =
      install.total > 0
        ? Math.min(100, Math.round((install.transferred / install.total) * 100))
        : 0;
    return (
      <div
        className="nx-banner flex min-h-[30px] shrink-0 items-center gap-2 border-b border-neutral-800/60 bg-neutral-900 px-3 text-xs text-neutral-300"
        role="region"
        aria-label={t("软件更新")}
      >
        <IconLoader size={13} className="shrink-0 animate-spin text-blue-300" />
        <span className="shrink-0">
          {install.phase === "install" ? t("正在安装") : t("正在下载")}{" "}
          <span className="font-medium text-neutral-100">v{status?.version || ""}</span>
        </span>
        <ProgressBar className="w-32 shrink-0" label={install.phase === "install" ? t("安装进度") : t("下载进度")}
          transferred={install.transferred} total={install.total} />
        <span className="shrink-0 text-neutral-400">
          {install.total > 0 ? `${percent}%` : t("请稍候")}
        </span>
      </div>
    );
  }
  const version = status?.available ? status.version : "";
  if (installedVersion) {
    return (
      <div
        className="nx-banner flex min-h-[30px] shrink-0 items-center gap-2 border-b border-neutral-800/60 bg-neutral-900 px-3 text-xs text-neutral-300"
        role="region"
        aria-label={t("软件更新")}
      >
        <IconDownload size={13} className="shrink-0 text-green-300" />
        <span className="min-w-0 truncate">
          {t("已安装")} <span className="font-medium text-neutral-100">v{installedVersion}</span>{t("，重启应用后生效")}
        </span>
        {restartError && <span className="shrink-0 text-red-300">{t("重启失败：{restartError}", { restartError })}</span>}
        <div className="nx-spacer" />
        <button
          type="button"
          className="nx-btn nx-btn-success nx-btn-sm shrink-0"
          disabled={restarting}
          onClick={() => void restart()}
        >
          {restarting ? t("正在重启…") : t("立即重启")}
        </button>
      </div>
    );
  }
  if (!version || skippedVersion === version) return null;
  return (
    <div
      className="nx-banner flex min-h-[30px] shrink-0 items-center gap-2 border-b border-neutral-800/60 bg-neutral-900 px-3 text-xs text-neutral-300"
      role="region"
      aria-label={t("软件更新")}
    >
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
        aria-label={t("跳过此版本")}
        title={t("跳过此版本")}
        onClick={() => skipVersion(version)}
      >
        <IconClose size={12} />
      </button>
    </div>
  );
}
