import { create } from "zustand";

import {
  parseProjectGroupCollapse,
  serializeProjectGroupCollapse,
  toggleProjectGroupCollapse,
} from "../../app/projectFocus";

const STORAGE_PREFIX = "nexterm:collapsed-groups-v1:";

function storageKey(accountId: string): string {
  return `${STORAGE_PREFIX}${encodeURIComponent(accountId)}`;
}

function load(accountId: string): string[] {
  try {
    const raw = window.localStorage.getItem(storageKey(accountId));
    return raw === null ? [] : parseProjectGroupCollapse(raw);
  } catch {
    return [];
  }
}

function save(accountId: string, ids: string[]): boolean {
  try {
    window.localStorage.setItem(storageKey(accountId), serializeProjectGroupCollapse(ids));
    return true;
  } catch {
    return false;
  }
}

interface GroupCollapseState {
  accountId: string;
  collapsedIds: string[];
  // ensureAccount is called at layout boot and on every account switch; collapse state
  // is persisted per account so one account's groups never leak into the next.
  ensureAccount: (accountId: string) => void;
  toggle: (id: string) => void;
  resetForTest: () => void;
}

export const useGroupCollapse = create<GroupCollapseState>((set, get) => ({
  accountId: "",
  collapsedIds: [],
  ensureAccount: (accountId) => {
    if (get().accountId === accountId) return;
    set({ accountId, collapsedIds: load(accountId) });
  },
  toggle: (id) =>
    set((s) => {
      const collapsedIds = toggleProjectGroupCollapse(s.collapsedIds, id);
      save(s.accountId, collapsedIds);
      return { collapsedIds };
    }),
  resetForTest: () => set({ accountId: "", collapsedIds: [] }),
}));
