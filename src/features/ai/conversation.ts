import { confirmationNonceOf, interactionIdentityOf, questionFromEvent } from "./aiWire";
import type { AiHitlInterruptDto } from "../../ipc/types";
import type { AiUsage } from "./UsageRing";

export interface FilePreviewItem {
  path: string;
  before: string;
  after: string;
  kind: string;
}

interface ItemBase {
  id: string;
  attempt: number | null;
}

export type SteerDelivery = "pending" | "delivered" | "dropped";

export interface SubagentToolEntry {
  callId: string;
  name: string;
  status: "running" | "ok" | "error";
  summary?: string;
  panic?: boolean;
}

export interface SubagentTimeline {
  subagentId: string;
  depth: number;
  status: "running" | "completed" | "failed" | "canceled";
  text: string;
  tools: SubagentToolEntry[];
  summary?: string;
  error?: string;
}

export type ChatItem =
  | (ItemBase & {
      role: "user";
      text: string;
      imageCount?: number;
      steer?: SteerDelivery;
      messageId?: string;
    })
  | (ItemBase & { role: "assistant"; text: string })
  | (ItemBase & { role: "reasoning"; text: string })
  | (ItemBase & {
      role: "tool";
      callId: string;
      name: string;
      display: string;
      summary?: string;
      text?: string;
      ok?: boolean;
      exitCode?: number | null;
      panic?: boolean;
      subagent?: SubagentTimeline;
    })
  | (ItemBase & { role: "diff"; path: string; before: string; after: string })
  | (ItemBase & { role: "plan"; text: string })
  | (ItemBase & {
      role: "confirm";
      jobId: string;
      callId: string;
      tool: string;
      rendered: string;
      reason?: string;
      preview?: FilePreviewItem | null;
      nonce: string;
      requestId?: string;
      hitlAttempt?: number;
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
      role: "outcome";
      outcome: "done" | "error" | "canceled";
      text: string;
      retryable?: boolean;
      maxIterations?: boolean;
    });

export type ToolItem = Extract<ChatItem, { role: "tool" }>;
export type ConfirmItem = Extract<ChatItem, { role: "confirm" }>;
export type QuestionItem = Extract<ChatItem, { role: "question" }>;
export type InteractionItem = ConfirmItem | QuestionItem;

export interface StatusLine {
  phase: string;
  detail?: string;
  turn?: number;
  tool?: string;
  chars?: number;
}

export interface TodoRow {
  content: string;
  status: string;
}

export interface AiRunAttempt {
  generation: number;
  kind: "chat" | "takeover";
  jobId: string | null;
  planPending: boolean;
  outcome: "done" | "error" | "canceled" | null;
}

