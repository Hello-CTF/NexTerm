import { useEffect, useState } from "react";
import { CronPanel } from "../CronPanel";
import { AgentRosterPanel } from "../AgentRosterPanel";
import { WatchPanel } from "../WatchPanel";
import { watchPanelApi } from "../../../ipc/watchApi";
import type { AiChatCtx } from "./aiChatCtx";

// BackgroundTasksPanel is the one place to see and stop what the AI left
// running: scheduled jobs, armed watches and sub-agent terminals. A section is
// hidden while it has never had anything, but once shown it stays for as long
// as the panel is open, so finishing or cancelling the last item shows an
// empty state instead of making the section (or the panel) vanish.
export function BackgroundTasksPanel({ ctx }: { ctx: AiChatCtx }) {
  const {
    conversationId,
    activeWorkspace,
    backgroundTasks,
    refreshBackgroundTasks,
    setTasksOpen,
    agentRosterApi,
    mentionAgentHandle,
    openAgentTerminal,
    watchOwner,
  } = ctx;
  const refresh = () => void refreshBackgroundTasks();
  const [shown, setShown] = useState({ cron: false, watch: false, agents: false });
  useEffect(() => {
    setShown((prev) => {
      const next = {
        cron: prev.cron || backgroundTasks.cron > 0,
        watch: prev.watch || backgroundTasks.watch > 0,
        agents: prev.agents || backgroundTasks.agents > 0,
      };
      return next.cron === prev.cron && next.watch === prev.watch && next.agents === prev.agents ? prev : next;
    });
  }, [backgroundTasks]);
  const showCron = (shown.cron || backgroundTasks.cron > 0) && Boolean(conversationId);
  const showWatch = shown.watch || backgroundTasks.watch > 0;
  const showAgents = (shown.agents || backgroundTasks.agents > 0) && Boolean(conversationId) && Boolean(agentRosterApi);
  return (
    <section
      aria-label="后台任务"
      className="max-h-[60%] shrink-0 overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60"
    >
      {!showCron && !showWatch && !showAgents && (
        <div className="nx-hint px-3 py-3 text-center text-[11.5px]">
          暂无后台任务。AI 设置的定时任务、监视和子代理会显示在这里。
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
      {showAgents && conversationId && agentRosterApi && (
        // Keyed by conversation: a switch remounts the panel, so the composer
        // can never mention the previous conversation's rows.
        <AgentRosterPanel
          key={`roster-${conversationId}`}
          api={agentRosterApi}
          onMention={mentionAgentHandle}
          onOpened={openAgentTerminal}
          onChanged={refresh}
        />
      )}
    </section>
  );
}
