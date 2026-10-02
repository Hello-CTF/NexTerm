// AI 侧栏：对话 + 流式事件 + 工具调用卡片 + 确认卡片 + 接管 + 历史会话。
//
// 形态：顶栏（历史 / 新建）、消息流、底部输入区
// （大输入框 + 功能行 + 圆形发送）。**AI 接管终端**是我们独有的能力，完整保留。
//
// 四条自己定的规矩：
//   · 权限是**档位**（只读/读写/静默），不是"一个静默开关" —— 见 §零 的决策表；
//   · 危险命令在任何档位都要问，硬底线在任何档位都不执行；
//   · 图片走 data URI 内联，截图不出本机，也只在当次请求里存在（不落库）；
//   · @ 引用是给模型的**显式目标**，不改内核 scope —— scope 仍由当前会话决定。
//
// 流式路径的分层（本文件只管副作用与渲染）：
//   · conversation.ts —— 事件 → 结构化会话（条目 / attempt / 终态），重放幂等；
//   · conversationStream.ts —— 逐 token 文本按帧合并，工具 / 交互 / 终态前强制 flush；
//   · conversationFollow.ts —— 只有用户还在最新输出时才自动滚动。
import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { ask, promptText } from "../../ui/dialogs";
import { aiApi, type AiPermissionConfig, type AiPermissionMode } from "../../ipc/commands";
import { createAiChannel, disposeChannel, onChannelReopen } from "../../ipc/events";
import { useUi, type TakeoverState } from "../../app/store";
import { describeError } from "../../ui/errorText";
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
} from "./conversation";
import { createConversationStream, type ConversationStream } from "./conversationStream";
import { useConversationFollow } from "./conversationFollow";
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
  IconChevronRight,
  IconClose,
  IconEdit,
  IconHistory,
  IconImage,
  IconList,
  IconLoader,
  IconMonitor,
  IconPlay,
  IconPlus,
  IconShield,
  IconXCircle,
} from "../../ui/icons";

/** @ 引用选出来的一枚 chip。 */
interface RefChip {
  id: string;
  kind: "asset" | "tab";
  label: string;
  detail: string;
}

/**
 * 三档权限的展示文案。
 *
 * 说明文字写"会发生什么"而不是"叫什么" —— 用户选档位时要判断的是
 * 「AI 接下来会不会突然动我的机器」，不是记住三个名词。
 */
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

/** 确认决定 → 留在消息流里的结算文案（交互完成后仍然可查）。 */
const CONFIRM_RESOLUTION: Record<"allow" | "allow_session" | "deny", string> = {
  allow: "已允许一次",
  allow_session: "本会话已允许此类",
  deny: "已拒绝",
};

