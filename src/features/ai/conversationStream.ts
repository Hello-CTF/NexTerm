import {
  applyAiEvent,
  appendSteerMessage,
  appendUserMessage,
  attemptOf,
  beginRun,
  bindRunJob,
  cancelRun,
  createConversation,
  resetConversation,
  resolveInteraction,
  resolveSteerById,
  type ApplyResult,
  type ChatItem,
  type ConversationState,
  type SteerDelivery,
} from "./conversation";
import {
  applyHitlReplay as foldHitlReplay,
  planHitlReplay as planHitlReplayFold,
  type HitlFoldResult,
  type HitlReplayPlan,
} from "./hitlReplay";
import type { AiHitlEventDto, AiHitlSnapshotDto } from "../../ipc/types";

export type StreamScheduler = (cb: () => void) => () => void;

export const rafScheduler: StreamScheduler = (cb) => {
  if (typeof requestAnimationFrame === "function") {
    const id = requestAnimationFrame(cb);
    return () => cancelAnimationFrame(id);
  }
  const id = setTimeout(cb, 16);
  return () => clearTimeout(id);
};

export const MAX_PENDING_CHARS = 64 * 1024;

interface PendingFragment {
  generation: number;
  role: "assistant" | "reasoning";
  text: string;
}

function positiveSafeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 1;
}

function eventSequence(ev: Record<string, unknown>): number | null {
  return positiveSafeInteger(ev.seq) ? ev.seq : null;
}

export interface ConversationStream {
  getState(): ConversationState;
  subscribe(listener: (state: ConversationState) => void): () => void;
  beginRun(generation: number, kind?: "chat" | "takeover"): void;
  appendUser(generation: number, text: string, imageCount?: number): void;
  appendSteer(generation: number, text: string): string;
  resolveSteer(generation: number, itemId: string, delivery: SteerDelivery): void;
  bindJob(generation: number, jobId: string): void;
  pushEvent(generation: number, ev: Record<string, unknown>): ApplyResult;
  resolveInteraction(generation: number, itemId: string, nonce: string, label: string): void;
  cancelRun(generation: number, settle: boolean): void;
  reset(items?: ChatItem[]): void;
  hitlSeq(generation: number): number;
  planHitlReplay(generation: number): HitlReplayPlan;
  applyHitlReplay(
    generation: number,
    plan: HitlReplayPlan,
    events: AiHitlEventDto[],
    snapshot: AiHitlSnapshotDto | null,
  ): void;
  flush(): void;
  dispose(): void;
}

