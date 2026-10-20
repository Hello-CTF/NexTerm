import { call } from "./commands";
import type {
  AdvanceRequest,
  ExecBatchRunHistory,
  PrepareRequest,
  RenderedCommand,
  RunRequest,
  RunState,
  StartRequest,
  Step,
  StepState,
} from "./generated/types";

export type {
  AdvanceRequest,
  ExecBatchRunHistory,
  PrepareRequest,
  RenderedCommand,
  RunRequest,
  RunState,
  StartRequest,
  Step,
  StepState,
};
export type RunbookStep = Step;
export type RunbookStepState = StepState;
export type RunbookRunState = RunState;
export type RunbookStartRequest = StartRequest;
export type RunbookPrepareRequest = PrepareRequest;
export type RunbookAdvanceRequest = AdvanceRequest;
export type RunbookHistory = ExecBatchRunHistory;
export type RunbookConfirmationPolicy = "every_step" | "production_only";

export function createRunbookId(): string {
  return globalThis.crypto.randomUUID();
}

export const runbookApi = {
  start: (request: StartRequest) => call("runbook_run_start", request),
  get: (runId: string) => call("runbook_run_get", { runId }),
  prepare: (request: PrepareRequest) => call("runbook_run_prepare", request),
  advance: (request: AdvanceRequest) => call("runbook_run_advance", request),
  cancel: (runId: string) => call("runbook_run_cancel", { runId }),
  historyGet: (runId: string) => call("runbook_run_history_get", { runId }),
};