export function AiSidebar({ sessionId, tabId }: { sessionId?: string; tabId?: string }) {
  const { rightOpen, setRightOpen, aiBusy, setAiBusy, pushToast, rightWidth, workspaces } = useUi();
  /**
   * 会话流控制器：组件生命周期内唯一。卸载时只 flush 不 dispose ——
   * StrictMode 的开发态会走一遍"挂载→清理→再挂载"，dispose 是不可逆的，
   * 会把第二段生命周期里的控制器变成哑巴；flush 则两个场景都正确。
   */
  const streamRef = useRef<ConversationStream | null>(null);
  if (!streamRef.current) streamRef.current = createConversationStream();
  const stream = streamRef.current;
  const subscribe = useCallback((onChange: () => void) => stream.subscribe(() => onChange()), [stream]);
  const getSnapshot = useCallback(() => stream.getState(), [stream]);
  const conv = useSyncExternalStore(subscribe, getSnapshot);
  useEffect(() => () => stream.flush(), [stream]);

  const [input, setInput] = useState("");
  const [questionInput, setQuestionInput] = useState("");
  const runSequenceRef = useRef(0);
  const activeRunRef = useRef<AiRunSlot | null>(null);
  const beginRun = (kind: "chat" | "takeover" = "chat"): AiRunSlot => {
    const run = createAiRun(++runSequenceRef.current, kind);
    activeRunRef.current = run;
    stream.beginRun(run.generation, kind);
    return run;
  };
  /** 当前轮的待处理交互：从会话里派生，终态 / 停止 / 切换会话会自动关闭。 */
  const activeGeneration = activeRunRef.current?.generation ?? null;
  const confirmCard = pendingInteraction(conv, activeGeneration, "confirm");
  const questionCard = pendingInteraction(conv, activeGeneration, "question");
  useEffect(() => {
    // 新提问卡 / 卡片关闭都重置草稿；同一张卡的流式重渲染不清空用户输入。
    setQuestionInput("");
  }, [questionCard?.id]);
  /** 计划模式：只调研、出方案，等批准。 */
  const [planMode, setPlanMode] = useState(false);
  const [modelPanelOpen, setModelPanelOpen] = useState(false);
  const [conversationId, setConversationId] = useState<string | undefined>(undefined);
  /** 权限档位（+ 规则数量展示）。规则库本身在设置页，这里只留入口。 */
  const [perm, setPerm] = useState<AiPermissionConfig | null>(null);
  const [permOpen, setPermOpen] = useState(false);
  /** 待发送的图片（data URI）。 */
  const [images, setImages] = useState<string[]>([]);
  /** @ 引用 chip。 */
  const [refs, setRefs] = useState<RefChip[]>([]);
  const [atOpen, setAtOpen] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [conversations, setConversations] = useState<{ id: string; title: string; updatedAt: number }[]>([]);
  /** 接管状态来自全局 store（§8.6）：顶部横幅与这里读的是同一份。 */
  const takeover = useUi((s) => s.takeover);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const follow = useConversationFollow(conv.items);

  useEffect(() => {
    void aiApi
      .getPermission()
      .then(setPerm)
      .catch(() => undefined);
  }, []);

  // 规则库挪去设置页之后，档位和规则可能在那里被改 ——
  // 浮层每次打开都拉最新，别拿旧数据覆盖（切档位是整包保存）。
  useEffect(() => {
    if (permOpen) {
      void aiApi
        .getPermission()
        .then(setPerm)
        .catch(() => undefined);
    }
  }, [permOpen]);

  /** 改权限：先落 UI 再写库（开关的手感不能等一次 IPC 往返），写失败再回滚提示。 */
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

  /** 切档位：先取最新配置再改 mode —— dangerRules 归设置页管，不能拿旧列表整包覆盖。 */
  const switchMode = (mode: AiPermissionConfig["mode"]) => {
    void aiApi
      .getPermission()
      .then((latest) => savePerm({ ...latest, mode }))
      .catch(() => savePerm({ ...(perm as AiPermissionConfig), mode }));
  };

  /**
   * 可引用的对象：当前会话的资产 + 工作区里所有终端标签。
   *
   * 刻意不列**别的**资产的连接信息 —— 内核的 AiScope 是当前会话单点，
   * 列一个 AI 根本够不着的目标只会让它答非所问。
   */
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

  /** 把引用折进消息文本：它们是给模型的显式目标，不是内核 scope 的一部分。 */
  const composeMessage = (text: string): string => {
    if (refs.length === 0) return text;
    const list = refs.map((r) => `- ${r.label}（${r.detail}）`).join("\n");
    return `[引用对象]\n${list}\n\n${text}`;
  };

  const send = async (override?: { message?: string; planMode?: boolean }) => {
    const message = (override?.message ?? input).trim();
    if ((!message && images.length === 0) || aiBusy || aiRunBlocksStart(activeRunRef.current)) return;
    const run = beginRun();
    // 计划模式可以按次覆盖：「批准并执行」那一下必须走普通模式，
    // 否则模型会再给你一份计划 —— 用户点的是"执行"，不是"再想想"。
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
      const terminalEvent = type === "done" || type === "error";
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, run.generation) || current.settled) {
        if (terminalEvent) disposeChannel(channel);
        return;
      }
      // 聚合层负责幂等与可见终态；这里只保留副作用（ownership / toast / 通道释放）。
      const result = stream.pushEvent(run.generation, ev);
      if (!terminalEvent) return;
      if (result.accepted) {
        activeRunRef.current = settleAiRun(current);
        if (!current.spawnPending) setAiBusy(false);
        if (type === "error") pushToast("error", `AI: ${ev.message as string}`);
      }
      // 终态到达 ⇒ 这次作业彻底结束，释放本次作业专用的通道（重复 / 迟到终态也一样，
      // dispose 幂等）。不释放的话它对应的 WS 会一直挂着退避重连。
      disposeChannel(channel);
    });
    // WS 重连 ⇒ 服务端会补发缓存帧：开启本轮的文本重放抑制。
    // Wails / demo 没有这一生命周期，这里是空操作；通道 dispose 时订阅随 id 一并清除。
    onChannelReopen(channel, () => stream.notifyReopen(run.generation));

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
        disposeChannel(channel);
        return;
      }
      // 即使终态早于 RPC 返回，也必须续用内核会话 id，否则下一轮会另开新会话。
      setConversationId(res.conversationId);
      if (current.settled) {
        activeRunRef.current = finishAiRunSpawn(current);
        setAiBusy(false);
        return;
      }
      activeRunRef.current = bindAiRunJob(current, res.jobId);
      stream.bindJob(run.generation, res.jobId);
    } catch (e) {
      const current = activeRunRef.current;
      if (isCurrentAiRun(current, run.generation)) {
        // 命令级失败（未拿到 jobId）时服务端不会推任何终态事件：补一条本地 error，
        // 失败的那一轮在消息流里同样留痕可查；已终态（如早到的 done）则不改写。
        const result = stream.pushEvent(run.generation, {
          type: "error",
          message: describeError(e),
        });
        if (result.accepted && !current.settled) pushToast("error", describeError(e));
        activeRunRef.current = settleAiRun(finishAiRunSpawn(current));
        setAiBusy(false);
      }
      disposeChannel(channel);
    }
  };

  /**
   * 批准计划：关掉计划模式，把方案原样回灌一条消息重新发起。
   *
   * 不复用 exit_plan_mode 那一轮的 job —— 那一轮已经结束了；而且新的一轮本该
   * 就是「计划模式关掉」的状态。让模型在同一个 job 里接着跑，等于把「用户点头」
   * 这个动作本身绕过去了。
   */
  const approvePlan = (plan: string) => {
    setPlanMode(false);
    void send({ message: `按上面的方案执行。\n\n方案原文：\n${plan}`, planMode: false });
  };

  const confirm = async (decision: "allow" | "allow_session" | "deny") => {
    const run = activeRunRef.current;
    const card = confirmCard;
    const id = run?.jobId;
    if (!run || !id || !card) {
      pushToast("info", "这一轮还没启动完，稍等一下再点");
      return;
    }
    try {
      await aiApi.confirm(
        confirmationInput({ jobId: id, callId: card.callId, nonce: card.nonce }, decision),
      );
    } catch (e) {
      pushToast("error", `确认失败：${describeError(e)}`);
      return;
    }
    if (isCurrentAiRun(activeRunRef.current, run.generation)) {
      // 只结算这张卡（id + nonce 双重要件）：RPC 等待期间到来的新交互不受影响。
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
    try {
      await aiApi.answer(answerInput({ jobId: id, callId: card.callId, nonce: card.nonce }, text));
    } catch (e) {
      pushToast("error", `回答失败：${describeError(e)}`);
      return;
    }
    if (isCurrentAiRun(activeRunRef.current, run.generation)) {
      stream.resolveInteraction(run.generation, card.id, card.nonce, `已回答：${text}`);
    }
  };

  /** chat 取消成功即可收尾；takeover 必须等终端终态清横幅，不能提前解锁。 */
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
      pushToast(
        "info",
        cancellation.waitForTerminal ? "已请求停止接管，等待终端退出" : "已停止本轮",
      );
    } catch (e) {
      pushToast("error", `停止失败：${describeError(e)}`);
    }
  };

  /* ── 历史会话 ──────────────────────────────────────────────────────── */

  const loadConversations = async () => {
    try {
      const list = await aiApi.conversationList();
      setConversations(
        list.map((c) => ({ id: c.id, title: c.title, updatedAt: c.updatedAt })),
      );
    } catch (e) {
      pushToast("error", `读取历史会话失败：${describeError(e)}`);
    }
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
      const msgs = await aiApi.messages(id);
      if (
        aiRunBlocksStart(activeRunRef.current) ||
        (activeRunRef.current?.generation ?? null) !== generation
      ) {
        return;
      }
      stream.reset(historyToItems(stream.getState(), msgs));
      setConversationId(id);
      setHistoryOpen(false);
    } catch (e) {
      pushToast("error", `打开会话失败：${describeError(e)}`);
    }
  };

  const newConversation = () => {
    if (aiBusy || aiRunBlocksStart(activeRunRef.current)) {
      pushToast("info", "这一轮仍在运行，请先停止再新建会话");
      return;
    }
    setConversationId(undefined);
    stream.reset();
    setHistoryOpen(false);
  };

  /* ── 输入区：@ 引用 / 粘贴图片 ─────────────────────────────────────── */

  const onInputChange = (v: string) => {
    setInput(v);
    // 刚打出一个 `@`（词首）→ 弹引用列表；已开着就一直跟着过滤
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

  /** 粘贴截图 → 附件列表。纯文本粘贴原样放行（不 preventDefault）。 */
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
    // 接管任务通常是好几行的步骤描述（先装什么、再改哪个配置、最后重启什么），
    // 单行输入框写不下 —— 显式要求多行。
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
      const terminalEvent = type === "done" || type === "error";
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, run.generation) || current.settled) {
        if (terminalEvent) disposeChannel(channel);
        return;
      }
      const result = stream.pushEvent(run.generation, ev);
      if (!terminalEvent) return;
      if (result.accepted) {
        holder.settled = true;
        activeRunRef.current = settleAiRun(current);
        if (!current.spawnPending) setAiBusy(false);
        clearTakeover();
        if (type === "error") pushToast("error", `接管：${ev.message as string}`);
      }
      // 接管的终态。注意「Esc 夺回」也走这里 —— 服务端 `run_takeover` 在取消
      // 分支 break 之后仍然会补推一条 `Done`，所以取消路径不需要另找释放点。
      // 此刻之后该通道再无任何事件，可安全关闭（重复 / 迟到终态同样幂等释放）。
      disposeChannel(channel);
    });
    // 与 chat 相同：重连补帧时开启本轮的文本重放抑制。
    onChannelReopen(channel, () => stream.notifyReopen(run.generation));

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
      pushToast("info", "接管已启动 —— 顶部横幅可随时夺回，Esc 亦可");
    } catch (e) {
      if (ownershipToken) {
        await aiApi.takeoverExit(tabId, ownershipToken, "接管启动失败").catch(() => undefined);
      }
      const current = activeRunRef.current;
      if (isCurrentAiRun(current, run.generation)) {
        // 命令级失败：服务端若在 `begin`/建客户端阶段就退出，一个事件都不会推，
        // 与 chat 一样补一条本地 error 留痕。
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
      className="flex h-full shrink-0 flex-col border-l border-neutral-800/60 bg-neutral-950"
      style={{ width: rightWidth }}
    >
      {/* 顶栏：历史会话 / 新建会话 */}
      <div className="flex h-[38px] shrink-0 items-center gap-1 border-b border-neutral-800/60 px-2 pl-3">
        <span className="text-[12.5px] font-semibold text-neutral-100">AI 助手</span>
        {takeover && (
          <span className="nx-badge nx-badge-red" title="终端接管进行中 —— 按 Esc 随时夺回">
            <span className="nx-dot nx-dot-pulse" />
            接管中
          </span>
        )}
        <div className="nx-spacer" />
        <button
          className={`nx-icon-btn nx-icon-btn-sm ${historyOpen ? "is-active" : ""}`}
          title="历史会话"
          onClick={toggleHistory}
        >
          <IconHistory size={14} />
        </button>
        <button className="nx-icon-btn nx-icon-btn-sm" title="新建会话" onClick={newConversation}>
          <IconPlus size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="收起 AI 侧栏 (Ctrl+J)"
          onClick={() => setRightOpen(false)}
        >
          <IconChevronRight size={14} />
        </button>
      </div>

      {/* 历史会话浮层 */}
      {historyOpen && (
        <div className="max-h-[40%] shrink-0 overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60 p-1.5">
          {conversations.length === 0 ? (
            <div className="nx-hint px-2 py-3 text-center text-[11.5px]">还没有历史会话</div>
          ) : (
            conversations.map((c) => (
              <button
                key={c.id}
                className="nx-menu-item w-full"
                title={c.title || "(未命名会话)"}
                onClick={() => void openConversation(c.id)}
              >
                <span className="nx-menu-label">{c.title || "(未命名会话)"}</span>
                <span className="nx-menu-hint">
                  {c.updatedAt ? new Date(c.updatedAt).toLocaleDateString() : ""}
                </span>
              </button>
            ))
          )}
        </div>
      )}

      {/* 权限设置浮层 */}
      {permOpen && perm && (
        <div className="max-h-[55%] shrink-0 overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60 p-2.5">
          <div className="mb-1.5 flex items-center gap-1.5 text-[11.5px] font-medium text-neutral-200">
            <IconShield size={12} />
            AI 权限
            <span className="nx-spacer" />
            <button
              className="nx-icon-btn nx-icon-btn-sm"
              title="关闭"
              onClick={() => setPermOpen(false)}
            >
              <IconClose size={11} />
            </button>
          </div>

          {/* 三档 */}
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

          {/* 工作方式：计划模式单独成组，不混进上面三档 ——
              档位管的是"能不能动手"，计划模式管的是"先不先出方案"，
              混在一起会被当成第四档权限。整行可点 + ●/○ 与三档的呈现保持一致。 */}
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

          {/* 拦截规则：规则库整套在设置页，这里只留状态 + 显式跳转按钮 ——
              侧栏是干活时顺手看档位的地方，不是管理规则的地方。 */}
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

      {/* 消息流：跟随滚动由 useConversationFollow 决定，不再无条件拽到底部 */}
      <div className="relative min-h-0 flex-1">
        <div
          ref={follow.scrollRef}
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
          {conv.items.map((item, i) => (
            <ChatBubble
              key={item.id}
              item={item}
              streaming={aiBusy && i === conv.items.length - 1}
              onApprovePlan={approvePlan}
            />
          ))}
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

      {confirmCard && (
        <div className="shrink-0 border-t border-neutral-800/60 bg-neutral-950 p-2.5">
          <div className="nx-alert">
            <div className="mb-1.5 flex items-center gap-1.5 font-semibold">
              <IconAlert size={13} />
              需要你确认
              <span className="nx-spacer" />
              <span className="nx-badge nx-badge-amber">{confirmCard.tool}</span>
            </div>
            <ConfirmBody card={confirmCard} />
            <div className="mb-2 text-[10.5px] leading-relaxed text-neutral-500">
              不想每次都弹这个？把它加进「自定义危险操作」，或在权限设置里调整档位。
            </div>
            <div className="flex flex-wrap gap-1.5">
              <button className="nx-btn nx-btn-primary nx-btn-xs" onClick={() => void confirm("allow")}>
                允许一次
              </button>
              <button
                className="nx-btn nx-btn-outline nx-btn-xs"
                onClick={() => void confirm("allow_session")}
              >
                本会话允许此类
              </button>
              <button className="nx-btn nx-btn-ghost nx-btn-xs" onClick={() => void confirm("deny")}>
                拒绝
              </button>
              {/* 跳去设置页的规则库，并把这条命令预填成新规则的草稿 ——
                  预填走全局 store：设置页可能还没挂载，等它读到再展开草稿行。 */}
              <button
                className="nx-btn nx-btn-outline nx-btn-xs"
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
        </div>
      )}

      {questionCard && (
        <div className="shrink-0 border-t border-neutral-800/60 bg-neutral-950 p-2.5">
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
                    className="nx-btn nx-btn-outline nx-btn-xs"
                    onClick={() => void answer(option)}
                  >
                    {option}
                  </button>
                ))}
              </div>
            )}
            <div className="flex items-end gap-1.5">
              <textarea
                className="nx-textarea min-h-[36px] flex-1"
                rows={2}
                value={questionInput}
                placeholder="输入回答…"
                aria-label="回答 AI 的问题"
                onChange={(event) => setQuestionInput(event.target.value)}
              />
              <button
                type="submit"
                className="nx-btn nx-btn-primary nx-btn-xs shrink-0"
                disabled={!questionInput.trim()}
              >
                回答
              </button>
            </div>
          </form>
        </div>
      )}

      {/* 任务清单：AI 自己维护的待办。
          钉在输入区上方而不是塞进消息流 —— 它是「现在做到哪了」的常驻视图，
          要滚动上去才能看到的清单等于没做这个功能。 */}
      {conv.todos.length > 0 && (
        <div className="shrink-0 border-t border-neutral-800/60 bg-neutral-900/50 px-2.5 py-2">
          <div className="mb-1 flex items-center gap-1.5 text-[10.5px] text-neutral-400">
            <IconList size={11} />
            任务清单
            <span className="text-neutral-600">
              {conv.todos.filter((t) => t.status === "completed").length}/{conv.todos.length}
            </span>
          </div>
          <div className="flex max-h-28 flex-col gap-0.5 overflow-y-auto">
            {conv.todos.map((t, i) => (
              <div key={i} className="flex items-start gap-1.5 text-[11px] leading-snug">
                <span
                  className={
                    t.status === "completed"
                      ? "text-teal-300"
                      : t.status === "in_progress"
                        ? "text-blue-300"
                        : "text-neutral-600"
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

      {/* 输入区 */}
      <div className="shrink-0 border-t border-neutral-800/60 p-2.5">
        {/* 计划模式开着时必须一直看得见。它已经从功能行搬进权限浮层，
            而浮层一收起来就没人提醒用户"这一轮 AI 只出方案、不动手"了 ——
            忘了它还开着，会以为 AI 变磨叽了。 */}
        {planMode && (
          <div className="mb-1.5 flex items-center gap-1.5 text-[11px] text-blue-300/90">
            <IconList size={10} className="shrink-0" />
            <span className="truncate">计划模式 · 先出方案，你批准了再动手</span>
          </div>
        )}
        {/* 运行状态条：回答「它还在动吗」。phase 来自内核 status / toolArgs 事件。 */}
        {aiBusy && conv.status && (
          <div
            className="mb-1.5 flex items-center gap-1.5 text-[11px] text-neutral-500"
            role="status"
            aria-live="polite"
          >
            <IconLoader size={10} className="animate-spin text-amber-300/80" />
            <span className="truncate">{statusText(conv.status)}</span>
          </div>
        )}
        {/* 引用 chip */}
        {refs.length > 0 && (
          <div className="mb-1.5 flex flex-wrap gap-1">
            {refs.map((r) => (
              <span key={r.id} className="nx-chip nx-chip-accent" title={r.detail}>
                <span className="truncate">{r.label}</span>
                <button
                  className="nx-chip-x"
                  title="移除引用"
                  onClick={() => setRefs((prev) => prev.filter((x) => x.id !== r.id))}
                >
                  <IconClose size={10} />
                </button>
              </span>
            ))}
          </div>
        )}

        {/* 图片附件 */}
        {images.length > 0 && (
          <div className="mb-1.5 flex flex-wrap gap-1.5">
            {images.map((src, i) => (
              <span key={i} className="nx-attach" title="粘贴的图片">
                <img src={src} alt="" />
                <button
                  className="nx-attach-x"
                  title="移除图片"
                  onClick={() => setImages((prev) => prev.filter((_, j) => j !== i))}
                >
                  <IconClose size={10} />
                </button>
              </span>
            ))}
          </div>
        )}

        {/* @ 引用选择 */}
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
          className="nx-textarea max-h-40 min-h-[64px] w-full"
          placeholder={
            sessionId
              ? "向 NexTerm 提问，@ 引用资产或终端标签，可直接粘贴图片"
              : "未连接会话（仍可全局提问）"
          }
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
              e.preventDefault();
              void send();
            }
          }}
        />

        {/* 功能行 */}
        <div className="mt-1.5 flex items-center gap-1">
          <ModelSelector onManage={() => setModelPanelOpen(true)} />
          <div className="nx-spacer" />
          <UsageRing usage={conv.usage} />
          {/* 盾牌不再是"静默开关"，而是权限设置入口（图标刻意不变，位置也不动） */}
          <button
            className={`nx-icon-btn nx-icon-btn-sm ${
              perm?.mode === "silent" ? "is-active" : ""
            } ${permOpen ? "bg-neutral-800 text-neutral-100" : ""}`}
            title={`AI 权限：${MODE_LABEL[perm?.mode ?? "read_write"]}（点击设置）`}
            onClick={() => setPermOpen((v) => !v)}
          >
            <IconShield size={13} />
          </button>
          {/* 接管 = 完全权限，颜色要比旁边任何按钮都重 */}
          <button
            className="nx-icon-btn nx-icon-btn-sm text-red-400 hover:text-red-300"
            title={
              tabId
                ? "终端接管（实验性功能）：AI 直接接手当前终端，随时按 Esc 夺回"
                : "终端接管需要先打开一个终端标签"
            }
            disabled={!tabId || aiBusy}
            onClick={() => void runTakeover()}
          >
            <IconMonitor size={13} />
          </button>
          {/* 运行中这个圆钮就从「发送」变成「停止」——把它放在同一位置，
              是因为用户想中断时的第一反应就是去点那个正在转圈的东西。 */}
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
              title="发送 (Enter)"
              disabled={!input.trim() && images.length === 0}
              onClick={() => void send()}
            >
              <IconSendArrow />
            </button>
          )}
        </div>
      </div>

      {/* 模型配置面板：与权限面板同一套浮层位置，顶部展开、消息流让位 */}
      {modelPanelOpen && <ModelPanel onClose={() => setModelPanelOpen(false)} />}
    </aside>
  );
}

