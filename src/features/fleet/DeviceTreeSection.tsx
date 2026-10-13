// Asset tree "devices" section: mounts server-enrolled devices into the asset tree as a read-only section.
// Devices are not asset table records: they are excluded from asset search/CRUD/sync, cannot be edited or deleted,
// cannot be dragged into groups, and there is no cross-device drag; row visuals and keyboard/click behavior match regular assets
// (role=treeitem, single/double click/Enter opens a remote terminal, no separate "terminal" button).
// Renders nothing and issues no requests when logged out; revoked devices are not shown (not even grayed out). Revocation requires two
// warning confirmations (the second explicitly states "credentials become invalid immediately, removed from assets and unrecoverable"),
// and the device is removed from the section immediately on success.
// Convergence cadence matches the device management view: WEB polls every 60s / desktop every 30s as fallback (coming online/newly enrolled/
// revoked elsewhere converge on refresh), WEB additionally subscribes to device://status for realtime online flips
// (events win within the event threshold, otherwise heartbeat determination), and refetches immediately on event WS reconnect resync;
// the desktop has no event channel and relies on polling alone.

import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { connectDevice, useUi } from "../../app/store";
import { useCoarsePointer } from "../../app/platform";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { ContextMenu, type MenuItem } from "../../ui/ContextMenu";
import {
  IconInfo,
  IconMonitor,
  IconRefresh,
  IconTerminal,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";
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
  const { pushToast } = useUi();
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
    if (!d.agent) return false;
    const p = presence[d.id];
    if (p && now - p.at <= ONLINE_THRESHOLD_MS) return p.online;
    return isOnline(d.agent.last_seen_at || d.last_seen_at, now);
  };

  const removeDevice = (id: string) => {
    qc.setQueryData<{ devices: FleetDevice[] }>(["fleet-devices", userId], (old) =>
      old ? { devices: old.devices.filter((d) => d.id !== id) } : old,
    );
  };

  const revoke = async (device: FleetDevice) => {
    const first = await ask(`吊销设备「${device.name}」？\n\n吊销后设备凭证立即失效、控制通道断开。`, {
      title: "吊销设备",
      kind: "warning",
    });
    if (!first) return;
    const second = await ask(
      `再次确认吊销「${device.name}」？\n\n吊销后设备凭证立即失效，设备会从资产中移除且不可恢复。`,
      { title: "确认吊销", kind: "warning" },
    );
    if (!second) return;
    setBusyId(device.id);
    try {
      await fleetClient().revoke(device.id);
      pushToast("success", `已吊销「${device.name}」`);
      removeDevice(device.id);
    } catch (e) {
      pushToast("error", describeError(e));
    } finally {
      setBusyId(null);
    }
  };

  const buildMenu = (device: FleetDevice, connectable: boolean, disabledReason: string | null): MenuItem[] => {
    const items: MenuItem[] = [];
    items.push({
      kind: "item",
      label: "打开终端",
      icon: <IconTerminal size={13} />,
      disabled: !connectable,
      hint: disabledReason ?? undefined,
      onSelect: () => void connectDevice(device),
    });
    items.push(
      { kind: "item", label: "详情", icon: <IconInfo size={13} />, hint: "打开设备管理视图", onSelect: openDevicesTab },
      { kind: "separator" },
      {
        kind: "item",
        label: "吊销",
        icon: <IconTrash size={13} />,
        danger: true,
        hint: "凭证立即失效，从资产中移除且不可恢复",
        onSelect: () => void revoke(device),
      },
    );
    return items;
  };

  const devices = (devicesQuery.data?.devices ?? []).filter((d) => d.revoked_at === 0);

  return (
    <div role="group" aria-label="设备" className="mb-0.5">
      <div className="nx-row cursor-default text-neutral-400">
        <IconMonitor size={13} className="shrink-0 text-neutral-500" />
        <span className="nx-row-name min-w-0 flex-1 truncate text-xs font-medium">设备</span>
        <span className="nx-count">{devices.length}</span>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="刷新设备列表"
          aria-label="刷新设备列表"
          disabled={devicesQuery.isFetching}
          onClick={() => void devicesQuery.refetch()}
        >
          <IconRefresh size={12} className={devicesQuery.isFetching ? "animate-spin" : ""} />
        </button>
      </div>

      {devicesQuery.isPending ? (
        <div className="nx-hint px-2 py-1.5 text-[11px]">设备列表加载中…</div>
      ) : devicesQuery.isError ? (
        <div className="mx-1 mb-1 flex items-center gap-2 rounded bg-red-500/10 px-2 py-1.5 text-[11.5px] text-red-400">
          <IconXCircle size={12} className="shrink-0" />
          <span className="min-w-0 flex-1 truncate" title={describeError(devicesQuery.error)}>
            设备列表加载失败
          </span>
          <button className="nx-link shrink-0" onClick={() => void devicesQuery.refetch()}>
            重试
          </button>
        </div>
      ) : devices.length === 0 ? (
        <div className="nx-hint px-2 py-1.5 text-[11px]">还没有接入设备，在「设备管理」里接入。</div>
      ) : (
        devices.map((device) => (
          <DeviceTreeRow
            key={device.id}
            device={device}
            online={onlineOf(device)}
            busy={busyId === device.id}
            onMenu={(x, y) => setMenu({ x, y, device })}
            onRevoke={() => void revoke(device)}
          />
        ))
      )}

      <ContextMenu
        state={
          menu
            ? {
                x: menu.x,
                y: menu.y,
                title: menu.device.name,
                items: buildMenu(
                  menu.device,
                  disabledReasonOf(menu.device, onlineOf(menu.device)) === null,
                  disabledReasonOf(menu.device, onlineOf(menu.device)),
                ),
              }
            : null
        }
        onClose={() => setMenu(null)}
      />
    </div>
  );
}

