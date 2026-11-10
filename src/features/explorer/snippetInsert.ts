import { ask } from "../../ui/dialogs";
import { terminalApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import type { SnippetStep } from "../../ipc/types";
import { t as tr } from "../../i18n";
import { readTerminalScreen, subscribeTerminalOutput } from "../terminal/terminalOutputTap";
import {
  createOutputTail,
  createRegexMatcher,
  createTextMatcher,
  loadRegexEngine,
  regexPatternError,
  stepWaitTimeoutMs,
  waitForOutput,
  type OutputTail,
  type RegexEngine,
  type StepMatcher,
} from "./snippetWaitFor";
import { beginSnippetRun, type SnippetRunHandle } from "./snippetRuns";

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

function stepLabel(step: SnippetStep, index: number): string {
  const name = step.name.trim();
  return name ? tr("第 {value} 步「{name}」", { value: index + 1, name }) : tr("第 {value} 步", { value: index + 1 });
}

export interface SnippetRunOptions {
  // Called once when the run first waits for terminal output, so a dialog that started it can get out of the way.
  onWaiting?: () => void;
}

// Terminals with a snippet run that waits for output.
const waitingTabs = new Set<string>();

async function runSteps(s: Snippet, steps: readonly SnippetStep[], tabId: string, options: SnippetRunOptions): Promise<boolean> {
  const pushToast = useUi.getState().pushToast;
  const epoch = useUi.getState().accountEpoch;
  if (steps.some((step) => insertRisk(step.command) === "control")) {
    if (!(await confirmRisk(s, "control", "execute"))) return false;
  }
  if (!steps.some((step) => step.waitFor)) return sendSteps(s, steps, tabId, epoch, null, options);
  // Two runs waiting on one terminal would race for the same output.
  if (waitingTabs.has(tabId)) {
    pushToast("info", tr("当前终端已有片段正在等待输出，请等它结束或取消后再执行「{name}」", { name: s.name }));
    return false;
  }
  waitingTabs.add(tabId);
  try {
    return await runWaitingSteps(s, steps, tabId, epoch, options);
  } finally {
    waitingTabs.delete(tabId);
  }
}

async function runWaitingSteps(s: Snippet, steps: readonly SnippetStep[], tabId: string, epoch: number, options: SnippetRunOptions): Promise<boolean> {
  const pushToast = useUi.getState().pushToast;
  let engine: RegexEngine | null = null;
  if (steps.some((step) => step.waitFor && step.waitForRegex)) {
    try {
      engine = await loadRegexEngine();
    } catch (cause) {
      pushToast("error", tr("无法加载正则表达式引擎，未执行「{name}」：{value}", { name: s.name, value: describeError(cause) }));
      return false;
    }
    if (useUi.getState().accountEpoch !== epoch) return false;
  }
  // Wait patterns are checked before anything is sent, so a bad one never leaves the run half done.
  const matchers = new Map<number, StepMatcher>();
  for (const [index, step] of steps.entries()) {
    if (!step.waitFor) continue;
    if (step.waitForRegex && engine) {
      const problem = regexPatternError(engine, step.waitFor);
      if (problem !== null) {
        pushToast("error", tr("「{name}」{step}的等待条件不是有效的正则表达式，未执行任何步骤：{value}", { name: s.name, step: stepLabel(step, index), value: problem }));
        return false;
      }
      matchers.set(index, createRegexMatcher(engine, step.waitFor));
    } else {
      matchers.set(index, createTextMatcher(step.waitFor));
    }
  }
  // The first wait starts from the current screen; every later one sees only the output after the previous step.
  const tail = createOutputTail();
  tail.append(readTerminalScreen(tabId) ?? "");
  const unsubscribeOutput = subscribeTerminalOutput(tabId, tail.appendBytes);
  const abort = new AbortController();
  const run = beginSnippetRun({ name: s.name, total: steps.length, cancel: () => abort.abort() });
  // A closed or ended terminal and an account switch end a wait at once instead of after its timeout.
  const unsubscribeUi = useUi.subscribe((state) => {
    if (state.accountEpoch !== epoch || stepTargetState(tabId)) abort.abort();
  });
  try {
    return await sendSteps(s, steps, tabId, epoch, { tail, matchers, signal: abort.signal, run }, options);
  } finally {
    unsubscribeUi();
    unsubscribeOutput();
    run.end();
  }
}

interface StepWaits {
  tail: OutputTail;
  matchers: ReadonlyMap<number, StepMatcher>;
  signal: AbortSignal;
  run: SnippetRunHandle;
}

async function sendSteps(
  s: Snippet,
  steps: readonly SnippetStep[],
  tabId: string,
  epoch: number,
  waits: StepWaits | null,
  options: SnippetRunOptions,
): Promise<boolean> {
  const pushToast = useUi.getState().pushToast;
  let notifiedWaiting = false;
  for (const [index, step] of steps.entries()) {
    const done = index;
    if (useUi.getState().accountEpoch !== epoch) return false;
    const matcher = waits?.matchers.get(index);
    if (waits && matcher && step.waitFor) {
      waits.run.update({ step: index + 1, waitingFor: step.waitFor });
      if (!notifiedWaiting) {
        notifiedWaiting = true;
        options.onWaiting?.();
      }
      const timeoutMs = stepWaitTimeoutMs(step);
      const outcome = await waitForOutput(waits.tail, matcher, timeoutMs, waits.signal);
      waits.run.update({ waitingFor: null });
      if (useUi.getState().accountEpoch !== epoch) return false;
      if (outcome === "timeout") {
        pushToast("error", tr("「{name}」{step}等待输出「{pattern}」超时（{seconds} 秒），已停止：已执行到第 {done} 步", {
          name: s.name, step: stepLabel(step, index), pattern: step.waitFor, seconds: Math.round(timeoutMs / 100) / 10, done,
        }));
        return false;
      }
      if (outcome === "cancelled") {
        const blocked = stepTargetState(tabId);
        pushToast("info", blocked
          ? tr("{blocked}，已停止「{name}」：已执行到第 {done} 步", { blocked, name: s.name, done })
          : tr("已取消「{name}」：已执行到第 {done} 步", { name: s.name, done }));
        return false;
      }
    } else {
      waits?.run.update({ step: index + 1 });
    }
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
    // Cancelled while a confirmation was open.
    if (waits?.signal.aborted) {
      pushToast("info", tr("已取消「{name}」：已执行到第 {done} 步", { name: s.name, done }));
      return false;
    }
    const payload = /[\r\n]$/.test(step.command) ? step.command : step.command + "\r";
    // Output that arrives from here on belongs to this step.
    waits?.tail.clear();
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

export async function runSnippetAction(s: Snippet, action: SnippetAction, options: SnippetRunOptions = {}): Promise<boolean> {
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
    return runSteps(s, s.steps ?? [], first.tabId, options);
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

export function executeSnippet(s: Snippet, options?: SnippetRunOptions): Promise<boolean> {
  return runSnippetAction(s, "execute", options);
}
