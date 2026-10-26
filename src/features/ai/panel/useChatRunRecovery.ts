import { useCallback, useEffect } from "react";
import { isCurrentAiRun, markAiRunCancellationUnconfirmed, requestAiRunCancellation, confirmAiRunTerminal, settleAiRun, clearAiTabRun, registerAiRunGeneration, aiTabRunControls, bindAiRunJob, createAiRun } from "../runOwnership";
import type { AiHitlEventDto, AiHitlSnapshotDto, AiRunDto, MessageDto } from "../../../ipc/types";
import { aiApi } from "../../../ipc/commands";
import { describeError } from "../../../ui/errorText";
import { isRunTerminal, replayRunEventsAfter, pendingDeadline, RUN_RESTORE_LIMIT, replayableRuns, findResumableRun } from "../runRestore";
import { type ApplyResult } from "../conversation";
import { useUi, nextTabId } from "../../../app/store";
import { createAiChannel, onChannelReopen } from "../../../ipc/events";
import { terminalEventOf, aiCreatedTerminalOf, AI_RESYNC_MAX_ATTEMPTS, AI_RESYNC_BACKOFF_MS, AI_HITL_EXPIRY_GRACE_MS, type AiChatBase } from "./aiSidebarShared";
import type { UseChatRunSessionOut } from "./useChatRunSession";
import type { UseChatPanelStateOut } from "./useChatPanelState";

