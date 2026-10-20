import { call } from "./commands";
import type { ForwardRule, ForwardRuleInput } from "./generated/types";

export type { ForwardRule, ForwardRuleInput };
export type ForwardRuleStatus = ForwardRule["status"];

export const forwardRuleApi = {
  list: () => call("forward_rule_list"),
  save: (rule: ForwardRuleInput) => call("forward_rule_save", { ...rule }),
  delete: async (id: string): Promise<void> => {
    await call("forward_rule_delete", { id });
  },
  start: (id: string, sessionId: string) => call("forward_rule_start", { id, sessionId }),
  stop: (id: string) => call("forward_rule_stop", { id }),
};
