import type { DockerProductionConfirmation } from "../../ipc/commands";

export interface DockerConfirmationTarget {
  sessionId: string;
  sessionName: string;
  assetId?: string;
  hostname?: string;
  environment: string;
}

export interface DockerConfirmationChallenge {
  kind: "docker_production_confirmation";
  targetSetHash: string;
  commandHash: string;
  productionTargets: DockerConfirmationTarget[];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function isHash(value: unknown): value is string {
  return typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
}

export function getDockerConfirmationChallenge(cause: unknown): DockerConfirmationChallenge | null {
  if (!isRecord(cause) || cause.code !== "needs_confirm" || !isRecord(cause.detail)) return null;
  const detail = cause.detail;
  if (
    detail.kind !== "docker_production_confirmation" ||
    !isHash(detail.targetSetHash) ||
    !isHash(detail.commandHash) ||
    !Array.isArray(detail.productionTargets) ||
    detail.productionTargets.length !== 1
  ) {
    return null;
  }
  const target = detail.productionTargets[0];
  if (
    !isRecord(target) ||
    typeof target.sessionId !== "string" ||
    typeof target.sessionName !== "string" ||
    typeof target.environment !== "string" ||
    (target.hostname !== undefined && typeof target.hostname !== "string") ||
    (target.assetId !== undefined && typeof target.assetId !== "string")
  ) {
    return null;
  }
  return {
    kind: "docker_production_confirmation",
    targetSetHash: detail.targetSetHash,
    commandHash: detail.commandHash,
    productionTargets: [{
      sessionId: target.sessionId,
      sessionName: target.sessionName,
      environment: target.environment,
      ...(typeof target.hostname === "string" ? { hostname: target.hostname } : {}),
      ...(typeof target.assetId === "string" ? { assetId: target.assetId } : {}),
    }],
  };
}

export function dockerConfirmation(challenge: DockerConfirmationChallenge): DockerProductionConfirmation {
  return {
    confirmed: true,
    targetSetHash: challenge.targetSetHash,
    commandHash: challenge.commandHash,
  };
}
