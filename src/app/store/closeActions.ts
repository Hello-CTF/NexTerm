import { dbApi, terminalApi, type SessionInfo } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import type { ChoiceOption } from "../../ui/dialogs";
import { dirtyFileEditors } from "../../features/files/editorGuards";
import { runningAiTabs as getRunningAiTabs, type AiCloseTab } from "../../features/ai/runOwnership";
import { getTerminalPrefs, setTerminalCloseAction, type TerminalCloseAction } from "../preferences";
import { GLOBAL_AI_BOARD_KEY } from "./model";
import { findTab } from "./tabActions";
import type { AppTab, CloseTabReport, SqlDraft, WorkspaceKind } from "./types";
import { useUi } from "./useUi";

export async function confirmDirtyEditors(tabs: AppTab[], title: string): Promise<boolean> {
  const dirty = dirtyFileEditors(tabs, useUi.getState().sessions);
  if (dirty.length === 0) return true;
  const { ask } = await import("../../ui/dialogs");
  const names = dirty.map((tab) => tab.title).join("、");
  return ask(`有 ${dirty.length} 个文件尚未保存：${names}\n关闭后会丢失这些改动。仍要继续？`, {
    title,
    kind: "warning",
  });
}

function isTerminalCloseCandidate(t: AppTab): boolean {
  return t.kind === "terminal" && Boolean(t.tabId) && !t.dead;
}

function isRunningTerminal(t: AppTab): boolean {
  return isTerminalCloseCandidate(t) && !t.exited;
}

function detachBlockOf(
  t: AppTab,
  sessions: SessionInfo[],
  assetKind?: string,
): "exec" | "winrm" | "local" | "regular" | "unknown" | null {
  if (t.containerId) return "exec";
  const kind = t.sessionId ? sessions.find((s) => s.id === t.sessionId)?.kind : undefined;
  if (kind === "winrm" || (!kind && assetKind === "winrm")) return "winrm";
  // A regular SSH PTY ends with the connection, so it never offers "background".
  // durable is undefined until the backend reports it; retainable decides until then.
  // Until the durable flag arrives, a remote tab that reports itself as not retainable is a regular PTY too.
  if ((kind === "ssh" || (!kind && assetKind === "ssh")) && (t.durable === false || (t.durable === undefined && t.retainable === false))) return "regular";
  if (t.retainable === true) return null;
  if (kind === "local" || (!kind && assetKind === "local")) return "local";
  return "unknown";
}

function detachBlockLabel(block: "exec" | "winrm" | "local" | "regular" | "unknown"): string {
  if (block === "exec") return "容器 exec";
  if (block === "regular") return "普通";
  if (block === "winrm") return "WinRM 非交互";
  if (block === "local") return "本地";
  return "其他";
}

function assetKindForTab(t: AppTab): string | undefined {
  for (const w of useUi.getState().workspaces) {
    for (const p of w.panes) {
      if (p.tabs.some((x) => x.id === t.id)) return w.assetKind;
    }
  }
  return undefined;
}

export function countBlockedTerminals(tabs: AppTab[], assetKind?: string): number {
  const sessions = useUi.getState().sessions;
  return tabs.filter((t) => isRunningTerminal(t) && detachBlockOf(t, sessions, assetKind) !== null)
    .length;
}

interface ClosePreferencePlan {
  detachable: AppTab[];
  preferredKills: AppTab[];
  confirmBlocked: boolean;
}

export interface WorkspaceCloseSummary extends ClosePreferencePlan {
  tabs: AppTab[];
  dirty: AppTab[];
  blocked: AppTab[];
  blockedKinds: string;
  cleanup: AppTab[];
  dbConnIds: string[];
  hasSqlDraft: boolean;
  sqlDraft?: SqlDraft;
  runningAiTabs: AiCloseTab[];
  aiBoardKey: string;
  aiTabIds: Set<string>;
}

