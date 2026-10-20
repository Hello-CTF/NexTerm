import { terminalApi } from "../ipc/commands";
import { useUi } from "./store";
import { resetUnreadTabs, updateUnreadTabs, useUnreadStore } from "./unreadStore";

const POLL_MS = 2000;

let started = false;
const lastOutputAt = new Map<string, number>();
const legacyLastOutputMsAgo = new Map<string, number>();

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
// New responses use the absolute output timestamp, including the zero-to-first-output transition. Older servers retain the age-based fallback.
export async function pollUnreadTabsOnce(): Promise<void> {
  const { storeIdByTabId, backgroundIds, foregroundIds } = collectTracked();
  for (const tabId of [...lastOutputAt.keys()]) {
    if (!storeIdByTabId.has(tabId)) lastOutputAt.delete(tabId);
  }
  for (const tabId of [...legacyLastOutputMsAgo.keys()]) {
    if (!storeIdByTabId.has(tabId)) legacyLastOutputMsAgo.delete(tabId);
  }
  if (storeIdByTabId.size === 0) return;
  let live;
  try {
    live = await terminalApi.listLive();
  } catch {
    return;
  }
  const mark = new Set<string>();
  for (const info of live) {
    const storeId = storeIdByTabId.get(info.tabId);
    if (!storeId) continue;
    if (info.lastOutputAt !== undefined) {
      const prev = lastOutputAt.get(info.tabId);
      lastOutputAt.set(info.tabId, info.lastOutputAt);
      legacyLastOutputMsAgo.delete(info.tabId);
      if (prev !== undefined && info.lastOutputAt > prev && backgroundIds.has(storeId)) mark.add(storeId);
      continue;
    }
    const prev = legacyLastOutputMsAgo.get(info.tabId);
    legacyLastOutputMsAgo.set(info.tabId, info.lastOutputMsAgo);
    lastOutputAt.delete(info.tabId);
    if (prev !== undefined && info.lastOutputMsAgo < prev && backgroundIds.has(storeId)) mark.add(storeId);
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

export function startUnreadTracking(): void {
  if (started) return;
  started = true;
  window.setInterval(() => void pollUnreadTabsOnce(), POLL_MS);
}

export function resetUnreadTrackingForTest(): void {
  lastOutputAt.clear();
  legacyLastOutputMsAgo.clear();
  resetUnreadTabs();
}
