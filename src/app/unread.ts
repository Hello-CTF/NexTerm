import { terminalApi } from "../ipc/commands";
import { useUi } from "./store";

const POLL_MS = 2000;

let started = false;
const lastOutputMsAgo = new Map<string, number>();

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
// Afterwards, lastOutputMsAgo decreasing means new output between two polls; only non-active tabs are marked, applied in one batched setState.
export async function pollUnreadTabsOnce(): Promise<void> {
  const { storeIdByTabId, backgroundIds, foregroundIds } = collectTracked();
  for (const tabId of [...lastOutputMsAgo.keys()]) {
    if (!storeIdByTabId.has(tabId)) lastOutputMsAgo.delete(tabId);
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
    const prev = lastOutputMsAgo.get(info.tabId);
    lastOutputMsAgo.set(info.tabId, info.lastOutputMsAgo);
    if (prev === undefined) continue;
    if (info.lastOutputMsAgo < prev && backgroundIds.has(storeId)) mark.add(storeId);
  }
  // Stale marks on activated tabs are cleared too, covering activation paths that bypass setActiveTab such as addTab focus/close succession.
  const state = useUi.getState();
  const stale = new Set<string>();
  for (const w of state.workspaces) {
    for (const p of w.panes) {
      for (const t of p.tabs) {
        if (t.unread && foregroundIds.has(t.id)) stale.add(t.id);
      }
    }
  }
  if (mark.size === 0 && stale.size === 0) return;
  useUi.setState((s) => ({
    workspaces: s.workspaces.map((w) =>
      w.panes.some((p) => p.tabs.some((t) => (mark.has(t.id) && !t.unread) || (stale.has(t.id) && t.unread)))
        ? {
            ...w,
            panes: w.panes.map((p) =>
              p.tabs.some((t) => (mark.has(t.id) && !t.unread) || (stale.has(t.id) && t.unread))
                ? {
                    ...p,
                    tabs: p.tabs.map((t) => {
                      if (mark.has(t.id) && !t.unread) return { ...t, unread: true };
                      if (stale.has(t.id) && t.unread) return { ...t, unread: undefined };
                      return t;
                    }),
                  }
                : p,
            ),
          }
        : w,
    ),
  }));
}

export function startUnreadTracking(): void {
  if (started) return;
  started = true;
  window.setInterval(() => void pollUnreadTabsOnce(), POLL_MS);
}

export function resetUnreadTrackingForTest(): void {
  lastOutputMsAgo.clear();
}
