import { confirmationNonceOf, interactionIdentityOf, questionFromEvent, requiredEnvironmentNamesFromEvent } from "./aiWire";
import type { AiHitlInterruptDto } from "../../ipc/types";
import type { AiUsage } from "./UsageRing";
import { CONFIRM_RESOLUTION, type ConfirmDecision } from "./panel/aiConfirmLabels";
import { t } from "../../i18n";

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
      // History rows keep the job they started (or steered) so a replayed run
      // can be placed after them and a retry can find its source message.
      jobId?: string;
      steered?: boolean;
      // event marks the fixed note of a run a terminal event started; the
      // user did not type it, so it shows as a notice without edit or resend.
      event?: boolean;
    })
  | (ItemBase & {
      role: "assistant";
      text: string;
      // interrupted marks a saved partial answer: text streamed before the run
      // failed or was stopped.
      interrupted?: boolean;
    })
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
      // reasonDetail is technical context for reason, such as a shell parser
      // error; the card shows it only on request.
      reasonDetail?: string;
      preview?: FilePreviewItem | null;
      nonce: string;
      risk?: string;
      commands?: string[];
      rulePattern?: string;
      // sessionScope names what "本会话允许" covers; absent means the card
      // offers no session approval (risky, production or cross-target calls).
      sessionScope?: string;
      // permanentScope names what "永久允许" covers; absent means the card
      // offers no permanent approval.
      permanentScope?: string;
      requestId?: string;
      hitlAttempt?: number;
      resolution?: string;
      // Production challenge: the environment names the confirmation must
      // type back, present only for production-classified targets.
      requiredEnvironmentNames?: string[];
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
      // compact marks where /compact replaced earlier history with a summary.
      role: "compact";
      text: string;
      messages: number;
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
  usageCallIds?: ReadonlySet<string>;
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

/**
 * Removes the run's instructions that never reached the model and returns their text, oldest first, so the caller
 * can send them as a new turn or hand them back to the composer instead of leaving them marked as undelivered.
 */
export function takeDroppedSteers(state: ConversationState, generation: number): { state: ConversationState; texts: string[] } {
  const dropped = (item: ChatItem) => item.attempt === generation && item.role === "user" && item.steer === "dropped";
  const texts = state.items.filter(dropped).map((item) => (item as Extract<ChatItem, { role: "user" }>).text);
  if (texts.length === 0) return { state, texts };
  return { state: { ...state, items: state.items.filter((item) => !dropped(item)) }, texts };
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

// Labels for an interaction another client (or the server) resolved. A confirm
// decision keeps the source-language CONFIRM_RESOLUTION label, which the agent
// approval view maps back to the decision.
export const INTERACTION_HANDLED_ELSEWHERE = t("已在其他地方处理");
const INTERACTION_ANSWERED = t("已回答");
const INTERACTION_EXPIRED = t("等待已过期，无需再处理");

function interactionResolvedLabel(role: "confirm" | "question", decision: unknown): string {
  if (decision === "expired") return INTERACTION_EXPIRED;
  if (decision === "answered") return role === "question" ? INTERACTION_ANSWERED : INTERACTION_HANDLED_ELSEWHERE;
  if (role === "confirm" && typeof decision === "string" && Object.hasOwn(CONFIRM_RESOLUTION, decision)) {
    return CONFIRM_RESOLUTION[decision as ConfirmDecision];
  }
  return INTERACTION_HANDLED_ELSEWHERE;
}

// resolveInteractionsByCallId closes the open confirm/question cards of one
// tool call, keeping a resolution this client already stamped.
export function resolveInteractionsByCallId(
  state: ConversationState,
  generation: number,
  callId: string,
  label: (role: "confirm" | "question") => string,
): { state: ConversationState; changed: boolean } {
  if (!callId) return { state, changed: false };
  let changed = false;
  const items = state.items.map((item) => {
    if (
      item.attempt !== generation ||
      (item.role !== "confirm" && item.role !== "question") ||
      item.callId !== callId ||
      item.resolution !== undefined
    ) {
      return item;
    }
    changed = true;
    return { ...item, resolution: label(item.role) };
  });
  return changed ? { state: { ...state, items }, changed } : { state, changed };
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
  // The snapshot interrupt carries the same production challenge contract as
  // the live event; parse it with the same fail-closed rules so a restored
  // card never degrades into the legacy flow the backend would reject.
  const requiredEnvironmentNames = requiredEnvironmentNamesFromEvent({
    requiredEnvironmentNames: request.requiredEnvironmentNames,
  });
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
      ...(requiredEnvironmentNames ? { requiredEnvironmentNames } : {}),
    },
  ]);
}

