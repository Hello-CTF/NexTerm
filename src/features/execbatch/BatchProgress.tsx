import { forwardRef, useEffect, useImperativeHandle, useLayoutEffect, useRef, useState } from "react";
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

export interface BatchFinalResult {
  batchId: string;
  command: string;
  startedAt: number;
  finishedAt: number;
  results: {
    sessionId: string;
    sessionName?: string;
    status: string;
    stdout?: string;
    stderr?: string;
    exitCode?: number | null;
    signal?: string;
    error?: string;
    durationMs?: number;
    truncated?: boolean;
  }[];
}

export interface BatchProgressApi {
  progress(batchId: string): Promise<BatchProgressSnapshot>;
  cancel(batchId: string): Promise<void>;
  rerun(request: BatchRerunRequest): Promise<BatchFinalResult>;
}

export const liveBatchProgressApi: BatchProgressApi = {
  progress: (batchId) => call("exec_batch_progress", { batchId }),
  cancel: (batchId) => call("exec_batch_cancel", { batchId }).then(() => undefined),
  rerun: (request) => call("exec_batch_rerun", request),
};

export interface BatchProgressHandle {
  rerun: (request: BatchRerunRequest) => void;
}

export const BatchProgressPanel = forwardRef<BatchProgressHandle, {
  api: BatchProgressApi;
  batchId: string;
  original: { command: string; timeoutMs?: number; maxConcurrent?: number };
  onConfirmationRequired?: (detail: unknown, request: BatchRerunRequest) => void;
  onRerunStart?: (batchId: string) => void;
  onFinished?: (batchId: string) => void;
  pollIntervalMs?: number;
  newBatchId?: () => string;
  rerunDisabled?: boolean;
  startupGraceMs?: number;
}>(function BatchProgressPanel({
  api,
  batchId,
  original,
  onConfirmationRequired,
  onRerunStart,
  onFinished,
  pollIntervalMs = 1000,
  newBatchId = () => globalThis.crypto.randomUUID(),
  rerunDisabled = false,
  startupGraceMs = 10000,
}, ref) {
  const [snapshot, setSnapshot] = useState<BatchProgressSnapshot | null>(null);
  const [finalBatch, setFinalBatch] = useState<{
    forApi: BatchProgressApi;
    batchId: string;
    batch: BatchFinalResult;
  } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [activeBatchId, setActiveBatchId] = useState(batchId);
  // pollGeneration restarts the polling effect on every rerun, even when the
  // batch ID is unchanged: a stopped effect must not survive a new attempt.
  const [pollGeneration, setPollGeneration] = useState(0);
  const onFinishedRef = useRef(onFinished);
  onFinishedRef.current = onFinished;
  // rerunPendingRef marks the startup window of a rerun: the new batch's
  // first polls may legitimately see not_found before the tracker is
  // published, which is a temporary absence, not a terminal one.
  const rerunPendingRef = useRef(false);
  const rerunGenerationRef = useRef(0);
  const apiRef = useRef(api);
  const activeBatchIdRef = useRef(activeBatchId);
  // The startup grace covers the ordinary execute window between the active
  // batch being admitted and its tracker being published: not_found is a
  // temporary absence until the grace expires or a snapshot was observed.
  const startupGraceUntilRef = useRef(0);
  const observedRef = useRef(false);
  // previousBatchIdRef detects an external prop transition during render:
  // the prop changing to a different ID is an external switch (hide the old
  // final from the first frame), while an unchanged prop means the current
  // view still belongs to the component's own rerun. Refs that render reads
  // are updated in layout effects (commit phase), never during render, so a
  // StrictMode double render evaluates the guard against the same committed
  // prop value twice.
  const previousBatchIdRef = useRef(batchId);
  useLayoutEffect(() => {
    previousBatchIdRef.current = batchId;
  }, [batchId]);
  useLayoutEffect(() => {
    activeBatchIdRef.current = activeBatchId;
  }, [activeBatchId]);
  useLayoutEffect(() => {
    if (apiRef.current !== api) {
      apiRef.current = api;
      rerunGenerationRef.current++;
      rerunPendingRef.current = false;
      setBusy(false);
    }
  }, [api]);

  useEffect(() => {
    if (batchId === activeBatchIdRef.current) {
      // The parent mirroring the self-switch is not an external switch:
      // keep the in-flight rerun and its continuations valid.
      return;
    }
    setActiveBatchId(batchId);
    setFinalBatch(null);
    // An external batch switch invalidates any in-flight rerun: its late
    // continuations must not commit into the new view.
    rerunGenerationRef.current++;
    rerunPendingRef.current = false;
    setBusy(false);
  }, [batchId]);

  useEffect(() => {
    // Only the latest poll response commits: a slow response from a previous
    // batchId or API must never overwrite the current snapshot. The effect
    // restarts on every pollGeneration bump (a new rerun), so a previously
    // stopped poll never survives a new attempt.
    let stopped = false;
    let generation = 0;
    startupGraceUntilRef.current = Date.now() + startupGraceMs;
    observedRef.current = false;
    const poll = async () => {
      if (stopped) return;
      const current = ++generation;
      try {
        const next = await api.progress(activeBatchId);
        if (stopped || current !== generation) return;
        if (!isProgressSnapshot(next)) {
          setError("进度响应格式错误");
          stopped = true;
          return;
        }
        observedRef.current = true;
        setSnapshot(next);
        setError(null);
        if (next.batch.phase === "finished") {
          stopped = true;
          onFinishedRef.current?.(activeBatchId);
        }
      } catch (cause) {
        if (stopped || current !== generation) return;
        const appError = toAppError(cause);
        setError(appError.message);
        if (appError.code === "not_found") {
          // Terminal absence only when the batch was already observed (its
          // snapshot expired), the startup grace expired without a snapshot,
          // or no rerun is pending: an admitted batch's tracker is published
          // shortly after admission, and a pending rerun's tracker follows
          // the same path. The boundary is strict so a zero grace stops at
          // the first response.
          const startupGrace = !observedRef.current && Date.now() < startupGraceUntilRef.current;
          if (!startupGrace && !rerunPendingRef.current) {
            stopped = true;
          }
        }
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
  }, [api, activeBatchId, pollIntervalMs, startupGraceMs, pollGeneration]);

  const cancel = () => {
    if (busy) return;
    const generation = ++rerunGenerationRef.current;
    const cancelApi = api;
    const cancelBatchId = activeBatchId;
    const isCurrent = () =>
      rerunGenerationRef.current === generation &&
      apiRef.current === cancelApi &&
      activeBatchIdRef.current === cancelBatchId;
    setBusy(true);
    setError(null);
    void api
      .cancel(activeBatchId)
      .catch((cause: unknown) => {
        if (isCurrent()) setError(describeError(cause));
      })
      .finally(() => {
        if (isCurrent()) setBusy(false);
      });
  };

  const rerunWith = (request: BatchRerunRequest) => {
    if (busy) return;
    const rerunGeneration = ++rerunGenerationRef.current;
    const rerunApi = api;
    const rerunBatchId = request.batchId;
    const isCurrentRerun = () =>
      rerunGenerationRef.current === rerunGeneration &&
      apiRef.current === rerunApi &&
      activeBatchIdRef.current === rerunBatchId;
    setBusy(true);
    setError(null);
    // The new batch identity is already known: notify before awaiting and
    // start polling it so the rerun's live progress is observable while the
    // rerun request is still pending.
    onRerunStart?.(request.batchId);
    rerunPendingRef.current = true;
    setActiveBatchId(request.batchId);
    // Keep the identity ref in sync at mutation time: the rerun's promise
    // continuations may run before the re-render that refreshes it.
    activeBatchIdRef.current = request.batchId;
    setFinalBatch(null);
    // Restart polling for the new attempt even when the batch ID is
    // unchanged: a previously stopped effect must not survive a new rerun.
    setPollGeneration((generation) => generation + 1);
    void api
      .rerun(request)
      .then((finalBatch) => {
        if (!isCurrentRerun()) return;
        setFinalBatch({ forApi: rerunApi, batchId: rerunBatchId, batch: finalBatch });
      })
      .catch((cause: unknown) => {
        if (!isCurrentRerun()) return;
        const appError = toAppError(cause);
        if (appError.code === "needs_confirm") {
          onConfirmationRequired?.(appError.detail, request);
        }
        setError(appError.message);
      })
      .finally(() => {
        if (!isCurrentRerun()) return;
        rerunPendingRef.current = false;
        setBusy(false);
      });
  };

  const rerun = () => {
    if (snapshot === null) return;
    rerunWith({
      batchId: newBatchId(),
      command: original.command,
      timeoutMs: original.timeoutMs,
      maxConcurrent: original.maxConcurrent,
      results: snapshot.targets.map((target) => ({ sessionId: target.sessionId, status: target.status ?? "" })),
    });
  };

  // The parent drives confirmed reruns through the same continuation so the
  // generation/API/batch guards, startup polling, and final-batch rendering
  // apply exactly as they do for the button path. The handle stays stable
  // and calls the latest rerunWith through a ref.
  const rerunWithRef = useRef(rerunWith);
  rerunWithRef.current = rerunWith;
  useImperativeHandle(ref, () => ({ rerun: (request) => rerunWithRef.current(request) }), []);

  // The final batch renders only for the API identity and batch that
  // produced it: an API or external batch switch hides it synchronously, so
  // the previous owner's output never mixes into the new view. An external
  // transition is a prop change to a different ID; an unchanged prop means
  // the view still belongs to the component's own rerun.
  const externallySwitched = batchId !== activeBatchId && batchId !== previousBatchIdRef.current;
  const visibleFinalBatch =
    finalBatch !== null && finalBatch.forApi === api && finalBatch.batchId === activeBatchId && !externallySwitched
      ? finalBatch.batch
      : null;

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
            {snapshot.batch.phase === "finished" && snapshot.batch.failed > 0 && !rerunDisabled && (
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
      {visibleFinalBatch !== null && (
        <div className="flex flex-col gap-2">
          <div className="text-[11px] text-neutral-400">批次 {visibleFinalBatch.batchId} 已结束</div>
          <ul className="grid max-h-64 grid-cols-1 gap-1 overflow-y-auto rounded border border-neutral-800/70 p-2">
            {visibleFinalBatch.results.map((result) => (
              <li key={result.sessionId} className="min-w-0 px-1.5 py-1 text-[11px]">
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  <span className="truncate font-medium text-neutral-200" title={result.sessionId}>
                    {result.sessionId}
                  </span>
                  <span className="nx-badge">{result.status}</span>
                  {result.exitCode != null && <span className="text-neutral-400">exit {result.exitCode}</span>}
                  {result.durationMs !== undefined && <span className="text-neutral-500">{result.durationMs}ms</span>}
                </div>
                {result.error !== undefined && result.error !== "" && (
                  <div className="text-amber-300">{result.error}</div>
                )}
                {result.stdout !== undefined && result.stdout !== "" && (
                  <div className="min-w-0 font-mono text-[10px] text-neutral-400">
                    <span className="text-neutral-500">stdout:</span>{" "}
                    <span className="whitespace-pre-wrap break-all">{result.stdout}</span>
                  </div>
                )}
                {result.stderr !== undefined && result.stderr !== "" && (
                  <div className="min-w-0 font-mono text-[10px] text-neutral-400">
                    <span className="text-neutral-500">stderr:</span>{" "}
                    <span className="whitespace-pre-wrap break-all">{result.stderr}</span>
                  </div>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
    </fieldset>
  );
});

function isProgressSnapshot(value: unknown): value is BatchProgressSnapshot {
  if (value === null || typeof value !== "object") return false;
  const candidate = value as { batch?: unknown };
  return candidate.batch !== null && typeof candidate.batch === "object";
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
