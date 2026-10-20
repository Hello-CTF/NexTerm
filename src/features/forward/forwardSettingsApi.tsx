import { createContext, useContext, type ReactNode } from "react";

// Explicit contract for the R-11/R-12 settings slice. The production IPC
// adapter is registered as a follow-up once the IPC surface can carry the
// autoStart flag; until then callers inject an implementation through
// ForwardSettingsApiProvider, and tests inject fakes.
export interface ForwardRuleSettings {
  id: string;
  assetId: string;
  name: string;
  listenPort: number;
  targetHost: string;
  targetPort: number;
  autoStart: boolean;
  status: string;
}

export interface ForwardRecoverySettings {
  enabled: boolean;
  maxConcurrentConnections: number;
  skipProduction: boolean;
}

export const DEFAULT_FORWARD_RECOVERY_SETTINGS: ForwardRecoverySettings = {
  enabled: false,
  maxConcurrentConnections: 4,
  skipProduction: true,
};

export interface ForwardSettingsApi {
  listRules(): Promise<ForwardRuleSettings[]>;
  setRuleAutoStart(id: string, autoStart: boolean): Promise<void>;
  getRecoverySettings(): Promise<ForwardRecoverySettings>;
  setRecoverySettings(settings: ForwardRecoverySettings): Promise<void>;
  recoverNow(): Promise<void>;
}

const ForwardSettingsApiContext = createContext<ForwardSettingsApi | null>(null);

export function ForwardSettingsApiProvider(props: { api: ForwardSettingsApi; children?: ReactNode }) {
  return <ForwardSettingsApiContext.Provider value={props.api}>{props.children}</ForwardSettingsApiContext.Provider>;
}

export function useForwardSettingsApi(): ForwardSettingsApi {
  const api = useContext(ForwardSettingsApiContext);
  if (!api) throw new Error("ForwardSettingsApiProvider is not configured");
  return api;
}
