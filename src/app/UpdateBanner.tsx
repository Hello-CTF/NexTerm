import { DESKTOP } from "../ipc/env";
import { IconClose, IconDownload } from "../ui/icons";
import { useUpdate } from "./useUpdate";

export function UpdateBanner({ onOpenSettings }: { onOpenSettings: () => void }) {
  const { status, installedVersion, dismissedVersion, dismissBanner } = useUpdate();
  if (!DESKTOP) return null;
  const version = status?.available ? status.version : "";
  if (!version || installedVersion || dismissedVersion === version) return null;
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
        aria-label="忽略此版本"
        title="忽略此版本"
        onClick={dismissBanner}
      >
        <IconClose size={12} />
      </button>
    </div>
  );
}
