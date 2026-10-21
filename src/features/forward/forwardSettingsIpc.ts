import { call } from "../../ipc/commands";
import type { ForwardRecoverySettings, ForwardRuleSettings, ForwardSettingsApi } from "./forwardSettingsApi";

// Production IPC adapter for the R-11/R-12 settings slice. The backend keeps
// every guardrail: saved rules are loopback-only local forwards by
// construction, remote/SOCKS rules cannot be persisted, the recovery switch
// stays default-off, and recovery skips production assets and never accepts
// an unknown host key on its own.
export function createForwardSettingsIpcApi(): ForwardSettingsApi {
  return {
    listRules: async (): Promise<ForwardRuleSettings[]> => {
      const rules = await call("forward_rule_settings_list");
      return rules.map((rule) => ({
        id: rule.id,
        assetId: rule.assetId,
        name: rule.name,
        kind: rule.kind,
        listenHost: rule.listenHost,
        listenPort: rule.listenPort,
        targetHost: rule.targetHost,
        targetPort: rule.targetPort,
        autoStart: rule.autoStart,
        status: rule.status,
      }));
    },
    setRuleAutoStart: async (id: string, autoStart: boolean): Promise<void> => {
      await call("forward_rule_set_auto_start", { id, autoStart });
    },
    getRecoverySettings: async (): Promise<ForwardRecoverySettings> => {
      const settings = await call("forward_rule_recovery_settings_get");
      return {
        enabled: settings.enabled,
        maxConcurrentConnections: settings.maxConcurrentConnections,
        skipProduction: settings.skipProduction,
      };
    },
    setRecoverySettings: async (settings: ForwardRecoverySettings): Promise<void> => {
      await call("forward_rule_recovery_settings_set", { enabled: settings.enabled });
    },
    recoverNow: async (): Promise<void> => {
      const outcomes = await call("forward_rule_recover_now");
      // Deliberate skips (production assets) are documented in the panel;
      // real failures stay visible instead of being swallowed.
      const failures = outcomes.filter((outcome) => typeof outcome.error === "string" && outcome.error !== "");
      if (failures.length > 0) {
        throw new Error(failures.map((outcome) => `${outcome.assetId}: ${outcome.error}`).join("\n"));
      }
    },
  };
}
