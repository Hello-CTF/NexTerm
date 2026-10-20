import { create } from "zustand";

import {
  parseProjectGroupCollapse,
  sanitizeProjectGroupCollapse,
  serializeProjectGroupCollapse,
  toggleProjectGroupCollapse,
} from "../../app/projectFocus";

const STORAGE_PREFIX = "nexterm:collapsed-groups-v1:";
const LEGACY_KEY = "nexterm.collapsedAssetGroups.v1";

function storageKey(accountId: string): string {
  return `${STORAGE_PREFIX}${encodeURIComponent(accountId)}`;
}

// loadLegacy reads the pre-per-account global list with its original semantics
// (non-array or malformed JSON degrade to an empty list, non-string items are dropped).
function loadLegacy(): string[] | null {
  try {
    const raw = window.localStorage.getItem(LEGACY_KEY);
    if (raw === null) return null;
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return sanitizeProjectGroupCollapse(parsed.filter((x): x is string => typeof x === "string"));
  } catch {
    return [];
  }
}

function load(accountId: string): string[] {
  try {
    const raw = window.localStorage.getItem(storageKey(accountId));
    // The per-account key is authoritative whenever it exists, even when corrupt.
    if (raw !== null) return parseProjectGroupCollapse(raw);
    const legacy = loadLegacy();
    if (legacy === null) return [];
    // One-time migration, write-before-delete: the legacy key is only removed after the
    // per-account value is confirmed on disk, so a failed write keeps the legacy key and
    // the migration is retried on a later boot instead of being silently consumed.
    if (save(accountId, legacy)) {
      window.localStorage.removeItem(LEGACY_KEY);
    }
    return legacy;
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
