import { create } from "zustand";

// Per-terminal view state that changes at output frequency (scrollback position, remote title).
// Components subscribe through the narrow selectors below so a burst on one tab re-renders only
// that tab's chrome; unchanged values are no-ops and never trigger a render.
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
