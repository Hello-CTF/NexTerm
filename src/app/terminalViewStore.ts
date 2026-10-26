import { create } from "zustand";
import { applyRemoteTabTitle, findTab, sanitizeRemoteTabTitle, useUi } from "./store";

// Per-terminal view state that changes at output frequency (scrollback position, remote title).
// Components subscribe through the narrow selectors below so a burst on one tab re-renders only
// that tab's chrome; unchanged values are no-ops and never trigger a render.
// titles is keyed by the workspace storeTabId (the id applyRemoteTabTitle and the tab strip
// know); linesBelow is keyed by the owning pane's stable view id (the storeTabId TerminalPane
// passes as viewTabId — never the shared pre-attach "pending" placeholder), so concurrent
// attaching tabs cannot contaminate each other and the value survives the attach transition.
interface TerminalViewState {
  linesBelow: Record<string, number>;
  titles: Record<string, string>;
}

export const useTerminalViewStore = create<TerminalViewState>()(() => ({
  linesBelow: {},
  titles: {},
}));

export function useTerminalLinesBelow(tabId: string): number {
  return useTerminalViewStore((state) => state.linesBelow[tabId] ?? 0);
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

export function setTerminalTabTitle(tabId: string, title: string): void {
  useTerminalViewStore.setState((state) => {
    if (state.titles[tabId] === title) return state;
    return { titles: { ...state.titles, [tabId]: title } };
  });
}

export function resetTerminalView(tabId?: string): void {
  useTerminalViewStore.setState((state) => {
    if (tabId === undefined) {
      if (Object.keys(state.linesBelow).length === 0 && Object.keys(state.titles).length === 0) return state;
      return { linesBelow: {}, titles: {} };
    }
    if (state.linesBelow[tabId] === undefined && state.titles[tabId] === undefined) return state;
    const linesBelow = { ...state.linesBelow };
    const titles = { ...state.titles };
    delete linesBelow[tabId];
    delete titles[tabId];
    return { linesBelow, titles };
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
// its own linesBelow entry, never the tab's OSC title, which outlives the terminal.
export function resetTerminalLinesBelow(tabId: string): void {
  useTerminalViewStore.setState((state) => {
    if (state.linesBelow[tabId] === undefined) return state;
    const linesBelow = { ...state.linesBelow };
    delete linesBelow[tabId];
    return { linesBelow };
  });
}

// An authoritative custom title (a user rename or a backend-owned title) lands on the
// workspace tab with renamedByUser — locking out OSC title updates — and clears only
// the OSC-derived view title: the scrollback badge under the same key must survive.
export function applyAuthoritativeTabTitle(tabId: string, title: string): void {
  const tab = findTab(tabId)?.tab;
  if (tab && (tab.title !== title || tab.renamedByUser !== true)) {
    useUi.getState().updateTab(tabId, { title, renamedByUser: true });
  }
  resetTerminalTabTitle(tabId);
}

// Clearing a custom title (an empty rename) hands the tab strip back to OSC titles:
// the fallback is the backend's current program title when it reported one.
export function clearAuthoritativeTabTitle(tabId: string, fallback: string): void {
  const tab = findTab(tabId)?.tab;
  if (!tab) return;
  const title = sanitizeRemoteTabTitle(fallback) || "终端";
  useUi.getState().updateTab(tabId, { title, renamedByUser: undefined });
  resetTerminalTabTitle(tabId);
}

// The backend reports a tab title with titleCustom telling a custom title (a manual
// or AI rename, or a restored durable title) apart from the OSC-derived program
// title it tracks otherwise. Only a custom title is authoritative; a program title
// goes through the OSC path and never replaces a title the user chose.
export function applyBackendTabTitle(tabId: string, title: string | undefined, custom: boolean | undefined): void {
  if (!title) return;
  if (custom === true) {
    applyAuthoritativeTabTitle(tabId, title);
    return;
  }
  applyRemoteTabTitleToView(tabId, title);
}

// A fresh attachment reconciles both sides: a backend custom title wins; a local
// custom title the backend does not hold (a kernel tab re-created by a reconnect,
// or a rename made before the tab had a kernel id) is written back so every device
// and the AI tools see it. Returns the rename to await, or null when none is needed.
export function reconcileAttachedTabTitle(
  tabId: string,
  info: { tabId: string; title?: string; titleCustom?: boolean },
  rename: ((kernelTabId: string, title: string) => Promise<unknown>) | null,
): Promise<unknown> | null {
  if (info.titleCustom === true) {
    applyBackendTabTitle(tabId, info.title, true);
    return null;
  }
  const tab = findTab(tabId)?.tab;
  if (tab?.renamedByUser === true) {
    resetTerminalTabTitle(tabId);
    return rename && tab.title.trim() !== "" ? rename(info.tabId, tab.title) : null;
  }
  applyBackendTabTitle(tabId, info.title, false);
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
