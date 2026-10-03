// AI 会话流聚合：把内核推来的逐条事件折成**结构化、可重放、可幂等**的会话状态。
//
// 为什么独立成纯函数模块：
//   · WS 断线重连会沿用同一通道 id 并补齐服务端缓存的帧 —— 同一事件可能到达两次，
//     聚合层必须自己幂等，不能假设"每条事件恰好一次"；
//   · 终态（done/error/取消）是**可见结果**，迟到或重复的事件不得把它复制或抹掉；
//   · 纯数据不依赖 React / IPC，测试可以用假事件流确定性地驱动全部分支。
//
// 线格式字段与内核 `AiEvent` 对齐（camelCase、`type` 标签），这里只读不改。
import { confirmationNonceOf, interactionIdentityOf, questionFromEvent } from "./aiWire";
import type { AiHitlInterruptDto } from "../../ipc/types";
import type { AiUsage } from "./UsageRing";

/** 写文件类工具的改动预览（与内核 `FilePreview` 对应）。 */
export interface FilePreviewItem {
  path: string;
  /** 执行前的内容。新建文件是空串。 */
  before: string;
  /** 执行后（预测）的内容。 */
  after: string;
  kind: string;
}

interface ItemBase {
  /**
   * 稳定的 React key / 身份。带内核 id 的条目用 `g<代>:<类型>:<内核id>`，
   * 重放同一事件时靠它幂等；无 id 的条目用本地自增序号。
   */
  id: string;
  /** 属于哪一次运行（`AiRunSlot.generation`）；历史消息为 null。 */
  attempt: number | null;
}

export type ChatItem =
  | (ItemBase & { role: "user"; text: string; imageCount?: number })
  | (ItemBase & { role: "assistant"; text: string })
  | (ItemBase & { role: "reasoning"; text: string })
  | (ItemBase & {
      role: "tool";
      /** 内核调用 id（`toolCall.id`），toolResult 按它对齐；缺失时为 ""。 */
      callId: string;
      name: string;
      display: string;
      /** 折叠态看到的一行摘要（内核截到 400 字）。 */
      summary?: string;
      /** 完整输出，点「展开」看的就是它（内核上限 64K）。 */
      text?: string;
      ok?: boolean;
      exitCode?: number | null;
    })
  | (ItemBase & { role: "diff"; path: string; before: string; after: string })
  | (ItemBase & { role: "plan"; text: string })
  | (ItemBase & {
      role: "confirm";
      jobId: string;
      callId: string;
      tool: string;
      /** 原始参数 + 判定理由的兜底文案（也是「加为拦截规则」的预填来源）。 */
      rendered: string;
      /** 判定理由，单独一行展示。 */
      reason?: string;
      /** 写文件类工具的改动预览：**批准之前**就能看到改什么。 */
      preview?: FilePreviewItem | null;
      nonce: string;
      /** HITL 请求的稳定身份（内核 interrupt id）：重连对账按它匹配。 */
      requestId?: string;
      /** HITL 运行 attempt（第几次中断）；与 `ItemBase.attempt`（前端轮次）无关。 */
      hitlAttempt?: number;
      /** 已有定论的交互：决定文案（"已允许一次"…）或关闭原因；undefined = 仍待处理。 */
      resolution?: string;
    })
  | (ItemBase & {
      role: "question";
      jobId: string;
      callId: string;
      nonce: string;
      question: string;
      options: string[];
      requestId?: string;
      hitlAttempt?: number;
      resolution?: string;
    })
  | (ItemBase & {
      /**
       * 一轮运行的终态标记：完成 / 失败 / 已停止。
       * 落进消息流而不是只弹 toast —— 流关闭或重连之后，失败的那一轮仍然可查。
       */
      role: "outcome";
      outcome: "done" | "error" | "canceled";
      text: string;
    });

export type ToolItem = Extract<ChatItem, { role: "tool" }>;
export type ConfirmItem = Extract<ChatItem, { role: "confirm" }>;
export type QuestionItem = Extract<ChatItem, { role: "question" }>;
export type InteractionItem = ConfirmItem | QuestionItem;