export interface ConversationState {
  items: ChatItem[];
  attempts: AiRunAttempt[];
  status: StatusLine | null;
  usage: AiUsage | null;
  todos: TodoRow[];
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

export function beginRun(
  state: ConversationState,
  generation: number,
  kind: "chat" | "takeover" = "chat",
): ConversationState {
  if (attemptOf(state, generation)) return state;
  return {
    ...state,
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

export function appendSteerMessage(
  state: ConversationState,
  generation: number,
  text: string,
): { state: ConversationState; id: string } {
  const { id, seq } = nextId(state, "u");
  return {
    state: appendItems({ ...state, seq }, [{ id, attempt: generation, role: "user", text, steer: "pending" }]),
    id,
  };
}

export function resolveSteerById(
  state: ConversationState,
  generation: number,
  itemId: string,
  delivery: SteerDelivery,
): ConversationState {
  const item = state.items.find((i) => i.id === itemId);
  if (!item || item.attempt !== generation || item.role !== "user" || item.steer !== "pending") {
    return state;
  }
  return replaceItem(state, itemId, { ...item, steer: delivery });
}

function resolveOldestSteer(
  state: ConversationState,
  generation: number,
  delivery: SteerDelivery,
): { state: ConversationState; matched: boolean } {
  const index = state.items.findIndex(
    (item) => item.attempt === generation && item.role === "user" && item.steer === "pending",
  );
  if (index < 0) return { state, matched: false };
  const item = state.items[index] as Extract<ChatItem, { role: "user" }>;
  return { state: replaceItem(state, item.id, { ...item, steer: delivery }), matched: true };
}

function settlePendingSteers(state: ConversationState, generation: number): ConversationState {
  return {
    ...state,
    items: state.items.map((item) =>
      item.attempt === generation && item.role === "user" && item.steer === "pending"
        ? { ...item, steer: "dropped" as const }
        : item,
    ),
  };
}

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

function hasItem(state: ConversationState, id: string): boolean {
  return state.items.some((item) => item.id === id);
}

function replaceItem(state: ConversationState, id: string, next: ChatItem): ConversationState {
  return { ...state, items: state.items.map((item) => (item.id === id ? next : item)) };
}

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

function settleOpenTools(
  state: ConversationState,
  generation: number,
  summary: string,
  subagentStatus: "failed" | "canceled",
): ConversationState {
  return {
    ...state,
    items: state.items.map((item) => {
      if (item.attempt !== generation || item.role !== "tool") return item;
      const subagent =
        item.subagent && item.subagent.status === "running"
          ? {
              ...item.subagent,
              status: subagentStatus,
              tools: item.subagent.tools.map((tool) =>
                tool.status === "running" ? { ...tool, status: "error" as const, summary: tool.summary ?? summary } : tool,
              ),
            }
          : item.subagent;
      if (item.summary !== undefined && subagent === item.subagent) return item;
      return { ...item, summary: item.summary ?? summary, subagent };
    }),
  };
}

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

function renderInterruptParameters(parameters: unknown): string {
  try {
    return JSON.stringify(parameters, null, 2) ?? "";
  } catch {
    return String(parameters);
  }
}

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
  accepted: boolean;
  terminal: "done" | "error" | "canceled" | null;
  gap?: { from: number; to: number } | null;
}

function rejected(state: ConversationState, terminal: "done" | "error" | "canceled" | null = null): ApplyResult {
  return { state, accepted: false, terminal };
}

function accepted(state: ConversationState, terminal: "done" | "error" | "canceled" | null = null): ApplyResult {
  return { state, accepted: true, terminal };
}

function textOf(ev: Record<string, unknown>): string {
  return typeof ev.text === "string" ? ev.text : "";
}

function kernelId(ev: Record<string, unknown>): string {
  return typeof ev.id === "string" ? ev.id : "";
}

function foldSubagent(
  state: ConversationState,
  generation: number,
  ev: Record<string, unknown>,
  fold: (timeline: SubagentTimeline) => SubagentTimeline,
  allowTerminal = false,
): ApplyResult {
  const parentCallId = typeof ev.parentCallId === "string" ? ev.parentCallId : "";
  if (!parentCallId) return rejected(state);
  const target = state.items.find(
    (item): item is ToolItem =>
      item.role === "tool" && item.attempt === generation && item.callId === parentCallId,
  );
  if (!target) return rejected(state);
  const subagentId = typeof ev.subagentId === "string" ? ev.subagentId : "";
  const existing = target.subagent && target.subagent.subagentId === subagentId ? target.subagent : null;
  if (target.subagent && !existing) return rejected(state);
  if (existing && existing.status !== "running" && !allowTerminal) return rejected(state);
  const depth = Number(ev.depth) >= 1 ? Number(ev.depth) : 1;
  const base: SubagentTimeline = existing ?? {
    subagentId,
    depth,
    status: "running",
    text: "",
    tools: [],
  };
  return accepted(replaceItem(state, target.id, { ...target, subagent: fold(base) }));
}

export function safeTokenCount(value: unknown): number {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) return 0;
  return Math.min(Math.floor(parsed), Number.MAX_SAFE_INTEGER);
}

export function applyAiEvent(
  state: ConversationState,
  generation: number,
  ev: Record<string, unknown>,
): ApplyResult {
  const type = ev.type as string;
  const terminalEvent =
    type === "done" || type === "error" || type === "canceled"
      ? (type as "done" | "error" | "canceled")
      : null;
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
      return accepted({ ...appendStreamText(state, generation, "assistant", textOf(ev)), status: null });
    case "reasoning":
      return accepted(appendStreamText(state, generation, "reasoning", textOf(ev)));
    case "steered": {
      const resolved = resolveOldestSteer(state, generation, "delivered");
      return resolved.matched ? accepted(resolved.state) : rejected(state);
    }
    case "steerDropped": {
      const resolved = resolveOldestSteer(state, generation, "dropped");
      return resolved.matched ? accepted(resolved.state) : rejected(state);
    }
    case "toolArgs":
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
        target = [...state.items].reverse().find(
          (item): item is ToolItem =>
            item.role === "tool" && item.attempt === generation && item.summary === undefined,
        );
      }
      if (!target) return rejected(state);
      if (target.summary !== undefined) return rejected(state);
      return accepted(
        replaceItem(state, target.id, {
          ...target,
          summary: ev.summary as string,
          text: textOf(ev),
          ok: ev.ok as boolean,
          exitCode: ev.exitCode as number | null,
          panic: ev.panic === true,
        }),
      );
    }
    case "subagentDelta":
      return foldSubagent(state, generation, ev, (timeline) => ({
        ...timeline,
        text: (timeline.text + textOf(ev)).slice(0, 4000),
      }));
    case "subagentToolCall":
      return foldSubagent(state, generation, ev, (timeline) => {
        const callId = kernelId(ev);
        const entry: SubagentToolEntry = { callId, name: (ev.name as string) ?? "", status: "running" };
        const tools = timeline.tools.some((tool) => tool.callId === callId)
          ? timeline.tools.map((tool) => (tool.callId === callId ? { ...tool, ...entry } : tool))
          : [...timeline.tools, entry];
        return { ...timeline, tools };
      });
    case "subagentToolResult":
      return foldSubagent(state, generation, ev, (timeline) => {
        const callId = kernelId(ev);
        const patch = {
          status: (ev.ok ? "ok" : "error") as "ok" | "error",
          summary: (ev.summary as string) ?? "",
          panic: ev.panic === true,
        };
        const tools = timeline.tools.some((tool) => tool.callId === callId)
          ? timeline.tools.map((tool) => (tool.callId === callId ? { ...tool, ...patch } : tool))
          : [...timeline.tools, { callId, name: "", ...patch }];
        return { ...timeline, tools };
      });
    case "subagentDone":
      return foldSubagent(
        state,
        generation,
        ev,
        (timeline) => {
          const status = ev.status;
          return {
            ...timeline,
            status:
              status === "completed" || status === "failed" || status === "canceled"
                ? status
                : "failed",
            summary: (ev.summary as string) || undefined,
            error: (ev.error as string) || undefined,
          };
        },
        true,
      );
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
            display: "读取屏幕",
            summary: textOf(ev).slice(-200),
            text: textOf(ev).slice(-4000),
            ok: true,
          },
        ],
      });
    }
    case "usage":
      return accepted({
        ...state,
        usage: {
          promptTokens: safeTokenCount(ev.promptTokens),
          completionTokens: safeTokenCount(ev.completionTokens),
          cachedTokens: safeTokenCount(ev.cachedTokens),
          contextWindow: safeTokenCount(ev.contextWindow),
        },
      });
    case "todos":
      return accepted({ ...state, todos: (ev.items as TodoRow[]) ?? [] });
    case "planSubmitted":
      return accepted(patchAttempt(state, generation, { planPending: true }));
    case "done":
      return accepted(settleOpenTools(finishDone(state, attempt, ev), generation, "未返回结果", "canceled"), "done");
    case "error": {
      const message = (ev.message as string) || "未知错误";
      let next = patchAttempt(state, generation, { outcome: "error", planPending: false });
      next = closeInteractions({ ...next, status: null }, generation, "本轮已出错，交互已关闭");
      next = settlePendingSteers(next, generation);
      next = settleOpenTools(next, generation, "本轮出错中断", "failed");
      next = appendOutcome(next, generation, "error", message, ev.retryable === true, ev.maxIterations === true);
      return accepted(next, "error");
    }
    case "canceled": {
      const message = (ev.message as string) || "已停止本轮";
      let next = patchAttempt(state, generation, { outcome: "canceled", planPending: false });
      next = closeInteractions({ ...next, status: null }, generation, "本轮已停止，交互已关闭");
      next = settlePendingSteers(next, generation);
      next = settleOpenTools(next, generation, "已停止", "canceled");
      next = appendOutcome(next, generation, "canceled", message);
      return accepted(next, "canceled");
    }
    default:
      return rejected(state);
  }
}