function disabledReasonOf(device: FleetDevice, online: boolean): string | null {
  if (!device.agent) return "该设备未安装设备代理，无法打开终端";
  if (!device.agent.terminal_enabled) return "该设备已关闭远程终端";
  if (!online) return "设备离线，无法打开终端";
  return null;
}

function DeviceTreeRow({
  device,
  online,
  busy,
  onMenu,
  onRevoke,
}: {
  device: FleetDevice;
  online: boolean;
  busy: boolean;
  onMenu: (x: number, y: number) => void;
  onRevoke: () => void;
}) {
  const coarse = useCoarsePointer();
  const agent = device.agent;
  const disabledReason = disabledReasonOf(device, online);
  const connectable = disabledReason === null;

  const open = () => {
    if (!connectable || busy) return;
    void connectDevice(device);
  };

  const tip = [
    device.name,
    agent?.platform || "",
    disabledReason ?? "单击打开远程终端 · 右键管理",
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
        className={`nx-row nx-row-reserve-actions ${connectable ? "" : "cursor-not-allowed opacity-55"}`}
        onClick={open}
        onDoubleClick={open}
        onContextMenu={(e) => {
          e.preventDefault();
          onMenu(e.clientX, e.clientY);
        }}
        onKeyDown={(e) => {
          if (e.target !== e.currentTarget) return;
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            open();
            return;
          }
          deviceRowKeyDown(e);
        }}
        title={tip}
      >
        <IconMonitor size={14} className="shrink-0 text-neutral-500" />
        <span className="nx-row-name min-w-0 flex-auto truncate">{device.name}</span>
        {agent?.platform && <span className="nx-row-target min-w-0 truncate text-[11px] text-neutral-500">{agent.platform}</span>}
        {agent ? (
          <span className={`nx-badge ${online ? "nx-badge-green" : ""}`}>{online ? "在线" : "离线"}</span>
        ) : (
          <span className="nx-badge">无代理</span>
        )}
        {coarse ? (
          <button
            className="nx-icon-btn nx-icon-btn-sm nx-row-more"
            title="更多操作"
            aria-label={`更多操作 ${device.name}`}
            onClick={(e) => {
              e.stopPropagation();
              const r = e.currentTarget.getBoundingClientRect();
              onMenu(r.left, r.bottom);
            }}
            onDoubleClick={(e) => e.stopPropagation()}
          >
            ⋯
          </button>
        ) : (
          <span className="nx-row-actions" onClick={(e) => e.stopPropagation()} onDoubleClick={(e) => e.stopPropagation()}>
            <button
              className="nx-icon-btn nx-icon-btn-sm"
              title="详情（打开设备管理）"
              aria-label={`详情 ${device.name}`}
              onClick={openDevicesTab}
            >
              <IconInfo size={12} />
            </button>
            <button
              className="nx-icon-btn nx-icon-btn-sm is-danger"
              title="吊销"
              aria-label={`吊销 ${device.name}`}
              disabled={busy}
              onClick={onRevoke}
            >
              <IconTrash size={12} />
            </button>
            <button
              className="nx-icon-btn nx-icon-btn-sm"
              title="更多操作"
              aria-label={`更多操作 ${device.name}`}
              onClick={(e) => {
                const r = e.currentTarget.getBoundingClientRect();
                onMenu(r.left, r.bottom);
              }}
            >
              ⋯
            </button>
          </span>
        )}
      </div>
    </>
  );
}