interface WorkspaceCloseOutcome {
  detached: number;
  killed: number;
  // Kills a host that cannot be reached could not perform: the tabs closed locally and the processes still run.
  killUnreachable: number;
  failed: number;
  failureText?: string;
  dbFailed: number;
  dbError?: string;
  succeededTabIds: Set<string>;
  succeededDbConnIds: Set<string>;
}

async function applyClosePreference(plan: ClosePreferencePlan, title: string): Promise<boolean> {
  const action = getTerminalPrefs().closeAction;
  plan.confirmBlocked = action !== "kill";
  if (plan.detachable.length === 0 || action === "detach") return true;
  if (action === "kill") {
    plan.preferredKills = plan.detachable;
    plan.detachable = [];
    return true;
  }
  const { askChoiceRemember } = await import("../../ui/dialogs");
  const result = await askChoiceRemember(
    `${plan.detachable.length} 个终端的进程正在运行。\n转到后台后，可在「后台会话」中随时接管；直接关闭会结束进程。`,
    { title, choices: CLOSE_CHOICE_OPTIONS },
  );
  if (!result) return false;
  if (result.key === "kill") {
    plan.preferredKills = plan.detachable;
    plan.detachable = [];
  }
  return true;
}

export function collectWorkspaceClose(
  tabs: AppTab[],
  assetKind: string | undefined,
  workspaceKind: WorkspaceKind,
  workspaceId: string,
): WorkspaceCloseSummary {
  const { sessions, workspaceRuntime } = useUi.getState();
  const live = tabs.filter(isRunningTerminal);
  const blocked = live.filter((t) => detachBlockOf(t, sessions, assetKind) !== null);
  const blockedKinds = [
    ...new Set(
      blocked
        .map((t) => detachBlockOf(t, sessions, assetKind))
        .filter((b): b is "exec" | "winrm" | "local" | "unknown" => b !== null),
    ),
  ]
    .map(detachBlockLabel)
    .join("、");
  const runningAiTabs = getRunningAiTabs(workspaceKind, workspaceId);
  const aiBoardKey = workspaceKind === "tools" ? GLOBAL_AI_BOARD_KEY : workspaceId;
  return {
    tabs,
    dirty: dirtyFileEditors(tabs, sessions),
    detachable: live.filter((t) => detachBlockOf(t, sessions, assetKind) === null),
    preferredKills: [],
    confirmBlocked: false,
    blocked,
    blockedKinds,
    cleanup: tabs.filter((t) => t.kind === "terminal" && t.tabId && !t.dead && t.exited),
    dbConnIds: [
      ...new Set(tabs.filter((t) => t.kind === "db" && t.connId).map((t) => t.connId as string)),
    ],
    hasSqlDraft: Boolean(workspaceRuntime[workspaceId]?.sqlDraft?.text.trim()),
    sqlDraft: workspaceRuntime[workspaceId]?.sqlDraft,
    runningAiTabs,
    aiBoardKey,
    aiTabIds: new Set((useUi.getState().aiBoards[aiBoardKey]?.tabs ?? []).map((tab) => tab.id)),
  };
}