/**
 * 状态条上的一行字（`status` 事件，外加「正在生成工具参数」这个阶段）。
 *
 * `tool` / `chars` 只在 `phase === "tool_args"` 时有值：模型正在逐 token
 * 生成某个工具调用的参数，写文件的整份内容就藏在这个阶段里。
 */
export interface StatusLine {
  phase: string;
  detail?: string;
  turn?: number;
  tool?: string;
  /** 参数 JSON 的**已累积字节数**。不是最终内容长度，别当百分比的分母。 */
  chars?: number;
}

/** 任务清单的一条（与内核 `TodoItem` 对应）。 */
export interface TodoRow {
  content: string;
  status: string;
}

/** 一次运行的聚合视图：ownership 本身仍归 `runOwnership`，这里管**可见结果**。 */
export interface AiRunAttempt {
  generation: number;
  kind: "chat" | "takeover";
  jobId: string | null;
  /** 计划模式：planSubmitted 已收到，done 时要把收尾气泡升级成方案卡片。 */
  planPending: boolean;
  /** 终态只能被写一次：先到先得，后续 done/error/取消一律不再改写。 */
  outcome: "done" | "error" | "canceled" | null;
}

export interface ConversationState {
  items: ChatItem[];
  attempts: AiRunAttempt[];
  status: StatusLine | null;
  usage: AiUsage | null;
  todos: TodoRow[];
  /** 本地条目 id 自增序号（在 state 里，保证纯函数可重放）。 */
  seq: number;
}

export function createConversation(): ConversationState {
  return { items: [], attempts: [], status: null, usage: null, todos: [], seq: 0 };
}

export function attemptOf(
  state: ConversationState,
  generation: number,
): AiRunAttempt | undefined {
  return state.attempts.find((a) => a.generation === generation);
}

function patchAttempt(
  state: ConversationState,
  generation: number,
  patch: Partial<AiRunAttempt>,
): ConversationState {
  return {
    ...state,
    attempts: state.attempts.map((a) => (a.generation === generation ? { ...a, ...patch } : a)),
  };
}

function nextId(state: ConversationState, prefix: string): { id: string; seq: number } {
  return { id: `${prefix}${state.seq}`, seq: state.seq + 1 };
}

function appendItems(state: ConversationState, items: ChatItem[]): ConversationState {
  return { ...state, items: [...state.items, ...items] };
}

/** 新一轮开始：登记 attempt（幂等），不碰既有条目 —— 历史输出必须原样保留。 */
export function beginRun(
  state: ConversationState,
  generation: number,
  kind: "chat" | "takeover" = "chat",
): ConversationState {
  if (attemptOf(state, generation)) return state;
  return {
    ...state,
    // 新一轮 = 新的阶段序列，上一轮的状态条不遗留（与旧 send 的 setStatus(null) 一致）。
    status: null,
    attempts: [...state.attempts, { generation, kind, jobId: null, planPending: false, outcome: null }],
  };
}

export function appendUserMessage(
  state: ConversationState,
  generation: number,
  text: string,
  imageCount?: number,
): ConversationState {
  const { id, seq } = nextId(state, "u");
  return appendItems({ ...state, seq }, [
    { id, attempt: generation, role: "user", text, imageCount: imageCount || undefined },
  ]);
}

/** RPC 返回后回填 jobId；待处理的交互卡也一并补上，保持身份字段一致。 */
export function bindRunJob(
  state: ConversationState,
  generation: number,
  jobId: string,
): ConversationState {
  const attempt = attemptOf(state, generation);
  if (!attempt || attempt.jobId === jobId) return state;
  const next = patchAttempt(state, generation, { jobId });
  return {
    ...next,
    items: next.items.map((item) =>
      item.attempt === generation &&
      (item.role === "confirm" || item.role === "question") &&
      !item.jobId
        ? { ...item, jobId }
        : item,
    ),
  };
}

