import { dbApi, terminalApi, type SessionInfo } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import type { ChoiceOption } from "../../ui/dialogs";
import { dirtyFileEditors } from "../../features/files/editorGuards";
import { runningAiTabs as getRunningAiTabs, type AiCloseTab } from "../../features/ai/runOwnership";
import { getTerminalPrefs, setTerminalCloseAction, setTerminalConfirmTemporaryClose } from "../preferences";
import { GLOBAL_AI_BOARD_KEY, REMOTE_KILL_SKIPPED_TOAST } from "./model";
import { isUnconfirmedDurableExit } from "./durableExit";
import { findTab } from "./tabActions";
import { useRemoteStatus } from "../../features/remote/remoteStatus";
import type { AppTab, CloseTabReport, SqlDraft, Workspace, WorkspaceKind } from "./types";
import { useUi } from "./useUi";
import { t as tr } from "../../i18n";

export async function confirmDirtyEditors(tabs: AppTab[], title: string): Promise<boolean> {
  const dirty = dirtyFileEditors(tabs, useUi.getState().sessions);
  if (dirty.length === 0) return true;
  const { ask } = await import("../../ui/dialogs");
  const names = dirty.map((tab) => tab.title).join("、");
  return ask(tr("有 {length} 个文件尚未保存：{names}\n关闭后会丢失这些改动。仍要继续？", { length: dirty.length, names }), {
    title,
    kind: "warning",
    confirmLabel: tr("放弃改动"),
  });
}

function isTerminalCloseCandidate(t: AppTab): boolean {
  return t.kind === "terminal" && Boolean(t.tabId) && !t.dead;
}

// A connected remote desktop ends when its tab closes; there is no background to move it to.
export function isLiveRemoteTab(t: AppTab): boolean {
  return t.kind === "remote" && useRemoteStatus.getState().entries[t.id]?.status === "connected";
}

function isRunningTerminal(t: AppTab): boolean {
  return isTerminalCloseCandidate(t) && !t.exited;
}

// unattached is a local terminal tab that never attached: whether it holds a durable or a regular terminal is unknown,
// so closing it sends no mode and the backend decides (a durable terminal goes to the background, a regular one closes;
// a terminal the backend does not hold is only dropped). It is never reported as a process that will end.
type DetachBlock = "exec" | "winrm" | "local" | "regular" | "unknown" | "unattached";

function detachBlockOf(
  t: AppTab,
  sessions: SessionInfo[],
  assetKind?: string,
): DetachBlock | null {
  if (t.containerId) return "exec";
  const kind = t.sessionId ? sessions.find((s) => s.id === t.sessionId)?.kind : undefined;
  if (kind === "winrm" || (!kind && assetKind === "winrm")) return "winrm";
  // A regular SSH PTY ends with the connection, so it never offers "background".
  // durable is undefined until the backend reports it; retainable decides until then.
  // Until the durable flag arrives, a remote tab that reports itself as not retainable is a regular PTY too.
  if ((kind === "ssh" || (!kind && assetKind === "ssh")) && (t.durable === false || (t.durable === undefined && t.retainable === false))) return "regular";
  // A Telnet terminal is always temporary: it is the connection itself.
  if (kind === "telnet" || (!kind && assetKind === "telnet")) return "regular";
  if (t.retainable === true) return null;
  if (kind === "local" || (!kind && assetKind === "local")) {
    return t.retainable === undefined && t.durable === undefined ? "unattached" : "local";
  }
  return "unknown";
}

function endsOnClose(block: DetachBlock | null): block is Exclude<DetachBlock, "unattached"> {
  return block !== null && block !== "unattached";
}

