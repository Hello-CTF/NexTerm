/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({ list: vi.fn(), ack: vi.fn() }));

vi.mock("../../ipc/fleetAlertsApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/fleetAlertsApi")>()),
  fleetAlertsClient: () => ({ list: mocks.list, ack: mocks.ack, getSettings: vi.fn(), putSettings: vi.fn() }),
}));
vi.mock("../../features/fleet/useFleetAccount", () => ({
  useFleetAccount: () => ({ loading: false, userId: "u1", isAdmin: false }),
}));

import { resetAlertHintForTest } from "../../features/inbox/alertFirstHint";
import { DeviceAlertInbox } from "../../features/inbox/DeviceAlertInbox";
import type { FleetAlertEvent } from "../../ipc/fleetAlertsApi";

function event(id: string, patch: Partial<FleetAlertEvent> = {}): FleetAlertEvent {
  return { id, device_id: "d1", rule: "disk", kind: "fired", ts: 1_700_000_000_000, value: 93, threshold: 90, duration_ms: 0, muted: false, ...patch };
}

let view: MountedView | undefined;
let events: FleetAlertEvent[];

async function open() {
  const expectContent = events.length > 0;
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(["fleet-devices", "u1"], { devices: [{ id: "d1", name: "web-01" }] });
  view = mount(createElement(QueryClientProvider, { client }, createElement(DeviceAlertInbox)));
  if (expectContent) await flushUntil(() => view!.container.querySelector('[data-testid="device-alert-inbox"]') !== null);
  await flush();
}

const text = () => view!.container.textContent ?? "";
const button = (name: string) => [...view!.container.querySelectorAll("button")].find((b) => (b.getAttribute("aria-label") ?? b.textContent) === name || b.textContent === name);

beforeEach(() => {
  resetAlertHintForTest();
  try {
    localStorage.clear();
  } catch {
    // ignore
  }
  events = [event("a"), event("b", { rule: "offline", duration_ms: 600_000 }), event("m", { rule: "offline", kind: "resolved", muted: true, duration_ms: 1_200_000 })];
  mocks.list.mockImplementation(() => Promise.resolve({ active: [], events }));
  mocks.ack.mockImplementation((ids: string[]) => {
    events = events.filter((e) => !ids.includes(e.id));
    return Promise.resolve({ ok: true, acked: ids.length });
  });
});
afterEach(() => {
  view?.unmount();
  view = undefined;
  vi.clearAllMocks();
});

describe("DeviceAlertInbox", () => {
  it("lists unacknowledged events with the device name and keeps muted records collapsed", async () => {
    await open();
    expect(text()).toContain("web-01 · 磁盘告警");
    expect(text()).toContain("已离线 10 分钟");
    expect(text()).toContain("静音期间的离线记录（1）");
    expect(text()).not.toContain("静音期间离线 20 分钟");
    click(button("静音期间的离线记录（1）")!);
    expect(text()).toContain("静音期间离线 20 分钟，已恢复");
  });

  it("acknowledges one event", async () => {
    await open();
    click(button("确认 web-01 · 磁盘告警")!);
    await flush();
    expect(mocks.ack).toHaveBeenCalledWith(["a"]);
    expect(text()).not.toContain("web-01 · 磁盘告警");
    expect(text()).toContain("已离线 10 分钟");
  });

  it("acknowledges all visible events at once, leaving muted records", async () => {
    await open();
    click(button("全部确认")!);
    await flush();
    expect(mocks.ack).toHaveBeenCalledWith(["a", "b"]);
    expect(text()).not.toContain("全部确认");
    expect(text()).toContain("静音期间的离线记录（1）");
  });

  it("acknowledges a muted record once expanded", async () => {
    await open();
    click(button("静音期间的离线记录（1）")!);
    click(view!.container.querySelector<HTMLElement>('[data-testid="device-alert-muted-list"] button')!);
    await flush();
    expect(mocks.ack).toHaveBeenCalledWith(["m"]);
    expect(text()).not.toContain("静音期间的离线记录");
  });

  it("renders nothing without events", async () => {
    events = [];
    await open();
    expect(view!.container.querySelector('[data-testid="device-alert-inbox"]')).toBeNull();
  });

  it("shows the first-alert hint once and not again after it is acknowledged", async () => {
    await open();
    expect(text()).toContain("可在设备详情关闭规则，或在通知设置里关闭系统通知");
    click(button("知道了")!);
    await flush();
    expect(text()).not.toContain("可在设备详情关闭规则");
    view!.unmount();
    view = undefined;
    await open();
    expect(text()).not.toContain("可在设备详情关闭规则");
  });

  it("does not show the hint again after it was displayed once and the panel closed", async () => {
    await open();
    expect(text()).toContain("可在设备详情关闭规则");
    view!.unmount();
    view = undefined;
    await open();
    expect(text()).not.toContain("可在设备详情关闭规则");
  });
});
