import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { aiApi } from "../../../ipc/commands";
import type { MessageDto } from "../../../ipc/types";
import { describeError } from "../../../ui/errorText";
import { createConversation, historyToItems, type ChatItem } from "../conversation";

// A running conversation keeps saving messages (tool calls and results, finished turns); the read-only view
// re-reads them at this pace so its progress shows without taking part in the run.
export const RUNNING_REFRESH_MS = 4000;

export interface ConversationRecord {
  items: ChatItem[];
  loading: boolean;
  error: string | null;
  reload: () => void;
}

// useConversationRecord loads the saved messages of one conversation as chat items, only while one is selected.
export function useConversationRecord(conversationId: string | null, running: boolean): ConversationRecord {
  const [messages, setMessages] = useState<MessageDto[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [revision, setRevision] = useState(0);
  const loadedIdRef = useRef<string | null>(null);

  useEffect(() => {
    if (!conversationId) {
      loadedIdRef.current = null;
      setMessages(null);
      setError(null);
      return;
    }
    let current = true;
    if (loadedIdRef.current !== conversationId) {
      loadedIdRef.current = conversationId;
      setMessages(null);
    }
    setLoading(true);
    aiApi.messages(conversationId).then(
      (rows) => {
        if (!current) return;
        setMessages(Array.isArray(rows) ? rows : []);
        setError(null);
        setLoading(false);
      },
      (e: unknown) => {
        if (!current) return;
        setError(describeError(e));
        setLoading(false);
      },
    );
    return () => {
      current = false;
    };
  }, [conversationId, revision]);

  useEffect(() => {
    if (!conversationId || !running) return;
    const timer = setInterval(() => setRevision((value) => value + 1), RUNNING_REFRESH_MS);
    return () => clearInterval(timer);
  }, [conversationId, running]);

  const items = useMemo(() => (messages ? historyToItems(createConversation(), messages) : []), [messages]);
  const reload = useCallback(() => setRevision((value) => value + 1), []);
  return { items, loading: loading && messages === null, error, reload };
}
