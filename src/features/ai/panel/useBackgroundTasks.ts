import { useCallback, useEffect, useRef, useState } from "react";
import { cronApi } from "../../../ipc/cron";
import { watchPanelApi } from "../../../ipc/watchApi";
import type { AiChatBase } from "./aiSidebarShared";
import type { UseChatRunSessionOut } from "./useChatRunSession";
import type { UseChatActionsOut } from "./useChatActions";

export interface BackgroundTaskCounts {
  cron: number;
  watch: number;
}

const EMPTY: BackgroundTaskCounts = { cron: 0, watch: 0 };
const POLL_MS = 15_000;

async function loadCounts(conversationId: string | undefined, watchOwner: string): Promise<BackgroundTaskCounts> {
  // Each source fails independently: an unreadable list counts as empty here
  // and surfaces its own error once the panel is opened.
  const [cron, watch] = await Promise.all([
    (async () => (conversationId ? (await cronApi.list(conversationId)).length : 0))().catch(() => 0),
    (async () => (await watchPanelApi.listWatches(watchOwner)).filter((w) => w.status === "armed").length)().catch(
      () => 0,
    ),
  ]);
  return { cron, watch };
}

// useBackgroundTasks counts what the AI left running for this conversation
// (scheduled jobs and armed watches). It refreshes when the
// conversation changes, when a run settles, when the panel toggles, and on a
// slow poll while the chat is visible, so the top bar badge shows how many
// items there are to manage; the entry itself is always offered.
export function useBackgroundTasks(p: AiChatBase & UseChatRunSessionOut & UseChatActionsOut) {
  const { conversationId, watchOwner, busy, rightOpen, active } = p;
  const [counts, setCounts] = useState<BackgroundTaskCounts>(EMPTY);
  const [tasksOpen, setTasksOpen] = useState(false);
  const genRef = useRef(0);
  const visible = Boolean(rightOpen && active);

  const refreshBackgroundTasks = useCallback(async () => {
    const gen = ++genRef.current;
    const next = await loadCounts(conversationId, watchOwner);
    if (gen !== genRef.current) return;
    setCounts((prev) =>
      prev.cron === next.cron && prev.watch === next.watch ? prev : next,
    );
  }, [conversationId, watchOwner]);

  useEffect(() => {
    if (!visible) return;
    void refreshBackgroundTasks();
    const timer = window.setInterval(() => void refreshBackgroundTasks(), POLL_MS);
    const gen = genRef;
    return () => {
      window.clearInterval(timer);
      gen.current++;
    };
  }, [visible, busy, refreshBackgroundTasks, tasksOpen]);

  // Leaving the conversation (new or deleted) is the user's own action and the
  // panel's content belongs to it, so it closes then. Switching straight to
  // another conversation keeps the panel and remounts its sections.
  useEffect(() => {
    if (!conversationId) setTasksOpen(false);
  }, [conversationId]);

  // The panel never closes by itself: only the user's toggle closes it, so an
  // empty list shows an empty state instead of vanishing mid-read.
  const backgroundTaskTotal = counts.cron + counts.watch;

  return { backgroundTasks: counts, backgroundTaskTotal, tasksOpen, setTasksOpen, refreshBackgroundTasks };
}

export type UseBackgroundTasksOut = ReturnType<typeof useBackgroundTasks>;
