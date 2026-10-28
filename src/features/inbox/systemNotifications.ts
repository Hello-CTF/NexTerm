import { systemApi } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { EVENTS, listenEvent, type NotificationClickEvent } from "../../ipc/events";
import { describeError } from "../../ui/errorText";

export type NotificationPermissionState = "granted" | "denied" | "prompt";

export interface SystemNotificationStatus {
  available: boolean;
  permission?: NotificationPermissionState;
  // Why notifications are unavailable or denied, shown to the user.
  reason?: string;
}

export interface SystemNotificationInput {
  // A later notification with the same id replaces the earlier one.
  id: string;
  title: string;
  body?: string;
  // The backend terminal tab a click leads to.
  tabId?: string;
}

// SystemNotifier shows operating system notifications: the desktop bridge in the app, the Notification API in a
// browser. Clicks are handed to onClick with the tab the notification was about.
export interface SystemNotifier {
  status(): Promise<SystemNotificationStatus>;
  // knownStatus answers without waiting where the platform knows the status locally (a browser).
  knownStatus?: () => SystemNotificationStatus;
  // request asks for the permission; a browser only allows it from a user gesture.
  request(): Promise<SystemNotificationStatus>;
  show(notification: SystemNotificationInput): Promise<void>;
  remove(id: string): void;
  onClick(handler: (click: NotificationClickEvent) => void): void;
}

function normalizePermission(value: unknown): NotificationPermissionState | undefined {
  return value === "granted" || value === "denied" || value === "prompt" ? value : undefined;
}

function desktopStatus(raw: { available: boolean; permission?: string; reason?: string }): SystemNotificationStatus {
  return { available: raw.available, permission: normalizePermission(raw.permission), reason: raw.reason || undefined };
}

export function createDesktopNotifier(): SystemNotifier {
  let clickHandler: (click: NotificationClickEvent) => void = () => {};
  let listening = false;
  // The system asks for the permission once, the first time a notification is due.
  let asked = false;
  const listen = () => {
    if (listening) return;
    listening = true;
    void listenEvent<NotificationClickEvent>(EVENTS.notificationClick, (click) => clickHandler(click));
  };
  return {
    status: async () => desktopStatus(await systemApi.notificationStatus()),
    request: async () => {
      asked = true;
      return desktopStatus(await systemApi.requestNotifications());
    },
    show: async (notification) => {
      listen();
      if (!asked) {
        const status = desktopStatus(await systemApi.notificationStatus());
        if (status.permission === "prompt") {
          asked = true;
          await systemApi.requestNotifications();
        }
      }
      await systemApi.notify(notification);
    },
    remove: (id) => void systemApi.removeNotification(id).catch(() => {}),
    onClick: (handler) => {
      clickHandler = handler;
      listen();
    },
  };
}

type BrowserNotificationCtor = typeof Notification;

function browserNotification(): BrowserNotificationCtor | null {
  return typeof Notification === "undefined" ? null : Notification;
}

function browserStatus(api: BrowserNotificationCtor | null): SystemNotificationStatus {
  if (!api) return { available: false, reason: "此浏览器不支持系统通知" };
  if (typeof isSecureContext === "boolean" && !isSecureContext) {
    return { available: false, reason: "系统通知需要通过 HTTPS 访问" };
  }
  const permission = api.permission === "default" ? "prompt" : api.permission;
  return {
    available: true,
    permission,
    reason: permission === "denied" ? "浏览器已阻止此站点的通知，可在站点设置中允许" : undefined,
  };
}

export function createBrowserNotifier(api: BrowserNotificationCtor | null = browserNotification()): SystemNotifier {
  let clickHandler: (click: NotificationClickEvent) => void = () => {};
  const shown = new Map<string, Notification>();
  return {
    status: () => Promise.resolve(browserStatus(api)),
    knownStatus: () => browserStatus(api),
    request: async () => {
      if (!api) return browserStatus(api);
      await api.requestPermission();
      return browserStatus(api);
    },
    show: (notification) => {
      if (!api || api.permission !== "granted") return Promise.resolve();
      shown.get(notification.id)?.close();
      const shownNotification = new api(notification.title, { body: notification.body, tag: notification.id });
      shownNotification.onclick = () => {
        window.focus();
        shownNotification.close();
        clickHandler({ id: notification.id, tabId: notification.tabId });
      };
      shownNotification.onclose = () => {
        if (shown.get(notification.id) === shownNotification) shown.delete(notification.id);
      };
      shown.set(notification.id, shownNotification);
      return Promise.resolve();
    },
    remove: (id) => {
      shown.get(id)?.close();
      shown.delete(id);
    },
    onClick: (handler) => {
      clickHandler = handler;
    },
  };
}

let notifier: SystemNotifier | null = null;

export function systemNotifier(): SystemNotifier {
  notifier ??= WEB ? createBrowserNotifier() : createDesktopNotifier();
  return notifier;
}

export function setSystemNotifierForTest(next: SystemNotifier | null): void {
  notifier = next;
}

export function notificationErrorText(error: unknown): string {
  return `系统通知发送失败：${describeError(error)}`;
}
