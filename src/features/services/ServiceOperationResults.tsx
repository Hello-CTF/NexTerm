import type { ServiceOperationBatch } from "../../ipc/servicesApi";
import { EXEC_STATUS_LABELS, serviceResultMessage } from "./model";

export function ServiceOperationResults({ batch, title }: { batch: ServiceOperationBatch; title: string }) {
  return (
    <section className="border-t border-neutral-800/70 px-3 py-3" aria-label={title}>
      <div className="mb-2 flex flex-wrap items-center gap-2 text-[11px]">
        <h3 className="font-medium text-neutral-200">{title}</h3>
        <span className="nx-hint">批次 {batch.batchId}</span>
      </div>
      <div className="overflow-x-auto">
        <table className="nx-table min-w-[680px]">
          <thead><tr><th>主机</th><th>状态</th><th>退出码</th><th>输出 / 错误</th></tr></thead>
          <tbody>
            {batch.hosts.map((host) => (
              <tr key={host.result.sessionId} data-session-id={host.result.sessionId}>
                <td className="text-neutral-200">{host.result.sessionName || host.result.sessionId}</td>
                <td className={host.result.status === "success" ? "text-green-300" : "text-red-300"}>
                  {EXEC_STATUS_LABELS[host.result.status] ?? host.result.status}
                  {host.result.truncated && <span className="ml-1 text-amber-300">已截断</span>}
                </td>
                <td className="nx-mono">{host.result.exitCode ?? "—"}</td>
                <td className="whitespace-pre-wrap break-all text-neutral-400">
                  {host.result.status === "success" && !host.result.stdout && !host.result.stderr ? "无输出" : serviceResultMessage(host)}
                  {(host.result.stdout || host.result.stderr) && (
                    <details className="mt-1">
                      <summary className="cursor-pointer text-neutral-500">查看完整输出</summary>
                      {host.result.stdout && <pre className="nx-pre mt-1 max-h-56 overflow-auto whitespace-pre-wrap break-all">{host.result.stdout}</pre>}
                      {host.result.stderr && <pre className="nx-pre mt-1 max-h-56 overflow-auto whitespace-pre-wrap break-all text-red-300">{host.result.stderr}</pre>}
                    </details>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
