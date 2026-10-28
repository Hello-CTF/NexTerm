// Asset tree device rows: mounts server-enrolled devices into the asset tree as plain rows, with no section chrome of any kind -
// no header row, no count, no refresh button, and no placeholder hints. While the device list is loading, when it failed to load,
// when no devices are enrolled, the component renders null. Offline and agent-less enrolled devices remain visible but disabled;
// management (enrollment, manual refresh, error details) lives in the device management view.
// Devices are not asset table records: they are excluded from asset search/CRUD/sync, cannot be edited or deleted,
// cannot be dragged into groups, and there is no cross-device drag; row visuals and keyboard/click behavior match regular assets
// (role=treeitem, single/double click/Enter or the play button opens the device workspace).
// Renders nothing and issues no requests when logged out. Revoked devices are not listed at all (the device management view
// reveals them on demand); offline and agent-less devices remain visible with a status badge, disabled for terminal access.
// Revoking from the context menu removes the row immediately. Revocation requires two warning confirmations.
// Convergence cadence matches the device management view: WEB polls every 60s / desktop every 30s as fallback (coming online/newly enrolled/
// revoked elsewhere disappear on refresh), WEB additionally subscribes to device://status for realtime online flips
// (events win within the event threshold, otherwise heartbeat determination), and refetches immediately on event WS reconnect resync;
// the desktop has no event channel and relies on polling alone.

import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { connectDevice, deviceAssetId, useUi } from "../../app/store";
import { openHostDetail } from "../hosts/hostDetail";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { ContextMenu, type MenuItem } from "../../ui/ContextMenu";
import { IconInfo, IconMonitor, IconPlay, IconTerminal, IconTrash } from "../../ui/icons";
import type { FleetDevice } from "../../ipc/fleetApi";
import { WEB } from "../../ipc/env";
import { EVENTS, listenEvent, type DeviceStatusEvent } from "../../ipc/events";
import { onEventsResync } from "../../ipc/webTransport";
import { fleetClient } from "./clients";
import { useFleetAccount } from "./useFleetAccount";
import { isOnline, ONLINE_THRESHOLD_MS } from "./format";

function deviceRowKeyDown(event: React.KeyboardEvent<HTMLElement>): void {
  if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
  const tree = event.currentTarget.closest("[role='tree']");
  if (!tree) return;
  const rows = [...tree.querySelectorAll<HTMLElement>("[role='treeitem']")];
  const index = rows.indexOf(event.currentTarget);
  const next = event.key === "ArrowDown" ? index + 1 : index - 1;
  if (index < 0 || next < 0 || next >= rows.length) return;
  event.preventDefault();
  rows[next]?.focus();
}

function openDevicesTab(): void {
  useUi.getState().addTab({ id: "devices", kind: "devices", title: "设备管理", closable: true });
}