export interface DrainedEvent {
  ev: Record<string, unknown>;
  accepted: boolean;
}

export interface ApplyResult {
  state: ConversationState;
  accepted: boolean;
  terminal: "done" | "error" | "canceled" | null;
  gap?: { from: number; to: number } | null;
  drained?: DrainedEvent[];
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

export type AiUsageChannel = "main" | "summary" | "tool-subagent";

export function usageChannelOf(ev: Record<string, unknown>): AiUsageChannel {
  if (ev.channel === "main" || ev.channel === "summary" || ev.channel === "tool-subagent") {
    return ev.channel;
  }
  return typeof ev.callId === "string" && ev.callId.startsWith("summary:") ? "summary" : "main";
}

export function hasAgentOwnedUsageID(ev: Record<string, unknown>): boolean {
  const channel = ev.channel;
  const callID = typeof ev.callId === "string" ? ev.callId : "";
  return (
    (channel === "main" || channel === "summary" || channel === "tool-subagent") &&
    callID.startsWith(`${channel}:`)
  );
}

export function usageSnapshotOf(ev: Record<string, unknown>): AiUsage {
  return {
    promptTokens: safeTokenCount(ev.promptTokens),
    completionTokens: safeTokenCount(ev.completionTokens),
    cachedTokens: safeTokenCount(ev.cachedTokens),
    contextWindow: safeTokenCount(ev.contextWindow),
  };
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
      // A result means the call's confirmation or question was settled, even when
      // this client never learned who settled it (servers without interactionResolved).
      const resolved = resolveInteractionsByCallId(state, generation, target?.callId ?? callId, () => INTERACTION_HANDLED_ELSEWHERE);
      if (!target || target.summary !== undefined) {
        return resolved.changed ? accepted(resolved.state) : rejected(state);
      }
      const settled = resolved.state;
      return accepted(
        replaceItem(settled, target.id, {
          ...target,
          summary: ev.summary as string,
          text: textOf(ev),
          ok: ev.ok as boolean,
          exitCode: ev.exitCode as number | null,
          panic: ev.panic === true,
        }),
      );
    }
    case "interactionResolved": {
      const callId = typeof ev.callId === "string" ? ev.callId : kernelId(ev);
      const resolved = resolveInteractionsByCallId(state, generation, callId, (role) =>
        interactionResolvedLabel(role, ev.decision),
      );
      return resolved.changed ? accepted(resolved.state) : rejected(state);
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
      const commands = Array.isArray(ev.commands)
        ? ev.commands.filter((command): command is string => typeof command === "string")
        : undefined;
      const requiredEnvironmentNames = requiredEnvironmentNamesFromEvent(ev);
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
            ...(typeof ev.reasonDetail === "string" && ev.reasonDetail !== "" ? { reasonDetail: ev.reasonDetail } : {}),
            preview: (ev.preview as FilePreviewItem | null) ?? null,
            nonce: confirmationNonceOf(ev),
            risk: typeof ev.risk === "string" ? ev.risk : undefined,
            commands: commands && commands.length > 0 ? commands : undefined,
            rulePattern: typeof ev.rulePattern === "string" ? ev.rulePattern : undefined,
            sessionScope: typeof ev.sessionScope === "string" && ev.sessionScope !== "" ? ev.sessionScope : undefined,
            permanentScope: typeof ev.permanentScope === "string" && ev.permanentScope !== "" ? ev.permanentScope : undefined,
            ...(requiredEnvironmentNames ? { requiredEnvironmentNames } : {}),
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
            display: t("读取屏幕"),
            summary: textOf(ev).slice(-200),
            text: textOf(ev).slice(-4000),
            ok: true,
          },
        ],
      });
    }
    case "usage": {
      const channel = usageChannelOf(ev);
      if (channel === "tool-subagent") return accepted(state);
      const callID = typeof ev.callId === "string" ? ev.callId : "";
      const eventKey = hasAgentOwnedUsageID(ev) ? `${generation}:${channel}:${callID}` : "";
      if (eventKey && state.usageCallIds?.has(eventKey)) return rejected(state);
      let nextCallIds = state.usageCallIds;
      if (eventKey) {
        const mutable = new Set<string>(state.usageCallIds);
        mutable.add(eventKey);
        nextCallIds = mutable;
      }
      const current = state.usage ?? { promptTokens: 0, completionTokens: 0, cachedTokens: 0, contextWindow: 0 };
      if (channel === "summary") {
        return accepted({
          ...state,
          usageCallIds: nextCallIds,
          usage: {
            ...current,
            summaryPromptTokens: safeTokenCount((current.summaryPromptTokens ?? 0) + safeTokenCount(ev.promptTokens)),
            summaryCompletionTokens: safeTokenCount((current.summaryCompletionTokens ?? 0) + safeTokenCount(ev.completionTokens)),
          },
        });
      }
      return accepted({ ...state, usageCallIds: nextCallIds, usage: { ...current, ...usageSnapshotOf(ev) } });
    }
    case "todos":
      return accepted({ ...state, todos: (ev.items as TodoRow[]) ?? [] });
    case "planSubmitted":
      return accepted(patchAttempt(state, generation, { planPending: true }));
    case "paused":
      if (attempt.kind !== "takeover") return rejected(state);
      return accepted({
        ...state,
        status: { phase: "paused", detail: (ev.reason as string) || t("用户暂停") },
      });
    case "resumed":
      if (attempt.kind !== "takeover") return rejected(state);
      return accepted({ ...state, status: null });
    case "done":
      return accepted(settleOpenTools(finishDone(state, attempt, ev), generation, t("未返回结果"), "canceled"), "done");
    case "error": {
      const message = (ev.message as string) || t("未知错误");
      let next = patchAttempt(state, generation, { outcome: "error", planPending: false });
      next = closeInteractions({ ...next, status: null }, generation, t("本次回答已出错，无需再处理"));
      next = settlePendingSteers(next, generation);
      next = settleOpenTools(next, generation, t("回答出错而中断"), "failed");
      next = appendOutcome(next, generation, "error", message, ev.retryable === true, ev.maxIterations === true);
      return accepted(next, "error");
    }
    case "canceled": {
      const message = (ev.message as string) || t("已停止");
      let next = patchAttempt(state, generation, { outcome: "canceled", planPending: false });
      next = closeInteractions({ ...next, status: null }, generation, t("本次回答已停止，无需再处理"));
      next = settlePendingSteers(next, generation);
      next = settleOpenTools(next, generation, t("已停止"), "canceled");
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
  const answer = raw || t("（无回答）");
  const wasPlan = attempt.planPending;
  let next = patchAttempt(state, generation, { outcome: "done", planPending: false });
  next = closeInteractions({ ...next, status: null }, generation, t("本次回答已结束，无需再处理"));
  next = settlePendingSteers(next, generation);

  if (attempt.kind === "takeover") {
    const { id, seq } = nextId(next, "a");
    next = appendItems({ ...next, seq }, [
      { id, attempt: generation, role: "assistant", text: t("接管结束：{answer}", { answer }) },
    ]);
    // Only a takeover the AI reported as done is "completed"; a reclaim, a failure or a step limit just ended.
    const completed = ev.status === undefined || ev.status === "completed";
    return appendOutcome(next, generation, "done", completed ? t("接管已完成") : t("接管已结束"));
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
    return appendOutcome({ ...next, items }, generation, "done", t("回答已完成"));
  }
  if (!raw && segment >= 0) {
    const last = next.items[segment] as Extract<ChatItem, { role: "assistant" | "plan" }>;
    if (last.text.trim()) {
      const items = wasPlan
        ? next.items.map((item, index) =>
            index === segment ? { ...last, role: "plan" as const } : item,
          )
        : next.items;
      return appendOutcome({ ...next, items }, generation, "done", t("回答已完成"));
    }
  }
  const { id, seq } = nextId(next, wasPlan ? "p" : "a");
  next = appendItems({ ...next, seq }, [
    wasPlan
      ? { id, attempt: generation, role: "plan", text: answer }
      : { id, attempt: generation, role: "assistant", text: answer },
  ]);
  return appendOutcome(next, generation, "done", t("回答已完成"));
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
  let next = closeInteractions({ ...state, status: null }, generation, t("本次回答已停止，无需再处理"));
  next = settlePendingSteers(next, generation);
  next = settleOpenTools(next, generation, t("已停止"), "canceled");
  if (!settle) return next;
  next = patchAttempt(next, generation, { outcome: "canceled", planPending: false });
  return appendOutcome(next, generation, "canceled", t("已停止"));
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
  const value = historyField(raw, "content");
  return typeof value === "string" ? value : "";
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
  // A saved model turn keeps its reasoning on the row that opens the turn
  // (its text, or its first tool call), so it is shown before that row.
  const pushReasoning = (raw: unknown) => {
    const reasoning = historyString(raw, "reasoning");
    if (reasoning.trim()) items.push({ id: `h${seq++}`, attempt: null, role: "reasoning", text: reasoning });
  };
  for (const m of messages) {
    const raw = m.content;
    const structured = historyField(raw, "type");
    if (typeof structured === "string") {
      if (jobSkipped(raw)) continue;
      if (structured === "toolCall") {
        const callId = historyString(raw, "id");
        const name = historyString(raw, "name");
        pushReasoning(raw);
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
      if (structured === "summary") {
        const messages = Number(historyField(raw, "messages") ?? 0);
        const compact: ChatItem = { id: `h${seq++}`, attempt: null, role: "compact", text: historyString(raw, "summary"), messages: messages || 0 };
        // An automatic summary is saved after the run's own message but replaces
        // the history before that run, so it is shown before the message.
        const beforeJobId = historyField(raw, "auto") === true ? historyField(raw, "beforeJobId") : undefined;
        let anchor = -1;
        if (typeof beforeJobId === "string" && beforeJobId) {
          for (let i = items.length - 1; i >= 0; i--) {
            const candidate = items[i];
            if (candidate.role === "user" && candidate.jobId === beforeJobId && !candidate.steered) {
              anchor = i;
              break;
            }
          }
        }
        if (anchor >= 0) items.splice(anchor, 0, compact);
        else items.push(compact);
        continue;
      }
      continue;
    }
    const text = historyTextOf(raw);
    if (!text) continue;
    if (historyField(raw, "interim") === true) {
      // Assistant text written before a turn's tool calls. An unfinished run is
      // rebuilt from its events, which carry the same text, so its rows are skipped.
      if (jobSkipped(raw)) continue;
      pushReasoning(raw);
      items.push({ id: `h${seq++}`, attempt: null, role: "assistant", text });
      continue;
    }
    if (historyField(raw, "partial") === true) {
      // Text a run streamed before it failed or was stopped. A replayed run
      // shows the same text from its events, so its row is skipped.
      if (jobSkipped(raw) || !text.trim()) continue;
      pushReasoning(raw);
      items.push({ id: `h${seq++}`, attempt: null, role: "assistant", text, interrupted: true });
      continue;
    }
    if (m.role === "user") {
      const n = Number(historyField(raw, "imageCount") ?? 0);
      const jobId = historyString(raw, "jobId");
      items.push({
        id: `h${seq++}`,
        attempt: null,
        role: "user",
        text,
        imageCount: n || undefined,
        ...(typeof m.id === "string" && m.id ? { messageId: m.id } : {}),
        ...(jobId ? { jobId } : {}),
        ...(historyField(raw, "steered") === true ? { steered: true } : {}),
        ...(historyString(raw, "source") === "event" ? { event: true } : {}),
      });
    } else if (m.role === "assistant") {
      pushReasoning(raw);
      items.push({ id: `h${seq++}`, attempt: null, role: "assistant", text });
    }
  }
  return items;
}

export function historyJobIdOf(raw: unknown): string {
  return historyString(raw, "jobId");
}

// placeReplayedRuns moves the items of each run replayed from its events to
// just after that job's saved user rows (its message and any steers), so a
// rebuilt conversation keeps the message order instead of piling replayed
// output after all history. A run whose rows lie before the loaded history
// window (leadingJobIds) goes to the top. Runs sent in this view carry their
// own user item and stay where they are.
export function placeReplayedRuns(
  state: ConversationState,
  leadingJobIds?: ReadonlySet<string>,
): ConversationState {
  let items = state.items;
  let front = 0;
  for (const attempt of state.attempts) {
    const jobId = attempt.jobId;
    if (!jobId) continue;
    const generation = attempt.generation;
    const run = items.filter((item) => item.attempt === generation);
    if (run.length === 0 || run.some((item) => item.role === "user" && item.steer === undefined)) continue;
    const rest = items.filter((item) => item.attempt !== generation);
    let at = -1;
    for (let i = rest.length - 1; i >= 0; i--) {
      const item = rest[i];
      if (item.attempt === null && item.role === "user" && item.jobId === jobId) {
        at = i + 1;
        break;
      }
    }
    if (at < 0) {
      if (!leadingJobIds?.has(jobId)) continue;
      at = front;
      front += run.length;
    }
    items = [...rest.slice(0, at), ...run, ...rest.slice(at)];
  }
  if (items.length === state.items.length && items.every((item, i) => item === state.items[i])) return state;
  return { ...state, items };
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
    usageCallIds: undefined,
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
