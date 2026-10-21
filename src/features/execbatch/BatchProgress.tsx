import { useEffect, useState } from "react";
import { call, toAppError } from "../../ipc/commands";
import type { ProductionConfirmation } from "../../ipc/generated/types";
import { describeError } from "../../ui/errorText";

// Feature-local contracts for the batch progress view. The live API maps
// them onto the typed exec_batch_progress / exec_batch_cancel /
// exec_batch_rerun commands; tests inject a fake.
export interface BatchOutputSnapshot {
  head: string;
  tail: string;
  totalBytes: number;
  droppedBytes: number;
  truncated: boolean;
}

export interface BatchProgressTarget {
  sessionId: string;
  position: number;
  phase: string;
  status?: string;
  exitCode?: number | null;
  signal?: string;
  error?: string;
  output: {
    stdout: BatchOutputSnapshot;
    stderr: BatchOutputSnapshot;
  };
  startedAt?: number;
  finishedAt?: number;
  durationMs?: number;
}

export interface BatchProgressSnapshot {
  batch: {
    batchId: string;
    phase: string;
    total: number;
    pending: number;
    running: number;
    succeeded: number;
    failed: number;
    canceled: number;
    finishedAt?: number;
    droppedEvents?: number;
  };
  targets: BatchProgressTarget[];
}

export interface BatchRerunResult {
  sessionId: string;
  status: string;
}

export interface BatchRerunRequest {
  batchId: string;
  command: string;
  timeoutMs?: number;
  maxConcurrent?: number;
  results: BatchRerunResult[];
  productionConfirmation?: ProductionConfirmation | null;
}

export interface BatchProgressApi {
  progress(batchId: string): Promise<BatchProgressSnapshot>;
  cancel(batchId: string): Promise<void>;
  rerun(request: BatchRerunRequest): Promise<{ batchId?: string }>;
}

export const liveBatchProgressApi: BatchProgressApi = {
  progress: (batchId) => call("exec_batch_progress", { batchId }),
  cancel: (batchId) => call("exec_batch_cancel", { batchId }).then(() => undefined),
  rerun: (request) => call("exec_batch_rerun", request),
};

export function BatchProgressPanel({
  api,
  batchId,
  original,
  onConfirmationRequired,
  onRerunStart,
  pollIntervalMs = 1000,
  newBatchId = () => globalThis.crypto.randomUUID(),
}: {
  api: BatchProgressApi;
  batchId: string;
  original: { command: string; timeoutMs?: number; maxConcurrent?: number };
  onConfirmationRequired?: (detail: unknown) => void;
  onRerunStart?: (batchId: string) => void;
  pollIntervalMs?: number;
  newBatchId?: () => string;
}) {
  const [snapshot, setSnapshot] = useState<BatchProgressSnapshot | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    // Only the latest poll response commits: a slow response from a previous
    // batchId or API must never overwrite the current snapshot.
    let stopped = false;
    let generation = 0;
    const poll = async () => {
      if (stopped) return;
      const current = ++generation;
      try {
        const next = await api.progress(batchId);
        if (stopped || current !== generation) return;
        setSnapshot(next);
        setError(null);
        if (next.batch.phase === "finished") {
          stopped = true;
        }
      } catch (cause) {
        if (stopped || current !== generation) return;
        setError(describeError(cause));
      }
    };
    setSnapshot(null);
    setError(null);
    void poll();
    const timer = setInterval(() => void poll(), pollIntervalMs);
    return () => {
      stopped = true;
      generation++;
      clearInterval(timer);
    };
  }, [api, batchId, pollIntervalMs]);

  const cancel = () => {
    if (busy) return;
    setBusy(true);
    setError(null);
    void api
      .cancel(batchId)
      .catch((cause: unknown) => setError(describeError(cause)))
      .finally(() => setBusy(false));
  };

  const rerun = () => {
    if (snapshot === null || busy) return;
    const request: BatchRerunRequest = {
      batchId: newBatchId(),
      command: original.command,
      timeoutMs: original.timeoutMs,
      maxConcurrent: original.maxConcurrent,
      results: snapshot.targets.map((target) => ({ sessionId: target.sessionId, status: target.status ?? "" })),
    };
    setBusy(true);
    setError(null);
    void api
      .rerun(request)
      .then((started) => {
        onRerunStart?.(started.batchId ?? request.batchId);
      })
      .catch((cause: unknown) => {
        const appError = toAppError(cause);
        if (appError.code === "needs_confirm") {
          onConfirmationRequired?.(appError.detail);
        }
        setError(appError.message);
      })
      .finally(() => setBusy(false));
  };

  return (
    <fieldset className="min-w-0">
      <legend className="mb-1.5 text-[11px] text-neutral-500">批次进度</legend>
      {error !== null && (
        <div className="mb-2 text-[11px] text-amber-300" role="alert">
          {error}
        </div>
      )}
      {snapshot !== null && (
        <div className="flex flex-col gap-2">
          <div className="flex flex-wrap items-center gap-2 text-[11px] text-neutral-400">
            <span>
              成功 {snapshot.batch.succeeded} · 失败 {snapshot.batch.failed} · 取消 {snapshot.batch.canceled} / 共{" "}
              {snapshot.batch.total}
            </span>
            {snapshot.batch.phase === "running" && (
              <button type="button" className="nx-link" disabled={busy} onClick={cancel}>
                取消执行
              </button>
            )}
            {snapshot.batch.phase === "finished" && snapshot.batch.failed > 0 && (
              <>
                <button type="button" className="nx-link" disabled={busy} onClick={rerun}>
                  重跑失败目标
                </button>
                <span className="nx-hint">{snapshot.batch.failed} 个失败目标将按原顺序重跑，生产目标需要重新确认</span>
              </>
            )}
          </div>
          <ul className="grid max-h-64 grid-cols-1 gap-1 overflow-y-auto rounded border border-neutral-800/70 p-2">
            {snapshot.targets.map((target) => (
              <li key={target.sessionId} className="min-w-0 px-1.5 py-1 text-[11px]">
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  <span className="truncate font-medium text-neutral-200" title={target.sessionId}>
                    {target.sessionId}
                  </span>
                  <span className="nx-badge">{target.status ?? target.phase}</span>
                  {target.exitCode != null && <span className="text-neutral-400">exit {target.exitCode}</span>}
                  {target.signal !== undefined && target.signal !== "" && (
                    <span className="text-neutral-400">{target.signal}</span>
                  )}
                  {target.durationMs !== undefined && <span className="text-neutral-500">{target.durationMs}ms</span>}
                </div>
                {target.error !== undefined && target.error !== "" && (
                  <div className="text-amber-300">{target.error}</div>
                )}
                <OutputLine label="stdout" output={target.output.stdout} />
                <OutputLine label="stderr" output={target.output.stderr} />
              </li>
            ))}
          </ul>
        </div>
      )}
    </fieldset>
  );
}

function OutputLine({ label, output }: { label: string; output: BatchOutputSnapshot }) {
  if (output.totalBytes === 0) return null;
  return (
    <div className="min-w-0 font-mono text-[10px] text-neutral-400">
      <span className="text-neutral-500">{label}:</span>{" "}
      <span className="whitespace-pre-wrap break-all">{output.head}</span>
      {output.truncated && <span className="text-neutral-500">… 中间省略 {output.droppedBytes} 字节 …</span>}
      <span className="whitespace-pre-wrap break-all">{output.tail}</span>
    </div>
  );
}
