import type { AiAnswerInput, AiConfirmationInput, AiDecision } from "../../ipc/commands";

export interface AiInteractionRequest {
  jobId: string;
  callId: string;
  nonce: string;
  /**
   * HITL 请求的稳定身份（内核 interrupt id）：重连对账按它匹配卡片，
   * 不按内容猜。旧内核事件可能缺省。
   */
  requestId?: string;
  /** HITL 运行 attempt（第几次中断），同样来自事件，缺省表示旧内核。 */
  hitlAttempt?: number;
}

export interface AiQuestionData {
  question: string;
  options: string[];
}

export function confirmationNonceOf(event: Record<string, unknown>): string {
  return typeof event.confirmationNonce === "string" ? event.confirmationNonce : "";
}

/** 从 confirmRequired / questionRequired 事件解析 HITL 身份（requestId/attempt）。 */
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
  // 只带后端消费的字段：requestId/hitlAttempt 是本地对账身份，不上线。
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
