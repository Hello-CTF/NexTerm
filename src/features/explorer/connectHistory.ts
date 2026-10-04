import { create } from "zustand";

const KEY = "nexterm.connectHistory.v1";
const MAX_ENTRIES = 100;
const RECENCY_HALF_LIFE_MS = 3 * 24 * 3_600_000;

export interface ConnectHistoryEntry {
  count: number;
  at: number;
}

export type ConnectHistoryEntries = Record<string, ConnectHistoryEntry>;

export interface QuickConnectAsset {
  id: string;
  name: string;
  host: string | null;
  username: string | null;
  tags: string;
}

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

function matchRank(asset: QuickConnectAsset, query: string): number | null {
  const name = asset.name.toLowerCase();
  if (name.startsWith(query)) return 0;
  if (name.includes(query)) return 1;
  const fields = [asset.host, asset.username, asset.tags];
  if (fields.some((field) => field && field.toLowerCase().includes(query))) return 2;
  return null;
}

function compareName(a: QuickConnectAsset, b: QuickConnectAsset): number {
  if (a.name === b.name) return 0;
  return a.name < b.name ? -1 : 1;
}

export function orderQuickConnectAssets<T extends QuickConnectAsset>(
  assets: T[],
  entries: ConnectHistoryEntries,
  query: string,
  now: number = Date.now(),
): T[] {
  const q = query.trim().toLowerCase();
  const scored = assets.map((asset) => ({
    asset,
    rank: q.length > 0 ? matchRank(asset, q) : 0,
  }));
  const kept = scored.filter((item): item is { asset: T; rank: number } => item.rank !== null);
  kept.sort(
    (x, y) =>
      x.rank - y.rank ||
      quickConnectScore(entries[y.asset.id], now) - quickConnectScore(entries[x.asset.id], now) ||
      compareName(x.asset, y.asset),
  );
  return kept.map((item) => item.asset);
}
