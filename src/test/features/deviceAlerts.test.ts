import { describe, expect, it } from "vitest";
import {
  activeAlertCounts,
  alertEventText,
  alertEventTitle,
  bellAlertCount,
  clockSkewText,
  mutedAlertEvents,
  ruleName,
  visibleAlertEvents,
} from "../../features/fleet/deviceAlerts";
import type { FleetAlertEvent, FleetAlerts } from "../../ipc/fleetAlertsApi";

function event(patch: Partial<FleetAlertEvent>): FleetAlertEvent {
  return { id: "e1", device_id: "d1", rule: "disk", kind: "fired", ts: 1, duration_ms: 0, muted: false, ...patch };
}

describe("device alert wording", () => {
  it("names the rules", () => {
    expect(["disk", "mem", "cpu", "offline"].map(ruleName)).toEqual(["磁盘", "内存", "CPU", "离线"]);
  });

  it("describes a threshold alert with value, threshold and the fixed window", () => {
    expect(alertEventText(event({ value: 93, threshold: 90 }))).toBe("磁盘使用率 93%，超过 90% 已持续 10 分钟");
    expect(alertEventText(event({ rule: "cpu", value: 95, threshold: 90 }))).toContain("已持续 15 分钟");
    expect(alertEventText(event({ kind: "resolved" }))).toBe("已恢复");
  });

  it("describes offline alerts, recoveries and muted records", () => {
    const ten = 10 * 60_000;
    expect(alertEventText(event({ rule: "offline", duration_ms: ten }))).toBe("已离线 10 分钟");
    expect(alertEventText(event({ rule: "offline", kind: "resolved", duration_ms: ten * 3 }))).toBe("已恢复，离线 30 分钟");
    expect(alertEventText(event({ rule: "offline", kind: "resolved", muted: true, duration_ms: ten * 6 }))).toBe(
      "静音期间离线 60 分钟，已恢复",
    );
  });

  it("builds notification titles", () => {
    expect(alertEventTitle("web-01", event({}))).toBe("web-01 · 磁盘告警");
    expect(alertEventTitle("web-01", event({ kind: "resolved" }))).toContain("已恢复");
  });
});

describe("device alert aggregation", () => {
  const alerts: FleetAlerts = {
    active: [
      { device_id: "d1", rule: "disk", status: "firing", since: 1 },
      { device_id: "d1", rule: "cpu", status: "firing", since: 2 },
      { device_id: "d2", rule: "offline", status: "firing", since: 3 },
    ],
    events: [event({ id: "a" }), event({ id: "b", muted: true })],
  };

  it("counts active alerts per device", () => {
    const counts = activeAlertCounts(alerts);
    expect(counts.get("d1")).toBe(2);
    expect(counts.get("d2")).toBe(1);
    expect(counts.get("d3")).toBeUndefined();
    expect(activeAlertCounts(undefined).size).toBe(0);
  });

  it("separates muted records from visible events", () => {
    expect(visibleAlertEvents(alerts).map((e) => e.id)).toEqual(["a"]);
    expect(mutedAlertEvents(alerts).map((e) => e.id)).toEqual(["b"]);
  });
});

describe("bell count", () => {
  it("counts only unacknowledged, unmuted fired events", () => {
    const alerts: FleetAlerts = {
      active: [],
      events: [
        event({ id: "f1" }),
        event({ id: "f2", device_id: "d2" }),
        event({ id: "r1", kind: "resolved" }),
        event({ id: "m1", kind: "fired", muted: true }),
      ],
    };
    expect(bellAlertCount(alerts)).toBe(2);
    expect(bellAlertCount(undefined)).toBe(0);
  });
});

describe("clock skew wording", () => {
  it("is empty without a skew", () => {
    expect(clockSkewText(0)).toBe("");
    expect(clockSkewText(undefined)).toBe("");
  });
  it("names direction and rounds to seconds, minutes or hours", () => {
    expect(clockSkewText(90_000)).toContain("约 2 分钟（快）");
    expect(clockSkewText(-5 * 3_600_000)).toContain("约 5 小时（慢）");
    expect(clockSkewText(-75_000)).toContain("（慢）");
    expect(clockSkewText(30_000)).toContain("约 30 秒（快）");
    expect(clockSkewText(90_000)).toContain("指标按服务器接收时间记录");
  });
});
