import { useMemo, useState } from "react";
import { aiWorkbenchTab, useUi } from "../../../app/store";
import type { AiConversationFilter } from "../../../ipc/types";
import { IconDownload, IconGlobe, IconRefresh } from "../../../ui/icons";
import { useHostLabeler } from "../../hosts/hostLabel";
import { ConversationArchiveDialog } from "./ConversationArchiveDialog";
import { ConversationList } from "./ConversationList";
import { ConversationPane } from "./ConversationPane";
import { useConversationPages } from "./useConversationPages";
import { useSelectedConversation } from "./useSelectedConversation";
import { t } from "../../../i18n";

// HostAiHistoryPanel is the AI workbench of one host workspace: only the conversations whose scope names this host,
// without filters. "去到全局" opens the global workbench in the tools workspace with this host preselected.
export function HostAiHistoryPanel({ visible, assetId }: { visible: boolean; assetId?: string }) {
  const label = useHostLabeler();
  const hostName = assetId ? label(assetId) || assetId : "";
  const filter = useMemo<AiConversationFilter | null>(() => (assetId ? { assetId } : null), [assetId]);
  const pages = useConversationPages(filter, visible);
  const selection = useSelectedConversation(pages, visible);
  const [archiveOpen, setArchiveOpen] = useState(false);

  const openGlobal = () => useUi.getState().addTab(aiWorkbenchTab(assetId));

  return (
    <div className="flex h-full min-h-0 flex-col" data-testid="host-ai-history">
      <div className="flex shrink-0 items-center gap-1.5 border-b border-neutral-800/60 px-3 py-1.5">
        <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-neutral-200">
          {t("{host} 的 AI 会话（{count}）", { host: hostName, count: pages.total })}
        </span>
        <button type="button" className="nx-icon-btn nx-icon-btn-sm" title={t("刷新")} aria-label={t("刷新会话列表")} onClick={pages.reload}>
          <IconRefresh size={12} />
        </button>
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-xs"
          title={t("批量导出此主机的 AI 会话")}
          disabled={!assetId}
          onClick={() => setArchiveOpen(true)}
        >
          <IconDownload size={12} />
          {t("批量导出")}
        </button>
        <button
          type="button"
          className="nx-btn nx-btn-outline nx-btn-xs"
          title={t("在工具工作区打开全局 AI 工作台，可筛选全部主机的会话")}
          onClick={openGlobal}
        >
          <IconGlobe size={12} />
          {t("去到全局")}
        </button>
      </div>
      <div className="flex min-h-0 flex-1 flex-wrap">
        <div className="flex min-h-0 min-w-[220px] flex-[1_1_260px] flex-col border-r border-neutral-800/60">
          <ConversationList
            pages={pages}
            selectedId={selection.summary?.id ?? null}
            onSelect={(item) => selection.select(item)}
            emptyText={t("此主机还没有 AI 会话")}
          />
        </div>
        <div className="flex min-h-0 min-w-[320px] flex-[3_1_480px] flex-col">
          <ConversationPane summary={selection.summary} hostLabel={hostName} record={selection.record} onDeleted={selection.deleted} />
        </div>
      </div>
      {archiveOpen ? (
        <ConversationArchiveDialog
          assetId={assetId ?? null}
          hostName={hostName}
          allowAllHosts={false}
          onClose={() => setArchiveOpen(false)}
        />
      ) : null}
    </div>
  );
}
