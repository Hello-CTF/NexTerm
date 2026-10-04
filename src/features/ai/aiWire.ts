import type { AiAnswerInput, AiConfirmationInput, AiDecision } from "../../ipc/commands";

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

export function confirmationInput(
  request: AiInteractionRequest,
  decision: AiDecision,
): AiConfirmationInput {
  return { jobId: request.jobId, callId: request.callId, nonce: request.nonce, decision };
}

export function answerInput(request: AiInteractionRequest, text: string): AiAnswerInput {
  return { jobId: request.jobId, callId: request.callId, nonce: request.nonce, text };
}

export type AiInteractionKind = "confirm" | "question";

export interface AiInteractionCard extends AiInteractionRequest {
  role: AiInteractionKind;
}

export function clearInteractionIfMatch<T extends AiInteractionCard>(
  current: T | null,
  completed: T,
  kind: AiInteractionKind,
): T | null {
  if (!current || current.role !== kind || completed.role !== kind) return current;
  return current.jobId === completed.jobId &&
    current.callId === completed.callId &&
    current.nonce === completed.nonce
    ? null
    : current;
}
