import { call } from "./commands";

export type PrivacyPrecision = "int8" | "fp32" | "fp16";
export interface PrivacyRelease { version: string; revision: string }
export interface PrivacyInstallation extends PrivacyRelease {
  precision: PrivacyPrecision;
  files: Record<string, { size: number; sha256: string }>;
}
export interface PrivacyConfig extends PrivacyRelease {
  enabled: boolean;
  precision: PrivacyPrecision;
}
export interface PrivacyStatus {
  config: PrivacyConfig;
  installed: PrivacyInstallation[];
  latest?: PrivacyRelease;
  newerIncompatible?: PrivacyRelease;
  latestSizes?: Partial<Record<PrivacyPrecision, number>>;
  runtime: { supported: boolean; reason?: string };
  selection?: { available: boolean; error?: string };
  diskUsage: number;
  canManage: boolean;
  download: {
    running: boolean;
    precision: PrivacyPrecision;
    progress: { file: string; downloaded: number; total: number };
    error?: string;
    detail?: string;
    canceled?: boolean;
    completed?: PrivacyRelease;
  };
}

// The chat indicator follows the latest status any caller fetched.
type Listener = (status: PrivacyStatus) => void;
const listeners = new Set<Listener>();
export function onPrivacyStatus(listener: Listener): () => void {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}
const publish = (status: PrivacyStatus) => {
  for (const listener of listeners) listener(status);
  return status;
};

/** Protection is on but requests are blocked: missing model, failed load or unsupported device. */
export const privacyBroken = (status: PrivacyStatus) =>
  status.config.enabled && (!status.runtime.supported || status.selection?.available === false);

export const privacyApi = {
  status: () => call<PrivacyStatus>("ai_privacy_status", {}).then(publish),
  configure: (config: PrivacyConfig) => call<PrivacyStatus>("ai_privacy_configure", { enabled: config.enabled, precision: config.precision, revision: config.revision }).then(publish),
  checkUpdates: () => call<PrivacyRelease>("ai_privacy_check_updates", {}),
  download: (precision: PrivacyPrecision) => call<PrivacyStatus>("ai_privacy_download", { precision }).then(publish),
  remove: (revision: string, precision: PrivacyPrecision) => call<PrivacyStatus>("ai_privacy_remove", { revision, precision }).then(publish),
  cancelDownload: () => call<void>("ai_privacy_cancel_download", {}),
};
