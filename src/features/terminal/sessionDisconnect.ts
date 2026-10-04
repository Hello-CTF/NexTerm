import { sessionApi } from "../../ipc/commands";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";

export function runningTerminalCount(sessionId: string): number {
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
    ).length;
}

export async function disconnectSessionWithConfirm(
  sessionId: string,
  sessionName: string,
): Promise<void> {
  const n = runningTerminalCount(sessionId);
  const ok = await ask(
    n > 0
      ? `断开「${sessionName}」？\n\n连接断开后，${n} 个正在运行的终端进程会被结束，无法恢复。`
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
