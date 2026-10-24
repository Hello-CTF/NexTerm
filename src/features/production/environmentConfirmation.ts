// View-model for the R-01 typed environment-name production confirmation.
//
// Cross-language contract with the backend (guard.EnvironmentNamesMatch):
// goTrimSpace trims exactly Go's unicode.IsSpace White_Space set, and
// goEqualFold mirrors Go's strings.EqualFold (ASCII fast path, caseOrbit
// special fold cycles, and the default simple-lowercase class). A local
// accept is therefore exactly a backend accept; the backend remains the
// final authorization boundary.

import { goEqualFold, goToLowerString, goTrimSpace } from "./goFold";

export { goTrimSpace } from "./goFold";

// Mirrors guard.ManagedDeviceEnvironment: the environment the server assigns to
// managed devices, which carry no environment tag. The wire value never changes;
// only its display is translated.
export const MANAGED_DEVICE_ENVIRONMENT = "managed-device";
export const MANAGED_DEVICE_ENVIRONMENT_LABEL = "受管设备（无环境标签，按生产环境处理）";
export const MANAGED_DEVICE_ENVIRONMENT_HINT =
  "受管设备没有环境标签，因此一律按生产环境处理，每台主机都需要输入确认；请在对应输入框输入 managed-device。";

export function isManagedDeviceEnvironment(environment: string): boolean {
  return matchesEnvironmentName(environment, MANAGED_DEVICE_ENVIRONMENT);
}

// environmentDisplayLabel is the human-readable form of a wire environment name.
export function environmentDisplayLabel(environment: string): string {
  return isManagedDeviceEnvironment(environment) ? MANAGED_DEVICE_ENVIRONMENT_LABEL : environment;
}

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
    const missing = goTrimSpace(typed) === "";
    return { target, typed, missing, matches: !missing && matchesEnvironmentName(typed, target.environment) };
  });
  // The backend's requiredEnvironmentNames is deduplicated with goTrimSpace +
  // strings.ToLower keys (not EqualFold), so fold-equivalent but distinct
  // names like ſ/s and ς/σ stay separate entries; deduplicate the same way.
  const submittedNames: string[] = [];
  const seenNames = new Set<string>();
  for (const entry of targets) {
    if (entry.missing) {
      continue;
    }
    const name = goTrimSpace(entry.typed);
    const key = goToLowerString(name);
    if (seenNames.has(key)) {
      continue;
    }
    seenNames.add(key);
    submittedNames.push(name);
  }
  const required = challenge.requiredEnvironmentNames.map((name) => goTrimSpace(name));
  const setMatches =
    submittedNames.length === required.length &&
    required.every((name) => submittedNames.some((submitted) => goEqualFold(submitted, name)));
  const canConfirm =
    !stale && extraInputs.length === 0 && targets.length > 0 && targets.every((entry) => entry.matches) && setMatches;
  return { targets, stale, extraInputs, submittedNames, canConfirm };
}
