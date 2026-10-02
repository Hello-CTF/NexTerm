import type { ModelProfilesView } from "../../ipc/commands";

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
