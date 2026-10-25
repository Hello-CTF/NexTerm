import { terminalApi } from "../ipc/commands";
import { useUi } from "./store";
import { resetUnreadTabs, updateUnreadTabs, useUnreadStore } from "./unreadStore";

// Unread dots are a hint, not a live feed: 5 s keeps idle chatter low. The loop sleeps while the page is hidden.
const POLL_MS = 5000;

let started = false;
let timer: number | undefined;
let inFlight: Promise<void> | null = null;
let trackingEpoch = 0;
const lastOutputAt = new Map<string, number>();

function collectTracked(): { storeIdByTabId: Map<string, string>; backgroundIds: Set<string>; foregroundIds: Set<string> } {
  const storeIdByTabId = new Map<string, string>();
  const backgroundIds = new Set<string>();
  const foregroundIds = new Set<string>();
  const activeWsId = useUi.getState().activeWorkspaceId;
  for (const w of useUi.getState().workspaces) {
    for (const p of w.panes) {
      for (const t of p.tabs) {
        if (t.kind !== "terminal" || !t.tabId || t.dead || t.exited) continue;
        storeIdByTabId.set(t.tabId, t.id);
        if (p.activeTabId === t.id && w.id === activeWsId) foregroundIds.add(t.id);
        else backgroundIds.add(t.id);
      }
    }
  }
  return { storeIdByTabId, backgroundIds, foregroundIds };
}

// The first poll only builds a baseline without marking: restored layouts may have daemon terminals that just produced output, avoiding all dots lighting up at startup.
// Absolute output timestamps include the zero-to-first-output transition and do not depend on response timing.
async function doPollUnreadTabsOnce(): Promise<void> {
  const epoch = trackingEpoch;
  const { storeIdByTabId, backgroundIds, foregroundIds } = collectTracked();
  for (const tabId of [...lastOutputAt.keys()]) {
    if (!storeIdByTabId.has(tabId)) lastOutputAt.delete(tabId);
  }
  if (storeIdByTabId.size === 0) return;
  let live;
  try {
    live = await terminalApi.listLive();
  } catch {
    return;
  }
  // A response that outlives its tracking epoch (account switch/reset) is stale: discard it.
  if (epoch !== trackingEpoch) return;
  const mark = new Set<string>();
  for (const info of live) {
    const storeId = storeIdByTabId.get(info.tabId);
    if (!storeId || typeof info.lastOutputAt !== "number") continue;
    const prev = lastOutputAt.get(info.tabId);
    lastOutputAt.set(info.tabId, info.lastOutputAt);
    if (prev !== undefined && info.lastOutputAt > prev && backgroundIds.has(storeId)) mark.add(storeId);
  }
  // Stale marks on activated tabs are cleared too, covering activation paths that bypass setActiveTab such as addTab focus/close succession.
  const state = useUi.getState();
  const unreadTabIds = useUnreadStore.getState().unreadTabIds;
  const stale = new Set([...foregroundIds].filter((tabId) => unreadTabIds.has(tabId)));
  if (mark.size === 0 && stale.size === 0) return;
  const openTabIds = new Set(
    state.workspaces.flatMap((workspace) =>
      workspace.panes.flatMap((pane) => pane.tabs.map((tab) => tab.id)),
    ),
  );
  updateUnreadTabs({ mark, clear: stale, openTabIds });
}

// Single-flight: overlapping callers share one listLive request instead of stacking polls.
export function pollUnreadTabsOnce(): Promise<void> {
  inFlight ??= doPollUnreadTabsOnce().finally(() => {
    inFlight = null;
  });
  return inFlight;
}

function schedule(delayMs: number): void {
  if (!started || timer !== undefined) return;
  timer = window.setTimeout(() => {
    timer = undefined;
    void pollAndSchedule();
  }, delayMs);
}

async function pollAndSchedule(): Promise<void> {
  try {
    await pollUnreadTabsOnce();
  } finally {
    // Completion scheduling: the next poll is due POLL_MS after the previous one finished.
    // While the page is hidden the loop pauses; visibilitychange resumes it.
    if (!document.hidden) schedule(POLL_MS);
  }
}

function onVisibilityChange(): void {
  if (!started) return;
  if (document.hidden) {
    if (timer !== undefined) {
      window.clearTimeout(timer);
      timer = undefined;
    }
    return;
  }
  if (timer === undefined && inFlight === null) void pollAndSchedule();
}

export function startUnreadTracking(): void {
  if (started) return;
  started = true;
  document.addEventListener("visibilitychange", onVisibilityChange);
  schedule(POLL_MS);
}

export function stopUnreadTrackingForTest(): void {
  started = false;
  if (timer !== undefined) {
    window.clearTimeout(timer);
    timer = undefined;
  }
  document.removeEventListener("visibilitychange", onVisibilityChange);
}

export function resetUnreadTracking(): void {
  trackingEpoch += 1;
  lastOutputAt.clear();
  resetUnreadTabs();
}

export function resetUnreadTrackingForTest(): void {
  stopUnreadTrackingForTest();
  resetUnreadTracking();
}
