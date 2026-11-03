/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flush, flushUntil, mountWithClient, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  getSettings: vi.fn(),
  putSettings: vi.fn(),
}));

vi.mock("../../ipc/fleetAlertsApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/fleetAlertsApi")>()),
  fleetAlertsClient: () => ({ list: mocks.list, getSettings: mocks.getSettings, putSettings: mocks.putSettings, ack: vi.fn() }),
}));
vi.mock("../../features/fleet/useFleetAccount", () => ({
  useFleetAccount: () => ({ loading: false, userId: "u1", isAdmin: false }),
}));

import { DeviceAlertsCard } from "../../features/fleet/DeviceAlertsCard";
import type { FleetAlertSettings } from "../../ipc/fleetAlertsApi";
import type { FleetDevice } from "../../ipc/fleetApi";

const settings: FleetAlertSettings = {
  device_id: "d1",
  kind: "agent",
  disk_enabled: true,
  disk_threshold: 90,
  mem_enabled: true,
  mem_threshold: 85,
  cpu_enabled: false,
  cpu_threshold: 90,
  offline_enabled: true,
  offline_minutes: 10,
  expected_offline: false,
  quiet_enabled: false,
  quiet_start: 1140,
  quiet_end: 540,
  quiet_tz: "",
  server_tz: "UTC",
  suspect_after_minutes: 5,
};

function device(patch: Partial<FleetDevice> = {}): FleetDevice {
  return { id: "d1", name: "web-01", kind: "agent", created_at: 1, last_seen_at: 1, revoked_at: 0, ...patch };
}

let view: MountedView | undefined;
const q = <T extends HTMLElement>(label: string) => view!.container.querySelector<T>(`[aria-label="${label}"]`)!;
const saveButton = () => [...view!.container.querySelectorAll("button")].find((b) => b.textContent === "保存")!;

async function open(dev: FleetDevice = device()) {
  view = mountWithClient(createElement(DeviceAlertsCard, { device: dev }));
  await flushUntil(() => view!.container.querySelector('[aria-label="磁盘使用率阈值"]') !== null || view!.container.textContent!.includes("仅设备所有者"));
  await flush();
}

beforeEach(() => {
  mocks.list.mockResolvedValue({ active: [{ device_id: "d1", rule: "disk", status: "firing", since: 1_700_000_000_000, value: 93 }], events: [] });
  mocks.getSettings.mockResolvedValue(settings);
  mocks.putSettings.mockImplementation((_id: string, input: object) => Promise.resolve({ ...settings, ...input }));
});
afterEach(() => {
  view?.unmount();
  view = undefined;
  vi.clearAllMocks();
});

describe("DeviceAlertsCard", () => {
  it("shows the effective settings, fixed windows, suspect time and active alerts", async () => {
    await open();
    expect(q<HTMLInputElement>("磁盘使用率阈值").value).toBe("90");
    expect(q<HTMLInputElement>("内存使用率阈值").value).toBe("85");
    expect(q<HTMLInputElement>("CPU 使用率告警").checked).toBe(false);
    expect(q<HTMLInputElement>("离线分钟数").value).toBe("10");
    expect(view!.container.textContent).toContain("持续 15 分钟");
    expect(view!.container.textContent).toContain("连续约 5 分钟未收到上报");
    expect(view!.container.querySelectorAll('[data-testid="device-active-alert"]')).toHaveLength(1);
  });

  it("saves every field in one PUT", async () => {
    await open();
    setInputValue(q<HTMLInputElement>("磁盘使用率阈值"), "95");
    click(q("预期离线"));
    click(saveButton());
    await flush();
    expect(mocks.putSettings).toHaveBeenCalledTimes(1);
    const [id, body] = mocks.putSettings.mock.calls[0] as [string, Record<string, unknown>];
    expect(id).toBe("d1");
    expect(body).toEqual({
      disk_enabled: true,
      disk_threshold: 95,
      mem_enabled: true,
      mem_threshold: 85,
      cpu_enabled: false,
      cpu_threshold: 90,
      offline_enabled: true,
      offline_minutes: 10,
      expected_offline: true,
      quiet_enabled: false,
      quiet_start: 1140,
      quiet_end: 540,
      quiet_tz: "",
    });
  });

  it("rejects an out-of-range threshold without calling the server", async () => {
    await open();
    setInputValue(q<HTMLInputElement>("磁盘使用率阈值"), "101");
    click(saveButton());
    await flush();
    expect(mocks.putSettings).not.toHaveBeenCalled();
    expect(view!.container.querySelector('[role="alert"]')?.textContent).toContain("1 到 100");
  });

  it("fills the browser time zone when quiet hours are switched on", async () => {
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    await open();
    click(q("静音时段"));
    click(saveButton());
    await flush();
    const body = mocks.putSettings.mock.calls[0][1] as Record<string, unknown>;
    expect(body.quiet_enabled).toBe(true);
    expect(body.quiet_tz).toBe(zone);
  });

  it("suggests quiet hours for a local helper without changing the settings", async () => {
    await open(device({ kind: "local-helper" }));
    expect(view!.container.querySelector('[data-testid="quiet-suggestion"]')?.textContent).toContain("19:00-09:00");
    expect(q<HTMLInputElement>("静音时段").checked).toBe(false);
    click(saveButton());
    await flush();
    const body = mocks.putSettings.mock.calls[0][1] as Record<string, unknown>;
    expect(body.quiet_enabled).toBe(false);
    expect(body.quiet_tz).toBe("");
  });

  it("does not suggest quiet hours for a plain server", async () => {
    await open();
    expect(view!.container.querySelector('[data-testid="quiet-suggestion"]')).toBeNull();
  });

  it("disables editing for a non-owner after the server answers 403", async () => {
    mocks.putSettings.mockRejectedValue({ code: "forbidden", status: 403, message: "no" });
    await open();
    click(saveButton());
    await flush();
    expect(view!.container.textContent).toContain("仅设备所有者可修改告警设置");
    expect(q<HTMLInputElement>("磁盘使用率阈值").disabled).toBe(true);
    expect(saveButton().disabled).toBe(true);
  });

  it("shows the owner-only note when the settings cannot be read", async () => {
    mocks.getSettings.mockRejectedValue({ code: "forbidden", status: 403, message: "no" });
    await open();
    expect(view!.container.textContent).toContain("仅设备所有者可修改告警设置");
    expect(view!.container.querySelector('[aria-label="磁盘使用率阈值"]')).toBeNull();
  });
});
