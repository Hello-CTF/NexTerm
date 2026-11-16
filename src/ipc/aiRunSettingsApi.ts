import { call } from "./commands";
import type { RunSettings, RunSettingsInput } from "./generated/types";

export type { RunSettings as AiRunSettings, RunSettingsInput as AiRunSettingsInput };

// The backend accepts a step limit in this range and falls back to the default for anything else it finds stored.
export const AI_MAX_TURNS_MIN = 1;
export const AI_MAX_TURNS_MAX = 999;
export const AI_MAX_TURNS_DEFAULT = 999;

export const aiRunSettingsApi = {
  get: () => call("ai_run_settings_get", {}),
  set: (input: RunSettingsInput) => call("ai_run_settings_set", { ...input }),
};