/** 发送按钮里的上箭头（细一号，圆钮里不显笨）。 */
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

/** 停止按钮里的实心方块（停止 = 方块，是播放器/终端里最通用的语汇）。 */
function IconStopSquare() {
  return (
    <svg width="15" height="15" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <rect x="4.5" y="4.5" width="7" height="7" rx="1.5" fill="currentColor" />
    </svg>
  );
}

/** 读图 → data URI（内联进请求，不落盘）。 */
function readAsDataUrl(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const fr = new FileReader();
    fr.onload = () => resolve(String(fr.result));
    fr.onerror = () => reject(new Error("读取图片失败"));
    fr.readAsDataURL(file);
  });
}

/** 运行状态的一句话（把 `AiEvent::Status` 与工具参数进度翻成人话）。 */
function statusText(s: StatusLine): string {
  if (s.phase === "compacting") return s.detail ?? "上下文接近上限，正在压缩早期工具结果…";
  if (s.phase === "thinking") return s.turn ? `第 ${s.turn} 轮 · 正在思考…` : "正在思考…";
  if (s.phase === "tool_args") {
    // 报「已生成多少」而不是百分比：内核给的是参数 JSON 的**字节数**，
    // 而这份 JSON 最终会长到多大没人知道 —— 分母不存在，百分比就是编的。
    // 给一个会动的数字，用户就能判断它是活的，这正是这行字存在的全部意义。
    return `${preparingLabel(s.tool)} · 已生成 ${formatBytes(s.chars ?? 0)}`;
  }
  return s.detail ?? s.phase;
}

