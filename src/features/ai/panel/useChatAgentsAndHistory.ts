import { useCallback, useEffect, useRef } from "react";
import type { AgentRosterEntry } from "../agentTypes";
import { useUi, nextTabId, type TakeoverState } from "../../../app/store";
import { terminalApi, aiApi, modelApi } from "../../../ipc/commands";
import { describeError } from "../../../ui/errorText";
import { aiTabBusyIds, aiTabRunControls, setAiTabBusy, clearAiTabRun, aiRunBlocksStart, type AiRunSlot, isCurrentAiRun, bindAiRunJob, createAiRun, registerAiRunGeneration, finishAiRunSpawn, settleAiRun } from "../runOwnership";
import { RUN_RESTORE_LIMIT } from "../runRestore";
import type { AiRunDto } from "../../../ipc/types";
import { ask, promptText, askChoice } from "../../../ui/dialogs";
import { withTakeoverPaused, registerTakeoverResumeHandler, takeoverPaused } from "../../../app/TakeoverBanner";
import { createAiChannel, disposeChannel } from "../../../ipc/events";
import { runStatusOf, type SlashItem, type RefChip, readAsDataUrl, type AiChatBase } from "./aiSidebarShared";
import type { UseChatRunSessionOut } from "./useChatRunSession";
import type { UseChatPanelStateOut } from "./useChatPanelState";
import type { UseChatRunRecoveryOut } from "./useChatRunRecovery";
import type { UseChatActionsOut } from "./useChatActions";

/** Image files on a paste, deduplicated across the items and files views. */
export function clipboardImageFiles(data: DataTransfer): File[] {
  const files = Array.from(data.items)
    .filter((it) => it.kind === "file" && it.type.startsWith("image/"))
    .map((it) => it.getAsFile())
    .filter((f): f is File => f !== null);
  for (const file of Array.from(data.files)) {
    if (!file.type.startsWith("image/")) continue;
    if (files.some((f) => f.name === file.name && f.size === file.size && f.type === file.type)) continue;
    files.push(file);
  }
  return files;
}

