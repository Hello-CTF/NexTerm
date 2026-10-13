import { useEffect } from "react";
import { DESKTOP } from "../ipc/env";
import { IconClose, IconDownload, IconLoader } from "../ui/icons";
import { useUi } from "./store";
import { useUpdate } from "./useUpdate";

export function UpdateBanner({ onOpenSettings }: { onOpenSettings: () => void }) {
  const { status, installedVersion, skippedVersion, install, skipVersion, restart, restarting, restartError } =
    useUpdate();
  const installError = install.error;
  useEffect(() => {
    if (installError) useUi.getState().pushToast("error", `更新安装失败：${installError}`);
  }, [installError]);
  if (!DESKTOP) return null;
  if (install.active) {
    const percent =
      install.total > 0
        ? Math.min(100, Math.round((install.transferred / install.total) * 100))
        : 0;
    return (
      <div
        className="flex h-[30px] shrink-0 items-center gap-2 border-b border-neutral-800/60 bg-neutral-900 px-3 text-xs text-neutral-300"
        role="region"
        aria-label="软件更新"
      >
        <IconLoader size={13} className="shrink-0 animate-spin text-blue-300" />
        <span className="shrink-0">
          {install.phase === "install" ? "正在安装" : "正在下载"}{" "}
          <span className="font-medium text-neutral-100">v{status?.version || ""}</span>
        </span>
        <div
          className="nx-progress w-32 shrink-0"
          role="progressbar"
          aria-label={install.phase === "install" ? "安装进度" : "下载进度"}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={install.total > 0 ? percent : undefined}
        >
          {install.total > 0 ? (
            <span className="nx-progress-bar" style={{ width: `${percent}%` }} />
          ) : (
            <span className="nx-progress-bar w-1/3 animate-pulse motion-reduce:animate-none" />
          )}
        </div>
        <span className="shrink-0 text-neutral-400">
          {install.total > 0 ? `${percent}%` : "进度未知"}
        </span>
      </div>
    );
  }
  const version = status?.available ? status.version : "";
  if (installedVersion) {
    return (
      <div
        className="flex h-[30px] shrink-0 items-center gap-2 border-b border-neutral-800/60 bg-neutral-900 px-3 text-xs text-neutral-300"
        role="region"
        aria-label="软件更新"
      >
        <IconDownload size={13} className="shrink-0 text-green-300" />
        <span className="min-w-0 truncate">
          已安装 <span className="font-medium text-neutral-100">v{installedVersion}</span>，重启应用后生效
        </span>
        {restartError && <span className="shrink-0 text-red-300">重启失败：{restartError}</span>}
        <div className="nx-spacer" />
        <button
          type="button"
          className="nx-btn nx-btn-primary nx-btn-sm shrink-0"
          disabled={restarting}
          onClick={() => void restart()}
        >
          {restarting ? "正在重启…" : "立即重启"}
        </button>
      </div>
    );
  }
  if (!version || skippedVersion === version) return null;
  return (
    <div
      className="flex h-[30px] shrink-0 items-center gap-2 border-b border-neutral-800/60 bg-neutral-900 px-3 text-xs text-neutral-300"
      role="region"
      aria-label="软件更新"
    >
      <IconDownload size={13} className="shrink-0 text-blue-300" />
      <span className="min-w-0 truncate">
        发现新版本 <span className="font-medium text-neutral-100">v{version}</span>
        {status?.currentVersion ? ` · 当前 v${status.currentVersion}` : ""}
      </span>
      <div className="nx-spacer" />
      <button
        type="button"
        className="nx-btn nx-btn-outline nx-btn-sm shrink-0"
        onClick={onOpenSettings}
      >
        查看更新
      </button>
      <button
        type="button"
        className="nx-icon-btn nx-icon-btn-sm shrink-0"
        aria-label="跳过此版本"
        title="跳过此版本"
        onClick={() => skipVersion(version)}
      >
        <IconClose size={12} />
      </button>
    </div>
  );
}
