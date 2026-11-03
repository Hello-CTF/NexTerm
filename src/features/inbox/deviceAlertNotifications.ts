import type { ToastAction } from "../../app/store";
import { useUi } from "../../app/store";
import type { FleetAlerts } from "../../ipc/fleetAlertsApi";
import { alertEventText, alertEventTitle, visibleAlertEvents } from "../fleet/deviceAlerts";
import { getNotificationPrefs } from "./notificationPrefs";
import { systemNotifier, type SystemNotifier } from "./systemNotifications";

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
    for (const event of fresh) {
      const title = alertEventTitle(deps.deviceName(event.device_id) ?? "设备", event);
      const body = alertEventText(event);
      void (async () => {
        if (prefs.system && !deps.appFocused() && (await systemUsable(deps.notifier))) {
          try {
            await deps.notifier.show({ id: `device-alert:${event.id}`, title, body });
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
