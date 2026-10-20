/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import {
  createFleetHealthTabId,
  FLEET_HEALTH_TAB_ID,
  isFleetHealthTab,
  isFleetHealthTabId,
} from "../../features/fleet/fleetHealthTab";

describe("fleet health tab identity", () => {
  it("creates a unique prefixed instance ID per opened instance", () => {
    const ids = new Set(Array.from({ length: 100 }, () => createFleetHealthTabId()));
    expect(ids.size).toBe(100);
    for (const id of ids) {
      expect(id.startsWith(`${FLEET_HEALTH_TAB_ID}:`)).toBe(true);
      expect(id.length).toBeGreaterThan(FLEET_HEALTH_TAB_ID.length + 1);
    }
  });

  it("recognizes the legacy fixed ID and new instance IDs", () => {
    expect(isFleetHealthTabId(FLEET_HEALTH_TAB_ID)).toBe(true);
    expect(isFleetHealthTabId(createFleetHealthTabId())).toBe(true);
  });

  it("rejects IDs that only look similar and other special tabs", () => {
    expect(isFleetHealthTabId("fleet-health")).toBe(false);
    expect(isFleetHealthTabId(`${FLEET_HEALTH_TAB_ID}-extra`)).toBe(false);
    expect(isFleetHealthTabId(`${FLEET_HEALTH_TAB_ID}x:1`)).toBe(false);
    expect(isFleetHealthTabId("multi-host-log-tail")).toBe(false);
    expect(isFleetHealthTabId("multi-host-log-tail:123")).toBe(false);
  });

  it("resolves fleet health tabs from tab metadata independently of the holding workspace", () => {
    const legacy = { id: FLEET_HEALTH_TAB_ID, kind: "log" };
    const instance = { id: createFleetHealthTabId(), kind: "log" };
    const logFile = { id: "log-tab-1", kind: "log", path: "/var/log/syslog" };
    const terminal = { id: FLEET_HEALTH_TAB_ID, kind: "terminal" };
    const heldIn = <T extends { id: string; kind: string }>(tab: T, _workspaceId: string): T => tab;

    for (const workspaceId of ["ws-a", "ws-b"]) {
      expect(isFleetHealthTab(heldIn(legacy, workspaceId))).toBe(true);
      expect(isFleetHealthTab(heldIn(instance, workspaceId))).toBe(true);
      expect(isFleetHealthTab(heldIn(logFile, workspaceId))).toBe(false);
      expect(isFleetHealthTab(heldIn(terminal, workspaceId))).toBe(false);
    }
  });
});
