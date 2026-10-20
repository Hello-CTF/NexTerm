import { useEffect, useRef, useState } from "react";
import {
  execBatchApi,
  getExecBatchConfirmationChallenge,
  type ExecBatchConfirmationChallenge,
  type ProductionConfirmation,
} from "../../ipc/execBatchApi";
import { describeError } from "../../ui/errorText";

export interface OperationRequest {
  batchId: string;
  productionConfirmation?: ProductionConfirmation | null;
}

export function useExecOperation<Request extends OperationRequest, Result>(
  executor: (request: Request) => Promise<Result>,
) {
  const [result, setResult] = useState<Result | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [cancelError, setCancelError] = useState<string | null>(null);
  const [runningId, setRunningId] = useState<string | null>(null);
  const [canceling, setCanceling] = useState(false);
  const [production, setProduction] = useState<{
    request: Request;
    challenge: ExecBatchConfirmationChallenge;
    summary: string;
    onSuccess?: (result: Result) => void;
  } | null>(null);
  const activeRef = useRef<string | null>(null);

  useEffect(
    () => () => {
      const active = activeRef.current;
      activeRef.current = null;
      if (active) void execBatchApi.cancel(active).catch(() => undefined);
    },
    [],
  );

  const execute = async (request: Request, summary: string, onSuccess?: (result: Result) => void) => {
    if (activeRef.current) return;
    activeRef.current = request.batchId;
    setRunningId(request.batchId);
    setCanceling(false);
    setResult(null);
    setError(null);
    setCancelError(null);
    setProduction(null);
    try {
      const next = await executor(request);
      if (activeRef.current !== request.batchId) return;
      setResult(next);
      onSuccess?.(next);
    } catch (cause) {
      if (activeRef.current !== request.batchId) return;
      const challenge = getExecBatchConfirmationChallenge(cause);
      if (challenge?.batchId === request.batchId) {
        const unconfirmed = { ...request };
        delete unconfirmed.productionConfirmation;
        setProduction({ request: unconfirmed, challenge, summary, onSuccess });
      } else {
        setError(describeError(cause));
      }
    } finally {
      if (activeRef.current === request.batchId) {
        activeRef.current = null;
        setRunningId(null);
        setCanceling(false);
      }
    }
  };

  const confirmProduction = () => {
    const pending = production;
    if (!pending || activeRef.current) return;
    void execute(
      {
        ...pending.request,
        productionConfirmation: {
          confirmed: true,
          targetSetHash: pending.challenge.targetSetHash,
          commandHash: pending.challenge.commandHash,
        },
      },
      pending.summary,
      pending.onSuccess,
    );
  };

  const cancel = async () => {
    const active = activeRef.current;
    if (!active || canceling) return;
    setCanceling(true);
    setCancelError(null);
    try {
      await execBatchApi.cancel(active);
    } catch (cause) {
      if (activeRef.current === active) setCancelError(`取消失败：${describeError(cause)}`);
    } finally {
      if (activeRef.current === active) setCanceling(false);
    }
  };

  return {
    result,
    error,
    cancelError,
    running: runningId !== null,
    runningId,
    canceling,
    production,
    execute,
    confirmProduction,
    clearProduction: () => setProduction(null),
    cancel,
  };
}