/**
 * 逐 token 文本的归并：与上一条**同 role 同 attempt** 才合并，
 * 否则新开一条。跨 attempt / 跨类型绝不合并 —— 那是两轮输出糊在一起的根因。
 */
function appendStreamText(
  state: ConversationState,
  generation: number,
  role: "assistant" | "reasoning",
  text: string,
): ConversationState {
  if (!text) return state;
  const last = state.items[state.items.length - 1];
  if (last && last.role === role && last.attempt === generation) {
    const merged = { ...last, text: last.text + text };
    return { ...state, items: [...state.items.slice(0, -1), merged] };
  }
  const { id, seq } = nextId(state, role === "assistant" ? "a" : "r");
  return appendItems({ ...state, seq }, [{ id, attempt: generation, role, text }]);
}

/** 带内核 id 的条目：重放同一事件 ⇒ 同一 id ⇒ 已存在就不再追加。 */
function hasItem(state: ConversationState, id: string): boolean {
  return state.items.some((item) => item.id === id);
}

function replaceItem(state: ConversationState, id: string, next: ChatItem): ConversationState {
  return { ...state, items: state.items.map((item) => (item.id === id ? next : item)) };
}

/** 本轮所有待处理交互的收尾：终态 / 停止时统一关闭，卡片不再可点。 */
function closeInteractions(
  state: ConversationState,
  generation: number,
  label: string,
): ConversationState {
  return {
    ...state,
    items: state.items.map((item) =>
      item.attempt === generation &&
      (item.role === "confirm" || item.role === "question") &&
      item.resolution === undefined
        ? { ...item, resolution: label }
        : item,
    ),
  };
}

/**
 * 用户完成一次交互（确认 / 回答）后的落账。
 *
 * 按**条目 id + nonce** 匹配：RPC 等待期间到来的新交互有不同的 id，
 * 不会被这次完成误清 —— 与 `clearInteractionIfMatch` 同一套身份语义。
 */
export function resolveInteraction(
  state: ConversationState,
  generation: number,
  itemId: string,
  nonce: string,
  label: string,
): ConversationState {
  const item = state.items.find((i) => i.id === itemId);
  if (
    !item ||
    item.attempt !== generation ||
    (item.role !== "confirm" && item.role !== "question") ||
    item.nonce !== nonce ||
    item.resolution !== undefined
  ) {
    return state;
  }
  return replaceItem(state, itemId, { ...item, resolution: label });
}

/** 底部常驻卡片 = 当前轮**最后一张**仍未处理的交互（与旧实现同一选择口径）。 */
export function pendingInteraction<K extends "confirm" | "question">(
  state: ConversationState,
  generation: number | null,
  kind: K,
): Extract<InteractionItem, { role: K }> | null {
  if (generation === null) return null;
  for (let i = state.items.length - 1; i >= 0; i--) {
    const item = state.items[i];
    if (item.attempt === generation && item.role === kind && item.resolution === undefined) {
      return item as Extract<InteractionItem, { role: K }>;
    }
  }
  return null;
}

/** 快照补卡时确认卡的参数兜底文案：内核给多少就展示多少，不做渲染层猜测。 */
function renderInterruptParameters(parameters: unknown): string {
  try {
    return JSON.stringify(parameters, null, 2) ?? "";
  } catch {
    return String(parameters);
  }
}

/**
 * 从 HITL 快照的中断请求补一张交互卡（重连对账专用）。
 *
 * 与 confirmRequired/questionRequired 共用同一套 id 方案：同一 callId 的流事件
 * 后到时按 id 去重，不会出第二张卡；id 已被占（同 callId 的旧请求已结算）时
 * 退回本地自增 id。快照没有渲染层字段（rendered/preview），确认卡只能退回
 * 展示原始参数。已终态的轮次不补卡 —— 终态是可见结果，不被快照复活。
 */
