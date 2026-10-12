// 资产树「设备」分区: 把服务端接入设备以只读分区形式挂进资产树。
// 设备不是 asset 表记录: 不参与资产搜索/CRUD/同步, 不可编辑、不可删除、
// 不可拖入分组, 也没有跨设备拖拽; 行的视觉与键盘/点击行为对齐普通资产
// (role=treeitem, 单击/双击/Enter 打开远程终端, 不再有单独的「终端」按钮)。
// 未登录不渲染且不发任何请求; 已吊销设备不显示 (不是灰显)。吊销需两个
// warning 确认 (第二个明确「凭证立即失效, 从资产中移除且不可恢复」),
// 成功后立即把设备从分区移除。待审批的批准/拒绝与设备管理视图同一确认语义
// (批准直接执行, 拒绝需一次 warning 确认)。
// 收敛节奏与设备管理视图一致: WEB 每 60s / 桌面每 30s 轮询兜底 (上线/新接入/
// 他处吊销随刷新收敛), WEB 另订阅 device://status 实时翻转在线状态
// (事件阈值内以事件为准, 否则回落心跳判定), 事件 WS 重连 resync 时立即重取;
// 桌面无事件通道, 只依赖轮询。

import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { openDeviceTerminalTab, useUi } from "../../app/store";
import { useCoarsePointer } from "../../app/platform";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { ContextMenu, type MenuItem } from "../../ui/ContextMenu";
import {
  IconActivity,
  IconCheckCircle,
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
import { MetricsPanel } from "./MetricsPanel";

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
  const isAdmin = account.isAdmin;
  const [menu, setMenu] = useState<{ x: number; y: number; device: FleetDevice } | null>(null);
  const [metricsOpenId, setMetricsOpenId] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  // presence 是 device://status 事件的实时信号 (设备 ID → 在线状态 + 收到时间),
  // 超过心跳阈值后回落到心跳判定, 与设备管理视图同语义; 桌面无事件通道, 恒为空。
  const [presence, setPresence] = useState<Record<string, { online: boolean; at: number }>>({});

  const devicesQuery = useQuery({
    queryKey: ["fleet-devices", userId],
    queryFn: () => fleetClient().devices(),
    enabled: Boolean(userId),
    refetchOnWindowFocus: false,
    // 与设备管理视图同节奏: WEB 靠事件流保持实时, 60s 轮询兜底; 桌面无事件通道, 30s。
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

  // device://status 实时上下线: 只写入 presence, 徽章与行禁用态随渲染更新;
  // 不在当前账号设备清单内的 ID (他人设备) 不会被渲染, 自然忽略。仅 WEB 有该通道。
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

  // 事件 WS 重连并错过事件 (resync) 时立即重取, 不等下一轮轮询。
  useEffect(() => {
    if (!WEB || !userId) return;
    return onEventsResync(() => {
      void qc.invalidateQueries({ queryKey: ["fleet-devices", userId] });
    });
  }, [qc, userId]);

  if (!userId) return null;

  // 有效在线 = 实时事件优先 (心跳阈值内), 否则回落心跳判定; 非 agent 设备恒离线。
  const onlineOf = (d: FleetDevice): boolean => {
    if (!d.agent) return false;
    const p = presence[d.id];
    if (p && now - p.at <= ONLINE_THRESHOLD_MS) return p.online;
    return isOnline(d.agent.last_seen_at || d.last_seen_at, now);
  };

  const patchDevice = (id: string, patch: Partial<FleetDevice>) => {
    qc.setQueryData<{ devices: FleetDevice[] }>(["fleet-devices", userId], (old) =>
      old ? { devices: old.devices.map((d) => (d.id === id ? { ...d, ...patch } : d)) } : old,
    );
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

  const approve = async (device: FleetDevice) => {
    setBusyId(device.id);
    try {
      await fleetClient().approve(device.id);
      pushToast("success", `已批准「${device.name}」，设备上线后可用`);
      patchDevice(device.id, { state: "active" });
    } catch (e) {
      pushToast("error", describeError(e));
    } finally {
      setBusyId(null);
    }
  };

  const reject = async (device: FleetDevice) => {
    const ok = await ask(`拒绝设备「${device.name}」的接入？\n\n拒绝后该设备立即被删除、凭证失效，需要新接入码才能重新接入。`, {
      title: "拒绝接入",
      kind: "warning",
    });
    if (!ok) return;
    setBusyId(device.id);
    try {
      await fleetClient().reject(device.id);
      pushToast("success", `已拒绝并删除「${device.name}」`);
      removeDevice(device.id);
    } catch (e) {
      pushToast("error", describeError(e));
    } finally {
      setBusyId(null);
    }
  };

  const buildMenu = (device: FleetDevice, pending: boolean, connectable: boolean, disabledReason: string | null): MenuItem[] => {
    const items: MenuItem[] = [];
    if (pending && isAdmin) {
      items.push(
        { kind: "item", label: "批准", icon: <IconCheckCircle size={13} />, onSelect: () => void approve(device) },
        { kind: "item", label: "拒绝", icon: <IconXCircle size={13} />, onSelect: () => void reject(device) },
        { kind: "separator" },
      );
    } else {
      items.push({
        kind: "item",
        label: "打开终端",
        icon: <IconTerminal size={13} />,
        disabled: !connectable,
        hint: disabledReason ?? undefined,
        onSelect: () => openDeviceTerminalTab(device),
      });
      if (device.agent) {
        items.push({
          kind: "item",
          label: "指标",
          icon: <IconActivity size={13} />,
          onSelect: () => setMetricsOpenId((id) => (id === device.id ? null : device.id)),
        });
      }
    }
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
            isAdmin={isAdmin}
            busy={busyId === device.id}
            metricsOpen={metricsOpenId === device.id}
            onToggleMetrics={() => setMetricsOpenId((id) => (id === device.id ? null : device.id))}
            onMenu={(x, y) => setMenu({ x, y, device })}
            onApprove={() => void approve(device)}
            onReject={() => void reject(device)}
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
                  menu.device.state === "pending",
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
  if (device.state === "pending") return "设备待审批，批准后才能打开终端";
  if (!device.agent) return "该设备未安装设备代理，无法打开终端";
  if (!device.agent.terminal_enabled) return "该设备已关闭远程终端";
  if (!online) return "设备离线，无法打开终端";
  return null;
}

function DeviceTreeRow({
  device,
  online,
  isAdmin,
  busy,
  metricsOpen,
  onToggleMetrics,
  onMenu,
  onApprove,
  onReject,
  onRevoke,
}: {
  device: FleetDevice;
  online: boolean;
  isAdmin: boolean;
  busy: boolean;
  metricsOpen: boolean;
  onToggleMetrics: () => void;
  onMenu: (x: number, y: number) => void;
  onApprove: () => void;
  onReject: () => void;
  onRevoke: () => void;
}) {
  const coarse = useCoarsePointer();
  const agent = device.agent;
  const pending = device.state === "pending";
  const disabledReason = disabledReasonOf(device, online);
  const connectable = disabledReason === null;

  const open = () => {
    if (!connectable || busy) return;
    openDeviceTerminalTab(device);
  };

  const tip = [
    device.name,
    agent?.platform || "",
    disabledReason ?? "单击打开远程终端 · 右键管理",
    pending ? "待管理员审批" : "",
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
        {pending ? (
          <span className="nx-badge nx-badge-amber" title="管理员批准前设备不会上线，也不同步数据">
            待审批
          </span>
        ) : agent ? (
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
            {pending && isAdmin ? (
              <>
                <button
                  className="nx-icon-btn nx-icon-btn-sm"
                  title="批准接入"
                  aria-label={`批准 ${device.name}`}
                  disabled={busy}
                  onClick={onApprove}
                >
                  <IconCheckCircle size={12} />
                </button>
                <button
                  className="nx-icon-btn nx-icon-btn-sm is-danger"
                  title="拒绝并删除"
                  aria-label={`拒绝 ${device.name}`}
                  disabled={busy}
                  onClick={onReject}
                >
                  <IconXCircle size={12} />
                </button>
              </>
            ) : (
              <>
                {agent && !pending && (
                  <button
                    className="nx-icon-btn nx-icon-btn-sm"
                    title="指标"
                    aria-label={`指标 ${device.name}`}
                    aria-expanded={metricsOpen}
                    onClick={onToggleMetrics}
                  >
                    <IconActivity size={12} />
                  </button>
                )}
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
              </>
            )}
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
      {metricsOpen && agent && (
        <div className="mx-1 mb-1 rounded border border-neutral-800/60 px-2 py-2">
          <MetricsPanel deviceId={device.id} online={online} />
        </div>
      )}
    </>
  );
}
