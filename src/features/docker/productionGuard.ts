import type { DockerProductionConfirmation } from "../../ipc/commands";
import {
  getProductionChallenge,
  type DockerProductionChallenge,
  type ProductionChallengeTarget,
} from "../production/productionChallenge";

export type DockerConfirmationTarget = ProductionChallengeTarget;
export type DockerConfirmationChallenge = DockerProductionChallenge;

// The hand-written DockerProductionConfirmation in ipc/commands predates the
// typed-name contract; the extended shape is structurally assignable to it,
// so environmentNames flows through the legacy docker envelopes without
// editing the hand-written IPC module.
export type DockerEnvironmentConfirmation = DockerProductionConfirmation & { environmentNames?: string[] };

export function getDockerConfirmationChallenge(cause: unknown): DockerConfirmationChallenge | null {
  return getProductionChallenge(cause, "docker_production_confirmation");
}

export function dockerConfirmation(
  challenge: DockerConfirmationChallenge,
  environmentNames?: string[],
): DockerEnvironmentConfirmation {
  return {
    confirmed: true,
    targetSetHash: challenge.targetSetHash,
    commandHash: challenge.commandHash,
    ...(environmentNames && environmentNames.length > 0 ? { environmentNames } : {}),
  };
}