export function appendHitlInterrupt(
  state: ConversationState,
  generation: number,
  request: AiHitlInterruptDto,
): ConversationState {
  const attempt = attemptOf(state, generation);
  if (!attempt || attempt.outcome) return state;
  const role = request.kind === "question" ? "question" : "confirm";
  const callId = request.callId || "";
  const kernelId = callId ? `g${generation}:${role}:${callId}` : "";
  let id = kernelId;
  let seq = state.seq;
  if (!id || hasItem(state, id)) {
    const local = nextId(state, role === "confirm" ? "c" : "q");
    id = local.id;
    seq = local.seq;
  }
  const base = {
    id,
    attempt: generation,
    jobId: attempt.jobId ?? "",
    callId,
    nonce: request.nonce,
    requestId: request.id || undefined,
    hitlAttempt: request.attempt >= 1 ? request.attempt : undefined,
  };
  if (role === "question") {
    return appendItems({ ...state, seq }, [
      {
        ...base,
        role: "question",
        question: request.question?.text ?? "",
        options: request.question?.options ?? [],
      },
    ]);
  }
  return appendItems({ ...state, seq }, [
    {
      ...base,
      role: "confirm",
      tool: request.tool,
      rendered: renderInterruptParameters(request.parameters),
      reason: "",
      preview: null,
    },
  ]);
}

export interface ApplyResult {
  state: ConversationState;
  /** false =  stale / 迟到 / 重复事件，状态未变（终态除外，见 terminal）。 */
  accepted: boolean;
  /** 本条是不是终态事件（即使因重复而未被接受也如实上报，调用方要释放通道）。 */
  terminal: "done" | "error" | null;
}

function rejected(state: ConversationState, terminal: "done" | "error" | null = null): ApplyResult {
  return { state, accepted: false, terminal };
}

function accepted(state: ConversationState, terminal: "done" | "error" | null = null): ApplyResult {
  return { state, accepted: true, terminal };
}

function textOf(ev: Record<string, unknown>): string {
  return typeof ev.text === "string" ? ev.text : "";
}

function kernelId(ev: Record<string, unknown>): string {
  return typeof ev.id === "string" ? ev.id : "";
}

/**
 * 把一条内核事件折进会话状态。
 *
 * 幂等口径：
 *   · 未知 / 已终态的 attempt：非终态事件丢弃；终态事件只上报不改动（先到先得）；
 *   · 带内核 id 的条目按 id upsert，重放不会产生第二张卡；
 *   · done.answer 是内核的权威最终消息：末段流式气泡**整条**以它为准
 *     （截断 / 重放损坏都一并修复），早先段落不受影响；末段的定位允许
 *     尾部跟着本轮 reasoning（finalStreamedSegment）；
 *   · raw answer 为空时不再对已流出的正文补「(无回答)」占位气泡。
 *   · delta / reasoning 没有内核 id，它们的重放去重在控制器里
 *     （conversationStream.ts，只靠 attempt + seq 精确身份，不按内容）。
 */
