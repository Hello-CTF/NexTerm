/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it } from "vitest";
import { projectFocusForGroup } from "../../app/projectFocus";
import {
  DEFAULT_FLEET_HEALTH_SETTINGS,
  FLEET_HEALTH_MAX_INTERVAL_MS,
  FLEET_HEALTH_MIN_INTERVAL_MS,
  fleetHealthSettingsKey,
  normalizeFleetHealthInterval,
  readFleetHealthSettings,
  writeFleetHealthSettings,
} from "../../features/fleet/healthSettings";

describe("fleet health project settings", () => {
  beforeEach(() => window.localStorage.clear());

  it("defaults to manual polling and keeps opt-in isolated per project", () => {
    const first = projectFocusForGroup("p1");
    const second = projectFocusForGroup("p2");
    expect(readFleetHealthSettings(first)).toEqual(DEFAULT_FLEET_HEALTH_SETTINGS);

    writeFleetHealthSettings(first, { autoPoll: true, intervalMs: 1 });

    expect(readFleetHealthSettings(first)).toEqual({ autoPoll: true, intervalMs: FLEET_HEALTH_MIN_INTERVAL_MS });
    expect(readFleetHealthSettings(second)).toEqual(DEFAULT_FLEET_HEALTH_SETTINGS);
    expect(fleetHealthSettingsKey(first)).not.toBe(fleetHealthSettingsKey(second));
  });

  it("normalizes the interval with a lower bound and preserves the five-minute default", () => {
    expect(normalizeFleetHealthInterval(Number.NaN)).toBe(300_000);
    expect(normalizeFleetHealthInterval(0)).toBe(FLEET_HEALTH_MIN_INTERVAL_MS);
    expect(normalizeFleetHealthInterval(300_000)).toBe(300_000);
    expect(normalizeFleetHealthInterval(Number.MAX_SAFE_INTEGER)).toBe(FLEET_HEALTH_MAX_INTERVAL_MS);
  });
});
