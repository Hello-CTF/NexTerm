// Connection path of managed devices (relay or direct P2P) on the desktop and mobile Go core. Browser mode always
// relays and has none of these commands, so callers hide the whole surface there and never call this module.

import { call } from "./commands";
import { listenEvent, type UnlistenFn } from "./events";

export type DeviceConnectionMode = "auto" | "relay" | "direct";

export type DevicePath = "relay" | "direct" | "connecting_direct";

export interface DevicePathStatus {
  deviceId: string;
  mode: DeviceConnectionMode;
  // What live streams use now.
  path: DevicePath;
  // Why the path is not direct, when it is not: mode_relay, server_disabled, device_unsupported, key_pending,
  // browser_or_share, nat_blocked, connect_failed, unstable, background, proxy, offline or "".
  reason: string;
  // Epoch ms of the last path change.
  since: number;
}

export interface DevicePathSwitch {
  at: number;
  from: DevicePath;
  to: DevicePath;
  reason: string;
}

export interface DeviceP2PDiagnostics {
  status: DevicePathStatus;
  // independent | dependent | blocked | unknown
  localNat: string;
  remoteNat: string;
  stunReachable: boolean;
  selectedPair?: { local: string; remote: string; type: string };
  directRttMs?: number;
  relayRttMs?: number;
  lossPct?: number;
  recentSwitches: DevicePathSwitch[];
  policy: { server: boolean; device: boolean; mode: string };
}

export const DEVICE_PATH_EVENT = "fleet://device-path";

// The commands and the event are not in the generated IPC contract yet; once it is regenerated, switch these to the
// typed call() and EVENTS.devicePath.
function untypedCall<T>(cmd: string, args: Record<string, unknown>): Promise<T> {
  return (call as unknown as (name: string, input: Record<string, unknown>) => Promise<T>)(cmd, args);
}

export const fleetPathApi = {
  getMode: (deviceId: string) =>
    untypedCall<{ mode: DeviceConnectionMode }>("fleet_device_connection_mode_get", { deviceId }),
  setMode: (deviceId: string, mode: DeviceConnectionMode) =>
    untypedCall<{ mode: DeviceConnectionMode }>("fleet_device_connection_mode_set", { deviceId, mode }),
  status: (deviceId: string) => untypedCall<DevicePathStatus>("fleet_device_path_status", { deviceId }),
  diagnostics: (deviceId: string) => untypedCall<DeviceP2PDiagnostics>("fleet_device_p2p_diagnostics", { deviceId }),
  onPathChange: (handler: (status: DevicePathStatus) => void): Promise<UnlistenFn> =>
    listenEvent<DevicePathStatus>(DEVICE_PATH_EVENT, (payload) => {
      if (payload && typeof payload.deviceId === "string" && typeof payload.path === "string") handler(payload);
    }),
};
