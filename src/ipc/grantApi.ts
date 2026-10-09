import { call } from "./commands";

export type AiGrantKind = "terminal_write" | "session_exec";

export interface AiDeviceGrant {
  deviceId: string;
  kinds: AiGrantKind[];
  updatedAt: number;
}

export type AiGrantRuleAction = "write_file" | "edit_file" | "exec_commands" | "send_keys";

export interface AiGrantRule {
  id: string;
  deviceId: string;
  action: AiGrantRuleAction;
  path: string;
  expiresAt: number;
  createdAt: number;
  updatedAt: number;
}

export const grantApi = {
  list: () => call<AiDeviceGrant[]>("ai_grant_list"),
  set: (deviceId: string, kinds: AiGrantKind[]) =>
    call<AiDeviceGrant>("ai_grant_set", { deviceId, kinds }),
  revoke: (deviceId: string) => call<void>("ai_grant_revoke", { deviceId }),
  ruleList: () => call<AiGrantRule[]>("ai_grant_rule_list"),
  ruleSet: (deviceId: string, action: AiGrantRuleAction, path: string, expiresAt: number) =>
    call<AiGrantRule>("ai_grant_rule_set", { deviceId, action, path, expiresAt }),
  ruleRevoke: (id: string) => call<void>("ai_grant_rule_revoke", { id }),
};
