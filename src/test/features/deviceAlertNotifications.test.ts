import { describe, expect, it, vi } from "vitest";
import { DeviceAlertNotifier, type DeviceAlertNotificationDeps } from "../../features/inbox/deviceAlertNotifications";
import type { FleetAlertEvent, FleetAlerts } from "../../ipc/fleetAlertsApi";

function event(id: string, patch: Partial<FleetAlertEvent> = {}): FleetAlertEvent {
  return { id, device_id: "d1", rule: "disk", kind: "fired", ts: 1, value: 93, threshold: 90, duration_ms: 0, muted: false, ...patch };
}

const alerts = (...events: FleetAlertEvent[]): FleetAlerts => ({ active: [], events });

function setup(over: { focused?: boolean; system?: boolean; deviceAlerts?: boolean; available?: boolean } = {}) {
  const show = vi.fn(() => Promise.resolve());
  const toast = vi.fn();
  const deps: DeviceAlertNotificationDeps = {
    notifier: {
      status: () => Promise.resolve({ available: over.available ?? true, permission: "granted" }),
      request: () => Promise.resolve({ available: true }),
      show,
      remove: () => {},
      onClick: () => {},
    },
    appFocused: () => over.focused ?? false,
    prefs: () => ({ system: over.system ?? true, deviceAlerts: over.deviceAlerts ?? true }),
    deviceName: (id) => (id === "d1" ? "web-01" : undefined),
    toast,
  };
  return { deps, show, toast, notifier: new DeviceAlertNotifier() };
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("device alert notifications", () => {
  it("does not replay events present on the first load", async () => {
    const { deps, show, toast, notifier } = setup();
    notifier.consume("u1", alerts(event("old")), deps);
    notifier.consume("u1", alerts(event("old")), deps);
    await settle();
    expect(show).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
  });

  it("notifies a new event once through the system", async () => {
    const { deps, show, toast, notifier } = setup();
    notifier.consume("u1", alerts(), deps);
    notifier.consume("u1", alerts(event("n1")), deps);
    notifier.consume("u1", alerts(event("n1")), deps);
    await settle();
    expect(show).toHaveBeenCalledTimes(1);
    expect(show).toHaveBeenCalledWith({
      id: "device-alert:n1",
      title: "web-01 · 磁盘告警",
      body: "磁盘使用率 93%，超过 90% 已持续 10 分钟",
    });
    expect(toast).not.toHaveBeenCalled();
  });

  it("names an unknown device generically", async () => {
    const { deps, show, notifier } = setup();
    notifier.consume("u1", alerts(), deps);
    notifier.consume("u1", alerts(event("n1", { device_id: "gone" })), deps);
    await settle();
    expect(show).toHaveBeenCalledWith(expect.objectContaining({ title: "设备 · 磁盘告警" }));
  });

  it("stays silent for muted events", async () => {
    const { deps, show, toast, notifier } = setup();
    notifier.consume("u1", alerts(), deps);
    notifier.consume("u1", alerts(event("m1", { rule: "offline", kind: "resolved", muted: true })), deps);
    await settle();
    expect(show).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
  });

  it("stays silent when the device alert preference is off", async () => {
    const { deps, show, toast, notifier } = setup({ deviceAlerts: false });
    notifier.consume("u1", alerts(), deps);
    notifier.consume("u1", alerts(event("n1")), deps);
    await settle();
    expect(show).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
  });

  it("uses an in-app toast while the app is focused, when system notifications are off or unavailable", async () => {
    for (const over of [{ focused: true }, { system: false }, { available: false }]) {
      const { deps, show, toast, notifier } = setup(over);
      notifier.consume("u1", alerts(), deps);
      notifier.consume("u1", alerts(event("n1")), deps);
      await settle();
      expect(show).not.toHaveBeenCalled();
      expect(toast).toHaveBeenCalledTimes(1);
    }
  });

  it("starts over when the account changes", async () => {
    const { deps, show, notifier } = setup();
    notifier.consume("u1", alerts(), deps);
    notifier.consume("u2", alerts(event("x1")), deps);
    await settle();
    expect(show).not.toHaveBeenCalled();
    notifier.consume("u2", alerts(event("x1"), event("x2")), deps);
    await settle();
    expect(show).toHaveBeenCalledTimes(1);
  });
});
