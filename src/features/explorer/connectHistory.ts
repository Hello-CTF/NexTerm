import { create } from "zustand";

const KEY = "nexterm.connectHistory.v1";
const MAX_ENTRIES = 100;
const RECENCY_HALF_LIFE_MS = 3 * 24 * 3_600_000;

export interface ConnectHistoryEntry {
  count: number;
  at: number;
}

export type ConnectHistoryEntries = Record<string, ConnectHistoryEntry>;

function load(): ConnectHistoryEntries {
  try {
    const raw = localStorage.getItem(KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : {};
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const entries: ConnectHistoryEntries = {};
    for (const [id, value] of Object.entries(parsed as Record<string, unknown>)) {
      if (!value || typeof value !== "object") continue;
      const { count, at } = value as { count?: unknown; at?: unknown };
      if (typeof count !== "number" || typeof at !== "number") continue;
      entries[id] = { count, at };
    }
    return entries;
  } catch {
    return {};
  }
}

function save(entries: ConnectHistoryEntries): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(entries));
  } catch {
  }
}

interface ConnectHistoryState {
  entries: ConnectHistoryEntries;
  record: (assetId: string) => void;
}

export const useConnectHistory = create<ConnectHistoryState>((set) => ({
  entries: load(),
  record: (assetId) =>
    set((s) => {
      const prev = s.entries[assetId];
      const entries: ConnectHistoryEntries = {
        ...s.entries,
        [assetId]: { count: (prev?.count ?? 0) + 1, at: Date.now() },
      };
      const ids = Object.keys(entries);
      if (ids.length > MAX_ENTRIES) {
        const drop = ids
          .sort((a, b) => entries[a].at - entries[b].at)
          .slice(0, ids.length - MAX_ENTRIES);
        for (const id of drop) delete entries[id];
      }
      save(entries);
      return { entries };
    }),
}));

export function quickConnectScore(entry: ConnectHistoryEntry | undefined, now: number): number {
  if (!entry) return 0;
  const age = Math.max(0, now - entry.at);
  const recency = Math.pow(0.5, age / RECENCY_HALF_LIFE_MS);
  return recency + Math.min(entry.count, 10) * 0.05;
}
