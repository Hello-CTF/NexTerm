import type { ToastAction } from "../../app/store";
import { useUi } from "../../app/store";
import type { FleetAlerts } from "../../ipc/fleetAlertsApi";
import { alertEventText, alertEventTitle, visibleAlertEvents } from "../fleet/deviceAlerts";
import { getNotificationPrefs } from "./notificationPrefs";
import { systemNotifier, type SystemNotifier } from "./systemNotifications";
import { t } from "../../i18n";

export interface DeviceAlertNotificationDeps {
  notifier: SystemNotifier;
  appFocused: () => boolean;
  prefs: () => { system: boolean; deviceAlerts: boolean };
  deviceName: (deviceId: string) => string | undefined;
  toast: (text: string, action?: ToastAction) => void;
}

export const defaultDeviceAlertDeps = (deviceName: (deviceId: string) => string | undefined): DeviceAlertNotificationDeps => ({
  notifier: systemNotifier(),
  appFocused: () => !document.hidden && document.hasFocus(),
  prefs: getNotificationPrefs,
  deviceName,
  toast: (text) => useUi.getState().pushToast("info", text),
});

const MAX_BODY = 120;

function truncate(text: string): string {
  return text.length > MAX_BODY ? `${text.slice(0, MAX_BODY - 1)}…` : text;
}

async function systemUsable(notifier: SystemNotifier): Promise<boolean> {
  try {
    const status = await notifier.status();
    if (!status.available || status.permission === "denied") return false;
    return status.permission === "granted" || !notifier.knownStatus;
  } catch {
    return false;
  }
}

// DeviceAlertNotifier remembers which events were seen. The first batch only seeds the set, so opening the app
// does not replay events that were already waiting.
export class DeviceAlertNotifier {
  private seen = new Set<string>();
  private loaded = false;
  private userId: string | null | undefined;

  reset(): void {
    this.seen = new Set();
    this.loaded = false;
  }

  // consume handles one fresh poll result of the given account.
  consume(userId: string | null, alerts: FleetAlerts | undefined, deps: DeviceAlertNotificationDeps): void {
    if (this.userId !== userId) {
      this.userId = userId;
      this.reset();
    }
    if (!userId || !alerts) return;
    const events = alerts.events;
    if (!this.loaded) {
      this.loaded = true;
      for (const event of events) this.seen.add(event.id);
      return;
    }
    const fresh = visibleAlertEvents(alerts).filter((event) => !this.seen.has(event.id));
    for (const event of events) this.seen.add(event.id);
    const prefs = deps.prefs();
    if (!prefs.deviceAlerts) return;
    const byDevice = new Map<string, typeof fresh>();
    for (const event of fresh) byDevice.set(event.device_id, [...(byDevice.get(event.device_id) ?? []), event]);
    for (const [deviceId, group] of byDevice) {
      const name = deps.deviceName(deviceId) ?? t("设备");
      const first = group[0];
      const title = group.length === 1 ? alertEventTitle(name, first) : t("{name} · {length} 条告警", { name, length: group.length });
      const body = group.length === 1 ? alertEventText(first) : truncate(group.map(alertEventText).join("；"));
      void (async () => {
        if (prefs.system && !deps.appFocused() && (await systemUsable(deps.notifier))) {
          try {
            await deps.notifier.show({ id: `device-alert:${first.id}`, title, body });
            return;
          } catch {
            // Fall through to the in-app toast.
          }
        }
        deps.toast(`${title}：${body}`);
      })();
    }
  }
}

export const deviceAlertNotifier = new DeviceAlertNotifier();