function finishDone(
  state: ConversationState,
  attempt: AiRunAttempt,
  ev: Record<string, unknown>,
): ConversationState {
  const generation = attempt.generation;
  const raw = (ev.answer as string) || "";
  const answer = raw || "(无回答)";
  const wasPlan = attempt.planPending;
  let next = patchAttempt(state, generation, { outcome: "done", planPending: false });
  next = closeInteractions({ ...next, status: null }, generation, "本轮已结束，交互已关闭");
  next = settlePendingSteers(next, generation);

  if (attempt.kind === "takeover") {
    const { id, seq } = nextId(next, "a");
    next = appendItems({ ...next, seq }, [
      { id, attempt: generation, role: "assistant", text: `接管结束：${answer}` },
    ]);
    return appendOutcome(next, generation, "done", "接管已完成");
  }

  const segment = finalStreamedSegment(next.items, generation);
  if (raw && segment >= 0) {
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
      const items = wasPlan
        ? next.items.map((item, index) =>
            index === segment ? { ...last, role: "plan" as const } : item,
          )
        : next.items;
      return appendOutcome({ ...next, items }, generation, "done", "本轮已完成");
    }
  }
  const { id, seq } = nextId(next, wasPlan ? "p" : "a");
  next = appendItems({ ...next, seq }, [
    wasPlan
      ? { id, attempt: generation, role: "plan", text: answer }
      : { id, attempt: generation, role: "assistant", text: answer },
  ]);
  return appendOutcome(next, generation, "done", "本轮已完成");
}

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
  retryable?: boolean,
  maxIterations?: boolean,
): ConversationState {
  const id = `g${generation}:outcome`;
  if (hasItem(state, id)) return state;
  return appendItems(state, [
    {
      id,
      attempt: generation,
      role: "outcome",
      outcome,
      text,
      retryable,
      ...(maxIterations ? { maxIterations: true } : {}),
    },
  ]);
}

