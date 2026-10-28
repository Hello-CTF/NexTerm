import { formatBytes } from "./format";

interface ProgressBarProps {
  label: string;
  transferred: number;
  total: number;
  className?: string;
}

export function ProgressBar({ label, transferred, total, className = "" }: ProgressBarProps) {
  const determinate = total > 0;
  const percent = determinate ? Math.min(100, Math.round((transferred / total) * 100)) : 0;
  return (
    <div className={`nx-progress ${className}`} role="progressbar" aria-label={label}
      aria-valuemin={0} aria-valuemax={100} aria-valuenow={determinate ? percent : undefined}>
      {determinate ? (
        <span className="nx-progress-bar" style={{ width: `${percent}%` }} />
      ) : (
        <span className="nx-progress-bar w-1/3 animate-pulse motion-reduce:animate-none" />
      )}
    </div>
  );
}

interface DownloadProgressProps extends ProgressBarProps {
  onCancel?: () => void;
  cancelDisabled?: boolean;
}

export function DownloadProgress({ label, transferred, total, onCancel, cancelDisabled, className = "" }: DownloadProgressProps) {
  const determinate = total > 0;
  const percent = determinate ? Math.min(100, Math.round((transferred / total) * 100)) : 0;
  return (
    <div className={`flex flex-col gap-1.5 ${className}`} role="status">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <span className="min-w-0 flex-1 truncate">{label}</span>
        <span className="shrink-0 text-neutral-400">
          {determinate ? `${percent}% · ${formatBytes(transferred)} / ${formatBytes(total)}` : "请稍候"}
        </span>
        {onCancel && <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={cancelDisabled} onClick={onCancel}>取消</button>}
      </div>
      <ProgressBar label={label} transferred={transferred} total={total} />
    </div>
  );
}
