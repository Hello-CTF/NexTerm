import { ask } from "../../ui/dialogs";
import { terminalApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import type { SnippetStep } from "../../ipc/types";
import { t as tr } from "../../i18n";

export interface Snippet {
  id: string;
  name: string;
  body: string;
  groupId: string | null;
  sort: number;
  description?: string;
  // Present and non-empty for a multi-step snippet; the commands are already rendered when it is run.
  steps?: SnippetStep[];
}

export function isMultiStep(s: Pick<Snippet, "steps">): boolean {
  return (s.steps?.length ?? 0) > 0;
}

export type SnippetAction = "insert" | "execute";

const ACTION_LABEL: Record<SnippetAction, string> = {
  insert: tr("插入"),
  execute: tr("执行"),
};

type TerminalTarget = { tabId: string } | { blocked: string };

export function terminalTarget(): TerminalTarget {
  const st = useUi.getState();
  const ws =
    st.workspaces.find((w) => w.id === st.activeWorkspaceId) ??
    st.workspaces[st.workspaces.length - 1] ??
    null;
  const pane = ws?.panes.find((p) => p.id === ws.activePaneId) ?? ws?.panes[0] ?? null;
  const tab = pane?.tabs.find((t) => t.id === pane.activeTabId) ?? null;
  if (!tab || tab.kind !== "terminal" || !tab.tabId) {
    return { blocked: tr("请先打开一个终端") };
  }
  if (tab.dead || tab.exited) {
    return { blocked: tr("当前终端已断开或已结束") };
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
    return ask(
      tr("片段「{name}」包含回车或换行，{label}时会逐行提交执行。仍要{label2}吗？", { name: s.name, label, label2: label }),
      { kind: "warning" },
    );
  }
  if (risk === "control") {
    return ask(
      tr("片段「{name}」包含终端控制字符，可能执行 shell 代码、中断前台进程或改变终端状态。仍要{label}吗？", { name: s.name, label }),
      { kind: "warning" },
    );
  }
  return true;
}

// stepTargetState reports why the terminal a run started in can no longer take input, or null while it can.
function stepTargetState(tabId: string): string | null {
  for (const ws of useUi.getState().workspaces) {
    for (const pane of ws.panes) {
      const tab = pane.tabs.find((t) => t.kind === "terminal" && t.tabId === tabId);
      if (!tab) continue;
      return tab.dead || tab.exited ? tr("原终端已断开或已结束") : null;
    }
  }
  return tr("原终端已关闭");
}

async function runSteps(s: Snippet, steps: readonly SnippetStep[], tabId: string): Promise<boolean> {
  const pushToast = useUi.getState().pushToast;
  const epoch = useUi.getState().accountEpoch;
  if (steps.some((step) => insertRisk(step.command) === "control")) {
    if (!(await confirmRisk(s, "control", "execute"))) return false;
  }
  for (const [index, step] of steps.entries()) {
    const done = index;
    if (useUi.getState().accountEpoch !== epoch) return false;
    if (step.confirm) {
      const title = step.name.trim() ? `「${step.name.trim()}」` : "";
      const ok = await ask(
        tr("片段「{name}」第 {value} / {length} 步{title}将执行：\n{command}\n\n确认执行吗？", { name: s.name, value: index + 1, length: steps.length, title, command: step.command }),
        { kind: "warning" },
      );
      if (useUi.getState().accountEpoch !== epoch) return false;
      if (!ok) {
        pushToast("info", tr("已停止「{name}」：已执行到第 {done} 步", { name: s.name, done }));
        return false;
      }
    }
    const blocked = stepTargetState(tabId);
    if (blocked) {
      pushToast("info", tr("{blocked}，已停止「{name}」：已执行到第 {done} 步", { blocked, name: s.name, done }));
      return false;
    }
    const payload = /[\r\n]$/.test(step.command) ? step.command : step.command + "\r";
    try {
      await terminalApi.write(tabId, new TextEncoder().encode(payload));
    } catch (e) {
      pushToast("error", tr("执行失败：{value}（已执行到第 {done} 步）", { value: describeError(e), done }));
      return false;
    }
  }
  pushToast("success", tr("已执行「{name}」 · 共 {length} 步", { name: s.name, length: steps.length }));
  return true;
}

export async function runSnippetAction(s: Snippet, action: SnippetAction): Promise<boolean> {
  const pushToast = useUi.getState().pushToast;
  const label = ACTION_LABEL[action];
  if (isMultiStep(s)) {
    if (action === "insert") {
      pushToast("info", tr("多步片段只能执行，不能插入"));
      return false;
    }
    const first = terminalTarget();
    if ("blocked" in first) {
      pushToast("info", tr("{blocked}，才能{label}片段", { blocked: first.blocked, label }));
      return false;
    }
    return runSteps(s, s.steps ?? [], first.tabId);
  }
  const target = terminalTarget();
  if ("blocked" in target) {
    pushToast("info", tr("{blocked}，才能{label}片段", { blocked: target.blocked, label }));
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
          ? tr("已插入「{name}」，按回车执行", { name: s.name })
          : risk === "execute"
            ? tr("已插入「{name}」 · 回车或换行处已逐行执行", { name: s.name })
            : tr("已插入「{name}」 · 控制字符可能已改变终端状态", { name: s.name })
        : risk === "none"
          ? tr("已执行「{name}」", { name: s.name })
          : risk === "execute"
            ? tr("已执行「{name}」 · 回车或换行处已逐行执行", { name: s.name })
            : tr("已执行「{name}」 · 控制字符可能已改变终端状态", { name: s.name }),
    );
    return true;
  } catch (e) {
    pushToast("error", tr("{label}失败：{value}", { label, value: describeError(e) }));
    return false;
  }
}

export function insertSnippet(s: Snippet): Promise<boolean> {
  return runSnippetAction(s, "insert");
}

export function executeSnippet(s: Snippet): Promise<boolean> {
  return runSnippetAction(s, "execute");
}
