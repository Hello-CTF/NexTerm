import type { AiConversationSummaryDto } from "../../../ipc/types";
import { IconBot, IconDownload, IconPanelRight, IconTrash } from "../../../ui/icons";
import { ConversationReadView } from "./ConversationReadView";
import type { ConversationRecord } from "./useConversationRecord";
import {
  conversationStatusBadge,
  conversationStatusLabel,
  conversationTitle,
  deleteConversationGuarded,
  exportConversationMarkdown,
  openConversationInDock,
  releaseDeletedConversation,
} from "./workbenchShared";
import { t } from "../../../i18n";

// ConversationPane is the read-only middle of the workbench: the selected conversation with its light actions.
// Continuing, confirming and stopping happen in the AI dock.
export function ConversationPane({
  summary,
  hostLabel,
  record,
  onDeleted,
}: {
  summary: AiConversationSummaryDto | null;
  hostLabel: string;
  record: ConversationRecord;
  onDeleted: (id: string) => void;
}) {
  if (!summary) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center">
        <span className="nx-empty-icon">
          <IconBot size={19} />
        </span>
        <div className="nx-hint">{t("选择左侧的会话查看内容")}</div>
      </div>
    );
  }
  const running = summary.status === "running";
  const remove = async () => {
    if (await deleteConversationGuarded(summary)) {
      releaseDeletedConversation(summary.id);
      onDeleted(summary.id);
    }
  };
  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col" data-testid="ai-workbench-conversation">
      <div className="flex shrink-0 flex-wrap items-center gap-1.5 border-b border-neutral-800/60 px-3 py-2">
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-semibold text-neutral-100" title={conversationTitle(summary.title)}>
            {conversationTitle(summary.title)}
          </div>
          <div className="nx-hint flex items-center gap-1.5 truncate">
            <span className={`nx-badge ${conversationStatusBadge(summary.status)}`}>{conversationStatusLabel(summary.status)}</span>
            <span className="truncate">{hostLabel}</span>
          </div>
        </div>
        <button
          type="button"
          className="nx-btn nx-btn-primary nx-btn-xs"
          title={t("在右侧 AI 侧边栏打开此会话，继续对话")}
          onClick={() => openConversationInDock(summary.id, summary.title)}
        >
          <IconPanelRight size={12} />
          {t("在侧边栏继续")}
        </button>
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-xs"
          title={t("将此会话导出为 Markdown 文件")}
          onClick={() => void exportConversationMarkdown(summary, hostLabel)}
        >
          <IconDownload size={12} />
          {t("导出 Markdown")}
        </button>
        <button
          type="button"
          className="nx-icon-btn nx-icon-btn-sm"
          title={t("删除会话")}
          aria-label={t("删除会话「{value}」", { value: conversationTitle(summary.title) })}
          onClick={() => void remove()}
        >
          <IconTrash size={12} />
        </button>
      </div>
      <ConversationReadView record={record} running={running} />
    </div>
  );
}
