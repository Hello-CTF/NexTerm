import type { AiAnswerInput, AiConfirmationInput, AiDecision } from "../../ipc/commands";

export const MESSAGE_HISTORY_PAGE_SIZE = 100;

export function messageHistoryStart(messageCount: number, pageSize = MESSAGE_HISTORY_PAGE_SIZE): number {
  return Math.max(0, messageCount - Math.max(1, pageSize));
}

export function earlierMessageHistoryStart(currentStart: number, pageSize = MESSAGE_HISTORY_PAGE_SIZE): number {
  return Math.max(0, currentStart - Math.max(1, pageSize));
}

export interface AiInteractionRequest {
  jobId: string;
  callId: string;
  nonce: string;
  requestId?: string;
  hitlAttempt?: number;
}

export interface AiQuestionData {
  question: string;
  options: string[];
}

export function confirmationNonceOf(event: Record<string, unknown>): string {
  return typeof event.confirmationNonce === "string" ? event.confirmationNonce : "";
}

export function interactionIdentityOf(event: Record<string, unknown>): {
  requestId?: string;
  hitlAttempt?: number;
} {
  const requestId = typeof event.requestId === "string" && event.requestId ? event.requestId : undefined;
  const attempt =
    typeof event.attempt === "number" && Number.isSafeInteger(event.attempt) && event.attempt >= 1
      ? event.attempt
      : undefined;
  return attempt === undefined && requestId === undefined ? {} : { requestId, hitlAttempt: attempt };
}

export function questionFromEvent(event: Record<string, unknown>): AiQuestionData {
  const raw = event.question as { question?: unknown; options?: unknown } | undefined;
  return {
    question: typeof raw?.question === "string" ? raw.question : "",
    options: Array.isArray(raw?.options)
      ? raw.options.filter((option): option is string => typeof option === "string")
      : [],
  };
}

// requiredEnvironmentNamesFromEvent reads the production challenge's typed
// environment names from a confirmRequired event. The backend emits the field
// only for production-classified targets; anything malformed fails closed to
// undefined so a corrupt challenge degrades to the legacy flow instead of the
// typed UI (the backend still rejects a names-less answer at resume).
export function requiredEnvironmentNamesFromEvent(event: Record<string, unknown>): string[] | undefined {
  const value = event.requiredEnvironmentNames;
  if (value === undefined) return undefined;
  if (!Array.isArray(value) || value.length === 0) return undefined;
  for (const name of value) {
    if (typeof name !== "string" || name.trim() === "") return undefined;
  }
  return value as string[];
}

export function confirmationInput(
  request: AiInteractionRequest,
  decision: AiDecision,
  environmentNames?: string[],
): AiConfirmationInput {
  return {
    jobId: request.jobId,
    callId: request.callId,
    nonce: request.nonce,
    decision,
    ...(environmentNames && environmentNames.length > 0 ? { environmentNames } : {}),
  };
}

export function answerInput(request: AiInteractionRequest, text: string): AiAnswerInput {
  return { jobId: request.jobId, callId: request.callId, nonce: request.nonce, text };
}