export function DeviceTreeSection() {
  const qc = useQueryClient();
  const pushToast = useUi((s) => s.pushToast);
  const account = useFleetAccount();
  const userId = account.userId;
  const [menu, setMenu] = useState<{ x: number; y: number; device: FleetDevice } | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  // presence is the realtime signal from device://status events (device ID -> online state + receive time),
  // falling back to the heartbeat determination past the heartbeat threshold, same semantics as the device management view; always empty on desktop (no event channel).
  const [presence, setPresence] = useState<Record<string, { online: boolean; at: number }>>({});

  const devicesQuery = useQuery({
    queryKey: ["fleet-devices", userId],
    queryFn: () => fleetClient().devices(),
    enabled: Boolean(userId),
    refetchOnWindowFocus: false,
    // Same cadence as the device management view: WEB stays realtime via the event stream with 60s polling fallback; desktop has no event channel, 30s.
    refetchInterval: WEB ? 60_000 : 30_000,
  });

  useEffect(() => {
    if (!userId) return;
    const timer = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(timer);
  }, [userId]);

  useEffect(() => {
    setPresence({});
  }, [userId]);

  // device://status realtime online/offline: only writes presence; badges and row disabled states update on render;
  // IDs not in the current account's device list (other users' devices) are never rendered and are naturally ignored. WEB only.
  useEffect(() => {
    if (!WEB || !userId) return;
    let cancelled = false;
    let unlisten: (() => void) | undefined;
    void listenEvent<DeviceStatusEvent>(EVENTS.deviceStatus, (p) => {
      if (!p || typeof p.deviceId !== "string" || typeof p.online !== "boolean") return;
      setPresence((prev) => ({ ...prev, [p.deviceId]: { online: p.online, at: Date.now() } }));
    }).then((off) => {
      if (cancelled) off();
      else unlisten = off;
    });
    return () => {
      cancelled = true;
      unlisten?.();
    };
  }, [userId]);

  // Refetch immediately when the event WS reconnects and may have missed events (resync), without waiting for the next poll round.
  useEffect(() => {
    if (!WEB || !userId) return;
    return onEventsResync(() => {
      void qc.invalidateQueries({ queryKey: ["fleet-devices", userId] });
    });
  }, [qc, userId]);

  if (!userId) return null;

  // Effective online = realtime event first (within the heartbeat threshold), otherwise fall back to the heartbeat determination; non-agent devices are always offline.
  const onlineOf = (d: FleetDevice): boolean => {
    if (d.revoked_at !== 0 || !d.agent) return false;
    const p = presence[d.id];
    if (p && now - p.at <= ONLINE_THRESHOLD_MS) return p.online;
    return isOnline(d.agent.last_seen_at || d.last_seen_at, now);
  };

  const markRevoked = (id: string) => {
    qc.setQueryData<{ devices: FleetDevice[] }>(["fleet-devices", userId], (old) =>
      old ? { devices: old.devices.map((device) => device.id === id ? { ...device, revoked_at: Date.now() } : device) } : old,
    );
  };

  const revoke = async (device: FleetDevice) => {
    if (device.revoked_at !== 0 || busyId === device.id) return;
    const first = await ask(`吊销设备「${device.name}」？\n\n吊销后设备凭证立即失效、控制通道断开。`, {
      title: "吊销设备",
      kind: "warning",
    });
    if (!first) return;
    const second = await ask(
      `再次确认吊销「${device.name}」？\n\n吊销后设备凭证立即失效，设备仍显示为已吊销但无法连接，凭证不可恢复。`,
      { title: "确认吊销", kind: "warning" },
    );
    if (!second) return;
    setBusyId(device.id);
    try {
      await fleetClient().revoke(device.id);
      pushToast("success", `已吊销「${device.name}」`);
      markRevoked(device.id);
    } catch (e) {
      pushToast("error", describeError(e));
    } finally {
      setBusyId(null);
    }
  };

  const buildMenu = (device: FleetDevice, disabledReason: string | null): MenuItem[] => {
    const items: MenuItem[] = [];
    items.push({
      kind: "item",
      label: "打开工作区",
      icon: <IconTerminal size={13} />,
      disabled: disabledReason !== null,
      hint: disabledReason ?? undefined,
      onSelect: () => {
        if (disabledReason === null) void connectDevice(device);
      },
    });
    items.push(
      { kind: "item", label: "主机详情", icon: <IconInfo size={13} />, hint: "会话、容器和指标", onSelect: () => openHostDetail(deviceAssetId(device.id)) },
      { kind: "item", label: "设备管理", icon: <IconMonitor size={13} />, hint: "接入、自动启动与撤销", onSelect: openDevicesTab },
    );
    if (device.revoked_at === 0) items.push(
      { kind: "separator" },
      {
        kind: "item",
        label: "吊销",
        icon: <IconTrash size={13} />,
        danger: true,
        disabled: busyId === device.id,
        hint: "凭证立即失效",
        onSelect: () => void revoke(device),
      },
    );
    return items;
  };

  // Revoked devices are never listed in the asset tree; they stay reachable only from the device management view.
  const devices = (devicesQuery.data?.devices ?? []).filter((device) => device.revoked_at === 0);
  const menuDevice = menu ? devices.find((device) => device.id === menu.device.id) : undefined;
  if (devices.length === 0) return null;

  return (
    <>
      {devices.map((device) => (
        <DeviceTreeRow
          key={device.id}
          device={device}
          online={onlineOf(device)}
          busy={busyId === device.id}
          onMenu={(x, y) => setMenu({ x, y, device })}
        />
      ))}

      <ContextMenu
        state={
          menu && menuDevice
            ? {
                x: menu.x,
                y: menu.y,
                title: menuDevice.name,
                items: buildMenu(menuDevice, deviceOpenBlockReason(menuDevice, onlineOf(menuDevice))),
              }
            : null
        }
        onClose={() => setMenu(null)}
      />
    </>
  );
}

