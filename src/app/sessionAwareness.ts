import { terminalApi } from "../ipc/commands";
import { EVENTS, listenEvent, type TerminalAttentionEvent } from "../ipc/events";
import { onEventsResync } from "../ipc/webTransport";
import { resetSessionAggregate, startSessionAggregateTracking } from "./sessionAggregate";
import { useUi } from "./store";
import { isMobileHomeOpen, useMobileHome } from "./shell/mobileHome";
import {
  applyAwareness,
  markAwarenessSeenLocally,
  needsAttention,
  resetAwarenessStore,
  useAwarenessStore,
} from "./sessionAwarenessStore";

// A state counts as seen only while the user is at the device: they pressed a key, clicked, scrolled or touched
// within this window. A screen left open on an unattended device does not clear other devices' badges.
export const ACTIVE_USER_WINDOW_MS = 45_000;
// Pointer moves and key repeats arrive in bursts; the seen scan runs at most once per interval.
export const ACTIVITY_SCAN_INTERVAL_MS = 250;

let started = false;
// reloadEpoch discards a list reply that an account reset or a newer reload has overtaken.
let reloadEpoch = 0;
// requestedSeen holds, per backend tab id, the state (by its since) this client already reported as seen.
const requestedSeen = new Map<string, string>();
let lastActivityAt = Number.NEGATIVE_INFINITY;
let lastScanAt = Number.NEGATIVE_INFINITY;
let scanTimer: ReturnType<typeof setTimeout> | undefined;

interface AwarenessEnvironment {
  // The page is shown and the window has the focus.
  visible: () => boolean;
  now: () => number;
}

let environment: AwarenessEnvironment = {
  visible: () => !document.hidden && document.hasFocus(),
  now: () => Date.now(),
};

// The user looks at the app: it is shown, focused, and they were active recently.
function userLooking(): boolean {
  return environment.visible() && environment.now() - lastActivityAt <= ACTIVE_USER_WINDOW_MS;
}

// noteUserActivity records input from the user and reports what they now see. The activity time is always recorded,
// but the scan runs at most once per ACTIVITY_SCAN_INTERVAL_MS: the first event scans at once and a trailing call
// covers the last event of a burst.
export function noteUserActivity(): void {
  const now = environment.now();
  lastActivityAt = now;
  if (scanTimer !== undefined) return;
  const wait = ACTIVITY_SCAN_INTERVAL_MS - (now - lastScanAt);
  if (wait <= 0) {
    lastScanAt = now;
    markVisibleAwarenessSeen();
    return;
  }
  scanTimer = setTimeout(() => {
    scanTimer = undefined;
    lastScanAt = environment.now();
    markVisibleAwarenessSeen();
  }, wait);
}

function cancelPendingScan(): void {
  if (scanTimer !== undefined) clearTimeout(scanTimer);
  scanTimer = undefined;
  lastScanAt = Number.NEGATIVE_INFINITY;
}

// reloadAwareness replaces the attention states with the backend's list. Versions restart with the backend process,
// so the store starts empty; events that arrive while the list is in flight are newer and survive it.
export async function reloadAwareness(): Promise<void> {
  const epoch = ++reloadEpoch;
  resetAwarenessStore();
  let listed: TerminalAttentionEvent[];
  try {
    listed = await terminalApi.attentionList();
  } catch {
    return;
  }
  if (epoch !== reloadEpoch) return;
  applyAwareness(listed);
  markVisibleAwarenessSeen();
}

// visibleTerminalTabIds are the backend ids of the terminal tabs on screen: the active tab of each pane in the
// active workspace. While the mobile home covers the workspaces, no terminal is on screen.
export function visibleTerminalTabIds(): Set<string> {
  const visible = new Set<string>();
  if (isMobileHomeOpen()) return visible;
  const { workspaces, activeWorkspaceId } = useUi.getState();
  const workspace = workspaces.find((w) => w.id === activeWorkspaceId);
  for (const pane of workspace?.panes ?? []) {
    const tab = pane.tabs.find((t) => t.id === pane.activeTabId);
    if (tab?.kind === "terminal" && tab.tabId) visible.add(tab.tabId);
  }
  return visible;
}

// isSeenUnsupported is true only for the backend answer that the helper can never share a seen mark. Other
// "unsupported" answers (a helper that is momentarily unavailable) are transient and retried.
export function isSeenUnsupported(error: unknown): boolean {
  if (typeof error !== "object" || error === null || (error as { code?: unknown }).code !== "unsupported") return false;
  const detail = (error as { detail?: unknown }).detail;
  return typeof detail === "object" && detail !== null && (detail as { reason?: unknown }).reason === "seen_unsupported";
}

// markVisibleAwarenessSeen reports a needs_me or just_finished state on a tab the user is looking at as seen, once
// per state. The backend clears it on every device. A failed report is retried on the next change; where the helper
// cannot share the mark, the state is seen on this device only and not reported again.
export function markVisibleAwarenessSeen(): void {
  if (!userLooking()) return;
  const byTabId = useAwarenessStore.getState().byTabId;
  for (const tabId of visibleTerminalTabIds()) {
    const awareness = byTabId.get(tabId);
    const since = awareness?.since;
    if (!needsAttention(awareness) || !since || requestedSeen.get(tabId) === since) continue;
    requestedSeen.set(tabId, since);
    void terminalApi.attentionSeen(tabId, since).catch((error: unknown) => {
      if (isSeenUnsupported(error)) {
        markAwarenessSeenLocally(tabId, since);
        return;
      }
      if (requestedSeen.get(tabId) === since) requestedSeen.delete(tabId);
    });
  }
  for (const tabId of requestedSeen.keys()) {
    if (!byTabId.has(tabId)) requestedSeen.delete(tabId);
  }
}

function visibleKey(): string {
  return [...visibleTerminalTabIds()].sort().join("\n");
}

export function startAwarenessTracking(): void {
  if (started) return;
  started = true;
  void listenEvent<TerminalAttentionEvent>(EVENTS.terminalAttention, (event) => {
    applyAwareness([event]);
  });
  onEventsResync(() => void reloadAwareness());
  let lastVisible = visibleKey();
  const onVisibleChange = () => {
    const key = visibleKey();
    if (key === lastVisible) return;
    lastVisible = key;
    markVisibleAwarenessSeen();
  };
  useUi.subscribe(onVisibleChange);
  useMobileHome.subscribe(onVisibleChange);
  useAwarenessStore.subscribe(markVisibleAwarenessSeen);
  for (const type of ["keydown", "pointerdown", "wheel", "touchstart"]) {
    window.addEventListener(type, noteUserActivity, { capture: true, passive: true });
  }
  document.addEventListener("visibilitychange", markVisibleAwarenessSeen);
  window.addEventListener("focus", markVisibleAwarenessSeen);
  void reloadAwareness();
  // Sessions on hosts this client holds no tab for come from the cross-host listing.
  startSessionAggregateTracking();
}

// resetAwarenessTracking drops everything known for the previous account and loads the current one's states.
export function resetAwarenessTracking(): void {
  requestedSeen.clear();
  resetSessionAggregate();
  if (started) void reloadAwareness();
  else resetAwarenessStore();
}

export function setAwarenessEnvironmentForTest(next: Partial<AwarenessEnvironment>): void {
  environment = { ...environment, ...next };
}

export function resetAwarenessForTest(): void {
  reloadEpoch += 1;
  requestedSeen.clear();
  lastActivityAt = Number.NEGATIVE_INFINITY;
  cancelPendingScan();
  resetAwarenessStore();
}