export function applyAiEvent(
  state: ConversationState,
  generation: number,
  ev: Record<string, unknown>,
): ApplyResult {
  const type = ev.type as string;
  const terminalEvent = type === "done" || type === "error" ? (type as "done" | "error") : null;
  const attempt = attemptOf(state, generation);
  if (!attempt) return rejected(state, terminalEvent);
  if (attempt.outcome) return rejected(state, terminalEvent);

  switch (type) {
    case "status":
      return accepted({
        ...state,
        status: {
          phase: ev.phase as string,
          detail: (ev.detail as string) || undefined,
          turn: (ev.turn as number) ?? undefined,
        },
      });
    case "delta":
      // 开始吐字就说明不再"思考中"了，状态条让位给正文。
      return accepted({ ...appendStreamText(state, generation, "assistant", textOf(ev)), status: null });
    case "reasoning":
      return accepted(appendStreamText(state, generation, "reasoning", textOf(ev)));
    case "toolArgs":
      // 参数还在长：只更新状态条（内核已节流），不产生消息条目。
      return accepted({
        ...state,
        status: { phase: "tool_args", tool: ev.tool as string, chars: ev.chars as number },
      });
    case "toolCall": {
      const callId = kernelId(ev);
      const id = callId ? `g${generation}:tool:${callId}` : "";
      if (id && hasItem(state, id)) return rejected(state);
      const seqId = id || nextId(state, "t").id;
      const seq = id ? state.seq : state.seq + 1;
      // 参数齐了 ⇒ 卡片接管，状态条让位（否则会同时挂着两处「正在写入」）。
      const next = appendItems({ ...state, seq, status: null }, [
        {
          id: seqId,
          attempt: generation,
          role: "tool",
          callId,
          name: ev.name as string,
          display: (ev.display as string) || (ev.name as string),
        },
      ]);
      return accepted(next);
    }
    case "toolResult": {
      const callId = kernelId(ev);
      let target: ToolItem | undefined;
      if (callId) {
        const found = state.items.find(
          (item): item is ToolItem =>
            item.role === "tool" && item.attempt === generation && item.callId === callId,
        );
        target = found;
      } else {
        // 无 id 的兜底（旧内核）：沿用「最近一张未完结的工具卡」。
        target = [...state.items].reverse().find(
          (item): item is ToolItem =>
            item.role === "tool" && item.attempt === generation && item.summary === undefined,
        );
      }
      if (!target) return rejected(state);
      // 每次调用只会有一个结果：已有结果再到 ⇒ 重放，先到先得，不改写。
      if (target.summary !== undefined) return rejected(state);
      return accepted(
        replaceItem(state, target.id, {
          ...target,
          summary: ev.summary as string,
          text: textOf(ev),
          ok: ev.ok as boolean,
          exitCode: ev.exitCode as number | null,
        }),
      );
    }
    case "fileChange": {
      const changeId = kernelId(ev);
      const id = changeId ? `g${generation}:diff:${changeId}` : "";
      if (id && hasItem(state, id)) return rejected(state);
      const seqId = id || nextId(state, "d").id;
      const seq = id ? state.seq : state.seq + 1;
      return accepted(
        appendItems({ ...state, seq }, [
          {
            id: seqId,
            attempt: generation,
            role: "diff",
            path: ev.path as string,
            before: (ev.before as string) ?? "",
            after: (ev.after as string) ?? "",
          },
        ]),
      );
    }
    case "confirmRequired": {
      const callId = kernelId(ev);
      const id = callId ? `g${generation}:confirm:${callId}` : "";
      if (id && hasItem(state, id)) return rejected(state);
      const seqId = id || nextId(state, "c").id;
      const seq = id ? state.seq : state.seq + 1;
      return accepted(
        appendItems({ ...state, seq }, [
          {
            id: seqId,
            attempt: generation,
            role: "confirm",
            jobId: attempt.jobId ?? "",
            callId,
            tool: ev.tool as string,
            rendered: ev.rendered as string,
            reason: (ev.reason as string) || "",
            // 没有改动预览就是 null —— 卡片退回展示原始参数，这里不做任何猜测。
            preview: (ev.preview as FilePreviewItem | null) ?? null,
            nonce: confirmationNonceOf(ev),
            ...interactionIdentityOf(ev),
          },
        ]),
      );
    }
    case "questionRequired": {
      const callId = kernelId(ev);
      const id = callId ? `g${generation}:question:${callId}` : "";
      if (id && hasItem(state, id)) return rejected(state);
      const seqId = id || nextId(state, "q").id;
      const seq = id ? state.seq : state.seq + 1;
      const question = questionFromEvent(ev);
      return accepted(
        appendItems({ ...state, seq }, [
          {
            id: seqId,
            attempt: generation,
            role: "question",
            jobId: attempt.jobId ?? "",
            callId,
            nonce: confirmationNonceOf(ev),
            ...question,
            ...interactionIdentityOf(ev),
          },
        ]),
      );
    }
    case "screen": {
      // 接管读屏：只保留最新一屏（全会话范围去重，与旧实现一致）。
      const { id, seq } = nextId(state, "s");
      const rest = state.items.filter((i) => !(i.role === "tool" && i.name === "read_screen"));
      return accepted({
        ...state,
        seq,
        items: [
          ...rest,
          {
            id,
            attempt: generation,
            role: "tool",
            callId: "",
            name: "read_screen",
            display: "读屏",
            summary: textOf(ev).slice(-200),
            text: textOf(ev).slice(-4000),
            ok: true,
          },
        ],
      });
    }
    case "usage":
      // 每轮覆盖（不是累加）：圆环要回答的是「现在还剩多少」。
      return accepted({
        ...state,
        usage: {
          promptTokens: Number(ev.promptTokens) || 0,
          completionTokens: Number(ev.completionTokens) || 0,
          cachedTokens: Number(ev.cachedTokens) || 0,
          contextWindow: Number(ev.contextWindow) || 0,
        },
      });
    case "todos":
      return accepted({ ...state, todos: (ev.items as TodoRow[]) ?? [] });
    case "planSubmitted":
      // 只立旗子，气泡等 done 到了再落 —— 否则同一份方案会在对话里出现两遍。
      return accepted(patchAttempt(state, generation, { planPending: true }));
    case "done":
      return accepted(finishDone(state, attempt, ev), "done");
    case "error": {
      const message = (ev.message as string) || "未知错误";
      let next = patchAttempt(state, generation, { outcome: "error", planPending: false });
      next = closeInteractions({ ...next, status: null }, generation, "本轮已出错，交互已关闭");
      next = appendOutcome(next, generation, "error", message);
      return accepted(next, "error");
    }
    default:
      return rejected(state);
  }
}

