import { create } from "zustand";

const KEY = "nexterm.collapsedAssetGroups.v1";

function load(): string[] {
  try {
    const raw = localStorage.getItem(KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed) ? parsed.filter((x): x is string => typeof x === "string") : [];
  } catch {
    return [];
  }
}

function save(ids: string[]): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(ids));
  } catch {
  }
}

interface GroupCollapseState {
  collapsedIds: string[];
  toggle: (id: string) => void;
}

export const useGroupCollapse = create<GroupCollapseState>((set) => ({
  collapsedIds: load(),
  toggle: (id) =>
    set((s) => {
      const collapsedIds = s.collapsedIds.includes(id)
        ? s.collapsedIds.filter((x) => x !== id)
        : [...s.collapsedIds, id];
      save(collapsedIds);
      return { collapsedIds };
    }),
}));