/**
 * 「正在准备什么」的人话说法。
 *
 * 单独一张表，而不是拼成 `${tool}…`：工具名是给模型看的标识符
 * （`write_file` / `edit_file`），直接摆给用户看等于让人读代码。
 */
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

/** 字节数的粗略说法。参数是逐 token 长起来的，精确到个位没有意义。 */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} 字节`;
  return `${(n / 1024).toFixed(1)} KB`;
}

/**
 * 从确认卡片的内核渲染文本里抠出一条待编辑的危险规则。
 *
 * 内核渲染一般会把真正要跑的命令单独放在一行（以 `$` 开头），优先取它；
 * 找不到就退回第一行。截到 120 字是因为规则是给人扫一眼的短模式，
 * 把整段上下文塞进去反而看不清要匹配什么。
 */
function ruleFromRendered(rendered: string): string {
  const lines = rendered
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);
  const picked = lines.find((l) => l.startsWith("$")) ?? lines[0] ?? "";
  return picked.slice(0, 120);
}

function ChatBubble({
  item,
  streaming,
  onApprovePlan,
}: {
  item: ChatItem;
  /** 是不是"正在流出的最后一条"：思考过程在此期间保持展开，收尾自动折叠。 */
  streaming: boolean;
  onApprovePlan: (plan: string) => void;
}) {
  if (item.role === "user") {
    return (
      <div className="ml-10 rounded-[10px] rounded-br-[3px] border border-blue-500/25 bg-blue-500/20 px-3 py-2 text-[12.3px] leading-relaxed text-blue-50">
        {item.imageCount ? (
          <div className="mb-1 flex items-center gap-1 text-[11px] text-blue-200/80">
            <IconImage size={11} />
            {item.imageCount} 张图片
          </div>
        ) : null}
        <pre className="font-sans whitespace-pre-wrap">{item.text}</pre>
      </div>
    );
  }
  if (item.role === "assistant") {
    return (
      <div className="mr-2 rounded-[10px] rounded-bl-[3px] border border-neutral-800 bg-neutral-800 px-3 py-2 text-[12.3px] leading-relaxed text-neutral-200">
        <Markdown text={item.text} />
      </div>
    );
  }
  if (item.role === "reasoning") {
    // 旧版把思考截到 200 字且无处可看全文。改成可折叠全文：
    // 流式期间默认展开（看得见"它在想什么"），这一轮结束后收起成一行，
    // 既保留现场又不让大段推理长期霸占消息流。
    return (
      <details
        className="ml-1 border-l-2 border-neutral-700 pl-2.5 text-[11.5px] leading-relaxed text-neutral-500"
        open={streaming}
      >
        <summary className="cursor-pointer select-none text-[10.5px] text-neutral-600 hover:text-neutral-400">
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
    // 一轮的可见终态：失败 / 取消 / 完成都留在消息流里，
    // 流关闭或重连之后仍能回看，不再只是一个转瞬即逝的 toast。
    if (item.outcome === "error") {
      return (
        <div
          role="alert"
          className="flex items-start gap-1.5 rounded-lg border border-red-500/30 bg-red-500/[0.08] px-2.5 py-2 text-[11.5px] leading-relaxed text-red-200"
        >
          <IconAlert size={12} className="mt-0.5 shrink-0 text-red-300" />
          <span className="whitespace-pre-wrap">本轮出错：{item.text}</span>
        </div>
      );
    }
    return (
      <div
        role="status"
        className={`flex items-center justify-center gap-1.5 py-0.5 text-[10.5px] ${
          item.outcome === "canceled" ? "text-amber-300/70" : "text-neutral-600"
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
}