export function deviceOpenBlockReason(device: FleetDevice, online: boolean): string | null {
  if (device.revoked_at !== 0) return "设备已吊销，无法打开工作区";
  if (!device.agent) return "该设备尚未接入 Agent";
  if (!online) return "设备离线，无法打开工作区";
  if (!device.agent.terminal_enabled) return "该设备已关闭远程终端";
  return null;
}

function DeviceTreeRow({
  device,
  online,
  busy,
  onMenu,
}: {
  device: FleetDevice;
  online: boolean;
  busy: boolean;
  onMenu: (x: number, y: number) => void;
}) {
  const agent = device.agent;
  const disabledReason = deviceOpenBlockReason(device, online);
  const connectable = disabledReason === null;
  const status = device.revoked_at !== 0 ? "已吊销" : !agent ? "未接入 Agent" : !online ? "离线" : null;
  const longPressTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const longPressStart = useRef<{ x: number; y: number } | null>(null);
  const suppressClick = useRef(false);

  const cancelLongPress = () => {
    if (longPressTimer.current !== null) clearTimeout(longPressTimer.current);
    longPressTimer.current = null;
    longPressStart.current = null;
  };

  useEffect(() => cancelLongPress, []);

  const open = () => {
    if (!connectable || busy) return;
    void connectDevice(device);
  };

  const tip = [
    device.name,
    agent?.platform || "",
    disabledReason ?? "单击打开工作区 · 右键管理",
  ]
    .filter(Boolean)
    .join(" · ");

  return (
    <>
      <div
        role="treeitem"
        aria-level={1}
        aria-disabled={!connectable}
        tabIndex={0}
        className={`nx-row nx-row-oneline nx-row-reserve-actions ${connectable ? "" : "cursor-not-allowed opacity-55"}`}
        onClick={(e) => {
          if (suppressClick.current) {
            e.preventDefault();
            suppressClick.current = false;
            return;
          }
          open();
        }}
        onDoubleClick={open}
        onPointerDown={(e) => {
          suppressClick.current = false;
          if ((e.pointerType !== "touch" && e.pointerType !== "pen") || (e.target as HTMLElement).closest("button")) return;
          cancelLongPress();
          longPressStart.current = { x: e.clientX, y: e.clientY };
          longPressTimer.current = setTimeout(() => {
            longPressTimer.current = null;
            suppressClick.current = true;
            onMenu(e.clientX, e.clientY);
          }, 500);
        }}
        onPointerMove={(e) => {
          const start = longPressStart.current;
          if (start && (Math.abs(e.clientX - start.x) > 10 || Math.abs(e.clientY - start.y) > 10)) cancelLongPress();
        }}
        onPointerUp={cancelLongPress}
        onPointerCancel={cancelLongPress}
        onPointerLeave={cancelLongPress}
        onContextMenu={(e) => {
          e.preventDefault();
          const touchPress = longPressStart.current !== null;
          cancelLongPress();
          if (suppressClick.current) return;
          if (touchPress) suppressClick.current = true;
          onMenu(e.clientX, e.clientY);
        }}
        onKeyDown={(e) => {
          if (e.target !== e.currentTarget) return;
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            open();
            return;
          }
          if (e.key === "ContextMenu" || (e.key === "F10" && e.shiftKey)) {
            e.preventDefault();
            const rect = e.currentTarget.getBoundingClientRect();
            onMenu(rect.left, rect.bottom);
            return;
          }
          deviceRowKeyDown(e);
        }}
        title={tip}
      >
        <IconMonitor size={14} className="shrink-0 text-neutral-500" />
        <span className="nx-row-name min-w-0 flex-auto truncate">{device.name}</span>
        {status && <span className="nx-badge shrink-0">{status}</span>}
        {agent?.platform && <span className="nx-row-target min-w-0 truncate text-[11px] text-neutral-500">{agent.platform}</span>}
        <span className="nx-row-actions" onClick={(e) => e.stopPropagation()} onDoubleClick={(e) => e.stopPropagation()}>
          <button
            className="nx-icon-btn nx-icon-btn-sm"
            title={disabledReason ?? "打开工作区"}
            aria-label={`打开工作区 ${device.name}`}
            disabled={!connectable || busy}
            onClick={open}
          >
            <IconPlay size={12} />
          </button>
        </span>
      </div>
    </>
  );
}
