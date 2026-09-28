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
  | {
      role: "tool";
      name: string;
      display: string;
      /** 折叠态看到的一行摘要（内核截到 400 字）。 */
      summary?: string;
      /** 完整输出，点「展开」看的就是它（内核上限 64K）。 */
      text?: string;
      ok?: boolean;
      exitCode?: number | null;
    }
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

export function AiSidebar({ sessionId, tabId }: { sessionId?: string; tabId?: string }) {
  const { rightOpen, setRightOpen, aiBusy, setAiBusy, pushToast, rightWidth, workspaces } = useUi();
  const [items, setItems] = useState<ChatItem[]>([]);
  const [input, setInput] = useState("");
  const [confirmCard, setConfirmCard] = useState<Extract<ChatItem, { role: "confirm" }> | null>(null);
  const [jobId, setJobId] = useState<string | null>(null);
  /**
   * 内核推来的运行状态（`AiEvent::Status`）。
   *
   * 这个事件一直在推，但前端**从来没渲染过** —— 于是 AI 跑长命令时界面上
   * 什么动静都没有，"卡住了"的焦虑有一半来自这里：明明还在干活，
   * 用户看到的是一个不动的转圈和一张点不动的发送按钮。
   */
  const [status, setStatus] = useState<{ phase: string; detail?: string; turn?: number } | null>(null);
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
        case "status":
          setStatus({
            phase: ev.phase as string,
            detail: (ev.detail as string) || undefined,
            turn: (ev.turn as number) ?? undefined,
          });
          break;
        case "delta":
          // 开始吐字就说明不再"思考中"了，状态条让位给正文。
          setStatus(null);
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
                text: (ev.text as string) ?? "",
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
          // raw 记下"内核到底给没给答案"：answer 会被填成占位串，那就不能再拿它
          // 跟流式正文比对 —— 占位串永远不可能等于正文，会误走追加分支。
          const raw = (ev.answer as string) || "";
          const answer = raw || "(无回答)";
          const wasPlan = planPendingRef.current;
          planPendingRef.current = false;
          setAiBusy(false);
          setConfirmCard(null);
          setStatus(null);
          setItems((prev) => {
            const last = prev[prev.length - 1];
            if (last && (last.role === "assistant" || last.role === "plan")) {
              const streamed = last.text.trim();
              // 判据：stream 已经把这段吐完了，done 再补一条就是重复 —— 这就是
              // 「同一段回答出现两遍」的根因。逐字相同只说明同一个答案，
              // 此时既不追加，也不改内容。
              if (raw && streamed === answer.trim()) {
                // 计划模式下"收尾"这个语义不能丢：把流式那条升级成 plan 气泡，
                // 让批准按钮仍然出现，而不是多长出一条方案。
                if (wasPlan) return [...prev.slice(0, -1), { role: "plan", text: answer }];
                return prev;
              }
              // 末条是权威答案的真前缀（长度 ≥ 8 才认，免得一个"好"字就被当成
              // 截断）→ 流被代理截了，或对端回退成了非流式。直接换成完整答案。
              if (
                last.role === "assistant" &&
                raw &&
                streamed.length >= 8 &&
                answer.trim().startsWith(streamed)
              ) {
                return [...prev.slice(0, -1), { ...last, text: answer }];
              }
            }
            return [
              ...prev,
              wasPlan ? { role: "plan", text: answer } : { role: "assistant", text: answer },
            ];
          });
          break;
        }
        case "error":
          setAiBusy(false);
          setStatus(null);
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

  /**
   * 停止本轮。
   *
   * 以前界面上**没有这个入口** —— 内核早就实现了 `ai_cancel`、`ipc/commands.ts`
   * 也导出了 `cancel()`，但从来没有任何地方调用它。结果 AI 一旦卡在长命令上，
   * 发送按钮就只剩一个转不停的圈：点不动、也没别的办法，只能重启应用。
   *
   * 点完立刻解锁输入，不等后端回事件 —— 停止是用户按下的动作，手感必须即时；
   * 后端稍后会推 `error("已取消")`，那一下只是把状态再确认一遍。
   */
  const stop = async () => {
    const id = jobId;
    if (!id) {
      // 极短窗口：请求刚发出去、内核还没把 jobId 回过来，这时没有东西可取消。
      // 静默 return 会让人以为按钮坏了，所以说一句。
      pushToast("info", "这一轮还没启动完，稍等一下再点");
      return;
    }
    setAiBusy(false);
    setConfirmCard(null);
    setStatus(null);
    try {
      await aiApi.cancel(id);
      pushToast("info", "已停止本轮");
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
        // 只保留最新一屏：把旧的读屏卡全部摘掉再追加新的。
        // 最初实现只在「上一张恰好是读屏卡」时去重 —— 一旦中间插进了
        // 动作卡/思考气泡，后续每步都会多出一张读屏卡，越滚越长。
        setItems((prev) => {
          const rest = prev.filter((i) => !(i.role === "tool" && i.name === "read_screen"));
          return [
            ...rest,
            {
              role: "tool" as const,
              name: "read_screen",
              display: "读屏",
              summary: String(ev.text).slice(-200),
              text: String(ev.text).slice(-4000),
              ok: true,
            },
          ];
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
        // 模型每步的「看到…因为…所以…」叙述，实时流成气泡
        setItems((prev) => {
          const last = prev[prev.length - 1];
          if (last && last.role === "assistant") {
            return [...prev.slice(0, -1), { ...last, text: last.text + (ev.text as string) }];
          }
          return [...prev, { role: "assistant", text: ev.text as string }];
        });
      } else if (type === "reasoning") {
        // 与主对话同款：逐 token 合并到上一条，避免「几个字一行」
        setItems((prev) => {
          const last = prev[prev.length - 1];
          if (last && last.role === "reasoning") {
            return [...prev.slice(0, -1), { ...last, text: last.text + (ev.text as string) }];
          }
          return [...prev, { role: "reasoning", text: ev.text as string }];
        });
      } else if (type === "toolCall") {
        // 接管的每个动作（send_keys/wait_for/done）也发卡片：
        // 气泡讲因果，卡片记动作 —— 用户才看得懂 AI 在终端里敲了什么
        setItems((prev) => [
          ...prev,
          {
            role: "tool" as const,
            name: ev.name as string,
            display: (ev.display as string) || (ev.name as string),
          },
        ]);
      } else if (type === "toolResult") {
        setItems((prev) => {
          const idx = [...prev]
            .reverse()
            .findIndex((i) => i.role === "tool" && !("summary" in i));
          if (idx >= 0) {
            const realIdx = prev.length - 1 - idx;
            const copy = [...prev];
            copy[realIdx] = {
              ...(copy[realIdx] as Extract<ChatItem, { role: "tool" }>),
              summary: ev.summary as string,
              text: (ev.text as string) ?? "",
              ok: ev.ok as boolean,
              exitCode: ev.exitCode as number | null,
            };
            return copy;
          }
          return prev;
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

      {/* 消息流 */}
      <div ref={scrollRef} className="min-h-0 flex-1 space-y-2.5 overflow-y-auto p-3">
        {items.length === 0 && (
          <div className="flex flex-col items-center gap-3 px-2 pt-10 text-center">
            <span className="nx-empty-icon">
              <IconBot size={19} />
            </span>
            <div className="text-xs text-neutral-400">新建会话 · 命令与输出全程留痕</div>
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
            <pre className="mb-1.5 max-h-28 overflow-auto whitespace-pre-wrap font-mono text-[11px] text-neutral-200">
              {confirmCard.rendered}
            </pre>
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
        {/* 计划模式开着时必须一直看得见。它已经从功能行搬进权限浮层，
            而浮层一收起来就没人提醒用户"这一轮 AI 只出方案、不动手"了 ——
            忘了它还开着，会以为 AI 变磨叽了。 */}
        {planMode && (
          <div className="mb-1.5 flex items-center gap-1.5 text-[11px] text-blue-300/90">
            <IconList size={10} className="shrink-0" />
            <span className="truncate">计划模式 · 先出方案，你批准了再动手</span>
          </div>
        )}
        {/* 运行状态条。
            内核一直在推 `status`，前端却从没渲染过 —— AI 跑长命令时界面
            完全静止，用户只能猜它死了没有。这行字就是回答「它还在动吗」。 */}
        {aiBusy && status && (
          <div className="mb-1.5 flex items-center gap-1.5 text-[11px] text-neutral-500">
            <IconLoader size={10} className="animate-spin text-amber-300/80" />
            <span className="truncate">{statusText(status)}</span>
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

/** 运行状态的一句话（把 `AiEvent::Status` 翻成人话）。 */
function statusText(s: { phase: string; detail?: string; turn?: number }): string {
  if (s.phase === "compacting") return s.detail ?? "上下文接近上限，正在压缩早期工具结果…";
  if (s.phase === "thinking") return s.turn ? `第 ${s.turn} 轮 · 正在思考…` : "正在思考…";
  return s.detail ?? s.phase;
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
  if (item.role === "tool") return <ToolBubble item={item} />;
  return null;
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
