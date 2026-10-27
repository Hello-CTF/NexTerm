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
  download: {
    running: boolean;
    precision: PrivacyPrecision;
    progress: { file: string; downloaded: number; total: number };
    error?: string;
  };
}
export const privacyApi = {
  status: () => call<PrivacyStatus>("ai_privacy_status", {}),
  configure: (config: PrivacyConfig) => call<PrivacyStatus>("ai_privacy_configure", { enabled: config.enabled, precision: config.precision, revision: config.revision }),
  checkUpdates: () => call<PrivacyRelease>("ai_privacy_check_updates", {}),
  download: (precision: PrivacyPrecision) => call<PrivacyStatus>("ai_privacy_download", { precision }),
  cancelDownload: () => call<void>("ai_privacy_cancel_download", {}),
};
