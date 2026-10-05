import type { ModelProfile, ModelProfilesView } from "../../ipc/commands";

export function requestTimeoutLabel(profile: ModelProfile): string {
  return profile.requestTimeoutSeconds?.toString() ?? "";
}

export function idleTimeoutLabel(profile: ModelProfile): string {
  return profile.idleTimeoutSeconds?.toString() ?? "";
}

export function timeoutSecondsFromInput(raw: string, allowZero: boolean): number | null {
  const trimmed = raw.trim();
  if (!trimmed) return null;
  const value = Number(trimmed);
  if (!Number.isFinite(value)) return null;
  const rounded = Math.round(value);
  if (allowZero ? rounded < 0 : rounded < 1) return null;
  return rounded;
}

export const MAX_TOKENS_HARD_LIMIT = 32768;

export function maxTokensLabel(profile: ModelProfile): string {
  return profile.maxTokens?.toString() ?? "";
}

export function maxTokensFromInput(raw: string): number | null {
  return positiveIntFromInput(raw);
}

export const CIRCUIT_DEFAULT_THRESHOLD = 5;
export const CIRCUIT_DEFAULT_COOLDOWN_SECONDS = 300;

export function circuitThresholdLabel(profile: ModelProfile): string {
  return profile.circuitFailureThreshold?.toString() ?? "";
}

export function circuitCooldownLabel(profile: ModelProfile): string {
  return profile.circuitCooldownSeconds?.toString() ?? "";
}

export function circuitThresholdFromInput(raw: string): number | null {
  return positiveIntFromInput(raw);
}

export function circuitCooldownFromInput(raw: string): number | null {
  return positiveIntFromInput(raw);
}

export function circuitEffective(profile: ModelProfile): {
  threshold: number;
  cooldownSeconds: number;
} {
  return {
    threshold: profile.circuitFailureThreshold ?? CIRCUIT_DEFAULT_THRESHOLD,
    cooldownSeconds: profile.circuitCooldownSeconds ?? CIRCUIT_DEFAULT_COOLDOWN_SECONDS,
  };
}

function positiveIntFromInput(raw: string): number | null {
  const trimmed = raw.trim();
  if (!trimmed) return null;
  const value = Number(trimmed);
  if (!Number.isFinite(value)) return null;
  const rounded = Math.round(value);
  if (rounded < 1) return null;
  return rounded;
}

export function selectModelProfileId(
  view: ModelProfilesView,
  preferredId?: string,
): string | null {
  const candidates = [preferredId, view.activeId, view.profiles[0]?.id];
  for (const candidate of candidates) {
    if (candidate && view.profiles.some((profile) => profile.id === candidate)) return candidate;
  }
  return null;
}

export function fallbackModelFromInput(raw: string): string | null {
  const trimmed = raw.trim();
  return trimmed ? trimmed : null;
}

export function fallbackModelLabel(profile: ModelProfile): string {
  return profile.fallbackModel ?? "";
}

export const MODEL_PARAM_DEFAULTS = {
  temperature: 0.3,
  contextWindow: 32768,
  proxy: null,
  stream: true,
  fallbackModel: null,
} as const;

export function resetModelParams(profile: ModelProfile): ModelProfile {
  return { ...profile, ...MODEL_PARAM_DEFAULTS };
}

export function modelParamsAtDefaults(profile: ModelProfile): boolean {
  return (
    profile.temperature === MODEL_PARAM_DEFAULTS.temperature &&
    profile.contextWindow === MODEL_PARAM_DEFAULTS.contextWindow &&
    (profile.proxy ?? null) === MODEL_PARAM_DEFAULTS.proxy &&
    profile.stream === MODEL_PARAM_DEFAULTS.stream &&
    fallbackModelLabel(profile) === (MODEL_PARAM_DEFAULTS.fallbackModel ?? "")
  );
}

export function profileKeyUnavailable(profile: ModelProfile): boolean {
  const trimmed = profile.apiKey.trim();
  return trimmed !== "" && /^[*•●·…]+$/.test(trimmed);
}

export function sameModelProfile(a: ModelProfile, b: ModelProfile): boolean {
  return (
    a.name === b.name &&
    a.baseUrl === b.baseUrl &&
    a.apiKey === b.apiKey &&
    a.model === b.model &&
    a.temperature === b.temperature &&
    a.contextWindow === b.contextWindow &&
    (a.proxy ?? "") === (b.proxy ?? "") &&
    a.stream === b.stream &&
    fallbackModelLabel(a) === fallbackModelLabel(b) &&
    (a.requestTimeoutSeconds ?? null) === (b.requestTimeoutSeconds ?? null) &&
    (a.idleTimeoutSeconds ?? null) === (b.idleTimeoutSeconds ?? null) &&
    (a.maxTokens ?? null) === (b.maxTokens ?? null) &&
    (a.circuitFailureThreshold ?? null) === (b.circuitFailureThreshold ?? null) &&
    (a.circuitCooldownSeconds ?? null) === (b.circuitCooldownSeconds ?? null)
  );
}
