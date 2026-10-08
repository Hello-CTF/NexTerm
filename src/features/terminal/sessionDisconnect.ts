import { sessionApi, terminalApi, type LiveTabInfo } from "../../ipc/commands";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import { isTabDurable } from "./durableTabs";

function localRunningTabs(sessionId: string): string[] {
  return useUi
    .getState()
    .workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs))
    .filter(
      (t) =>
        t.kind === "terminal" &&
        t.sessionId === sessionId &&
        t.tabId &&
        !t.dead &&
        !t.exited,
    )
    .map((t) => t.tabId as string);
}

export interface RunningTerminalCounts {
  ordinary: number;
  durable: number;
}

function countByDurability(tabIds: string[]): RunningTerminalCounts {
  let ordinary = 0;
  let durable = 0;
  for (const tabId of tabIds) {
    if (isTabDurable(tabId)) durable += 1;
    else ordinary += 1;
  }
  return { ordinary, durable };
}

export async function runningTerminalCounts(sessionId: string): Promise<RunningTerminalCounts> {
  const local = localRunningTabs(sessionId);
  let listed: LiveTabInfo[];
  try {
    listed = await terminalApi.listLive();
  } catch {
    return countByDurability(local);
  }
  const sessionTabs = listed.filter((t) => t.sessionId === sessionId);
  const listedIds = new Set(sessionTabs.map((t) => t.tabId));
  const live = sessionTabs.filter((t) => !t.exited).map((t) => t.tabId);
  return countByDurability([...live, ...local.filter((tabId) => !listedIds.has(tabId))]);
}

export async function disconnectSessionWithConfirm(
  sessionId: string,
  sessionName: string,
): Promise<void> {
  const { ordinary, durable } = await runningTerminalCounts(sessionId);
  let consequence = "";
  if (ordinary > 0 && durable > 0) {
    consequence = `\n\n连接断开后，${ordinary} 个普通终端进程会被结束，无法恢复；${durable} 个守护终端会留在「后台会话」，之后可接管。`;
  } else if (ordinary > 0) {
    consequence = `\n\n连接断开后，${ordinary} 个普通终端进程会被结束，无法恢复。`;
  } else if (durable > 0) {
    consequence = `\n\n连接断开后，${durable} 个守护终端会留在「后台会话」，进程不会被结束，之后可接管。`;
  }
  const ok = await ask(`断开「${sessionName}」？${consequence}`, {
    title: "断开连接",
    kind: "warning",
  });
  if (!ok) return;
  const { pushToast } = useUi.getState();
  try {
    await sessionApi.disconnect(sessionId);
    pushToast("info", "已断开连接");
  } catch (e) {
    pushToast("error", `断开失败：${describeError(e)}`);
  }
}