function detachBlockLabel(block: Exclude<DetachBlock, "unattached">): string {
  if (block === "exec") return tr("容器 exec");
  if (block === "regular") return tr("临时");
  if (block === "winrm") return tr("WinRM 非交互");
  if (block === "local") return tr("本地");
  return tr("其他");
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
  return tabs.filter((t) => isRunningTerminal(t) && endsOnClose(detachBlockOf(t, sessions, assetKind)))
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
  // Exited durable terminals with no proof the process ended: they may still run on the host, so they are detached.
  unconfirmed: AppTab[];
  dbConnIds: string[];
  liveRemote: AppTab[];
  hasSqlDraft: boolean;
  sqlDraft?: SqlDraft;
  runningAiTabs: AiCloseTab[];
  aiBoardKey: string;
  aiTabIds: Set<string>;
  // Set when the user chose to disconnect after moving terminals to the background: the closed workspace's session is
  // then released even though its host still holds persistent terminals, which stop being monitored until it
  // reconnects.
  disconnectAfterDetach?: boolean;
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

// applyCloseDecision moves the running terminals that could go to the background into the kill list when the
// decision is to end them.
function applyCloseDecision(plan: ClosePreferencePlan, action: "detach" | "kill"): void {
  if (action !== "kill") return;
  plan.preferredKills = [...plan.preferredKills, ...plan.detachable];
  plan.detachable = [];
}

export function collectWorkspaceClose(
  tabs: AppTab[],
  assetKind: string | undefined,
  workspaceKind: WorkspaceKind,
  workspaceId: string,
): WorkspaceCloseSummary {
  const { sessions, workspaceRuntime } = useUi.getState();
  const live = tabs.filter(isRunningTerminal);
  const blocked = live.filter((t) => endsOnClose(detachBlockOf(t, sessions, assetKind)));
  const blockedKinds = [
    ...new Set(
      blocked
        .map((t) => detachBlockOf(t, sessions, assetKind))
        .filter((b): b is Exclude<DetachBlock, "unattached"> => endsOnClose(b)),
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
    cleanup: tabs.filter((t) =>
      t.kind === "terminal" && t.tabId && !t.dead && (
        (t.exited && !isUnconfirmedDurableExit(t)) || (!t.exited && detachBlockOf(t, sessions, assetKind) === "unattached")
      )),
    unconfirmed: tabs.filter((t) => Boolean(t.tabId) && isUnconfirmedDurableExit(t)),
    dbConnIds: [
      ...new Set(tabs.filter((t) => t.kind === "db" && t.connId).map((t) => t.connId as string)),
    ],
    liveRemote: tabs.filter(isLiveRemoteTab),
    hasSqlDraft: Boolean(workspaceRuntime[workspaceId]?.sqlDraft?.text.trim()),
    sqlDraft: workspaceRuntime[workspaceId]?.sqlDraft,
    runningAiTabs,
    aiBoardKey,
    aiTabIds: new Set((useUi.getState().aiBoards[aiBoardKey]?.tabs ?? []).map((tab) => tab.id)),
  };
}

// mergeWorkspaceCloseSummaries folds the plans of several workspaces (or tab groups) into one, so a single dialog
// confirms them all.
export function mergeWorkspaceCloseSummaries(summaries: WorkspaceCloseSummary[]): WorkspaceCloseSummary {
  const all = <T>(pick: (summary: WorkspaceCloseSummary) => T[]): T[] => summaries.flatMap(pick);
  return {
    tabs: all((s) => s.tabs),
    dirty: all((s) => s.dirty),
    detachable: all((s) => s.detachable),
    preferredKills: all((s) => s.preferredKills),
    confirmBlocked: false,
    blocked: all((s) => s.blocked),
    blockedKinds: [...new Set(all((s) => (s.blockedKinds ? s.blockedKinds.split("、") : [])))].join("、"),
    cleanup: all((s) => s.cleanup),
    unconfirmed: all((s) => s.unconfirmed),
    dbConnIds: [...new Set(all((s) => s.dbConnIds))],
    liveRemote: all((s) => s.liveRemote),
    hasSqlDraft: summaries.some((s) => s.hasSqlDraft),
    runningAiTabs: all((s) => s.runningAiTabs),
    aiBoardKey: "",
    aiTabIds: new Set(all((s) => [...s.aiTabIds])),
  };
}

const idsOf = (...lists: AppTab[][]): Set<string> => new Set(lists.flat().map((tab) => tab.id));
const within = (tabs: AppTab[], allowed: Set<string>): boolean => tabs.every((tab) => allowed.has(tab.id));

// closeCoveredBy reports whether a confirmation already given (for a batch) covers this plan, category by category:
// a terminal it now ends was confirmed as ending (not as one to keep running), and no unsaved content, remote desktop
// or AI task was added since. Moving a terminal to the background is covered by any confirmation of it.
export function closeCoveredBy(plan: WorkspaceCloseSummary, confirmed: WorkspaceCloseSummary): boolean {
  const ending = idsOf(confirmed.blocked, confirmed.preferredKills);
  const terminals = idsOf(confirmed.blocked, confirmed.preferredKills, confirmed.detachable, confirmed.unconfirmed);
  const aiIds = new Set(confirmed.runningAiTabs.map((tab) => tab.id));
  return (
    within([...plan.blocked, ...plan.preferredKills], ending) &&
    within([...plan.detachable, ...plan.unconfirmed], terminals) &&
    within(plan.dirty, idsOf(confirmed.dirty)) &&
    within(plan.liveRemote, idsOf(confirmed.liveRemote)) &&
    (!plan.hasSqlDraft || confirmed.hasSqlDraft) &&
    plan.runningAiTabs.every((tab) => aiIds.has(tab.id))
  );
}

// WorkspaceClosePreconfirmed closes a workspace under a batch confirmation: the decision for its running terminals
// and the plan that was confirmed. A plan that names something new is confirmed again.
export interface WorkspaceClosePreconfirmed {
  action: "detach" | "kill";
  confirmed: WorkspaceCloseSummary;
}

export function applyPreconfirmedClose(plan: WorkspaceCloseSummary, preconfirmed: WorkspaceClosePreconfirmed): boolean {
  if (!closeCoveredBy(plan, preconfirmed.confirmed)) return false;
  plan.confirmBlocked = false;
  applyCloseDecision(plan, preconfirmed.action);
  plan.disconnectAfterDetach = preconfirmed.confirmed.disconnectAfterDetach;
  return true;
}

// The decision a confirmed batch took for running terminals that could go to the background.
export function confirmedCloseAction(summary: WorkspaceCloseSummary): "detach" | "kill" {
  return summary.preferredKills.length > 0 || getTerminalPrefs().closeAction === "kill" ? "kill" : "detach";
}

// confirmWorkspaceClose merges unsaved content, terminal close consequences and, when the preference asks, the
// background-or-end choice into one dialog; on cancel, no IPC is issued and no tab is closed. offerDisconnect adds the
// choice to also disconnect the host once its terminals are in the background, for closes that end a workspace.
export async function confirmWorkspaceClose(
  summary: WorkspaceCloseSummary,
  title: string,
  detachOnly = false,
  dialogTitle = tr("关闭「{title}」", { title }),
  offerDisconnect = false,
): Promise<boolean> {
  const action = detachOnly ? "detach" : getTerminalPrefs().closeAction;
  summary.confirmBlocked = action !== "kill" && !detachOnly;
  if (detachOnly) summary.preferredKills = [];
  if (action === "kill") applyCloseDecision(summary, "kill");
  summary.disconnectAfterDetach = offerDisconnect && action === "detach-disconnect";
  const choose = action === "ask" && summary.detachable.length > 0;
  const lines = workspaceCloseLines(summary, !choose);
  const confirmBlocked = summary.blocked.length > 0 && summary.confirmBlocked;
  const unconfirmed = summary.unconfirmed.length;
  const destructive = summary.dirty.length > 0 || confirmBlocked || summary.hasSqlDraft || summary.runningAiTabs.length > 0 || summary.liveRemote.length > 0;
  if (choose) {
    const { askChoiceRemember } = await import("../../ui/dialogs");
    const question = tr("{length} 个终端的进程正在运行。\n转到后台后，可在「后台会话」中随时接管；直接关闭会结束进程。", { length: summary.detachable.length });
    const result = await askChoiceRemember(lines.length > 0 ? `${question}\n\n${lines.join("\n")}` : question, {
      title: dialogTitle,
      choices: offerDisconnect ? WORKSPACE_CLOSE_CHOICE_OPTIONS : CLOSE_CHOICE_OPTIONS,
      rememberLabel: tr("以后都按此选择"),
      // Either choice also discards what the lines name, so the dialog warns and Enter does not pick a choice.
      ...(destructive ? { level: "warning" as const, focusCancel: true } : {}),
    });
    if (!result) return false;
    const decided = result.key === "kill" ? "kill" : "detach";
    applyCloseDecision(summary, decided);
    summary.disconnectAfterDetach = result.key === "detach-disconnect";
    if (result.remember) setTerminalCloseAction(result.key === "detach-disconnect" ? "detach-disconnect" : decided);
    return true;
  }
  if (!destructive && unconfirmed === 0) return true;
  const { ask } = await import("../../ui/dialogs");
  return ask(tr("{join}\n仍要继续？", { join: lines.join("\n") }), {
    title: dialogTitle,
    kind: "warning",
    confirmLabel: tr("关闭"),
    danger: destructive || summary.preferredKills.length > 0,
  });
}

// workspaceCloseLines lists what closing ends or moves; withDecision adds what happens to the running terminals that
// could go to the background, once that is decided.
function workspaceCloseLines(summary: WorkspaceCloseSummary, withDecision: boolean): string[] {
  const confirmBlocked = summary.blocked.length > 0 && summary.confirmBlocked;
  const unconfirmed = summary.unconfirmed.length;
  const lines: string[] = [];
  if (summary.dirty.length > 0) {
    lines.push(
      tr("· {length} 个文件尚未保存，关闭后会丢失改动：{join}", { length: summary.dirty.length, join: summary.dirty.map((t) => t.title).join("、") }),
    );
  }
  if (summary.hasSqlDraft) {
    lines.push(tr("· 此工作区仍有未保存的 SQL 草稿，关闭后将丢失"));
  }
  if (summary.liveRemote.length > 0) {
    lines.push(tr("· {length} 个远程桌面仍在连接，断开后会话结束", { length: summary.liveRemote.length }));
  }
  if (summary.runningAiTabs.length > 0) {
    lines.push(tr("· {length} 个 AI 标签页仍在运行：{join}", { length: summary.runningAiTabs.length, join: summary.runningAiTabs.map((tab) => tab.title).join("、") }));
  }
  if (confirmBlocked) {
    lines.push(tr("· {length} 个{blockedKinds}终端不支持转入后台，将结束进程", { length: summary.blocked.length, blockedKinds: summary.blockedKinds }));
  }
  if (withDecision && summary.preferredKills.length > 0) {
    lines.push(tr("· {length} 个运行中的终端将结束进程", { length: summary.preferredKills.length }));
  }
  if (withDecision && summary.detachable.length > 0) {
    lines.push(tr("· {length} 个运行中的终端将转入后台", { length: summary.detachable.length }));
  }
  if (unconfirmed > 0) {
    lines.push(tr("· {unconfirmed} 个终端无法确认状态，主机上可能仍在运行，将转入后台", { unconfirmed }));
  }
  return lines;
}

export async function executeWorkspaceClose(summary: WorkspaceCloseSummary): Promise<WorkspaceCloseOutcome> {
  const operations: { tab: AppTab; mode?: "kill" | "detach" }[] = [
    ...summary.detachable.map((tab) => ({ tab, mode: "detach" as const })),
    ...summary.unconfirmed.map((tab) => ({ tab, mode: "detach" as const })),
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
  const killStart = summary.detachable.length + summary.unconfirmed.length;
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
  if (outcome.detached > 0) parts.push(tr("{detached} 个终端转入后台，可在「后台会话」接管", { detached: outcome.detached }));
  if (outcome.killed > 0) parts.push(tr("{killed} 个进程已结束", { killed: outcome.killed }));
  if (outcome.failed > 0) {
    pushToast("error", tr("关闭「{title}」：{failed} 个终端操作失败：{failureText}", { title, failed: outcome.failed, failureText: outcome.failureText }));
    return;
  }
  if (outcome.killUnreachable > 0) {
    // The kill was skipped, not performed: say so as a warning, never inside the success summary.
    parts.push(
      tr("{killUnreachable} 个终端所在主机已断开或无法访问，远端进程未能结束，仍在「后台会话」中，重新连接后可接管或结束", { killUnreachable: outcome.killUnreachable }),
    );
    // A process left running stays on screen until the user dismisses the notice.
    pushToast("error", `${deleted ? tr("已关闭「{title}」", { title }) : tr("已关闭部分标签页「{title}」", { title })}：${parts.join("，")}`, undefined, { persistent: true, group: REMOTE_KILL_SKIPPED_TOAST });
    return;
  }
  if (parts.length === 0) return;
  pushToast("info", `${deleted ? tr("已关闭「{title}」", { title }) : tr("已关闭部分标签页「{title}」", { title })}：${parts.join("，")}`);
}

export function closeActionHint(t: AppTab): string | undefined {
  if (t.kind !== "terminal" || !t.tabId || t.dead || t.exited) return undefined;
  const block = detachBlockOf(t, useUi.getState().sessions, assetKindForTab(t));
  if (block === "exec") return tr("结束容器内命令进程");
  if (block === "winrm") return tr("结束 WinRM 命令");
  if (block === "local") return tr("结束本机进程");
  if (block === "unattached") return tr("终端未连接，仅关闭标签页");
  if (block === "regular" || block === "unknown") return tr("结束进程");
  const action = getTerminalPrefs().closeAction;
  if (action === "kill") return tr("结束进程");
  if (action === "detach" || action === "detach-disconnect") return tr("转入后台");
  return tr("转入后台或结束进程");
}

export function closeTabHint(t: AppTab): string {
  if (t.kind === "remote") return tr("关闭标签页（断开后会话结束）");
  if (t.kind !== "terminal" || !t.tabId || t.dead) return tr("关闭标签页");
  if (isUnconfirmedDurableExit(t)) return tr("关闭标签页（转入后台）");
  if (t.exited) return tr("关闭标签页（进程已结束）");
  const action = closeActionHint(t);
  return action ? tr("关闭标签页（{action}）", { action }) : tr("关闭标签页");
}

const CLOSE_CHOICE_OPTIONS: ChoiceOption[] = [
  { key: "detach", label: tr("转入后台"), hint: tr("进程继续运行，可在「后台会话」随时接管"), primary: true },
  { key: "kill", label: tr("直接关闭"), hint: tr("结束进程，无法恢复"), danger: true },
];

// A workspace close can also end the connection: background terminals keep running on the host, but their state and
// alerts pause until the host is connected again.
const WORKSPACE_CLOSE_CHOICE_OPTIONS: ChoiceOption[] = [
  { key: "detach", label: tr("转入后台"), hint: tr("进程继续运行，保持连接以更新状态和提醒"), primary: true },
  {
    key: "detach-disconnect",
    label: tr("转入后台并断开连接"),
    hint: tr("进程继续在主机上运行，重新连接前不更新状态和提醒"),
  },
  { key: "kill", label: tr("直接关闭"), hint: tr("结束进程，无法恢复"), danger: true },
];

export interface CloseRequestOptions {
  // The close came from a key chord: every dialog focuses 取消 first, and a close that ends a running process always
  // asks once, whatever the preferences say, so a stray chord never ends one.
  viaShortcut?: boolean;
}

async function requestCloseTabWithAction(id: string, options: CloseRequestOptions): Promise<void> {
  const viaShortcut = options.viaShortcut === true;
  const st = useUi.getState();
  const found = findTab(id);
  if (!found) return;
  const { tab: target, ws } = found;
  if (!(await confirmDirtyEditors([target], tr("关闭「{title}」", { title: target.title })))) return;
  if (isLiveRemoteTab(target)) {
    const { ask } = await import("../../ui/dialogs");
    const ok = await ask(tr("「{title}」的远程桌面仍在连接。\n断开后会话结束，仍要关闭？", { title: ws.title }), {
      title: tr("关闭「{title}」", { title: ws.title }),
      kind: "warning",
      confirmLabel: tr("断开"),
    });
    if (!ok) return;
  }
  if (target.kind !== "terminal" || !target.tabId || target.dead || target.exited) {
    await st.closeTab(id);
    return;
  }
  const block = detachBlockOf(target, st.sessions, ws.assetKind);
  if (block === "unattached") {
    await st.closeTab(id);
    return;
  }
  const prefs = getTerminalPrefs();
  if (block) {
    if (block === "regular") {
      if (viaShortcut || (prefs.closeAction !== "kill" && prefs.confirmTemporaryClose)) {
        // A regular SSH terminal has a single outcome, so offer only the red close action.
        const { askChoiceRemember } = await import("../../ui/dialogs");
        const choice = await askChoiceRemember(tr("「{title}」是临时终端，进程随连接结束，不能转到后台。", { title: target.title }), {
          title: tr("关闭「{title}」", { title: target.title }),
          level: "warning",
          choices: [{ key: "kill", label: tr("直接关闭"), hint: tr("结束进程，无法恢复"), danger: true }],
          // A chord always asks, so the dialog it opens offers no way to stop asking.
          rememberLabel: viaShortcut ? undefined : tr("不再询问"),
          focusCancel: viaShortcut,
        });
        if (choice?.key !== "kill") return;
        if (choice.remember) setTerminalConfirmTemporaryClose(false);
      }
    } else if (viaShortcut || prefs.closeAction !== "kill") {
      const { ask } = await import("../../ui/dialogs");
      const ok = await ask(
        tr("「{title}」是{value}终端，不支持转入后台。\n关闭会结束这个进程，仍要关闭？", { title: target.title, value: detachBlockLabel(block) }),
        { title: tr("关闭「{title}」", { title: target.title }), kind: "warning", confirmLabel: tr("结束进程") },
      );
      if (!ok) return;
    }
    await st.closeTab(id, "kill");
    return;
  }
  let action: "detach" | "kill" = prefs.closeAction === "kill" ? "kill" : "detach";
  if (prefs.closeAction === "ask") {
    const { askChoiceRemember } = await import("../../ui/dialogs");
    const result = await askChoiceRemember(
      tr("「{title}」的进程正在运行。\n转到后台后，可在「后台会话」中随时接管；直接关闭会结束进程。", { title: target.title }),
      {
        title: tr("关闭「{title}」", { title: target.title }),
        rememberLabel: tr("以后都按此选择"),
        choices: CLOSE_CHOICE_OPTIONS,
        focusCancel: viaShortcut,
      },
    );
    if (!result) return;
    action = result.key === "kill" ? "kill" : "detach";
    if (result.remember) setTerminalCloseAction(action);
  } else if (action === "kill" && viaShortcut) {
    const { ask } = await import("../../ui/dialogs");
    const ok = await ask(tr("结束「{title}」里的进程？\n\n进程会被终止，无法恢复。", { title: target.title }), {
      title: tr("关闭「{title}」", { title: target.title }),
      kind: "warning",
      confirmLabel: tr("结束进程"),
    });
    if (!ok) return;
  }
  if (action === "kill") {
    await st.closeTab(id, "kill");
    return;
  }
  if (await st.closeTab(id, "detach")) {
    st.pushToast("info", tr("「{title}」已转入后台，可在「后台会话」接管", { title: target.title }));
  }
}

const CLOSE_CHECK_TIMEOUT_MS = 10_000;

function withinCloseCheckTime<T>(pending: Promise<T>): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const expired = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error("close check timed out")), CLOSE_CHECK_TIMEOUT_MS);
  });
  return Promise.race([pending, expired]).finally(() => clearTimeout(timer));
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
    return !byId.has(tab.tabId as string) && !tab.containerId && (kind === "ssh" || kind === "device") && tab.sessionId;
  }).map((tab) => tab.sessionId as string))];
  // A host whose helper never answers counts as one that cannot be checked, so the close is not held up.
  const results = await Promise.allSettled(remoteSessions.map((id) => withinCloseCheckTime(terminalApi.listDurable(id))));
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
    if (!candidate.containerId && (kind === "ssh" || kind === "device")) {
      const descriptors = remote.get(candidate.sessionId as string);
      if (!descriptors) {
        // The host cannot be checked (not connected or the listing failed): a device tab keeps its state, and an
        // exited durable one is closed by detaching, never by killing.
        if (kind !== "device") useUi.getState().updateTab(candidate.id, { exited: undefined });
        continue;
      }
      const descriptor = descriptors.get(candidate.tabId as string);
      if (descriptor) {
        // A dead helper descriptor is proof the process ended.
        useUi.getState().updateTab(candidate.id, { exited: descriptor.dead, ...(descriptor.dead ? { durableLost: true } : {}) });
        continue;
      }
    }
    useUi.getState().updateTab(candidate.id, { exited: undefined });
  }
}

