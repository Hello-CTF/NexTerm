import { useCallback, useEffect, useRef, useState } from "react";
import { aiApi } from "../../../ipc/commands";
import type { AiConversationFilter, AiConversationOverviewDto, AiConversationSummaryDto } from "../../../ipc/types";
import { describeError } from "../../../ui/errorText";

export const CONVERSATION_PAGE_SIZE = 50;

export interface ConversationPages {
  items: AiConversationSummaryDto[];
  total: number;
  hosts: AiConversationOverviewDto["hosts"];
  loading: boolean;
  error: string | null;
  hasMore: boolean;
  loadMore: () => void;
  reload: () => void;
  remove: (id: string) => void;
  // patch replaces a listed conversation with a fresher summary of it.
  patch: (item: AiConversationSummaryDto) => void;
}

// useConversationPages lists conversations page by page from the backend, which filters and counts them, so the
// view only holds the pages it has shown. A visible view refreshes when it becomes visible again.
export function useConversationPages(filter: AiConversationFilter | null, visible: boolean): ConversationPages {
  const [items, setItems] = useState<AiConversationSummaryDto[]>([]);
  const [total, setTotal] = useState(0);
  const [hosts, setHosts] = useState<AiConversationOverviewDto["hosts"]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [revision, setRevision] = useState(0);
  const generationRef = useRef(0);
  const key = filter ? JSON.stringify(filter) : null;

  const fetchPage = useCallback(
    (offset: number, replace: boolean) => {
      if (!key) return;
      const generation = replace ? ++generationRef.current : generationRef.current;
      setLoading(true);
      aiApi.conversationOverview({ ...(JSON.parse(key) as AiConversationFilter), offset, limit: CONVERSATION_PAGE_SIZE }).then(
        (result) => {
          if (generation !== generationRef.current) return;
          const page = result?.items ?? [];
          setItems((previous) => {
            if (replace) return page;
            const seen = new Set(previous.map((item) => item.id));
            return [...previous, ...page.filter((item) => !seen.has(item.id))];
          });
          setTotal(result?.total ?? 0);
          setHosts(result?.hosts ?? []);
          setError(null);
          setLoading(false);
        },
        (e: unknown) => {
          if (generation !== generationRef.current) return;
          setError(describeError(e));
          setLoading(false);
        },
      );
    },
    [key],
  );

  useEffect(() => {
    if (!visible) return;
    fetchPage(0, true);
  }, [fetchPage, visible, revision]);

  const loadMore = useCallback(() => fetchPage(items.length, false), [fetchPage, items.length]);
  const reload = useCallback(() => setRevision((value) => value + 1), []);
  const remove = useCallback((id: string) => {
    setItems((previous) => previous.filter((item) => item.id !== id));
    setTotal((value) => Math.max(0, value - 1));
  }, []);
  const patch = useCallback((item: AiConversationSummaryDto) => {
    setItems((previous) => previous.map((candidate) => (candidate.id === item.id ? item : candidate)));
  }, []);
  return { items, total, hosts, loading, error, hasMore: items.length < total, loadMore, reload, remove, patch };
}
