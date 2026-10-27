import { create } from "zustand";
import { applyRemoteTabTitle, findTab, hasCustomTitle, sanitizeRemoteTabTitle, useUi } from "./store";

// Per-terminal view state that changes at output frequency (scrollback position, remote title).
// Components subscribe through the narrow selectors below so a burst on one tab re-renders only
// that tab's chrome; unchanged values are no-ops and never trigger a render.
// titles is keyed by the workspace storeTabId (the id applyRemoteTabTitle and the tab strip
// know); linesBelow is keyed by the owning pane's stable view id (the storeTabId TerminalPane
// passes as viewTabId — never the shared pre-attach "pending" placeholder), so concurrent
// attaching tabs cannot contaminate each other and the value survives the attach transition.
// atTop (same key as linesBelow) is set while the normal screen's viewport is at the top
// of the scrollback, where a durable tab offers its earlier history.
interface TerminalViewState {
  linesBelow: Record<string, number>;
  atTop: Record<string, boolean>;
  titles: Record<string, string>;
}

export const useTerminalViewStore = create<TerminalViewState>()(() => ({
  linesBelow: {},
  atTop: {},
  titles: {},
}));

export function useTerminalLinesBelow(tabId: string): number {
  return useTerminalViewStore((state) => state.linesBelow[tabId] ?? 0);
}

export function useTerminalAtTop(tabId: string): boolean {
  return useTerminalViewStore((state) => state.atTop[tabId] === true);
}

export function useTerminalTabTitle(tabId: string): string | undefined {
  return useTerminalViewStore((state) => state.titles[tabId]);
}

export function getTerminalLinesBelow(tabId: string): number {
  return useTerminalViewStore.getState().linesBelow[tabId] ?? 0;
}

export function getTerminalTabTitle(tabId: string): string | undefined {
  return useTerminalViewStore.getState().titles[tabId];
}

export function setTerminalLinesBelow(tabId: string, value: number): void {
  useTerminalViewStore.setState((state) => {
    if ((state.linesBelow[tabId] ?? 0) === value) return state;
    return { linesBelow: { ...state.linesBelow, [tabId]: value } };
  });
}

export function setTerminalAtTop(tabId: string, value: boolean): void {
  useTerminalViewStore.setState((state) => {
    if ((state.atTop[tabId] === true) === value) return state;
    const atTop = { ...state.atTop };
    if (value) atTop[tabId] = true;
    else delete atTop[tabId];
    return { atTop };
  });
}

export function setTerminalTabTitle(tabId: string, title: string): void {
  useTerminalViewStore.setState((state) => {
    if (state.titles[tabId] === title) return state;
    return { titles: { ...state.titles, [tabId]: title } };
  });
}

export function resetTerminalView(tabId?: string): void {
  useTerminalViewStore.setState((state) => {
    if (tabId === undefined) {
      if (
        Object.keys(state.linesBelow).length === 0 &&
        Object.keys(state.atTop).length === 0 &&
        Object.keys(state.titles).length === 0
      ) {
        return state;
      }
      return { linesBelow: {}, atTop: {}, titles: {} };
    }
    if (state.linesBelow[tabId] === undefined && state.atTop[tabId] === undefined && state.titles[tabId] === undefined) {
      return state;
    }
    const linesBelow = { ...state.linesBelow };
    const atTop = { ...state.atTop };
    const titles = { ...state.titles };
    delete linesBelow[tabId];
    delete atTop[tabId];
    delete titles[tabId];
    return { linesBelow, atTop, titles };
  });
}

// The narrow reset for title-owning paths (user rename, backend custom title): only the
// OSC-derived view title is dropped; the scrollback badge state under the same key and
// every other tab's entries are untouched. resetTerminalView stays the whole-tab
// teardown for unmount and account cleanup.
export function resetTerminalTabTitle(tabId: string): void {
  useTerminalViewStore.setState((state) => {
    if (state.titles[tabId] === undefined) return state;
    const titles = { ...state.titles };
    delete titles[tabId];
    return { titles };
  });
}

// The narrow reset for the scrollback owner (XtermView): a terminal remount clears only
// its own scroll position entries, never the tab's OSC title, which outlives the terminal.
export function resetTerminalLinesBelow(tabId: string): void {
  useTerminalViewStore.setState((state) => {
    if (state.linesBelow[tabId] === undefined && state.atTop[tabId] === undefined) return state;
    const linesBelow = { ...state.linesBelow };
    const atTop = { ...state.atTop };
    delete linesBelow[tabId];
    delete atTop[tabId];
    return { linesBelow, atTop };
  });
}

