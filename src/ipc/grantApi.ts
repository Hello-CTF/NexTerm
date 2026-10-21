import { call } from "./commands";
import type { DeviceGrant, GrantRule } from "./generated/types";

// The grant kinds the guard accepts; db_query covers the AI database tools and
// is only meaningful on database assets. The typed IPC contract carries the
// wire shape (DeviceGrant.permanent is explicit server-side); this union keeps
// the UI from offering kinds the server would reject.
export type AiGrantKind = "terminal_write" | "session_exec" | "db_query";

export type AiDeviceGrant = DeviceGrant;

export type AiGrantRuleAction = "write_file" | "edit_file" | "exec_commands" | "send_keys";

export type AiGrantRule = GrantRule;

export const grantApi = {
  list: () => call("ai_grant_list"),
  set: (deviceId: string, kinds: AiGrantKind[]) => call("ai_grant_set", { deviceId, kinds }),
  revoke: (deviceId: string) => call("ai_grant_revoke", { deviceId }),
  ruleList: () => call("ai_grant_rule_list"),
  ruleSet: (deviceId: string, action: AiGrantRuleAction, path: string, expiresAt: number) =>
    call("ai_grant_rule_set", { deviceId, action, path, expiresAt }),
  ruleRevoke: (id: string) => call("ai_grant_rule_revoke", { id }),
};