// confirmWorkspaceClose merges unsaved content and terminal close consequences into one confirmation;
// on cancel, no IPC is issued and no tab is closed.
export async function confirmWorkspaceClose(
  summary: WorkspaceCloseSummary,
  title: string,
  detachOnly = false,
): Promise<boolean> {
  if (detachOnly) {
    summary.preferredKills = [];
    summary.confirmBlocked = false;
  } else if (!(await applyClosePreference(summary, `关闭「${title}」`))) return false;
  const confirmBlocked = summary.blocked.length > 0 && summary.confirmBlocked;
  if (summary.dirty.length === 0 && !confirmBlocked && !summary.hasSqlDraft && summary.runningAiTabs.length === 0) return true;
  const lines: string[] = [];
  if (summary.dirty.length > 0) {
    lines.push(
      `· ${summary.dirty.length} 个文件尚未保存，关闭后会丢失改动：${summary.dirty.map((t) => t.title).join("、")}`,
    );
  }
  if (summary.hasSqlDraft) {
    lines.push("· 此工作区仍有未保存的 SQL 草稿，关闭后将丢失");
  }
  if (summary.runningAiTabs.length > 0) {
    lines.push(`· ${summary.runningAiTabs.length} 个 AI 标签仍在运行：${summary.runningAiTabs.map((tab) => tab.title).join("、")}`);
  }
  if (confirmBlocked) {
    lines.push(`· ${summary.blocked.length} 个${summary.blockedKinds}终端不支持转入后台，将结束进程`);
  }
  if (summary.preferredKills.length > 0) {
    lines.push(`· ${summary.preferredKills.length} 个运行中的终端将结束进程`);
  }
  if (summary.detachable.length > 0) {
    lines.push(`· ${summary.detachable.length} 个运行中的终端将转入后台`);
  }
  const { ask } = await import("../../ui/dialogs");
  return ask(`${lines.join("\n")}\n仍要继续？`, { title: `关闭「${title}」`, kind: "warning" });
}

export async function executeWorkspaceClose(summary: WorkspaceCloseSummary): Promise<WorkspaceCloseOutcome> {
  const operations: { tab: AppTab; mode?: "kill" | "detach" }[] = [
    ...summary.detachable.map((tab) => ({ tab, mode: "detach" as const })),
    ...summary.preferredKills.map((tab) => ({ tab, mode: "kill" as const })),
    ...summary.blocked.map((tab) => ({ tab, mode: "kill" as const })),
    ...summary.cleanup.map((tab) => ({ tab, mode: undefined })),
  ];
  const results = await Promise.all(
    operations.map(async ({ tab, mode }) => {
      const report: CloseTabReport = {};
      const ok = await useUi.getState().closeTab(tab.id, mode, true, report);
      return { tab, ok, killSkipped: report.remoteKillSkipped === true };
    }),
  );
  const failed = results.filter((result) => !result.ok);
  const dbResults = failed.length === 0
    ? await Promise.allSettled(summary.dbConnIds.map((connId) => dbApi.disconnect(connId)))
    : [];
  void useUi.getState().resyncSessions({ silent: true });
  const dbFailed = dbResults.filter((r): r is PromiseRejectedResult => r.status === "rejected");
  const killStart = summary.detachable.length;
  const killEnd = killStart + summary.preferredKills.length + summary.blocked.length;
  return {
    detached: results.filter((r, i) => r.ok && i < killStart).length,
    killed: results.filter((r, i) => r.ok && !r.killSkipped && i >= killStart && i < killEnd).length,
    killUnreachable: results.filter((r) => r.ok && r.killSkipped).length,
    failed: failed.length,
    failureText: failed[0] ? findTab(failed[0].tab.id)?.tab.closeError : undefined,
    dbFailed: dbFailed.length,
    dbError: dbFailed[0] ? describeError(dbFailed[0].reason) : undefined,
    succeededTabIds: new Set(results.filter((result) => result.ok).map((result) => result.tab.id)),
    succeededDbConnIds: new Set(
      summary.dbConnIds.filter((_, index) => dbResults[index]?.status === "fulfilled"),
    ),
  };
}

