import { type SessionRuntimeInfo } from "../store";
import { IconLock, IconNetwork, IconServer } from "../../ui/icons";
import { type SyncStatusSummary, type VaultStatusSummary } from "./useStatusBarState";

export interface StatusBarProps {
  sessions: SessionRuntimeInfo[];
  vaultStatus: VaultStatusSummary;
  sync: SyncStatusSummary;
  onOpenVaultProtection: () => void;
  onOpenSyncSettings: () => void;
}

export function StatusBar({ sessions, vaultStatus, sync, onOpenVaultProtection, onOpenSyncSettings }: StatusBarProps) {
  const connectedList = sessions.filter((s) => s.status === "connected");
  const connectedSessions = connectedList.length;
  const idleSessions = connectedList.filter(
    (s) => s.kind !== "remote" && (s.tabs?.length ?? 0) === 0,
  ).length;
  return (
    <footer className="nx-statusbar flex h-[25px] shrink-0 items-center gap-3 border-t border-neutral-800/60 bg-neutral-950 px-3 text-[11px] text-neutral-500">
      <div className="nx-statusbar-info">
        <span className="flex items-center gap-1.5">
          <IconServer size={11} />
          <strong className="font-medium text-neutral-300">{sessions.length}</strong> 个会话
          <span className="text-neutral-700">·</span>
          <span
            className={
              connectedSessions > 0
                ? "flex items-center gap-1.5 text-green-300"
                : "flex items-center gap-1.5"
            }
          >
            <span className="nx-dot" />
            <strong className="font-medium">{connectedSessions}</strong> 已连接
            {idleSessions > 0 ? (
              <span className="text-neutral-500">（{idleSessions} 空闲）</span>
            ) : null}
          </span>
        </span>
        <span className="text-neutral-700">|</span>
        {vaultStatus.uninitialized ? (
          <button
            type="button"
            className="nx-statusbar-truncate flex items-center gap-1.5 text-amber-300 hover:text-[var(--nx-fg-warning)]"
            title="在「设置 → 凭据保护」中开启密码保护"
            onClick={onOpenVaultProtection}
          >
            <IconLock size={11} />
            凭据库未初始化 · 去开启保护
          </button>
        ) : (
          <span className="nx-statusbar-truncate flex items-center gap-1.5" title={vaultStatus.text}>
            <IconLock size={11} />
            {vaultStatus.text}
          </span>
        )}
      </div>
      <div className="nx-statusbar-side">
        <span className={`nx-statusbar-truncate flex items-center gap-1.5 ${sync.className}`} title={sync.title}>
          <IconNetwork size={11} />
          {sync.text}
        </span>
        {sync.showWarnings && (
          <button
            type="button"
            className="nx-badge nx-badge-amber shrink-0"
            title={sync.warnings.join("\n")}
            onClick={onOpenSyncSettings}
          >
            同步警告 {sync.warnings.length}
          </button>
        )}
      </div>
    </footer>
  );
}
