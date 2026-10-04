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
