// Parsers for the R-01 production confirmation challenges raised by the
// exec-batch and broadcast surfaces. The M15 backend detail always carries
// requiredEnvironmentNames (the sorted distinct environment names a
// confirmation must type back); challenges without the field are legacy
// contracts that keep the acknowledge-only flow. Parsing fails closed on any
// unexpected shape, mirroring the stricter ipc-side validators.

import type { EnvironmentConfirmationChallenge } from "./environmentConfirmation";

export type ProductionChallengeKind = "exec_batch_production_confirmation" | "broadcast_production_confirmation";

export interface ProductionChallengeTarget {
  sessionId: string;
  sessionName: string;
  tabId?: string;
  assetId?: string;
  hostname?: string;
  environment: string;
}

interface ProductionChallengeBase {
  targetSetHash: string;
  requiredEnvironmentNames?: string[];
  productionTargets: ProductionChallengeTarget[];
}

export interface ExecBatchProductionChallenge extends ProductionChallengeBase {
  kind: "exec_batch_production_confirmation";
  batchId: string;
  commandHash: string;
}

export interface BroadcastProductionChallenge extends ProductionChallengeBase {
  kind: "broadcast_production_confirmation";
  inputHash: string;
}

export type ProductionChallenge = ExecBatchProductionChallenge | BroadcastProductionChallenge;

// BroadcastProductionConfirmation in ipc/commands predates the typed-name
// contract; the extended shape is structurally assignable to it, so the extra
// field flows through the legacy terminal_write/broadcast_prepare envelope
// without editing the hand-written IPC module.
export interface BroadcastEnvironmentConfirmation {
  confirmed: boolean;
  targetSetHash: string;
  inputHash: string;
  environmentNames?: string[];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function isHash(value: unknown): value is string {
  return typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
}

function parseTargets(value: unknown, requireTabId: boolean): ProductionChallengeTarget[] | null {
  if (!Array.isArray(value) || value.length === 0) return null;
  const targets: ProductionChallengeTarget[] = [];
  for (const entry of value) {
    if (
      !isRecord(entry) ||
      typeof entry.sessionId !== "string" ||
      typeof entry.sessionName !== "string" ||
      typeof entry.environment !== "string" ||
      (entry.tabId !== undefined && typeof entry.tabId !== "string") ||
      (entry.assetId !== undefined && typeof entry.assetId !== "string") ||
      (entry.hostname !== undefined && typeof entry.hostname !== "string")
    ) {
      return null;
    }
    if (requireTabId && typeof entry.tabId !== "string") return null;
    targets.push({
      sessionId: entry.sessionId,
      sessionName: entry.sessionName,
      environment: entry.environment,
      ...(typeof entry.tabId === "string" ? { tabId: entry.tabId } : {}),
      ...(typeof entry.assetId === "string" ? { assetId: entry.assetId } : {}),
      ...(typeof entry.hostname === "string" ? { hostname: entry.hostname } : {}),
    });
  }
  return targets;
}

function parseRequiredEnvironmentNames(value: unknown): string[] | undefined | null {
  if (value === undefined) return undefined;
  if (!Array.isArray(value) || value.length === 0) return null;
  for (const name of value) {
    if (typeof name !== "string" || name.trim() === "") return null;
  }
  return value as string[];
}

export function getProductionChallenge(
  cause: unknown,
  kind: "exec_batch_production_confirmation",
): ExecBatchProductionChallenge | null;
export function getProductionChallenge(
  cause: unknown,
  kind: "broadcast_production_confirmation",
): BroadcastProductionChallenge | null;
export function getProductionChallenge(cause: unknown, kind: ProductionChallengeKind): ProductionChallenge | null {
  if (!isRecord(cause) || cause.code !== "needs_confirm" || !isRecord(cause.detail)) return null;
  const detail = cause.detail;
  if (detail.kind !== kind || !isHash(detail.targetSetHash) || !Array.isArray(detail.productionTargets)) {
    return null;
  }
  const requiredEnvironmentNames = parseRequiredEnvironmentNames(detail.requiredEnvironmentNames);
  if (requiredEnvironmentNames === null) return null;
  if (kind === "exec_batch_production_confirmation") {
    if (typeof detail.batchId !== "string" || !isHash(detail.commandHash)) return null;
    const productionTargets = parseTargets(detail.productionTargets, false);
    if (!productionTargets) return null;
    return {
      kind,
      batchId: detail.batchId,
      targetSetHash: detail.targetSetHash,
      commandHash: detail.commandHash,
      ...(requiredEnvironmentNames ? { requiredEnvironmentNames } : {}),
      productionTargets,
    };
  }
  if (!isHash(detail.inputHash)) return null;
  const productionTargets = parseTargets(detail.productionTargets, true);
  if (!productionTargets) return null;
  return {
    kind,
    targetSetHash: detail.targetSetHash,
    inputHash: detail.inputHash,
    ...(requiredEnvironmentNames ? { requiredEnvironmentNames } : {}),
    productionTargets,
  };
}

export function toEnvironmentConfirmationChallenge(challenge: ProductionChallenge): EnvironmentConfirmationChallenge {
  return {
    targetSetHash: challenge.targetSetHash,
    requiredEnvironmentNames: challenge.requiredEnvironmentNames ?? [],
    productionTargets: challenge.productionTargets,
  };
}
