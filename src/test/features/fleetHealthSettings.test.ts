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
    expect(readFleetHealthSettings(first, "user-a")).toEqual(DEFAULT_FLEET_HEALTH_SETTINGS);

    writeFleetHealthSettings(first, { autoPoll: true, intervalMs: 1 }, "user-a");

    expect(readFleetHealthSettings(first, "user-a")).toEqual({ autoPoll: true, intervalMs: FLEET_HEALTH_MIN_INTERVAL_MS });
    expect(readFleetHealthSettings(second, "user-a")).toEqual(DEFAULT_FLEET_HEALTH_SETTINGS);
    expect(fleetHealthSettingsKey(first, "user-a")).not.toBe(fleetHealthSettingsKey(second, "user-a"));
  });

  it("keeps the opt-in isolated per account", () => {
    const project = projectFocusForGroup("p1");
    writeFleetHealthSettings(project, { autoPoll: true, intervalMs: 300_000 }, "user-a");

    expect(readFleetHealthSettings(project, "user-b")).toEqual(DEFAULT_FLEET_HEALTH_SETTINGS);
    expect(fleetHealthSettingsKey(project, "user-a")).not.toBe(fleetHealthSettingsKey(project, "user-b"));

    writeFleetHealthSettings(project, { autoPoll: true, intervalMs: 300_000 }, "user-b");
    writeFleetHealthSettings(project, { autoPoll: false, intervalMs: 300_000 }, "user-a");
    expect(readFleetHealthSettings(project, "user-b")).toEqual({ autoPoll: true, intervalMs: 300_000 });
    expect(readFleetHealthSettings(project, "user-a")).toEqual({ autoPoll: false, intervalMs: 300_000 });
  });

  it("never migrates a legacy global or unscoped key as authorization", () => {
    const project = projectFocusForGroup("p1");
    window.localStorage.setItem("nexterm:fleet-health:v1", JSON.stringify({ autoPoll: true, intervalMs: 300_000 }));
    window.localStorage.setItem(`nexterm:fleet-health:v1:${encodeURIComponent(project)}`, JSON.stringify({ autoPoll: true, intervalMs: 300_000 }));

    expect(readFleetHealthSettings(project, "user-a")).toEqual(DEFAULT_FLEET_HEALTH_SETTINGS);
    expect(fleetHealthSettingsKey(project, "user-a")).not.toBe(`nexterm:fleet-health:v1:${encodeURIComponent(project)}`);
  });

  it("normalizes the interval with a lower bound and preserves the five-minute default", () => {
    expect(normalizeFleetHealthInterval(Number.NaN)).toBe(300_000);
    expect(normalizeFleetHealthInterval(0)).toBe(FLEET_HEALTH_MIN_INTERVAL_MS);
    expect(normalizeFleetHealthInterval(300_000)).toBe(300_000);
    expect(normalizeFleetHealthInterval(Number.MAX_SAFE_INTEGER)).toBe(FLEET_HEALTH_MAX_INTERVAL_MS);
  });
});
