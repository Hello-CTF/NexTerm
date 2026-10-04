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

let probeRun = 0;

export const useAssetReachability = create<AssetReachabilityState>((set, get) => ({
  entries: {},
  batchError: null,
  probe: async (assetIds) => {
    if (DEMO) return;
    const ids = [...new Set(assetIds)]
      .slice(0, REACHABILITY_BATCH_LIMIT)
      .filter((id) => get().entries[id]?.state !== "checking");
    if (ids.length === 0) return;
    const run = ++probeRun;
    const checkingAt = Date.now();
    const checking: Record<string, ReachabilityEntry> = {};
    for (const id of ids) checking[id] = { state: "checking", at: checkingAt };
    set((s) => ({ entries: { ...s.entries, ...checking }, batchError: null }));
    try {
      const res = await assetApi.probeBatch(
        ids,
        REACHABILITY_PROBE_TIMEOUT_MS,
        REACHABILITY_MAX_CONCURRENT,
      );
      if (run !== probeRun) return;
      const byId = new Map(res.results.map((result) => [result.assetId, result]));
      set((s) => {
        const entries = { ...s.entries };
        for (const id of ids) {
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
      if (run !== probeRun) return;
      const message = describeError(e);
      set((s) => {
        const entries = { ...s.entries };
        for (const id of ids) {
          if (entries[id]?.state === "checking") delete entries[id];
        }
        return { entries, batchError: message };
      });
    }
  },
  reset: () => set({ entries: {}, batchError: null }),
}));

export async function probeAssetReachability(assetId: string): Promise<ReachabilityEntry | null> {
  await useAssetReachability.getState().probe([assetId]);
  return useAssetReachability.getState().entries[assetId] ?? null;
}
