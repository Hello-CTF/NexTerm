import { ask } from "../../ui/dialogs";
import { terminalApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";

export interface Snippet {
  id: string;
  name: string;
  body: string;
  groupId: string | null;
  sort: number;
}

export type SnippetAction = "insert" | "execute";

const ACTION_LABEL: Record<SnippetAction, string> = {
  insert: "插入",
  execute: "执行",
};

type TerminalTarget = { tabId: string } | { blocked: string };

function terminalTarget(): TerminalTarget {
  const st = useUi.getState();
  const ws =
    st.workspaces.find((w) => w.id === st.activeWorkspaceId) ??
    st.workspaces[st.workspaces.length - 1] ??
    null;
  const pane = ws?.panes.find((p) => p.id === ws.activePaneId) ?? ws?.panes[0] ?? null;
  const tab = pane?.tabs.find((t) => t.id === pane.activeTabId) ?? null;
  if (!tab || tab.kind !== "terminal" || !tab.tabId) {
    return { blocked: "请先在当前工作区打开一个终端" };
  }
  if (tab.dead || tab.exited) {
    return { blocked: "当前终端已结束" };
  }
  return { tabId: tab.tabId };
}

export type InsertRisk = "none" | "execute" | "control";

export function insertRisk(body: string): InsertRisk {
  for (const ch of body) {
    const c = ch.codePointAt(0) ?? 0;
    if (c === 0x0a || c === 0x0d) return "execute";
  }
  for (const ch of body) {
    const c = ch.codePointAt(0) ?? 0;
    if (c < 0x20 || (c >= 0x7f && c <= 0x9f)) return "control";
  }
  return "none";
}

async function confirmRisk(s: Snippet, risk: InsertRisk, action: SnippetAction): Promise<boolean> {
  const label = ACTION_LABEL[action];
  if (risk === "execute") {
    const n = s.body.match(/[\r\n]/g)?.length ?? 0;
    return ask(
      `片段「${s.name}」包含 ${n} 处回车或换行，${label}时会逐行提交执行。仍要${label}吗？`,
      { kind: "warning" },
    );
  }
  if (risk === "control") {
    return ask(
      `片段「${s.name}」包含终端控制字符，可能执行 shell 代码、中断前台进程或改变终端状态。仍要${label}吗？`,
      { kind: "warning" },
    );
  }
  return true;
}

function isNotController(e: unknown): boolean {
  return (
    typeof e === "object" &&
    e !== null &&
    (e as { code?: unknown }).code === "not_controller"
  );
}

export async function runSnippetAction(s: Snippet, action: SnippetAction): Promise<boolean> {
  const pushToast = useUi.getState().pushToast;
  const label = ACTION_LABEL[action];
  const target = terminalTarget();
  if ("blocked" in target) {
    pushToast("info", `${target.blocked}，无法${label}片段`);
    return false;
  }
  const risk = insertRisk(s.body);
  if (risk !== "none" && !(await confirmRisk(s, risk, action))) return false;
  const payload =
    action === "execute" && !/[\r\n]$/.test(s.body) ? s.body + "\r" : s.body;
  try {
    await terminalApi.write(target.tabId, new TextEncoder().encode(payload));
    pushToast(
      "success",
      action === "insert"
        ? risk === "none"
          ? `已插入「${s.name}」，按回车执行`
          : risk === "execute"
            ? `已插入「${s.name}」 · 回车或换行处已逐行执行`
            : `已插入「${s.name}」 · 控制字符可能已改变终端状态`
        : risk === "none"
          ? `已执行「${s.name}」`
          : risk === "execute"
            ? `已执行「${s.name}」 · 回车或换行处已逐行执行`
            : `已执行「${s.name}」 · 控制字符可能已改变终端状态`,
    );
    return true;
  } catch (e) {
    if (isNotController(e)) {
      pushToast("error", `${label}失败：当前终端由其他设备控制，请先在终端上获取控制权`);
    } else {
      pushToast("error", `${label}失败：${describeError(e)}`);
    }
    return false;
  }
}

export function insertSnippet(s: Snippet): Promise<boolean> {
  return runSnippetAction(s, "insert");
}

export function executeSnippet(s: Snippet): Promise<boolean> {
  return runSnippetAction(s, "execute");
}
