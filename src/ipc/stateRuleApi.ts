import { call } from "./commands";
import type {
  StateRuleDTO,
  StateRuleDraftDTO,
  StateRuleExportDTO,
  StateRuleHelperStatusDTO,
  StateRuleImportRequest,
  StateRuleImportResultDTO,
  StateRuleSaveRequest,
  StateRuleScreenDTO,
  StateRuleValidateResult,
} from "./generated/types";

export type {
  StateRuleDTO,
  StateRuleDraftDTO,
  StateRuleExportDTO,
  StateRuleHelperStatusDTO,
  StateRuleImportRequest,
  StateRuleImportResultDTO,
  StateRuleSaveRequest,
  StateRuleScreenDTO,
  StateRuleValidateResult,
};

// What the user says about the state a tab shows: it is wrong, or the screen needs them.
export type StateRuleIntent = "not_this" | "this_is_it";

export const stateRuleApi = {
  list: () => call("state_rule_list"),
  save: (rule: StateRuleSaveRequest) => call("state_rule_save", { ...rule }),
  delete: async (id: string): Promise<void> => {
    await call("state_rule_delete", { id });
  },
  reorder: (ids: string[]) => call("state_rule_reorder", { ids }),
  export: () => call("state_rule_export"),
  import: (file: StateRuleImportRequest) => call("state_rule_import", { ...file }),
  status: () => call("state_rule_status"),
  validate: (rule: StateRuleSaveRequest, screen?: StateRuleScreenDTO) =>
    call("state_rule_validate", screen ? { rule, screen } : { rule }),
  // draft sends the screen to the active AI model; the result is never saved by the backend.
  draft: (intent: StateRuleIntent, current: string, screen: StateRuleScreenDTO) =>
    call("state_rule_draft", { intent, current, screen }),
};
