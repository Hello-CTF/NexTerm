import type { ProjectFocus } from "../../app/projectFocus";

export const FLEET_HEALTH_DEFAULT_INTERVAL_MS = 5 * 60_000;
export const FLEET_HEALTH_MIN_INTERVAL_MS = 60_000;
export const FLEET_HEALTH_MAX_INTERVAL_MS = 60 * 60_000;

const STORAGE_PREFIX = "nexterm:fleet-health:v1:";

export interface FleetHealthProjectSettings {
  autoPoll: boolean;
  intervalMs: number;
}

export const DEFAULT_FLEET_HEALTH_SETTINGS: FleetHealthProjectSettings = {
  autoPoll: false,
  intervalMs: FLEET_HEALTH_DEFAULT_INTERVAL_MS,
};

export function normalizeFleetHealthInterval(value: number): number {
  if (!Number.isFinite(value)) return FLEET_HEALTH_DEFAULT_INTERVAL_MS;
  const minutes = Math.round(value / 60_000);
  return Math.min(
    FLEET_HEALTH_MAX_INTERVAL_MS,
    Math.max(FLEET_HEALTH_MIN_INTERVAL_MS, minutes * 60_000),
  );
}

export function fleetHealthSettingsKey(project: ProjectFocus): string {
  return `${STORAGE_PREFIX}${encodeURIComponent(project)}`;
}

export function readFleetHealthSettings(project: ProjectFocus): FleetHealthProjectSettings {
  try {
    const raw = window.localStorage.getItem(fleetHealthSettingsKey(project));
    if (!raw) return { ...DEFAULT_FLEET_HEALTH_SETTINGS };
    const value: unknown = JSON.parse(raw);
    if (typeof value !== "object" || value === null) return { ...DEFAULT_FLEET_HEALTH_SETTINGS };
    const candidate = value as { autoPoll?: unknown; intervalMs?: unknown };
    return {
      autoPoll: candidate.autoPoll === true,
      intervalMs: typeof candidate.intervalMs === "number"
        ? normalizeFleetHealthInterval(candidate.intervalMs)
        : FLEET_HEALTH_DEFAULT_INTERVAL_MS,
    };
  } catch {
    return { ...DEFAULT_FLEET_HEALTH_SETTINGS };
  }
}

export function writeFleetHealthSettings(project: ProjectFocus, settings: FleetHealthProjectSettings): void {
  const normalized = {
    autoPoll: settings.autoPoll,
    intervalMs: normalizeFleetHealthInterval(settings.intervalMs),
  };
  try {
    window.localStorage.setItem(fleetHealthSettingsKey(project), JSON.stringify(normalized));
  } catch {
  }
}
