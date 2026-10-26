import { clearAiTabRun, clearAiTabRuns } from "../../features/ai/runOwnership";
import { moveItem } from "../tabReorder";
import { makeAiBoard, makeAiDockTab } from "./model";
import { clearRestoredTakeover, peekRestoredTakeover, persistTakeover } from "./persistence";
import type { UiGet, UiSet, UiState } from "./types";

export function createAiSlice(set: UiSet, get: UiGet) {
  return {
    aiBusy: false,
    takeover: peekRestoredTakeover(),
    aiBoards: {},
    aiAccountEpoch: 0,
    modelProfilesRevision: 0,
    bumpModelProfilesRevision: () =>
      set((state) => ({ modelProfilesRevision: state.modelProfilesRevision + 1 })),
    aiRulePrefill: null,
    setAiRulePrefill: (v) => set({ aiRulePrefill: v }),
    setAiBusy: (v) => set({ aiBusy: v }),
    setTakeover: (t) => {
      clearRestoredTakeover();
      persistTakeover(t);
      set({ takeover: t });
    },
    resetAiAccount: () => {
      const tabIds = Object.values(get().aiBoards).flatMap((board) => board.tabs.map((tab) => tab.id));
      clearAiTabRuns(tabIds);
      get().setTakeover(null);
      set((state) => ({ aiBoards: {}, aiBusy: false, aiAccountEpoch: state.aiAccountEpoch + 1 }));
    },
    ensureAiBoard: (key) => {
      const existing = get().aiBoards[key];
      if (existing) return existing;
      const board = makeAiBoard();
      set((s) => (s.aiBoards[key] ? s : { aiBoards: { ...s.aiBoards, [key]: board } }));
      return get().aiBoards[key];
    },
    addAiTab: (key) => {
      get().ensureAiBoard(key);
      const tab = makeAiDockTab();
      set((s) => {
        const board = s.aiBoards[key];
        if (!board) return s;
        return { aiBoards: { ...s.aiBoards, [key]: { tabs: [...board.tabs, tab], activeTabId: tab.id } } };
      });
      return tab.id;
    },
    closeAiTab: (key, tabId, expectedEpoch) =>
      set((s) => {
        if (expectedEpoch !== undefined && s.aiAccountEpoch !== expectedEpoch) return s;
        const board = s.aiBoards[key];
        if (!board) return s;
        if (board.tabs.length <= 1) {
          if (board.tabs[0]?.id !== tabId) return s;
          clearAiTabRun(tabId);
          return { aiBoards: { ...s.aiBoards, [key]: makeAiBoard() } };
        }
        const index = board.tabs.findIndex((t) => t.id === tabId);
        if (index < 0) return s;
        const tabs = board.tabs.filter((t) => t.id !== tabId);
        const activeTabId =
          board.activeTabId === tabId
            ? (tabs[Math.min(index, tabs.length - 1)]?.id ?? null)
            : board.activeTabId;
        return { aiBoards: { ...s.aiBoards, [key]: { tabs, activeTabId } } };
      }),
    setActiveAiTab: (key, tabId) =>
      set((s) => {
        const board = s.aiBoards[key];
        if (!board || !board.tabs.some((t) => t.id === tabId)) return s;
        return { aiBoards: { ...s.aiBoards, [key]: { ...board, activeTabId: tabId } } };
      }),
    moveAiTab: (key, fromIndex, toIndex) =>
      set((s) => {
        const board = s.aiBoards[key];
        if (!board) return s;
        const tabs = moveItem(board.tabs, fromIndex, toIndex);
        if (tabs === board.tabs) return s;
        return { aiBoards: { ...s.aiBoards, [key]: { ...board, tabs } } };
      }),
    updateAiTab: (key, tabId, patch, expectedEpoch) =>
      set((s) => {
        if (expectedEpoch !== undefined && s.aiAccountEpoch !== expectedEpoch) return s;
        const board = s.aiBoards[key];
        if (!board || !board.tabs.some((t) => t.id === tabId)) return s;
        return {
          aiBoards: {
            ...s.aiBoards,
            [key]: { ...board, tabs: board.tabs.map((t) => (t.id === tabId ? { ...t, ...patch } : t)) },
          },
        };
      }),
  } satisfies Partial<UiState>;
}
