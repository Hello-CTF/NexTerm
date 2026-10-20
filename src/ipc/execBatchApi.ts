import { call } from "./commands";
import type {
  ExecBatch,
  ExecBatchCancelResult,
  ExecBatchHistory,
  ExecBatchHistoryPage,
  ExecBatchHistoryQuery,
  ExecBatchRequest,
  ExecBatchResult,
  ProductionConfirmation,
  ExecBatchHealth,
  ExecBatchHealthRequest,
  ExecBatchHealthResult,
  ExecBatchHealthTarget,
} from "./generated/types";

export type {
  ExecBatch,
  ExecBatchCancelResult,
  ExecBatchHistory,
  ExecBatchHistoryPage,
  ExecBatchHistoryQuery,
  ExecBatchRequest,
  ExecBatchResult,
  ProductionConfirmation,
  ExecBatchHealth,
  ExecBatchHealthRequest,
  ExecBatchHealthResult,
  ExecBatchHealthTarget,
};
export type { ExecBatchHistoryItem, ExecBatchResultRow, ExecBatchRow } from "./generated/types";
export type ExecBatchStatus = ExecBatchResult["status"];
export type ExecBatchHistoryScope = "self" | "ownerless";

export interface ExecBatchConfirmationTarget {
  sessionId: string;
  sessionName: string;
  assetId?: string;
  hostname?: string;
  environment: string;
}

export interface ExecBatchConfirmationChallenge {
  kind: "exec_batch_production_confirmation";
  batchId: string;
  targetSetHash: string;
  commandHash: string;
  productionTargets: ExecBatchConfirmationTarget[];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function isHash(value: unknown): value is string {
  return typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
}

export function getExecBatchConfirmationChallenge(cause: unknown): ExecBatchConfirmationChallenge | null {
  if (!isRecord(cause) || cause.code !== "needs_confirm" || !isRecord(cause.detail)) return null;
  const detail = cause.detail;
  if (
    detail.kind !== "exec_batch_production_confirmation" ||
    typeof detail.batchId !== "string" ||
    !isHash(detail.targetSetHash) ||
    !isHash(detail.commandHash) ||
    !Array.isArray(detail.productionTargets) ||
    detail.productionTargets.length === 0
  ) {
    return null;
  }
  const productionTargets: ExecBatchConfirmationTarget[] = [];
  for (const value of detail.productionTargets) {
    if (
      !isRecord(value) ||
      typeof value.sessionId !== "string" ||
      typeof value.sessionName !== "string" ||
      typeof value.environment !== "string" ||
      (value.hostname !== undefined && typeof value.hostname !== "string") ||
      (value.assetId !== undefined && typeof value.assetId !== "string")
    ) {
      return null;
    }
    productionTargets.push({
      sessionId: value.sessionId,
      sessionName: value.sessionName,
      environment: value.environment,
      ...(typeof value.hostname === "string" ? { hostname: value.hostname } : {}),
      ...(typeof value.assetId === "string" ? { assetId: value.assetId } : {}),
    });
  }
  return {
    kind: "exec_batch_production_confirmation",
    batchId: detail.batchId,
    targetSetHash: detail.targetSetHash,
    commandHash: detail.commandHash,
    productionTargets,
  };
}

export function createExecBatchId(): string {
  return globalThis.crypto.randomUUID();
}

export const execBatchApi = {
  execute: (request: ExecBatchRequest) =>
    call("exec_batch_execute", {
      batchId: request.batchId,
      sessionIds: request.sessionIds,
      command: request.command,
      timeoutMs: request.timeoutMs,
      maxConcurrent: request.maxConcurrent,
      ...(request.productionConfirmation !== undefined
        ? { productionConfirmation: request.productionConfirmation }
        : {}),
    }),
  cancel: (batchId: string) => call("exec_batch_cancel", { batchId }),
  healthProbe: (request: ExecBatchHealthRequest) => call("exec_batch_health_probe", request),
  healthCancel: (batchId: string) => call("exec_batch_health_cancel", { batchId }),
  historyList: (query: ExecBatchHistoryQuery = {}) =>
    call("exec_batch_history_list", { limit: query.limit, offset: query.offset }),
  historyGet: (batchId: string, scope: ExecBatchHistoryScope = "self") =>
    call("exec_batch_history_get", { batchId, scope }),
};
