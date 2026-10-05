export interface DaemonProps {
  durable: boolean;
  sessionKind?: string;
}

export function Daemon({ durable, sessionKind }: DaemonProps) {
  if (sessionKind === "winrm" || sessionKind === "docker") return null;
  if (durable) {
    return (
      <span
        className="nx-badge nx-badge-green"
        title="进程由 NexTerm 守护进程托管：断开连接或重启应用后，可在「后台会话」接管"
      >
        守护进程
      </span>
    );
  }
  return (
    <span className="nx-badge" title="直接连接：进程随会话断开而退出，不会被守护进程托管">
      直接连接
    </span>
  );
}
