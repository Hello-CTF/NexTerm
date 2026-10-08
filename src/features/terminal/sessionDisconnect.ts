import { sessionApi, terminalApi, type LiveTabInfo } from "../../ipc/commands";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";

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

export async function runningTerminalCount(sessionId: string): Promise<number> {
  const local = localRunningTabs(sessionId);
  let listed: LiveTabInfo[];
  try {
    listed = await terminalApi.listLive();
  } catch {
    return local.length;
  }
  const sessionTabs = listed.filter((t) => t.sessionId === sessionId);
  const listedIds = new Set(sessionTabs.map((t) => t.tabId));
  const live = sessionTabs.filter((t) => !t.exited);
  return live.length + local.filter((tabId) => !listedIds.has(tabId)).length;
}

export async function disconnectSessionWithConfirm(
  sessionId: string,
  sessionName: string,
): Promise<void> {
  const n = await runningTerminalCount(sessionId);
  const ok = await ask(
    n > 0
      ? `断开「${sessionName}」？\n\n连接断开后，${n} 个正在运行的终端里：普通终端的进程会被结束，无法恢复；守护终端会留在「后台会话」，之后可接管。`
      : `断开「${sessionName}」？`,
    { title: "断开连接", kind: "warning" },
  );
  if (!ok) return;
  const { pushToast } = useUi.getState();
  try {
    await sessionApi.disconnect(sessionId);
    pushToast("info", "已断开连接");
  } catch (e) {
    pushToast("error", `断开失败：${describeError(e)}`);
  }
}
