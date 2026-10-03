// HITL 重连对账：把内核 `ai_hitl_events` / `ai_hitl_snapshot` 的结果折进会话状态。
//
// 两条铁律（与流事件的 per-job seq 同一哲学）：
//   · 事件身份只看 per-run `seq`：小于等于高水位的一律是已处理过的重放，直接跳过，
//     **绝不按内容猜**；
//   · 快照是权威：pending 之外残留的本地交互卡都是服务端已结束的。但清扫只动
//     对账启动时刻已存在的卡 —— 对账途中 racing 到达的新流事件不被误伤；
//     终态除外：run 既已终结，任何本地挂起都不合法，全量关闭。
//
// 纯函数模块：不依赖 React / IPC，测试用假 DTO 确定性驱动全部分支。
import type { AiHitlEventDto, AiHitlSnapshotDto } from "../../ipc/types";
import {
  appendHitlInterrupt,
  attemptOf,
  resolveInteraction,
  type ChatItem,
  type ConversationState,
  type InteractionItem,
} from "./conversation";

/** 一张未决交互卡的对账身份（requestId 优先，nonce+callId 兜底旧内核）。 */
export interface HitlCardIdentity {
  itemId: string;
  requestId?: string;
  nonce: string;
  callId: string;
}

export interface HitlReplayPlan {
  /** 已处理的 HITL 事件序号高水位（增量拉取的 afterSeq）。 */
  afterSeq: number;
  /** 对账启动时刻的未决交互身份快照 —— 清扫只动这些卡。 */
  sweepable: HitlCardIdentity[];
}

export interface HitlFoldResult {
  state: ConversationState;
  lastSeq: number;
  changed: boolean;
}

/** resumed 事件的结算文案：请求已被（本窗口或别处）回答。 */
const RESUMED_LABEL = "已在服务端回答";
/** 快照清扫的兜底文案：服务端不再挂起，也没有更具体的事件标签。 */
const SWEPT_LABEL = "该交互已在服务端结束";

/** 终态原因 → 交互卡关闭文案（与 conversation.ts 本地收尾同一口径）。 */
export function hitlTerminalLabel(reason: string): string {
  switch (reason) {
    case "canceled":
      return "本轮已停止，交互已关闭";
    case "failed":
      return "本轮已出错，交互已关闭";
    case "expired":
      return "等待已过期，交互已关闭";
    default:
      return "本轮已结束，交互已关闭";
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

/** requestId 优先；旧内核事件没有 requestId，退回 nonce+callId 双重要件。 */
function matchesRequest(
  card: { requestId?: string; nonce: string; callId: string },
  request: { id: string; nonce: string; callId: string },
): boolean {
  if (card.requestId) return card.requestId === request.id;
  return card.nonce === request.nonce && card.callId === request.callId;
}

function openInteractions(state: ConversationState, generation: number): InteractionItem[] {
  return state.items.filter((item): item is InteractionItem => isOpenInteraction(item, generation));
}

function identityOf(item: InteractionItem): HitlCardIdentity {
  return { itemId: item.id, requestId: item.requestId, nonce: item.nonce, callId: item.callId };
}

/** 对账启动：记下已见序号与当前未决交互，之后 racing 的新卡不被本次清扫误伤。 */
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

/**
 * 折叠一次 HITL 对账的结果：先按 seq 升序折事件（补结算标签），再用快照兜底
 * （权威挂起列表 + 补卡）。`plan` 由对账启动时的 `planHitlReplay` 给出。
 */
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
    // 未知 / 已终态的轮次：对账不复活卡片；序号照收（这些事件确实已见过），
    // 快照之后每次重连都会再拉，不依赖本地累计。
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
    // interrupted：事件不带请求体，补卡是快照的职责。
  }

  if (snapshot) {
    const pending = Array.isArray(snapshot.pending) ? snapshot.pending : [];
    const terminalLabel = snapshot.terminal ? hitlTerminalLabel(snapshot.terminal.reason) : null;
    const runOver = terminal || terminalLabel !== null;

    // 1) 清扫：对账启动时已在、且服务端不再挂起的卡，按最具体的事实结算。
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

    // 2) 终态之后不存在合法挂起：racing 新建的卡也一并关闭。
    if (runOver) {
      for (const card of openInteractions(next, generation)) {
        next = resolveInteraction(next, generation, card.id, card.nonce, terminalLabel ?? SWEPT_LABEL);
        changed = true;
      }
    }

    // 3) 补卡：服务端挂起、本地没有的请求（按当前未决集合匹配 —— racing 流事件
    //    已建的卡不会再补一张）。
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
