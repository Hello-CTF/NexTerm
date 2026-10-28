import type { ExecBatchProductionChallenge } from "../production/productionChallenge";
import { ProductionConfirmationDialog } from "../execbatch/ProductionConfirmationDialog";
import { IconStop } from "../../ui/icons";

export function OperationFeedback({
  error,
  cancelError,
  running,
  runningId,
  canceling,
  onCancel,
}: {
  error: string | null;
  cancelError: string | null;
  running: boolean;
  runningId: string | null;
  canceling: boolean;
  onCancel: () => void;
}) {
  return (
    <>
      {error && <div role="alert" className="px-3 py-1.5 text-[11px] text-red-300">{error}</div>}
      {cancelError && <div role="alert" className="px-3 py-1.5 text-[11px] text-red-300">{cancelError}</div>}
      {running && (
        <div className="flex items-center gap-2 px-3 py-1.5 text-[11px] text-blue-300" role="status">
          <span>正在执行批次 {runningId}，逐目标结果会在返回后保留。</span>
          <button type="button" className="nx-btn nx-btn-danger nx-btn-xs" disabled={canceling} onClick={onCancel}>
            <IconStop size={11} />{canceling ? "正在取消…" : "取消"}
          </button>
        </div>
      )}
    </>
  );
}

export function OperationProductionDialog({
  pending,
  onConfirm,
  onCancel,
}: {
  pending: {
    challenge: ExecBatchProductionChallenge;
    summary: string;
  } | null;
  onConfirm: (environmentNames?: string[]) => void;
  onCancel: () => void;
}) {
  if (!pending) return null;
  return (
    <ProductionConfirmationDialog
      key={`${pending.challenge.targetSetHash}:${pending.challenge.commandHash}`}
      challenge={pending.challenge}
      contentHash={pending.challenge.commandHash}
      command={pending.summary}
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  );
}
