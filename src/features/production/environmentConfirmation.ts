// View-model for the R-01 typed environment-name production confirmation.
//
// Cross-language contract with the backend (guard.EnvironmentNamesMatch):
// the backend trims with Go's strings.TrimSpace (the Unicode White_Space
// set) and compares with strings.EqualFold (simple case folding). The local
// check is a provable fail-closed subset: it trims exactly the Go
// White_Space set and then requires exact codepoint equality. Identical
// runes are always EqualFold-equal, so a local accept implies a backend
// accept; case-folding differences (uppercase input, long s, final sigma)
// are rejected locally and accepted by the backend, which remains the final
// authorization boundary.

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

// goSpaceChars is the complete Unicode White_Space set used by Go's
// unicode.IsSpace: U+0085 is included and U+FEFF is excluded, matching the
// backend exactly (unlike JS trim(), which does the opposite).
const goSpaceChars = new Set([
  0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x20, 0x85, 0xa0, 0x1680, 0x2000, 0x2001, 0x2002, 0x2003, 0x2004, 0x2005, 0x2006,
  0x2007, 0x2008, 0x2009, 0x200a, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000,
]);

export function goTrimSpace(value: string): string {
  let start = 0;
  let end = value.length;
  while (start < end && goSpaceChars.has(value.codePointAt(start) ?? 0)) {
    start += 1;
  }
  while (end > start && goSpaceChars.has(value.codePointAt(end - 1) ?? 0)) {
    end -= 1;
  }
  return value.slice(start, end);
}

export function matchesEnvironmentName(typed: string, environment: string): boolean {
  return goTrimSpace(typed) === goTrimSpace(environment);
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
  // Any non-empty input for a key outside the challenge's targets fails
  // closed, mirroring the backend's len(typed) != len(required) rejection.
  const extraInputs = Object.entries(typedByTarget)
    .filter(([key, typed]) => !targetKeys.has(key) && typed.trim() !== "")
    .map(([key, typed]) => ({ key, typed }));
  const targets = challenge.productionTargets.map((target) => {
    const typed = typedByTarget[targetKey(target)] ?? "";
    const missing = typed.trim() === "";
    return { target, typed, missing, matches: !missing && matchesEnvironmentName(typed, target.environment) };
  });
  const submittedNames = targets.filter((entry) => !entry.missing).map((entry) => goTrimSpace(entry.typed));
  const required = challenge.requiredEnvironmentNames.map((name) => goTrimSpace(name));
  const setMatches =
    submittedNames.length === required.length && required.every((name) => submittedNames.includes(name));
  const canConfirm =
    !stale && extraInputs.length === 0 && targets.length > 0 && targets.every((entry) => entry.matches) && setMatches;
  return { targets, stale, extraInputs, submittedNames, canConfirm };
}