// pushWorkspaceCloseToast emits one summary toast per close: an error if anything failed, otherwise one info toast only when terminals were affected.
export function pushWorkspaceCloseToast(title: string, outcome: WorkspaceCloseOutcome, deleted: boolean): void {
  const { pushToast } = useUi.getState();
  const parts: string[] = [];
  if (outcome.detached > 0) parts.push(`${outcome.detached} 个终端转入后台，可在「后台会话」接管`);
  if (outcome.killed > 0) parts.push(`${outcome.killed} 个进程已结束`);
  if (outcome.failed > 0) {
    pushToast("error", `关闭「${title}」：${outcome.failed} 个终端操作失败：${outcome.failureText}`);
    return;
  }
  if (outcome.killUnreachable > 0) {
    // The kill was skipped, not performed: say so as a warning, never inside the success summary.
    parts.push(
      `${outcome.killUnreachable} 个终端所在主机已断开或无法访问，远端进程未能结束，仍在「后台会话」中，重新连接后可接管或结束`,
    );
    pushToast("error", `${deleted ? `已关闭「${title}」` : `已关闭部分标签「${title}」`}：${parts.join("，")}`);
    return;
  }
  if (parts.length === 0) return;
  pushToast("info", `${deleted ? `已关闭「${title}」` : `已关闭部分标签「${title}」`}：${parts.join("，")}`);
}

export function closeActionHint(t: AppTab): string | undefined {
  if (t.kind !== "terminal" || !t.tabId || t.dead || t.exited) return undefined;
  const block = detachBlockOf(t, useUi.getState().sessions, assetKindForTab(t));
  if (block === "exec") return "结束容器 exec 进程";
  if (block === "winrm") return "结束 WinRM 非交互进程";
  if (block === "local") return "结束本地进程";
  if (block === "regular" || block === "unknown") return "结束进程";
  const action = getTerminalPrefs().closeAction;
  if (action === "kill") return "结束进程";
  if (action === "detach") return "转入后台运行";
  return "转入后台运行或结束进程";
}

export function closeTabHint(t: AppTab): string {
  if (t.kind !== "terminal" || !t.tabId || t.dead) return "关闭标签";
  if (t.exited) return "关闭标签（进程已结束）";
  const action = closeActionHint(t);
  return action ? `关闭标签（${action}）` : "关闭标签";
}

const CLOSE_CHOICE_OPTIONS: ChoiceOption[] = [
  { key: "detach", label: "转到后台运行", hint: "进程继续运行，可在「后台会话」随时接管", primary: true },
  { key: "kill", label: "直接关闭", hint: "结束进程，无法恢复", danger: true },
];

// decided is only passed by batch close: undefined = dispatch by preference, null = batch choice canceled (keep running terminals), anything else is the unified decision.
async function requestCloseTabWithAction(
  id: string,
  decided: TerminalCloseAction | null | undefined,
): Promise<void> {
  const st = useUi.getState();
  const found = findTab(id);
  if (!found) return;
  const { tab: target, ws } = found;
  if (!(await confirmDirtyEditors([target], `关闭「${target.title}」`))) return;
  if (target.kind !== "terminal" || !target.tabId || target.dead) {
    await st.closeTab(id);
    return;
  }
  if (target.exited) {
    await st.closeTab(id, decided === "kill" ? "kill" : undefined);
    return;
  }
  if (decided === null) return;
  const block = detachBlockOf(target, st.sessions, ws.assetKind);
  if (block) {
    const action = decided ?? getTerminalPrefs().closeAction;
    if (block === "regular" && (decided !== undefined || action !== "kill")) {
      // A regular SSH terminal has a single outcome, so offer only the red close action.
      const { askChoice } = await import("../../ui/dialogs");
      const choice = await askChoice(`「${target.title}」是普通终端，进程随连接结束，不能转到后台。`, {
        title: `关闭「${target.title}」`,
        level: "warning",
        choices: [{ key: "kill", label: "直接关闭", hint: "结束进程，无法恢复", danger: true }],
      });
      if (choice !== "kill") return;
    } else if (decided !== undefined || action !== "kill") {
      const { ask } = await import("../../ui/dialogs");
      const ok = await ask(
        `「${target.title}」是${detachBlockLabel(block)}终端，不支持转入后台。\n关闭会结束这个进程，仍要关闭？`,
        { title: `关闭「${target.title}」`, kind: "warning" },
      );
      if (!ok) return;
    }
    await st.closeTab(id, "kill");
    return;
  }
  let action = decided;
  if (action === undefined) {
    action = getTerminalPrefs().closeAction;
    if (action === "ask") {
      const { askChoiceRemember } = await import("../../ui/dialogs");
      const result = await askChoiceRemember(
        `「${target.title}」的进程正在运行。\n转到后台后，可在「后台会话」中随时接管；直接关闭会结束进程。`,
        {
          title: `关闭「${target.title}」`,
          rememberLabel: "以后都按此选择",
          choices: CLOSE_CHOICE_OPTIONS,
        },
      );
      if (!result) return;
      action = result.key === "kill" ? "kill" : "detach";
      if (result.remember) setTerminalCloseAction(action);
    }
  }
  if (action === "kill") {
    await st.closeTab(id, "kill");
    return;
  }
  if (await st.closeTab(id, "detach")) {
    st.pushToast("info", `「${target.title}」已转入后台，可在「后台会话」接管`);
  }
}

