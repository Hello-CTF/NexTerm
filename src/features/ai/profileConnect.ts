import { call } from "../../ipc/commands";
import type { ModelProfileTimeouts } from "./modelLifecycle";

export interface ProviderTestResult {
  modelsOk: boolean;
  modelsError?: string | null;
  chatOk: boolean;
  chatError?: string | null;
}

export interface ProfileModelList {
  models: string[];
  malformed: number;
}

export function testProfileConnection(id: string): Promise<ProviderTestResult> {
  return call<ProviderTestResult>("ai_test_provider", { id });
}

export async function fetchProfileModels(profile: ModelProfileTimeouts): Promise<ProfileModelList> {
  const result = await call<ProfileModelList | string[] | null>("ai_model_refresh", { profile });
  if (!result) return { models: [], malformed: 0 };
  if (Array.isArray(result)) return { models: result, malformed: 0 };
  return {
    models: Array.isArray(result.models) ? result.models : [],
    malformed: typeof result.malformed === "number" && result.malformed > 0 ? Math.floor(result.malformed) : 0,
  };
}
