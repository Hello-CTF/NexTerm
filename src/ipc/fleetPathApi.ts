// Connection path of managed devices (relay or direct P2P) on the desktop and mobile Go core. Browser mode always
// relays, so callers hide the whole surface there and never call this module.

import { call } from "./commands";
import { EVENTS, listenEvent, type UnlistenFn } from "./events";
import type {
  DeviceP2PDiagnostics as GeneratedDiagnostics,
  DevicePathStatus as GeneratedStatus,
  P2PPolicy,
  P2PSelectedPair,
  PathSwitch as GeneratedSwitch,
} from "./generated/types";

export type DeviceConnectionMode = "auto" | "relay" | "direct";
export type DevicePath = "relay" | "direct" | "connecting_direct";
// Why the path is not direct; "" when it is. Unknown codes from a newer core are kept as strings.
export type DevicePathReason =
  | "mode_relay"
  | "server_disabled"
  | "device_unsupported"
  | "key_pending"
  | "browser_or_share"
  | "nat_blocked"
  | "connect_failed"
  | "unstable"
  | "background"
  | "proxy"
  | "insecure"
  | "offline"
  | "";

export type DevicePathStatus = Omit<GeneratedStatus, "mode" | "path" | "reason"> & {
  mode: DeviceConnectionMode;
  path: DevicePath;
  reason: string;
};

export type DevicePathSwitch = Omit<GeneratedSwitch, "from" | "to"> & { from: DevicePath; to: DevicePath };

export type DeviceP2PDiagnostics = Omit<GeneratedDiagnostics, "status" | "recentSwitches"> & {
  status: DevicePathStatus;
  recentSwitches: DevicePathSwitch[];
};

export type { P2PPolicy, P2PSelectedPair };

export const DEVICE_PATH_EVENT = EVENTS.devicePath;

export const fleetPathApi = {
  getMode: async (deviceId: string) =>
    (await call("fleet_device_connection_mode_get", { deviceId })) as { mode: DeviceConnectionMode },
  setMode: async (deviceId: string, mode: DeviceConnectionMode) =>
    (await call("fleet_device_connection_mode_set", { deviceId, mode })) as { mode: DeviceConnectionMode },
  status: async (deviceId: string) => (await call("fleet_device_path_status", { deviceId })) as DevicePathStatus,
  diagnostics: async (deviceId: string) =>
    (await call("fleet_device_p2p_diagnostics", { deviceId })) as DeviceP2PDiagnostics,
  onPathChange: (handler: (status: DevicePathStatus) => void): Promise<UnlistenFn> =>
    listenEvent<DevicePathStatus>(DEVICE_PATH_EVENT, (payload) => {
      if (payload && typeof payload.deviceId === "string" && typeof payload.path === "string") handler(payload);
    }),
};