// An authoritative custom title (a user rename, an AI rename or a backend custom title)
// lands on the workspace tab with renamedByUser and its decision time — locking out OSC
// title updates — and clears only the OSC-derived view title: the scrollback badge under
// the same key must survive.
export function applyAuthoritativeTabTitle(tabId: string, title: string, at: number = Date.now()): void {
  const tab = findTab(tabId)?.tab;
  if (tab && (tab.title !== title || !hasCustomTitle(tab))) {
    useUi.getState().updateTab(tabId, { title, renamedByUser: true, customTitleAt: at });
  }
  resetTerminalTabTitle(tabId);
}

// Clearing a custom title (an empty rename) hands the tab strip back to OSC titles. The
// clear is a title decision too, so its time is kept to order it against stale renames.
// The fallback is the backend's current program title, else the tab's default name source
// (the session/asset name).
export function clearAuthoritativeTabTitle(tabId: string, fallback: string, at: number = Date.now()): void {
  const found = findTab(tabId);
  if (!found) return;
  const session = useUi.getState().sessions.find((item) => item.id === found.tab.sessionId);
  const title = sanitizeRemoteTabTitle(fallback) || session?.name || found.ws.title || found.tab.title;
  useUi.getState().updateTab(tabId, { title, renamedByUser: undefined, customTitleAt: at });
  resetTerminalTabTitle(tabId);
}

export interface BackendTabTitle {
  tabId: string;
  title?: string;
  titleCustom?: boolean;
  titleChangedAt?: number;
}

// The backend reports a tab title with titleCustom telling a custom title (a manual or AI
// rename, or a restored durable title) apart from the OSC-derived program title it tracks
// otherwise, and titleChangedAt, the time of its last explicit title decision. The newer
// decision wins: a backend decision older than the local one never replaces it (a fresh
// kernel tab after a reconnect reports none), and a newer one — a rename or clear made on
// another device or by the AI — is adopted. A program title only goes through the OSC
// path, which never replaces a custom title.
//
// On a fresh attachment (rename given) a newer local decision the backend does not hold
// is written back so every device and the AI tools see it. Returns that write, or null.
export function reconcileBackendTabTitle(
  tabId: string,
  info: BackendTabTitle,
  rename: ((kernelTabId: string, title: string, at: number) => Promise<unknown>) | null,
): Promise<unknown> | null {
  const tab = findTab(tabId)?.tab;
  if (!tab) return null;
  const custom = info.titleCustom === true;
  const title = info.title ?? "";
  const backendAt = info.titleChangedAt ?? 0;
  const localAt = tab.customTitleAt;
  if (localAt !== undefined && localAt > backendAt) {
    if (hasCustomTitle(tab)) {
      resetTerminalTabTitle(tabId);
      if (custom && title === tab.title) return null;
      return rename && tab.title.trim() !== "" ? rename(info.tabId, tab.title, localAt) : null;
    }
    // A newer local clear: the backend's custom title is stale.
    if (custom) return rename ? rename(info.tabId, "", localAt) : null;
    if (title) applyRemoteTabTitleToView(tabId, title);
    return null;
  }
  if (custom) {
    if (title) applyAuthoritativeTabTitle(tabId, title, backendAt || Date.now());
    return null;
  }
  if (hasCustomTitle(tab)) {
    // Only a backend clear newer than the local rename reaches here.
    clearAuthoritativeTabTitle(tabId, title, backendAt);
    return null;
  }
  if (title) applyRemoteTabTitleToView(tabId, title);
  return null;
}

// The OSC title path writes through the authoritative workspace-store application first:
// only a title the workspace store actually accepted (tab exists, not user-renamed,
// non-empty after sanitizing, changed) lands in the per-tab view state, so custom-title
// precedence holds for every precise-selector consumer.
export function applyRemoteTabTitleToView(tabId: string, rawTitle: string): boolean {
  if (!applyRemoteTabTitle(tabId, rawTitle)) return false;
  setTerminalTabTitle(tabId, sanitizeRemoteTabTitle(rawTitle));
  return true;
}
