// AI 侧栏：对话 + 流式事件 + 工具调用卡片 + 确认卡片 + 接管 + 历史会话。
//
// 形态参考 同类工具 的 AI 分栏：顶栏（历史 / 新建）、消息流、底部输入区
// （大输入框 + 功能行 + 圆形发送）。**AI 接管终端**是我们独有的能力，完整保留。
//
// 四条自己定的规矩：
//   · 权限是**档位**（只读/读写/静默），不是"一个静默开关" —— 见 §零 的决策表；
//   · 危险命令在任何档位都要问，硬底线在任何档位都不执行；
//   · 图片走 data URI 内联，截图不出本机，也只在当次请求里存在（不落库）；
//   · @ 引用是给模型的**显式目标**，不改内核 scope —— scope 仍由当前会话决定。
import { useEffect, useMemo, useRef, useState } from "react";
import { ask, promptText } from "../../ui/dialogs";
import { aiApi, type AiPermissionConfig, type AiPermissionMode } from "../../ipc/commands";
import { createAiChannel } from "../../ipc/events";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { ModelPanel } from "./ModelPanel";
import { ModelSelector } from "./ModelSelector";
import { Markdown } from "./Markdown";
import { UsageRing, type AiUsage } from "./UsageRing";
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

type ChatItem =
  | { role: "user"; text: string; imageCount?: number }
  | { role: "assistant"; text: string }
  | { role: "reasoning"; text: string }
  | { role: "tool"; name: string; display: string; summary?: string; ok?: boolean; exitCode?: number | null }
  | { role: "diff"; path: string; before: string; after: string }
  | { role: "plan"; text: string }
  | { role: "confirm"; jobId: string; callId: string; tool: string; rendered: string };

/** 任务清单的一条（与内核 `TodoItem` 对应）。 */
interface TodoRow {
  content: string;
  status: string;
}

/** @ 引用选出来的一枚 chip。 */
interface RefChip {
  id: string;
  kind: "asset" | "tab";
  label: string;
  detail: string;
}

/** 空态时给的几个"问一句就能看出能力"的例子。 */
const EXAMPLES = [
  "api-server 好像挂了，帮我排查",
  "这台机器内存够不够？",
  "nginx 配置有没有问题",
  "重启 mysql-prod",
];

/**
 * 三档权限的展示文案。
 *
 * 说明文字刻意写成"会发生什么"而不是"叫什么" —— 用户选档位时要判断的是
 * 「AI 接下来会不会突然动我的机器」，不是记住三个名词。
 */
const MODE_OPTIONS: { value: AiPermissionMode; label: string; hint: string }[] = [
  { value: "read_only", label: "只读", hint: "只看不动，不会改你的机器" },
  { value: "read_write", label: "读写", hint: "要动手之前先问你" },
  { value: "silent", label: "完全静默", hint: "只有危险操作才问你" },
];

const MODE_LABEL: Record<AiPermissionMode, string> = {
  read_only: "只读",
  read_write: "读写",
  silent: "完全静默",
};

