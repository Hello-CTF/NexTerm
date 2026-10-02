import type { AiAnswerInput, AiConfirmationInput, AiDecision } from "../../ipc/commands";

export interface AiInteractionRequest {
  jobId: string;
  callId: string;
  nonce: string;
}

export interface AiQuestionData {
  question: string;
  options: string[];
}

export function confirmationNonceOf(event: Record<string, unknown>): string {
  return typeof event.confirmationNonce === "string" ? event.confirmationNonce : "";
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
  return { ...request, decision };
}

export function answerInput(request: AiInteractionRequest, text: string): AiAnswerInput {
  return { ...request, text };
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