export async function requestCloseTab(id: string, options: CloseRequestOptions = {}): Promise<void> {
  await reconcileTerminalCloseState([id]);
  await requestCloseTabWithAction(id, options);
}

// requestCloseTabs closes several tabs (关闭左侧 / 右侧标签页) as one batch: it plans every close, asks once for all of
// them, then closes. Cancel closes nothing.
export async function requestCloseTabs(ids: string[]): Promise<void> {
  await reconcileTerminalCloseState(ids);
  const st = useUi.getState();
  for (const id of ids) {
    if (findTab(id)?.tab.closeError) st.updateTab(id, { closeError: undefined });
  }
  const groups = new Map<string, { ws: Workspace; tabs: AppTab[] }>();
  for (const id of ids) {
    const found = findTab(id);
    if (!found) continue;
    const group = groups.get(found.ws.id) ?? { ws: found.ws, tabs: [] };
    group.tabs.push(found.tab);
    groups.set(found.ws.id, group);
  }
  if (groups.size === 0) return;
  // Workspace-level content (SQL draft, AI tabs, database connections) stays with the workspace; the database tabs in
  // the batch disconnect through their own close.
  const plan = mergeWorkspaceCloseSummaries(
    [...groups.values()].map(({ ws, tabs }) => ({
      ...collectWorkspaceClose(tabs, ws.assetKind, ws.kind, ws.id),
      hasSqlDraft: false,
      sqlDraft: undefined,
      runningAiTabs: [],
      aiTabIds: new Set<string>(),
      dbConnIds: [],
    })),
  );
  const title = plan.tabs.every((tab) => tab.kind === "terminal")
    ? tr("关闭终端标签页")
    : tr("关闭 {length} 个标签页", { length: plan.tabs.length });
  if (!(await confirmWorkspaceClose(plan, "", false, title))) return;
  const outcome = await executeWorkspaceClose(plan);
  const handled = new Set(
    [...plan.detachable, ...plan.unconfirmed, ...plan.preferredKills, ...plan.blocked, ...plan.cleanup].map((tab) => tab.id),
  );
  for (const tab of plan.tabs) {
    if (!handled.has(tab.id)) await useUi.getState().closeTab(tab.id);
  }
  const first = [...groups.values()][0].ws;
  // One notice per batch: the summary when every terminal closed, otherwise the failure count with the first reason.
  if (outcome.failed === 0) pushWorkspaceCloseToast(groups.size === 1 ? first.title : tr("多个工作区"), outcome, false);
  const failed = ids.filter((id) => findTab(id)?.tab.closeError).length;
  if (failed > 0) {
    const text = tr("{failed} 个标签页未能关闭，已保留并在标签页上标出原因", { failed });
    useUi.getState().pushToast("error", outcome.failureText ? `${text}：${outcome.failureText}` : text);
  }
}

