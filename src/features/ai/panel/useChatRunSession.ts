import { useState, useRef, useMemo, useCallback, useSyncExternalStore, useEffect } from "react";
import { useUi, useActiveWorkspace, useWorkspaceList } from "../../../app/store";
import { useStableValue } from "../../../ui/useStableValue";
import { useAssetProductionClassification } from "../../production/productionSettings";
import { setAiTabBusy, type AiRunSlot, createAiRun, registerAiRunGeneration, aiTabRunControls, isCurrentAiRun } from "../runOwnership";
import { useKeybindings, formatBinding } from "../../../app/keybindings";
import { type ConversationStream, createConversationStream } from "../conversationStream";
import { ToolHandleRegistry } from "../agentToolData";
import { type IpcChannel, disposeChannel } from "../../../ipc/events";
import { pendingInteraction } from "../conversation";
import { type RefChip, aiScopeForWorkspace, currentAiBoardKey, AI_SYNC_NOTICE_DISMISS_MS, type AiChatBase } from "./aiSidebarShared";


export function useChatRunSession(p: AiChatBase) {
  const { sessionId, tabId, active, boardKey, tabKey, draft, onDraftChange, boundConversationId, onConversationChange } = p;

  const [images, setImages] = useState<string[]>([]);
  const [refs, setRefs] = useState<RefChip[]>([]);

  const cancelOperationRef = useRef<{
    generation: number;
    jobId: string | null;
    cancelSent: boolean;
  } | null>(null);
  const cancelWaitersRef = useRef(
    new Set<{
      generation: number;
      closeTab: boolean;
      resolve: (confirmed: boolean) => void;
      timer: ReturnType<typeof setTimeout>;
    }>(),
  );

  const requestSnapshotsRef = useRef(
    new Map<number, { message: string; images: string[]; refs: RefChip[]; planMode: boolean }>(),
  );
  const dockOpen = useUi((s) => s.rightOpen);
  const rightOpen = p.visible ?? dockOpen;
  const rightOpenRef = useRef(rightOpen);
  rightOpenRef.current = rightOpen;
  const anyAiBusy = useUi((s) => s.aiBusy);
  const takeoverState = useUi((s) => s.takeover);
  const pushToast = useUi((s) => s.pushToast);
  const workspaces = useWorkspaceList();
  const activeWorkspace = useActiveWorkspace();
  // Only the current session's asset can widen the scope, so the hook follows that one value.
  const scopeSessionId = activeWorkspace?.kind === "session" && !activeWorkspace.assetId
    ? (sessionId ?? activeWorkspace.sessionId)
    : undefined;
  const scopeSessionAssetId = useUi((s) =>
    scopeSessionId ? s.sessions.find((item) => item.id === scopeSessionId)?.assetId : undefined);
  const aiScopeCandidate = useMemo(() => {
    let workspace = activeWorkspace;
    if (workspace?.kind === "session" && !workspace.assetId && scopeSessionAssetId) {
      workspace = { ...workspace, assetId: scopeSessionAssetId };
    }
    return aiScopeForWorkspace(workspace, sessionId, tabId);
  }, [activeWorkspace, sessionId, tabId, scopeSessionAssetId]);
  // The candidate is rebuilt whenever the active workspace changes identity; keep the previous object while its
  // content is equal so the permission lookup below does not re-run (two IPC calls and a re-render) each time.
  const aiScope = useStableValue(aiScopeCandidate);
  const hasAiScope = Boolean(sessionId || (activeWorkspace?.kind === "db" && activeWorkspace.connId));
  const permScope = hasAiScope ? aiScope : undefined;
  const aiScopeAssetId = typeof aiScope.assetId === "string" ? aiScope.assetId : undefined;
  const aiScopeProduction = useAssetProductionClassification(aiScopeAssetId);
  const activeRef = useRef(active);
  activeRef.current = active;
  const mountedRef = useRef(false);
  const restoreGenerationRef = useRef(0);
  const isLifecycleActive = useCallback(
    () => mountedRef.current && currentAiBoardKey() === boardKey,
    [boardKey],
  );
  const [busy, setBusyState] = useState(false);
  const busyRef = useRef(false);
  const setBusy = useCallback((v: boolean) => {
    const run = activeRunRef.current;
    const cancellationPending = run !== null && run.cancellation !== "idle" && !run.settled;
    const next = !v && cancellationPending ? true : v;
    busyRef.current = next;
    if (mountedRef.current) setBusyState(next);
    setAiTabBusy(tabKey, next);
  }, [tabKey]);
  const bindings = useKeybindings();
  const reclaimKeyLabel = formatBinding(bindings.reclaimTakeover);
  const streamRef = useRef<ConversationStream | null>(null);
  if (!streamRef.current) streamRef.current = createConversationStream();
  const stream = streamRef.current;
  const subscribe = useCallback((onChange: () => void) => stream.subscribe(() => onChange()), [stream]);
  const getSnapshot = useCallback(() => stream.getState(), [stream]);
  const conv = useSyncExternalStore(subscribe, getSnapshot);

  // Tool-card handle tracking: the verified roster handle of each tool call
  // is captured from the actual toolCall/toolResult event stream (live,
  // replayed, and cross-restart resumed alike) instead of ever being
  // re-derived. The registry fails closed on conflicting evidence, and the
  // roster snapshot joins host/cwd/presence through real ai_roster_list data.
  const toolHandlesRef = useRef<ToolHandleRegistry | null>(null);
  if (toolHandlesRef.current === null) toolHandlesRef.current = new ToolHandleRegistry();
  const [toolHandleVersion, setToolHandleVersion] = useState(0);
  const lastRefreshedHandleVersionRef = useRef(0);
  const pushAgentEvent = useCallback(
    (generation: number, ev: Record<string, unknown>): ReturnType<typeof stream.pushEvent> => {
      if (toolHandlesRef.current?.record(generation, ev)) setToolHandleVersion((version) => version + 1);
      return stream.pushEvent(generation, ev);
    },
    [stream],
  );

  const [input, setInput] = useState(draft ?? "");
  const onDraftChangeRef = useRef(onDraftChange);
  onDraftChangeRef.current = onDraftChange;
  useEffect(() => {
    onDraftChangeRef.current(input);
  }, [input]);
  const [questionInput, setQuestionInput] = useState("");
  const [submittingCardId, setSubmittingCardId] = useState<string | null>(null);
  const submittingCardIdRef = useRef(submittingCardId);
  submittingCardIdRef.current = submittingCardId;
  const [syncNotice, setSyncNotice] = useState<{
    generation: number;
    phase: "syncing" | "synced" | "failed";
    count: number;
  } | null>(null);
  const runSequenceRef = useRef(0);
  const activeRunRef = useRef<AiRunSlot | null>(null);
  const [activeRun, setActiveRunState] = useState<AiRunSlot | null>(null);
  const activeGeneration = activeRun?.generation ?? null;
  const setActiveRun = useCallback((run: AiRunSlot) => {
    activeRunRef.current = run;
    if (mountedRef.current) setActiveRunState(run);
  }, []);
  const stopRef = useRef<(closeTab: boolean) => Promise<boolean>>(() => Promise.resolve(false));
  const runControl = useCallback(() => stopRef.current(true), []);
  const catchupChains = useRef(new Map<number, Promise<{ applied: number; failed: boolean }>>());
  const terminalPollRef = useRef<(generation: number, jobId: string) => Promise<void>>(async () => undefined);
  const terminalPollFailuresRef = useRef(0);
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
    restoreGenerationRef.current += 1;
    const run = createAiRun(++runSequenceRef.current, kind);
    registerAiRunGeneration(tabKey, run.generation);
    aiTabRunControls.set(tabKey, runControl);
    setActiveRun(run);
    stream.beginRun(run.generation, kind);
    return run;
  };
  const disposeRestoredChannel = useCallback(() => {
    const restored = restoredChannelRef.current;
    if (!restored) return;
    restoredChannelRef.current = null;
    if (restored.expiryTimer !== null) clearTimeout(restored.expiryTimer);
    restored.offReopen();
    disposeChannel(restored.channel);
  }, []);
  useEffect(() => {
    mountedRef.current = true;
    const catchupChainsMap = catchupChains.current;
    const requestSnapshots = requestSnapshotsRef.current;
    const cancelWaiters = cancelWaitersRef.current;
    return () => {
      mountedRef.current = false;
      restoreGenerationRef.current += 1;
      runSequenceRef.current += 1;
      // eslint-disable-next-line react-hooks/exhaustive-deps
      runChannelRef.current?.dispose();
      disposeRestoredChannel();
      if (syncTimerRef.current !== null) {
        clearTimeout(syncTimerRef.current);
        syncTimerRef.current = null;
      }
      if (resyncTimerRef.current !== null) {
        clearTimeout(resyncTimerRef.current.timer);
        resyncTimerRef.current = null;
      }
      catchupChainsMap.clear();
      requestSnapshots.clear();
      for (const waiter of cancelWaiters) {
        clearTimeout(waiter.timer);
        waiter.resolve(false);
      }
      cancelWaiters.clear();
      cancelOperationRef.current = null;
      queueMicrotask(() => {
        if (!mountedRef.current) stream.dispose();
      });
    };
  }, [stream, disposeRestoredChannel]);
  const clearResyncTimer = useCallback((generation?: number) => {
    const pending = resyncTimerRef.current;
    if (!pending) return;
    if (generation !== undefined && pending.generation !== generation) return;
    clearTimeout(pending.timer);
    resyncTimerRef.current = null;
  }, []);
  const clearResyncState = useCallback((generation: number) => {
    clearResyncTimer(generation);
    setSyncNotice((prev) => (prev && prev.generation === generation ? null : prev));
  }, [clearResyncTimer]);
  const markSyncing = useCallback((generation: number) => {
    const current = activeRunRef.current;
    if (!isCurrentAiRun(current, generation)) return;
    if (syncTimerRef.current !== null) {
      clearTimeout(syncTimerRef.current);
      syncTimerRef.current = null;
    }
    setSyncNotice({ generation, phase: "syncing", count: 0 });
  }, []);
  const markSynced = useCallback((generation: number, count: number) => {
    const current = activeRunRef.current;
    if (!isCurrentAiRun(current, generation)) return;
    setSyncNotice((prev) =>
      prev && prev.generation === generation ? { generation, phase: "synced", count } : prev,
    );
    if (syncTimerRef.current !== null) clearTimeout(syncTimerRef.current);
    syncTimerRef.current = setTimeout(() => {
      setSyncNotice((prev) => (prev && prev.generation === generation ? null : prev));
    }, AI_SYNC_NOTICE_DISMISS_MS);
  }, []);
  const markSyncFailed = useCallback((generation: number) => {
    const current = activeRunRef.current;
    if (!isCurrentAiRun(current, generation) || current.settled) return;
    setSyncNotice((prev) =>
      prev && prev.generation === generation ? { generation, phase: "failed", count: 0 } : prev,
    );
  }, []);
  const confirmCard = pendingInteraction(conv, activeGeneration, "confirm");
  const questionCard = pendingInteraction(conv, activeGeneration, "question");
  const confirmCardRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (confirmCard?.id) confirmCardRef.current?.focus({ preventScroll: true });
  }, [confirmCard?.id]);
  const productionNames = confirmCard?.requiredEnvironmentNames;
  // The confirm state is bound to the owning card id and honored only while
  // that id is the current card: a directly replaced card starts locked in
  // the same render, never relying on a passive effect for the safety reset.
  const [productionConfirm, setProductionConfirm] = useState<{
    cardId: string;
    canConfirm: boolean;
    submittedNames: string[];
  } | null>(null);
  const productionConfirmRef = useRef(productionConfirm);
  productionConfirmRef.current = productionConfirm;
  const handleProductionState = useCallback((canConfirm: boolean, submittedNames: string[]) => {
    const cardId = confirmCard?.id;
    if (!cardId) return;
    setProductionConfirm((current) =>
      current !== null &&
      current.cardId === cardId &&
      current.canConfirm === canConfirm &&
      current.submittedNames.join("\u0000") === submittedNames.join("\u0000")
        ? current
        : { cardId, canConfirm, submittedNames },
    );
  }, [confirmCard?.id]);
  const currentProductionConfirm =
    productionConfirm !== null && productionConfirm.cardId === confirmCard?.id
      ? productionConfirm
      : null;
  const productionBlocked = productionNames !== undefined && currentProductionConfirm?.canConfirm !== true;
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
  const [conversationId, setConversationId] = useState<string | undefined>(boundConversationId);
  const [conversationError, setConversationError] = useState<string | null>(null);
  const conversationIdRef = useRef<string | undefined>(undefined);
  const restoreBindingRef = useRef<{ id: string; generation: number } | null>(null);
  const onConversationChangeRef = useRef(onConversationChange);
  onConversationChangeRef.current = onConversationChange;

  return { isLifecycleActive, conversationIdRef, toolHandlesRef, setToolHandleVersion, lastRefreshedHandleVersionRef, restoreBindingRef, restoreGenerationRef, setConversationId, onConversationChangeRef, stream, conv, permScope, conversationId, pushToast, input, workspaces, activeRunRef, cancelWaitersRef, cancelOperationRef, setActiveRun, pushAgentEvent, setBusy, runChannelRef, restoredChannelRef, disposeRestoredChannel, clearResyncState, activeRef, catchupChains, clearResyncTimer, markSyncing, markSynced, markSyncFailed, resyncTimerRef, terminalPollRef, terminalPollFailuresRef, activeRun, runSequenceRef, runControl, setConversationError, images, refs, planMode, busyRef, beginRun, requestSnapshotsRef, setInput, setRefs, setImages, aiScope, setPlanMode, busy, submittingCardIdRef, setSubmittingCardId, confirmCard, submittingCardId, currentProductionConfirm, questionCard, questionInput, productionConfirmRef, toolHandleVersion, stopRef, takeoverState, anyAiBusy, bindings, reclaimKeyLabel, aiScopeAssetId, activeWorkspace, conversationError, confirmCardRef, productionNames, handleProductionState, productionBlocked, setQuestionInput, syncNotice, activeGeneration, hitlWaiting, aiScopeProduction, setModelPanelOpen, modelPanelOpen, rightOpen, rightOpenRef };
}

export type UseChatRunSessionOut = ReturnType<typeof useChatRunSession>;
