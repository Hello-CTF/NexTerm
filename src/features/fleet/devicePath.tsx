// Connection path of managed devices: quiet by default. The current path only appears as one tooltip line on the
// device's connection, its tabs and its asset tree row, and in the device detail diagnostics; switches never notify.
// The per-device connection mode is a radio submenu in those right-click menus. Browser mode always relays, so the
// whole surface is hidden there and nothing is requested.

import { useEffect } from "react";
import { create } from "zustand";
import { useUi, workspaceConnectionState, type Workspace } from "../../app/store";
import { WEB } from "../../ipc/env";
import { fleetPathApi, type DeviceConnectionMode, type DevicePath, type DevicePathStatus } from "../../ipc/fleetPathApi";
import type { UnlistenFn } from "../../ipc/events";
import type { MenuItem } from "../../ui/ContextMenu";
import { describeError } from "../../ui/errorText";
import { IconCheck } from "../../ui/icons";
import { deviceIdOfHost } from "../hosts/hostDetail";
import { t } from "../../i18n";

interface DevicePathState {
  statuses: Record<string, DevicePathStatus>;
  modes: Record<string, DeviceConnectionMode>;
}

export const useDevicePathStore = create<DevicePathState>(() => ({ statuses: {}, modes: {} }));

const watchers = new Map<string, number>();
const statusRequests = new Set<string>();
const modeRequests = new Set<string>();
let unlisten: UnlistenFn | null = null;
let listening: Promise<void> | null = null;

function acceptStatus(status: DevicePathStatus): void {
  if (!watchers.has(status.deviceId)) return;
  useDevicePathStore.setState((state) => {
    const current = state.statuses[status.deviceId];
    // A status read that resolves after a newer event must not roll the path back.
    if (current && current.since > status.since) return state;
    return {
      statuses: { ...state.statuses, [status.deviceId]: status },
      modes: { ...state.modes, [status.deviceId]: status.mode },
    };
  });
}

function startListening(): void {
  if (listening) return;
  listening = fleetPathApi
    .onPathChange(acceptStatus)
    .then((off) => {
      if (watchers.size === 0) {
        off();
        listening = null;
      } else unlisten = off;
    })
    .catch(() => {
      listening = null;
    });
}

function stopListening(): void {
  if (!unlisten) return;
  unlisten();
  unlisten = null;
  listening = null;
}

function fetchStatus(deviceId: string): void {
  if (statusRequests.has(deviceId)) return;
  statusRequests.add(deviceId);
  // A failed read leaves the tooltip without a path line; the path is informational, so it is not reported.
  void fleetPathApi
    .status(deviceId)
    .then(acceptStatus, () => undefined)
    .finally(() => statusRequests.delete(deviceId));
}

function watchDevice(deviceId: string): () => void {
  watchers.set(deviceId, (watchers.get(deviceId) ?? 0) + 1);
  startListening();
  if (!useDevicePathStore.getState().statuses[deviceId]) fetchStatus(deviceId);
  return () => {
    const count = (watchers.get(deviceId) ?? 1) - 1;
    if (count > 0) {
      watchers.set(deviceId, count);
      return;
    }
    watchers.delete(deviceId);
    // A device no open workspace shows is read again when it is shown again, instead of keeping a stale path.
    useDevicePathStore.setState((state) => {
      const { [deviceId]: _removed, ...statuses } = state.statuses;
      return { statuses };
    });
    if (watchers.size === 0) stopListening();
  };
}

// useDevicePathStatus keeps the path status of one device live while a caller shows it.
export function useDevicePathStatus(deviceId: string | null): DevicePathStatus | undefined {
  const active = !WEB && deviceId ? deviceId : null;
  useEffect(() => (active ? watchDevice(active) : undefined), [active]);
  return useDevicePathStore((state) => (active ? state.statuses[active] : undefined));
}

// deviceIdOfWorkspace names the managed device behind an open session workspace, or null for any other workspace.
export function deviceIdOfWorkspace(workspace: Pick<Workspace, "kind" | "assetId"> | undefined): string | null {
  if (WEB || !workspace || workspace.kind !== "session" || !workspace.assetId) return null;
  return deviceIdOfHost(workspace.assetId);
}

// useDevicePathNote is the tooltip line of an open device workspace; null when the workspace is not a device
// connection, is not connected, or its path is not known yet.
export function useDevicePathNote(workspace: Workspace | undefined): string | null {
  const deviceId = deviceIdOfWorkspace(workspace);
  const connected = useUi((state) => workspaceConnectionState(workspace, state.sessions) === "connected");
  const status = useDevicePathStatus(connected ? deviceId : null);
  return status ? devicePathTooltip(status) : null;
}

