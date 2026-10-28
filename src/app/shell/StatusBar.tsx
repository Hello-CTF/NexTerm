import { useSessionSummary } from "../store";
import { IconLock, IconNetwork, IconServer } from "../../ui/icons";
import { type SyncStatusSummary, type VaultStatusSummary } from "./useStatusBarState";

export interface StatusBarProps {
  vaultStatus: VaultStatusSummary;
  sync: SyncStatusSummary;
  onOpenVaultProtection: () => void;
  onOpenSyncSettings: () => void;
  onRetryVaultStatus?: () => void;
}

const ITEM = "nx-statusbar-truncate flex items-center gap-1.5 text-amber-300 hover:text-[var(--nx-fg-warning)]";

// The status bar only shows what the user can act on: a healthy vault and a normal sync state render nothing.
export function StatusBar({ vaultStatus, sync, onOpenVaultProtection, onOpenSyncSettings, onRetryVaultStatus }: StatusBarProps) {
  const { total: sessionCount, connected: connectedSessions, idle: idleSessions } = useSessionSummary();
  return (
    <footer className="nx-statusbar flex h-[25px] shrink-0 items-center gap-3 border-t border-neutral-800/60 bg-neutral-950 px-3 text-[11px] text-neutral-500">
      <div className="nx-statusbar-info">
        <span className="flex items-center gap-1.5">
          <IconServer size={11} />
          <strong className="font-medium text-neutral-300">{sessionCount}</strong> 个会话
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
        {vaultStatus.attention === "uninitialized" ? (
          <button type="button" className={ITEM} title="在「设置 → 凭据保护」中开启密码保护" onClick={onOpenVaultProtection}>
            <IconLock size={11} />
            凭据库未初始化 · 去开启保护
          </button>
        ) : vaultStatus.attention === "locked" ? (
          <button type="button" className={ITEM} title="输入保护密码后才能使用已保存的凭据" onClick={onOpenVaultProtection}>
            <IconLock size={11} />
            凭据库已锁定 · 解锁
          </button>
        ) : vaultStatus.attention === "unknown" && onRetryVaultStatus ? (
          <button type="button" className={ITEM} title="未能读取凭据库状态，选择后重试" onClick={onRetryVaultStatus}>
            <IconLock size={11} />
            凭据库状态未知 · 重试
          </button>
        ) : null}
      </div>
      <div className="nx-statusbar-side">
        {sync.failed ? (
          <button
            type="button"
            className="nx-statusbar-truncate flex items-center gap-1.5 text-red-300 hover:text-red-200"
            title={sync.title}
            onClick={onOpenSyncSettings}
          >
            <IconNetwork size={11} />
            同步失败 · 查看
          </button>
        ) : null}
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
