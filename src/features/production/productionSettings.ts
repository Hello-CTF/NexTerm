// Per-owner production safety settings (R-01 configurable production list +
// R-07 connect opt-ins) and the read-only classification used by the
// workspace/terminal/AI visual warnings. The M15 IPC is the only persistence
// source; every value defaults to off/empty server-side. Classification is a
// display aid computed from the owner's configured list plus the built-in
// names — the backend remains the final authorization boundary and never
// infers authorization from session memory.

import { useSyncExternalStore } from "react";
import { assetApi, call } from "../../ipc/commands";
import { useUi } from "../../app/store";
import type { ProductionSettingsDTO, ProductionSettingsUpdateRequest } from "../../ipc/generated/types";
import { assetEnvironment } from "./assetEnvironment";
import { goEqualFold, goTrimSpace } from "./goFold";

export type { ProductionSettingsDTO, ProductionSettingsUpdateRequest };

export const productionSettingsApi = {
  // Deferred invocation: a synchronous transport failure becomes a rejection
  // the settings surfaces can catch and show, never a render/effect crash.
  get: () => Promise.resolve().then(() => call("production_settings_get")),
  set: (patch: ProductionSettingsUpdateRequest) => Promise.resolve().then(() => call("production_settings_set", patch)),
};

// Built-in names mirror guard.builtInProductionEnvironments: a configuration
// mistake cannot unguard them.
export const BUILT_IN_PRODUCTION_ENVIRONMENTS: readonly string[] = ["production", "prod"];

export function isProductionEnvironment(environment: string, configured: readonly string[]): boolean {
  const name = goTrimSpace(environment);
  if (name === "") return false;
  return [...BUILT_IN_PRODUCTION_ENVIRONMENTS, ...configured].some((candidate) =>
    goEqualFold(goTrimSpace(candidate), name),
  );
}

export interface AssetProductionClassification {
  environment: string;
  production: boolean;
}

interface ClassificationState {
  epoch: number;
  version: number;
  environments: Map<string, string>;
  configured: string[];
  loaded: boolean;
}

let state: ClassificationState = { epoch: -1, version: 0, environments: new Map(), configured: [], loaded: false };
let loadPromise: Promise<void> | null = null;
const listeners = new Set<() => void>();
let unsubscribeAccount: (() => void) | null = null;

function notify(): void {
  state = { ...state, version: state.version + 1 };
  for (const listener of listeners) listener();
}

function resetClassification(): void {
  state = { epoch: useUi.getState().accountEpoch, version: state.version, environments: new Map(), configured: [], loaded: false };
  loadPromise = null;
  notify();
  // Existing subscribers must not keep stale per-owner classifications: reload
  // for the new epoch immediately.
  if (listeners.size > 0) ensureLoaded();
}

async function loadClassification(epoch: number): Promise<void> {
  try {
    const [assets, settings] = await Promise.all([assetApi.list(), productionSettingsApi.get()]);
    if (useUi.getState().accountEpoch !== epoch) return;
    const environments = new Map<string, string>();
    for (const asset of assets) {
      const environment = assetEnvironment(typeof asset.tags === "string" ? asset.tags : "");
      if (environment !== "") environments.set(asset.id, environment);
    }
    state = {
      epoch,
      version: state.version,
      environments,
      configured: Array.isArray(settings.productionEnvironments) ? settings.productionEnvironments : [],
      loaded: true,
    };
  } catch {
    if (useUi.getState().accountEpoch !== epoch) return;
    state = { epoch, version: state.version, environments: new Map(), configured: [], loaded: false };
  }
  notify();
}

function ensureLoaded(): void {
  const epoch = useUi.getState().accountEpoch;
  if (state.epoch !== epoch) {
    state = { epoch, version: state.version, environments: new Map(), configured: [], loaded: false };
    loadPromise = null;
  }
  if (loadPromise) return;
  loadPromise = loadClassification(epoch).finally(() => {
    loadPromise = null;
  });
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  if (unsubscribeAccount === null) {
    let previousEpoch = useUi.getState().accountEpoch;
    unsubscribeAccount = useUi.subscribe((current) => {
      if (current.accountEpoch === previousEpoch) return;
      previousEpoch = current.accountEpoch;
      resetClassification();
    });
  }
  ensureLoaded();
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0 && unsubscribeAccount !== null) {
      unsubscribeAccount();
      unsubscribeAccount = null;
    }
  };
}

// Classify an asset for the visual warnings. Unknown or unloaded assets stay
// unclassified (no warning) rather than guessing from local heuristics.
export function classifyAssetProduction(assetId: string | null | undefined): AssetProductionClassification | null {
  if (!assetId) return null;
  const environment = state.environments.get(assetId);
  if (environment === undefined) return null;
  return { environment, production: isProductionEnvironment(environment, state.configured) };
}

export function useAssetProductionClassification(assetId: string | null | undefined): AssetProductionClassification | null {
  useSyncExternalStore(subscribe, () => state.version);
  return classifyAssetProduction(assetId);
}

// Test hook: drop cached state so the next subscribe reloads.
export function resetProductionClassificationCache(): void {
  resetClassification();
}