/** done 的收尾：权威答案对账 + 终态标记，整个函数只被「首次 done」调用。 */
function finishDone(
  state: ConversationState,
  attempt: AiRunAttempt,
  ev: Record<string, unknown>,
): ConversationState {
  const generation = attempt.generation;
  // raw 记下"内核到底给没给答案"：answer 会被填成占位串，那就不能再拿它
  // 跟流式正文比对 —— 占位串永远不可能等于正文，会误走追加分支。
  const raw = (ev.answer as string) || "";
  const answer = raw || "(无回答)";
  const wasPlan = attempt.planPending;
  let next = patchAttempt(state, generation, { outcome: "done", planPending: false });
  next = closeInteractions({ ...next, status: null }, generation, "本轮已结束，交互已关闭");

  if (attempt.kind === "takeover") {
    const { id, seq } = nextId(next, "a");
    next = appendItems({ ...next, seq }, [
      { id, attempt: generation, role: "assistant", text: `接管结束：${answer}` },
    ]);
    return appendOutcome(next, generation, "done", "接管已完成");
  }

  const segment = finalStreamedSegment(next.items, generation);
  if (raw && segment >= 0) {
    // 权威答案对账：**整条末段换成 done.answer**，不做相似度猜测。
    // answer 就是内核的最后一条 assistant 消息，而末段气泡正是它的流式形态 ——
    // 截断、代理改包、重连重放把 chunk 重复或交错，都只是"同一条消息的损坏版本"，
    // 权威答案一律为真。逐字相同 / 前缀 / 超集只是这条规则的特例，不再单列。
    // 早先的气泡（前几轮 assistant 消息、工具卡）不动，只有末段参与对账。
    // 计划模式要把这条升级成 plan 气泡，批准按钮才有着落。
    const last = next.items[segment] as Extract<ChatItem, { role: "assistant" | "plan" }>;
    const items = next.items.map((item, index) =>
      index === segment
        ? wasPlan
          ? { ...last, role: "plan" as const, text: answer }
          : { ...last, text: answer }
        : item,
    );
    return appendOutcome({ ...next, items }, generation, "done", "本轮已完成");
  }
  if (!raw && segment >= 0) {
    const last = next.items[segment] as Extract<ChatItem, { role: "assistant" | "plan" }>;
    if (last.text.trim()) {
      // 内核没给答案（raw 为空）而正文已经流出：内容就在屏幕上，
      // 不再补一条「(无回答)」占位气泡冒充新输出。
      const items = wasPlan
        ? next.items.map((item, index) =>
            index === segment ? { ...last, role: "plan" as const } : item,
          )
        : next.items;
      return appendOutcome({ ...next, items }, generation, "done", "本轮已完成");
    }
  }
  // 找不到本轮的流式末段（非流式回退 / 只有推理或工具卡）：答案单独落一条。
  const { id, seq } = nextId(next, wasPlan ? "p" : "a");
  next = appendItems({ ...next, seq }, [
    wasPlan
      ? { id, attempt: generation, role: "plan", text: answer }
      : { id, attempt: generation, role: "assistant", text: answer },
  ]);
  return appendOutcome(next, generation, "done", "本轮已完成");
}

