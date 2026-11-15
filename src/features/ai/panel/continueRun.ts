import { useCallback, useRef } from "react";
import type { ChatItem } from "../conversation";
import { aiRunBlocksStart } from "../runOwnership";
import type { AiChatCtx } from "./aiChatCtx";
import { t } from "../../../i18n";

// CONTINUE_MESSAGE is the user message that resumes a run stopped at the step limit; the model picks up from the
// persisted tool history of the conversation.
export const CONTINUE_MESSAGE = t("继续");

// continuableOutcomeId returns the step-limit notice that can still be continued: the latest outcome of the
// conversation, with no user message after it.
export function continuableOutcomeId(items: readonly ChatItem[]): string | null {
  for (let index = items.length - 1; index >= 0; index -= 1) {
    const item = items[index];
    if (item.role === "user") return null;
    if (item.role === "outcome") return item.outcome === "error" && item.maxIterations ? item.id : null;
  }
  return null;
}

type ContinueCtx = Pick<
  AiChatCtx,
  "send" | "busy" | "activeRunRef" | "pushToast" | "conv" | "conversationRuns" | "requestSnapshotsRef" | "input" | "images" | "refs" | "setInput" | "setImages" | "setRefs"
>;

// useContinueRun returns a stable handler that sends CONTINUE_MESSAGE in the same conversation, keeping the plan mode of
// the stopped run and whatever the user has drafted in the composer.
export function useContinueRun(ctx: ContinueCtx): () => void {
  const latest = useRef(ctx);
  latest.current = ctx;
  return useCallback(() => {
    const { send, busy, activeRunRef, pushToast, conv, conversationRuns, requestSnapshotsRef, input, images, refs, setInput, setImages, setRefs } = latest.current;
    if (busy || aiRunBlocksStart(activeRunRef.current)) {
      pushToast("info", t("AI 正在回答，请等待当前回答结束"));
      return;
    }
    const id = continuableOutcomeId(conv.items);
    const outcome = id ? conv.items.find((item) => item.id === id) : undefined;
    const generation = outcome?.attempt ?? null;
    const jobId = generation === null ? undefined : conv.attempts.find((attempt) => attempt.generation === generation)?.jobId;
    const planMode =
      (generation === null ? undefined : requestSnapshotsRef.current.get(generation)?.planMode) ??
      conversationRuns.find((run) => run.id === jobId)?.planMode ??
      false;
    void send({ message: CONTINUE_MESSAGE, images: [], refs: [], planMode });
    // send clears the composer; a draft the user has not sent yet stays.
    if (input) setInput(input);
    if (images.length > 0) setImages(images);
    if (refs.length > 0) setRefs(refs);
  }, []);
}
