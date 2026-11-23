import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ChatBubble, ToolGroupSummary } from "../panel/AiMessageBubbles";
import { PLAN_EXECUTE_MESSAGE } from "../panel/aiSidebarShared";
import { type ToolGroup, groupToolRuns, pendingConfirmCallIds, toolNeedsAttention } from "../toolGroups";
import { findItemMatches, stepMatch } from "../conversationSearch";
import { IconAlert, IconChevronDown, IconChevronUp, IconRefresh, IconSearch } from "../../../ui/icons";
import type { ConversationRecord } from "./useConversationRecord";
import { t } from "../../../i18n";
import { isImeEnter } from "../../../ui/imeEnter";

// Long conversations show their newest items first; earlier ones load on request.
export const READ_WINDOW = 300;

const noop = () => {};

// ConversationReadView renders a saved conversation with the dock's bubbles and tool groups, without any action that
// would change it: no composer, no edit, retry, plan approval or confirmation.
export function ConversationReadView({ record, running }: { record: ConversationRecord; running: boolean }) {
  const { items, loading, error, reload } = record;
  const [windowSize, setWindowSize] = useState(READ_WINDOW);
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(-1);
  const [expandedGroups, setExpandedGroups] = useState<ReadonlySet<string>>(() => new Set());
  const scrollRef = useRef<HTMLDivElement>(null);
  const start = Math.max(0, items.length - windowSize);
  const groupAt = useMemo(() => {
    const out = new Map<number, ToolGroup>();
    for (const group of groupToolRuns(items)) {
      for (let index = group.start; index < group.end; index++) out.set(index, group);
    }
    return out;
  }, [items]);
  const pendingConfirms = useMemo(() => pendingConfirmCallIds(items), [items]);
  const matches = useMemo(() => findItemMatches(items, query), [items, query]);
  const activeMatch = cursor >= 0 && cursor < matches.length ? matches[cursor] : -1;

  useEffect(() => setCursor(-1), [query]);

  const jump = useCallback(
    (direction: 1 | -1) => {
      if (matches.length === 0) return;
      const next = stepMatch(cursor, matches.length, direction);
      setCursor(next);
      const index = matches[next];
      if (index < start) setWindowSize(items.length - index);
      requestAnimationFrame(() => {
        scrollRef.current?.querySelector(`[data-read-index="${index}"]`)?.scrollIntoView?.({ block: "center" });
      });
    },
    [cursor, items.length, matches, start],
  );

  const toggleGroup = useCallback((key: string) => {
    setExpandedGroups((prev) => {
      const next = new Set(prev);
      if (!next.delete(key)) next.add(key);
      return next;
    });
  }, []);

  if (error && items.length === 0) {
    return (
      <div className="flex flex-col items-center gap-2 p-6 text-center">
        <IconAlert size={16} className="text-red-300" />
        <div className="text-xs text-red-300">{t("未能读取对话：{value}", { value: error })}</div>
        <button type="button" className="nx-btn nx-btn-outline nx-btn-xs" onClick={reload}>
          <IconRefresh size={11} />
          {t("重试")}
        </button>
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-1 border-b border-neutral-800/60 px-2 py-1">
        <IconSearch size={12} className="shrink-0 text-neutral-500" />
        <input
          className="nx-input nx-input-sm min-w-0 flex-1"
          placeholder={t("在此对话中搜索，Enter 下一个")}
          aria-label={t("在此对话中搜索")}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && !isImeEnter(event)) {
              event.preventDefault();
              jump(event.shiftKey ? -1 : 1);
            }
          }}
        />
        <span className="shrink-0 font-mono text-[10.5px] text-neutral-500" aria-live="polite">
          {query.trim() ? (matches.length > 0 ? `${cursor >= 0 ? cursor + 1 : 0}/${matches.length}` : t("无匹配")) : ""}
        </span>
        <button type="button" className="nx-icon-btn shrink-0" aria-label={t("上一个匹配")} disabled={matches.length === 0} onClick={() => jump(-1)}>
          <IconChevronUp size={12} />
        </button>
        <button type="button" className="nx-icon-btn shrink-0" aria-label={t("下一个匹配")} disabled={matches.length === 0} onClick={() => jump(1)}>
          <IconChevronDown size={12} />
        </button>
      </div>
      <div ref={scrollRef} role="log" aria-label={t("AI 对话记录（只读）")} className="min-h-0 flex-1 overflow-y-auto p-3">
        {loading ? <div className="nx-hint py-6 text-center">{t("正在读取对话…")}</div> : null}
        {!loading && items.length === 0 ? <div className="nx-hint py-6 text-center">{t("此对话还没有保存的消息")}</div> : null}
        {start > 0 ? (
          <div className="mb-2 flex justify-center">
            <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" onClick={() => setWindowSize((size) => size + READ_WINDOW)}>
              {t("加载更早消息")}
            </button>
          </div>
        ) : null}
        {items.slice(start).map((item, offset) => {
          const index = start + offset;
          const group = groupAt.get(index);
          const expanded = group ? expandedGroups.has(group.key) : true;
          const showSummary = group !== undefined && index === group.start;
          const showItem = expanded || index === activeMatch || (item.role === "tool" && toolNeedsAttention(item, pendingConfirms));
          if (!showItem && !showSummary) return null;
          const planApproved =
            item.role === "plan" &&
            items.some((candidate, candidateIndex) => candidateIndex > index && candidate.role === "user" && candidate.text.trim() === PLAN_EXECUTE_MESSAGE);
          const bubble = showItem ? (
            <ChatBubble
              item={item}
              streaming={false}
              planApproved={planApproved}
              onApprovePlan={noop}
              onRetry={noop}
              onEdit={noop}
              agentToolCall={null}
              onAgentApproval={noop}
              agentApprovalBusy={false}
              readOnly
            />
          ) : null;
          return (
            <div
              key={item.id}
              data-read-index={index}
              className={[offset > 0 ? "mt-2.5" : null, index === activeMatch ? "rounded-xl ring-2 ring-amber-400/70" : null].filter(Boolean).join(" ") || undefined}
            >
              {showSummary && group ? (
                <>
                  <ToolGroupSummary group={group} expanded={expanded} onToggle={toggleGroup} />
                  {bubble ? <div className="mt-2.5">{bubble}</div> : null}
                </>
              ) : (
                bubble
              )}
            </div>
          );
        })}
        {running ? (
          <div role="status" className="mt-3 rounded-lg border border-amber-500/30 bg-amber-500/[0.08] px-2.5 py-2 text-[11.5px] text-[var(--nx-fg-warning)]">
            {t("AI 仍在运行，这里只显示已保存的进度。确认操作和停止请在侧边栏中进行。")}
          </div>
        ) : null}
      </div>
    </div>
  );
}
