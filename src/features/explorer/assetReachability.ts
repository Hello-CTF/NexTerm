import { create } from "zustand";
import { assetApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { DEMO } from "../../demo";

export type ReachabilityState = "checking" | "reachable" | "unreachable";

export interface ReachabilityEntry {
  state: ReachabilityState;
  error?: string;
  durationMs?: number;
  at: number;
}

export const REACHABILITY_PROBE_TIMEOUT_MS = 2500;
export const REACHABILITY_MAX_CONCURRENT = 8;
export const REACHABILITY_BATCH_LIMIT = 32;

interface AssetReachabilityState {
  entries: Record<string, ReachabilityEntry>;
  batchError: string | null;
  probe: (assetIds: string[]) => Promise<void>;
  reset: () => void;
}

const generations = new Map<string, number>();
const inflight = new Map<string, Promise<void>>();

export const useAssetReachability = create<AssetReachabilityState>((set) => ({
  entries: {},
  batchError: null,
  probe: async (assetIds) => {
    if (DEMO) return;
    const ids = [...new Set(assetIds)].slice(0, REACHABILITY_BATCH_LIMIT);
    if (ids.length === 0) return;
    const myGen = new Map<string, number>();
    const checkingAt = Date.now();
    const checking: Record<string, ReachabilityEntry> = {};
    for (const id of ids) {
      const gen = (generations.get(id) ?? 0) + 1;
      generations.set(id, gen);
      myGen.set(id, gen);
      checking[id] = { state: "checking", at: checkingAt };
    }
    set((s) => ({ entries: { ...s.entries, ...checking }, batchError: null }));

    const owned = (id: string) => generations.get(id) === myGen.get(id);
    const run = (async () => {
      try {
        const res = await assetApi.probeBatch(
          ids,
          REACHABILITY_PROBE_TIMEOUT_MS,
          REACHABILITY_MAX_CONCURRENT,
        );
        const byId = new Map(res.results.map((result) => [result.assetId, result]));
        set((s) => {
          const entries = { ...s.entries };
          for (const id of ids) {
            if (!owned(id)) continue;
            const result = byId.get(id);
            if (!result) {
              delete entries[id];
              continue;
            }
            entries[id] = result.reachable
              ? { state: "reachable", durationMs: result.durationMs, at: Date.now() }
              : {
                  state: "unreachable",
                  error: result.error,
                  durationMs: result.durationMs,
                  at: Date.now(),
                };
          }
          return { entries };
        });
      } catch (e) {
        const message = describeError(e);
        set((s) => {
          const entries = { ...s.entries };
          let anyOwned = false;
          for (const id of ids) {
            if (!owned(id)) continue;
            anyOwned = true;
            if (entries[id]?.state === "checking") delete entries[id];
          }
          return anyOwned ? { entries, batchError: message } : { entries };
        });
      }
    })();
    for (const id of ids) inflight.set(id, run);
    try {
      await run;
    } finally {
      for (const id of ids) {
        if (inflight.get(id) === run) inflight.delete(id);
      }
    }
  },
  reset: () => {
    generations.clear();
    inflight.clear();
    set({ entries: {}, batchError: null });
  },
}));

export async function probeAssetReachability(assetId: string): Promise<ReachabilityEntry | null> {
  for (;;) {
    const pending = inflight.get(assetId);
    if (pending) {
      await pending;
    } else {
      await useAssetReachability.getState().probe([assetId]);
    }
    const entry = useAssetReachability.getState().entries[assetId];
    if (!entry) return null;
    if (entry.state !== "checking") return entry;
  }
}