export function useChatRunRecovery(p: AiChatBase & UseChatRunSessionOut & UseChatPanelStateOut) {
  const { isLifecycleActive, activeRunRef, stream, cancelWaitersRef, cancelOperationRef, setActiveRun, pushToast, conversationIdRef, pushAgentEvent, setBusy, runChannelRef, restoredChannelRef, disposeRestoredChannel, clearResyncState, tabKey, activeRef, catchupChains, clearResyncTimer, markSyncing, markSynced, markSyncFailed, resyncTimerRef, terminalPollRef, terminalPollFailuresRef, activeRun, isRestoreActive, setConversationRuns, runSequenceRef, runControl, restoreGenerationRef, updateConversationId, clearMessageHistory, setConversationError, resetMessageHistory, boundConversationId, setConversationId, restoreBindingRef } = p;


  const replayHitl = useCallback(async (generation: number, jobId: string) => {
    const canApply = () =>
      isLifecycleActive() && isCurrentAiRun(activeRunRef.current, generation);
    const plan = stream.planHitlReplay(generation);
    let events: AiHitlEventDto[] = [];
    try {
      events = await aiApi.hitlEvents(jobId, plan.afterSeq);
    } catch {
      events = [];
    }
    if (!canApply()) return;
    try {
      const snapshot = await aiApi.hitlSnapshot(jobId);
      if (canApply()) stream.applyHitlReplay(generation, plan, events, snapshot);
    } catch {
      if (canApply() && events.length > 0) stream.applyHitlReplay(generation, plan, events, null);
    }
  }, [stream, isLifecycleActive, activeRunRef]);
  const hasCloseWaiter = (generation: number) =>
    [...cancelWaitersRef.current].some((waiter) => waiter.generation === generation && waiter.closeTab);

  const confirmCancellation = useCallback((generation: number) => {
    if (cancelOperationRef.current?.generation === generation) cancelOperationRef.current = null;
    for (const waiter of [...cancelWaitersRef.current]) {
      if (waiter.generation !== generation) continue;
      clearTimeout(waiter.timer);
      cancelWaitersRef.current.delete(waiter);
      waiter.resolve(true);
    }
  }, [cancelOperationRef, cancelWaitersRef]);

  const unconfirmCancellation = useCallback((generation: number, error?: unknown) => {
    const current = activeRunRef.current;
    if (isCurrentAiRun(current, generation) && current.cancellation !== "idle") {
      setActiveRun(markAiRunCancellationUnconfirmed(current));
    }
    let resolved = 0;
    for (const waiter of [...cancelWaitersRef.current]) {
      if (waiter.generation !== generation) continue;
      clearTimeout(waiter.timer);
      cancelWaitersRef.current.delete(waiter);
      waiter.resolve(false);
      resolved += 1;
    }
    if (resolved > 0) {
      pushToast(
        "error",
        error === undefined
          ? "停止未确认：等待任务结束超时，可重试或查看任务"
          : `停止未确认：${describeError(error)}`,
      );
    }
  }, [activeRunRef, setActiveRun, cancelWaitersRef, pushToast]);

  const confirmCancellationFromQuery = async (
    operation: { generation: number; jobId: string | null },
  ): Promise<boolean> => {
    const conversationId = conversationIdRef.current;
    if (!conversationId || !operation.jobId) return false;
    try {
      const runs = await aiApi.runs(conversationId);
      if (cancelOperationRef.current?.generation !== operation.generation) return false;
      const terminalRun = runs.find((run) => run.id === operation.jobId && isRunTerminal(run));
      if (!terminalRun) return false;
      if (!hasCloseWaiter(operation.generation)) {
        await catchUpRunEvents(operation.generation, operation.jobId);
        const hasOutcome = stream.getState().attempts.some(
          (attempt) => attempt.generation === operation.generation && attempt.outcome,
        );
        if (!hasOutcome) {
          const terminalEvent = terminalEventOf(terminalRun);
          if (terminalEvent) {
            pushAgentEvent(operation.generation, terminalEvent);
            stream.flush();
          }
        }
      }
      settleRestoredRun(operation.generation);
      return true;
    } catch {
      return false;
    }
  };

  const driveCancellation = async () => {
    const operation = cancelOperationRef.current;
    const current = activeRunRef.current;
    if (!operation || !isCurrentAiRun(current, operation.generation)) return;
    if (current.settled && current.cancellation === "idle") {
      confirmCancellation(operation.generation);
      return;
    }
    if (current.spawnPending || !current.jobId) return;
    operation.jobId = current.jobId;
    if (!operation.cancelSent) {
      operation.cancelSent = true;
      try {
        if (current.kind === "takeover") await aiApi.takeoverCancel(current.jobId);
        else await aiApi.cancel(current.jobId);
      } catch (error) {
        if (cancelOperationRef.current !== operation) return;
        operation.cancelSent = false;
        if (await confirmCancellationFromQuery(operation)) return;
        if (cancelOperationRef.current !== operation) return;
        unconfirmCancellation(operation.generation, error);
        return;
      }
      if (cancelOperationRef.current !== operation) return;
      for (const item of stream.getState().items) {
        if (item.attempt === operation.generation && item.role === "user" && item.steer === "pending") {
          stream.resolveSteer(operation.generation, item.id, "dropped");
        }
      }
      pushToast("info", current.kind === "takeover" ? "已请求停止接管，等待终端退出" : "已请求停止，等待任务结束");
    }
    if (!isLifecycleActive()) {
      const deadline = Date.now() + 4500;
      while (cancelOperationRef.current === operation) {
        if (await confirmCancellationFromQuery(operation)) return;
        const remaining = deadline - Date.now();
        if (remaining <= 0) return;
        await new Promise<void>((resolve) => setTimeout(resolve, Math.min(100, remaining)));
      }
      return;
    }
    await confirmCancellationFromQuery(operation);
  };

  const requestCancellation = (closeTab: boolean): Promise<boolean> => {
    const current = activeRunRef.current;
    if (!current || (current.settled && current.cancellation === "idle")) return Promise.resolve(true);
    const retry = current.cancellation === "unconfirmed";
    setActiveRun(requestAiRunCancellation(current));
    if (cancelOperationRef.current?.generation !== current.generation) {
      cancelOperationRef.current = {
        generation: current.generation,
        jobId: null,
        cancelSent: false,
      };
    } else if (retry) {
      cancelOperationRef.current.cancelSent = false;
    }
    const generation = current.generation;
    const promise = new Promise<boolean>((resolve) => {
      const waiter = {
        generation,
        closeTab,
        resolve,
        timer: setTimeout(() => {
          if (!cancelWaitersRef.current.delete(waiter)) return;
          const latest = activeRunRef.current;
          if (isCurrentAiRun(latest, generation) && latest.cancellation !== "idle") {
            setActiveRun(markAiRunCancellationUnconfirmed(latest));
          }
          pushToast("error", "停止未确认：等待任务结束超时，可重试或查看任务");
          resolve(false);
        }, 5000),
      };
      cancelWaitersRef.current.add(waiter);
    });
    void driveCancellation();
    return promise;
  };

  const settleRestoredRun = useCallback((generation: number, confirmed = true) => {
    const current = activeRunRef.current;
    if (!confirmed && isCurrentAiRun(current, generation) && current.cancellation !== "idle") return;
    if (isCurrentAiRun(current, generation)) {
      const next = confirmed ? confirmAiRunTerminal(current) : settleAiRun(current);
      setActiveRun(next);
      if (!current.spawnPending) setBusy(false);
    }
    const live = runChannelRef.current;
    if (live && live.generation === generation) live.dispose();
    const restored = restoredChannelRef.current;
    if (restored && restored.generation === generation) disposeRestoredChannel();
    clearResyncState(generation);
    if (confirmed) {
      confirmCancellation(generation);
      if (activeRunRef.current?.generation === generation) clearAiTabRun(tabKey);
    }
  }, [activeRunRef, runChannelRef, restoredChannelRef, disposeRestoredChannel, clearResyncState, setActiveRun, setBusy, confirmCancellation, tabKey]);

  // maybeOpenAiCreatedTerminal opens a terminal_create foreground tab in the
  // workspace, the same recipe as the background-sessions takeover. It fires
  // only for events of the live run; historical replays never re-open old tabs.
  const maybeOpenAiCreatedTerminal = useCallback((generation: number, ev: Record<string, unknown>, result: ApplyResult) => {
    const current = activeRunRef.current;
    if (!isLifecycleActive() || !isCurrentAiRun(current, generation) || current.settled) return;
    for (const candidate of [{ ev, accepted: result.accepted }, ...(result.drained ?? [])]) {
      if (!candidate.accepted) continue;
      const terminal = aiCreatedTerminalOf(candidate.ev);
      if (!terminal) continue;
      if (!isLifecycleActive()) return;
      const st = useUi.getState();
      const session = st.sessions.find((s) => s.id === terminal.sessionId);
      const activate = activeRef.current && st.rightOpen;
      const previousActiveWorkspaceId = st.activeWorkspaceId;
      const previousWorkspaces = st.workspaces;
      st.addTab({
        id: nextTabId(`term-${terminal.sessionId}`),
        kind: "terminal",
        title: terminal.title || session?.name || terminal.sessionId,
        ...(terminal.title ? { renamedByUser: true, customTitleAt: Date.now() } : {}),
        sessionId: terminal.sessionId,
        tabId: terminal.tabId,
        closable: true,
      });
      if (activate) continue;
      const previousById = new Map(previousWorkspaces.map((workspace) => [workspace.id, workspace]));
      useUi.setState((state) => ({
        activeWorkspaceId: previousActiveWorkspaceId,
        workspaces: state.workspaces.map((workspace) => {
          const previous = previousById.get(workspace.id);
          if (!previous) return workspace;
          return {
            ...workspace,
            activePaneId: previous.activePaneId,
            panes: workspace.panes.map((pane) => {
              const previousPane = previous.panes.find((candidatePane) => candidatePane.id === pane.id);
              return previousPane ? { ...pane, activeTabId: previousPane.activeTabId } : pane;
            }),
          };
        }),
      }));
    }
  }, [activeRef, activeRunRef, isLifecycleActive]);

  const replayRunEvents = useCallback(async (
    generation: number,
    jobId: string,
  ): Promise<{ applied: number; failed: boolean }> => {
    let applied = 0;
    const { failed } = await replayRunEventsAfter(
      (afterSeq, limit) => aiApi.runEvents(jobId, afterSeq, limit),
      stream.lastSeq(generation),
      (events) => {
        for (const event of events) {
          if (!isLifecycleActive()) return;
          const result = pushAgentEvent(generation, event as Record<string, unknown>);
          if (result.accepted) applied += 1;
          maybeOpenAiCreatedTerminal(generation, event as Record<string, unknown>, result);
          if (result.terminal) settleRestoredRun(generation);
        }
        if (isLifecycleActive()) stream.flush();
      },
      { shouldContinue: isLifecycleActive },
    );
    return { applied, failed };
  }, [stream, isLifecycleActive, maybeOpenAiCreatedTerminal, settleRestoredRun, pushAgentEvent]);

  const catchUpRunEvents = useCallback((
    generation: number,
    jobId: string,
  ): Promise<{ applied: number; failed: boolean }> => {
    const chains = catchupChains.current;
    const prev = chains.get(generation) ?? Promise.resolve({ applied: 0, failed: false });
    const next = prev
      .catch(() => ({ applied: 0, failed: true }))
      .then(() => replayRunEvents(generation, jobId));
    chains.set(generation, next);
    const cleanup = () => {
      if (chains.get(generation) === next) chains.delete(generation);
    };
    void next.then(cleanup, cleanup);
    return next;
  }, [catchupChains, replayRunEvents]);

  const resyncRun = useCallback((generation: number, jobId: string, attempt = 0) => {
    clearResyncTimer(generation);
    markSyncing(generation);
    void catchUpRunEvents(generation, jobId).then((result) => {
      if (!isLifecycleActive()) return;
      if (!result.failed) {
        markSynced(generation, result.applied);
        return;
      }
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, generation) || current.settled) return;
      if (attempt >= AI_RESYNC_MAX_ATTEMPTS) {
        markSyncFailed(generation);
        if (current.cancellation === "idle") {
          setActiveRun(settleAiRun(current));
          setBusy(false);
        }
        return;
      }
      clearResyncTimer(generation);
      resyncTimerRef.current = {
        generation,
        timer: setTimeout(
          () => resyncRun(generation, jobId, attempt + 1),
          AI_RESYNC_BACKOFF_MS * (attempt + 1),
        ),
      };
    });
    void replayHitl(generation, jobId);
  }, [clearResyncTimer, markSyncing, catchUpRunEvents, replayHitl, isLifecycleActive, activeRunRef, resyncTimerRef, markSynced, markSyncFailed, setActiveRun, setBusy]);

  terminalPollRef.current = async (generation, jobId) => {
    const current = activeRunRef.current;
    const id = conversationIdRef.current;
    if (!isLifecycleActive() || !isCurrentAiRun(current, generation) || current.settled || current.jobId !== jobId || !id) return;
    try {
      const run = await aiApi.run(jobId);
      if (!isLifecycleActive() || !isCurrentAiRun(activeRunRef.current, generation)) return;
      terminalPollFailuresRef.current = 0;
      if (run.conversationId !== id || !isRunTerminal(run)) return;
      await catchUpRunEvents(generation, jobId);
      const latest = activeRunRef.current;
      if (!isLifecycleActive() || !isCurrentAiRun(latest, generation) || latest.settled || latest.jobId !== jobId) return;
      const hasOutcome = stream.getState().attempts.some(
        (attempt) => attempt.generation === generation && attempt.outcome,
      );
      if (!hasOutcome) {
        const event = terminalEventOf(run);
        if (event) {
          pushAgentEvent(generation, event);
          stream.flush();
        }
      }
      settleRestoredRun(generation);
    } catch {
      if (!isLifecycleActive() || !isCurrentAiRun(activeRunRef.current, generation)) return;
      terminalPollFailuresRef.current += 1;
      if (terminalPollFailuresRef.current === 3) {
        pushToast("error", "AI 任务状态检查失败，正在重试；可刷新会话确认结果");
      }
    }
  };

  useEffect(() => {
    const generation = activeRun?.generation;
    const jobId = activeRun?.jobId;
    if (generation === undefined || !jobId || activeRun?.settled) return;
    terminalPollFailuresRef.current = 0;
    let inFlight = false;
    const timer = window.setInterval(() => {
      if (inFlight) return;
      inFlight = true;
      void terminalPollRef.current(generation, jobId).finally(() => { inFlight = false; });
    }, 5000);
    return () => window.clearInterval(timer);
  }, [activeRun?.generation, activeRun?.jobId, activeRun?.settled, terminalPollFailuresRef, terminalPollRef]);

  const replayRestoredIfBound = useCallback((jobId: string) => {
    const restored = restoredChannelRef.current;
    if (!restored || restored.jobId !== jobId) return;
    void replayRunEvents(restored.generation, jobId);
  }, [replayRunEvents, restoredChannelRef]);

  const armExpiryWatch = useCallback((jobId: string) => {
    const restored = restoredChannelRef.current;
    if (!restored || restored.jobId !== jobId) return;
    if (restored.expiryRetries <= 0) {
      settleRestoredRun(restored.generation, false);
      return;
    }
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
  }, [restoredChannelRef, settleRestoredRun, replayRunEvents, activeRunRef]);

  const scheduleExpiryWatch = useCallback((snapshot: AiHitlSnapshotDto) => {
    const deadline = pendingDeadline(snapshot);
    if (deadline === null) return null;
    const delay = Math.min(
      Math.max(0, deadline - Date.now()) + AI_HITL_EXPIRY_GRACE_MS,
      2_000_000_000,
    );
    return setTimeout(() => {
      const restored = restoredChannelRef.current;
      if (!restored) return;
      void replayRunEvents(restored.generation, restored.jobId).finally(() => {
        const settled = activeRunRef.current?.settled ?? true;
        if (!settled) armExpiryWatch(restored.jobId);
      });
    }, delay);
  }, [restoredChannelRef, replayRunEvents, activeRunRef, armExpiryWatch]);

  const restoreConversationRuns = useCallback(async (
    id: string,
    restoreGeneration: number,
    preloaded?: AiRunDto[],
  ) => {
    let runs: AiRunDto[];
    if (preloaded) {
      runs = preloaded.slice(0, RUN_RESTORE_LIMIT);
    } else {
      try {
        runs = (await aiApi.runs(id, RUN_RESTORE_LIMIT)).slice(0, RUN_RESTORE_LIMIT);
      } catch {
        return;
      }
    }
    if (!isRestoreActive(restoreGeneration, id)) return;
    setConversationRuns(runs);
    if (runs.length === 0) return;
    const replay = replayableRuns(runs);
    const generations = new Map<string, number>();
    for (const run of replay) {
      if (!isRestoreActive(restoreGeneration, id)) return;
      const generation = ++runSequenceRef.current;
      generations.set(run.id, generation);
      stream.beginRun(generation, "chat");
      stream.bindJob(generation, run.id);
      await replayRunEventsAfter(
        (afterSeq, limit) => aiApi.runEvents(run.id, afterSeq, limit),
        0,
        (events) => {
          for (const event of events) {
            if (!isRestoreActive(restoreGeneration, id)) return;
            pushAgentEvent(generation, event as Record<string, unknown>);
          }
          if (isRestoreActive(restoreGeneration, id)) stream.flush();
        },
        { shouldContinue: () => isRestoreActive(restoreGeneration, id) },
      );
      if (!isRestoreActive(restoreGeneration, id)) return;
    }
    const resumable = await findResumableRun(
      runs,
      async (jobId) => {
        try {
          return await aiApi.hitlSnapshot(jobId);
        } catch {
          return null;
        }
      },
      () => isRestoreActive(restoreGeneration, id),
    );
    if (!resumable || !isRestoreActive(restoreGeneration, id)) return;
    const generation = generations.get(resumable.run.id);
    if (generation === undefined) return;
    if (activeRunRef.current && !activeRunRef.current.settled) return;
    const channel = createAiChannel((ev) => {
      if (!isRestoreActive(restoreGeneration, id)) return;
      const type = ev.type as string;
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, generation) || (current.settled && current.cancellation === "idle")) {
        if (type === "done" || type === "error" || type === "canceled") {
          const restored = restoredChannelRef.current;
          if (restored && restored.generation === generation) disposeRestoredChannel();
          clearResyncState(generation);
        }
        return;
      }
      const result = pushAgentEvent(generation, ev);
      if (result.gap) resyncRun(generation, resumable.run.id);
      maybeOpenAiCreatedTerminal(generation, ev, result);
      if (!result.terminal) return;
      settleRestoredRun(generation);
      if (type === "error" && result.accepted) pushToast("error", `AI: ${ev.message as string}`);
    });
    const offReopen = onChannelReopen(channel, () => {
      if (!isRestoreActive(restoreGeneration, id)) return;
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, generation) || current.settled) return;
      resyncRun(generation, resumable.run.id);
    });
    disposeRestoredChannel();
    restoredChannelRef.current = {
      channel,
      offReopen,
      jobId: resumable.run.id,
      generation,
      expiryTimer: scheduleExpiryWatch(resumable.snapshot),
      expiryRetries: 8,
    };
    registerAiRunGeneration(tabKey, generation);
    aiTabRunControls.set(tabKey, runControl);
    setActiveRun(bindAiRunJob(createAiRun(generation), resumable.run.id));
    setBusy(true);
    void replayHitl(generation, resumable.run.id);
  }, [isRestoreActive, setConversationRuns, activeRunRef, disposeRestoredChannel, restoredChannelRef, scheduleExpiryWatch, tabKey, runControl, setActiveRun, setBusy, replayHitl, runSequenceRef, stream, pushAgentEvent, resyncRun, maybeOpenAiCreatedTerminal, settleRestoredRun, pushToast, clearResyncState]);

  const restoreConversation = useCallback(
    async (id: string) => {
      if (!isLifecycleActive()) return;
      const restoreGeneration = ++restoreGenerationRef.current;
      const startSeq = runSequenceRef.current;
      updateConversationId(id);
      try {
        const list = await aiApi.conversationList();
        if (!isRestoreActive(restoreGeneration, id)) return;
        const hit = list.find((c) => c.id === id);
        if (!hit) {
          updateConversationId(undefined);
          clearMessageHistory();
          stream.reset();
          setConversationError(null);
          return;
        }
        if (hit.title) updateConversationId(id, hit.title);
      } catch {
        if (!isRestoreActive(restoreGeneration, id)) return;
      }
      let msgs: MessageDto[];
      let runs: AiRunDto[];
      try {
        [msgs, runs] = await Promise.all([
          aiApi.messages(id),
          aiApi
            .runs(id, RUN_RESTORE_LIMIT)
            .then((rows) => rows.slice(0, RUN_RESTORE_LIMIT))
            .catch((): AiRunDto[] => []),
        ]);
      } catch (e) {
        if (!isRestoreActive(restoreGeneration, id)) return;
        setConversationError(describeError(e));
        return;
      }
      if (!isRestoreActive(restoreGeneration, id) || runSequenceRef.current !== startSeq) return;
      setConversationError(null);
      resetMessageHistory(id, msgs, runs);
      if (!isRestoreActive(restoreGeneration, id) || runSequenceRef.current !== startSeq) return;
      void restoreConversationRuns(id, restoreGeneration, runs);
    },
    [clearMessageHistory, isLifecycleActive, isRestoreActive, resetMessageHistory, restoreConversationRuns, restoreGenerationRef, runSequenceRef, setConversationError, stream, updateConversationId],
  );

  // Conversation restore driven by the external binding (persisted board); proactive in-tab switches already synced the ref and do not restore again.
  useEffect(() => {
    if (boundConversationId === undefined) {
      if (conversationIdRef.current !== undefined) {
        restoreGenerationRef.current += 1;
        conversationIdRef.current = undefined;
        setConversationId(undefined);
        clearMessageHistory();
        stream.reset();
      }
      return;
    }
    const binding = restoreBindingRef.current;
    if (
      conversationIdRef.current === boundConversationId &&
      binding?.id === boundConversationId &&
      binding.generation === restoreGenerationRef.current
    ) {
      return;
    }
    void restoreConversation(boundConversationId);
  }, [boundConversationId, clearMessageHistory, conversationIdRef, restoreBindingRef, restoreConversation, restoreGenerationRef, setConversationId, stream]);

  const hydrateMessageIds = async (convId: string) => {
    try {
      const msgs = await aiApi.messages(convId);
      if (!isLifecycleActive() || conversationIdRef.current !== convId) return;
      stream.hydrateUserMessageIds(msgs);
    } catch {
      return;
    }
  };

  return { resyncRun, maybeOpenAiCreatedTerminal, settleRestoredRun, driveCancellation, hydrateMessageIds, unconfirmCancellation, replayHitl, replayRestoredIfBound, requestCancellation, restoreConversationRuns, restoreConversation };
}

export type UseChatRunRecoveryOut = ReturnType<typeof useChatRunRecovery>;