export function createConversationStream(
  scheduler: StreamScheduler = rafScheduler,
): ConversationStream {
  let state = createConversation();
  let pending: PendingFragment[] = [];
  let pendingChars = 0;
  let cancelScheduled: (() => void) | null = null;
  let disposed = false;
  const listeners = new Set<(state: ConversationState) => void>();
  const seenSequences = new Map<number, Set<number>>();
  const hitlSequences = new Map<number, number>();
  const seenOf = (generation: number): Set<number> => {
    let seen = seenSequences.get(generation);
    if (!seen) {
      seen = new Set();
      seenSequences.set(generation, seen);
    }
    return seen;
  };
  const hitlSeqOf = (generation: number): number => hitlSequences.get(generation) ?? 0;

  const publish = () => {
    for (const listener of [...listeners]) listener(state);
  };

  const cancelFrame = () => {
    if (cancelScheduled) {
      cancelScheduled();
      cancelScheduled = null;
    }
  };

  const flush = () => {
    cancelFrame();
    if (pending.length === 0) return;
    const fragments = pending;
    pending = [];
    pendingChars = 0;
    let changed = false;
    for (const fragment of fragments) {
      const result = applyAiEvent(state, fragment.generation, {
        type: fragment.role === "assistant" ? "delta" : "reasoning",
        text: fragment.text,
      });
      if (result.accepted) {
        state = result.state;
        changed = true;
      }
    }
    if (changed) publish();
  };

  const scheduleFlush = () => {
    if (cancelScheduled || disposed) return;
    cancelScheduled = scheduler(() => {
      cancelScheduled = null;
      flush();
    });
  };

  const mutate = (fn: () => void) => {
    if (disposed) return;
    flush();
    fn();
  };

  return {
    getState: () => state,
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    beginRun(generation, kind = "chat") {
      mutate(() => {
        const next = beginRun(state, generation, kind);
        if (next !== state) {
          state = next;
          publish();
        }
      });
    },
    appendUser(generation, text, imageCount) {
      mutate(() => {
        state = appendUserMessage(state, generation, text, imageCount);
        publish();
      });
    },
    appendSteer(generation, text) {
      let id = "";
      mutate(() => {
        const appended = appendSteerMessage(state, generation, text);
        state = appended.state;
        id = appended.id;
        publish();
      });
      return id;
    },
    resolveSteer(generation, itemId, delivery) {
      mutate(() => {
        const next = resolveSteerById(state, generation, itemId, delivery);
        if (next !== state) {
          state = next;
          publish();
        }
      });
    },
    bindJob(generation, jobId) {
      mutate(() => {
        const next = bindRunJob(state, generation, jobId);
        if (next !== state) {
          state = next;
          publish();
        }
      });
    },
    pushEvent(generation, ev): ApplyResult {
      if (disposed) return { state, accepted: false, terminal: null };
      const type = ev.type as string;
      const terminalEvent = type === "done" || type === "error" ? (type as "done" | "error") : null;
      const seq = eventSequence(ev);
      if (seq !== null) {
        const seen = seenOf(generation);
        if (seen.has(seq)) {
          return { state, accepted: false, terminal: terminalEvent };
        }
        seen.add(seq);
      }
      if (type === "delta" || type === "reasoning") {
        const attempt = attemptOf(state, generation);
        if (!attempt || attempt.outcome) return { state, accepted: false, terminal: null };
        const text = typeof ev.text === "string" ? ev.text : "";
        if (!text) return { state, accepted: true, terminal: null };
        const role = type === "delta" ? "assistant" : "reasoning";
        const last = pending[pending.length - 1];
        if (last && last.generation === generation && last.role === role) {
          last.text += text;
        } else {
          pending.push({ generation, role, text });
        }
        pendingChars += text.length;
        if (pendingChars > MAX_PENDING_CHARS) {
          flush();
        } else {
          scheduleFlush();
        }
        return { state, accepted: true, terminal: null };
      }
      flush();
      const result = applyAiEvent(state, generation, ev);
      if (result.accepted) {
        state = result.state;
        if (result.terminal) {
          seenSequences.delete(generation);
          hitlSequences.delete(generation);
        }
        publish();
      }
      return result;
    },
    resolveInteraction(generation, itemId, nonce, label) {
      mutate(() => {
        const next = resolveInteraction(state, generation, itemId, nonce, label);
        if (next !== state) {
          state = next;
          publish();
        }
      });
    },
    cancelRun(generation, settle) {
      mutate(() => {
        const next = cancelRun(state, generation, settle);
        if (next !== state) {
          state = next;
          if (settle) {
            seenSequences.delete(generation);
            hitlSequences.delete(generation);
          }
          publish();
        }
      });
    },
    reset(items = []) {
      mutate(() => {
        state = resetConversation(state, items);
        seenSequences.clear();
        hitlSequences.clear();
        publish();
      });
    },
    hitlSeq: hitlSeqOf,
    planHitlReplay(generation) {
      return planHitlReplayFold(state, generation, hitlSeqOf(generation));
    },
    applyHitlReplay(generation, plan, events, snapshot) {
      if (disposed) return;
      flush();
      const result: HitlFoldResult = foldHitlReplay(
        state,
        generation,
        plan,
        events,
        snapshot,
        hitlSeqOf(generation),
      );
      if (result.lastSeq !== hitlSeqOf(generation)) {
        hitlSequences.set(generation, result.lastSeq);
      }
      if (result.changed) {
        state = result.state;
        publish();
      }
    },
    flush,
    dispose() {
      if (disposed) return;
      flush();
      disposed = true;
      cancelFrame();
      seenSequences.clear();
      hitlSequences.clear();
      listeners.clear();
    },
  };
}
