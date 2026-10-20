// View-model for the R-01 typed environment-name production confirmation.
// The validation mirrors the backend guard.EnvironmentNamesMatch semantics:
// per-target set equality with surrounding space and letter case ignored, and
// a confirmation is valid only when every production target's typed name
// matches its own actual environment and the typed set equals the required
// set. A stale challenge, an empty or wrong name, a cross-target mismatch, or
// a cancelled dialog never confirms.

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

export interface EnvironmentConfirmationState {
  targets: TargetNameStatus[];
  stale: boolean;
  canConfirm: boolean;
}

export function targetKey(target: ProductionEnvironmentTarget): string {
  return target.tabId ?? target.sessionId ?? target.hostname ?? target.environment;
}

export function matchesEnvironmentName(typed: string, environment: string): boolean {
  return typed.trim().toLowerCase() === environment.trim().toLowerCase();
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
  const targets = challenge.productionTargets.map((target) => {
    const typed = typedByTarget[targetKey(target)] ?? "";
    const missing = typed.trim() === "";
    return { target, typed, missing, matches: !missing && matchesEnvironmentName(typed, target.environment) };
  });
  const required = challenge.requiredEnvironmentNames.map((name) => name.trim().toLowerCase());
  const typedSet = new Set(targets.filter((entry) => !entry.missing).map((entry) => entry.typed.trim().toLowerCase()));
  const setMatches =
    typedSet.size === new Set(required).size && [...typedSet].every((name) => new Set(required).has(name));
  const canConfirm = !stale && targets.length > 0 && targets.every((entry) => entry.matches) && setMatches;
  return { targets, stale, canConfirm };
}