// closeWorkspaces closes several workspaces (关闭全部工作区) as one batch: one dialog confirms everything they would
// end, then each closes under that decision. Cancel closes nothing; a workspace that gained content since asks again.
export async function closeWorkspaces(ids: string[]): Promise<void> {
  const present = (id: string) => useUi.getState().workspaces.find((w) => w.id === id);
  const targets = ids.filter((id) => present(id));
  if (targets.length === 0) return;
  if (targets.length === 1) {
    await useUi.getState().closeWorkspace(targets[0]);
    return;
  }
  await reconcileTerminalCloseState(targets.flatMap((id) => present(id)?.panes.flatMap((p) => p.tabs.map((tab) => tab.id)) ?? []));
  const summaries = new Map<string, WorkspaceCloseSummary>();
  for (const id of targets) {
    const ws = present(id);
    if (ws) summaries.set(id, collectWorkspaceClose(ws.panes.flatMap((p) => p.tabs), ws.assetKind, ws.kind, ws.id));
  }
  if (summaries.size === 0) return;
  const merged = mergeWorkspaceCloseSummaries([...summaries.values()]);
  if (!(await confirmWorkspaceClose(merged, "", false, tr("关闭 {length} 个工作区", { length: summaries.size }), true))) return;
  const action = confirmedCloseAction(merged);
  for (const [id, confirmed] of summaries) {
    confirmed.disconnectAfterDetach = merged.disconnectAfterDetach;
    await useUi.getState().closeWorkspace(id, { action, confirmed });
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
  const ok = await ask(tr("结束「{title}」里的进程？\n\n进程会被终止，无法恢复。", { title: target.title }), {
    title: tr("结束进程"),
    kind: "warning",
    confirmLabel: tr("结束进程"),
  });
  if (!ok) return;
  const report: CloseTabReport = {};
  if ((await st.closeTab(id, "kill", false, report)) && !report.remoteKillSkipped) {
    st.pushToast("success", tr("已结束「{title}」的进程", { title: target.title }));
  }
}

export async function requestKillWorkspaceTerminals(workspaceId: string): Promise<void> {
  const before = useUi.getState().workspaces.find((w) => w.id === workspaceId);
  if (!before) return;
  const st = useUi.getState();
  const live = before.panes.flatMap((p) => p.tabs).filter(isRunningTerminal);
  if (live.length === 0) {
    st.pushToast("info", tr("该工作区里没有正在运行的终端"));
    return;
  }
  const { ask } = await import("../../ui/dialogs");
  const ok = await ask(tr("结束「{title}」里 {length} 个终端的进程？\n\n进程会被终止，无法恢复。", { title: before.title, length: live.length }), {
    title: tr("结束全部终端进程"),
    kind: "warning",
    confirmLabel: tr("结束进程"),
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
    st.pushToast("error", tr("{failed}/{length} 个终端结束失败", { failed, length: live.length }));
  } else {
    st.pushToast("success", tr("已结束 {length} 个终端进程", { length: live.length }));
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