/**
 * 本轮**最后一段流式 assistant/plan 气泡**的下标；找不到返回 -1。
 *
 * 允许它后面跟着本轮的 reasoning：两家提供方都按到达顺序推事件，
 * 最后一段正文之后再补一段推理是合法流。中间隔着工具卡 / 交互卡 / 文件变更
 * 就不算了 —— 那说明模型已经走进下一轮工具调用，最终答案应该另起一条，
 * 而不是回头改写早先的旁白。
 */
function finalStreamedSegment(items: ChatItem[], generation: number): number {
  let i = items.length - 1;
  while (i >= 0 && items[i].attempt === generation && items[i].role === "reasoning") i--;
  const last = items[i];
  if (last && last.attempt === generation && (last.role === "assistant" || last.role === "plan")) {
    return i;
  }
  return -1;
}

function appendOutcome(
  state: ConversationState,
  generation: number,
  outcome: "done" | "error" | "canceled",
  text: string,
): ConversationState {
  const id = `g${generation}:outcome`;
  if (hasItem(state, id)) return state;
  return appendItems(state, [{ id, attempt: generation, role: "outcome", outcome, text }]);
}

/**
 * 用户点「停止」：chat 取消成功即收尾；takeover 必须等终端终态，
 * 这里只清状态条与待处理交互，不抢先写终态（与 runOwnership 的分工一致）。
 */
export function cancelRun(
  state: ConversationState,
  generation: number,
  settle: boolean,
): ConversationState {
  const attempt = attemptOf(state, generation);
  if (!attempt || attempt.outcome) return state;
  let next = closeInteractions({ ...state, status: null }, generation, "本轮已停止，交互已关闭");
  if (!settle) return next;
  next = patchAttempt(next, generation, { outcome: "canceled", planPending: false });
  return appendOutcome(next, generation, "canceled", "已停止本轮");
}

/** 新建 / 切换历史会话：条目与 attempt 全部换掉，旧代事件从此一律 stale。 */
export function resetConversation(
  state: ConversationState,
  items: ChatItem[] = [],
): ConversationState {
  return { ...createConversation(), seq: state.seq + items.length, items, usage: state.usage, todos: state.todos };
}

/** 持久化消息 → 会话项。只还原文本，工具调用与图片不入历史。 */
export function historyToItems(
  state: ConversationState,
  messages: { role: string; content: unknown }[],
): ChatItem[] {
  const items: ChatItem[] = [];
  let seq = state.seq;
  for (const m of messages) {
    const raw = m.content;
    const text =
      typeof raw === "string"
        ? raw
        : typeof raw === "object" && raw !== null && "content" in raw
          ? String((raw as { content?: unknown }).content ?? "")
          : "";
    if (!text) continue;
    if (m.role === "user") {
      const n =
        typeof raw === "object" && raw !== null && "imageCount" in raw
          ? Number((raw as { imageCount?: unknown }).imageCount ?? 0)
          : 0;
      items.push({ id: `h${seq++}`, attempt: null, role: "user", text, imageCount: n || undefined });
    } else if (m.role === "assistant") {
      items.push({ id: `h${seq++}`, attempt: null, role: "assistant", text });
    }
  }
  return items;
}