/** 交互在消息流里的留痕：待处理是一行提示，结算后留下决定 / 回答。 */
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
        pending ? "text-amber-300/80" : "text-neutral-600"
      }`}
      title={text}
    >
      {item.role === "confirm" ? <IconShield size={10} className="shrink-0" /> : <IconAlert size={10} className="shrink-0" />}
      <span className="truncate">{text}</span>
    </div>
  );
}

/**
 * 工具调用卡片：折叠态一行摘要，点「展开」看完整输出。
 *
 * 摘要只有 400 字（内核截的），排查问题时基本不够用 —— 一个 `docker ps`
 * 就可能超。所以完整输出也一并推过来了（上限 64K），这里默认收着，
 * 要用再铺开：一次几十屏的文本会把消息流冲散，反而找不到东西。
 */
function ToolBubble({ item }: { item: Extract<ChatItem, { role: "tool" }> }) {
  const [open, setOpen] = useState(false);
  const running = item.summary === undefined;
  const full = item.text ?? "";
  const preview = item.summary ?? "";
  // 完整输出确实比摘要长，才给展开入口 —— 一条 40 字的 `pwd` 没什么可展开的。
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
        <span className="sr-only">{running ? "工具执行中" : item.ok ? "工具执行成功" : "工具执行失败"}</span>
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

/**
 * 「方案已提交」卡片（计划模式收尾）。
 *
 * 刻意带一个批准按钮而不是让用户自己打「开始执行」：计划模式下 AI 什么都没动，
 * 这一步是唯一的授权动作，给它一个明确的落点，用户才知道自己点的是什么。
 */
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
        <span className="font-medium text-blue-200">方案已提交</span>
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

/**
 * 确认卡片的主体。
 *
 * 写文件类工具在这里展示「到底要改什么」——**在用户按下「允许」之前**。
 * 这是「文件变更可审」真正生效的地方：执行完再看一张事后卡片没有意义，
 * 那时文件已经落盘了。内核算不出前后对照（非写文件类工具 / 二进制 / 超大文件）
 * 时退回展示原始参数，前端不做任何猜测。
 */
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
        <div className="mb-1.5 text-[10.5px] leading-relaxed text-amber-300/90">
          {card.reason}
        </div>
      ) : null}
      {/* 原始参数折起来：它跟 diff 说的是同一件事，但没人读那坨 JSON；
          留着是为了「参数被截断时还能看全」和排查用。 */}
      <details className="mb-2">
        <summary className="cursor-pointer text-[10.5px] text-neutral-600 hover:text-neutral-400">
          查看原始参数
        </summary>
        <pre className="mt-1 max-h-28 overflow-auto whitespace-pre-wrap font-mono text-[10.5px] text-neutral-500">
          {card.rendered}
        </pre>
      </details>
    </>
  );
}

/** 「变更记录」卡片：AI 改过的文件 + 行级 diff。 */
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

/**
 * 逐行 diff 的渲染，确认卡片与变更记录卡片共用一套。
 *
 * 共用是刻意的：同一个改动在「允许之前」和「执行之后」显示成两个样子，
 * 用户会以为中间又变了一次。
 */
function DiffLines({ before, after }: { before: string; after: string }) {
  const lines = simpleDiff(before, after);
  if (!hasVisibleChange(lines)) {
    // 逐行比不出增删。两种来源，都不能渲染成"一段没变的内容" ——
    // 那看起来像卡片坏了：
    //   · 两段完全相同（内核已经过滤掉了，这里只是防御）；
    //   · 只差文件末尾的那一个换行（"a" → "a\n"）。要精确表达它得引入
    //     「文件末尾无换行」标记，属完整 diff 算法的范围，本模块刻意不做，
    //     但至少要明说是换行差异，而不是假装没变。
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
            // diff 专用色，**不**复用通用成功/危险色：那两套色在本界面里还
            // 扛着「操作成功 / 操作危险」的含义，而 diff 的增删只说明
            // 「内容变了」—— 借同一套色会被读成价值判断（删了就是坏事）。
            l.kind === "add" ? "nx-diff-add" : l.kind === "del" ? "nx-diff-del" : "text-neutral-500"
          }
        >
          {diffLineText(l)}
        </div>
      ))}
    </>
  );
}
