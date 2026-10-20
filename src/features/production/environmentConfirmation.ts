// View-model for the R-01 typed environment-name production confirmation.
//
// Cross-language contract with the backend (guard.EnvironmentNamesMatch):
// goTrimSpace trims exactly Go's unicode.IsSpace White_Space set, and
// goEqualFold mirrors Go's strings.EqualFold (ASCII fast path, caseOrbit
// special fold cycles, and the default simple-lowercase class). A local
// accept is therefore exactly a backend accept; the backend remains the
// final authorization boundary.

import { goEqualFold, goTrimSpace } from "./goFold";

export { goTrimSpace } from "./goFold";

export interface ProductionEnvironmentTarget {
  sessionId?: string;
  tabId?: string;
  assetId?: string;
  hostname?: string;
  sessionName?: string;
  environment: string;
}

export interface EnvironmentConfirmationChallenge {
  targetSetHash: string;
  requiredEnvironmentNames: string[];
  productionTargets: ProductionEnvironmentTarget[];
}

export interface TargetNameStatus {
  target: ProductionEnvironmentTarget;
  typed: string;
  missing: boolean;
  matches: boolean;
}

export interface ExtraTargetInput {
  key: string;
  typed: string;
}

export interface EnvironmentConfirmationState {
  targets: TargetNameStatus[];
  stale: boolean;
  extraInputs: ExtraTargetInput[];
  submittedNames: string[];
  canConfirm: boolean;
}

export function targetKey(target: ProductionEnvironmentTarget): string {
  return target.tabId ?? target.sessionId ?? target.hostname ?? target.environment;
}

export function matchesEnvironmentName(typed: string, environment: string): boolean {
  return goEqualFold(goTrimSpace(typed), goTrimSpace(environment));
}

export function isStaleChallenge(challenge: EnvironmentConfirmationChallenge, latestTargetSetHash: string): boolean {
  return challenge.targetSetHash !== latestTargetSetHash;
}

export function evaluateEnvironmentConfirmation(
  challenge: EnvironmentConfirmationChallenge,
  typedByTarget: Record<string, string>,
  latestTargetSetHash: string,
): EnvironmentConfirmationState {
  const stale = isStaleChallenge(challenge, latestTargetSetHash);
  const targetKeys = new Set(challenge.productionTargets.map(targetKey));
  // Any input for a key outside the challenge's targets fails closed:
  // out-of-challenge state never slips through, mirroring the backend's
  // len(typed) != len(required) cardinality contract.
  const extraInputs = Object.entries(typedByTarget)
    .filter(([key]) => !targetKeys.has(key))
    .map(([key, typed]) => ({ key, typed }));
  const targets = challenge.productionTargets.map((target) => {
    const typed = typedByTarget[targetKey(target)] ?? "";
    const missing = typed.trim() === "";
    return { target, typed, missing, matches: !missing && matchesEnvironmentName(typed, target.environment) };
  });
  const submittedNames = targets.filter((entry) => !entry.missing).map((entry) => goTrimSpace(entry.typed));
  const required = challenge.requiredEnvironmentNames.map((name) => goTrimSpace(name));
  const setMatches =
    submittedNames.length === required.length &&
    required.every((name) => submittedNames.some((submitted) => goEqualFold(submitted, name)));
  const canConfirm =
    !stale && extraInputs.length === 0 && targets.length > 0 && targets.every((entry) => entry.matches) && setMatches;
  return { targets, stale, extraInputs, submittedNames, canConfirm };
}
