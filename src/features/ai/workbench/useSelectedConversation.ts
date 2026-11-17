import { useCallback, useEffect, useState } from "react";
import { aiApi } from "../../../ipc/commands";
import type { AiConversationSummaryDto } from "../../../ipc/types";
import type { ConversationPages } from "./useConversationPages";
import { RUNNING_REFRESH_MS, useConversationRecord, type ConversationRecord } from "./useConversationRecord";

export interface SelectedConversation {
  summary: AiConversationSummaryDto | null;
  record: ConversationRecord;
  select: (item: AiConversationSummaryDto | null) => void;
  // deleted drops a conversation deleted from the view from the selection and the list.
  deleted: (id: string) => void;
}

// useSelectedConversation keeps the selected conversation, loads only its messages, and while it runs re-reads its
// summary so the status shown follows the run without reloading the list.
export function useSelectedConversation(pages: ConversationPages, visible: boolean): SelectedConversation {
  const [summary, setSummary] = useState<AiConversationSummaryDto | null>(null);
  const listed = summary ? pages.items.find((item) => item.id === summary.id) : undefined;
  const current = listed ?? summary;
  const running = current?.status === "running";
  const record = useConversationRecord(current?.id ?? null, running && visible);
  const { patch, remove } = pages;
  const id = current?.id;

  useEffect(() => {
    if (!id || !running || !visible) return;
    const timer = setInterval(() => {
      aiApi.conversationOverview({ ids: [id], limit: 1 }).then(
        (result) => {
          const fresh = result?.items?.[0];
          if (!fresh) return;
          setSummary((previous) => (previous?.id === fresh.id ? fresh : previous));
          patch(fresh);
        },
        () => {},
      );
    }, RUNNING_REFRESH_MS);
    return () => clearInterval(timer);
  }, [id, running, visible, patch]);

  const deleted = useCallback(
    (deletedId: string) => {
      remove(deletedId);
      setSummary((previous) => (previous?.id === deletedId ? null : previous));
    },
    [remove],
  );
  return { summary: current, record, select: setSummary, deleted };
}