export function AiSidebar({ sessionId, tabId }: { sessionId?: string; tabId?: string }) {
  const { rightOpen, setRightOpen, aiBusy, setAiBusy, pushToast, rightWidth, workspaces } = useUi();
  const [items, setItems] = useState<ChatItem[]>([]);
  const [input, setInput] = useState("");
  const [confirmCard, setConfirmCard] = useState<Extract<ChatItem, { role: "confirm" }> | null>(null);
  const [jobId, setJobId] = useState<string | null>(null);
  /** 最近一轮的用量快照（功能行右侧的圆环）。null = 本轮还没跑过。 */
  const [usage, setUsage] = useState<AiUsage | null>(null);
  /** 当前任务清单（todo_write 推整份）。 */
  const [todos, setTodos] = useState<TodoRow[]>([]);
  /** 计划模式：只调研、出方案，等批准。 */
  const [planMode, setPlanMode] = useState(false);
  const [modelPanelOpen, setModelPanelOpen] = useState(false);
  /**
   * 「这一轮是不是计划模式收的尾」。
   *
   * 用 ref 而不是 state：它在**流中途**被 planSubmitted 改写，而 done 分支
   * 拿到的是 send() 那一刻的闭包，state 在那里永远是旧值。
   */
  const planPendingRef = useRef(false);
  const [conversationId, setConversationId] = useState<string | undefined>(undefined);
  /** 权限档位 + 自定义危险规则（以内核为准，挂载时拉一次）。 */
  const [perm, setPerm] = useState<AiPermissionConfig | null>(null);
  const [permOpen, setPermOpen] = useState(false);
  /** 新增危险规则的输入草稿。 */
  const [ruleDraft, setRuleDraft] = useState("");
  /** 待发送的图片（data URI）。 */
  const [images, setImages] = useState<string[]>([]);
  /** @ 引用 chip。 */
  const [refs, setRefs] = useState<RefChip[]>([]);
  const [atOpen, setAtOpen] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [conversations, setConversations] = useState<{ id: string; title: string; updatedAt: number }[]>([]);
  /** 接管状态来自全局 store（§8.6）：顶部横幅与这里读的是同一份。 */
  const takeover = useUi((s) => s.takeover);
  const scrollRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight });
  }, [items]);

  useEffect(() => {
    void aiApi
      .getPermission()
      .then(setPerm)
      .catch(() => undefined);
  }, []);

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

  const addDangerRule = () => {
    const r = ruleDraft.trim();
    if (!r || !perm) return;
    if (perm.dangerRules.some((x) => x.toLowerCase() === r.toLowerCase())) {
      setRuleDraft("");
      return;
    }
    void savePerm({ ...perm, dangerRules: [...perm.dangerRules, r] });
    setRuleDraft("");
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
    if ((!message && images.length === 0) || aiBusy) return;
    // 计划模式可以按次覆盖：「批准并执行」那一下必须走普通模式，
    // 否则模型会再给你一份计划 —— 用户点的是"执行"，不是"再想想"。
    const usePlan = override?.planMode ?? planMode;
    setAiBusy(true);
    setInput("");
    setRefs([]);
    setAtOpen(false);
    const sentImages = images;
    setImages([]);
    setItems((prev) => [
      ...prev,
      { role: "user", text: message, imageCount: sentImages.length || undefined },
    ]);

    const channel = createAiChannel((ev) => {
      const type = ev.type as string;
      switch (type) {
        case "delta":
          setItems((prev) => {
            const last = prev[prev.length - 1];
            if (last && last.role === "assistant") {
              return [...prev.slice(0, -1), { ...last, text: last.text + (ev.text as string) }];
            }
            return [...prev, { role: "assistant", text: ev.text as string }];
          });
          break;
        case "reasoning":
          // 与 delta 同款**合并到上一条**：推理是逐 token 流式来的，
          // 每个片段都新开一个气泡的话，就成了"几个字一行"。
          setItems((prev) => {
            const last = prev[prev.length - 1];
            if (last && last.role === "reasoning") {
              return [...prev.slice(0, -1), { ...last, text: last.text + (ev.text as string) }];
            }
            return [...prev, { role: "reasoning", text: ev.text as string }];
          });
          break;
        case "toolCall":
          setItems((prev) => [
            ...prev,
            {
              role: "tool",
              name: ev.name as string,
              display: (ev.display as string) || (ev.name as string),
            },
          ]);
          break;
        case "toolResult":
          setItems((prev) => {
            const idx = [...prev].reverse().findIndex((i) => i.role === "tool" && !("summary" in i));
            if (idx >= 0) {
              const realIdx = prev.length - 1 - idx;
              const copy = [...prev];
              copy[realIdx] = {
                ...(copy[realIdx] as Extract<ChatItem, { role: "tool" }>),
                summary: ev.summary as string,
                ok: ev.ok as boolean,
                exitCode: ev.exitCode as number | null,
              };
              return copy;
            }
            return prev;
          });
          break;
        case "fileChange":
          setItems((prev) => [
            ...prev,
            {
              role: "diff",
              path: ev.path as string,
              before: (ev.before as string) ?? "",
              after: (ev.after as string) ?? "",
            },
          ]);
          break;
        case "confirmRequired":
          {
            const card: ChatItem = {
              role: "confirm",
              jobId: jobId ?? "",
              callId: ev.id as string,
              tool: ev.tool as string,
              rendered: ev.rendered as string,
            };
            setConfirmCard(card);
            setItems((prev) => [...prev, card]);
          }
          break;
        case "usage":
          // 每轮覆盖（不是累加）：圆环要回答的是「现在还剩多少」，
          // 累计值会把历史请求也滚进来，越用越吓人。
          setUsage({
            promptTokens: Number(ev.promptTokens) || 0,
            completionTokens: Number(ev.completionTokens) || 0,
            cachedTokens: Number(ev.cachedTokens) || 0,
            contextWindow: Number(ev.contextWindow) || 0,
          });
          break;
        case "todos":
          setTodos((ev.items as TodoRow[]) ?? []);
          break;
        case "planSubmitted":
          // 只立旗子，气泡等 done 到了再落 —— 否则这里加一条、done 再加一条，
          // 同一份方案会在对话里出现两遍。
          planPendingRef.current = true;
          break;
        case "done": {
          const answer = (ev.answer as string) || "(无回答)";
          const wasPlan = planPendingRef.current;
          planPendingRef.current = false;
          setAiBusy(false);
          setConfirmCard(null);
          setItems((prev) => [
            ...prev,
            wasPlan ? { role: "plan", text: answer } : { role: "assistant", text: answer },
          ]);
          break;
        }
        case "error":
          setAiBusy(false);
          pushToast("error", `AI: ${ev.message as string}`);
          break;
        default:
          break;
      }
    });

    try {
      const res = await aiApi.chat({
        conversationId,
        scope: { sessionId, tabId },
        message: composeMessage(message),
        images: sentImages.length ? sentImages : undefined,
        planMode: usePlan || undefined,
        channel,
      });
      setJobId(res.jobId);
      // 必须用内核回传的 id —— 不能用发出去的那个变量：首轮它是 `undefined`，
      // 设回去等于没设，于是下一轮追问又被当成新会话，聊天记录被切成一条条碎片。
      setConversationId(res.conversationId);
    } catch (e) {
      pushToast("error", describeError(e));
      setAiBusy(false);
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
    if (!jobId) return;
    await aiApi.confirm(jobId, decision);
    setConfirmCard(null);
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
    try {
      const msgs = await aiApi.messages(id);
      setItems(msgs.flatMap(msgToItems));
      setConversationId(id);
      setHistoryOpen(false);
      setConfirmCard(null);
    } catch (e) {
      pushToast("error", `打开会话失败：${describeError(e)}`);
    }
  };

  const newConversation = () => {
    setConversationId(undefined);
    setItems([]);
    setConfirmCard(null);
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

    // 命令是「spawn 后立刻返回」，事件可能在 await 拿到 jobId 之前就到达；
    // 闭包直接读 state 会拿到 null，故用 holder 承载。
    const holder: { jobId: string | null } = { jobId: null };
    const clearTakeover = () => useUi.getState().setTakeover(null);

    const channel = createAiChannel((ev) => {
      const type = ev.type as string;
      if (type === "screen") {
        // 只保留最新一屏，避免 30 步把对话刷满
        setItems((prev) => {
          const last = prev[prev.length - 1];
          const item: ChatItem = {
            role: "tool",
            name: "read_screen",
            display: "读屏",
            summary: String(ev.text).slice(-200),
          };
          if (last && last.role === "tool" && last.name === "read_screen") {
            return [...prev.slice(0, -1), item];
          }
          return [...prev, item];
        });
      } else if (type === "confirmRequired") {
        // 接管中遇到需确认的动作：走与主对话一致的确认卡片，不再自动拒绝
        const card: ChatItem = {
          role: "confirm",
          jobId: holder.jobId ?? "",
          callId: ev.id as string,
          tool: ev.tool as string,
          rendered: ev.rendered as string,
        };
        setConfirmCard(card);
        setItems((prev) => [...prev, card]);
      } else if (type === "delta") {
        setItems((prev) => {
          const last = prev[prev.length - 1];
          if (last && last.role === "assistant") {
            return [...prev.slice(0, -1), { ...last, text: last.text + (ev.text as string) }];
          }
          return [...prev, { role: "assistant", text: ev.text as string }];
        });
      } else if (type === "done") {
        setAiBusy(false);
        setConfirmCard(null);
        clearTakeover();
        setItems((prev) => [
          ...prev,
          { role: "assistant", text: `接管结束：${(ev.answer as string) || "(无回答)"}` },
        ]);
      } else if (type === "error") {
        setAiBusy(false);
        setConfirmCard(null);
        clearTakeover();
        pushToast("error", `接管：${ev.message as string}`);
      }
    });

    setAiBusy(true);
    try {
      const id = await aiApi.takeoverRun({ tabId, instruction, allowWrite, channel });
      holder.jobId = id;
      setJobId(id); // 让确认卡片对得上这次接管
      useUi.getState().setTakeover({
        tabId,
        jobId: id,
        task: instruction,
        allowWrite,
        startedAt: Date.now(),
      });
      pushToast("info", "接管已启动 —— 顶部横幅可随时夺回，Esc 亦可");
    } catch (e) {
      setAiBusy(false);
      clearTakeover();
      pushToast("error", describeError(e));
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
                onClick={() => void savePerm({ ...perm, mode: m.value })}
              >
                <span className="nx-menu-label">
                  {perm.mode === m.value ? "● " : "○ "}
                  {m.label}
                </span>
                <span className="nx-menu-hint">{m.hint}</span>
              </button>
            ))}
          </div>

          {/* 接管是独立模块，这里只说明、不配置 */}
          <div className="mb-2 rounded border border-red-500/25 bg-red-500/[0.07] px-2 py-1.5 text-[10.5px] leading-relaxed text-red-200/90">
            <div className="mb-0.5 font-medium text-red-200">终端接管（实验性功能）</div>
            让 AI 直接接手当前终端干活。它不受上面三档限制 ——
            用功能行的显示器图标发起，随时按 Esc 夺回。
          </div>

          {/* 自定义危险规则 */}
          <div className="mb-1 text-[11px] text-neutral-300">
            自定义危险操作
            <span className="ml-1 text-neutral-500">（遇到就问你，静默档也会问）</span>
          </div>
          <div className="mb-1.5 flex gap-1">
            <input
              className="nx-input nx-input-sm min-w-0 flex-1 font-mono"
              placeholder="例如 kubectl delete"
              value={ruleDraft}
              onChange={(e) => setRuleDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  addDangerRule();
                }
              }}
            />
            <button className="nx-btn nx-btn-outline nx-btn-xs" onClick={addDangerRule}>
              添加
            </button>
          </div>
          {perm.dangerRules.length === 0 ? (
            <div className="nx-hint text-[10.5px]">还没有自定义规则</div>
          ) : (
            <div className="flex flex-wrap gap-1">
              {perm.dangerRules.map((r) => (
                <span key={r} className="nx-chip" title={r}>
                  <span className="max-w-[160px] truncate font-mono">{r}</span>
                  <button
                    className="nx-chip-x"
                    title="删除"
                    onClick={() =>
                      void savePerm({
                        ...perm,
                        dangerRules: perm.dangerRules.filter((x) => x !== r),
                      })
                    }
                  >
                    <IconClose size={10} />
                  </button>
                </span>
              ))}
            </div>
          )}

          <div className="mt-2 border-t border-neutral-800/60 pt-1.5 text-[10px] leading-relaxed text-neutral-500">
            另外有几条铁律：格式化磁盘、清空系统目录、删库这类毁掉就回不来的操作，
            任何档位都不会执行，也不会弹确认框 —— 这种「确定」按钮本来就不该存在。
          </div>
        </div>
      )}

      {/* 消息流 */}
      <div ref={scrollRef} className="min-h-0 flex-1 space-y-2.5 overflow-y-auto p-3">
        {items.length === 0 && (
          <div className="flex flex-col items-center gap-3 px-2 pt-10 text-center">
            <span className="nx-empty-icon">
              <IconBot size={19} />
            </span>
            <div className="text-xs text-neutral-400">
              开始新对话 —— AI 跑过的每条命令都会出现在你眼前的终端里，不是黑盒。
            </div>
            <div className="flex w-full flex-col gap-1.5">
              {EXAMPLES.map((ex) => (
                <button
                  key={ex}
                  className="nx-chip w-full justify-start text-left hover:border-neutral-600 hover:text-neutral-200"
                  onClick={() => setInput(ex)}
                >
                  <IconChevronRight size={11} className="shrink-0" />
                  <span className="truncate">{ex}</span>
                </button>
              ))}
            </div>
          </div>
        )}
        {items.map((item, i) => (
          <ChatBubble key={i} item={item} onApprovePlan={approvePlan} />
        ))}
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
            <pre className="mb-2.5 max-h-28 overflow-auto whitespace-pre-wrap font-mono text-[11px] text-neutral-200">
              {confirmCard.rendered}
            </pre>
            <div className="flex gap-1.5">
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
            </div>
          </div>
        </div>
      )}

      {/* 任务清单：AI 自己维护的待办。
          钉在输入区上方而不是塞进消息流 —— 它是「现在做到哪了」的常驻视图，
          要滚动上去才能看到的清单等于没做这个功能。 */}
      {todos.length > 0 && (
        <div className="shrink-0 border-t border-neutral-800/60 bg-neutral-900/50 px-2.5 py-2">
          <div className="mb-1 flex items-center gap-1.5 text-[10.5px] text-neutral-400">
            <IconList size={11} />
            任务清单
            <span className="text-neutral-600">
              {todos.filter((t) => t.status === "completed").length}/{todos.length}
            </span>
          </div>
          <div className="flex max-h-28 flex-col gap-0.5 overflow-y-auto">
            {todos.map((t, i) => (
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
          {/* 计划模式：先出方案、等你点头再动手 */}
          <button
            className={`nx-icon-btn nx-icon-btn-sm ${planMode ? "is-active" : ""}`}
            title={
              planMode
                ? "计划模式已开：AI 只做只读调研，把方案交给你之后再动手"
                : "计划模式：先让 AI 出方案，你批准了再执行"
            }
            onClick={() => setPlanMode((v) => !v)}
          >
            <IconList size={13} />
          </button>
          <div className="nx-spacer" />
          <UsageRing usage={usage} />
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
          <button
            className="nx-send-btn"
            title="发送 (Enter)"
            disabled={aiBusy || (!input.trim() && images.length === 0)}
            onClick={() => void send()}
          >
            {aiBusy ? <IconLoader size={14} className="animate-spin" /> : <IconSendArrow />}
          </button>
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

/** 读图 → data URI（内联进请求，不落盘）。 */
function readAsDataUrl(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const fr = new FileReader();
    fr.onload = () => resolve(String(fr.result));
    fr.onerror = () => reject(new Error("读取图片失败"));
    fr.readAsDataURL(file);
  });
}

/** 持久化消息 → 会话项。只还原文本，工具调用与图片不入历史。 */
function msgToItems(m: { role: string; content: unknown }): ChatItem[] {
  const raw = m.content;
  const text =
    typeof raw === "string"
      ? raw
      : typeof raw === "object" && raw !== null && "content" in raw
        ? String((raw as { content?: unknown }).content ?? "")
        : "";
  if (!text) return [];
  if (m.role === "user") {
    const n =
      typeof raw === "object" && raw !== null && "imageCount" in raw
        ? Number((raw as { imageCount?: unknown }).imageCount ?? 0)
        : 0;
    return [{ role: "user", text, imageCount: n || undefined }];
  }
  if (m.role === "assistant") return [{ role: "assistant", text }];
  return [];
}

function ChatBubble({ item, onApprovePlan }: { item: ChatItem; onApprovePlan: (plan: string) => void }) {
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
    return (
      <div className="ml-1 border-l-2 border-neutral-700 pl-2.5 text-[11.5px] leading-relaxed text-neutral-500 italic">
        {item.text.length > 200 ? `${item.text.slice(0, 200)}…` : item.text}
      </div>
    );
  }
  if (item.role === "confirm") return null;
  if (item.role === "diff") return <DiffBubble item={item} />;
  if (item.role === "plan") {
    return <PlanBubble item={item} onApprove={() => onApprovePlan(item.text)} />;
  }
  if (item.role === "tool") {
    const running = item.summary === undefined;
    return (
      <div className="rounded-lg border border-neutral-800 bg-neutral-900/70 px-2.5 py-2 text-[11.5px]">
        <div className="flex items-center gap-1.5">
          <span className="nx-badge nx-badge-purple font-mono">{item.name}</span>
          <span className="min-w-0 flex-1 truncate font-mono text-neutral-400" title={item.display}>
            {item.display}
          </span>
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
        {item.summary && (
          <pre className="mt-1.5 max-h-32 overflow-auto whitespace-pre-wrap font-mono text-[10.5px] text-neutral-500">
            {item.summary}
          </pre>
        )}
      </div>
    );
  }
  return null;
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

/** 「变更记录」卡片：AI 改过的文件 + 行级 diff。 */
function DiffBubble({ item }: { item: Extract<ChatItem, { role: "diff" }> }) {
  const lines = simpleDiff(item.before, item.after);
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
        {lines.map((l, i) => (
          <div
            key={i}
            className={
              // diff 专用色，**不**复用通用成功/危险色：那两套色在本界面里还
              // 扛着「操作成功 / 操作危险」的含义，而 diff 的增删只说明
              // 「内容变了」—— 借同一套色会被读成价值判断（删了就是坏事）。
              l.kind === "add"
                ? "nx-diff-add"
                : l.kind === "del"
                  ? "nx-diff-del"
                  : "text-neutral-500"
            }
          >
            {(l.kind === "add" ? "+ " : l.kind === "del" ? "- " : "  ") + l.text}
          </div>
        ))}
      </pre>
    </div>
  );
}

/**
 * 极简行级 diff：摘掉公共前后缀，中间整段标成删/增。
 *
 * 刻意不做 LCS —— 这里只要"一眼看出改了哪几行"，而 AI 改配置基本都是
 * 局部替换，前后缀一摘就只剩那几行；上完整 diff 算法反而把界面和代码都搞重。
 * 两侧各留 2 行上下文，免得用户看不出改动的落点。
 */
function simpleDiff(
  before: string,
  after: string,
): { kind: "add" | "del" | "same"; text: string }[] {
  const a = before.split("\n");
  const b = after.split("\n");

  let head = 0;
  while (head < a.length && head < b.length && a[head] === b[head]) head++;

  let tail = 0;
  while (
    tail < a.length - head &&
    tail < b.length - head &&
    a[a.length - 1 - tail] === b[b.length - 1 - tail]
  ) {
    tail++;
  }

  const out: { kind: "add" | "del" | "same"; text: string }[] = [];
  const ctx = 2;
  for (let i = Math.max(0, head - ctx); i < head; i++) out.push({ kind: "same", text: a[i] });
  for (let i = head; i < a.length - tail; i++) out.push({ kind: "del", text: a[i] });
  for (let i = head; i < b.length - tail; i++) out.push({ kind: "add", text: b[i] });
  for (let i = 0; i < Math.min(ctx, tail); i++) {
    out.push({ kind: "same", text: a[a.length - tail + i] });
  }
  return out;
}