export async function reconcileTerminalCloseState(ids: string[]): Promise<void> {
  const candidates = ids
    .map((id) => findTab(id)?.tab)
    .filter((tab): tab is AppTab => tab !== undefined && isTerminalCloseCandidate(tab));
  if (candidates.length === 0) return;
  const currentCandidate = (candidate: AppTab): AppTab | undefined => {
    const current = findTab(candidate.id)?.tab;
    if (!current || current.tabId !== candidate.tabId || current.sessionId !== candidate.sessionId) return undefined;
    return isTerminalCloseCandidate(current) ? current : undefined;
  };
  const markUnknown = () => {
    for (const candidate of candidates) {
      if (currentCandidate(candidate)) useUi.getState().updateTab(candidate.id, { exited: undefined });
    }
  };
  let live: Awaited<ReturnType<typeof terminalApi.listLive>>;
  try {
    live = await terminalApi.listLive();
  } catch {
    markUnknown();
    return;
  }
  const byId = new Map(live.map((tab) => [tab.tabId, tab]));
  const remoteSessions = [...new Set(candidates.filter((tab) => {
    const kind = useUi.getState().sessions.find((session) => session.id === tab.sessionId)?.kind
      ?? findTab(tab.id)?.ws.assetKind;
    return !byId.has(tab.tabId as string) && !tab.containerId && kind === "ssh" && tab.sessionId;
  }).map((tab) => tab.sessionId as string))];
  const results = await Promise.allSettled(remoteSessions.map((id) => terminalApi.listDurable(id)));
  const remote = new Map(remoteSessions.map((id, index) => {
    const result = results[index];
    return [id, result.status === "fulfilled" ? new Map(result.value.map((tab) => [tab.id, tab])) : null] as const;
  }));
  for (const candidate of candidates) {
    if (!currentCandidate(candidate)) continue;
    const hit = byId.get(candidate.tabId as string);
    if (hit) {
      useUi.getState().updateTab(candidate.id, { exited: hit.exited });
      continue;
    }
    const found = findTab(candidate.id);
    const kind = useUi.getState().sessions.find((session) => session.id === candidate.sessionId)?.kind
      ?? found?.ws.assetKind;
    if (!candidate.containerId && kind === "device") {
      useUi.getState().updateTab(candidate.id, { exited: undefined });
      continue;
    }
    if (!candidate.containerId && kind === "ssh") {
      const descriptors = remote.get(candidate.sessionId as string);
      if (!descriptors) {
        useUi.getState().updateTab(candidate.id, { exited: undefined });
        continue;
      }
      const descriptor = descriptors.get(candidate.tabId as string);
      if (descriptor) {
        useUi.getState().updateTab(candidate.id, { exited: descriptor.dead });
        continue;
      }
    }
    useUi.getState().updateTab(candidate.id, { exited: undefined });
  }
}

export async function requestCloseTab(id: string): Promise<void> {
  await reconcileTerminalCloseState([id]);
  await requestCloseTabWithAction(id, undefined);
}

