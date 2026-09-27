// AI 侧栏（M2-T7）：对话 + 流式事件 + 工具调用卡片 + 确认卡片 + 接管。
import { useEffect, useRef, useState } from "react";
import { ask, promptText } from "../../ui/dialogs";
import { aiApi } from "../../ipc/commands";
import { createAiChannel } from "../../ipc/events";
import { useUi } from "../../app/store";
import {
  IconAlert,
  IconBot,
  IconCheck,
  IconChevronRight,
  IconGamepad,
  IconLoader,
  IconSparkles,
  IconXCircle,
} from "../../ui/icons";

type ChatItem =
  | { role: "user"; text: string }
  | { role: "assistant"; text: string }
  | { role: "reasoning"; text: string }
  | { role: "tool"; name: string; display: string; summary?: string; ok?: boolean; exitCode?: number | null }
  | { role: "confirm"; jobId: string; callId: string; tool: string; rendered: string };

/** 空态时给的几个"问一句就能看出能力"的例子。 */
const EXAMPLES = [
  "api-server 好像挂了，帮我排查",
  "这台机器内存够不够？",
  "nginx 配置有没有问题",
  "重启 mysql-prod",
];

export function AiSidebar({ sessionId, tabId }: { sessionId?: string; tabId?: string }) {
  const { rightOpen, setRightOpen, aiBusy, setAiBusy, pushToast } = useUi();
  const [items, setItems] = useState<ChatItem[]>([]);
  const [input, setInput] = useState("");
  const [confirmCard, setConfirmCard] = useState<Extract<ChatItem, { role: "confirm" }> | null>(null);
  const [jobId, setJobId] = useState<string | null>(null);
  const [model, setModel] = useState("");
  const [conversationId, setConversationId] = useState<string | undefined>(undefined);
  /** 接管状态来自全局 store（§8.6）：顶部横幅与这里读的是同一份。 */
  const takeover = useUi((s) => s.takeover);
  const pendingAsk = useUi((s) => s.pendingAsk);
  const setPendingAsk = useUi((s) => s.setPendingAsk);
  const scrollRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight });
  }, [items]);

  useEffect(() => {
    void aiApi
      .getProvider()
      .then((c) => setModel(c.model))
      .catch(() => undefined);
  }, []);

  /**
   * 终端里「向 AI 提问」把选中的那段输出灌进来。
   *
   * 只预填、不自动发送：选中的多半是一段报错，用户通常还要补一句
   * 「这是什么问题 / 怎么修」—— 替他把消息发出去等于替他提问，很容易答非所问。
   * 光标落在末尾并聚焦，接着打字就能发。
   */
  useEffect(() => {
    if (!pendingAsk) return;
    setInput((prev) => (prev.trim() ? `${prev}\n\n${pendingAsk}\n` : `${pendingAsk}\n\n`));
    setPendingAsk(null);
    const t = window.setTimeout(() => inputRef.current?.focus(), 30);
    return () => window.clearTimeout(t);
  }, [pendingAsk, setPendingAsk]);

  const send = async () => {
    const message = input.trim();
    if (!message || aiBusy) return;
    setAiBusy(true);
    setInput("");
    setItems((prev) => [...prev, { role: "user", text: message }]);

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
          setItems((prev) => [...prev, { role: "reasoning", text: ev.text as string }]);
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
        case "done":
          setAiBusy(false);
          setConfirmCard(null);
          setItems((prev) => [
            ...prev,
            { role: "assistant", text: (ev.answer as string) || "(无回答)" },
          ]);
          break;
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
        message,
        channel,
      });
      setJobId(res.jobId);
      setConversationId(conversationId); // 内核侧自动建会话并持久化
    } catch (e) {
      pushToast("error", typeof e === "object" ? JSON.stringify(e) : String(e));
      setAiBusy(false);
    }
  };

  const confirm = async (decision: "allow" | "allow_session" | "deny") => {
    if (!jobId) return;
    await aiApi.confirm(jobId, decision);
    setConfirmCard(null);
  };

  const runTakeover = async () => {
    if (!tabId) {
      pushToast("error", "接管需要先打开一个终端标签");
      return;
    }
    // 接管任务通常是好几行的步骤描述（先装什么、再改哪个配置、最后重启什么），
    // 单行输入框写不下 —— 显式要求多行。
    const instruction = await promptText(
      "AI 接管任务描述（例如：安装 nginx 并启动）",
      "",
      { multiLine: true },
    );
    if (!instruction) return;
    const allowWrite = await ask(
      "是否允许 AI 写操作（发送按键）？\n\n确定 = 允许写入\n取消 = 只读接管（send_keys 全被拒绝）",
      { title: "接管权限", kind: "warning" },
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
      pushToast("error", typeof e === "object" ? JSON.stringify(e) : String(e));
    }
  };

  if (!rightOpen) return null;

  return (
    <aside className="flex h-full w-[352px] shrink-0 flex-col border-l border-neutral-800/60 bg-neutral-950">
      <div className="flex h-[38px] shrink-0 items-center gap-2 border-b border-neutral-800/60 px-2.5 pl-3">
        <IconSparkles size={15} className="text-blue-300" />
        <span className="text-[12.5px] font-semibold text-neutral-100">AI 助手</span>
        {model && <span className="nx-badge">{model}</span>}
        {aiBusy && <IconLoader size={13} className="animate-spin text-blue-300" />}
        <div className="nx-spacer" />
        {takeover && (
          <span className="nx-badge nx-badge-red">
            <span className="nx-dot nx-dot-pulse" />
            接管中
          </span>
        )}
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="收起 AI 侧栏 (Ctrl+J)"
          onClick={() => setRightOpen(false)}
        >
          <IconChevronRight size={14} />
        </button>
      </div>

      <div ref={scrollRef} className="min-h-0 flex-1 space-y-2.5 overflow-y-auto p-3">
        {items.length === 0 && (
          <div className="flex flex-col items-center gap-3 px-2 pt-8 text-center">
            <span className="nx-empty-icon">
              <IconBot size={19} />
            </span>
            <div className="text-xs text-neutral-400">
              问点什么吧。AI 跑过的每条命令都会出现在你眼前的终端里，不是黑盒。
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
          <ChatBubble key={i} item={item} />
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

      <div className="shrink-0 border-t border-neutral-800/60 p-2.5">
        {tabId && (
          <button
            className="nx-btn nx-btn-danger mb-2 w-full nx-btn-sm"
            onClick={() => void runTakeover()}
            disabled={aiBusy}
            title="AI 直接在真 PTY 里操作这台机器，你随时可按 Esc 夺回"
          >
            <IconGamepad size={13} />
            AI 接管此终端
          </button>
        )}

        <div className="flex items-end gap-1.5">
          <textarea
            ref={inputRef}
            className="nx-textarea max-h-32 min-h-[46px] flex-1"
            placeholder={sessionId ? "询问当前会话 / 请求排障…（Enter 发送，Shift+Enter 换行）" : "未连接会话（仍可全局提问）"}
            value={input}
            rows={2}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                void send();
              }
            }}
          />
          <button
            className="nx-btn nx-btn-primary shrink-0"
            disabled={aiBusy || !input.trim()}
            onClick={() => void send()}
          >
            {aiBusy ? <IconLoader size={13} className="animate-spin" /> : null}
            发送
          </button>
        </div>
      </div>
    </aside>
  );
}

function ChatBubble({ item }: { item: ChatItem }) {
  if (item.role === "user") {
    return (
      <div className="ml-10 rounded-[10px] rounded-br-[3px] border border-blue-500/25 bg-blue-500/20 px-3 py-2 text-[12.3px] leading-relaxed text-blue-50">
        {item.text}
      </div>
    );
  }
  if (item.role === "assistant") {
    return (
      <div className="mr-2 rounded-[10px] rounded-bl-[3px] border border-neutral-800 bg-neutral-800 px-3 py-2 text-[12.3px] leading-relaxed text-neutral-200">
        <pre className="font-sans whitespace-pre-wrap">{item.text}</pre>
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