export function cancelRun(
  state: ConversationState,
  generation: number,
  settle: boolean,
): ConversationState {
  const attempt = attemptOf(state, generation);
  if (!attempt || attempt.outcome) return state;
  let next = closeInteractions({ ...state, status: null }, generation, "本轮已停止，交互已关闭");
  next = settlePendingSteers(next, generation);
  next = settleOpenTools(next, generation, "已停止", "canceled");
  if (!settle) return next;
  next = patchAttempt(next, generation, { outcome: "canceled", planPending: false });
  return appendOutcome(next, generation, "canceled", "已停止本轮");
}

export function resetConversation(
  state: ConversationState,
  items: ChatItem[] = [],
): ConversationState {
  return { ...createConversation(), seq: state.seq + items.length, items };
}

function historyField(raw: unknown, key: string): unknown {
  if (typeof raw !== "object" || raw === null) return undefined;
  return (raw as Record<string, unknown>)[key];
}

function historyString(raw: unknown, key: string): string {
  const value = historyField(raw, key);
  return typeof value === "string" ? value : "";
}

function historyTextOf(raw: unknown): string {
  if (typeof raw === "string") return raw;
  const value = historyField(raw, "content");
  return typeof value === "string" ? value : value == null ? "" : String(value);
}

export function historyToItems(
  state: ConversationState,
  messages: { id?: string; role: string; content: unknown }[],
  skipJobIds?: ReadonlySet<string>,
): ChatItem[] {
  const items: ChatItem[] = [];
  let seq = state.seq;
  const pendingTools = new Map<string, number>();
  const jobSkipped = (raw: unknown): boolean => {
    const jobId = historyString(raw, "jobId");
    return jobId !== "" && skipJobIds?.has(jobId) === true;
  };
  for (const m of messages) {
    const raw = m.content;
    const structured = historyField(raw, "type");
    if (typeof structured === "string") {
      if (jobSkipped(raw)) continue;
      if (structured === "toolCall") {
        const callId = historyString(raw, "id");
        const name = historyString(raw, "name");
        pendingTools.set(callId, items.length);
        items.push({
          id: `h${seq++}`,
          attempt: null,
          role: "tool",
          callId,
          name,
          display: name,
        });
        continue;
      }
      if (structured === "toolResult") {
        const callId = historyString(raw, "id");
        const exitCode = historyField(raw, "exitCode");
        const patch = {
          summary: historyString(raw, "summary"),
          text: historyString(raw, "text"),
          ok: historyField(raw, "ok") === true,
          exitCode: typeof exitCode === "number" && Number.isFinite(exitCode) ? exitCode : null,
          panic: historyField(raw, "panic") === true,
        };
        const pending = pendingTools.get(callId);
        if (pending !== undefined) {
          pendingTools.delete(callId);
          items[pending] = { ...(items[pending] as ToolItem), ...patch };
          continue;
        }
        const tool = historyString(raw, "tool");
        items.push({
          id: `h${seq++}`,
          attempt: null,
          role: "tool",
          callId,
          name: tool,
          display: patch.summary || tool,
          ...patch,
        });
        continue;
      }
      if (structured === "fileChange") {
        items.push({
          id: `h${seq++}`,
          attempt: null,
          role: "diff",
          path: historyString(raw, "path"),
          before: historyString(raw, "before"),
          after: historyString(raw, "after"),
        });
        continue;
      }
      if (structured === "planSubmitted") {
        items.push({ id: `h${seq++}`, attempt: null, role: "plan", text: historyString(raw, "plan") });
        continue;
      }
      continue;
    }
    const text = historyTextOf(raw);
    if (!text) continue;
    if (m.role === "user") {
      const n = Number(historyField(raw, "imageCount") ?? 0);
      items.push({
        id: `h${seq++}`,
        attempt: null,
        role: "user",
        text,
        imageCount: n || undefined,
        ...(typeof m.id === "string" && m.id ? { messageId: m.id } : {}),
      });
    } else if (m.role === "assistant") {
      items.push({ id: `h${seq++}`, attempt: null, role: "assistant", text });
    }
  }
  return items;
}

