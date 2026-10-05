import { memo, useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { ask, promptText } from "../../ui/dialogs";
import { aiApi, modelApi, type AiPermissionConfig, type AiPermissionMode } from "../../ipc/commands";
import { createAiChannel, disposeChannel, onChannelReopen, type IpcChannel } from "../../ipc/events";
import type { AiHitlEventDto, AiHitlSnapshotDto, AiRunDto, MessageDto } from "../../ipc/types";
import { useUi, type TakeoverState } from "../../app/store";
import { formatBinding, formatBindingAria, useKeybindings } from "../../app/keybindings";
import { describeError } from "../../ui/errorText";
import { isImeKeyEvent } from "../../ui/DialogHost";
import { ModelPanel } from "./ModelPanel";
import { ModelSelector } from "./ModelSelector";
import { Markdown } from "./Markdown";
import { diffLineText, hasVisibleChange, simpleDiff } from "./diff";
import { answerInput, confirmationInput } from "./aiWire";
import {
  historyToItems,
  pendingInteraction,
  type ChatItem,
  type ConfirmItem,
  type QuestionItem,
  type StatusLine,
  type SubagentTimeline,
} from "./conversation";
import { createConversationStream, type ConversationStream } from "./conversationStream";
import { useConversationFollow } from "./conversationFollow";
import { findResumableRun, pendingDeadline, replayableJobIds, replayableRuns } from "./runRestore";
import { findItemMatches, stepMatch } from "./conversationSearch";
import { useVirtualWindow } from "./conversationVirtual";
import {
  aiRunBlocksStart,
  bindAiRunJob,
  completeRunCancellation,
  createAiRun,
  finishAiRunSpawn,
  isCurrentAiRun,
  settleAiRun,
  type AiRunSlot,
} from "./runOwnership";
import { UsageRing } from "./UsageRing";
import {
  IconAlert,
  IconBot,
  IconCheck,
  IconChevronDown,
  IconChevronRight,
  IconChevronUp,
  IconClose,
  IconEdit,
  IconHistory,
  IconImage,
  IconList,
  IconLoader,
  IconMonitor,
  IconPlay,
  IconPlus,
  IconRefresh,
  IconSearch,
  IconShield,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";

interface RefChip {
  id: string;
  kind: "asset" | "tab";
  label: string;
  detail: string;
}

const MODE_OPTIONS: { value: AiPermissionMode; label: string; hint: string }[] = [
  { value: "read_only", label: "只读", hint: "仅执行只读命令" },
  { value: "read_write", label: "读写", hint: "写操作需确认" },
  { value: "silent", label: "完全静默", hint: "仅拦截规则命中时确认" },
];

const MODE_LABEL: Record<AiPermissionMode, string> = {
  read_only: "只读",
  read_write: "读写",
  silent: "完全静默",
};

const CONFIRM_RESOLUTION: Record<"allow" | "allow_session" | "deny", string> = {
  allow: "已允许一次",
  allow_session: "本会话已允许此类",
  deny: "已拒绝",
};

const CONVERSATION_KEY = "nexterm.ai.conversation.v1";

function loadPersistedConversationId(): string | undefined {
  try {
    return localStorage.getItem(CONVERSATION_KEY) ?? undefined;
  } catch {
    return undefined;
  }
}

function persistConversationId(id: string | undefined): void {
  try {
    if (id) localStorage.setItem(CONVERSATION_KEY, id);
    else localStorage.removeItem(CONVERSATION_KEY);
  } catch {
  }
}

export function runStatusOf(runs: AiRunDto[]): "running" | "interrupted" | null {
  if (runs.some((run) => run.status === "running")) return "running";
  if (runs.some((run) => run.status === "interrupted" && run.finishedAt == null)) return "interrupted";
  return null;
}

export function AiSidebar({ sessionId, tabId }: { sessionId?: string; tabId?: string }) {
  const { rightOpen, setRightOpen, aiBusy, setAiBusy, pushToast, rightWidth, workspaces } = useUi();
  const bindings = useKeybindings();
  const aiSidebarKeyLabel = formatBinding(bindings.toggleAiSidebar);
  const reclaimKeyLabel = formatBinding(bindings.reclaimTakeover);
  const streamRef = useRef<ConversationStream | null>(null);
  if (!streamRef.current) streamRef.current = createConversationStream();
  const stream = streamRef.current;
  const subscribe = useCallback((onChange: () => void) => stream.subscribe(() => onChange()), [stream]);
  const getSnapshot = useCallback(() => stream.getState(), [stream]);
  const conv = useSyncExternalStore(subscribe, getSnapshot);
  useEffect(() => () => stream.flush(), [stream]);

  const [input, setInput] = useState("");
  const [questionInput, setQuestionInput] = useState("");
  const [submittingCardId, setSubmittingCardId] = useState<string | null>(null);
  const [syncNotice, setSyncNotice] = useState<{
    generation: number;
    phase: "syncing" | "synced" | "failed";
    count: number;
  } | null>(null);
  const runSequenceRef = useRef(0);
  const activeRunRef = useRef<AiRunSlot | null>(null);
  const catchupChains = useRef(new Map<number, Promise<{ applied: number; failed: boolean }>>());
  const runChannelRef = useRef<{
    channel: IpcChannel<unknown>;
    dispose: () => void;
    generation: number;
  } | null>(null);
  const syncTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const resyncTimerRef = useRef<{ generation: number; timer: ReturnType<typeof setTimeout> } | null>(null);
  const restoredChannelRef = useRef<{
    channel: IpcChannel<unknown>;
    offReopen: () => void;
    jobId: string;
    generation: number;
    expiryTimer: ReturnType<typeof setTimeout> | null;
    expiryRetries: number;
  } | null>(null);
  const beginRun = (kind: "chat" | "takeover" = "chat"): AiRunSlot => {
    const run = createAiRun(++runSequenceRef.current, kind);
    activeRunRef.current = run;
    stream.beginRun(run.generation, kind);
    return run;
  };
  const disposeRestoredChannel = () => {
    const restored = restoredChannelRef.current;
    if (!restored) return;
    restoredChannelRef.current = null;
    if (restored.expiryTimer !== null) clearTimeout(restored.expiryTimer);
    restored.offReopen();
    disposeChannel(restored.channel);
  };
  useEffect(() => () => disposeRestoredChannel(), []);
  useEffect(
    () => () => {
      if (syncTimerRef.current !== null) clearTimeout(syncTimerRef.current);
      if (resyncTimerRef.current !== null) clearTimeout(resyncTimerRef.current.timer);
    },
    [],
  );
  const clearResyncTimer = (generation?: number) => {
    const pending = resyncTimerRef.current;
    if (!pending) return;
    if (generation !== undefined && pending.generation !== generation) return;
    clearTimeout(pending.timer);
    resyncTimerRef.current = null;
  };
  const clearResyncState = (generation: number) => {
    clearResyncTimer(generation);
    setSyncNotice((prev) => (prev && prev.generation === generation ? null : prev));
  };
  const markSyncing = (generation: number) => {
    const current = activeRunRef.current;
    if (!isCurrentAiRun(current, generation) || current.settled) return;
    if (syncTimerRef.current !== null) {
      clearTimeout(syncTimerRef.current);
      syncTimerRef.current = null;
    }
    setSyncNotice({ generation, phase: "syncing", count: 0 });
  };
  const markSynced = (generation: number, count: number) => {
    const current = activeRunRef.current;
    if (!isCurrentAiRun(current, generation) || current.settled) return;
    setSyncNotice((prev) =>
      prev && prev.generation === generation ? { generation, phase: "synced", count } : prev,
    );
    if (syncTimerRef.current !== null) clearTimeout(syncTimerRef.current);
    syncTimerRef.current = setTimeout(() => {
      setSyncNotice((prev) => (prev && prev.generation === generation ? null : prev));
    }, 2400);
  };
  const markSyncFailed = (generation: number) => {
    const current = activeRunRef.current;
    if (!isCurrentAiRun(current, generation) || current.settled) return;
    setSyncNotice((prev) =>
      prev && prev.generation === generation ? { generation, phase: "failed", count: 0 } : prev,
    );
  };
  const activeGeneration = activeRunRef.current?.generation ?? null;
  const confirmCard = pendingInteraction(conv, activeGeneration, "confirm");
  const questionCard = pendingInteraction(conv, activeGeneration, "question");
  const hitlWaiting = confirmCard
    ? "等待你确认后继续…"
    : questionCard
      ? "等待你回答后继续…"
      : null;
  useEffect(() => {
    setQuestionInput("");
  }, [questionCard?.id]);
  const [planMode, setPlanMode] = useState(false);
  const [modelPanelOpen, setModelPanelOpen] = useState(false);
  const [conversationId, setConversationId] = useState<string | undefined>(undefined);
  const conversationIdRef = useRef<string | undefined>(undefined);
  const updateConversationId = (id: string | undefined) => {
    conversationIdRef.current = id;
    setConversationId(id);
    persistConversationId(id);
  };
  const deletedConversationIdsRef = useRef<Set<string>>(new Set());
  const [perm, setPerm] = useState<AiPermissionConfig | null>(null);
  const [permOpen, setPermOpen] = useState(false);
  const [images, setImages] = useState<string[]>([]);
  const [refs, setRefs] = useState<RefChip[]>([]);
  const [atOpen, setAtOpen] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [conversations, setConversations] = useState<{ id: string; title: string; updatedAt: number }[]>([]);
  const [runStatusByConv, setRunStatusByConv] = useState<Record<string, "running" | "interrupted">>({});
  const [historyStatus, setHistoryStatus] = useState<"loading" | "error" | "ready">("ready");
  const [historyError, setHistoryError] = useState<string | null>(null);
  const takeover = useUi((s) => s.takeover);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const follow = useConversationFollow(conv.items);
  const virtual = useVirtualWindow(conv.items.length);
  const setScrollEl = useCallback(
    (el: HTMLElement | null) => {
      follow.scrollRef(el);
      virtual.scrollRef(el);
    },
    [follow.scrollRef, virtual.scrollRef],
  );
  const [conversationRuns, setConversationRuns] = useState<AiRunDto[]>([]);
  const [searchOpen, setSearchOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [matchCursor, setMatchCursor] = useState(-1);
  const searchMatches = useMemo(
    () => findItemMatches(conv.items, searchQuery),
    [conv.items, searchQuery],
  );
  const activeMatchIndex = matchCursor >= 0 ? (searchMatches[matchCursor] ?? -1) : -1;
  const jumpToMatch = (direction: 1 | -1) => {
    const next = stepMatch(matchCursor, searchMatches.length, direction);
    setMatchCursor(next);
    const target = searchMatches[next];
    if (target !== undefined) virtual.revealIndex(target);
  };
  useEffect(() => {
    setMatchCursor(searchMatches.length > 0 ? 0 : -1);
  }, [searchMatches]);

  useEffect(() => {
    void aiApi
      .getPermission()
      .then(setPerm)
      .catch(() => undefined);
  }, []);

  useEffect(() => {
    if (permOpen) {
      void aiApi
        .getPermission()
        .then(setPerm)
        .catch(() => undefined);
    }
  }, [permOpen]);

  const savePerm = async (next: AiPermissionConfig) => {
    const prev = perm;
    setPerm(next);
    try {
      await aiApi.setPermission(next);
    } catch (e) {
      setPerm(prev);
      pushToast("error", `保存权限失败：${describeError(e)}`);
    }
  };

  const switchMode = (mode: AiPermissionConfig["mode"]) => {
    void aiApi
      .getPermission()
      .then((latest) => savePerm({ ...latest, mode }))
      .catch(() => savePerm({ ...(perm as AiPermissionConfig), mode }));
  };

  const refCandidates = useMemo<RefChip[]>(() => {
    const out: RefChip[] = [];
    const sess = useUi.getState().sessions.find((s) => s.id === sessionId);
    if (sess) {
      out.push({
        id: `asset:${sess.id}`,
        kind: "asset",
        label: sess.name,
        detail: `${sess.kind} 会话`,
      });
    }
    for (const w of workspaces) {
      for (const p of w.panes) {
        for (const t of p.tabs) {
          if (t.kind !== "terminal") continue;
          out.push({
            id: `tab:${t.id}`,
            kind: "tab",
            label: t.title,
            detail: `${w.title} 的终端`,
          });
        }
      }
    }
    return out;
  }, [sessionId, workspaces]);

  const composeMessage = (text: string): string => {
    if (refs.length === 0) return text;
    const list = refs.map((r) => `- ${r.label}（${r.detail}）`).join("\n");
    return `[引用对象]\n${list}\n\n${text}`;
  };

  const replayHitl = async (generation: number, jobId: string) => {
    const plan = stream.planHitlReplay(generation);
    let events: AiHitlEventDto[] = [];
    try {
      events = await aiApi.hitlEvents(jobId, plan.afterSeq);
    } catch {
      events = [];
    }
    try {
      const snapshot = await aiApi.hitlSnapshot(jobId);
      stream.applyHitlReplay(generation, plan, events, snapshot);
    } catch {
      if (events.length > 0) stream.applyHitlReplay(generation, plan, events, null);
    }
  };

  const settleRestoredRun = (generation: number) => {
    const current = activeRunRef.current;
    if (isCurrentAiRun(current, generation) && !current.settled) {
      activeRunRef.current = settleAiRun(current);
      if (!current.spawnPending) setAiBusy(false);
    }
    const live = runChannelRef.current;
    if (live && live.generation === generation) live.dispose();
    const restored = restoredChannelRef.current;
    if (restored && restored.generation === generation) disposeRestoredChannel();
    clearResyncState(generation);
  };

  const replayRunEvents = async (
    generation: number,
    jobId: string,
  ): Promise<{ applied: number; failed: boolean }> => {
    try {
      const events = await aiApi.runEvents(jobId, stream.lastSeq(generation));
      let applied = 0;
      for (const event of events) {
        const result = stream.pushEvent(generation, event as Record<string, unknown>);
        if (result.accepted) applied += 1;
        if (result.terminal) {
          settleRestoredRun(generation);
        }
      }
      stream.flush();
      return { applied, failed: false };
    } catch {
      return { applied: 0, failed: true };
    }
  };

  const catchUpRunEvents = (
    generation: number,
    jobId: string,
  ): Promise<{ applied: number; failed: boolean }> => {
    const chains = catchupChains.current;
    const prev = chains.get(generation) ?? Promise.resolve({ applied: 0, failed: false });
    const next = prev
      .catch(() => ({ applied: 0, failed: true }))
      .then(() => replayRunEvents(generation, jobId));
    chains.set(generation, next);
    return next;
  };

  const RESYNC_MAX_ATTEMPTS = 2;

  const resyncRun = (generation: number, jobId: string, attempt = 0) => {
    clearResyncTimer(generation);
    markSyncing(generation);
    void catchUpRunEvents(generation, jobId).then((result) => {
      if (!result.failed) {
        markSynced(generation, result.applied);
        return;
      }
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, generation) || current.settled) return;
      if (attempt >= RESYNC_MAX_ATTEMPTS) {
        markSyncFailed(generation);
        return;
      }
      clearResyncTimer(generation);
      resyncTimerRef.current = {
        generation,
        timer: setTimeout(() => resyncRun(generation, jobId, attempt + 1), 1000 * (attempt + 1)),
      };
    });
    void replayHitl(generation, jobId);
  };

  const replayRestoredIfBound = (jobId: string) => {
    const restored = restoredChannelRef.current;
    if (!restored || restored.jobId !== jobId) return;
    void replayRunEvents(restored.generation, jobId);
  };

  const armExpiryWatch = (jobId: string) => {
    const restored = restoredChannelRef.current;
    if (!restored || restored.jobId !== jobId || restored.expiryRetries <= 0) return;
    restored.expiryRetries -= 1;
    restored.expiryTimer = setTimeout(() => {
      const current = restoredChannelRef.current;
      if (!current || current.jobId !== jobId) return;
      current.expiryTimer = null;
      void replayRunEvents(current.generation, jobId).finally(() => {
        const settled = activeRunRef.current?.settled ?? true;
        if (!settled) armExpiryWatch(jobId);
      });
    }, 3000);
  };

  const scheduleExpiryWatch = (snapshot: AiHitlSnapshotDto) => {
    const deadline = pendingDeadline(snapshot);
    if (deadline === null) return null;
    const delay = Math.min(Math.max(0, deadline - Date.now()) + 500, 2_000_000_000);
    return setTimeout(() => {
      const restored = restoredChannelRef.current;
      if (!restored) return;
      void replayRunEvents(restored.generation, restored.jobId).finally(() => {
        const settled = activeRunRef.current?.settled ?? true;
        if (!settled) armExpiryWatch(restored.jobId);
      });
    }, delay);
  };

  const restoreConversationRuns = async (id: string, preloaded?: AiRunDto[]) => {
    let runs: AiRunDto[];
    if (preloaded) {
      runs = preloaded;
    } else {
      try {
        runs = await aiApi.runs(id);
      } catch {
        return;
      }
    }
    if (conversationIdRef.current !== id) return;
    setConversationRuns(runs);
    if (runs.length === 0) return;
    const replay = replayableRuns(runs);
    const generations = new Map<string, number>();
    for (const run of replay) {
      const generation = ++runSequenceRef.current;
      generations.set(run.id, generation);
      stream.beginRun(generation, "chat");
      stream.bindJob(generation, run.id);
      try {
        const events = await aiApi.runEvents(run.id, 0);
        if (conversationIdRef.current !== id) return;
        for (const event of events) {
          stream.pushEvent(generation, event as Record<string, unknown>);
        }
        stream.flush();
      } catch {
        continue;
      }
    }
    const resumable = await findResumableRun(runs, async (jobId) => {
      try {
        return await aiApi.hitlSnapshot(jobId);
      } catch {
        return null;
      }
    });
    if (!resumable || conversationIdRef.current !== id) return;
    const generation = generations.get(resumable.run.id);
    if (generation === undefined) return;
    if (activeRunRef.current && !activeRunRef.current.settled) return;
    const channel = createAiChannel((ev) => {
      const type = ev.type as string;
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, generation) || current.settled) {
        if (type === "done" || type === "error" || type === "canceled") {
          const restored = restoredChannelRef.current;
          if (restored && restored.generation === generation) disposeRestoredChannel();
          clearResyncState(generation);
        }
        return;
      }
      const result = stream.pushEvent(generation, ev);
      if (result.gap) resyncRun(generation, resumable.run.id);
      if (!result.terminal) return;
      settleRestoredRun(generation);
      if (type === "error" && result.accepted) pushToast("error", `AI: ${ev.message as string}`);
    });
    const offReopen = onChannelReopen(channel, () => {
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, generation) || current.settled) return;
      resyncRun(generation, resumable.run.id);
    });
    restoredChannelRef.current = {
      channel,
      offReopen,
      jobId: resumable.run.id,
      generation,
      expiryTimer: scheduleExpiryWatch(resumable.snapshot),
      expiryRetries: 8,
    };
    activeRunRef.current = { generation, kind: "chat", jobId: resumable.run.id, spawnPending: false, settled: false };
    setAiBusy(true);
    void replayHitl(generation, resumable.run.id);
  };

  useEffect(() => {
    const id = loadPersistedConversationId();
    if (!id) return;
    const startSeq = runSequenceRef.current;
    updateConversationId(id);
    void (async () => {
      try {
        const list = await aiApi.conversationList();
        if (conversationIdRef.current !== id) return;
        if (!list.some((c) => c.id === id)) {
          updateConversationId(undefined);
          return;
        }
      } catch {
        if (conversationIdRef.current !== id) return;
      }
      let msgs: MessageDto[];
      let runs: AiRunDto[];
      try {
        [msgs, runs] = await Promise.all([
          aiApi.messages(id),
          aiApi.runs(id).catch((): AiRunDto[] => []),
        ]);
      } catch {
        return;
      }
      if (conversationIdRef.current !== id || runSequenceRef.current !== startSeq) return;
      stream.reset(historyToItems(stream.getState(), msgs, replayableJobIds(runs)));
      if (conversationIdRef.current !== id || runSequenceRef.current !== startSeq) return;
      void restoreConversationRuns(id, runs);
    })();
  }, []);

  const hydrateMessageIds = async (convId: string) => {
    try {
      const msgs = await aiApi.messages(convId);
      if (conversationIdRef.current !== convId) return;
      stream.hydrateUserMessageIds(msgs);
    } catch {
      return;
    }
  };

  const send = async (override?: { message?: string; planMode?: boolean }) => {
    const message = (override?.message ?? input).trim();
    if (
      (!message && images.length === 0) ||
      useUi.getState().aiBusy ||
      aiRunBlocksStart(activeRunRef.current)
    ) {
      return;
    }
    const run = beginRun();
    const usePlan = override?.planMode ?? planMode;
    setAiBusy(true);
    setInput("");
    setRefs([]);
    setAtOpen(false);
    const sentImages = images;
    setImages([]);
    stream.appendUser(run.generation, message, sentImages.length);

    const channel = createAiChannel((ev) => {
      const type = ev.type as string;
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, run.generation) || current.settled) {
        if (type === "done" || type === "error" || type === "canceled") {
          clearResyncState(run.generation);
          dispose();
        }
        return;
      }
      const result = stream.pushEvent(run.generation, ev);
      if (result.gap && current.jobId) resyncRun(run.generation, current.jobId);
      if (!result.terminal) return;
      settleRestoredRun(run.generation);
      if (type === "error" && result.accepted) pushToast("error", `AI: ${ev.message as string}`);
    });

    const offHitlReopen = onChannelReopen(channel, () => {
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, run.generation) || current.settled || !current.jobId) return;
      resyncRun(run.generation, current.jobId);
    });
    const dispose = () => {
      if (runChannelRef.current?.channel === channel) runChannelRef.current = null;
      offHitlReopen();
      disposeChannel(channel);
    };
    runChannelRef.current = { channel, dispose, generation: run.generation };

    try {
      const res = await aiApi.chat({
        conversationId,
        scope: { sessionId, tabId },
        message: composeMessage(message),
        images: sentImages.length ? sentImages : undefined,
        planMode: usePlan || undefined,
        channel,
      });
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, run.generation)) {
        dispose();
        return;
      }
      if (!deletedConversationIdsRef.current.has(res.conversationId)) {
        updateConversationId(res.conversationId);
      }
      if (current.settled) {
        activeRunRef.current = finishAiRunSpawn(current);
        setAiBusy(false);
        return;
      }
      activeRunRef.current = bindAiRunJob(current, res.jobId);
      stream.bindJob(run.generation, res.jobId);
      if (stream.hasGap(run.generation)) resyncRun(run.generation, res.jobId);
      void hydrateMessageIds(res.conversationId);
    } catch (e) {
      const current = activeRunRef.current;
      if (isCurrentAiRun(current, run.generation)) {
        const result = stream.pushEvent(run.generation, {
          type: "error",
          message: describeError(e),
        });
        if (result.accepted && !current.settled) pushToast("error", describeError(e));
        activeRunRef.current = settleAiRun(finishAiRunSpawn(current));
        setAiBusy(false);
        clearResyncState(run.generation);
      }
      dispose();
    }
  };

  const steer = async () => {
    const message = input.trim();
    if (!message) return;
    const run = activeRunRef.current;
    if (!run || run.settled) {
      pushToast("info", "这一轮已结束，补充指令没有送出，可直接作为新消息发送");
      return;
    }
    if (run.spawnPending) {
      pushToast("info", "这一轮还在启动，稍等一下再补充指令");
      return;
    }
    if (images.length > 0) {
      pushToast("info", "运行中的补充指令暂不支持图片，请先移除图片");
      return;
    }
    const target = run;
    const jobId = run.jobId;
    if (run.kind !== "chat" || !jobId) {
      pushToast("info", "终端接管这一轮不支持补充指令");
      return;
    }
    const text = composeMessage(message);
    setInput("");
    const itemId = stream.appendSteer(target.generation, message);
    try {
      await aiApi.steer(jobId, text);
    } catch (e) {
      stream.resolveSteer(target.generation, itemId, "dropped");
      setInput((prev) => (prev ? `${message}\n${prev}` : message));
      pushToast("error", `补充指令未送达：${describeError(e)}`);
    }
  };

  const approvePlan = (plan: string) => {
    setPlanMode(false);
    void send({ message: `按上面的方案执行。\n\n方案原文：\n${plan}`, planMode: false });
  };
  const approvePlanRef = useRef(approvePlan);
  approvePlanRef.current = approvePlan;
  const handleApprovePlan = useCallback((plan: string) => approvePlanRef.current(plan), []);

  const retryRun = (item: ChatItem) => {
    if (item.role !== "outcome" || item.outcome !== "error" || !item.retryable) return;
    if (aiBusy || aiRunBlocksStart(activeRunRef.current)) {
      pushToast("info", "另一轮仍在运行，请先停止再重试");
      return;
    }
    const source = [...conv.items]
      .reverse()
      .find(
        (candidate): candidate is Extract<ChatItem, { role: "user" }> =>
          candidate.attempt === item.attempt && candidate.role === "user" && candidate.steer === undefined,
      );
    if (!source) {
      pushToast("info", "找不到这一轮的消息原文，请手动重新发送");
      return;
    }
    void send({ message: source.text });
  };
  const retryRunRef = useRef(retryRun);
  retryRunRef.current = retryRun;
  const handleRetryRun = useCallback((item: ChatItem) => retryRunRef.current(item), []);

  const [editing, setEditing] = useState<{ itemId: string; messageId: string } | null>(null);
  const [editSubmitting, setEditSubmitting] = useState(false);
  const editSubmittingRef = useRef(false);
  const startEditResend = (item: ChatItem) => {
    if (item.role !== "user" || !item.messageId) return;
    setEditing({ itemId: item.id, messageId: item.messageId });
    setInput(item.text);
    setAtOpen(false);
    inputRef.current?.focus();
  };
  const startEditResendRef = useRef(startEditResend);
  startEditResendRef.current = startEditResend;
  const handleEditResend = useCallback((item: ChatItem) => startEditResendRef.current(item), []);

  const submitEditResend = async () => {
    const edit = editing;
    const text = input.trim();
    const conversationId = conversationIdRef.current;
    if (!edit || !text || editSubmittingRef.current) return;
    if (!conversationId) {
      pushToast("error", "编辑重发需要先打开一个会话");
      return;
    }
    editSubmittingRef.current = true;
    setEditSubmitting(true);
    try {
      await aiApi.editResend(conversationId, edit.messageId);
    } catch (e) {
      editSubmittingRef.current = false;
      setEditSubmitting(false);
      pushToast("error", `编辑重发失败：${describeError(e)}`);
      return;
    }
    try {
      const current = activeRunRef.current;
      if (current && !current.settled) {
        activeRunRef.current = settleAiRun(current);
        stream.cancelRun(current.generation, false);
        clearResyncState(current.generation);
      }
      setAiBusy(false);
      const live = runChannelRef.current;
      if (live) live.dispose();
      disposeRestoredChannel();
      stream.truncateAfterItem(edit.itemId);
      setEditing(null);
      await send({ message: text });
    } finally {
      editSubmittingRef.current = false;
      setEditSubmitting(false);
    }
  };

  const confirm = async (decision: "allow" | "allow_session" | "deny") => {
    const run = activeRunRef.current;
    const card = confirmCard;
    const id = run?.jobId;
    if (!run || !id || !card) {
      pushToast("info", "这一轮还没启动完，稍等一下再点");
      return;
    }
    if (submittingCardId) return;
    setSubmittingCardId(card.id);
    try {
      const input = confirmationInput({ jobId: id, callId: card.callId, nonce: card.nonce }, decision);
      const channel = card.jobId === restoredChannelRef.current?.jobId ? restoredChannelRef.current.channel : undefined;
      if (channel) {
        await aiApi.confirm(input, channel);
      } else {
        await aiApi.confirm(input);
      }
    } catch (e) {
      pushToast("error", `确认失败：${describeError(e)}`);
      if (run.kind === "chat" && isCurrentAiRun(activeRunRef.current, run.generation)) {
        void replayHitl(run.generation, id);
      }
      replayRestoredIfBound(id);
      return;
    } finally {
      setSubmittingCardId(null);
    }
    if (isCurrentAiRun(activeRunRef.current, run.generation)) {
      stream.resolveInteraction(run.generation, card.id, card.nonce, CONFIRM_RESOLUTION[decision]);
    }
  };

  const answer = async (option?: string) => {
    const run = activeRunRef.current;
    const card = questionCard;
    const id = run?.jobId;
    const text = option ?? questionInput;
    if (!run || !id || !card) {
      pushToast("info", "这一轮还没启动完，稍等一下再点");
      return;
    }
    if (!text.trim()) {
      pushToast("info", "请先输入回答，或选择一个问题选项");
      return;
    }
    if (submittingCardId) return;
    setSubmittingCardId(card.id);
    try {
      const input = answerInput({ jobId: id, callId: card.callId, nonce: card.nonce }, text);
      const channel = card.jobId === restoredChannelRef.current?.jobId ? restoredChannelRef.current.channel : undefined;
      if (channel) {
        await aiApi.answer(input, channel);
      } else {
        await aiApi.answer(input);
      }
    } catch (e) {
      pushToast("error", `回答失败：${describeError(e)}`);
      if (run.kind === "chat" && isCurrentAiRun(activeRunRef.current, run.generation)) {
        void replayHitl(run.generation, id);
      }
      replayRestoredIfBound(id);
      return;
    } finally {
      setSubmittingCardId(null);
    }
    if (isCurrentAiRun(activeRunRef.current, run.generation)) {
      stream.resolveInteraction(run.generation, card.id, card.nonce, `已回答：${text}`);
    }
  };

  const stop = async () => {
    const run = activeRunRef.current;
    const id = run?.jobId;
    if (!run || !id) {
      pushToast("info", "这一轮还没启动完，稍等一下再点");
      return;
    }
    try {
      await aiApi.cancel(id);
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, run.generation)) return;
      const cancellation = completeRunCancellation(current);
      activeRunRef.current = cancellation.run;
      stream.cancelRun(run.generation, !cancellation.waitForTerminal);
      if (!cancellation.waitForTerminal) setAiBusy(false);
      const restored = restoredChannelRef.current;
      if (restored && restored.generation === run.generation) disposeRestoredChannel();
      clearResyncState(run.generation);
      pushToast(
        "info",
        cancellation.waitForTerminal ? "已请求停止接管，等待终端退出" : "已停止本轮",
      );
    } catch (e) {
      pushToast("error", `停止失败：${describeError(e)}`);
      replayRestoredIfBound(id);
    }
  };

  const loadRunStatuses = async (ids: string[]) => {
    const entries = await Promise.all(
      ids.map(async (id) => {
        try {
          const runs = await aiApi.runs(id, 20);
          return [id, runStatusOf(Array.isArray(runs) ? runs : [])] as const;
        } catch {
          return [id, null] as const;
        }
      }),
    );
    setRunStatusByConv((prev) => {
      const next = { ...prev };
      for (const [id, status] of entries) {
        if (status) next[id] = status;
        else delete next[id];
      }
      return next;
    });
  };

  const loadConversations = async () => {
    setHistoryStatus("loading");
    setHistoryError(null);
    let ids: string[] = [];
    try {
      const list = await aiApi.conversationList();
      ids = list.map((c) => c.id);
      setConversations(
        list.map((c) => ({ id: c.id, title: c.title, updatedAt: c.updatedAt })),
      );
      setHistoryStatus("ready");
    } catch (e) {
      setHistoryStatus("error");
      setHistoryError(describeError(e));
      return;
    }
    void loadRunStatuses(ids);
  };

  const toggleHistory = () => {
    const next = !historyOpen;
    setHistoryOpen(next);
    if (next) void loadConversations();
  };

  const openConversation = async (id: string) => {
    if (aiBusy || aiRunBlocksStart(activeRunRef.current)) {
      pushToast("info", "这一轮仍在运行，请先停止再切换会话");
      return;
    }
    const generation = activeRunRef.current?.generation ?? null;
    try {
      const [msgs, runs] = await Promise.all([
        aiApi.messages(id),
        aiApi.runs(id).catch((): AiRunDto[] => []),
      ]);
      if (
        aiRunBlocksStart(activeRunRef.current) ||
        (activeRunRef.current?.generation ?? null) !== generation
      ) {
        return;
      }
      stream.reset(historyToItems(stream.getState(), msgs, replayableJobIds(runs)));
      updateConversationId(id);
      setEditing(null);
      setHistoryOpen(false);
      void restoreConversationRuns(id, runs);
    } catch (e) {
      pushToast("error", `打开会话失败：${describeError(e)}`);
    }
  };

  const newConversation = () => {
    if (aiBusy || aiRunBlocksStart(activeRunRef.current)) {
      pushToast("info", "这一轮仍在运行，请先停止再新建会话");
      return;
    }
    updateConversationId(undefined);
    setConversationRuns([]);
    setEditing(null);
    stream.reset();
    setHistoryOpen(false);
  };

  const deleteConversation = async (c: { id: string; title: string }) => {
    const blocksReset = (id: string) =>
      id === conversationIdRef.current && aiRunBlocksStart(activeRunRef.current);
    if (blocksReset(c.id)) {
      pushToast("info", "这一轮仍在运行，请先停止再删除当前会话");
      return;
    }
    try {
      const runs = await aiApi.runs(c.id, 20);
      const inFlight = runStatusOf(Array.isArray(runs) ? runs : []);
      if (inFlight) {
        pushToast(
          "info",
          inFlight === "running"
            ? "这个会话还有正在运行的 AI 任务，等它结束或先停止再删除"
            : "这个会话还有中断待恢复的 AI 任务，先打开会话处理完再删除",
        );
        return;
      }
    } catch {
    }
    const ok = await ask(`删除会话「${c.title || "(未命名会话)"}」？消息记录一并清除，不可恢复。`, {
      kind: "warning",
    });
    if (!ok) return;
    if (blocksReset(c.id)) {
      pushToast("info", "这一轮仍在运行，请先停止再删除当前会话");
      return;
    }
    try {
      await aiApi.conversationDelete(c.id);
    } catch (e) {
      pushToast("error", `删除会话失败：${describeError(e)}`);
      return;
    }
    setConversations((prev) => prev.filter((it) => it.id !== c.id));
    setRunStatusByConv((prev) => {
      const next = { ...prev };
      delete next[c.id];
      return next;
    });
    if (conversationIdRef.current === c.id) {
      deletedConversationIdsRef.current.add(c.id);
      updateConversationId(undefined);
      setEditing(null);
      if (!aiRunBlocksStart(activeRunRef.current)) stream.reset();
    }
  };

  const onInputChange = (v: string) => {
    setInput(v);
    const at = v.lastIndexOf("@");
    if (at < 0) {
      setAtOpen(false);
      return;
    }
    const prevCh = at === 0 ? " " : v[at - 1];
    if (/\s/.test(prevCh)) setAtOpen(true);
  };

  const pickRef = (ref: RefChip) => {
    setRefs((prev) => (prev.some((r) => r.id === ref.id) ? prev : [...prev, ref]));
    setInput((prev) => prev.replace(/@[^@\s]*$/, ""));
    setAtOpen(false);
    inputRef.current?.focus();
  };

  const onPaste = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    const files = Array.from(e.clipboardData.items)
      .filter((it) => it.kind === "file" && it.type.startsWith("image/"))
      .map((it) => it.getAsFile())
      .filter((f): f is File => f !== null);
    if (files.length === 0) return;
    e.preventDefault();
    void Promise.all(files.map(readAsDataUrl))
      .then((urls) => setImages((prev) => [...prev, ...urls]))
      .catch((err) => pushToast("error", `读取图片失败：${describeError(err)}`));
  };

  const runTakeover = async () => {
    if (!tabId) {
      pushToast("error", "终端接管需要先打开一个终端标签");
      return;
    }
    const instruction = await promptText(
      "终端接管：要 AI 去做什么？（例如：安装 nginx 并启动）",
      "",
      { multiLine: true },
    );
    if (!instruction) return;
    const allowWrite = await ask(
      "允许 AI 直接操作这台终端吗？\n\n确定 = 允许 —— AI 可以替你敲命令\n取消 = 只读 —— AI 只能看，不能敲",
      { title: "终端接管（实验性功能）", kind: "warning" },
    );

    if (aiBusy || aiRunBlocksStart(activeRunRef.current)) {
      pushToast("info", "另一轮 AI 仍在运行，请先停止再接管");
      return;
    }
    const run = beginRun("takeover");
    const holder = { settled: false };
    let banner: TakeoverState | null = null;
    const clearTakeover = () => {
      if (banner && useUi.getState().takeover === banner) useUi.getState().setTakeover(null);
    };

    const channel = createAiChannel((ev) => {
      const type = ev.type as string;
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, run.generation) || current.settled) {
        if (type === "done" || type === "error" || type === "canceled") disposeChannel(channel);
        return;
      }
      const result = stream.pushEvent(run.generation, ev);
      if (!result.terminal) return;
      holder.settled = true;
      settleRestoredRun(run.generation);
      clearTakeover();
      if (type === "error" && result.accepted) pushToast("error", `接管：${ev.message as string}`);
      disposeChannel(channel);
    });

    setAiBusy(true);
    let ownershipToken: string | undefined;
    try {
      ownershipToken = (await aiApi.takeoverEnter(tabId)).token;
      const result = await aiApi.takeoverRun({
        tabId,
        token: ownershipToken,
        instruction,
        allowWrite,
        channel,
      });
      ownershipToken = result.token;
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, run.generation)) {
        disposeChannel(channel);
        return;
      }
      if (current.settled || holder.settled) {
        activeRunRef.current = finishAiRunSpawn(current);
        setAiBusy(false);
        return;
      }
      activeRunRef.current = bindAiRunJob(current, result.jobId);
      stream.bindJob(run.generation, result.jobId);
      banner = {
        tabId,
        jobId: result.jobId,
        token: result.token,
        task: instruction,
        allowWrite,
        startedAt: Date.now(),
      };
      useUi.getState().setTakeover(banner);
      pushToast(
        "info",
        bindings.reclaimTakeover
          ? `接管已启动 —— 顶部横幅可随时夺回，${reclaimKeyLabel} 亦可`
          : "接管已启动 —— 顶部横幅可随时夺回",
      );
    } catch (e) {
      if (ownershipToken) {
        await aiApi.takeoverExit(tabId, ownershipToken, "接管启动失败").catch(() => undefined);
      }
      const current = activeRunRef.current;
      if (isCurrentAiRun(current, run.generation)) {
        const result = stream.pushEvent(run.generation, {
          type: "error",
          message: describeError(e),
        });
        if (result.accepted && !current.settled) pushToast("error", describeError(e));
        activeRunRef.current = settleAiRun(finishAiRunSpawn(current));
        setAiBusy(false);
        clearTakeover();
      }
      disposeChannel(channel);
    }
  };

  if (!rightOpen) return null;

  return (
    <aside
      className="flex h-full shrink-0 flex-col overflow-y-auto border-l border-neutral-800/60 bg-neutral-950"
      style={{ width: rightWidth }}
    >
      <div className="flex h-[38px] shrink-0 items-center gap-1 border-b border-neutral-800/60 px-2 pl-3 [@media(max-height:480px)]:h-8">
        <span className="text-[12.5px] font-semibold text-neutral-100">AI 助手</span>
        {takeover && (
          <span
            className="nx-badge nx-badge-red"
            title={
              bindings.reclaimTakeover
                ? `终端接管进行中 —— 按 ${reclaimKeyLabel} 随时夺回`
                : "终端接管进行中"
            }
          >
            <span className="nx-dot nx-dot-pulse" />
            接管中
          </span>
        )}
        <div className="nx-spacer" />
        <button
          className={`nx-icon-btn nx-icon-btn-sm pointer-coarse:min-h-6 pointer-coarse:min-w-6 ${searchOpen ? "is-active" : ""}`}
          title="搜索对话内容"
          aria-expanded={searchOpen}
          onClick={() => setSearchOpen((v) => !v)}
        >
          <IconSearch size={14} />
        </button>
        <button
          className={`nx-icon-btn nx-icon-btn-sm pointer-coarse:min-h-6 pointer-coarse:min-w-6 ${historyOpen ? "is-active" : ""}`}
          title="历史会话"
          onClick={toggleHistory}
        >
          <IconHistory size={14} />
        </button>
        <button className="nx-icon-btn nx-icon-btn-sm pointer-coarse:min-h-6 pointer-coarse:min-w-6" title="新建会话" onClick={newConversation}>
          <IconPlus size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm pointer-coarse:min-h-6 pointer-coarse:min-w-6"
          title={`收起 AI 侧栏${bindings.toggleAiSidebar ? ` (${aiSidebarKeyLabel})` : ""}`}
          aria-keyshortcuts={formatBindingAria(bindings.toggleAiSidebar) ?? undefined}
          onClick={() => setRightOpen(false)}
        >
          <IconChevronRight size={14} />
        </button>
      </div>

      {searchOpen && (
        <div className="flex shrink-0 items-center gap-1 border-b border-neutral-800/60 px-2 py-1">
          <input
            className="nx-input min-w-0 flex-1 py-1 text-[12px]"
            placeholder="搜索对话内容，Enter 下一个，Shift+Enter 上一个"
            aria-label="搜索对话内容"
            value={searchQuery}
            autoFocus
            onChange={(e) => setSearchQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                jumpToMatch(e.shiftKey ? -1 : 1);
              } else if (e.key === "Escape") {
                e.preventDefault();
                setSearchOpen(false);
              }
            }}
          />
          <span className="shrink-0 font-mono text-[10.5px] text-neutral-500" aria-live="polite">
            {searchQuery.trim()
              ? `${matchCursor >= 0 ? matchCursor + 1 : 0}/${searchMatches.length}`
              : "0/0"}
          </span>
          <button
            className="nx-icon-btn nx-icon-btn-sm pointer-coarse:min-h-6 pointer-coarse:min-w-6 shrink-0"
            title="上一个匹配（Shift+Enter）"
            aria-label="上一个匹配"
            disabled={searchMatches.length === 0}
            onClick={() => jumpToMatch(-1)}
          >
            <IconChevronUp size={12} />
          </button>
          <button
            className="nx-icon-btn nx-icon-btn-sm pointer-coarse:min-h-6 pointer-coarse:min-w-6 shrink-0"
            title="下一个匹配（Enter）"
            aria-label="下一个匹配"
            disabled={searchMatches.length === 0}
            onClick={() => jumpToMatch(1)}
          >
            <IconChevronDown size={12} />
          </button>
          <button
            className="nx-icon-btn nx-icon-btn-sm pointer-coarse:min-h-6 pointer-coarse:min-w-6 shrink-0"
            title="关闭搜索"
            aria-label="关闭搜索"
            onClick={() => setSearchOpen(false)}
          >
            <IconClose size={11} />
          </button>
        </div>
      )}

      {historyOpen && (
        <div className="max-h-[40%] shrink-0 overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60 p-1.5">
          {historyStatus === "loading" ? (
            <div className="nx-hint px-2 py-3 text-center text-[11.5px]">历史会话加载中…</div>
          ) : historyStatus === "error" ? (
            <div className="px-2 py-3 text-center">
              <div className="nx-hint text-red-300">历史会话加载失败 · {historyError}</div>
              <button
                className="nx-btn nx-btn-ghost nx-btn-xs mt-1.5"
                onClick={() => void loadConversations()}
              >
                <IconRefresh size={11} />
                重试
              </button>
            </div>
          ) : conversations.length === 0 ? (
            <div className="nx-hint px-2 py-3 text-center text-[11.5px]">还没有历史会话</div>
          ) : (
            conversations.map((c) => (
              <div key={c.id} className="flex items-center gap-0.5">
                <button
                  className="nx-menu-item flex-1"
                  title={c.title || "(未命名会话)"}
                  onClick={() => void openConversation(c.id)}
                >
                  <span className="nx-menu-label">{c.title || "(未命名会话)"}</span>
                  <span className="nx-menu-hint">
                    {c.updatedAt ? new Date(c.updatedAt).toLocaleDateString() : ""}
                  </span>
                </button>
                {runStatusByConv[c.id] && (
                  <span
                    className={`nx-badge shrink-0 ${runStatusByConv[c.id] === "running" ? "nx-badge-amber" : "nx-badge-purple"}`}
                    title={runStatusByConv[c.id] === "running" ? "这个会话有正在运行的 AI 任务" : "这个会话有中断待恢复的 AI 任务"}
                  >
                    {runStatusByConv[c.id] === "running" ? "运行中" : "待恢复"}
                  </span>
                )}
                <button
                  className="nx-icon-btn nx-icon-btn-sm pointer-coarse:min-h-6 pointer-coarse:min-w-6 shrink-0"
                  title={`删除「${c.title || "(未命名会话)"}」`}
                  aria-label={`删除会话「${c.title || "(未命名会话)"}」`}
                  onClick={() => void deleteConversation(c)}
                >
                  <IconTrash size={12} />
                </button>
              </div>
            ))
          )}
        </div>
      )}

      {permOpen && perm && (
        <div className="max-h-[55%] shrink-0 overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60 p-2.5">
          <div className="mb-1.5 flex items-center gap-1.5 text-[11.5px] font-medium text-neutral-200">
            <IconShield size={12} />
            AI 权限
            <span className="nx-spacer" />
            <button
              className="nx-icon-btn nx-icon-btn-sm pointer-coarse:min-h-6 pointer-coarse:min-w-6"
              title="关闭"
              onClick={() => setPermOpen(false)}
            >
              <IconClose size={11} />
            </button>
          </div>

          <div className="mb-2 flex flex-col gap-0.5">
            {MODE_OPTIONS.map((m) => (
              <button
                key={m.value}
                className={`nx-menu-item w-full ${
                  perm.mode === m.value ? "bg-blue-500/15" : ""
                }`}
                onClick={() => switchMode(m.value)}
              >
                <span className="nx-menu-label">
                  {perm.mode === m.value ? "● " : "○ "}
                  {m.label}
                </span>
                <span className="nx-menu-hint">{m.hint}</span>
              </button>
            ))}
          </div>

          <div className="mb-1 text-[11px] text-neutral-300">工作方式</div>
          <button
            className={`nx-menu-item mb-0.5 w-full ${planMode ? "bg-blue-500/15" : ""}`}
            title={planMode ? "方案阶段只读调研，批准后才执行" : "先出方案，批准后执行"}
            onClick={() => setPlanMode((v) => !v)}
          >
            <span className={`shrink-0 ${planMode ? "text-blue-300" : "text-neutral-500"}`}>
              <IconList size={12} />
            </span>
            <span className="nx-menu-label">
              {planMode ? "● " : "○ "}
              计划模式
            </span>
            <span className="nx-menu-hint">{planMode ? "已开启" : "关"}</span>
          </button>
          <div className="mb-2 px-2.5 text-[10.5px] leading-relaxed text-neutral-500">
            {planMode ? "先出方案，批准后执行；期间只读" : "直接执行"}
          </div>

          <div className="mb-2 flex items-center gap-1.5">
            <span className="shrink-0 text-neutral-500">
              <IconShield size={12} />
            </span>
            <span className="text-[11.5px] text-neutral-300">拦截规则</span>
            <span className="nx-count">
              {perm.dangerRules.length > 0 ? `${perm.dangerRules.length} 条` : "未设置"}
            </span>
            <span className="nx-spacer" />
            <button
              className="nx-btn nx-btn-outline nx-btn-xs"
              title="在设置中管理拦截规则"
              onClick={() =>
                useUi
                  .getState()
                  .addTab({ id: "settings", kind: "settings", title: "设置", closable: true })
              }
            >
              去设置
            </button>
          </div>

          <div className="mt-2 border-t border-neutral-800/60 pt-1.5 text-[10px] leading-relaxed text-neutral-500">
            格式化磁盘、清空系统目录、删库等不可逆操作一律直接拒绝，不弹确认。
          </div>
        </div>
      )}

      <div className="relative min-h-0 flex-1 overflow-hidden">
        <div
          ref={setScrollEl}
          role="log"
          aria-label="AI 对话记录"
          className="h-full space-y-2.5 overflow-y-auto p-3"
        >
          {conv.items.length === 0 && (
            <div className="flex flex-col items-center gap-3 px-2 pt-10 text-center">
              <span className="nx-empty-icon">
                <IconBot size={19} />
              </span>
              <div className="text-xs text-neutral-400">新建会话 · 命令与输出全程留痕</div>
            </div>
          )}
          {virtual.range.padTop > 0 && (
            <div style={{ height: virtual.range.padTop }} aria-hidden="true" />
          )}
          {conv.items.slice(virtual.range.start, virtual.range.end).map((item, i) => {
            const index = virtual.range.start + i;
            return (
              <div
                key={item.id}
                data-conversation-index={index}
                className={
                  index === activeMatchIndex ? "rounded-xl ring-2 ring-amber-400/70" : undefined
                }
              >
                <ChatBubble
                  item={item}
                  streaming={aiBusy && index === conv.items.length - 1}
                  onApprovePlan={handleApprovePlan}
                  onRetry={handleRetryRun}
                  onEdit={handleEditResend}
                />
              </div>
            );
          })}
          {virtual.range.padBottom > 0 && (
            <div style={{ height: virtual.range.padBottom }} aria-hidden="true" />
          )}
          {confirmCard && (
            <div className="nx-alert">
              <div className="mb-1.5 flex items-center gap-1.5 font-semibold">
                <IconAlert size={13} />
                需要你确认
                <span className="nx-spacer" />
                <span className="nx-badge nx-badge-amber">{confirmCard.tool}</span>
              </div>
              <ConfirmBody card={confirmCard} />
              <div className="mb-2 text-[10.5px] leading-relaxed text-neutral-500">
                不想每次都弹这个？把它加为拦截规则，或在权限设置里调整档位。
              </div>
              <div className="flex flex-wrap gap-1.5">
                <button
                  className="nx-btn nx-btn-primary nx-btn-xs pointer-coarse:min-h-6"
                  disabled={submittingCardId === confirmCard.id}
                  onClick={() => void confirm("allow")}
                >
                  允许一次
                </button>
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs pointer-coarse:min-h-6"
                  disabled={submittingCardId === confirmCard.id}
                  onClick={() => void confirm("allow_session")}
                >
                  本会话允许此类
                </button>
                <button
                  className="nx-btn nx-btn-ghost nx-btn-xs pointer-coarse:min-h-6"
                  disabled={submittingCardId === confirmCard.id}
                  onClick={() => void confirm("deny")}
                >
                  拒绝
                </button>
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs pointer-coarse:min-h-6"
                  title="打开设置的拦截规则，并把这条命令预填成一条新规则"
                  onClick={() => {
                    const ui = useUi.getState();
                    ui.setAiRulePrefill(ruleFromRendered(confirmCard.rendered));
                    ui.addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
                  }}
                >
                  加为拦截规则
                </button>
              </div>
            </div>
          )}

          {questionCard && (
            <form
              className="nx-alert"
              onSubmit={(event) => {
                event.preventDefault();
                void answer();
              }}
            >
              <div className="mb-1.5 flex items-center gap-1.5 font-semibold">
                <IconAlert size={13} />
                AI 需要你回答
              </div>
              <div className="mb-2 whitespace-pre-wrap text-[12px] leading-relaxed text-neutral-200">
                {questionCard.question}
              </div>
              {questionCard.options.length > 0 && (
                <div className="mb-2 flex flex-wrap gap-1.5">
                  {questionCard.options.map((option) => (
                    <button
                      key={option}
                      type="button"
                      className="nx-btn nx-btn-outline nx-btn-xs pointer-coarse:min-h-6"
                      disabled={submittingCardId === questionCard.id}
                      onClick={() => void answer(option)}
                    >
                      {option}
                    </button>
                  ))}
                </div>
              )}
              <div className="flex items-end gap-1.5">
                <textarea
                  className="nx-textarea pointer-coarse:text-[16px] min-h-[36px] flex-1"
                  rows={2}
                  value={questionInput}
                  placeholder="输入回答…"
                  aria-label="回答 AI 的问题"
                  onChange={(event) => setQuestionInput(event.target.value)}
                />
                <button
                  type="submit"
                  className="nx-btn nx-btn-primary nx-btn-xs pointer-coarse:min-h-6 shrink-0"
                  disabled={!questionInput.trim() || submittingCardId === questionCard.id}
                >
                  回答
                </button>
              </div>
            </form>
          )}

          {conv.todos.length > 0 && (
            <div className="rounded-lg border border-neutral-800/60 bg-neutral-900/50 px-2.5 py-2">
              <div className="mb-1 flex items-center gap-1.5 text-[10.5px] text-neutral-400">
                <IconList size={11} />
                任务清单
                <span className="text-[var(--nx-fg-tertiary)]">
                  {conv.todos.filter((t) => t.status === "completed").length}/{conv.todos.length}
                </span>
              </div>
              <div className="flex max-h-28 flex-col gap-0.5 overflow-y-auto">
                {conv.todos.map((t, i) => (
                  <div key={i} className="flex items-start gap-1.5 text-[11px] leading-snug">
                    <span
                      className={
                        t.status === "completed"
                          ? "text-green-300"
                          : t.status === "in_progress"
                            ? "text-blue-300"
                            : "text-[var(--nx-fg-tertiary)]"
                      }
                    >
                      {t.status === "completed" ? "✓" : t.status === "in_progress" ? "▸" : "○"}
                    </span>
                    <span
                      className={
                        t.status === "completed"
                          ? "text-neutral-500 line-through"
                          : "text-neutral-300"
                      }
                    >
                      {t.content}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
        {follow.newOutput && (
          <button
            type="button"
            className="absolute bottom-2 left-1/2 z-10 -translate-x-1/2 rounded-full border border-neutral-700 bg-neutral-800/95 px-3 py-1 text-[11px] text-neutral-200 shadow-lg hover:bg-neutral-700"
            aria-label="回到最新输出"
            onClick={follow.jumpToLatest}
          >
            ↓ 新输出
          </button>
        )}
      </div>

      <div className="shrink-0 border-t border-neutral-800/60 p-2.5 [padding-bottom:calc(0.625rem+var(--nx-kb-inset,0px))]">
        {planMode && (
          <div className="mb-1.5 flex items-center gap-1.5 text-[11px] text-blue-300/90">
            <IconList size={10} className="shrink-0" />
            <span className="truncate">计划模式 · 先出方案，你批准了再动手</span>
          </div>
        )}
        {editing && (
          <div className="mb-1.5 flex items-center gap-1.5 rounded-lg border border-blue-500/30 bg-blue-500/10 px-2 py-1 text-[11px] text-blue-200">
            <IconEdit size={10} className="shrink-0" />
            <span className="min-w-0 flex-1 truncate">
              {editSubmitting ? "正在替换并重新发送…" : "编辑重发：此后的消息与运行会被替换"}
            </span>
            <button
              className="nx-btn nx-btn-primary nx-btn-xs shrink-0"
              disabled={!input.trim() || editSubmitting}
              onClick={() => void submitEditResend()}
            >
              替换并重新发送
            </button>
            <button
              className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
              disabled={editSubmitting}
              onClick={() => setEditing(null)}
            >
              取消
            </button>
          </div>
        )}
        {syncNotice && syncNotice.generation === activeGeneration && (
          <div
            className="mb-1.5 flex items-center gap-1.5 text-[11px] text-neutral-500 [@media(max-height:480px)]:sr-only"
            role="status"
            aria-live="polite"
          >
            {syncNotice.phase === "syncing" ? (
              <>
                <IconLoader size={10} className="animate-spin text-amber-300/80" />
                <span className="truncate">连接已恢复，正在补齐输出…</span>
              </>
            ) : syncNotice.phase === "failed" ? (
              <>
                <IconAlert size={10} className="shrink-0 text-red-300" />
                <span className="truncate">补齐失败，输出可能不完整</span>
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs shrink-0"
                  onClick={() => {
                    const current = activeRunRef.current;
                    if (current?.jobId) resyncRun(current.generation, current.jobId);
                  }}
                >
                  重新补齐
                </button>
              </>
            ) : (
              <span className="truncate text-[var(--nx-fg-tertiary)]">
                {syncNotice.count > 0 ? `已补齐断线期间的 ${syncNotice.count} 条事件` : "连接已恢复，输出无缺失"}
              </span>
            )}
          </div>
        )}
        {aiBusy && hitlWaiting && (
          <div
            className="mb-1.5 flex items-center gap-1.5 text-[11px] text-amber-200/90 [@media(max-height:480px)]:sr-only"
            role="status"
            aria-live="polite"
          >
            <IconAlert size={10} className="shrink-0 text-amber-300/90" />
            <span className="truncate">{hitlWaiting}</span>
          </div>
        )}
        {aiBusy && !hitlWaiting && conv.status && (
          <div
            className="mb-1.5 flex items-center gap-1.5 text-[11px] text-neutral-500 [@media(max-height:480px)]:sr-only"
            role="status"
            aria-live="polite"
          >
            <IconLoader size={10} className="animate-spin text-amber-300/80" />
            <span className="truncate">{statusText(conv.status)}</span>
          </div>
        )}
        {refs.length > 0 && (
          <div className="mb-1.5 flex flex-wrap gap-1">
            {refs.map((r) => (
              <span key={r.id} className="nx-chip nx-chip-accent" title={r.detail}>
                <span className="truncate">{r.label}</span>
                <button
                  className="nx-chip-x pointer-coarse:min-h-6 pointer-coarse:min-w-6"
                  title="移除引用"
                  onClick={() => setRefs((prev) => prev.filter((x) => x.id !== r.id))}
                >
                  <IconClose size={10} />
                </button>
              </span>
            ))}
          </div>
        )}

        {images.length > 0 && (
          <div className="mb-1.5 flex flex-wrap gap-1.5">
            {images.map((src, i) => (
              <span key={i} className="nx-attach" title="粘贴的图片">
                <img src={src} alt="" />
                <button
                  className="nx-attach-x pointer-coarse:min-h-6 pointer-coarse:min-w-6"
                  title="移除图片"
                  onClick={() => setImages((prev) => prev.filter((_, j) => j !== i))}
                >
                  <IconClose size={10} />
                </button>
              </span>
            ))}
          </div>
        )}

        {atOpen && refCandidates.length > 0 && (
          <div className="mb-1.5 max-h-40 overflow-y-auto rounded-lg border border-neutral-800 bg-neutral-900 p-1">
            {refCandidates.map((r) => (
              <button
                key={r.id}
                className="nx-menu-item w-full"
                onClick={() => pickRef(r)}
              >
                <span className="nx-menu-label">{r.label}</span>
                <span className="nx-menu-hint">{r.detail}</span>
              </button>
            ))}
          </div>
        )}

        <textarea
          ref={inputRef}
          className="nx-textarea pointer-coarse:text-[16px] max-h-40 min-h-[64px] w-full [@media(max-height:480px)]:h-9 [@media(max-height:480px)]:min-h-0"
          placeholder={
            aiBusy
              ? "运行中：Enter 发送补充指令，将在当前步骤完成后注入"
              : sessionId
                ? "向 NexTerm 提问，@ 引用资产或终端标签，可直接粘贴图片"
                : "未连接会话（仍可全局提问）"
          }
          aria-label="消息输入"
          value={input}
          rows={3}
          onChange={(e) => onInputChange(e.target.value)}
          onPaste={onPaste}
          onKeyDown={(e) => {
            if (e.key === "Escape" && atOpen) {
              e.preventDefault();
              setAtOpen(false);
              return;
            }
            if (e.key === "Enter" && !e.shiftKey) {
              if (isImeKeyEvent(e)) return;
              e.preventDefault();
              if (aiBusy) void steer();
              else if (editing) void submitEditResend();
              else void send();
            }
          }}
        />

        <div className="mt-1.5 flex flex-wrap items-center gap-1">
          <ModelSelector onManage={() => setModelPanelOpen(true)} />
          <div className="nx-spacer" />
          <UsageRing
            usage={conv.usage}
            runs={conversationRuns}
            loadSummary={() => modelApi.usageSummary()}
          />
          <button
            className={`nx-icon-btn nx-icon-btn-sm pointer-coarse:min-h-6 pointer-coarse:min-w-6 ${
              perm?.mode === "silent" ? "is-active" : ""
            } ${permOpen ? "bg-neutral-800 text-neutral-100" : ""}`}
            title={`AI 权限：${MODE_LABEL[perm?.mode ?? "read_write"]}（点击设置）`}
            onClick={() => setPermOpen((v) => !v)}
          >
            <IconShield size={13} />
          </button>
          <button
            className="nx-icon-btn nx-icon-btn-sm pointer-coarse:min-h-6 pointer-coarse:min-w-6 text-red-400 hover:text-red-300"
            title={
              tabId
                ? bindings.reclaimTakeover
                  ? `终端接管（实验性功能）：AI 直接接手当前终端，随时按 ${reclaimKeyLabel} 夺回`
                  : "终端接管（实验性功能）：AI 直接接手当前终端"
                : "终端接管需要先打开一个终端标签"
            }
            disabled={!tabId || aiBusy}
            onClick={() => void runTakeover()}
          >
            <IconMonitor size={13} />
          </button>
          {aiBusy ? (
            <button
              className="nx-send-btn nx-send-btn-stop"
              title="停止这一轮（AI 会停在当前位置，已跑完的结果保留）"
              onClick={() => void stop()}
            >
              <IconStopSquare />
            </button>
          ) : (
            <button
              className="nx-send-btn"
              title={editing ? "替换并重新发送" : "发送 (Enter)"}
              disabled={(!input.trim() && images.length === 0) || editSubmitting}
              onClick={() => (editing ? void submitEditResend() : void send())}
            >
              <IconSendArrow />
            </button>
          )}
        </div>
      </div>

      {modelPanelOpen && <ModelPanel onClose={() => setModelPanelOpen(false)} />}
    </aside>
  );
}

function IconSendArrow() {
  return (
    <svg width="15" height="15" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path
        d="M8 12.5V3.5M8 3.5L4 7.5M8 3.5l4 4"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function IconStopSquare() {
  return (
    <svg width="15" height="15" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <rect x="4.5" y="4.5" width="7" height="7" rx="1.5" fill="currentColor" />
    </svg>
  );
}

function readAsDataUrl(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const fr = new FileReader();
    fr.onload = () => resolve(String(fr.result));
    fr.onerror = () => reject(new Error("读取图片失败"));
    fr.readAsDataURL(file);
  });
}

function statusText(s: StatusLine): string {
  if (s.phase === "compacting") return s.detail ?? "上下文接近上限，正在压缩早期工具结果…";
  if (s.phase === "thinking") return s.turn ? `第 ${s.turn} 轮 · 正在思考…` : "正在思考…";
  if (s.phase === "tool_args") {
    return `${preparingLabel(s.tool)} · 已生成 ${formatBytes(s.chars ?? 0)}`;
  }
  return s.detail ?? s.phase;
}

function preparingLabel(tool?: string): string {
  switch (tool) {
    case "write_file":
      return "正在准备写入内容";
    case "edit_file":
      return "正在准备修改内容";
    case "exec_commands":
      return "正在准备命令";
    case "send_keys":
      return "正在准备按键";
    default:
      return "正在准备工具参数";
  }
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} 字节`;
  return `${(n / 1024).toFixed(1)} KB`;
}

function ruleFromRendered(rendered: string): string {
  const lines = rendered
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);
  const picked = lines.find((l) => l.startsWith("$")) ?? lines[0] ?? "";
  return picked.slice(0, 120);
}

const ChatBubble = memo(function ChatBubble({
  item,
  streaming,
  onApprovePlan,
  onRetry,
  onEdit,
}: {
  item: ChatItem;
  streaming: boolean;
  onApprovePlan: (plan: string) => void;
  onRetry: (item: ChatItem) => void;
  onEdit: (item: ChatItem) => void;
}) {
  if (item.role === "user") {
    return (
      <div className="group/user relative ml-10 rounded-[10px] rounded-br-[3px] border border-blue-500/25 bg-blue-500/20 px-3 py-2 text-[12.3px] leading-relaxed text-[var(--nx-fg-on-tint)]">
        {item.messageId ? (
          <button
            type="button"
            className="absolute right-1.5 top-1.5 rounded p-0.5 text-[var(--nx-fg-soft)] opacity-0 transition-opacity hover:text-neutral-100 focus:opacity-100 group-hover/user:opacity-100"
            title="编辑并重新发送"
            aria-label="编辑并重新发送"
            onClick={() => onEdit(item)}
          >
            <IconEdit size={11} />
          </button>
        ) : null}
        {item.imageCount ? (
          <div className="mb-1 flex items-center gap-1 text-[11px] text-[var(--nx-fg-soft)]">
            <IconImage size={11} />
            {item.imageCount} 张图片
          </div>
        ) : null}
        <pre className="font-sans break-words whitespace-pre-wrap">{item.text}</pre>
        {item.steer ? (
          <div className="mt-1 text-[10.5px] text-[var(--nx-fg-soft)]">
            {item.steer === "pending"
              ? "补充指令 · 等待注入…"
              : item.steer === "delivered"
                ? "补充指令 · 已注入当前运行"
                : "补充指令 · 未送达（模型未看到）"}
          </div>
        ) : null}
      </div>
    );
  }
  if (item.role === "assistant") {
    return (
      <div className="mr-2 rounded-[10px] rounded-bl-[3px] border border-neutral-800 bg-neutral-800 px-3 py-2 text-[12.3px] leading-relaxed text-neutral-200">
        <Markdown text={item.text} streaming={streaming} />
      </div>
    );
  }
  if (item.role === "reasoning") {
    return (
      <details
        className="ml-1 border-l-2 border-neutral-700 pl-2.5 text-[11.5px] leading-relaxed text-neutral-500"
        open={streaming}
      >
        <summary className="cursor-pointer select-none text-[10.5px] text-[var(--nx-fg-tertiary)] hover:text-neutral-400">
          思考过程（{item.text.length} 字）{streaming ? " · 进行中" : ""}
        </summary>
        <div className="mt-1 whitespace-pre-wrap italic">{item.text}</div>
      </details>
    );
  }
  if (item.role === "confirm" || item.role === "question") {
    return <InteractionRecord item={item} />;
  }
  if (item.role === "outcome") {
    if (item.outcome === "error") {
      return (
        <div
          role="alert"
          className="flex items-start gap-1.5 rounded-lg border border-red-500/30 bg-red-500/[0.08] px-2.5 py-2 text-[11.5px] leading-relaxed text-red-200"
        >
          <IconAlert size={12} className="mt-0.5 shrink-0 text-red-300" />
          {item.maxIterations ? (
            <span className="nx-badge nx-badge-amber shrink-0">迭代上限</span>
          ) : null}
          <span className="whitespace-pre-wrap">本轮出错：{item.text}</span>
          {item.retryable ? (
            <button
              className="nx-btn nx-btn-outline nx-btn-xs ml-auto shrink-0"
              title="在同一会话里按原消息重新发起一轮"
              onClick={() => onRetry(item)}
            >
              <IconRefresh size={11} />
              重试
            </button>
          ) : null}
        </div>
      );
    }
    return (
      <div
        role="status"
        className={`flex items-center justify-center gap-1.5 py-0.5 text-[10.5px] ${
          item.outcome === "canceled" ? "text-[var(--nx-fg-warning)]" : "text-[var(--nx-fg-tertiary)]"
        }`}
      >
        {item.text}
      </div>
    );
  }
  if (item.role === "diff") return <DiffBubble item={item} />;
  if (item.role === "plan") {
    return <PlanBubble item={item} onApprove={() => onApprovePlan(item.text)} />;
  }
  if (item.role === "tool") return <ToolBubble item={item} />;
  return null;
});

function InteractionRecord({ item }: { item: ConfirmItem | QuestionItem }) {
  const pending = item.resolution === undefined;
  const text =
    item.role === "confirm"
      ? pending
        ? `等待确认：${item.tool}`
        : `${item.tool} · ${item.resolution}`
      : pending
        ? `等待回答：${item.question}`
        : `提问 · ${item.resolution}`;
  return (
    <div
      className={`flex items-center gap-1.5 text-[10.5px] ${
        pending ? "text-[var(--nx-fg-warning)]" : "text-[var(--nx-fg-tertiary)]"
      }`}
      title={text}
    >
      {item.role === "confirm" ? <IconShield size={10} className="shrink-0" /> : <IconAlert size={10} className="shrink-0" />}
      <span className="truncate">{text}</span>
    </div>
  );
}

function ToolBubble({ item }: { item: Extract<ChatItem, { role: "tool" }> }) {
  const [open, setOpen] = useState(false);
  const running = item.summary === undefined;
  const full = item.text ?? "";
  const preview = item.summary ?? "";
  const expandable = full.length > preview.length;
  const body = open ? full : preview;
  return (
    <div
      className="rounded-lg border border-neutral-800 bg-neutral-900/70 px-2.5 py-2 text-[11.5px]"
      aria-busy={running}
    >
      <div className="flex items-center gap-1.5">
        <span className="nx-badge nx-badge-purple font-mono">{item.name}</span>
        <span className="min-w-0 flex-1 truncate font-mono text-neutral-400" title={item.display}>
          {item.display}
        </span>
        <span className="sr-only">{running ? "工具执行中" : item.panic ? "工具执行崩溃" : item.ok ? "工具执行成功" : "工具执行失败"}</span>
        {item.panic && !running ? <span className="nx-badge nx-badge-red">崩溃</span> : null}
        {running ? (
          <IconLoader size={11} className="animate-spin text-amber-300" />
        ) : item.ok ? (
          <IconCheck size={12} className="text-green-300" />
        ) : (
          <IconXCircle size={12} className="text-red-300" />
        )}
        {!running && item.exitCode !== null && item.exitCode !== undefined && (
          <span className="font-mono text-[10px] text-neutral-500">{item.exitCode}</span>
        )}
      </div>
      {item.subagent ? <SubagentTimelineView timeline={item.subagent} /> : null}
      {body && (
        <pre
          className={`mt-1.5 overflow-auto whitespace-pre-wrap font-mono text-[10.5px] ${
            open ? "max-h-[26rem] text-neutral-300" : "max-h-32 text-neutral-500"
          }`}
        >
          {body}
        </pre>
      )}
      {expandable && (
        <button
          className="mt-1.5 flex items-center gap-1 text-[10.5px] text-neutral-500 hover:text-neutral-300"
          aria-expanded={open}
          onClick={() => setOpen((v) => !v)}
        >
          <IconChevronRight size={10} className={open ? "rotate-90" : undefined} />
          {open ? "收起" : `展开完整输出（${full.length} 字符）`}
        </button>
      )}
    </div>
  );
}

function SubagentTimelineView({ timeline }: { timeline: SubagentTimeline }) {
  const [open, setOpen] = useState(timeline.status === "running");
  const running = timeline.status === "running";
  const label = running
    ? "子代理 · 运行中"
    : timeline.status === "completed"
      ? "子代理 · 已完成"
      : timeline.status === "canceled"
        ? "子代理 · 已取消"
        : "子代理 · 失败";
  return (
    <div className="mt-1.5 border-t border-neutral-800 pt-1.5">
      <button
        className="flex w-full items-center gap-1.5 text-[10.5px] text-neutral-400 hover:text-neutral-200"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        {running ? (
          <IconLoader size={10} className="shrink-0 animate-spin text-amber-300" />
        ) : timeline.status === "completed" ? (
          <IconCheck size={11} className="shrink-0 text-green-300" />
        ) : (
          <IconXCircle size={11} className="shrink-0 text-red-300" />
        )}
        <IconBot size={11} className="shrink-0 text-neutral-500" />
        <span className="min-w-0 flex-1 truncate text-left">{label}</span>
        <span className="shrink-0 text-neutral-600">
          {timeline.text.length} 字 · {timeline.tools.length} 个工具
        </span>
        <IconChevronRight size={10} className={open ? "rotate-90" : undefined} />
      </button>
      {open ? (
        <div className="mt-1 space-y-1">
          {timeline.text ? (
            <pre className="max-h-24 overflow-auto whitespace-pre-wrap font-mono text-[10.5px] text-neutral-400">
              {timeline.text}
            </pre>
          ) : null}
          {timeline.tools.map((tool) => (
            <div key={tool.callId} className="flex items-center gap-1.5 text-[10.5px]">
              {tool.status === "running" ? (
                <IconLoader size={9} className="shrink-0 animate-spin text-amber-300" />
              ) : tool.status === "ok" ? (
                <IconCheck size={10} className="shrink-0 text-green-300" />
              ) : (
                <IconXCircle size={10} className="shrink-0 text-red-300" />
              )}
              <span className="shrink-0 font-mono text-neutral-400">{tool.name || "tool"}</span>
              {tool.panic && tool.status !== "running" ? <span className="nx-badge nx-badge-red">崩溃</span> : null}
              {tool.summary ? (
                <span className="min-w-0 flex-1 truncate text-neutral-600" title={tool.summary}>
                  {tool.summary}
                </span>
              ) : null}
            </div>
          ))}
          {!running ? (
            <div className="text-[10.5px] text-neutral-500">
              {timeline.status === "completed"
                ? (timeline.summary ?? "已完成")
                : (timeline.error ?? timeline.summary ?? "已结束")}
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function PlanBubble({
  item,
  onApprove,
}: {
  item: Extract<ChatItem, { role: "plan" }>;
  onApprove: () => void;
}) {
  return (
    <div className="rounded-lg border border-blue-500/30 bg-blue-500/[0.07] px-2.5 py-2 text-[11.5px]">
      <div className="mb-1.5 flex items-center gap-1.5">
        <IconList size={12} className="shrink-0 text-blue-300" />
        <span className="font-medium text-[var(--nx-fg-on-tint)]">方案已提交</span>
        <span className="text-neutral-500">还没动任何东西</span>
      </div>
      <div className="max-h-72 overflow-y-auto">
        <Markdown text={item.text} className="text-neutral-300" />
      </div>
      <button className="nx-btn nx-btn-outline nx-btn-xs mt-2" onClick={onApprove}>
        <IconPlay size={11} />
        批准，按这个方案执行
      </button>
    </div>
  );
}

function ConfirmBody({ card }: { card: ConfirmItem }) {
  const pv = card.preview;
  if (!pv) {
    return (
      <pre className="mb-1.5 max-h-28 overflow-auto whitespace-pre-wrap font-mono text-[11px] text-neutral-200">
        {card.rendered}
      </pre>
    );
  }
  return (
    <>
      <div className="mb-1 flex items-baseline gap-1.5 font-mono text-[11px]">
        <span className="shrink-0 text-neutral-500">
          {pv.kind === "create" ? "将新建" : "将修改"}
        </span>
        <span className="min-w-0 flex-1 truncate text-neutral-200" title={pv.path}>
          {pv.path}
        </span>
      </div>
      <div className="mb-1.5 max-h-52 overflow-auto whitespace-pre-wrap rounded border border-neutral-800 bg-neutral-950/70 px-2 py-1.5 font-mono text-[10.5px] leading-relaxed">
        <DiffLines before={pv.before} after={pv.after} />
      </div>
      {card.reason ? (
        <div className="mb-1.5 text-[10.5px] leading-relaxed text-[var(--nx-fg-warning)]">
          {card.reason}
        </div>
      ) : null}
      <details className="mb-2">
        <summary className="cursor-pointer text-[10.5px] text-[var(--nx-fg-tertiary)] hover:text-neutral-400">
          查看原始参数
        </summary>
        <pre className="mt-1 max-h-28 overflow-auto whitespace-pre-wrap font-mono text-[10.5px] text-neutral-500">
          {card.rendered}
        </pre>
      </details>
    </>
  );
}

function DiffBubble({ item }: { item: Extract<ChatItem, { role: "diff" }> }) {
  return (
    <div className="rounded-lg border border-neutral-700 bg-neutral-900/70 px-2.5 py-2 text-[11.5px]">
      <div className="mb-1.5 flex items-center gap-1.5">
        <IconEdit size={12} className="shrink-0 text-neutral-400" />
        <span className="shrink-0 font-medium text-neutral-200">文件已修改</span>
        <span className="min-w-0 flex-1 truncate font-mono text-neutral-400" title={item.path}>
          {item.path}
        </span>
      </div>
      <pre className="max-h-44 overflow-auto whitespace-pre-wrap font-mono text-[10.5px] leading-relaxed">
        <DiffLines before={item.before} after={item.after} />
      </pre>
    </div>
  );
}

function DiffLines({ before, after }: { before: string; after: string }) {
  const lines = simpleDiff(before, after);
  if (!hasVisibleChange(lines)) {
    return (
      <div className="text-neutral-500">
        {before === after ? "（内容没有变化）" : "（差异在文件末尾的换行）"}
      </div>
    );
  }
  return (
    <>
      {lines.map((l, i) => (
        <div
          key={i}
          className={
            l.kind === "add" ? "nx-diff-add" : l.kind === "del" ? "nx-diff-del" : "text-neutral-500"
          }
        >
          {diffLineText(l)}
        </div>
      ))}
    </>
  );
}
