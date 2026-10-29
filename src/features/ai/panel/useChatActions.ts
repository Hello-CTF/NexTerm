import { aiTabBusyIds, aiRunBlocksStart, isCurrentAiRun, finishAiRunSpawn, bindAiRunJob, settleAiRun } from "../runOwnership";
import { createAiChannel, onChannelReopen, disposeChannel } from "../../../ipc/events";
import { aiApi } from "../../../ipc/commands";
import { describeError } from "../../../ui/errorText";
import { useRef, useCallback, useState, useMemo, useEffect } from "react";
import { type ChatItem, type ConfirmItem, pendingInteraction, type ToolItem } from "../conversation";
import { confirmationInput, answerInput } from "../aiWire";
import { useAuth } from "../../auth/store";
import type { AgentApprovalDecision, AgentRosterEntry, AgentToolCall } from "../agentTypes";
import { createAgentRosterApi } from "../agentProduction";
import { useAgentRosterSnapshot, refreshAgentRosterSnapshot } from "../agentRosterSnapshot";
import { runSummaryFromAttempt, buildAgentToolCall, approvalFromConfirmCard } from "../agentToolData";
import { type RefChip, latestPlanApproved, PLAN_EXECUTE_MESSAGE, CONFIRM_RESOLUTION, type AiChatBase } from "./aiSidebarShared";
import type { UseChatRunSessionOut } from "./useChatRunSession";
import type { UseChatPanelStateOut } from "./useChatPanelState";
import type { UseChatRunRecoveryOut } from "./useChatRunRecovery";