export async function requestCloseTabs(ids: string[]): Promise<void> {
  await reconcileTerminalCloseState(ids);
  const st = useUi.getState();
  for (const id of ids) {
    if (findTab(id)?.tab.closeError) st.updateTab(id, { closeError: undefined });
  }
  let decided: TerminalCloseAction | null | undefined;
  if (getTerminalPrefs().closeAction === "ask") {
    const pending = ids.filter((id) => {
      const found = findTab(id);
      return (
        found !== null &&
        isRunningTerminal(found.tab) &&
        !detachBlockOf(found.tab, st.sessions, found.ws.assetKind)
      );
    });
    if (pending.length > 0) {
      const { askChoiceRemember } = await import("../../ui/dialogs");
      const result = await askChoiceRemember(
        `${pending.length} 个终端的进程正在运行。\n转到后台后，可在「后台会话」中随时接管；直接关闭会结束进程。`,
        { title: "关闭终端标签", choices: CLOSE_CHOICE_OPTIONS },
      );
      decided = result ? (result.key === "kill" ? "kill" : "detach") : null;
    }
  }
  for (const id of ids) {
    await requestCloseTabWithAction(id, decided);
  }
  const failed = ids.filter((id) => findTab(id)?.tab.closeError).length;
  if (failed > 0) {
    useUi.getState().pushToast("error", `${failed} 个标签未能关闭，已保留并在标签上标出原因`);
  }
}

export async function requestKillTab(id: string): Promise<void> {
  const st = useUi.getState();
  const found = findTab(id);
  if (!found) return;
  const { tab: target } = found;
  if (target.kind !== "terminal" || !target.tabId || target.dead) {
    await st.closeTab(id);
    return;
  }
  const { ask } = await import("../../ui/dialogs");
  const ok = await ask(`结束「${target.title}」里的进程？\n\n进程会被终止，无法恢复。`, {
    title: "结束进程",
    kind: "warning",
  });
  if (!ok) return;
  const report: CloseTabReport = {};
  if ((await st.closeTab(id, "kill", false, report)) && !report.remoteKillSkipped) {
    st.pushToast("success", `已结束「${target.title}」的进程`);
  }
}

export async function requestKillWorkspaceTerminals(workspaceId: string): Promise<void> {
  const before = useUi.getState().workspaces.find((w) => w.id === workspaceId);
  if (!before) return;
  const st = useUi.getState();
  const live = before.panes.flatMap((p) => p.tabs).filter(isRunningTerminal);
  if (live.length === 0) {
    st.pushToast("info", "这个工作区里没有正在运行的终端");
    return;
  }
  const { ask } = await import("../../ui/dialogs");
  const ok = await ask(`结束「${before.title}」里 ${live.length} 个终端的进程？\n\n进程会被终止，无法恢复。`, {
    title: "结束全部终端进程",
    kind: "warning",
  });
  if (!ok) return;
  // The dialog can stay open for a while; resolve session kinds from the state at confirmation time.
  const { sessions } = useUi.getState();
  const results = await Promise.allSettled(
    live.map((tab) => {
      const kind = sessions.find((session) => session.id === tab.sessionId)?.kind ?? before.assetKind;
      return !tab.containerId && (kind === "ssh" || kind === "device")
        ? terminalApi.closeTab(tab.tabId as string, "kill", tab.sessionId)
        : terminalApi.closeTab(tab.tabId as string, "kill");
    }),
  );
  const failed = results.filter((r) => r.status === "rejected").length;
  if (failed) {
    st.pushToast("error", `${failed}/${live.length} 个终端结束失败`);
  } else {
    st.pushToast("success", `已结束 ${live.length} 个终端进程`);
  }
  live.forEach((t, i) => {
    const result = results[i];
    if (result.status === "fulfilled") {
      st.updateTab(t.id, { exited: true, closeError: undefined });
    } else {
      st.updateTab(t.id, { closeError: describeError(result.reason) });
    }
  });
  void st.resyncSessions({ silent: true });
}
