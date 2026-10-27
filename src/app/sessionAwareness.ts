import { terminalApi } from "../ipc/commands";
import { EVENTS, listenEvent, type TerminalAttentionEvent } from "../ipc/events";
import { onEventsResync } from "../ipc/webTransport";
import { useUi } from "./store";
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

let started = false;
// reloadEpoch discards a list reply that an account reset or a newer reload has overtaken.
let reloadEpoch = 0;
// requestedSeen holds, per backend tab id, the state (by its since) this client already reported as seen.
const requestedSeen = new Map<string, string>();
let lastActivityAt = Number.NEGATIVE_INFINITY;

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

// noteUserActivity records input from the user and reports what they now see.
export function noteUserActivity(): void {
  lastActivityAt = environment.now();
  markVisibleAwarenessSeen();
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
// active workspace.
export function visibleTerminalTabIds(): Set<string> {
  const { workspaces, activeWorkspaceId } = useUi.getState();
  const visible = new Set<string>();
  const workspace = workspaces.find((w) => w.id === activeWorkspaceId);
  for (const pane of workspace?.panes ?? []) {
    const tab = pane.tabs.find((t) => t.id === pane.activeTabId);
    if (tab?.kind === "terminal" && tab.tabId) visible.add(tab.tabId);
  }
  return visible;
}

function unsupported(error: unknown): boolean {
  return typeof error === "object" && error !== null && (error as { code?: unknown }).code === "unsupported";
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
      if (unsupported(error)) {
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
  useUi.subscribe(() => {
    const key = visibleKey();
    if (key === lastVisible) return;
    lastVisible = key;
    markVisibleAwarenessSeen();
  });
  useAwarenessStore.subscribe(markVisibleAwarenessSeen);
  for (const type of ["keydown", "pointerdown", "wheel", "touchstart"]) {
    window.addEventListener(type, noteUserActivity, { capture: true, passive: true });
  }
  document.addEventListener("visibilitychange", markVisibleAwarenessSeen);
  window.addEventListener("focus", markVisibleAwarenessSeen);
  void reloadAwareness();
}

// resetAwarenessTracking drops everything known for the previous account and loads the current one's states.
export function resetAwarenessTracking(): void {
  requestedSeen.clear();
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
  resetAwarenessStore();
}
