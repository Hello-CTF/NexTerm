import type { AiHitlEventDto, AiHitlSnapshotDto } from "../../ipc/types";
import {
  appendHitlInterrupt,
  attemptOf,
  resolveInteraction,
  type ChatItem,
  type ConversationState,
  type InteractionItem,
} from "./conversation";

export interface HitlCardIdentity {
  itemId: string;
  requestId?: string;
  nonce: string;
  callId: string;
}

export interface HitlReplayPlan {
  afterSeq: number;
  sweepable: HitlCardIdentity[];
}

export interface HitlFoldResult {
  state: ConversationState;
  lastSeq: number;
  changed: boolean;
}

const RESUMED_LABEL = "已在服务端回答";
const SWEPT_LABEL = "已在服务端结束";

export function hitlTerminalLabel(reason: string): string {
  switch (reason) {
    case "canceled":
      return "本轮已停止，无需再处理";
    case "failed":
      return "本轮已出错，无需再处理";
    case "expired":
      return "等待已过期，无需再处理";
    default:
      return "本轮已结束，无需再处理";
  }
}

function validSeq(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 1;
}

function isOpenInteraction(
  item: ChatItem,
  generation: number,
): item is InteractionItem {
  return (
    item.attempt === generation &&
    (item.role === "confirm" || item.role === "question") &&
    item.resolution === undefined
  );
}

function matchesRequest(
  card: { requestId?: string },
  request: { id: string },
): boolean {
  return card.requestId !== undefined && card.requestId === request.id;
}

function openInteractions(state: ConversationState, generation: number): InteractionItem[] {
  return state.items.filter((item): item is InteractionItem => isOpenInteraction(item, generation));
}

function identityOf(item: InteractionItem): HitlCardIdentity {
  return { itemId: item.id, requestId: item.requestId, nonce: item.nonce, callId: item.callId };
}

export function planHitlReplay(
  state: ConversationState,
  generation: number,
  lastSeq: number,
): HitlReplayPlan {
  return {
    afterSeq: lastSeq,
    sweepable: openInteractions(state, generation).map(identityOf),
  };
}

function sortedValidEvents(events: AiHitlEventDto[]): AiHitlEventDto[] {
  return events.filter((ev) => validSeq(ev.seq)).sort((a, b) => a.seq - b.seq);
}

export function applyHitlReplay(
  state: ConversationState,
  generation: number,
  plan: HitlReplayPlan,
  events: AiHitlEventDto[],
  snapshot: AiHitlSnapshotDto | null,
  lastSeq: number,
): HitlFoldResult {
  let next = state;
  let changed = false;
  let seq = plan.afterSeq > lastSeq ? plan.afterSeq : lastSeq;
  const consumed = new Set<string>();
  let terminal = false;

  const attempt = attemptOf(next, generation);
  if (!attempt || attempt.outcome) {
    for (const ev of sortedValidEvents(events)) {
      if (ev.seq > seq) seq = ev.seq;
    }
    if (snapshot && validSeq(snapshot.seq) && snapshot.seq > seq) seq = snapshot.seq;
    return { state: next, lastSeq: seq, changed: false };
  }

  for (const ev of sortedValidEvents(events)) {
    if (ev.seq <= seq) continue;
    seq = ev.seq;
    if (ev.kind === "resumed" && ev.requestId) {
      consumed.add(ev.requestId);
      const card = openInteractions(next, generation).find((c) => c.requestId === ev.requestId);
      if (card) {
        next = resolveInteraction(next, generation, card.id, card.nonce, RESUMED_LABEL);
        changed = true;
      }
    } else if (ev.kind === "terminal") {
      terminal = true;
      const label = hitlTerminalLabel(ev.reason);
      for (const card of openInteractions(next, generation)) {
        next = resolveInteraction(next, generation, card.id, card.nonce, label);
        changed = true;
      }
    }
  }

  if (snapshot) {
    const pending = Array.isArray(snapshot.pending) ? snapshot.pending : [];
    const terminalLabel = snapshot.terminal ? hitlTerminalLabel(snapshot.terminal.reason) : null;
    const runOver = terminal || terminalLabel !== null;

    for (const identity of plan.sweepable) {
      const card = next.items.find((item) => item.id === identity.itemId);
      if (!card || !isOpenInteraction(card, generation)) continue;
      if (pending.some((request) => matchesRequest(identity, request))) continue;
      const label =
        identity.requestId !== undefined && consumed.has(identity.requestId)
          ? RESUMED_LABEL
          : runOver
            ? (terminalLabel ?? SWEPT_LABEL)
            : SWEPT_LABEL;
      next = resolveInteraction(next, generation, card.id, card.nonce, label);
      changed = true;
    }

    if (runOver) {
      for (const card of openInteractions(next, generation)) {
        next = resolveInteraction(next, generation, card.id, card.nonce, terminalLabel ?? SWEPT_LABEL);
        changed = true;
      }
    }

    for (const request of pending) {
      if (request.kind !== "confirm" && request.kind !== "question") continue;
      const exists = openInteractions(next, generation).some((card) => matchesRequest(card, request));
      if (exists) continue;
      const before = next;
      next = appendHitlInterrupt(next, generation, request);
      if (next !== before) changed = true;
    }

    if (validSeq(snapshot.seq) && snapshot.seq > seq) seq = snapshot.seq;
  }

  return { state: next, lastSeq: seq, changed };
}
