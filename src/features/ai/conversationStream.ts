// 流式会话控制器：在纯聚合器（conversation.ts）外面套一层**有界合并**。
//
// delta / reasoning 是逐 token 来的，直接每条都 setState 等于每个 token 一次
// React 渲染。这里把它们攒进 pending 队列，每个动画帧（或无 rAF 环境下的
// 16ms 定时器）最多落一次账；但**顺序边界绝不拖延**：
// 工具卡片、交互卡片、终态、停止、重置、卸载之前都必须先 flush，
// 否则"正文后半截"会跑到工具卡片甚至终态标记后面去。
//
// 内容口径：pending 只是延迟，不改字 —— 逐段拼接与逐事件直推逐字节相同。
//
// 重放去重（M23/tower 最终冻结的 per-job seq 契约，见 eventSequence）：
//   · 每个 chat/takeover job 的事件从 1 起严格递增编号（全事件类型共享，
//     done/error 也在内），重连补发的缓存帧保留原 seq，真正的新帧即使字节
//     相同也取新 seq；
//   · 因此「同一前端 attempt + 同一 seq」就是精确的重放，直接丢弃；
//   · **绝不按片段内容去重**：模型本来就会输出重复 token，字节相同什么也
//     证明不了。没有有效 seq 的事件一律按新事件落账 —— 宁可保留交给
//     done.answer 终态对账，也绝不误删任何可能是新内容的片段。
//
// HITL 对账（hitlReplay.ts）走**另一个**序号空间：内核 HITL 运行事件的
// per-run seq。两套身份互不通用，各自按各自的高水位去重，都不按内容猜。
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

/** 调度一次回调，返回取消函数。浏览器用 rAF；测试注入手动帧实现确定性。 */
export type StreamScheduler = (cb: () => void) => () => void;

export const rafScheduler: StreamScheduler = (cb) => {
  if (typeof requestAnimationFrame === "function") {
    const id = requestAnimationFrame(cb);
    return () => cancelAnimationFrame(id);
  }
  const id = setTimeout(cb, 16);
  return () => clearTimeout(id);
};

/**
 * pending 的内存上界：攒过这么多字符就不再等帧，立刻落账。
 * 防止极端吞吐下"等下一帧"变成无界积压；正常流式远够不着它。
 */
export const MAX_PENDING_CHARS = 64 * 1024;

interface PendingFragment {
  generation: number;
  role: "assistant" | "reasoning";
  text: string;
}

function positiveSafeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 1;
}

/**
 * 事件的重放身份（M23/tower 最终冻结：per-job seq）。
 * 每个 chat/takeover job 从 1 严格递增、全事件类型共享、重放保留原 seq。
 * 只有正安全整数才算有效身份；缺失 / 非法 ⇒ null：无身份，按新事件落账。
 */
function eventSequence(ev: Record<string, unknown>): number | null {
  return positiveSafeInteger(ev.seq) ? ev.seq : null;
}

export interface ConversationStream {
  getState(): ConversationState;
  subscribe(listener: (state: ConversationState) => void): () => void;
  beginRun(generation: number, kind?: "chat" | "takeover"): void;
  appendUser(generation: number, text: string, imageCount?: number): void;
  /**
   * 运行中补充：pending 气泡立即落账（内核 steered 事件可能比 RPC 响应先
   * 回来），返回条目 id 供 RPC 失败时按 id 结算。
   */
  appendSteer(generation: number, text: string): string;
  /** RPC 拒绝路径：把这条补充气泡标成 dropped（已结算过的一律不动）。 */
  resolveSteer(generation: number, itemId: string, delivery: SteerDelivery): void;
  bindJob(generation: number, jobId: string): void;
  pushEvent(generation: number, ev: Record<string, unknown>): ApplyResult;
  resolveInteraction(generation: number, itemId: string, nonce: string, label: string): void;
  cancelRun(generation: number, settle: boolean): void;
  reset(items?: ChatItem[]): void;
  /** 已处理的 HITL 事件序号高水位（per-run seq 空间，与流事件 seq 无关）。 */
  hitlSeq(generation: number): number;
  /**
   * 开始一次 HITL 对账：返回已见序号（增量拉取的 afterSeq）与当前未决交互
   * 身份快照 —— 对账途中 racing 到达的新流事件不被本次清扫误伤。
   */
  planHitlReplay(generation: number): HitlReplayPlan;
  /** 把 HITL 事件 / 快照折进会话（重连恢复、交互失败后的服务端对账）。 */
  applyHitlReplay(
    generation: number,
    plan: HitlReplayPlan,
    events: AiHitlEventDto[],
    snapshot: AiHitlSnapshotDto | null,
  ): void;
  /** 立刻把 pending 文本落账（边界 flush；幂等）。 */
  flush(): void;
  /** 卸载边界：最后一次 flush，取消已排度的帧，之后拒绝一切写入。 */
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
  /** 每个 attempt 已见过的 per-job seq：精确重放身份，与片段内容无关。 */
  const seenSequences = new Map<number, Set<number>>();
  /** 每个 attempt 已处理的 HITL 事件序号高水位（HITL 自己的 per-run seq 空间）。 */
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
    // 只有真的落了账才通知：避免空 flush 触发多余渲染。
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
          // 精确重放：状态不变；终态类型如实上报，调用方照常释放通道。
          return { state, accepted: false, terminal: terminalEvent };
        }
        seen.add(seq);
      }
      if (type === "delta" || type === "reasoning") {
        // 先验 attempt：stale / 已终态的文本不进 pending，结果也如实上报。
        const attempt = attemptOf(state, generation);
        if (!attempt || attempt.outcome) return { state, accepted: false, terminal: null };
        const text = typeof ev.text === "string" ? ev.text : "";
        if (!text) return { state, accepted: true, terminal: null };
        const role = type === "delta" ? "assistant" : "reasoning";
        const last = pending[pending.length - 1];
        if (last && last.generation === generation && last.role === role) {
          // 连续同种片段直接合并：pending 的段数也有界。
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
      // 顺序边界：任何非文本事件之前，先把攒下的文本落到正确位置。
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
      // 顺序边界：与工具卡 / 交互卡 / 终态同一待遇，先 flush 再落账。
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
