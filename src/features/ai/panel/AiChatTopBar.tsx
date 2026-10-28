import { IconSearch, IconClock, IconHistory, IconChevronUp, IconChevronDown, IconClose, IconRefresh, IconTrash } from "../../../ui/icons";
import type { AiChatCtx } from "./aiChatCtx";

export function AiChatTopBar({ ctx }: { ctx: AiChatCtx }) {
  const { searchOpen, setSearchOpen, backgroundTaskTotal, tasksOpen, setTasksOpen, historyOpen, toggleHistory, searchQuery, setSearchQuery, jumpToMatch, searchMatches, matchCursor, historyStatus, historyError, loadConversations, conversations, openConversation, runStatusByConv, deleteConversation } = ctx;
  return (
    <>
      <div className="flex h-[32px] shrink-0 items-center gap-1 border-b border-neutral-800/60 px-2 [@media(max-height:480px)]:h-7">
        <div className="nx-spacer" />
        <button
          className={`nx-icon-btn nx-icon-btn-sm ${searchOpen ? "is-active" : ""}`}
          title="搜索对话内容"
          aria-label="搜索对话内容"
          aria-expanded={searchOpen}
          onClick={() => setSearchOpen((v) => !v)}
        >
          <IconSearch size={14} />
        </button>
        {(backgroundTaskTotal > 0 || tasksOpen) && (
          <button
            className={`nx-btn nx-btn-ghost nx-btn-xs ${tasksOpen ? "is-active" : ""}`}
            title="AI 留下的后台任务：定时任务、监视、子代理，可在这里查看和停止"
            aria-label={`后台任务，共 ${backgroundTaskTotal} 项`}
            aria-expanded={tasksOpen}
            onClick={() => setTasksOpen((v) => !v)}
          >
            <IconClock size={13} />
            后台任务
            <span className="nx-badge nx-badge-amber">{backgroundTaskTotal}</span>
          </button>
        )}
        <button
          className={`nx-btn nx-btn-ghost nx-btn-xs ${historyOpen ? "is-active" : ""}`}
          title="历史会话"
          aria-expanded={historyOpen}
          onClick={toggleHistory}
        >
          <IconHistory size={13} />
          历史
        </button>
      </div>

      {searchOpen && (
        <div className="flex shrink-0 items-center gap-1 border-b border-neutral-800/60 px-2 py-1">
          <input
            className="nx-input nx-input-sm min-w-0 flex-1"
            placeholder="搜索对话内容，Enter 下一个，Shift+Enter 上一个"
            aria-label="搜索对话内容"
            value={searchQuery}
            autoFocus
            onChange={(e) => setSearchQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                jumpToMatch(e.shiftKey ? -1 : 1);
              } else if (e.key === "Escape") {
                e.preventDefault();
                setSearchOpen(false);
              }
            }}
          />
          <span className="shrink-0 font-mono text-[10.5px] text-neutral-500" aria-live="polite">
            {searchQuery.trim()
              ? searchMatches.length > 0
                ? `${matchCursor >= 0 ? matchCursor + 1 : 0}/${searchMatches.length}`
                : "无匹配"
              : ""}
          </span>
          <button
            className="nx-icon-btn shrink-0"
            title="上一个匹配（Shift+Enter）"
            aria-label="上一个匹配"
            disabled={searchMatches.length === 0}
            onClick={() => jumpToMatch(-1)}
          >
            <IconChevronUp size={12} />
          </button>
          <button
            className="nx-icon-btn shrink-0"
            title="下一个匹配（Enter）"
            aria-label="下一个匹配"
            disabled={searchMatches.length === 0}
            onClick={() => jumpToMatch(1)}
          >
            <IconChevronDown size={12} />
          </button>
          <button
            className="nx-icon-btn shrink-0"
            title="关闭搜索"
            aria-label="关闭搜索"
            onClick={() => setSearchOpen(false)}
          >
            <IconClose size={11} />
          </button>
        </div>
      )}

      {historyOpen && (
        <div className="max-h-[40%] shrink-0 overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60 p-1.5">
          {historyStatus === "loading" ? (
            <div className="nx-hint px-2 py-3 text-center text-[11.5px]">正在加载历史会话…</div>
          ) : historyStatus === "error" ? (
            <div className="px-2 py-3 text-center">
              <div className="nx-hint text-red-300">无法加载历史会话：{historyError}</div>
              <button
                className="nx-btn nx-btn-ghost nx-btn-xs mt-1.5"
                onClick={() => void loadConversations()}
              >
                <IconRefresh size={11} />
                重试
              </button>
            </div>
          ) : conversations.length === 0 ? (
            <div className="nx-hint px-2 py-3 text-center text-[11.5px]">尚无历史会话</div>
          ) : (
            conversations.map((c) => (
              <div key={c.id} className="flex items-center gap-0.5">
                <button
                  className="nx-menu-item flex-1"
                  title={c.title || "（未命名会话）"}
                  onClick={() => void openConversation(c.id, c.title)}
                >
                  <span className="nx-menu-label">{c.title || "（未命名会话）"}</span>
                  <span className="nx-menu-hint">
                    {c.updatedAt ? new Date(c.updatedAt).toLocaleDateString() : ""}
                  </span>
                </button>
                {runStatusByConv[c.id] && (
                  <span
                    className={`nx-badge shrink-0 ${runStatusByConv[c.id] === "running" ? "nx-badge-amber" : "nx-badge-purple"}`}
                    title={runStatusByConv[c.id] === "running" ? "此会话有正在运行的 AI 任务" : "此会话有被中断、待恢复的 AI 任务"}
                  >
                    {runStatusByConv[c.id] === "running" ? "运行中" : "待恢复"}
                  </span>
                )}
                <button
                  className="nx-icon-btn nx-icon-btn-sm shrink-0"
                  title={`删除「${c.title || "（未命名会话）"}」`}
                  aria-label={`删除会话「${c.title || "（未命名会话）"}」`}
                  onClick={() => void deleteConversation(c)}
                >
                  <IconTrash size={12} />
                </button>
              </div>
            ))
          )}
        </div>
      )}
    </>
  );
}
