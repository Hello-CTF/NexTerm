import { call } from "./commands";
import type {
  AdvanceRequest,
  ExecBatchRunHistory,
  PrepareRequest,
  RenderedCommand,
  RespondRequest,
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
  RespondRequest,
  RunRequest,
  RunState,
  StartRequest,
  Step,
  StepState,
};
export type RunbookStep = Step;
export type RunbookStepState = StepState;
export type RunbookRunState = RunState;
// The server loads the steps of the stored snippet named by snippetId (including confirm/interactive flags); steps,
// runbookId and runbookName are not sent, so the wire shape is narrower than the generated StartRequest.
export type RunbookStartRequest = Omit<StartRequest, "steps" | "runbookId" | "runbookName"> & { snippetId: string };
export type RunbookPrepareRequest = PrepareRequest;
export type RunbookAdvanceRequest = AdvanceRequest;
export type RunbookHistory = ExecBatchRunHistory;
export type RunbookConfirmationPolicy = "every_step" | "production_only";

export function createRunbookId(): string {
  return globalThis.crypto.randomUUID();
}

export const runbookApi = {
  start: (request: RunbookStartRequest) => call("runbook_run_start", request as unknown as StartRequest),
  get: (runId: string) => call("runbook_run_get", { runId }),
  prepare: (request: PrepareRequest) => call("runbook_run_prepare", request),
  advance: (request: AdvanceRequest) => call("runbook_run_advance", request),
  respond: (request: RespondRequest) => call("runbook_run_respond", request),
  cancel: (runId: string) => call("runbook_run_cancel", { runId }),
  historyGet: (runId: string) => call("runbook_run_history_get", { runId }),
};
