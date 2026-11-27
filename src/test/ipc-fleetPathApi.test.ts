import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ call: vi.fn(), listenEvent: vi.fn() }));
vi.mock("../ipc/commands", async () => {
  const { createFakeApi } = await import("./helpers/createFakeApi");
  return createFakeApi({ call: mocks.call });
});
vi.mock("../ipc/events", async () => {
  const actual = await vi.importActual<typeof import("../ipc/events")>("../ipc/events");
  return { ...actual, listenEvent: mocks.listenEvent };
});

import { DEVICE_PATH_EVENT, fleetPathApi, type DevicePathStatus } from "../ipc/fleetPathApi";

const status: DevicePathStatus = { deviceId: "dev-1", mode: "auto", path: "direct", reason: "", since: 10 };

describe("device path IPC adapter", () => {
  beforeEach(() => vi.clearAllMocks());

  it("forwards the device path commands with their arguments", async () => {
    mocks.call.mockResolvedValueOnce({ mode: "relay" });
    await expect(fleetPathApi.getMode("dev-1")).resolves.toEqual({ mode: "relay" });
    expect(mocks.call).toHaveBeenLastCalledWith("fleet_device_connection_mode_get", { deviceId: "dev-1" });

    mocks.call.mockResolvedValueOnce({ mode: "direct" });
    await expect(fleetPathApi.setMode("dev-1", "direct")).resolves.toEqual({ mode: "direct" });
    expect(mocks.call).toHaveBeenLastCalledWith("fleet_device_connection_mode_set", { deviceId: "dev-1", mode: "direct" });

    mocks.call.mockResolvedValueOnce(status);
    await expect(fleetPathApi.status("dev-1")).resolves.toEqual(status);
    expect(mocks.call).toHaveBeenLastCalledWith("fleet_device_path_status", { deviceId: "dev-1" });

    const diagnostics = { status, localNat: "independent", remoteNat: "dependent", probed: true, stunReachable: true, recentSwitches: [], policy: { server: "on", device: "on", deviceKey: "bound", mode: "auto" } };
    mocks.call.mockResolvedValueOnce(diagnostics);
    await expect(fleetPathApi.diagnostics("dev-1")).resolves.toEqual(diagnostics);
    expect(mocks.call).toHaveBeenLastCalledWith("fleet_device_p2p_diagnostics", { deviceId: "dev-1" });
  });

  it("delivers well-formed path events and drops malformed payloads", async () => {
    let deliver: (payload: unknown) => void = () => undefined;
    mocks.listenEvent.mockImplementation((_topic: string, cb: (payload: unknown) => void) => {
      deliver = cb;
      return Promise.resolve(() => undefined);
    });
    const handler = vi.fn();
    await fleetPathApi.onPathChange(handler);
    expect(mocks.listenEvent).toHaveBeenCalledWith(DEVICE_PATH_EVENT, expect.any(Function));
    expect(DEVICE_PATH_EVENT).toBe("fleet://device-path");

    deliver(status);
    deliver(null);
    deliver({ deviceId: 3, path: "direct" });
    deliver({ deviceId: "dev-1" });
    expect(handler).toHaveBeenCalledTimes(1);
    expect(handler).toHaveBeenCalledWith(status);
  });
});