export function useChatActions(p: AiChatBase & UseChatRunSessionOut & UseChatPanelStateOut & UseChatRunRecoveryOut) {
  const { input, images, refs, planMode, busyRef, tabKey, activeRunRef, permOpen, setPermOpen, beginRun, requestSnapshotsRef, setBusy, setInput, setRefs, setAtOpen, setImages, stream, isLifecycleActive, clearResyncState, pushAgentEvent, resyncRun, maybeOpenAiCreatedTerminal, settleRestoredRun, pushToast, runChannelRef, conversationId, aiScope, composeMessage, deletedConversationIdsRef, updateConversationId, setActiveRun, driveCancellation, hydrateMessageIds, unconfirmCancellation, conv, setPlanMode, busy, conversationRuns, inputRef, conversationIdRef, disposeRestoredChannel, submittingCardIdRef, setSubmittingCardId, restoredChannelRef, replayHitl, replayRestoredIfBound, confirmCard, submittingCardId, currentProductionConfirm, questionCard, questionInput, productionConfirmRef, toolHandleVersion, lastRefreshedHandleVersionRef, toolHandlesRef } = p;

  const send = async (override?: {
    message?: string;
    images?: string[];
    refs?: RefChip[];
    planMode?: boolean;
  }) => {
    const message = (override?.message ?? input).trim();
    const sentImages = override?.images ?? images;
    const sentRefs = override?.refs ?? refs;
    const usePlan = override?.planMode ?? planMode;
    if (
      (!message && sentImages.length === 0) ||
      busyRef.current ||
      aiTabBusyIds.has(tabKey) ||
      aiRunBlocksStart(activeRunRef.current)
    ) {
      return;
    }
    if (permOpen) setPermOpen(false);
    const run = beginRun();
    requestSnapshotsRef.current.set(run.generation, {
      message,
      images: [...sentImages],
      refs: [...sentRefs],
      planMode: usePlan,
    });
    setBusy(true);
    setInput("");
    setRefs([]);
    setAtOpen(false);
    setImages([]);
    stream.appendUser(run.generation, message, sentImages.length);

    let channelDisposed = false;
    const channel = createAiChannel((ev) => {
      if (!isLifecycleActive()) return;
      const type = ev.type as string;
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, run.generation) || (current.settled && current.cancellation === "idle")) {
        if (type === "done" || type === "error" || type === "canceled") {
          clearResyncState(run.generation);
          dispose();
        }
        return;
      }
      const result = pushAgentEvent(run.generation, ev);
      if (result.gap && current.jobId) resyncRun(run.generation, current.jobId);
      maybeOpenAiCreatedTerminal(run.generation, ev, result);
      if (!result.terminal) return;
      settleRestoredRun(run.generation);
      if (type === "error" && result.accepted) pushToast("error", `AI 回答出错：${ev.message as string}`);
    });

    const offHitlReopen = onChannelReopen(channel, () => {
      if (!isLifecycleActive()) return;
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, run.generation) || current.settled || !current.jobId) return;
      resyncRun(run.generation, current.jobId);
    });
    const dispose = () => {
      if (channelDisposed) return;
      channelDisposed = true;
      if (runChannelRef.current?.channel === channel) runChannelRef.current = null;
      offHitlReopen();
      disposeChannel(channel);
    };
    runChannelRef.current = { channel, dispose, generation: run.generation };

    try {
      const payloadRefs = sentRefs.map((r) => ({
        kind: r.kind,
        id: r.refId,
        label: r.label,
        path: r.path,
        sessionId: r.sessionId,
      }));
      const res = await aiApi.chat({
        conversationId,
        scope: aiScope,
        message: composeMessage(message, sentRefs),
        images: sentImages.length ? sentImages : undefined,
        refs: payloadRefs.length ? payloadRefs : undefined,
        planMode: usePlan || undefined,
        channel,
      });
      const current = activeRunRef.current;
      if (!isLifecycleActive() || !isCurrentAiRun(current, run.generation)) {
        dispose();
        return;
      }
      if (!deletedConversationIdsRef.current.has(res.conversationId)) {
        updateConversationId(res.conversationId);
      }
      if (current.settled && current.cancellation === "idle") {
        setActiveRun(finishAiRunSpawn(current));
        setBusy(false);
        return;
      }
      setActiveRun(bindAiRunJob(current, res.jobId));
      stream.bindJob(run.generation, res.jobId);
      void driveCancellation();
      if (stream.hasGap(run.generation)) resyncRun(run.generation, res.jobId);
      void hydrateMessageIds(res.conversationId);
    } catch (e) {
      const current = activeRunRef.current;
      if (isLifecycleActive() && isCurrentAiRun(current, run.generation)) {
        const result = pushAgentEvent(run.generation, {
          type: "error",
          message: describeError(e),
        });
        if (result.accepted && !current.settled) pushToast("error", describeError(e));
        setActiveRun(settleAiRun(finishAiRunSpawn(current)));
        setBusy(false);
        if (current.cancellation !== "idle") unconfirmCancellation(run.generation, e);
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
      pushToast("info", "任务已结束，请将此内容作为新消息发送");
      return;
    }
    if (run.spawnPending) {
      pushToast("info", "任务正在启动，请稍后再试");
      return;
    }
    if (images.length > 0) {
      pushToast("info", "回答过程中追加的指令不支持附带图片，请先移除图片");
      return;
    }
    const target = run;
    const jobId = run.jobId;
    if (run.kind !== "chat" || !jobId) {
      pushToast("info", "终端接管期间不支持追加指令");
      return;
    }
    if (permOpen) setPermOpen(false);
    const text = composeMessage(message, refs);
    setInput("");
    const itemId = stream.appendSteer(target.generation, message);
    try {
      await aiApi.steer(jobId, text);
    } catch (e) {
      stream.resolveSteer(target.generation, itemId, "dropped");
      setInput((prev) => (prev ? `${message}\n${prev}` : message));
      pushToast("error", `追加的指令未送达：${describeError(e)}`);
    }
  };

  const approvePlan = () => {
    if (latestPlanApproved(conv.items)) {
      pushToast("info", "该计划已执行，不会重复执行；需要调整时请让 AI 重新生成方案");
      return;
    }
    setPlanMode(false);
    void send({ message: PLAN_EXECUTE_MESSAGE, images: [], refs: [], planMode: false });
  };
  const approvePlanRef = useRef(approvePlan);
  approvePlanRef.current = approvePlan;
  const handleApprovePlan = useCallback(() => approvePlanRef.current(), []);

  const retryRun = (item: ChatItem) => {
    if (item.role !== "outcome" || item.outcome !== "error" || !item.retryable) return;
    if (busy || aiRunBlocksStart(activeRunRef.current)) {
      pushToast("info", "AI 正在回答，请先停止当前回答再重试");
      return;
    }
    const source = [...conv.items]
      .reverse()
      .find(
        (candidate): candidate is Extract<ChatItem, { role: "user" }> =>
          candidate.attempt === item.attempt && candidate.role === "user" && candidate.steer === undefined,
      );
    if (!source) {
      pushToast("info", "未找到原消息内容，请手动重新发送");
      return;
    }
    if (source.text.trim() === PLAN_EXECUTE_MESSAGE) {
      pushToast("info", "该计划已执行，无法通过重试再次执行；需要调整时请让 AI 重新生成方案");
      return;
    }
    const snapshot = item.attempt === null ? undefined : requestSnapshotsRef.current.get(item.attempt);
    if (snapshot) {
      void send(snapshot);
      return;
    }
    const jobId = item.attempt === null
      ? undefined
      : conv.attempts.find((attempt) => attempt.generation === item.attempt)?.jobId;
    const planMode = conversationRuns.find((run) => run.id === jobId)?.planMode ?? false;
    void send({ message: source.text, images: [], refs: [], planMode });
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
      pushToast("error", "编辑并重新发送需要先打开一个会话");
      return;
    }
    editSubmittingRef.current = true;
    setEditSubmitting(true);
    try {
      await aiApi.editResend(conversationId, edit.messageId);
    } catch (e) {
      editSubmittingRef.current = false;
      setEditSubmitting(false);
      pushToast("error", `未能编辑并重新发送：${describeError(e)}`);
      return;
    }
    try {
      const current = activeRunRef.current;
      if (current && !current.settled) {
        setActiveRun(settleAiRun(current));
        stream.cancelRun(current.generation, false);
        clearResyncState(current.generation);
      }
      setBusy(false);
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

  // One formal confirmation path shared by the card buttons and the agent
  // approval delegate: the busy guard, takeover/restored-channel routing,
  // failure replay/resync and interaction resolution are never a reduced
  // copy. Returns whether the decision reached the backend.
  const submitConfirmCard = useCallback(
    async (
      card: ConfirmItem,
      decision: "allow" | "allow_session" | "allow_persistent" | "deny",
      environmentNames?: string[],
    ): Promise<boolean> => {
      const run = activeRunRef.current;
      const id = run?.jobId;
      if (!run || !id) {
        pushToast("info", "任务正在启动，请稍后再试");
        return false;
      }
      if (submittingCardIdRef.current) return false;
      setSubmittingCardId(card.id);
      try {
        const input = confirmationInput(
          { jobId: id, callId: card.callId, nonce: card.nonce },
          decision,
          environmentNames,
        );
        const channel = card.jobId === restoredChannelRef.current?.jobId ? restoredChannelRef.current.channel : undefined;
        if (run.kind === "takeover") {
          await aiApi.takeoverConfirm(input);
        } else if (channel) {
          await aiApi.confirm(input, channel);
        } else {
          await aiApi.confirm(input);
        }
      } catch (e) {
        pushToast("error", `未能提交选择：${describeError(e)}`);
        if (run.kind === "chat" && isCurrentAiRun(activeRunRef.current, run.generation)) {
          void replayHitl(run.generation, id);
        }
        replayRestoredIfBound(id);
        return false;
      } finally {
        setSubmittingCardId(null);
      }
      if (isCurrentAiRun(activeRunRef.current, run.generation)) {
        stream.resolveInteraction(run.generation, card.id, card.nonce, CONFIRM_RESOLUTION[decision]);
      }
      return true;
    },
    [activeRunRef, submittingCardIdRef, setSubmittingCardId, pushToast, restoredChannelRef, replayRestoredIfBound, replayHitl, stream],
  );

  const confirm = async (decision: "allow" | "allow_session" | "allow_persistent" | "deny") => {
    const run = activeRunRef.current;
    const card = confirmCard;
    const id = run?.jobId;
    if (!run || !id || !card) {
      pushToast("info", "任务正在启动，请稍后再试");
      return;
    }
    if (submittingCardId) return;
    // A production challenge is only answered with the user's actual typed
    // environment names, and only with the state bound to this exact card;
    // anything less never reaches the backend.
    if (decision !== "deny" && card.requiredEnvironmentNames) {
      if (currentProductionConfirm === null || !currentProductionConfirm.canConfirm) return;
    }
    await submitConfirmCard(
      card,
      decision,
      decision !== "deny" && card.requiredEnvironmentNames && currentProductionConfirm ? currentProductionConfirm.submittedNames : undefined,
    );
  };

  const answer = async (option?: string) => {
    const run = activeRunRef.current;
    const card = questionCard;
    const id = run?.jobId;
    const text = option ?? questionInput;
    if (!run || !id || !card) {
      pushToast("info", "任务正在启动，请稍后再试");
      return;
    }
    if (!text.trim()) {
      pushToast("info", "请输入回答或选择选项");
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
      pushToast("error", `未能提交回答：${describeError(e)}`);
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

  // Agent production wiring: the reviewed roster/history panels consume the
  // real generated IPC through the adapter (no unsafeCall), and the approval
  // delegate maps reviewed decisions onto the pending HITL interaction.
  const authUser = useAuth((s) => s.user);
  const agentOwnerId = authUser?.id ?? "";
  const agentOwnerName = authUser?.display_name || authUser?.username || "";
  const resolveAgentApproval = useCallback(
    async (requestId: string, decision: AgentApprovalDecision) => {
      const run = activeRunRef.current;
      const card = pendingInteraction(stream.getState(), run?.generation ?? null, "confirm");
      if (!run?.jobId || !card || card.requestId !== requestId) {
        throw { code: "conflict", message: "审批请求不存在或已解决，请稍后刷新重试" };
      }
      // The same production challenge gate as the card buttons: only the
      // user's actual typed names, bound to this exact card, reach the
      // backend — never an auto-echoed or missing answer.
      let environmentNames: string[] | undefined;
      if (decision !== "deny" && card.requiredEnvironmentNames) {
        const challenge = productionConfirmRef.current;
        if (!challenge || challenge.cardId !== card.id || !challenge.canConfirm) {
          throw { code: "conflict", message: "生产环境挑战未完成：请先在确认卡片中输入环境名称" };
        }
        environmentNames = challenge.submittedNames;
      }
      const mapped =
        decision === "approve_once"
          ? "allow"
          : decision === "approve_session"
            ? "allow_session"
            : decision === "approve_persistent"
              ? "allow_persistent"
              : "deny";
      const submitted = await submitConfirmCard(card, mapped, environmentNames);
      if (!submitted) {
        throw { code: "internal", message: "审批提交失败，请重试" };
      }
    },
    [activeRunRef, productionConfirmRef, stream, submitConfirmCard],
  );
  const agentRosterApi = useMemo(
    () =>
      conversationId === undefined
        ? null
        : createAgentRosterApi({
            conversationId,
            identity: { ownerId: agentOwnerId, ownerName: agentOwnerName },
            activeJobId: () => activeRunRef.current?.jobId ?? "",
            resolveApproval: resolveAgentApproval,
          }),
    [conversationId, agentOwnerId, agentOwnerName, resolveAgentApproval, activeRunRef],
  );
  const watchOwner = agentOwnerId === "" ? "local" : agentOwnerId;

  // The roster snapshot joins the recorded handles onto real roster rows,
  // with an independent slot per conversation. It reloads once per
  // recorded-handle change, and again when the slot is empty (for example
  // after a store reset): a handle the roster never covers stays honestly
  // unknown instead of spinning reloads, and a failed load never retries in
  // a loop.
  const rosterSnapshot = useAgentRosterSnapshot(conversationId);
  const rosterByHandle = useMemo(() => {
    const map = new Map<string, AgentRosterEntry>();
    for (const entry of rosterSnapshot ?? []) map.set(entry.handle, entry);
    return map;
  }, [rosterSnapshot]);
  useEffect(() => {
    if (!conversationId || !agentRosterApi || toolHandleVersion === 0) return;
    if (lastRefreshedHandleVersionRef.current === toolHandleVersion && rosterSnapshot !== null) return;
    lastRefreshedHandleVersionRef.current = toolHandleVersion;
    void refreshAgentRosterSnapshot(agentRosterApi, conversationId);
  }, [conversationId, agentRosterApi, toolHandleVersion, rosterSnapshot, lastRefreshedHandleVersionRef]);
  const runRowsByJobId = useMemo(() => {
    const map = new Map<string, (typeof conversationRuns)[number]>();
    for (const row of conversationRuns) map.set(row.id, row);
    return map;
  }, [conversationRuns]);
  const agentToolCallFor = useCallback(
    (item: ToolItem): AgentToolCall | null => {
      const lookup = toolHandlesRef.current?.lookup(item.attempt, item.callId) ?? { kind: "unknown" as const };
      if (lookup.kind !== "handle") return null;
      const attempt = conv.attempts.find((candidate) => candidate.generation === item.attempt);
      const run = attempt
        ? runSummaryFromAttempt(attempt.outcome, attempt.jobId ? runRowsByJobId.get(attempt.jobId) : undefined)
        : null;
      const confirm = [...conv.items]
        .reverse()
        .find(
          (candidate): candidate is ConfirmItem =>
            candidate.role === "confirm" &&
            candidate.callId === item.callId &&
            (item.attempt === null || candidate.attempt === item.attempt),
        );
      return buildAgentToolCall(
        item,
        lookup.handle,
        rosterByHandle.get(lookup.handle),
        run,
        approvalFromConfirmCard(confirm),
      );
    },
    [toolHandlesRef, conv.attempts, conv.items, runRowsByJobId, rosterByHandle],
  );
  const submitAgentApproval = useCallback(
    (requestId: string, decision: AgentApprovalDecision) => {
      void resolveAgentApproval(requestId, decision).catch((error: unknown) => {
        pushToast("error", `未能提交审批：${describeError(error)}`);
      });
    },
    [resolveAgentApproval, pushToast],
  );
  const mentionAgentHandle = useCallback((token: string) => {
    setInput((prev) => {
      const base = prev.replace(/\s+$/, "");
      return base === "" ? `${token} ` : `${base} ${token} `;
    });
    inputRef.current?.focus();
  }, [inputRef, setInput]);

  return { setEditing, agentRosterApi, mentionAgentHandle, watchOwner, handleApprovePlan, handleRetryRun, handleEditResend, agentToolCallFor, submitAgentApproval, confirm, answer, editing, editSubmitting, submitEditResend, steer, send };
}

export type UseChatActionsOut = ReturnType<typeof useChatActions>;
