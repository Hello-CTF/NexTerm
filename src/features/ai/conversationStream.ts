// 流式会话控制器：在纯聚合器（conversation.ts）外面套一层**有界合并**。
//
// delta / reasoning 是逐 token 来的，直接每条都 setState 等于每个 token 一次
// React 渲染。这里把它们攒进 pending 队列，每个动画帧（或无 rAF 环境下的
// 16ms 定时器）最多落一次账；但**顺序边界绝不拖延**：
// 工具卡片、交互卡片、终态、停止、重置、卸载之前都必须先 flush，
// 否则"正文后半截"会跑到工具卡片甚至终态标记后面去。
//
// 内容口径：pending 只是延迟，不改字 —— 逐段拼接与逐事件直推逐字节相同。
import {
  applyAiEvent,
  appendUserMessage,
  attemptOf,
  beginRun,
  bindRunJob,
  cancelRun,
  createConversation,
  resetConversation,
  resolveInteraction,
  type ApplyResult,
  type ChatItem,
  type ConversationState,
} from "./conversation";

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

export interface ConversationStream {
  getState(): ConversationState;
  subscribe(listener: (state: ConversationState) => void): () => void;
  beginRun(generation: number, kind?: "chat" | "takeover"): void;
  appendUser(generation: number, text: string, imageCount?: number): void;
  bindJob(generation: number, jobId: string): void;
  pushEvent(generation: number, ev: Record<string, unknown>): ApplyResult;
  resolveInteraction(generation: number, itemId: string, nonce: string, label: string): void;
  cancelRun(generation: number, settle: boolean): void;
  reset(items?: ChatItem[]): void;
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
          publish();
        }
      });
    },
    reset(items = []) {
      mutate(() => {
        state = resetConversation(state, items);
        publish();
      });
    },
    flush,
    dispose() {
      if (disposed) return;
      flush();
      disposed = true;
      cancelFrame();
      listeners.clear();
    },
  };
}
