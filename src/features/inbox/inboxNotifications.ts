import { useUi, type ToastAction } from "../../app/store";
import { needsAttention, tabAwareness, toAwareness, type SessionAwareness } from "../../app/sessionAwarenessStore";
import { visibleTerminalTabIds } from "../../app/sessionAwareness";
import { EVENTS, listenEvent, type TerminalAttentionEvent } from "../../ipc/events";
import { attentionDetail, openInboxTab, uiTabForBackendTab } from "./inbox";
import { getNotificationPrefs } from "./notificationPrefs";
import {
  notificationErrorText,
  systemNotifier,
  type SystemNotificationStatus,
  type SystemNotifier,
} from "./systemNotifications";

// A state is notified only after it held this long. Every device receives the same state; a device whose user is
// at the session marks it seen within this window, and the others then drop it instead of popping a notification
// for something already handled.
export const NOTIFY_GRACE_MS = 1500;

export interface InboxNotificationDeps {
  notifier: SystemNotifier;
  // The user looks at the app: the page is shown and the window has the focus.
  appFocused: () => boolean;
  // The backend tab ids shown on screen.
  visibleTabIds: () => ReadonlySet<string>;
  // A system notification could not be shown.
  onError: (message: string) => void;
  // The in-app fallback while system notifications are off or unavailable.
  toast: (text: string, action: ToastAction) => void;
}

const defaultDeps = (): InboxNotificationDeps => ({
  notifier: systemNotifier(),
  appFocused: () => !document.hidden && document.hasFocus(),
  visibleTabIds: visibleTerminalTabIds,
  onError: (message) => useUi.getState().pushToast("error", message),
  toast: (text, action) => useUi.getState().pushToast("info", text, action),
});

let deps: InboxNotificationDeps | null = null;
let started = false;
const pending = new Map<string, ReturnType<typeof setTimeout>>();
// notified holds, per backend tab, the state already notified (or deliberately not notified) by notificationKey.
const notified = new Map<string, string>();
// shown holds the tabs with a system notification on screen.
const shown = new Set<string>();
let errorReported = false;
// statusCache holds the notifier's status for this run; refreshNotificationStatus drops it.
let statusCache: Promise<SystemNotificationStatus> | null = null;

// refreshNotificationStatus makes the next notification read the status again, after the user granted or
// refused the permission.
export function refreshNotificationStatus(): void {
  statusCache = null;
}

// systemUsable: notifications can be shown, or the desktop can still ask for the permission. A browser can only
// ask from a user gesture, so there a pending permission means no.
async function systemUsable(notifier: SystemNotifier): Promise<boolean> {
  statusCache ??= notifier.status().catch(() => ({ available: false }));
  const status = await statusCache;
  if (!status.available || status.permission === "denied") return false;
  return status.permission === "granted" || !notifier.knownStatus;
}

const notificationId = (tabId: string) => `attention:${tabId}`;

// One state of a tab: a new bell or notification text, or the state coming back after it was seen, is another.
function notificationKey(awareness: SessionAwareness): string {
  return [awareness.state, awareness.since ?? "", awareness.reason ?? "", awareness.title ?? "", awareness.body ?? ""].join("\n");
}

function withdraw(tabId: string): void {
  const timer = pending.get(tabId);
  if (timer !== undefined) clearTimeout(timer);
  pending.delete(tabId);
  notified.delete(tabId);
  if (shown.delete(tabId)) deps?.notifier.remove(notificationId(tabId));
}

// considerAttention runs for every live attention event. List reloads do not come through here, so opening the app
// does not replay notifications for states that were already waiting.
export function considerAttention(event: TerminalAttentionEvent): void {
  if (!deps || !event.tabId) return;
  const known = tabAwareness(event.tabId);
  if (known && event.version < known.version) return;
  const tabId = event.tabId;
  if (!needsAttention(toAwareness(event))) {
    withdraw(tabId);
    return;
  }
  if (pending.has(tabId)) return;
  pending.set(
    tabId,
    setTimeout(() => {
      pending.delete(tabId);
      deliver(tabId);
    }, NOTIFY_GRACE_MS),
  );
}

function deliver(tabId: string): void {
  if (!deps) return;
  const awareness = tabAwareness(tabId);
  if (!awareness || !needsAttention(awareness)) {
    withdraw(tabId);
    return;
  }
  const key = notificationKey(awareness);
  if (notified.get(tabId) === key) return;
  notified.set(tabId, key);
  const prefs = getNotificationPrefs();
  const wanted =
    awareness.state === "needs_me"
      ? prefs.needsMe && (!awareness.inferred || prefs.inferred)
      : prefs.finished && awareness.notable;
  if (!wanted) return;
  // The user sees the tab: its badge is enough.
  if (deps.appFocused() && deps.visibleTabIds().has(tabId)) return;
  // A click must lead somewhere: only sessions this client shows notify here.
  const uiTabId = uiTabForBackendTab(tabId);
  if (!uiTabId) return;
  const { workspaces } = useUi.getState();
  const workspace = workspaces.find((w) => w.panes.some((p) => p.tabs.some((t) => t.id === uiTabId)));
  // The title leads with the workspace the user named; text a remote program controls (its notification, the tab
  // title it set) stays in the body.
  const place = workspace?.title ?? "终端";
  const title = `${place} · ${awareness.state === "needs_me" ? "需要你处理" : "命令已结束"}`;
  const detail = attentionDetail(awareness);
  const active = deps;
  void (async () => {
    const usable = prefs.system && (await systemUsable(active.notifier));
    // The state may have been seen or replaced while the status was read.
    if (deps !== active || notified.get(tabId) !== key) return;
    if (!usable) {
      active.toast(`${title}：${detail}`, { label: "查看", onSelect: () => openInboxTab(tabId) });
      return;
    }
    shown.add(tabId);
    try {
      await active.notifier.show({ id: notificationId(tabId), title, body: `来自 ${place}：${detail}`, tabId });
    } catch (error) {
      shown.delete(tabId);
      if (errorReported) return;
      // Reported once per run, so a broken notification service does not repeat the same error.
      errorReported = true;
      active.onError(notificationErrorText(error));
    }
  })();
}

// notifyAi shows an AI notify-tool message as a system notification while the user is away from the app; the
// caller keeps the in-app toast.
export function notifyAi(text: string): void {
  const active = deps ?? defaultDeps();
  const prefs = getNotificationPrefs();
  if (!prefs.system || !prefs.ai || active.appFocused()) return;
  void systemUsable(active.notifier).then((usable) => {
    if (usable) return active.notifier.show({ id: "ai", title: "AI 通知", body: text });
  }).catch(() => {});
}

export function startInboxNotifications(override?: Partial<InboxNotificationDeps>): void {
  deps = { ...defaultDeps(), ...override };
  deps.notifier.onClick((click) => {
    if (click.tabId) openInboxTab(click.tabId);
  });
  if (started) return;
  started = true;
  void listenEvent<TerminalAttentionEvent>(EVENTS.terminalAttention, considerAttention);
}

// resetInboxNotifications forgets the previous account's states and takes its notifications away.
export function resetInboxNotifications(): void {
  for (const tabId of [...pending.keys(), ...shown]) withdraw(tabId);
  notified.clear();
  errorReported = false;
  statusCache = null;
}

export function stopInboxNotificationsForTest(): void {
  resetInboxNotifications();
  deps = null;
}
