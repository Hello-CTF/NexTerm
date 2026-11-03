import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useUi } from "../../app/store";
import { fleetAlertsClient, type FleetAlertEvent, type FleetAlerts } from "../../ipc/fleetAlertsApi";
import { describeError } from "../../ui/errorText";
import { IconCheck } from "../../ui/icons";
import {
  alertEventText,
  alertEventTitle,
  alertsQueryKey,
  mutedAlertEvents,
  useDeviceNames,
  useFleetAlerts,
  visibleAlertEvents,
} from "../fleet/deviceAlerts";
import { useFleetAccount } from "../fleet/useFleetAccount";
import { defaultDeviceAlertDeps, deviceAlertNotifier } from "./deviceAlertNotifications";
import { formatTime } from "../fleet/format";

// useDeviceAlertNotifications feeds every fresh poll to the notifier. InboxButton is always mounted in the
// header, so it is the one caller.
export function useDeviceAlertNotifications(): void {
  const { userId } = useFleetAccount();
  const alerts = useFleetAlerts();
  const names = useDeviceNames(userId);
  const namesRef = useRef(names);
  namesRef.current = names;
  useEffect(() => {
    if (!userId) {
      deviceAlertNotifier.reset();
      return;
    }
    if (!alerts.data) return;
    deviceAlertNotifier.consume(userId, alerts.data, defaultDeviceAlertDeps((id) => namesRef.current(id)));
  }, [userId, alerts.data]);
}

// useUnackedAlertCount: events that need the bell (muted records do not).
export function useUnackedAlertCount(): number {
  const alerts = useFleetAlerts();
  return visibleAlertEvents(alerts.data).length;
}

export function useAckAlerts() {
  const qc = useQueryClient();
  const { userId } = useFleetAccount();
  const pushToast = useUi((s) => s.pushToast);
  const [busy, setBusy] = useState(false);
  const ack = async (ids: string[]) => {
    if (ids.length === 0) return;
    setBusy(true);
    try {
      await fleetAlertsClient().ack(ids);
      const gone = new Set(ids);
      qc.setQueryData<FleetAlerts>(alertsQueryKey(userId), (old) =>
        old ? { ...old, events: old.events.filter((e) => !gone.has(e.id)) } : old,
      );
      void qc.invalidateQueries({ queryKey: alertsQueryKey(userId) });
    } catch (e) {
      pushToast("error", `确认告警失败：${describeError(e)}`);
    } finally {
      setBusy(false);
    }
  };
  return { ack, busy };
}

function EventRow({ event, name, busy, onAck }: { event: FleetAlertEvent; name: string; busy: boolean; onAck: () => void }) {
  return (
    <li className="rounded-md px-2 py-1.5 hover:bg-neutral-800/60" data-alert-event={event.id}>
      <div className="flex items-start gap-2">
        <span className="min-w-0 flex-1">
          <span className="flex items-baseline gap-1.5">
            <span className="truncate text-[12.5px] font-medium text-neutral-100">{alertEventTitle(name, event)}</span>
            <span className="shrink-0 text-[11px] text-neutral-500">{formatTime(event.ts)}</span>
          </span>
          <span className="block text-[11.5px] text-neutral-300">{alertEventText(event)}</span>
        </span>
        <button
          type="button"
          className="nx-icon-btn shrink-0"
          aria-label={`确认 ${alertEventTitle(name, event)}`}
          title="确认（所有设备）"
          disabled={busy}
          onClick={onAck}
        >
          <IconCheck size={14} />
        </button>
      </div>
    </li>
  );
}

// DeviceAlertInbox lists the unacknowledged device alerts. Quiet-hours offline records sit in a collapsed section.
export function DeviceAlertInbox() {
  const { userId } = useFleetAccount();
  const alerts = useFleetAlerts();
  const names = useDeviceNames(userId);
  const { ack, busy } = useAckAlerts();
  const [mutedOpen, setMutedOpen] = useState(false);
  const visible = visibleAlertEvents(alerts.data);
  const muted = mutedAlertEvents(alerts.data);
  if (visible.length === 0 && muted.length === 0) return null;
  const nameOf = (event: FleetAlertEvent) => names(event.device_id) ?? "设备";
  return (
    <div className="mt-1" data-testid="device-alert-inbox">
      {visible.length > 0 && (
        <>
          <div className="flex items-center gap-2 px-2 pb-1 pt-0.5">
            <span className="min-w-0 flex-1 text-[11.5px] font-semibold text-neutral-400">设备告警</span>
            {visible.length > 1 && (
              <button
                type="button"
                className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
                disabled={busy}
                onClick={() => void ack(visible.map((e) => e.id))}
              >
                全部确认
              </button>
            )}
          </div>
          <ul className="flex flex-col">
            {visible.map((event) => (
              <EventRow key={event.id} event={event} name={nameOf(event)} busy={busy} onAck={() => void ack([event.id])} />
            ))}
          </ul>
        </>
      )}
      {muted.length > 0 && (
        <div className="px-2 pt-1">
          <button
            type="button"
            className="text-[11.5px] text-neutral-400 hover:text-neutral-200"
            aria-expanded={mutedOpen}
            onClick={() => setMutedOpen((v) => !v)}
          >
            静音期间的离线记录（{muted.length}）
          </button>
          {mutedOpen && (
            <ul className="flex flex-col" data-testid="device-alert-muted-list">
              {muted.map((event) => (
                <EventRow key={event.id} event={event} name={nameOf(event)} busy={busy} onAck={() => void ack([event.id])} />
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
