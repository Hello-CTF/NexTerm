// AI 侧栏（M2-T7）：对话 + 流式事件 + 工具调用卡片 + 确认卡片 + 接管横幅。
import { useEffect, useRef, useState } from "react";
import { ask, promptText } from "../../ui/dialogs";
import { aiApi } from "../../ipc/commands";
import { createAiChannel } from "../../ipc/events";
import { useUi } from "../../app/store";

type ChatItem =
  | { role: "user"; text: string }
  | { role: "assistant"; text: string }
  | { role: "reasoning"; text: string }
  | { role: "tool"; name: string; display: string; summary?: string; ok?: boolean; exitCode?: number | null }
  | { role: "confirm"; jobId: string; callId: string; tool: string; rendered: string };

export function AiSidebar({ sessionId, tabId }: { sessionId?: string; tabId?: string }) {
  const { rightOpen, setRightOpen, aiBusy, setAiBusy, pushToast } = useUi();
  const [items, setItems] = useState<ChatItem[]>([]);
  const [input, setInput] = useState("");
  const [confirmCard, setConfirmCard] = useState<Extract<ChatItem, { role: "confirm" }> | null>(null);
  const [jobId, setJobId] = useState<string | null>(null);
  const [conversationId, setConversationId] = useState<string | undefined>(undefined);
  /** 接管状态来自全局 store（§8.6）：顶部横幅与这里读的是同一份。 */
  const takeover = useUi((s) => s.takeover);
  const scrollRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight });
  }, [items]);

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
    const instruction = await promptText("AI 接管任务描述（例如：安装 nginx 并启动）");
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

  if (!rightOpen) {
    return (
      <button
        className="h-full w-8 shrink-0 rounded text-neutral-400 hover:bg-neutral-800"
        onClick={() => setRightOpen(true)}
        title="展开 AI 助手"
      >
        ◂
      </button>
    );
  }

  return (
    <div className="flex h-full w-[380px] shrink-0 flex-col border-l border-neutral-800 bg-neutral-950/60">
      <div className="flex items-center gap-2 border-b border-neutral-800 px-3 py-2 text-sm">
        <span className="font-medium text-neutral-200">AI 助手</span>
        {aiBusy && <span className="h-2 w-2 animate-pulse rounded-full bg-blue-500" />}
        <div className="flex-1" />
        {takeover && (
          <span className="rounded bg-red-500/20 px-2 py-0.5 text-[10px] text-red-300">
            接管中（顶部横幅可夺回）
          </span>
        )}
        <button
          className="rounded px-1.5 text-xs text-neutral-400 hover:bg-neutral-800"
          onClick={() => setRightOpen(false)}
        >
          ✕
        </button>
      </div>
      <div ref={scrollRef} className="min-h-0 flex-1 space-y-2 overflow-y-auto p-3">
        {items.length === 0 && (
          <div className="mt-10 text-center text-xs text-neutral-600">
            问点什么，比如
            <div className="mt-2 text-neutral-500">「nginx 起不来，帮我排查」</div>
          </div>
        )}
        {items.map((item, i) => (
          <ChatBubble key={i} item={item} />
        ))}
      </div>
      {confirmCard && (
        <div className="m-2 rounded border border-amber-600/50 bg-amber-500/10 p-2 text-xs">
          <div className="mb-1 font-medium text-amber-300">⚠ 需要确认</div>
          <pre className="mb-2 max-h-24 overflow-auto whitespace-pre-wrap text-neutral-300">
            {confirmCard.rendered}
          </pre>
          <div className="flex gap-1">
            <button className="rounded bg-green-600 px-2 py-1 text-white" onClick={() => void confirm("allow")}>
              允许一次
            </button>
            <button
              className="rounded bg-green-700/70 px-2 py-1 text-white"
              onClick={() => void confirm("allow_session")}
            >
              本会话允许此类
            </button>
            <button className="rounded bg-neutral-700 px-2 py-1 text-white" onClick={() => void confirm("deny")}>
              拒绝
            </button>
          </div>
        </div>
      )}
      <div className="border-t border-neutral-800 p-2">
        {tabId && (
          <button
            className="mb-2 w-full rounded border border-red-500/40 px-2 py-1 text-xs text-red-300 hover:bg-red-500/10"
            onClick={() => void runTakeover()}
            disabled={aiBusy}
          >
            🎮 AI 接管此终端
          </button>
        )}
        <div className="flex items-end gap-1">
          <textarea
            className="max-h-32 min-h-[38px] flex-1 resize-none rounded bg-neutral-800 px-2 py-1.5 text-sm outline-none focus:ring-1 focus:ring-blue-500"
            placeholder={sessionId ? "询问当前会话 / 请求排障…" : "未连接会话（仍可全局提问）"}
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
            className="rounded bg-blue-600 px-3 py-2 text-sm text-white hover:bg-blue-500 disabled:opacity-40"
            disabled={aiBusy || !input.trim()}
            onClick={() => void send()}
          >
            发送
          </button>
        </div>
      </div>
    </div>
  );
}

function ChatBubble({ item }: { item: ChatItem }) {
  if (item.role === "user") {
    return (
      <div className="ml-8 rounded-lg rounded-br-sm bg-blue-600/80 px-3 py-1.5 text-sm text-white">
        {item.text}
      </div>
    );
  }
  if (item.role === "assistant") {
    return (
      <div className="rounded-lg rounded-bl-sm bg-neutral-800 px-3 py-1.5 text-sm text-neutral-200">
        <pre className="whitespace-pre-wrap font-sans">{item.text}</pre>
      </div>
    );
  }
  if (item.role === "reasoning") {
    return (
      <div className="ml-2 border-l-2 border-neutral-700 pl-2 text-xs italic text-neutral-500">
        {item.text.length > 200 ? `${item.text.slice(0, 200)}…` : item.text}
      </div>
    );
  }
  if (item.role === "tool") {
    return (
      <div className="rounded border border-neutral-800 bg-neutral-900/80 px-2 py-1.5 text-xs">
        <div className="flex items-center gap-1">
          <span className="rounded bg-purple-500/20 px-1.5 py-0.5 text-[10px] text-purple-300">
            {item.name}
          </span>
          <span className="text-neutral-400">{item.display}</span>
          {"ok" in item && item.ok !== undefined && (
            <span className={item.ok ? "text-green-400" : "text-red-400"}>
              {item.ok ? "✓" : "✕"} {item.exitCode !== null && item.exitCode !== undefined ? `(${item.exitCode})` : ""}
            </span>
          )}
        </div>
        {item.summary && (
          <pre className="mt-1 max-h-28 overflow-auto whitespace-pre-wrap text-neutral-500">
            {item.summary}
          </pre>
        )}
      </div>
    );
  }
  return null;
}
