import { create } from "zustand";

const KEY = "nexterm.hiddenAssets.v1";

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

interface AssetVisibilityState {
  hiddenIds: string[];
  showHidden: boolean;
  setShowHidden: (v: boolean) => void;
  hide: (id: string) => void;
  unhide: (id: string) => void;
}

export const useAssetVisibility = create<AssetVisibilityState>((set) => ({
  hiddenIds: load(),
  showHidden: false,
  setShowHidden: (showHidden) => set({ showHidden }),
  hide: (id) =>
    set((s) => {
      if (s.hiddenIds.includes(id)) return s;
      const hiddenIds = [...s.hiddenIds, id];
      save(hiddenIds);
      return { hiddenIds };
    }),
  unhide: (id) =>
    set((s) => {
      if (!s.hiddenIds.includes(id)) return s;
      const hiddenIds = s.hiddenIds.filter((x) => x !== id);
      save(hiddenIds);
      return { hiddenIds };
    }),
}));