export function truncateItemsAfter(state: ConversationState, itemId: string): ConversationState {
  const index = state.items.findIndex((item) => item.id === itemId);
  if (index < 0 || index === state.items.length - 1) return state;
  const items = state.items.slice(0, index + 1);
  return {
    ...state,
    items,
    attempts: state.attempts.filter((attempt) =>
      items.some((item) => item.attempt === attempt.generation),
    ),
    status: null,
    todos: [],
    usage: null,
  };
}

export function hydrateUserMessageIds(
  state: ConversationState,
  messages: { id?: string; role: string; content: unknown }[],
): ConversationState {
  const known = new Set(
    state.items.map((item) => (item.role === "user" ? item.messageId : undefined)),
  );
  const rowsByJob = new Map<string, string[]>();
  for (const m of messages) {
    if (m.role !== "user" || typeof m.id !== "string" || !m.id || known.has(m.id)) continue;
    const jobId = historyString(m.content, "jobId");
    if (!jobId) continue;
    const queue = rowsByJob.get(jobId);
    if (queue) queue.push(m.id);
    else rowsByJob.set(jobId, [m.id]);
  }
  if (rowsByJob.size === 0) return state;
  let changed = false;
  const items = state.items.map((item) => {
    if (item.role !== "user" || item.messageId || item.attempt === null || item.steer) return item;
    const jobId = attemptOf(state, item.attempt)?.jobId;
    if (!jobId) return item;
    const nextId = rowsByJob.get(jobId)?.shift();
    if (!nextId) return item;
    changed = true;
    return { ...item, messageId: nextId };
  });
  return changed ? { ...state, items } : state;
}
