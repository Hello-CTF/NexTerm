import type { ModelProfile, ModelProfilesView } from "../../ipc/commands";

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
    fallbackModelLabel(a) === fallbackModelLabel(b)
  );
}