export function useChatAgentsAndHistory(p: AiChatBase & UseChatRunSessionOut & UseChatPanelStateOut & UseChatRunRecoveryOut & UseChatActionsOut) {
  const { pushToast, requestCancellation, stopRef, tabKey, runControl, cancelWaitersRef, activeRunRef, cancelOperationRef, setRunStatusByConv, setHistoryStatus, setHistoryError, setConversations, conversationIdRef, onTitleChange, historyOpen, setHistoryOpen, busy, restoreGenerationRef, isRestoreActive, updateConversationId, resetMessageHistory, setConversationError, setEditing, restoreConversationRuns, clearMessageHistory, setConversationRuns, stream, isLifecycleActive, deletedConversationIdsRef, setInput, setAtOpen, inputRef, setCompacting, restoreConversation, planMode, setPlanMode, compacting, setPermOpen, slashQuery, modelQuery, modelOptions, skillOptions, setRefs, setImages, pushAgentEvent, settleRestoredRun, runChannelRef, tabId, activeRun, takeoverState, active, runSequenceRef, setActiveRun, setBusy, anyAiBusy, beginRun, driveCancellation, bindings, reclaimKeyLabel, unconfirmCancellation } = p;

  const openAgentTerminal = useCallback(
    (tabId: string, entry: AgentRosterEntry) => {
      const state = useUi.getState();
      for (const workspace of state.workspaces) {
        for (const pane of workspace.panes) {
          const existing = pane.tabs.find((tab) => tab.tabId === tabId);
          if (existing) {
            state.setActiveWorkspace(workspace.id);
            state.setActivePane(pane.id, workspace.id);
            state.setActiveTab(existing.id);
            return;
          }
        }
      }
      // No live view yet: resolve the owning session through the real backend
      // tab lists (live first, then durable per session), then attach a
      // terminal tab bound to that durable backend tab — never a new shell.
      void (async () => {
        try {
          const live = await terminalApi.listLive();
          const liveMatches = live.filter((tab) => tab.tabId === tabId);
          if (liveMatches.length > 1) {
            // Fail closed: ambiguous backend ownership never picks arbitrarily.
            pushToast("error", `终端 ${entry.handle} 匹配到多个会话，无法确定要打开哪个，请手动打开对应会话`);
            return;
          }
          let sessionId = liveMatches[0]?.sessionId;
          if (sessionId === undefined) {
            // Probe durable tabs only on sessions that can answer (connected,
            // durable-capable ssh/device), tolerating individual failures so
            // one broken session never masks another session's hit.
            const candidates = state.sessions.filter(
              (session) =>
                session.status === "connected" && (session.kind === "ssh" || session.kind === "device"),
            );
            const probed = await Promise.allSettled(
              candidates.map(async (session) => {
                const rows = await terminalApi.listDurable(session.id);
                return rows.some((row) => row.id === tabId) ? session.id : undefined;
              }),
            );
            const hits = probed
              .filter((result): result is PromiseFulfilledResult<string | undefined> => result.status === "fulfilled")
              .map((result) => result.value)
              .filter((id): id is string => id !== undefined);
            const failures = probed.length - probed.filter((result) => result.status === "fulfilled").length;
            if (hits.length > 1) {
              pushToast("error", `终端 ${entry.handle} 匹配到多个会话，无法确定要打开哪个，请手动打开对应会话`);
              return;
            }
            if (hits.length === 0) {
              pushToast(
                "error",
                failures > 0
                  ? `无法定位子代理 ${entry.handle} 的终端会话：已搜索的会话均无匹配，另有 ${failures} 个会话搜索失败`
                  : `无法定位子代理 ${entry.handle} 的终端会话（可能已断开或退出），请刷新子代理列表`,
              );
              return;
            }
            sessionId = hits[0];
          }
          const session = state.sessions.find((item) => item.id === sessionId);
          if (!session) {
            pushToast("error", `当前会话列表中无终端 ${entry.handle}，请刷新后重试`);
            return;
          }
          const workspaceId = state.ensureWorkspace({
            kind: "session",
            sessionId: session.id,
            assetId: session.assetId ?? undefined,
            title: session.name,
            assetKind: session.kind,
          });
          state.setActiveWorkspace(workspaceId);
          state.addTab({
            id: nextTabId(`term-${session.id}`),
            kind: "terminal",
            title: entry.handle,
            sessionId: session.id,
            tabId,
            closable: true,
          });
        } catch (e) {
          pushToast("error", `未能打开终端：${describeError(e)}`);
        }
      })();
    },
    [pushToast],
  );

  const stop = () => {
    void requestCancellation(false);
  };
  stopRef.current = requestCancellation;
  useEffect(() => {
    if (!aiTabBusyIds.has(tabKey) || !aiTabRunControls.has(tabKey)) {
      aiTabRunControls.set(tabKey, runControl);
    }
    const cancelWaiters = cancelWaitersRef.current;
    return () => {
      if (aiTabRunControls.get(tabKey) !== runControl) return;
      // eslint-disable-next-line react-hooks/exhaustive-deps
      if (activeRunRef.current && !activeRunRef.current.settled) return;
      setAiTabBusy(tabKey, false);
      clearAiTabRun(tabKey);
      for (const waiter of cancelWaiters) {
        clearTimeout(waiter.timer);
        waiter.resolve(false);
      }
      cancelWaiters.clear();
      cancelOperationRef.current = null;
    };
  }, [tabKey, runControl, cancelWaitersRef, activeRunRef, cancelOperationRef]);

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
      const currentId = conversationIdRef.current;
      const current = currentId ? list.find((c) => c.id === currentId) : undefined;
      if (current?.title) onTitleChange(current.title);
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

  const openConversation = async (id: string, title?: string) => {
    if (busy || aiRunBlocksStart(activeRunRef.current)) {
      pushToast("info", "AI 正在回答，请先停止当前回答再切换会话");
      return;
    }
    const restoreGeneration = ++restoreGenerationRef.current;
    const generation = activeRunRef.current?.generation ?? null;
    try {
      const [msgs, runs] = await Promise.all([
        aiApi.messages(id),
        aiApi
          .runs(id, RUN_RESTORE_LIMIT)
          .then((rows) => rows.slice(0, RUN_RESTORE_LIMIT))
          .catch((): AiRunDto[] => []),
      ]);
      if (
        !isRestoreActive(restoreGeneration) ||
        aiRunBlocksStart(activeRunRef.current) ||
        (activeRunRef.current?.generation ?? null) !== generation
      ) {
        return;
      }
      updateConversationId(id, title);
      resetMessageHistory(id, msgs, runs);
      setConversationError(null);
      setEditing(null);
      setHistoryOpen(false);
      void restoreConversationRuns(id, restoreGeneration, runs);
    } catch (e) {
      if (isRestoreActive(restoreGeneration)) {
        pushToast("error", `未能打开会话：${describeError(e)}`);
      }
    }
  };

  const newConversation = () => {
    if (busy || aiRunBlocksStart(activeRunRef.current)) {
      pushToast("info", "AI 正在回答，请先停止当前回答再新建会话");
      return;
    }
    restoreGenerationRef.current += 1;
    updateConversationId(undefined);
    clearMessageHistory();
    setConversationRuns([]);
    setConversationError(null);
    setEditing(null);
    stream.reset();
    setHistoryOpen(false);
  };

  const deleteConversation = async (c: { id: string; title: string }) => {
    const blocksReset = (id: string) =>
      id === conversationIdRef.current && aiRunBlocksStart(activeRunRef.current);
    if (blocksReset(c.id)) {
      pushToast("info", "AI 正在回答，请先停止当前回答再删除当前会话");
      return;
    }
    try {
      const runs = await aiApi.runs(c.id, 20);
      const inFlight = runStatusOf(Array.isArray(runs) ? runs : []);
      if (inFlight) {
        pushToast(
          "info",
          inFlight === "running"
            ? "此会话中 AI 正在工作，请等待结束或先停止，再删除"
            : "此会话中有被中断的 AI 任务，请先打开会话处理，再删除",
        );
        return;
      }
    } catch {
    }
    const ok = await ask(`确定要删除会话「${c.title || "（未命名会话）"}」吗？消息记录、运行记录和定时任务将一并清除，不可恢复。`, {
      kind: "warning",
    });
    if (!ok) return;
    if (blocksReset(c.id)) {
      pushToast("info", "AI 正在回答，请先停止当前回答再删除当前会话");
      return;
    }
    try {
      await aiApi.conversationDelete(c.id);
    } catch (e) {
      pushToast("error", `未能删除会话：${describeError(e)}`);
      return;
    }
    setConversations((prev) => prev.filter((it) => it.id !== c.id));
    setRunStatusByConv((prev) => {
      const next = { ...prev };
      delete next[c.id];
      return next;
    });
    if (isLifecycleActive() && conversationIdRef.current === c.id) {
      deletedConversationIdsRef.current.add(c.id);
      restoreGenerationRef.current += 1;
      updateConversationId(undefined);
      clearMessageHistory();
      setConversationError(null);
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

  const pickSkill = (name: string) => {
    setInput(`/${name} `);
    inputRef.current?.focus();
  };

  const compactConversation = async () => {
    const id = conversationIdRef.current;
    if (!id) {
      pushToast("info", "当前无对话内容，无需压缩");
      return;
    }
    if (busy) {
      pushToast("info", "AI 正在回答，请在回答结束后压缩");
      return;
    }
    setCompacting(true);
    try {
      const result = await aiApi.compactConversation(id);
      pushToast("success", `已将 ${result.messages} 条消息压缩为摘要，后续回答将基于该摘要`);
      await restoreConversation(id);
    } catch (e) {
      pushToast("error", `未能压缩对话：${describeError(e)}`);
    } finally {
      setCompacting(false);
    }
  };

  const switchModel = async (profile: { id: string; name: string }) => {
    setInput("");
    try {
      await modelApi.activate(profile.id);
      useUi.getState().bumpModelProfilesRevision();
      pushToast("success", `已切换模型：${profile.name}`);
    } catch (e) {
      pushToast("error", `未能切换模型：${describeError(e)}`);
    }
  };

  const slashCommands: SlashItem[] = [
    { key: "new", label: "/new", hint: "新建对话", run: () => { setInput(""); newConversation(); } },
    {
      key: "plan",
      label: "/plan",
      hint: planMode ? "关闭计划模式" : "开启计划模式：先提供方案，确认后再执行",
      run: () => {
        setInput("");
        setPlanMode((value) => !value);
        pushToast("info", planMode ? "已关闭计划模式" : "已开启计划模式");
      },
    },
    { key: "model", label: "/model", hint: "切换模型", run: () => setInput("/model ") },
    {
      key: "compact",
      label: "/compact",
      hint: compacting ? "正在压缩…" : "将当前对话压缩为摘要，节省上下文空间",
      run: () => {
        setInput("");
        void compactConversation();
      },
    },
    { key: "permissions", label: "/permissions", hint: "打开 AI 权限与免确认授权", run: () => { setInput(""); setPermOpen(true); } },
  ];
  const slashMenuOpen = slashQuery !== null || modelQuery !== null;
  const slashItems: SlashItem[] =
    modelQuery !== null
      ? (modelOptions ?? [])
          .filter((profile) => `${profile.name} ${profile.model}`.toLowerCase().includes(modelQuery.trim().toLowerCase()))
          .map((profile) => ({
            key: `model:${profile.id}`,
            label: profile.name,
            hint: profile.active ? `${profile.model} · 当前` : profile.model,
            run: () => void switchModel(profile),
          }))
      : slashQuery !== null
        ? [
            ...slashCommands.filter((command) => command.key.startsWith(slashQuery)),
            ...(skillOptions ?? [])
              .filter((skill) => skill.name.includes(slashQuery))
              .map((skill) => ({ key: `skill:${skill.name}`, label: `/${skill.name}`, hint: skill.description, run: () => pickSkill(skill.name) })),
          ].slice(0, 10)
        : [];
  const slashEmptyText =
    modelQuery !== null
      ? modelOptions === null
        ? "正在读取模型…"
        : modelOptions.length === 0
          ? "尚未配置模型"
          : "无匹配的模型"
      : "无匹配的命令或技能";

  const pickRef = (ref: RefChip) => {
    setRefs((prev) => (prev.some((r) => r.id === ref.id) ? prev : [...prev, ref]));
    setInput((prev) => prev.replace(/@[^@\s]*$/, ""));
    setAtOpen(false);
    inputRef.current?.focus();
  };

  const appendImages = (files: File[]) => {
    if (files.length === 0) return;
    void Promise.all(files.map(readAsDataUrl))
      .then((urls) => setImages((prev) => [...prev, ...urls]))
      .catch((err) => pushToast("error", describeError(err)));
  };

  const onPaste = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    const files = clipboardImageFiles(e.clipboardData);
    if (files.length === 0) return;
    e.preventDefault();
    appendImages(files);
  };

  const onPickImages = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? []).filter((f) => f.type.startsWith("image/"));
    e.target.value = "";
    appendImages(files);
  };

  const takeoverPausedRef = useRef(false);
  const resumeTakeoverRef = useRef<(generation: number, jobId: string) => Promise<void>>(async () => undefined);

  const updateTakeoverPaused = (jobId: string | null, paused: boolean) => {
    takeoverPausedRef.current = paused;
    if (!jobId) return;
    const current = useUi.getState().takeover;
    if (current?.jobId === jobId) useUi.getState().setTakeover(withTakeoverPaused(current, paused));
  };

  const clearTakeoverForJob = (jobId: string) => {
    const current = useUi.getState().takeover;
    if (current?.jobId === jobId) useUi.getState().setTakeover(null);
  };

  const createTakeoverChannel = (run: AiRunSlot, onTerminal: () => void) => {
    let channelDisposed = false;
    const emit = (ev: Record<string, unknown>) => {
      if (channelDisposed || !isLifecycleActive()) return;
      const type = ev.type as string;
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, run.generation) || (current.settled && current.cancellation === "idle")) {
        if (type === "done" || type === "error" || type === "canceled") dispose();
        return;
      }
      const result = pushAgentEvent(run.generation, ev);
      if (result.accepted && type === "paused") {
        updateTakeoverPaused(activeRunRef.current?.jobId ?? run.jobId, true);
      } else if (result.accepted && type === "resumed") {
        updateTakeoverPaused(activeRunRef.current?.jobId ?? run.jobId, false);
      }
      if (!result.terminal) return;
      takeoverPausedRef.current = false;
      onTerminal();
      settleRestoredRun(run.generation);
      if (type === "error" && result.accepted) pushToast("error", `终端接管出错：${ev.message as string}`);
      dispose();
    };
    const channel = createAiChannel(emit);
    const dispose = () => {
      if (channelDisposed) return;
      channelDisposed = true;
      if (runChannelRef.current?.channel === channel) runChannelRef.current = null;
      disposeChannel(channel);
    };
    return { channel, dispose, emit };
  };

  const resumeTakeoverRun = async (generation: number, jobId: string) => {
    const current = activeRunRef.current;
    const banner = useUi.getState().takeover;
    if (
      !isLifecycleActive() ||
      !isCurrentAiRun(current, generation) ||
      current.kind !== "takeover" ||
      current.jobId !== jobId ||
      current.settled ||
      !banner ||
      banner.jobId !== jobId ||
      banner.tabId !== tabId
    ) {
      throw new Error("接管任务已不在当前会话，请重新打开对应终端后重试");
    }
    const next = createTakeoverChannel(current, () => clearTakeoverForJob(jobId));
    try {
      const result = await aiApi.takeoverResume({
        tabId: banner.tabId,
        token: banner.token,
        jobId,
        channel: next.channel,
      });
      if (result.jobId !== jobId) throw new Error("接管恢复失败：返回的任务与当前任务不一致");
      const latest = activeRunRef.current;
      if (!isLifecycleActive() || !isCurrentAiRun(latest, generation) || latest.settled) {
        next.dispose();
        return;
      }
      const previous = runChannelRef.current;
      runChannelRef.current = { channel: next.channel, dispose: next.dispose, generation };
      previous?.dispose();
      stream.bindJob(generation, jobId);
      if (takeoverPausedRef.current) next.emit({ type: "resumed" });
      const latestBanner = useUi.getState().takeover;
      if (latestBanner?.jobId === jobId) {
        useUi.getState().setTakeover(
          withTakeoverPaused({ ...latestBanner, token: result.token }, false),
        );
      }
    } catch (e) {
      next.dispose();
      throw e;
    }
  };
  resumeTakeoverRef.current = resumeTakeoverRun;

  const activeRunGeneration = activeRun?.generation;
  const activeRunKind = activeRun?.kind;
  const activeRunJobId = activeRun?.jobId;
  const activeRunSettled = activeRun?.settled;
  useEffect(() => {
    if (activeRunKind !== "takeover" || !activeRunJobId || activeRunSettled) return;
    return registerTakeoverResumeHandler(
      activeRunJobId,
      () => resumeTakeoverRef.current(activeRunGeneration ?? 0, activeRunJobId),
    );
  }, [activeRunGeneration, activeRunKind, activeRunJobId, activeRunSettled]);

  useEffect(() => {
    const restored = takeoverState;
    if (
      !active ||
      !tabId ||
      !restored?.jobId ||
      restored.tabId !== tabId ||
      !takeoverPaused(restored) ||
      activeRunRef.current
    ) {
      return;
    }
    restoreGenerationRef.current += 1;
    const generation = ++runSequenceRef.current;
    const run = bindAiRunJob(createAiRun(generation, "takeover"), restored.jobId);
    registerAiRunGeneration(tabKey, generation);
    aiTabRunControls.set(tabKey, runControl);
    setActiveRun(run);
    stream.beginRun(generation, "takeover");
    stream.bindJob(generation, restored.jobId);
    stream.resumeFromNextEvent(generation);
    takeoverPausedRef.current = true;
    setBusy(true);
  }, [active, activeRunRef, restoreGenerationRef, runControl, runSequenceRef, setActiveRun, setBusy, stream, tabId, tabKey, takeoverState]);

  const runTakeover = async () => {
    if (!tabId) {
      pushToast("error", "终端接管需要先打开一个终端标签页");
      return;
    }
    const instruction = await promptText(
      "终端接管任务（例如：安装并启动 nginx）",
      "",
      { multiLine: true },
    );
    if (!instruction || !isLifecycleActive()) return;
    const choice = await askChoice("确定要允许 AI 直接操作此终端吗？", {
      title: "终端接管（实验性功能）",
      level: "warning",
      choices: [
        { key: "allow", label: "允许", hint: "AI 可以执行命令", primary: true },
        { key: "readonly", label: "只读", hint: "AI 只能查看，不能执行命令" },
        { key: "abort", label: "取消", hint: "不启动接管", danger: true },
      ],
    });
    if ((choice !== "allow" && choice !== "readonly") || !isLifecycleActive()) return;
    const allowWrite = choice === "allow";

    if (anyAiBusy || aiRunBlocksStart(activeRunRef.current)) {
      pushToast("info", "AI 正在回答，请先停止当前回答再接管终端");
      return;
    }
    takeoverPausedRef.current = false;
    const run = beginRun("takeover");
    const holder = { settled: false };
    let banner: TakeoverState | null = null;
    const clearTakeover = () => {
      if (banner && useUi.getState().takeover?.jobId === banner.jobId) useUi.getState().setTakeover(null);
    };
    const { channel, dispose } = createTakeoverChannel(run, () => {
      holder.settled = true;
      clearTakeover();
    });
    runChannelRef.current = { channel, dispose, generation: run.generation };

    setBusy(true);
    let ownershipToken: string | undefined;
    try {
      ownershipToken = (await aiApi.takeoverEnter(tabId)).token;
      if (!isLifecycleActive() || !isCurrentAiRun(activeRunRef.current, run.generation)) {
        await aiApi.takeoverExit(tabId, ownershipToken, "接管页面已关闭").catch(() => undefined);
        dispose();
        return;
      }
      const result = await aiApi.takeoverRun({
        tabId,
        token: ownershipToken,
        instruction,
        allowWrite,
        channel,
      });
      ownershipToken = result.token;
      const current = activeRunRef.current;
      if (!isLifecycleActive() || !isCurrentAiRun(current, run.generation)) {
        dispose();
        return;
      }
      if ((current.settled && current.cancellation === "idle") || holder.settled) {
        setActiveRun(finishAiRunSpawn(current));
        setBusy(false);
        return;
      }
      setActiveRun(bindAiRunJob(current, result.jobId));
      stream.bindJob(run.generation, result.jobId);
      void driveCancellation();
      banner = {
        tabId,
        jobId: result.jobId,
        token: result.token,
        task: instruction,
        allowWrite,
        startedAt: Date.now(),
      };
      if (takeoverPausedRef.current) banner = withTakeoverPaused(banner, true);
      useUi.getState().setTakeover(banner);
      pushToast(
        "info",
        bindings.reclaimTakeover
          ? `接管已启动，可通过顶部横幅或 ${reclaimKeyLabel} 收回控制权。`
          : "接管已启动，可通过顶部横幅收回控制权。",
      );
    } catch (e) {
      if (ownershipToken) {
        await aiApi.takeoverExit(tabId, ownershipToken, "接管启动失败").catch(() => undefined);
      }
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
        clearTakeover();
      }
      dispose();
    }
  };

  return { toggleHistory, newConversation, loadConversations, openConversation, deleteConversation, openAgentTerminal, stop, slashMenuOpen, slashItems, slashEmptyText, pickRef, onInputChange, onPaste, onPickImages, runTakeover };
}

export type UseChatAgentsAndHistoryOut = ReturnType<typeof useChatAgentsAndHistory>;
