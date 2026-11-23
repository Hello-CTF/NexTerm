import type { AiConversationSummaryDto } from "../../../ipc/types";
import { IconRefresh } from "../../../ui/icons";
import type { ConversationPages } from "./useConversationPages";
import { conversationStatusBadge, conversationStatusLabel, conversationTitle } from "./workbenchShared";
import { formatDateTime, t } from "../../../i18n";

export function ConversationList({
  pages,
  selectedId,
  onSelect,
  hostLabel,
  emptyText,
}: {
  pages: ConversationPages;
  selectedId: string | null;
  onSelect: (item: AiConversationSummaryDto) => void;
  // hostLabel names each row's host; omitted in a view scoped to one host.
  hostLabel?: (item: AiConversationSummaryDto) => string;
  emptyText: string;
}) {
  const { items, loading, error, hasMore, loadMore, reload, total } = pages;
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {error ? (
        <div className="px-3 py-3 text-center">
          <div className="text-[12px] text-red-300" role="alert">{t("未能读取对话列表：{value}", { value: error })}</div>
          <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs mt-1.5" onClick={reload}>
            <IconRefresh size={11} />
            {t("重试")}
          </button>
        </div>
      ) : null}
      {!error && !loading && items.length === 0 ? <div className="nx-hint px-3 py-6 text-center">{emptyText}</div> : null}
      <ul className="min-h-0 flex-1 overflow-y-auto p-1" aria-label={t("AI 对话列表")}>
        {items.map((item) => (
          <li key={item.id}>
            <button
              type="button"
              className={`nx-menu-item w-full flex-col !items-stretch gap-0.5 !py-1.5 ${item.id === selectedId ? "is-active" : ""}`}
              aria-current={item.id === selectedId ? "true" : undefined}
              onClick={() => onSelect(item)}
            >
              <span className="flex min-w-0 items-center gap-1.5">
                <span className="min-w-0 flex-1 truncate text-left">{conversationTitle(item.title)}</span>
                {item.status !== "idle" ? (
                  <span className={`nx-badge shrink-0 ${conversationStatusBadge(item.status)}`}>{conversationStatusLabel(item.status)}</span>
                ) : null}
              </span>
              <span className="nx-hint flex min-w-0 items-center gap-1.5 text-[11px]">
                <span className="shrink-0">{formatDateTime(item.updatedAt, { dateStyle: "short", timeStyle: "short" })}</span>
                {hostLabel ? <span className="min-w-0 truncate">{hostLabel(item)}</span> : null}
              </span>
            </button>
          </li>
        ))}
        {hasMore ? (
          <li className="flex justify-center py-1.5">
            <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={loading} onClick={loadMore}>
              {t("加载更多（已显示 {shown} / {total}）", { shown: items.length, total })}
            </button>
          </li>
        ) : null}
        {loading && items.length === 0 ? <li className="nx-hint px-3 py-6 text-center">{t("正在读取…")}</li> : null}
      </ul>
    </div>
  );
}
