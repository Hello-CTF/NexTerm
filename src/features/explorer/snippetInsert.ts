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

export function activeTerminalTabId(): string | null {
  const st = useUi.getState();
  const ws =
    st.workspaces.find((w) => w.id === st.activeWorkspaceId) ??
    st.workspaces[st.workspaces.length - 1] ??
    null;
  const pane = ws?.panes.find((p) => p.id === ws.activePaneId) ?? ws?.panes[0] ?? null;
  const tab = pane?.tabs.find((t) => t.id === pane.activeTabId) ?? null;
  if (!tab || tab.kind !== "terminal" || !tab.tabId) return null;
  return tab.tabId;
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

export async function insertSnippet(s: Snippet): Promise<boolean> {
  const pushToast = useUi.getState().pushToast;
  const tabId = activeTerminalTabId();
  if (!tabId) {
    pushToast("info", "请先在当前工作区打开一个终端，再插入片段");
    return false;
  }
  const risk = insertRisk(s.body);
  if (risk === "execute") {
    const n = s.body.match(/[\r\n]/g)?.length ?? 0;
    const ok = await ask(
      `片段「${s.name}」包含 ${n} 处回车/换行：插入时每一处都会立即提交执行（相当于替你按回车）。\n仍要插入吗？`,
      { kind: "warning" },
    );
    if (!ok) return false;
  } else if (risk === "control") {
    const ok = await ask(
      `片段「${s.name}」包含终端控制字符（Tab 补全 / DEL / Ctrl-C / Ctrl-D / ESC 序列等）：\n插入不会替你执行命令行，但 Tab 补全钩子本身就是 shell 代码（不需要回车就会运行），其他控制字符也可能触发 readline/终端绑定动作、中断前台进程或改变终端状态。\n仍要插入吗？`,
      { kind: "warning" },
    );
    if (!ok) return false;
  }
  try {
    await terminalApi.write(tabId, new TextEncoder().encode(s.body));
    pushToast(
      "success",
      risk === "none"
        ? `已插入「${s.name}」 · 未执行，确认后回车运行`
        : risk === "execute"
          ? `已插入「${s.name}」 · 已按原样写入，其中回车/换行处已逐行执行`
          : `已插入「${s.name}」 · 已按原样写入，控制字符可能已改变终端状态`,
    );
    return true;
  } catch (e) {
    pushToast("error", `插入失败：${describeError(e)}`);
    return false;
  }
}