// Reasons that keep a device on the relay whatever the network does; the tooltip names them even in automatic mode.
const POLICY_REASONS = new Set(["server_disabled", "device_unsupported", "key_pending", "browser_or_share", "proxy"]);

export function devicePathReasonText(reason: string): string {
  switch (reason) {
    case "mode_relay":
      return t("连接方式为中转");
    case "server_disabled":
      return t("管理员已禁用直连");
    case "device_unsupported":
      return t("设备代理版本不支持直连");
    case "key_pending":
      return t("设备密钥已变化，待确认");
    case "browser_or_share":
      return t("浏览器和分享连接只经中转");
    case "nat_blocked":
      return t("当前网络无法直连");
    case "connect_failed":
      return t("直连建立失败");
    case "unstable":
      return t("直连不稳定");
    case "background":
      return t("应用在后台");
    case "proxy":
      return t("经代理连接服务器");
    case "offline":
      return t("设备离线");
    default:
      return "";
  }
}

// devicePathTooltip is the single tooltip line for a device connection. A direct connection that is still being
// set up reads as relayed: streams use the relay until it is ready, and the diagnostics show the attempt.
export function devicePathTooltip(status: DevicePathStatus): string | null {
  if (status.reason === "offline") return null;
  if (status.path === "direct") return t("直连");
  const reason = devicePathReasonText(status.reason);
  if (status.mode === "direct") {
    if (status.path === "connecting_direct") return t("正在建立直连，当前经中转");
    return reason ? t("直连不可用，当前经中转（{reason}）", { reason }) : t("直连不可用，当前经中转");
  }
  if (reason && POLICY_REASONS.has(status.reason)) return t("经中转（{reason}）", { reason });
  return t("经中转");
}

export function devicePathLabel(path: DevicePath | string): string {
  if (path === "direct") return t("直连");
  if (path === "connecting_direct") return t("直连建立中");
  return t("中转");
}

export function connectionModeLabel(mode: DeviceConnectionMode | string): string {
  if (mode === "relay") return t("中转");
  if (mode === "direct") return t("直连");
  return t("自动");
}

// ensureConnectionMode reads a device's stored mode for a menu about to show it; a failed read leaves it unknown.
export function ensureConnectionMode(deviceId: string): void {
  if (WEB || useDevicePathStore.getState().modes[deviceId] || modeRequests.has(deviceId)) return;
  modeRequests.add(deviceId);
  void fleetPathApi
    .getMode(deviceId)
    .then(
      ({ mode }) => useDevicePathStore.setState((state) => ({ modes: { ...state.modes, [deviceId]: mode } })),
      () => undefined,
    )
    .finally(() => modeRequests.delete(deviceId));
}

export async function setConnectionMode(deviceId: string, mode: DeviceConnectionMode): Promise<void> {
  try {
    const result = await fleetPathApi.setMode(deviceId, mode);
    const stored = result?.mode ?? mode;
    useDevicePathStore.setState((state) => {
      const status = state.statuses[deviceId];
      return {
        modes: { ...state.modes, [deviceId]: stored },
        statuses: status ? { ...state.statuses, [deviceId]: { ...status, mode: stored } } : state.statuses,
      };
    });
  } catch (error) {
    useUi.getState().pushToast("error", t("设置连接方式失败：{message}", { message: describeError(error) }));
  }
}

const MODES: DeviceConnectionMode[] = ["auto", "relay", "direct"];

// connectionModeMenu is the "连接方式" submenu of a device connection, or an empty list in browser mode. The
// selected mode is the one stored for the device; while it is unknown no item is checked.
export function connectionModeMenu(deviceId: string, iconSize = 12): MenuItem[] {
  if (WEB) return [];
  ensureConnectionMode(deviceId);
  const current = useDevicePathStore.getState().modes[deviceId];
  const hints: Record<DeviceConnectionMode, string> = {
    auto: t("能直连时自动切换"),
    relay: t("不尝试直连"),
    direct: t("不可用时经中转"),
  };
  return [
    {
      kind: "item",
      label: t("连接方式"),
      submenu: MODES.map((mode): MenuItem => ({
        kind: "item",
        label: connectionModeLabel(mode),
        hint: hints[mode],
        checked: current === mode,
        icon: current === mode ? <IconCheck size={iconSize} /> : undefined,
        onSelect: () => {
          if (mode !== current) void setConnectionMode(deviceId, mode);
        },
      })),
    },
  ];
}

export function resetDevicePathForTest(): void {
  stopListening();
  watchers.clear();
  statusRequests.clear();
  modeRequests.clear();
  listening = null;
  useDevicePathStore.setState({ statuses: {}, modes: {} });
}
