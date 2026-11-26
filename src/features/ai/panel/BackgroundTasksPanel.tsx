import { useEffect, useState } from "react";
import { CronPanel } from "../CronPanel";
import { WatchPanel } from "../WatchPanel";
import { watchPanelApi } from "../../../ipc/watchApi";
import type { AiChatCtx } from "./aiChatCtx";
import { t } from "../../../i18n";

// BackgroundTasksPanel is the one place to see and stop what the AI left
// running: scheduled jobs and armed watches. The schedule section is always
// shown for a conversation, so its jobs can be created and managed before the
// AI has made any. The watch section is hidden while it has never had
// anything, but once shown it stays for as long as the panel is open, so
// finishing or cancelling the last item shows an empty state instead of making
// the section vanish.
export function BackgroundTasksPanel({ ctx }: { ctx: AiChatCtx }) {
  const { conversationId, activeWorkspace, backgroundTasks, refreshBackgroundTasks, setTasksOpen, watchOwner } = ctx;
  const refresh = () => void refreshBackgroundTasks();
  const [shownWatch, setShownWatch] = useState(false);
  useEffect(() => {
    if (backgroundTasks.watch > 0) setShownWatch(true);
  }, [backgroundTasks]);
  const showCron = Boolean(conversationId);
  const showWatch = shownWatch || backgroundTasks.watch > 0;
  return (
    <section
      aria-label={t("后台任务")}
      className="max-h-[60%] shrink-0 overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60"
    >
      {!showCron && !showWatch && (
        <div className="nx-hint px-3 py-3 text-center text-[11.5px]">
          {t("暂无后台任务。AI 设置的定时任务和监视会显示在这里。")}
        </div>
      )}
      {showCron && conversationId && (
        <CronPanel
          conversationId={conversationId}
          hostName={activeWorkspace?.kind === "session" ? activeWorkspace.title : undefined}
          onClose={() => setTasksOpen(false)}
          onJobsChanged={refresh}
        />
      )}
      {showWatch && <WatchPanel api={watchPanelApi} owner={watchOwner} onChanged={refresh} />}
    </section>
  );
}
