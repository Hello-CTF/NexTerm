import { terminalApi } from "../ipc/commands";
import { EVENTS, listenEvent, type TerminalAttentionEvent } from "../ipc/events";
import { onEventsResync } from "../ipc/webTransport";
import { useUi } from "./store";
import { applyAwareness, needsAttention, resetAwarenessStore, useAwarenessStore } from "./sessionAwarenessStore";

let started = false;
// reloadEpoch discards a list reply that an account reset or a newer reload has overtaken.
let reloadEpoch = 0;
// requestedSeen holds, per backend tab id, the state (by its since) this client already reported as seen.
const requestedSeen = new Map<string, string>();

// The user looks at the app: the page is shown and the window has the focus. A state shown on a screen nobody
// looks at is not seen.
let userLooking: () => boolean = () => !document.hidden && document.hasFocus();

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

// markVisibleAwarenessSeen reports a needs_me or just_finished state on a tab the user is looking at as seen, once
// per state. The backend clears it on every device; a failed report is retried on the next change.
export function markVisibleAwarenessSeen(): void {
  if (!userLooking()) return;
  const byTabId = useAwarenessStore.getState().byTabId;
  for (const tabId of visibleTerminalTabIds()) {
    const awareness = byTabId.get(tabId);
    const since = awareness?.since;
    if (!needsAttention(awareness) || !since || requestedSeen.get(tabId) === since) continue;
    requestedSeen.set(tabId, since);
    void terminalApi.attentionSeen(tabId, since).catch(() => {
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

export function setUserLookingForTest(probe: () => boolean): void {
  userLooking = probe;
}

export function resetAwarenessForTest(): void {
  reloadEpoch += 1;
  requestedSeen.clear();
  resetAwarenessStore();
}
