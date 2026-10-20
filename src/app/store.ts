import { create } from "zustand";
import { assetApi, dbApi, sessionApi, terminalApi, vaultApi, type RecordingStatus, type SessionInfo, type VaultStatus } from "../ipc/commands";
import { DESKTOP } from "../ipc/env";
import type { ConnectProgressEvent, HelperProgressEvent, SessionStatusEvent as BaseSessionStatusEvent } from "../ipc/events";
import { describeError } from "../ui/errorText";
import { formatTime } from "../ui/format";
import type { ChoiceOption } from "../ui/dialogs";
import { connectWithHostKeyConfirm, confirmHostKeyIfNeeded } from "./hostKeys";
import { formatQuickConnectTarget, type QuickConnectTarget } from "./quickConnectTarget";
import {
  clearUnknownFileEditorRename,
  dirtyFileEditors,
  fileEditorCaseInsensitive,
  fileEditorPathMatches,
  fileEditorRenamePending,
  migratedFileEditorPath,
  migrateFileEditorDirty,
  unknownFileEditorRename,
} from "../features/files/editorGuards";
import { baseName } from "../features/files/pathUtils";
import { aiTabRunControls, confirmAiRunCancellation, runningAiTabs as getRunningAiTabs, clearAiTabRun, clearAiTabRuns, type AiCloseTab } from "../features/ai/runOwnership";
import { splitAllowedForHeight, workspaceViewport } from "../features/terminal/workspaceLayout";
import { useConnectHistory } from "../features/explorer/connectHistory";
import {
  getResolvedTheme,
  getThemeMode,
  initTheme,
  setThemeMode as persistThemeMode,
  subscribeTheme,
  type ResolvedTheme,
  type ThemeMode,
} from "./theme";
import {
  getTerminalPrefs,
  saveThemeModePreference,
  setTerminalCloseAction,
  type TerminalCloseAction,
} from "./preferences";
import { moveItem } from "./tabReorder";
import { clearTabUnread, retainUnreadTabs } from "./unreadStore";
import {
  ALL_PROJECTS,
  focusedActiveWorkspace,
  focusedWorkspaces,
  reconcileActiveWorkspaceForFocus,
  type ProjectFocus,
  type ProjectInventory,
} from "./projectFocus";

export type PaneKind =
  | "terminal"
  | "files"
  | "log"
  | "mount"
  | "forward"
  | "docker"
  | "db"
  | "credentials"
  | "settings"
  | "audit"
  | "devices"
  | "background"
  | "history"
  | "execBatch";

const TOOL_TAB_KINDS = new Set<PaneKind>([
  "credentials",
  "settings",
  "audit",
  "devices",
  "history",
  "execBatch",
]);

export function isToolTabKind(kind: PaneKind): boolean {
  return TOOL_TAB_KINDS.has(kind);
}

export function isGlobalToolTab(tab: { kind: PaneKind; sessionId?: string }): boolean {
  return tab.kind === "forward" && !tab.sessionId;
}

function isWorkspaceToolTab(tab: { kind: PaneKind; sessionId?: string }): boolean {
  return isToolTabKind(tab.kind) || isGlobalToolTab(tab);
}

export type LeftMode = "assets" | "files" | "credentials";

export type DbKind = "mysql" | "postgres" | "redis";

export const DB_KIND_LABEL: Record<DbKind, string> = {
  mysql: "SQL",
  postgres: "PostgreSQL",
  redis: "Redis",
};

export function dbKindOf(kind: string): DbKind | null {
  return kind === "mysql" || kind === "postgres" || kind === "redis" ? kind : null;
}

export interface AppTab {
  id: string;
  kind: PaneKind;
  title: string;
  sessionId?: string;
  assetId?: string;
  tabId?: string;
  containerId?: string;
  connId?: string;
  dbKind?: DbKind;
  credId?: string;
  path?: string;
  shell?: "powershell" | "cmd" | "posix" | "unknown";
  pendingDirectory?: string;
  closable: boolean;
  dead?: boolean;
  exited?: boolean;
  retainable?: boolean;
  renamedByUser?: boolean;
  closeError?: string;
  restorePending?: boolean;
}

export type WorkspaceKind = "session" | "db" | "tools";

export interface Pane {
  id: string;
  tabs: AppTab[];
  activeTabId: string | null;
}

export interface Workspace {
  id: string;
  kind: WorkspaceKind;
  title: string;
  sessionId?: string;
  assetId?: string;
  connId?: string;
  dbKind?: DbKind;
  assetKind?: string;
  quickTarget?: QuickWorkspaceTarget;
  deletedTarget?: boolean;
  restorePending?: boolean;
  panes: Pane[];
  activePaneId: string;
  splitRatio: number;
  closable: boolean;
}

export interface WorkspaceSpec {
  kind: WorkspaceKind;
  title: string;
  sessionId?: string;
  assetId?: string;
  connId?: string;
  dbKind?: DbKind;
  assetKind?: string;
  quickTarget?: QuickWorkspaceTarget;
}

export interface QuickWorkspaceTarget extends QuickConnectTarget {
  id: string;
  authKind: "agent" | "password";
}

export interface SqlDraft {
  text: string;
  selection: { from: number; to: number };
  schema: string;
  connId: string;
  dbKind: DbKind;
}

export interface SqlExecutionTarget {
  connId: string;
  dbKind: DbKind;
  title: string;
  assetId?: string;
}

export type SqlExecutionOutcome = "success" | "error" | "unknown";

export interface SqlExecutionRecord {
  id: number;
  generation: number;
  target: SqlExecutionTarget;
  sql: string;
  sqlTruncated: boolean;
  schema: string;
  startedAt: number;
  endedAt: number;
  durationMs: number;
  rowsAffected: number | null;
  error: string | null;
  errorTruncated: boolean;
  outcome: SqlExecutionOutcome;
}

export type SqlExecutionInput = Omit<
  SqlExecutionRecord,
  "id" | "sqlTruncated" | "errorTruncated"
>;

export type WorkspaceProblemSource = "sql-query" | "sql-schemas" | "sql-tables" | "connect";

export interface WorkspaceProblemTarget {
  title: string;
  identity: string;
  kind?: string;
  assetId?: string;
  connId?: string;
  schema?: string;
}

export type WorkspaceProblemConnection =
  | { kind: "asset"; asset: ConnectAssetInput }
  | { kind: "quick"; mode: "temporary" | "save"; target: QuickConnectTarget; label: string };

export interface WorkspaceProblem {
  id: number;
  generation: number;
  resetGeneration?: number;
  createdAt: number;
  source: WorkspaceProblemSource;
  operation: string;
  target: WorkspaceProblemTarget;
  summary: string;
  detail: string;
  detailTruncated: boolean;
  canRetryRead: boolean;
  sql?: string;
  sqlTruncated?: boolean;
  outcome?: SqlExecutionOutcome;
  connection?: WorkspaceProblemConnection;
  connectionKey?: string;
}

export type WorkspaceProblemInput = Omit<
  WorkspaceProblem,
  "id" | "detailTruncated" | "sqlTruncated"
>;

export type ConnectionProblemInput = Omit<
  WorkspaceProblemInput,
  "generation" | "connectionKey"
> & {
  connectionKey: string;
  workspaceId?: string;
};

export interface WorkspaceRuntime {
  generation: number;
  sqlDraft?: SqlDraft;
  sqlExecutionHistory?: SqlExecutionRecord[];
  problems?: WorkspaceProblem[];
}

export type WorkspaceRestoreStatus = "waiting" | "connecting" | "success" | "failed" | "canceled" | "skipped";

export interface WorkspaceRestoreResult {
  status: WorkspaceRestoreStatus;
  detail?: string;
}

export interface SessionReconnectMetadata {
  error?: string | null;
  errorCode?: string | null;
  attempt?: number;
  nextRetryAt?: string | number | null;
  staleConfig?: boolean | null;
}

export type SessionRuntimeInfo = SessionInfo & SessionReconnectMetadata;
export type SessionStatusEvent = BaseSessionStatusEvent & SessionReconnectMetadata;

export interface ToastAction {
  label: string;
  onSelect: () => void;
}

export interface ToastItem {
  id: number;
  kind: "info" | "error" | "success";
  text: string;
  action?: ToastAction;
}

export interface TextPromptState {
  message: string;
  value: string;
  multiLine?: boolean;
  secret?: boolean;
  resolve: (v: string | null) => void;
}

export interface AppDialogState {
  kind: "ask" | "confirm" | "message";
  message: string;
  title?: string;
  level: "info" | "warning";
  resolve: (v: boolean) => void;
}

export interface AppChoiceOption {
  key: string;
  label: string;
  hint?: string;
  danger?: boolean;
  primary?: boolean;
}

export interface AppChoiceState {
  title: string;
  message: string;
  options: AppChoiceOption[];
  level: "info" | "warning";
  rememberLabel?: string;
  resolve: (v: string | null, remember: boolean) => void;
}

export interface TakeoverState {
  tabId: string;
  jobId?: string;
  token: string;
  task: string;
  allowWrite: boolean;
  startedAt: number;
}

// AI board groups by workspace: each workspace gets one tab group, each tab bound to an independent conversation.
export interface AiDockTab {
  id: string;
  title: string;
  conversationId?: string;
  draft?: string;
}

export interface AiBoard {
  tabs: AiDockTab[];
  activeTabId: string | null;
}

// Without a remote-host workspace (no workspace or the "tools" workspace), the AI board falls into the global group.
export const GLOBAL_AI_BOARD_KEY = "global";

export type BroadcastTargetStatus = "pending" | "sent" | "skipped" | "failed";

export interface BroadcastTargetReport {
  status: BroadcastTargetStatus;
  reason: string | null;
}

export interface BroadcastOrigin {
  workspaceId: string;
  storeTabId: string;
}

export interface BroadcastState {
  origin: BroadcastOrigin;
  targetIds: string[];
  generation: number;
  results: Record<string, BroadcastTargetReport>;
}

export interface BroadcastConfig {
  origin: BroadcastOrigin;
  targetIds: string[];
}

export interface BroadcastTargetUpdate extends BroadcastTargetReport {
  targetId: string;
}

interface UiState {
  leftOpen: boolean;
  leftMode: LeftMode;
  rightOpen: boolean;
  themeMode: ThemeMode;
  resolvedTheme: ResolvedTheme;
  setThemeMode: (m: ThemeMode) => void;
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
  activeProject: ProjectFocus;
  projectInventory: ProjectInventory | null;
  setActiveProject: (focus: ProjectFocus) => void;
  setProjectInventory: (inventory: ProjectInventory) => void;
  sessions: SessionRuntimeInfo[];
  aiBusy: boolean;
  takeover: TakeoverState | null;
  setTakeover: (t: TakeoverState | null) => void;
  aiBoards: Record<string, AiBoard>;
  aiAccountEpoch: number;
  workspaceRuntime: Record<string, WorkspaceRuntime>;
  workspaceRuntimeEpoch: number;
  connectionProblems: Record<string, WorkspaceProblem>;
  getWorkspaceRuntime: (workspaceId: string) => WorkspaceRuntime | undefined;
  ensureWorkspaceRuntime: (workspaceId: string) => WorkspaceRuntime | undefined;
  updateSqlDraft: (workspaceId: string, draft: SqlDraft, expectedGeneration?: number) => void;
  recordSqlExecution: (workspaceId: string, record: SqlExecutionInput) => void;
  recordWorkspaceProblem: (workspaceId: string, problem: WorkspaceProblemInput) => void;
  recordConnectionProblem: (problem: ConnectionProblemInput) => void;
  clearConnectionProblem: (connectionKey: string) => void;
  dismissWorkspaceProblem: (workspaceId: string, problemId: number, expectedGeneration?: number) => void;
  clearSqlDraft: (workspaceId: string, expectedGeneration?: number) => void;
  ensureAiBoard: (key: string) => AiBoard;
  addAiTab: (key: string) => string;
  closeAiTab: (key: string, tabId: string, expectedEpoch?: number) => void;
  setActiveAiTab: (key: string, tabId: string) => void;
  moveAiTab: (key: string, fromIndex: number, toIndex: number) => void;
  updateAiTab: (key: string, tabId: string, patch: Partial<AiDockTab>, expectedEpoch?: number) => void;
  resetAiAccount: () => void;
  broadcast: BroadcastState | null;
  setBroadcast: (b: BroadcastConfig | null) => void;
  reportBroadcastTarget: (generation: number, update: BroadcastTargetUpdate) => void;
  modelProfilesRevision: number;
  bumpModelProfilesRevision: () => void;
  connectFocusRevision: number;
  bumpConnectFocusRevision: () => void;
  toasts: ToastItem[];
  textPrompt: TextPromptState | null;
  openTextPrompt: (s: TextPromptState) => void;
  closeTextPrompt: (v: string | null) => void;
  aiRulePrefill: string | null;
  setAiRulePrefill: (v: string | null) => void;
  appDialog: AppDialogState | null;
  openAppDialog: (d: AppDialogState) => void;
  closeAppDialog: (v: boolean) => void;
  appChoice: AppChoiceState | null;
  openAppChoice: (c: AppChoiceState) => void;
  closeAppChoice: (v: string | null) => void;

  setLeftOpen: (v: boolean) => void;
  setLeftMode: (m: LeftMode) => void;
  setRightOpen: (v: boolean) => void;

  leftWidth: number;
  rightWidth: number;
  setLeftWidth: (w: number) => void;
  setRightWidth: (w: number) => void;

  terminalOsc52: boolean;
  setTerminalOsc52: (v: boolean) => void;

  ensureWorkspace: (spec: WorkspaceSpec) => string;
  setActiveWorkspace: (id: string) => void;
  closeWorkspace: (id: string, mode?: "detach") => Promise<boolean>;
  markDeletedAssetWorkspaces: (assetId: string) => void;

  splitWorkspace: (workspaceId?: string) => void;
  openInLowerPane: (tab: AppTab, workspaceId?: string) => void;
  unsplitWorkspace: (paneId?: string, workspaceId?: string) => Promise<void>;
  setActivePane: (paneId: string, workspaceId?: string) => void;
  setSplitRatio: (ratio: number, workspaceId?: string) => void;

  setActiveTab: (id: string) => void;
  addTab: (tab: AppTab, paneId?: string) => void;
  closeTab: (id: string, mode?: "kill" | "detach", suppressErrorToast?: boolean) => Promise<boolean>;
  updateTab: (id: string, patch: Partial<AppTab>) => void;
  migrateFileEditorLocators: (sessionId: string, from: string, to: string, kind: string) => void;
  resolveUnknownFileEditorRename: (sessionId: string, path: string, renamed: boolean) => void;
  moveTabInPane: (paneId: string, fromIndex: number, toIndex: number) => void;
  updateWorkspaceSession: (workspaceId: string, sessionId: string) => void;

  recordings: Record<string, RecordingStatus>;
  setRecordingStatus: (status: RecordingStatus) => void;
  resyncRecording: (tabId: string) => Promise<void>;
  setSessions: (s: SessionRuntimeInfo[]) => void;
  resyncSessions: (options?: { silent?: boolean }) => Promise<void>;
  resetSessions: (options?: { clearWorkspaceRuntime?: boolean }) => void;
  resetAccountWorkspaces: () => void;
  sessionResyncFailed: boolean;
  sessionResyncCompleted: boolean;
  layoutSyncCompleted: boolean;
  workspaceRestoreDismissed: boolean;
  workspaceRestoreResults: Record<string, WorkspaceRestoreResult>;
  dismissWorkspaceRestoreBanner: () => void;
  setAiBusy: (v: boolean) => void;
  pushToast: (kind: ToastItem["kind"], text: string, action?: ToastAction) => void;
  dismissToast: (id: number) => void;

  connectingAssetIds: string[];

  connectProgress: Record<string, ConnectProgressEvent>;
  setConnectProgress: (assetId: string, progress: ConnectProgressEvent | null) => void;
  helperProgress: Record<string, HelperProgressEvent>;
  setHelperProgress: (progress: HelperProgressEvent | null) => void;
}

let toastSeq = 1;
let tabSeq = 1;
let workspaceGenerationSeq = 0;
let sqlExecutionSeq = 0;
let workspaceProblemSeq = 0;
let workspaceProblemResetGeneration = 0;
let recordingRequestSeq = 0;
let recordingsEpoch = 0;
let broadcastGeneration = 0;
const recordingRequests = new Map<string, number>();

export function pruneBroadcastToWorkspaces(
  broadcast: BroadcastState,
  workspaces: readonly Workspace[],
): BroadcastState | null {
  const openTabIds = new Set(
    workspaces.flatMap((workspace) =>
      workspace.panes.flatMap((pane) => pane.tabs.map((tab) => tab.id)),
    ),
  );
  const originExists = workspaces.some(
    (workspace) =>
      workspace.id === broadcast.origin.workspaceId &&
      workspace.panes.some((pane) => pane.tabs.some((tab) => tab.id === broadcast.origin.storeTabId)),
  );
  const remaining = broadcast.targetIds.filter((targetId) => openTabIds.has(targetId));
  if (!originExists || remaining.length === 0) return null;
  if (remaining.length === broadcast.targetIds.length) return broadcast;
  return {
    ...broadcast,
    targetIds: remaining,
    generation: ++broadcastGeneration,
    results: {},
  };
}

const SQL_EXECUTION_HISTORY_LIMIT = 50;
const SQL_EXECUTION_TEXT_BYTES = 32 * 1024;
const WORKSPACE_PROBLEM_LIMIT = 50;
const WORKSPACE_PROBLEM_DETAIL_BYTES = 16 * 1024;
const PROBLEM_REDACTED = "[REDACTED]";
const PRIVATE_KEY_BLOCK = /-----BEGIN [^-]+-----[\s\S]*?-----END [^-]+-----/g;
const URL_CREDENTIAL = /([a-z][a-z0-9+.-]*:\/\/[^/@\s:]*):([^/@\s]+)@/gi;
const BEARER_TOKEN = /\b(Bearer\s+)([^\s]+)/gi;
const SENSITIVE_ASSIGNMENT =
  /(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential|authorization)(\s*["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)/gi;
const SENSITIVE_CLI_OPTION =
  /((?:--(?:password|passwd|pwd|secret|token|api-key|access-key)|-p)(?:=|\s+)?)("[^"]*"|'[^']*'|[^\s,;]+)/gi;
const ENV_ASSIGNMENT = /^(\s*(?:export\s+)?)([A-Za-z_][A-Za-z0-9_]*)(\s*=\s*)(.*)$/gm;
const ENV_ASSIGNMENT_ANY = /([A-Za-z_][A-Za-z0-9_]*)(\s*=\s*)("[^"]*"|'[^']*'|[^\s,;]+)/g;
const SENSITIVE_ENV_NAME = /password|passwd|pwd|secret|token|credential|authorization|api[_-]?key|access[_-]?key|private[_-]?key/i;

function boundedUtf8(text: string, maxBytes: number): { text: string; truncated: boolean } {
  let bytes = 0;
  let end = 0;
  for (const char of text) {
    const codePoint = char.codePointAt(0) as number;
    const charBytes = codePoint <= 0x7f ? 1 : codePoint <= 0x7ff ? 2 : codePoint <= 0xffff ? 3 : 4;
    if (bytes + charBytes > maxBytes) break;
    bytes += charBytes;
    end += char.length;
  }
  const truncated = end !== text.length;
  return { text: truncated ? text.slice(0, end) : text, truncated };
}

function boundedSqlExecutionText(text: string): { text: string; truncated: boolean } {
  return boundedUtf8(text, SQL_EXECUTION_TEXT_BYTES);
}

export function getWorkspaceProblemResetGeneration(): number {
  return workspaceProblemResetGeneration;
}

export function sanitizeWorkspaceProblemText(text: string): string {
  const allEnv = text.toLowerCase().includes(".env");
  const sanitized = text
    .replace(PRIVATE_KEY_BLOCK, PROBLEM_REDACTED)
    .replace(URL_CREDENTIAL, (_match, schemeAndUser: string) => `${schemeAndUser}:${PROBLEM_REDACTED}@`)
    .replace(BEARER_TOKEN, (_match, prefix: string) => `${prefix}${PROBLEM_REDACTED}`)
    .replace(
      SENSITIVE_ASSIGNMENT,
      (_match, name: string, separator: string) => `${name}${separator}${PROBLEM_REDACTED}`,
    )
    .replace(
      SENSITIVE_CLI_OPTION,
      (_match, option: string) => `${option}${PROBLEM_REDACTED}`,
    );
  if (allEnv) {
    return sanitized.replace(
      ENV_ASSIGNMENT_ANY,
      (_match, name: string, separator: string) => `${name}${separator}${PROBLEM_REDACTED}`,
    );
  }
  return sanitized.replace(
    ENV_ASSIGNMENT,
    (line, prefix: string, name: string, separator: string) =>
      SENSITIVE_ENV_NAME.test(name)
        ? `${prefix}${name}${separator}${PROBLEM_REDACTED}`
        : line,
  );
}

function makeWorkspaceProblem(problem: WorkspaceProblemInput): WorkspaceProblem {
  const detail = boundedUtf8(sanitizeWorkspaceProblemText(problem.detail), WORKSPACE_PROBLEM_DETAIL_BYTES);
  const sql = problem.sql === undefined ? undefined : boundedSqlExecutionText(problem.sql);
  const connection = problem.connection
    ? problem.connection.kind === "asset"
      ? { ...problem.connection, asset: { ...problem.connection.asset } }
      : { ...problem.connection, target: { ...problem.connection.target } }
    : undefined;
  return {
    ...problem,
    id: ++workspaceProblemSeq,
    target: {
      ...problem.target,
      title: sanitizeWorkspaceProblemText(problem.target.title),
      identity: sanitizeWorkspaceProblemText(problem.target.identity),
    },
    summary: boundedUtf8(sanitizeWorkspaceProblemText(problem.summary), 240).text,
    detail: detail.text,
    detailTruncated: detail.truncated,
    sql: sql?.text,
    sqlTruncated: sql?.truncated ?? false,
    connection,
  };
}

function withoutConnectionProblem(
  runtime: Record<string, WorkspaceRuntime>,
  connectionKey: string,
): Record<string, WorkspaceRuntime> {
  let changed = false;
  const next: Record<string, WorkspaceRuntime> = {};
  for (const [workspaceId, entry] of Object.entries(runtime)) {
    if (!entry.problems?.some((problem) => problem.connectionKey === connectionKey)) {
      next[workspaceId] = entry;
      continue;
    }
    changed = true;
    next[workspaceId] = {
      ...entry,
      problems: entry.problems.filter((problem) => problem.connectionKey !== connectionKey),
    };
  }
  return changed ? next : runtime;
}

// Auth guard for the session list: when login/logout switches accounts, in-flight old requests may arrive late and must not write back over the new session data.
let sessionsEpoch = 0;
const abnormalDisconnectNotices = new Set<string>();

const LEFT_MIN = 180;
const LEFT_MAX = 560;
const RIGHT_MIN = 300;
const RIGHT_MAX = 760;
const LEFT_DEFAULT = 248;
const RIGHT_DEFAULT = 352;
const LAYOUT_KEY = "nexterm.layout.v1";
const TERMINAL_PREFS_KEY = "nexterm.terminal.v1";

interface TerminalPrefs {
  osc52: boolean;
}

function loadTerminalPrefs(): TerminalPrefs {
  try {
    const raw = localStorage.getItem(TERMINAL_PREFS_KEY);
    if (!raw) return { osc52: false };
    const o = JSON.parse(raw) as { osc52?: unknown };
    return { osc52: o.osc52 === true };
  } catch {
    return { osc52: false };
  }
}

function saveTerminalPrefs(p: TerminalPrefs): void {
  try {
    localStorage.setItem(TERMINAL_PREFS_KEY, JSON.stringify(p));
  } catch {
  }
}

function clampWidth(w: number, min: number, max: number): number {
  if (!Number.isFinite(w)) return min;
  return Math.min(max, Math.max(min, Math.round(w)));
}

export const LEFT_WIDTH_RANGE = { min: LEFT_MIN, max: LEFT_MAX, default: LEFT_DEFAULT };
export const RIGHT_WIDTH_RANGE = { min: RIGHT_MIN, max: RIGHT_MAX, default: RIGHT_DEFAULT };

interface LocalLayout {
  leftWidth: number;
  rightWidth: number;
  rightOpen: boolean;
}

function loadLayout(): LocalLayout {
  const fallback = { leftWidth: LEFT_DEFAULT, rightWidth: RIGHT_DEFAULT, rightOpen: true };
  try {
    const raw = localStorage.getItem(LAYOUT_KEY);
    if (!raw) return fallback;
    const o = JSON.parse(raw) as Partial<LocalLayout>;
    return {
      leftWidth: clampWidth(o.leftWidth ?? LEFT_DEFAULT, LEFT_MIN, LEFT_MAX),
      rightWidth: clampWidth(o.rightWidth ?? RIGHT_DEFAULT, RIGHT_MIN, RIGHT_MAX),
      rightOpen: typeof o.rightOpen === "boolean" ? o.rightOpen : true,
    };
  } catch {
    return fallback;
  }
}

function saveLayout(w: LocalLayout): void {
  try {
    localStorage.setItem(LAYOUT_KEY, JSON.stringify(w));
  } catch {
  }
}

const TAKEOVER_KEY = "nexterm.takeover.v1";

function loadPersistedTakeover(): TakeoverState | null {
  try {
    const raw = localStorage.getItem(TAKEOVER_KEY);
    if (!raw) return null;
    const o = JSON.parse(raw) as Partial<TakeoverState>;
    if (
      typeof o.tabId !== "string" ||
      typeof o.token !== "string" ||
      typeof o.task !== "string" ||
      typeof o.allowWrite !== "boolean" ||
      typeof o.startedAt !== "number"
    ) {
      localStorage.removeItem(TAKEOVER_KEY);
      return null;
    }
    return {
      tabId: o.tabId,
      jobId: typeof o.jobId === "string" ? o.jobId : undefined,
      token: o.token,
      task: o.task,
      allowWrite: o.allowWrite,
      startedAt: o.startedAt,
    };
  } catch {
    return null;
  }
}

let restoredTakeover: TakeoverState | null = loadPersistedTakeover();

export function consumeRestoredTakeover(): TakeoverState | null {
  const current = restoredTakeover;
  restoredTakeover = null;
  return current;
}

function persistTakeover(t: TakeoverState | null): void {
  try {
    if (t) localStorage.setItem(TAKEOVER_KEY, JSON.stringify(t));
    else localStorage.removeItem(TAKEOVER_KEY);
  } catch {
  }
}

export function nextTabId(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}-${tabSeq++}`;
}

function makeAiDockTab(): AiDockTab {
  return { id: nextTabId("ai"), title: "新会话" };
}

function makeAiBoard(): AiBoard {
  const tab = makeAiDockTab();
  return { tabs: [tab], activeTabId: tab.id };
}

function omitAiBoard(boards: Record<string, AiBoard>, key: string): Record<string, AiBoard> {
  const next = { ...boards };
  delete next[key];
  return next;
}

function omitWorkspaceRuntime(
  runtime: Record<string, WorkspaceRuntime>,
  key: string,
): Record<string, WorkspaceRuntime> {
  const next = { ...runtime };
  delete next[key];
  return next;
}

const PLACEHOLDER_WS_TITLES = new Set(["工作区", "会话", "标签"]);

function workspaceFieldPatch(
  w: Workspace,
  spec: WorkspaceSpec,
): Partial<Pick<Workspace, "title" | "assetId" | "assetKind" | "quickTarget">> | null {
  const patch: Partial<Pick<Workspace, "title" | "assetId" | "assetKind" | "quickTarget">> = {};
  let changed = false;
  if (!w.assetId && spec.assetId) {
    patch.assetId = spec.assetId;
    changed = true;
  }
  if (!w.assetKind && spec.assetKind) {
    patch.assetKind = spec.assetKind;
    changed = true;
  }
  if (!w.quickTarget && spec.quickTarget) {
    patch.quickTarget = spec.quickTarget;
    changed = true;
  }
  if (spec.title && spec.title !== w.title && (!w.title || PLACEHOLDER_WS_TITLES.has(w.title))) {
    patch.title = spec.title;
    changed = true;
  }
  return changed ? patch : null;
}

const initialLayout = loadLayout();
const initialTerminalPrefs = loadTerminalPrefs();

function migrateWorkspaceFileLocators(
  st: UiState,
  sessionId: string,
  from: string,
  to: string,
  kind: string,
): UiState {
  const caseInsensitive = fileEditorCaseInsensitive(
    st.sessions.find((session) => session.id === sessionId)?.kind ?? "",
  );
  let changed = false;
  const workspaces = st.workspaces.map((workspace) => {
    let workspaceChanged = false;
    const panes = workspace.panes.map((pane) => {
      let paneChanged = false;
      const tabs = pane.tabs.map((tab) => {
        if ((tab.kind !== "files" && tab.kind !== "log") || tab.sessionId !== sessionId || !tab.path) {
          return tab;
        }
        const path = migratedFileEditorPath(tab.path, from, to, kind, caseInsensitive);
        if (path === null || path === tab.path) return tab;
        changed = true;
        workspaceChanged = true;
        paneChanged = true;
        return { ...tab, path, title: baseName(path) };
      });
      return paneChanged ? { ...pane, tabs } : pane;
    });
    return workspaceChanged ? { ...workspace, panes } : workspace;
  });
  migrateFileEditorDirty(sessionId, from, to, kind, caseInsensitive);
  return changed ? { ...st, workspaces } : st;
}

export const useUi = create<UiState>((set, get) => ({
  leftOpen: true,
  leftMode: "assets",
  rightOpen: initialLayout.rightOpen,
  themeMode: getThemeMode(),
  resolvedTheme: getResolvedTheme(),
  setThemeMode: (m) => {
    persistThemeMode(m);
    saveThemeModePreference(m);
    set({ themeMode: m, resolvedTheme: getResolvedTheme() });
  },
  leftWidth: initialLayout.leftWidth,
  rightWidth: initialLayout.rightWidth,
  workspaces: [],
  activeWorkspaceId: null,
  activeProject: ALL_PROJECTS,
  projectInventory: null,
  sessions: [],
  recordings: {},
  aiBusy: false,
  takeover: restoredTakeover,
  aiBoards: {},
  aiAccountEpoch: 0,
  workspaceRuntime: {},
  workspaceRuntimeEpoch: 0,
  connectionProblems: {},
  broadcast: null,
  modelProfilesRevision: 0,
  bumpModelProfilesRevision: () =>
    set((state) => ({ modelProfilesRevision: state.modelProfilesRevision + 1 })),
  connectFocusRevision: 0,
  bumpConnectFocusRevision: () =>
    set((state) => ({ connectFocusRevision: state.connectFocusRevision + 1 })),
  toasts: [],
  textPrompt: null,
  openTextPrompt: (s) => set({ textPrompt: s }),
  closeTextPrompt: (v) => {
    const cur = get().textPrompt;
    cur?.resolve(v);
    set({ textPrompt: null });
  },
  aiRulePrefill: null,
  setAiRulePrefill: (v) => set({ aiRulePrefill: v }),
  appDialog: null,
  openAppDialog: (d) => set({ appDialog: d }),
  closeAppDialog: (v) => {
    const cur = get().appDialog;
    cur?.resolve(v);
    set({ appDialog: null });
  },
  appChoice: null,
  openAppChoice: (c) => set({ appChoice: c }),
  closeAppChoice: (v) => {
    const cur = get().appChoice;
    cur?.resolve(v, false);
    set({ appChoice: null });
  },

  setLeftOpen: (v) => set({ leftOpen: v }),
  setLeftMode: (m) => set({ leftMode: m }),
  setRightOpen: (v) => set({ rightOpen: v }),
  setLeftWidth: (w) => {
    const next = clampWidth(w, LEFT_MIN, LEFT_MAX);
    set({ leftWidth: next });
  },
  setRightWidth: (w) => {
    const next = clampWidth(w, RIGHT_MIN, RIGHT_MAX);
    set({ rightWidth: next });
  },

  terminalOsc52: initialTerminalPrefs.osc52,
  setTerminalOsc52: (v) => {
    set({ terminalOsc52: v });
    saveTerminalPrefs({ osc52: v });
  },

  ensureWorkspace: (spec) => {
    const { workspaces, activeWorkspaceId } = get();
    const exists = workspaces.find((w) => {
      if (spec.sessionId !== undefined) return w.sessionId === spec.sessionId;
      if (spec.connId !== undefined) return w.connId === spec.connId;
      return w.kind === spec.kind;
    });
    if (exists) {
      const patch = workspaceFieldPatch(exists, spec);
      const activeChanged = activeWorkspaceId !== exists.id;
      if (patch || activeChanged) {
        set({
          workspaces: patch
            ? workspaces.map((w) => (w.id === exists.id ? { ...w, ...patch } : w))
            : workspaces,
          activeWorkspaceId: exists.id,
        });
      }
      return exists.id;
    }
    const id = nextTabId(`ws-${spec.kind}`);
    const pane = makePane();
    set({
      workspaces: [
        ...workspaces,
        {
          ...spec,
          id,
          panes: [pane],
          activePaneId: pane.id,
          splitRatio: 0.5,
          closable: true,
        },
      ],
      activeWorkspaceId: id,
    });
    return id;
  },

  setActiveWorkspace: (id) => set((state) => ({
    activeWorkspaceId: reconcileActiveWorkspaceForFocus({ ...state, activeWorkspaceId: id }),
  })),
  setActiveProject: (activeProject) => set((state) => ({
    activeProject,
    activeWorkspaceId: reconcileActiveWorkspaceForFocus({ ...state, activeProject }),
  })),
  setProjectInventory: (projectInventory) => set((state) => ({
    projectInventory,
    activeWorkspaceId: reconcileActiveWorkspaceForFocus({ ...state, projectInventory }),
  })),
  markDeletedAssetWorkspaces: (assetId) => set((st) => ({
    workspaces: st.workspaces.map((w) =>
      w.assetId === assetId ? { ...w, deletedTarget: true } : w,
    ),
  })),

  closeWorkspace: async (id, mode) => {
    const sameSqlDraft = (left: SqlDraft | undefined, right: SqlDraft | undefined): boolean => {
      if (!left || !right) return left === right;
      return (
        left.text === right.text &&
        left.selection.from === right.selection.from &&
        left.selection.to === right.selection.to &&
        left.schema === right.schema &&
        left.connId === right.connId &&
        left.dbKind === right.dbKind
      );
    };
    const { workspaces } = get();
    const target = workspaces.find((w) => w.id === id);
    if (!target) return false;
    const epoch = get().workspaceRuntimeEpoch;
    const tabs = target.panes.flatMap((p) => p.tabs);
    const summary = collectWorkspaceClose(tabs, target.assetKind, target.kind, id);
    const originalTabIds = new Set(summary.tabs.map((tab) => tab.id));
    await reconcileTerminalCloseState(tabs.map((tab) => tab.id));
    const currentTarget = get().workspaces.find((workspace) => workspace.id === id);
    if (!currentTarget) return false;
    const closePlan: WorkspaceCloseSummary = {
      ...collectWorkspaceClose(
        currentTarget.panes
          .flatMap((pane) => pane.tabs)
          .filter((tab) => originalTabIds.has(tab.id)),
        currentTarget.assetKind,
        currentTarget.kind,
        id,
      ),
      dirty: summary.dirty,
      hasSqlDraft: summary.hasSqlDraft,
      sqlDraft: summary.sqlDraft,
      runningAiTabs: summary.runningAiTabs,
      aiTabIds: summary.aiTabIds,
    };
    if (mode === "detach" && closePlan.blocked.length > 0) {
      get().pushToast(
        "error",
        `「${currentTarget.title}」有 ${closePlan.blocked.length} 个${closePlan.blockedKinds}终端不支持转入后台；工作区已保留，未结束任何进程`,
      );
      return false;
    }
    if (!(await confirmWorkspaceClose(closePlan, currentTarget.title, mode === "detach"))) return false;
    if (get().workspaceRuntimeEpoch !== epoch) return false;
    for (const tab of closePlan.runningAiTabs) {
      const cancelAndConfirm = aiTabRunControls.get(tab.id);
      if (!cancelAndConfirm) {
        get().pushToast("error", "停止未确认：运行状态不可用，标签已保留");
        return false;
      }
      if (!(await confirmAiRunCancellation(cancelAndConfirm))) return false;
      if (get().workspaceRuntimeEpoch !== epoch) return false;
    }

    const outcome = await executeWorkspaceClose(closePlan);
    if (get().workspaceRuntimeEpoch !== epoch) return false;
    if (outcome.failed > 0) {
      pushWorkspaceCloseToast(target.title, outcome, false);
      return false;
    }
    if (outcome.dbFailed > 0) {
      get().pushToast(
        "error",
        `「${target.title}」保持打开：${outcome.dbFailed}/${closePlan.dbConnIds.length} 个数据库断开失败：${outcome.dbError}`,
      );
    }

    const latest = get();
    const latestTarget = latest.workspaces.find((w) => w.id === id);
    if (!latestTarget) return false;
    const originalDirtyIds = new Set(summary.dirty.map((tab) => tab.id));
    const addedTabs = latestTarget.panes
      .flatMap((pane) => pane.tabs)
      .filter((tab) => !originalTabIds.has(tab.id));
    const latestRunningAiTabs = getRunningAiTabs(target.kind, id);
    const hasNewAiTasks = latestRunningAiTabs.some((tab) => {
      const original = summary.runningAiTabs.find((candidate) => candidate.id === tab.id);
      return original === undefined || original.generation !== tab.generation;
    });
    const hasNewAiTabs = (latest.aiBoards[summary.aiBoardKey]?.tabs ?? []).some(
      (tab) => !summary.aiTabIds.has(tab.id),
    );
    const addedDirty = dirtyFileEditors(
      latestTarget.panes.flatMap((pane) => pane.tabs),
      latest.sessions,
    ).filter((tab) => !originalDirtyIds.has(tab.id));
    const latestDraft = latest.workspaceRuntime[id]?.sqlDraft;
    const addedSqlDraft = !summary.hasSqlDraft && Boolean(latestDraft?.text.trim());
    const changedSqlDraft =
      summary.hasSqlDraft && Boolean(latestDraft?.text.trim()) && !sameSqlDraft(summary.sqlDraft, latestDraft);
    const hasNewContent =
      addedTabs.length > 0 ||
      addedDirty.length > 0 ||
      addedSqlDraft ||
      changedSqlDraft ||
      hasNewAiTabs ||
      hasNewAiTasks;
    const deleteWorkspace = outcome.failed === 0 && outcome.dbFailed === 0 && !hasNewContent;
    if (hasNewContent) {
      get().pushToast(
        "info",
        `「${target.title}」保持打开：关闭期间新增了未保存内容或其它标签，请处理后再次关闭`,
      );
    }

    const nextWorkspaces = deleteWorkspace
      ? latest.workspaces.filter((w) => w.id !== id)
      : latest.workspaces.map((workspace) => {
          if (workspace.id !== id) return workspace;
          const panes = workspace.panes.map((pane) => ({
            ...pane,
            tabs: pane.tabs.filter((tab) => {
              if (tab.kind === "terminal") return !outcome.succeededTabIds.has(tab.id);
              if (tab.kind === "db" && tab.connId) return !outcome.succeededDbConnIds.has(tab.connId);
              return true;
            }),
          }));
          return normalizeWorkspacePanes({ ...workspace, panes });
        });
    const broadcast = latest.broadcast
      ? pruneBroadcastToWorkspaces(latest.broadcast, nextWorkspaces)
      : null;
    set({
      workspaces: nextWorkspaces,
      activeWorkspaceId:
        latest.activeWorkspaceId === id
          ? (nextWorkspaces[nextWorkspaces.length - 1]?.id ?? null)
          : latest.activeWorkspaceId,
      ...(latest.broadcast ? { broadcast } : {}),
      ...(deleteWorkspace && latest.aiBoards[id]
        ? { aiBoards: omitAiBoard(latest.aiBoards, id) }
        : {}),
      ...(deleteWorkspace && latest.workspaceRuntime[id]
        ? { workspaceRuntime: omitWorkspaceRuntime(latest.workspaceRuntime, id) }
        : {}),
    });
    if (deleteWorkspace) {
      const board = latest.aiBoards[id];
      if (board) clearAiTabRuns(board.tabs.map((tab) => tab.id));
    }
    if (outcome.failed === 0) pushWorkspaceCloseToast(target.title, outcome, deleteWorkspace);
    return deleteWorkspace;
  },

  splitWorkspace: (workspaceId) => {
    const st = get();
    const id = workspaceId ?? st.activeWorkspaceId ?? st.workspaces[st.workspaces.length - 1]?.id;
    if (!id) return;
    const w = st.workspaces.find((x) => x.id === id);
    if (!w || w.panes.length >= 2) return;
    const pane = makePane();
    set((s2) => ({
      workspaces: s2.workspaces.map((x) =>
        x.id === id ? { ...x, panes: [...x.panes, pane], activePaneId: pane.id } : x,
      ),
    }));
    const session = st.sessions.find((x) => x.id === w.sessionId);
    if (session) void openTerminalTab(session, undefined, pane.id);
  },

  openInLowerPane: (tab, workspaceId) => {
    if (rejectPendingFileLocator(tab)) return;
    const st = get();
    const explicitWorkspace =
      workspaceId !== undefined ? st.workspaces.find((w) => w.id === workspaceId) : undefined;
    if (
      workspaceId !== undefined &&
      (!explicitWorkspace || !explicitWorkspaceMatchesTab(tab, explicitWorkspace))
    ) {
      get().pushToast("info", "标签与目标工作区不一致");
      return;
    }
    const id = resolveWorkspaceId(tab, st, workspaceId);
    if (workspaceId !== undefined && id !== workspaceId) {
      get().pushToast("info", "标签与目标工作区不一致");
      return;
    }
    const w = get().workspaces.find((x) => x.id === id);
    if (!w) return;
    if (w.panes.length >= 2) {
      get().addTab(tab, w.panes[1].id);
      return;
    }
    const height = typeof window === "undefined" ? Number.POSITIVE_INFINITY : window.innerHeight;
    if (!splitAllowedForHeight(height)) {
      get().addTab(tab, w.activePaneId ?? w.panes[0]?.id);
      get().pushToast("info", "窗口高度不足，已在当前栏打开");
      return;
    }
    const pane = makePane();
    set((s2) => ({
      workspaces: s2.workspaces.map((x) =>
        x.id === id ? { ...x, panes: [x.panes[0], pane], activePaneId: pane.id } : x,
      ),
    }));
    get().addTab(tab, pane.id);
  },

  unsplitWorkspace: async (paneId, workspaceId) => {
    const st = get();
    const id = workspaceId ?? st.activeWorkspaceId ?? st.workspaces[st.workspaces.length - 1]?.id;
    if (!id) return;
    const w = st.workspaces.find((x) => x.id === id);
    if (!w || w.panes.length < 2) return;
    const target = w.panes.find((p) => p.id === (paneId ?? w.activePaneId)) ?? w.panes[1];
    const keep = w.panes.filter((p) => p.id !== target.id);
    if (keep.length === 0) return;
    if (!(await confirmDirtyEditors(target.tabs, "取消分屏"))) return;
    if (!(await reclaimTerminals(target.tabs, "这个面板", "取消分屏", w.assetKind))) return;
    if (!(await disconnectDbTabs(target.tabs))) return;
    set((s2) => {
      const workspaces = s2.workspaces.map((workspace) =>
        workspace.id === id ? { ...workspace, panes: keep, activePaneId: keep[0].id } : workspace,
      );
      return {
        workspaces,
        ...(s2.broadcast
          ? { broadcast: pruneBroadcastToWorkspaces(s2.broadcast, workspaces) }
          : {}),
      };
    });
  },

  setActivePane: (paneId, workspaceId) =>
    set((st) => {
      const id = workspaceId ?? st.activeWorkspaceId;
      return {
        workspaces: st.workspaces.map((w) =>
          w.id === id && w.panes.some((p) => p.id === paneId) ? { ...w, activePaneId: paneId } : w,
        ),
      };
    }),

  setSplitRatio: (ratio, workspaceId) =>
    set((st) => {
      const id = workspaceId ?? st.activeWorkspaceId;
      const clamped = Math.min(0.85, Math.max(0.15, ratio));
      return {
        workspaces: st.workspaces.map((w) =>
          w.id === id ? { ...w, splitRatio: clamped } : w,
        ),
      };
    }),

  setActiveTab: (id) => {
    clearTabUnread(id);
    set((st) => {
      for (const w of st.workspaces) {
        for (const p of w.panes) {
          if (!p.tabs.some((t) => t.id === id)) continue;
          return {
            activeWorkspaceId: w.id,
            workspaces: st.workspaces.map((x) =>
              x.id === w.id
                ? {
                    ...x,
                    activePaneId: p.id,
                    panes: x.panes.map((q) =>
                      q.id === p.id ? { ...q, activeTabId: id } : q,
                    ),
                  }
                : x,
            ),
          };
        }
      }
      return {};
    });
  },

  addTab: (tab, paneId) => {
    if (rejectPendingFileLocator(tab)) return;
    const paneWorkspace = paneId
      ? get().workspaces.find((w) => w.panes.some((p) => p.id === paneId))
      : undefined;
    if (paneId && (!paneWorkspace || !explicitWorkspaceMatchesTab(tab, paneWorkspace))) {
      get().pushToast("info", "标签与目标工作区不一致");
      return;
    }
    if (tab.kind === "background" && !tab.sessionId && paneWorkspace?.kind !== "session") {
      get().pushToast("info", "后台会话需要明确的主机工作区");
      return;
    }

    const wsId = resolveWorkspaceId(tab, get(), paneWorkspace?.id);
    if (isWorkspaceToolTab(tab)) {
      const current = get();
      const migrated = migrateToolTabs(current.workspaces, current.aiBoards);
      if (migrated !== current.workspaces) set({ workspaces: migrated });
    }
    const w = get().workspaces.find((x) => x.id === wsId);
    if (!w) return;
    let scopedTab = tab;
    if (tab.kind === "background") {
      const sessionId = tab.sessionId ?? paneWorkspace?.sessionId;
      if (w.kind !== "session" || !sessionId || w.sessionId !== sessionId) {
        get().pushToast("info", "后台会话需要明确的主机工作区");
        return;
      }
      scopedTab = {
        ...tab,
        id: `background-${w.id}`,
        sessionId,
        ...(w.assetId ? { assetId: w.assetId } : {}),
      };
    }

    const panes = w.panes.length ? w.panes : [makePane()];
    const target = paneId
      ? panes.find((p) => p.id === paneId)
      : pickPaneForTab(panes, w.activePaneId, scopedTab);
    if (!target) {
      get().pushToast("info", "标签与目标工作区不一致");
      return;
    }

    const caseInsensitive = scopedTab.sessionId
      ? fileEditorCaseInsensitive(get().sessions.find((s) => s.id === scopedTab.sessionId)?.kind ?? "")
      : false;
    for (const p of w.panes) {
      const exists = p.tabs.find((t) => sameTab(t, scopedTab, caseInsensitive));
      if (!exists) continue;
      const tabPatch =
        scopedTab.kind === "history" && scopedTab.assetId === undefined
          ? { ...scopedTab, assetId: exists.assetId }
          : scopedTab;
      set((st) => ({
        activeWorkspaceId: w.id,
        workspaces: st.workspaces.map((x) =>
          x.id === w.id
            ? {
                ...x,
                activePaneId: p.id,
                panes: x.panes.map((q) =>
                  q.id === p.id
                    ? {
                        ...q,
                        tabs: q.tabs.map((t) =>
                          t.id === exists.id ? { ...t, ...tabPatch, id: exists.id } : t,
                        ),
                        activeTabId: exists.id,
                      }
                    : q,
                ),
              }
            : x,
        ),
      }));
      return;
    }
    set((st) => ({
      activeWorkspaceId: wsId,
      workspaces: st.workspaces.map((x) =>
        x.id === wsId
          ? {
              ...x,
              activePaneId: target.id,
              panes: panes.map((q) =>
                q.id === target.id
                  ? { ...q, tabs: [...q.tabs, scopedTab], activeTabId: scopedTab.id }
                  : q,
              ),
            }
          : x,
      ),
    }));
  },

  closeTab: async (id, mode, suppressErrorToast) => {
    const st = get();
    let target: AppTab | undefined;
    let wsId: string | undefined;
    let paneId: string | undefined;
    for (const w of st.workspaces) {
      for (const p of w.panes) {
        const t = p.tabs.find((x) => x.id === id);
        if (t) {
          target = t;
          wsId = w.id;
          paneId = p.id;
          break;
        }
      }
      if (target) break;
    }
    if (!target || !wsId || !paneId) return false;
    if (target.tabId) {
      try {
        const kind = st.sessions.find((session) => session.id === target.sessionId)?.kind
          ?? st.workspaces.find((workspace) => workspace.id === wsId)?.assetKind;
        const remote = !target.containerId && (kind === "ssh" || kind === "device");
        const closeMode = mode ?? (remote && target.exited !== true ? "detach" : undefined);
        if (remote) {
          await terminalApi.closeTab(target.tabId, closeMode, target.sessionId);
        } else {
          await terminalApi.closeTab(target.tabId, closeMode);
        }
      } catch (e) {
        const code = (e as { code?: string } | null)?.code;
        const endedTab = target.kind === "terminal" && (
          code === "not_found" || (code === "disconnected" && (target.dead || target.exited))
        );
        if (!endedTab) {
          get().updateTab(id, { closeError: describeError(e) });
          if (!suppressErrorToast) get().pushToast("error", `终端操作失败：${describeError(e)}`);
          void get().resyncSessions({ silent: true });
          return false;
        }
      }
    } else if (target.kind === "db" && target.connId) {
      try {
        await dbApi.disconnect(target.connId);
      } catch (e) {
        get().updateTab(id, { closeError: describeError(e) });
        get().pushToast("error", `数据库断开失败：${describeError(e)}`);
        return false;
      }
    }
    set((s2) => {
      const workspaces = s2.workspaces.map((w) => {
        if (w.id !== wsId) return w;
        const pane = w.panes.find((p) => p.id === paneId);
        if (!pane) return w;
        const tabs = pane.tabs.filter((t) => t.id !== id);
        const dropPane = tabs.length === 0 && w.panes.length > 1;
        const panes = dropPane
          ? w.panes.filter((p) => p.id !== paneId)
          : w.panes.map((p) =>
              p.id === paneId
                ? {
                    ...p,
                    tabs,
                    activeTabId: p.activeTabId === id ? (tabs[tabs.length - 1]?.id ?? null) : p.activeTabId,
                  }
                : p,
            );
        return {
          ...w,
          panes,
          activePaneId: dropPane ? (panes[0]?.id ?? w.activePaneId) : w.activePaneId,
        };
      });
      return {
        workspaces,
        ...(s2.broadcast
          ? { broadcast: pruneBroadcastToWorkspaces(s2.broadcast, workspaces) }
          : {}),
      };
    });
    void get().resyncSessions({ silent: true });
    return true;
  },

  updateTab: (id, patch) =>
    set((st) => {
      let found = false;
      let from: string | undefined;
      let workspaceId: string | undefined;
      for (const w of st.workspaces) {
        for (const p of w.panes) {
          const t = p.tabs.find((x) => x.id === id);
          if (t) {
            found = true;
            from = t.sessionId;
            workspaceId = w.id;
          }
        }
      }
      if (!found) return st;
      const to =
        typeof patch.sessionId === "string" && from !== undefined && patch.sessionId !== from
          ? patch.sessionId
          : undefined;
      const workspaces =
        to !== undefined && workspaceId !== undefined
          ? rebindWorkspaceSession(st.workspaces, workspaceId, from, to)
          : st.workspaces;
      return {
        workspaces: workspaces.map((w) => {
          let changed = false;
          const panes = w.panes.map((p) => {
            if (!p.tabs.some((t) => t.id === id)) return p;
            changed = true;
            return {
              ...p,
              tabs: p.tabs.map((t) => t.id === id
                ? { ...t, ...patch, restorePending: patch.tabId ? undefined : (patch.restorePending ?? t.restorePending) }
                : t),
            };
          });
          return changed ? { ...w, panes } : w;
        }),
      };
    }),

  migrateFileEditorLocators: (sessionId, from, to, kind) =>
    set((st) => migrateWorkspaceFileLocators(st, sessionId, from, to, kind)),

  resolveUnknownFileEditorRename: (sessionId, path, renamed) =>
    set((st) => {
      const operation = unknownFileEditorRename(sessionId, path);
      if (!operation) return st;
      if (!renamed) {
        clearUnknownFileEditorRename(operation.token);
        return st;
      }
      const next = migrateWorkspaceFileLocators(
        st,
        sessionId,
        operation.from,
        operation.to,
        operation.kind,
      );
      clearUnknownFileEditorRename(operation.token);
      return next;
    }),

  moveTabInPane: (paneId, fromIndex, toIndex) =>
    set((st) => {
      for (const w of st.workspaces) {
        const pane = w.panes.find((p) => p.id === paneId);
        if (!pane) continue;
        const tabs = moveItem(pane.tabs, fromIndex, toIndex);
        if (tabs === pane.tabs) return {};
        return {
          workspaces: st.workspaces.map((x) =>
            x.id === w.id
              ? { ...x, panes: x.panes.map((q) => (q.id === paneId ? { ...q, tabs } : q)) }
              : x,
          ),
        };
      }
      return {};
    }),

  updateWorkspaceSession: (workspaceId, sessionId) =>
    set((st) => ({
      workspaces: rebindWorkspaceSession(
        st.workspaces,
        workspaceId,
        st.workspaces.find((w) => w.id === workspaceId)?.sessionId,
        sessionId,
      ),
    })),

  setRecordingStatus: (status) =>
    set((state) => {
      const current = state.recordings[status.tabId];
      if (current && current.generation > status.generation) return state;
      if (status.state === "never-started") {
        const { [status.tabId]: _, ...recordings } = state.recordings;
        return { recordings };
      }
      return { recordings: { ...state.recordings, [status.tabId]: status } };
    }),
  resyncRecording: async (tabId) => {
    const request = ++recordingRequestSeq;
    recordingRequests.set(tabId, request);
    const epoch = recordingsEpoch;
    try {
      const status = await terminalApi.recordingStatus(tabId);
      if (epoch !== recordingsEpoch || recordingRequests.get(tabId) !== request) return;
      get().setRecordingStatus(status);
    } catch {
    }
  },
  setSessions: (s) => set({ sessions: s }),
  sessionResyncFailed: false,
  sessionResyncCompleted: false,
  layoutSyncCompleted: false,
  workspaceRestoreDismissed: false,
  workspaceRestoreResults: {},
  dismissWorkspaceRestoreBanner: () => set({ workspaceRestoreDismissed: true }),
  resyncSessions: async (options) => {
    const epoch = sessionsEpoch;
    try {
      const list = await sessionApi.list();
      if (epoch !== sessionsEpoch) return;
      set({ sessions: list, sessionResyncFailed: false });
    } catch (e) {
      console.error("[NexTerm] Failed to refresh session list", e);
      if (epoch !== sessionsEpoch || options?.silent) return;
      if (get().sessionResyncFailed) return;
      set({ sessionResyncFailed: true });
      get().pushToast("error", `刷新会话列表失败：${describeError(e)}`);
    } finally {
      if (epoch === sessionsEpoch) set({ sessionResyncCompleted: true });
    }
  },
  resetSessions: (options) => {
    const clearWorkspaceRuntime = options?.clearWorkspaceRuntime === true;
    sessionsEpoch += 1;
    workspaceProblemResetGeneration += 1;
    recordingsEpoch += 1;
    abnormalDisconnectNotices.clear();
    set((state) => ({
      sessions: [],
      recordings: {},
      helperProgress: {},
      sessionResyncFailed: false,
      sessionResyncCompleted: false,
      workspaceRuntimeEpoch: state.workspaceRuntimeEpoch + (clearWorkspaceRuntime ? 1 : 0),
      workspaceRuntime: Object.fromEntries(
        Object.entries(state.workspaceRuntime).map(([workspaceId, runtime]) => [
          workspaceId,
          clearWorkspaceRuntime
            ? { generation: ++workspaceGenerationSeq }
            : { ...runtime, problems: undefined },
        ]),
      ) as Record<string, WorkspaceRuntime>,
      connectionProblems: {},
    }));
  },
  resetAccountWorkspaces: () => {
    sessionsEpoch += 1;
    workspaceProblemResetGeneration += 1;
    recordingsEpoch += 1;
    abnormalDisconnectNotices.clear();
    set((state) => ({
      sessions: [],
      recordings: {},
      sessionResyncFailed: false,
      sessionResyncCompleted: false,
      workspaceRestoreDismissed: false,
      workspaceRestoreResults: {},
      workspaces: [],
      activeWorkspaceId: null,
      projectInventory: null,
      connectingAssetIds: [],
      connectProgress: {},
      helperProgress: {},
      workspaceRuntimeEpoch: state.workspaceRuntimeEpoch + 1,
      workspaceRuntime: {},
      connectionProblems: {},
      broadcast: null,
    }));
  },
  setAiBusy: (v) => set({ aiBusy: v }),
  setTakeover: (t) => {
    restoredTakeover = null;
    persistTakeover(t);
    set({ takeover: t });
  },
  setBroadcast: (b) =>
    set({
      broadcast: b
        ? {
            origin: b.origin,
            targetIds: [...new Set(b.targetIds)],
            generation: ++broadcastGeneration,
            results: {},
          }
        : null,
    }),
  reportBroadcastTarget: (generation, update) =>
    set((state) => {
      const broadcast = state.broadcast;
      if (
        !broadcast ||
        broadcast.generation !== generation ||
        !broadcast.targetIds.includes(update.targetId)
      ) {
        return state;
      }
      return {
        broadcast: {
          ...broadcast,
          results: {
            ...broadcast.results,
            [update.targetId]: { status: update.status, reason: update.reason },
          },
        },
      };
    }),

  getWorkspaceRuntime: (workspaceId) => get().workspaceRuntime[workspaceId],
  ensureWorkspaceRuntime: (workspaceId) => {
    const existing = get().workspaceRuntime[workspaceId];
    if (existing) return existing;
    if (!get().workspaces.some((workspace) => workspace.id === workspaceId)) return undefined;
    const generation = ++workspaceGenerationSeq;
    set((state) =>
      state.workspaceRuntime[workspaceId]
        ? state
        : {
            workspaceRuntime: {
              ...state.workspaceRuntime,
              [workspaceId]: { generation },
            },
          },
    );
    return get().workspaceRuntime[workspaceId];
  },
  updateSqlDraft: (workspaceId, draft, expectedGeneration) => {
    const current = get();
    if (!current.workspaces.some((w) => w.id === workspaceId)) return;
    const existing = current.workspaceRuntime[workspaceId];
    if (expectedGeneration !== undefined && existing?.generation !== expectedGeneration) return;
    const generation = existing?.generation ?? ++workspaceGenerationSeq;
    set({
      workspaceRuntime: {
        ...current.workspaceRuntime,
        [workspaceId]: { ...(existing ?? {}), generation, sqlDraft: draft },
      },
    });
  },
  recordSqlExecution: (workspaceId, record) => {
    set((state) => {
      const existing = state.workspaceRuntime[workspaceId];
      if (
        !state.workspaces.some((workspace) => workspace.id === workspaceId) ||
        !existing ||
        existing.generation !== record.generation
      ) {
        return state;
      }
      const sql = boundedSqlExecutionText(record.sql);
      const error = record.error === null ? null : boundedSqlExecutionText(record.error);
      const entry: SqlExecutionRecord = {
        ...record,
        id: ++sqlExecutionSeq,
        target: { ...record.target },
        sql: sql.text,
        sqlTruncated: sql.truncated,
        error: error?.text ?? null,
        errorTruncated: error?.truncated ?? false,
      };
      return {
        workspaceRuntime: {
          ...state.workspaceRuntime,
          [workspaceId]: {
            ...existing,
            sqlExecutionHistory: [entry, ...(existing.sqlExecutionHistory ?? [])].slice(
              0,
              SQL_EXECUTION_HISTORY_LIMIT,
            ),
          },
        },
      };
    });
  },
  recordWorkspaceProblem: (workspaceId, problem) => {
    set((state) => {
      const existing = state.workspaceRuntime[workspaceId];
      if (
        !state.workspaces.some((workspace) => workspace.id === workspaceId) ||
        !existing ||
        existing.generation !== problem.generation ||
        (problem.resetGeneration !== undefined &&
          problem.resetGeneration !== workspaceProblemResetGeneration)
      ) {
        return state;
      }
      const entry = makeWorkspaceProblem(problem);
      return {
        workspaceRuntime: {
          ...state.workspaceRuntime,
          [workspaceId]: {
            ...existing,
            problems: [entry, ...(existing.problems ?? [])].slice(0, WORKSPACE_PROBLEM_LIMIT),
          },
        },
      };
    });
  },
  recordConnectionProblem: (problem) => {
    if (
      problem.resetGeneration !== undefined &&
      problem.resetGeneration !== workspaceProblemResetGeneration
    ) {
      return;
    }
    const workspace = findConnectionWorkspace(get().workspaces, problem);
    const generation = workspace
      ? get().ensureWorkspaceRuntime(workspace.id)?.generation
      : undefined;
    if (workspace && generation === undefined) return;
    const input = { ...problem, generation: generation ?? 0 };
    delete input.workspaceId;
    const entry = makeWorkspaceProblem(input);
    set((state) => {
      if (
        problem.resetGeneration !== undefined &&
        problem.resetGeneration !== workspaceProblemResetGeneration
      ) {
        return state;
      }
      const workspaceId = workspace && state.workspaces.some((item) => item.id === workspace.id)
        ? workspace.id
        : null;
      const workspaceRuntime = withoutConnectionProblem(state.workspaceRuntime, problem.connectionKey);
      const connectionProblems = { ...state.connectionProblems };
      delete connectionProblems[problem.connectionKey];
      if (workspaceId) {
        const existing = workspaceRuntime[workspaceId];
        if (!existing || existing.generation !== generation) return state;
        return {
          connectionProblems,
          workspaceRuntime: {
            ...workspaceRuntime,
            [workspaceId]: {
              ...existing,
              problems: [entry, ...(existing.problems ?? [])].slice(0, WORKSPACE_PROBLEM_LIMIT),
            },
          },
        };
      }
      connectionProblems[problem.connectionKey] = entry;
      return {
        connectionProblems: Object.fromEntries(
          Object.entries(connectionProblems)
            .sort(([, left], [, right]) => right.id - left.id)
            .slice(0, WORKSPACE_PROBLEM_LIMIT),
        ),
        workspaceRuntime,
      };
    });
  },
  clearConnectionProblem: (connectionKey) => {
    set((state) => {
      const workspaceRuntime = withoutConnectionProblem(state.workspaceRuntime, connectionKey);
      if (!state.connectionProblems[connectionKey] && workspaceRuntime === state.workspaceRuntime) {
        return state;
      }
      const connectionProblems = { ...state.connectionProblems };
      delete connectionProblems[connectionKey];
      return { connectionProblems, workspaceRuntime };
    });
  },
  dismissWorkspaceProblem: (workspaceId, problemId, expectedGeneration) => {
    const current = get();
    const existing = current.workspaceRuntime[workspaceId];
    if (!existing) return;
    if (expectedGeneration !== undefined && existing.generation !== expectedGeneration) return;
    set({
      workspaceRuntime: {
        ...current.workspaceRuntime,
        [workspaceId]: {
          ...existing,
          problems: (existing.problems ?? []).filter((problem) => problem.id !== problemId),
        },
      },
    });
  },
  clearSqlDraft: (workspaceId, expectedGeneration) => {
    const current = get();
    const existing = current.workspaceRuntime[workspaceId];
    if (!existing) return;
    if (expectedGeneration !== undefined && existing.generation !== expectedGeneration) return;
    set({
      workspaceRuntime: {
        ...current.workspaceRuntime,
        [workspaceId]: { ...existing, sqlDraft: undefined },
      },
    });
  },

  resetAiAccount: () => {
    const tabIds = Object.values(get().aiBoards).flatMap((board) => board.tabs.map((tab) => tab.id));
    clearAiTabRuns(tabIds);
    get().setTakeover(null);
    set((state) => ({ aiBoards: {}, aiBusy: false, aiAccountEpoch: state.aiAccountEpoch + 1 }));
  },
  ensureAiBoard: (key) => {
    const existing = get().aiBoards[key];
    if (existing) return existing;
    const board = makeAiBoard();
    set((s) => (s.aiBoards[key] ? s : { aiBoards: { ...s.aiBoards, [key]: board } }));
    return get().aiBoards[key];
  },
  addAiTab: (key) => {
    get().ensureAiBoard(key);
    const tab = makeAiDockTab();
    set((s) => {
      const board = s.aiBoards[key];
      if (!board) return s;
      return { aiBoards: { ...s.aiBoards, [key]: { tabs: [...board.tabs, tab], activeTabId: tab.id } } };
    });
    return tab.id;
  },
  closeAiTab: (key, tabId, expectedEpoch) =>
    set((s) => {
      if (expectedEpoch !== undefined && s.aiAccountEpoch !== expectedEpoch) return s;
      const board = s.aiBoards[key];
      if (!board) return s;
      if (board.tabs.length <= 1) {
        if (board.tabs[0]?.id !== tabId) return s;
        clearAiTabRun(tabId);
        return { aiBoards: { ...s.aiBoards, [key]: makeAiBoard() } };
      }
      const index = board.tabs.findIndex((t) => t.id === tabId);
      if (index < 0) return s;
      const tabs = board.tabs.filter((t) => t.id !== tabId);
      const activeTabId =
        board.activeTabId === tabId
          ? (tabs[Math.min(index, tabs.length - 1)]?.id ?? null)
          : board.activeTabId;
      return { aiBoards: { ...s.aiBoards, [key]: { tabs, activeTabId } } };
    }),
  setActiveAiTab: (key, tabId) =>
    set((s) => {
      const board = s.aiBoards[key];
      if (!board || !board.tabs.some((t) => t.id === tabId)) return s;
      return { aiBoards: { ...s.aiBoards, [key]: { ...board, activeTabId: tabId } } };
    }),
  moveAiTab: (key, fromIndex, toIndex) =>
    set((s) => {
      const board = s.aiBoards[key];
      if (!board) return s;
      const tabs = moveItem(board.tabs, fromIndex, toIndex);
      if (tabs === board.tabs) return s;
      return { aiBoards: { ...s.aiBoards, [key]: { ...board, tabs } } };
    }),
  updateAiTab: (key, tabId, patch, expectedEpoch) =>
    set((s) => {
      if (expectedEpoch !== undefined && s.aiAccountEpoch !== expectedEpoch) return s;
      const board = s.aiBoards[key];
      if (!board || !board.tabs.some((t) => t.id === tabId)) return s;
      return {
        aiBoards: {
          ...s.aiBoards,
          [key]: { ...board, tabs: board.tabs.map((t) => (t.id === tabId ? { ...t, ...patch } : t)) },
        },
      };
    }),

  connectingAssetIds: [],

  connectProgress: {},
  setConnectProgress: (assetId, progress) =>
    set((st) => {
      if (!assetId) return st;
      const next = { ...st.connectProgress };
      if (progress) next[assetId] = progress;
      else delete next[assetId];
      return { connectProgress: next };
    }),
  helperProgress: {},
  setHelperProgress: (progress) =>
    set((st) => {
      if (!progress?.sessionId || !progress.assetId) return st;
      const key = JSON.stringify([progress.sessionId, progress.assetId]);
      const next = { ...st.helperProgress };
      if (progress.done && !progress.error) {
        delete next[key];
      } else {
        next[key] = progress;
      }
      return { helperProgress: next };
    }),

  pushToast: (kind, text, action) => {
    const existing = get().toasts.find((t) => t.kind === kind && t.text === text);
    if (existing) get().dismissToast(existing.id);
    const id = toastSeq++;
    set((st) => ({ toasts: [...st.toasts, { id, kind, text, action }] }));
    window.setTimeout(() => get().dismissToast(id), kind === "error" ? 8000 : 3500);
  },
  dismissToast: (id) =>
    set((st) => ({ toasts: st.toasts.filter((t) => t.id !== id) })),
}));

useUi.subscribe((state, previous) => {
  if (
    state.activeProject === previous.activeProject &&
    state.projectInventory === previous.projectInventory &&
    state.workspaces === previous.workspaces &&
    state.activeWorkspaceId === previous.activeWorkspaceId
  ) {
    return;
  }
  const activeWorkspaceId = reconcileActiveWorkspaceForFocus(state);
  if (activeWorkspaceId !== state.activeWorkspaceId) useUi.setState({ activeWorkspaceId });
});

useUi.subscribe((state, previous) => {
  if (
    state.rightOpen !== previous.rightOpen ||
    state.leftWidth !== previous.leftWidth ||
    state.rightWidth !== previous.rightWidth
  ) {
    // Cache remote changes too, so startup matches the last visible layout before sync finishes.
    saveLayout({
      leftWidth: state.leftWidth,
      rightWidth: state.rightWidth,
      rightOpen: state.rightOpen,
    });
  }
});

useUi.subscribe((state, previous) => {
  if (state.workspaces === previous.workspaces) return;
  const openTabIds = new Set(
    state.workspaces.flatMap((workspace) =>
      workspace.panes.flatMap((pane) => pane.tabs.map((tab) => tab.id)),
    ),
  );
  retainUnreadTabs(openTabIds);
});

initTheme();
subscribeTheme((m, resolved) => {
  useUi.setState({ themeMode: m, resolvedTheme: resolved });
});

function rejectPendingFileLocator(tab: AppTab): boolean {
  if (
    (tab.kind !== "files" && tab.kind !== "log") ||
    !tab.sessionId ||
    !tab.path ||
    !fileEditorRenamePending(tab.sessionId, tab.path)
  ) {
    return false;
  }
  useUi.getState().pushToast("info", "重命名进行中，完成后才能打开该文件");
  return true;
}

function explicitWorkspaceMatchesTab(tab: AppTab, workspace: Workspace): boolean {
  if (isWorkspaceToolTab(tab)) return workspace.kind === "tools";
  if (tab.kind === "background" && workspace.kind !== "session") return false;
  if (tab.sessionId) return workspace.sessionId === tab.sessionId;
  if (tab.connId) return workspace.connId === tab.connId;
  return true;
}

function resolveWorkspaceId(
  tab: AppTab,
  st: { workspaces: Workspace[]; sessions: SessionRuntimeInfo[]; activeWorkspaceId: string | null },
  workspaceId?: string,
): string {
  const { ensureWorkspace } = useUi.getState();
  if (isWorkspaceToolTab(tab)) return ensureWorkspace({ kind: "tools", title: "工具" });
  if (tab.sessionId) {
    const hit = st.workspaces.find((w) => w.sessionId === tab.sessionId);
    if (hit) return hit.id;
  }
  if (tab.connId) {
    const hit = st.workspaces.find((w) => w.connId === tab.connId);
    if (hit) return hit.id;
  }
  if (tab.sessionId) {
    const s = st.sessions.find((x) => x.id === tab.sessionId);
    return ensureWorkspace({
      kind: "session",
      sessionId: tab.sessionId,
      title: s?.name ?? "会话",
      assetId: s?.assetId ?? undefined,
      assetKind: s?.kind,
    });
  }
  if (tab.connId) {
    return ensureWorkspace({
      kind: "db",
      connId: tab.connId,
      dbKind: tab.dbKind,
      title: tab.title,
    });
  }
  if (workspaceId) {
    const explicit = st.workspaces.find((w) => w.id === workspaceId);
    if (explicit) return explicit.id;
  }
  if (st.activeWorkspaceId) return st.activeWorkspaceId;
  return ensureWorkspace({ kind: "tools", title: "工具" });
}

function rebindWorkspaceSession(
  workspaces: Workspace[],
  workspaceId: string,
  fromSessionId: string | undefined,
  sessionId: string,
): Workspace[] {
  if (fromSessionId === sessionId) return workspaces;
  return workspaces.map((w) => {
    if (w.id !== workspaceId || w.sessionId !== fromSessionId) return w;
    return {
      ...w,
      sessionId,
      panes:
        w.kind === "session" && fromSessionId !== undefined
          ? w.panes.map((pane) => ({
              ...pane,
              tabs: pane.tabs.map((tab) =>
                tab.sessionId === fromSessionId ? { ...tab, sessionId } : tab,
              ),
            }))
          : w.panes,
    };
  });
}

function makePane(): Pane {
  return { id: nextTabId("pane"), tabs: [], activeTabId: null };
}

function normalizeWorkspacePanes(w: Workspace): Workspace {
  const withActiveTabs = w.panes.map((pane) => ({
    ...pane,
    activeTabId:
      pane.activeTabId && pane.tabs.some((tab) => tab.id === pane.activeTabId)
        ? pane.activeTabId
        : (pane.tabs[pane.tabs.length - 1]?.id ?? null),
  }));
  const nonEmpty = withActiveTabs.filter((pane) => pane.tabs.length > 0);
  let panes: Pane[] = nonEmpty.length > 0 ? nonEmpty : withActiveTabs.slice(0, 1);
  if (panes.length === 0) panes = [makePane()];
  return {
    ...w,
    panes,
    activePaneId: panes.some((pane) => pane.id === w.activePaneId)
      ? w.activePaneId
      : panes[0].id,
  };
}

function hasValidAiBoard(aiBoards: Record<string, unknown> | undefined, workspaceId: string): boolean {
  const board = aiBoards?.[workspaceId];
  if (typeof board !== "object" || board === null || Array.isArray(board)) return false;
  const tabs = (board as { tabs?: unknown }).tabs;
  if (!Array.isArray(tabs)) return false;
  return tabs.some((tab) => {
    if (typeof tab !== "object" || tab === null || Array.isArray(tab)) return false;
    const id = (tab as { id?: unknown }).id;
    return typeof id === "string" && id.length > 0;
  });
}

export function migrateToolTabs(
  workspaces: Workspace[],
  aiBoards?: Record<string, unknown>,
): Workspace[] {
  const toolsIndex = workspaces.findIndex((w) => w.kind === "tools");
  const sourceTools = toolsIndex >= 0 ? workspaces[toolsIndex] : undefined;
  const hasToolTabs = workspaces.some((w) =>
    w.panes.some((pane) => pane.tabs.some((tab) => isWorkspaceToolTab(tab))),
  );
  if (!sourceTools && !hasToolTabs) return workspaces;

  const preservedTools = new Set(
    workspaces.filter((w) => w.kind === "tools" && hasValidAiBoard(aiBoards, w.id)),
  );
  const multipleTools = workspaces.some((w, index) => w.kind === "tools" && index !== toolsIndex);
  const hasExternalToolTabs = workspaces.some(
    (w) =>
      w !== sourceTools &&
      w.panes.some((pane) => pane.tabs.some((tab) => isWorkspaceToolTab(tab))),
  );
  const kinds = new Set<PaneKind>();
  let hasDuplicateToolTabs = false;
  for (const pane of sourceTools?.panes ?? []) {
    for (const tab of pane.tabs) {
      if (!isWorkspaceToolTab(tab)) continue;
      if (kinds.has(tab.kind)) hasDuplicateToolTabs = true;
      kinds.add(tab.kind);
    }
  }
  if (sourceTools && !multipleTools && !hasExternalToolTabs && !hasDuplicateToolTabs) {
    if (sourceTools.title === "工具") return workspaces;
    return workspaces.map((w) => (w === sourceTools ? { ...w, title: "工具" } : w));
  }

  let tools: Workspace;
  if (sourceTools) {
    tools = { ...sourceTools, title: "工具" };
  } else {
    const pane = makePane();
    tools = {
      id: nextTabId("ws-tools"),
      kind: "tools",
      title: "工具",
      panes: [pane],
      activePaneId: pane.id,
      splitRatio: 0.5,
      closable: true,
    };
  }

  const selected = new Map<PaneKind, AppTab>();
  const collect = (w: Workspace) => {
    for (const pane of w.panes) {
      for (const tab of pane.tabs) {
        if (!isWorkspaceToolTab(tab)) continue;
        const candidate =
          tab.kind === "history" && w.kind === "session"
            ? {
                ...tab,
                sessionId: tab.sessionId ?? w.sessionId,
                assetId: tab.assetId ?? w.assetId,
              }
            : tab;
        const existing = selected.get(tab.kind);
        if (!existing) {
          selected.set(tab.kind, candidate);
          continue;
        }
        if (tab.kind !== "history") continue;
        const sessionId = existing.sessionId ?? candidate.sessionId;
        const assetId = existing.assetId ?? candidate.assetId;
        if (sessionId !== existing.sessionId || assetId !== existing.assetId) {
          selected.set(tab.kind, { ...existing, sessionId, assetId });
        }
      }
    }
  };
  if (sourceTools) collect(sourceTools);
  for (const w of workspaces) {
    if (w !== sourceTools) collect(w);
  }

  const kept = new Set<AppTab>();
  const basePanes = (tools.panes.length > 0 ? tools.panes : [makePane()]).map((pane) => ({
    ...pane,
    tabs: pane.tabs.filter((tab) => {
      if (!isWorkspaceToolTab(tab)) return true;
      if (selected.get(tab.kind) !== tab) return false;
      kept.add(tab);
      return true;
    }),
  }));
  const additions = [...selected.values()].filter((tab) => !kept.has(tab));
  for (const w of workspaces) {
    if (w.kind !== "tools" || w === sourceTools) continue;
    for (const pane of w.panes) {
      additions.push(...pane.tabs.filter((tab) => !isWorkspaceToolTab(tab)));
    }
  }
  const targetPaneId = basePanes.some((pane) => pane.id === tools.activePaneId)
    ? tools.activePaneId
    : basePanes[0].id;
  const normalizedTools = normalizeWorkspacePanes({
    ...tools,
    panes: basePanes.map((pane) =>
      pane.id === targetPaneId ? { ...pane, tabs: [...pane.tabs, ...additions] } : pane,
    ),
  });

  const next: Workspace[] = [];
  let inserted = false;
  for (let i = 0; i < workspaces.length; i++) {
    const w = workspaces[i];
    if (i === toolsIndex) {
      next.push(normalizedTools);
      inserted = true;
      continue;
    }
    if (w.kind === "tools") {
      if (preservedTools.has(w)) {
        const panes = w.panes.map((pane) => ({ ...pane, tabs: [], activeTabId: null }));
        next.push({
          ...w,
          panes,
          activePaneId: panes.some((pane) => pane.id === w.activePaneId)
            ? w.activePaneId
            : (panes[0]?.id ?? w.activePaneId),
        });
      }
      continue;
    }
    const panes = w.panes.map((pane) => ({
      ...pane,
      tabs: pane.tabs.filter((tab) => !isWorkspaceToolTab(tab)),
    }));
    const changed = w.panes.some((pane, index) => pane.tabs.length !== panes[index].tabs.length);
    next.push(changed ? normalizeWorkspacePanes({ ...w, panes }) : w);
  }
  if (!inserted) next.push(normalizedTools);
  return next;
}

function sameTab(t: AppTab, tab: AppTab, caseInsensitive = false): boolean {
  if (t.id === tab.id) return true;
  if (t.tabId && tab.tabId && t.tabId === tab.tabId) return true;
  if (tab.kind === "background" && t.kind === "background") return true;
  if (isWorkspaceToolTab(tab) && t.kind === tab.kind) return true;
  if ((tab.kind === "files" || tab.kind === "log") && tab.path && t.path) {
    return (
      t.kind === tab.kind &&
      t.sessionId === tab.sessionId &&
      fileEditorPathMatches(t.path, tab.path, "file", caseInsensitive)
    );
  }
  return (
    tab.kind !== "terminal" &&
    t.kind === tab.kind &&
    t.sessionId === tab.sessionId &&
    t.connId === tab.connId &&
    !t.tabId &&
    !tab.tabId &&
    t.path === tab.path
  );
}

function pickPaneForTab(panes: Pane[], activePaneId: string, tab: AppTab): Pane {
  const active = panes.find((p) => p.id === activePaneId) ?? panes[0];
  if (tab.kind !== "files" || !tab.path) return active;
  return panes.find((p) => p.tabs.some((t) => t.kind === "files" && t.path)) ?? active;
}

export function useActiveWorkspace(): Workspace | null {
  return useUi((state) => focusedActiveWorkspace(state));
}

function workspaceForNavigation(
  state: Pick<UiState, "workspaces" | "activeWorkspaceId" | "activeProject" | "projectInventory">,
): Workspace | null {
  return focusedActiveWorkspace(state);
}

function cycleIndex(current: number, length: number, direction: 1 | -1): number {
  return (current + direction + length) % length;
}

export function cycleActiveWorkspace(direction: 1 | -1): string | null {
  const state = useUi.getState();
  const current = workspaceForNavigation(state);
  if (!current) return null;
  const visible = focusedWorkspaces(state);
  const index = visible.indexOf(current);
  const target = visible[cycleIndex(index, visible.length, direction)];
  if (!target) return null;
  state.setActiveWorkspace(target.id);
  return target.id;
}

export function cycleActiveTab(direction: 1 | -1): string | null {
  const state = useUi.getState();
  const workspace = workspaceForNavigation(state);
  const panes = workspace?.panes.slice(0, 2) ?? [];
  const pane = panes.find((candidate) => candidate.id === workspace?.activePaneId) ?? panes[0];
  if (!workspace || !pane || pane.tabs.length === 0) return null;
  const current = pane.tabs.find((tab) => tab.id === pane.activeTabId) ?? pane.tabs[pane.tabs.length - 1];
  const target = pane.tabs[cycleIndex(pane.tabs.indexOf(current), pane.tabs.length, direction)];
  if (!target) return null;
  state.setActiveTab(target.id);
  return target.id;
}

export function cycleActivePane(direction: 1 | -1): string | null {
  const state = useUi.getState();
  const workspace = workspaceForNavigation(state);
  const panes = workspace?.panes.slice(0, 2) ?? [];
  if (!workspace || panes.length === 0) return null;
  const current = panes.find((pane) => pane.id === workspace.activePaneId) ?? panes[0];
  const target = panes[cycleIndex(panes.indexOf(current), panes.length, direction)];
  if (!target) return null;
  if (state.activeWorkspaceId !== workspace.id) state.setActiveWorkspace(workspace.id);
  state.setActivePane(target.id, workspace.id);
  return target.id;
}

export async function openTerminalTab(
  session: SessionInfo,
  title?: string,
  paneId?: string,
  options?: { directory?: string },
) {
  const wsId = useUi.getState().ensureWorkspace({
    kind: "session",
    sessionId: session.id,
    assetId: session.assetId ?? undefined,
    title: session.name,
    assetKind: session.kind,
  });
  const ws = useUi.getState().workspaces.find((w) => w.id === wsId);
  const n = ws?.panes.flatMap((p) => p.tabs).filter((t) => t.kind === "terminal").length ?? 0;
  useUi.getState().addTab(
    {
      id: nextTabId(`term-${session.id}`),
      kind: "terminal",
      title: title ?? `终端 ${n + 1}`,
      sessionId: session.id,
      pendingDirectory: options?.directory,
      closable: true,
    },
    paneId,
  );
}

async function confirmDirtyEditors(tabs: AppTab[], title: string): Promise<boolean> {
  const dirty = dirtyFileEditors(tabs, useUi.getState().sessions);
  if (dirty.length === 0) return true;
  const { ask } = await import("../ui/dialogs");
  const names = dirty.map((tab) => tab.title).join("、");
  return ask(`有 ${dirty.length} 个文件尚未保存：${names}\n关闭后会丢失这些改动。仍要继续？`, {
    title,
    kind: "warning",
  });
}

function findTab(id: string): { tab: AppTab; ws: Workspace } | null {
  for (const w of useUi.getState().workspaces) {
    for (const p of w.panes) {
      const t = p.tabs.find((x) => x.id === id);
      if (t) return { tab: t, ws: w };
    }
  }
  return null;
}

export function sanitizeRemoteTabTitle(raw: string): string {
  const cleaned = raw
    .replace(/[\x00-\x1f\x7f]/g, " ")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, 80)
    .trim();
  return cleaned;
}

export function applyRemoteTabTitle(id: string, rawTitle: string): boolean {
  const found = findTab(id);
  if (!found) return false;
  if (found.tab.renamedByUser) return false;
  const title = sanitizeRemoteTabTitle(rawTitle);
  if (!title || title === found.tab.title) return false;
  useUi.getState().updateTab(id, { title });
  return true;
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
): "exec" | "winrm" | "local" | "unknown" | null {
  if (t.containerId) return "exec";
  const kind = t.sessionId ? sessions.find((s) => s.id === t.sessionId)?.kind : undefined;
  if (kind === "winrm" || (!kind && assetKind === "winrm")) return "winrm";
  if (t.retainable === true) return null;
  if (kind === "local" || (!kind && assetKind === "local")) return "local";
  return "unknown";
}

function detachBlockLabel(block: "exec" | "winrm" | "local" | "unknown"): string {
  if (block === "exec") return "容器 exec";
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

async function reclaimTerminals(
  tabs: AppTab[],
  scope: string,
  title: string,
  assetKind?: string,
): Promise<boolean> {
  await reconcileTerminalCloseState(tabs.map((tab) => tab.id));
  const currentTabs = tabs
    .map((tab) => findTab(tab.id)?.tab)
    .filter((tab): tab is AppTab => tab !== undefined);
  const st = useUi.getState();
  const live = currentTabs.filter(isRunningTerminal);
  const cleanup = currentTabs.filter(
    (t) => t.kind === "terminal" && t.tabId && !t.dead && t.exited,
  );
  if (live.length === 0 && cleanup.length === 0) return true;
  const blocked = live.filter((t) => detachBlockOf(t, st.sessions, assetKind) !== null);
  const plan: ClosePreferencePlan = {
    detachable: live.filter((t) => detachBlockOf(t, st.sessions, assetKind) === null),
    preferredKills: [],
    confirmBlocked: false,
  };
  if (!(await applyClosePreference(plan, title))) return false;
  if (blocked.length > 0 && plan.confirmBlocked) {
    const lines: string[] = [];
    if (blocked.length > 0 && plan.confirmBlocked) {
      const kinds = [
        ...new Set(
          blocked
            .map((t) => detachBlockOf(t, st.sessions, assetKind))
            .filter((b): b is "exec" | "winrm" | "local" | "unknown" => b !== null),
        ),
      ].map(detachBlockLabel).join("、");
      lines.push(`${scope}里有 ${blocked.length} 个${kinds}终端，不支持转入后台，关闭会结束这些进程，无法恢复。`);
    }
    const { ask } = await import("../ui/dialogs");
    const ok = await ask(`${lines.join("\n")}\n仍要继续？`, { title, kind: "warning" });
    if (!ok) return false;
  }
  const { detachable, preferredKills } = plan;
  const operations: { tab: AppTab; mode?: "kill" | "detach" }[] = [
    ...detachable.map((tab) => ({ tab, mode: "detach" as const })),
    ...preferredKills.map((tab) => ({ tab, mode: "kill" as const })),
    ...blocked.map((tab) => ({ tab, mode: "kill" as const })),
    ...cleanup.map((tab) => ({ tab, mode: undefined })),
  ];
  const results = await Promise.all(
    operations.map(async ({ tab, mode }) => ({ tab, ok: await useUi.getState().closeTab(tab.id, mode, true) })),
  );
  const failed = results.filter((result) => !result.ok);
  const { pushToast } = useUi.getState();
  if (failed.length > 0) {
    pushToast(
      "error",
      `${failed.length}/${live.length + cleanup.length} 个终端关闭失败：${findTab(failed[0].tab.id)?.tab.closeError}`,
    );
    return false;
  }
  const killStart = detachable.length;
  const killEnd = killStart + preferredKills.length + blocked.length;
  const detachedOk = results.filter((r, i) => r.ok && i < killStart).length;
  const killedOk = results.filter((r, i) => r.ok && i >= killStart && i < killEnd).length;
  if (detachedOk) {
    pushToast("info", `${detachedOk} 个终端已转入后台，可在「后台会话」接管`);
  }
  if (killedOk) {
    pushToast("info", `${killedOk} 个终端进程已结束`);
  }
  return failed.length === 0;
}

async function disconnectDbTabs(tabs: AppTab[]): Promise<boolean> {
  const connIds = [
    ...new Set(
      tabs.filter((t) => t.kind === "db" && t.connId).map((t) => t.connId as string),
    ),
  ];
  if (connIds.length === 0) return true;
  const results = await Promise.allSettled(connIds.map((connId) => dbApi.disconnect(connId)));
  const failed = results.filter((r): r is PromiseRejectedResult => r.status === "rejected");
  if (failed.length === 0) return true;
  useUi.getState().pushToast(
    "error",
    `${failed.length}/${connIds.length} 个数据库断开失败：${describeError(failed[0].reason)}`,
  );
  return false;
}

interface ClosePreferencePlan {
  detachable: AppTab[];
  preferredKills: AppTab[];
  confirmBlocked: boolean;
}

interface WorkspaceCloseSummary extends ClosePreferencePlan {
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
  const { askChoiceRemember } = await import("../ui/dialogs");
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

function collectWorkspaceClose(
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
async function confirmWorkspaceClose(
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
  const { ask } = await import("../ui/dialogs");
  return ask(`${lines.join("\n")}\n仍要继续？`, { title: `关闭「${title}」`, kind: "warning" });
}

async function executeWorkspaceClose(summary: WorkspaceCloseSummary): Promise<WorkspaceCloseOutcome> {
  const operations: { tab: AppTab; mode?: "kill" | "detach" }[] = [
    ...summary.detachable.map((tab) => ({ tab, mode: "detach" as const })),
    ...summary.preferredKills.map((tab) => ({ tab, mode: "kill" as const })),
    ...summary.blocked.map((tab) => ({ tab, mode: "kill" as const })),
    ...summary.cleanup.map((tab) => ({ tab, mode: undefined })),
  ];
  const results = await Promise.all(
    operations.map(async ({ tab, mode }) => ({ tab, ok: await useUi.getState().closeTab(tab.id, mode, true) })),
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
    killed: results.filter((r, i) => r.ok && i >= killStart && i < killEnd).length,
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
function pushWorkspaceCloseToast(title: string, outcome: WorkspaceCloseOutcome, deleted: boolean): void {
  const { pushToast } = useUi.getState();
  const parts: string[] = [];
  if (outcome.detached > 0) parts.push(`${outcome.detached} 个终端转入后台，可在「后台会话」接管`);
  if (outcome.killed > 0) parts.push(`${outcome.killed} 个进程已结束`);
  if (outcome.failed > 0) {
    pushToast("error", `关闭「${title}」：${outcome.failed} 个终端操作失败：${outcome.failureText}`);
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
  if (block === "unknown") return "结束进程";
  const action = getTerminalPrefs().closeAction;
  if (action === "kill") return "结束进程";
  if (action === "detach") return "转入后台运行";
  return "转入后台运行或结束进程";
}

export function sessionReconnectStatusText(
  session: Pick<SessionRuntimeInfo, "status" | "attempt" | "nextRetryAt"> | undefined,
): string {
  if (session?.status !== "reconnecting") return "";
  const attempt = Math.max(0, Math.floor(session.attempt ?? 0));
  const retryAt = typeof session.nextRetryAt === "number"
    ? session.nextRetryAt
    : session.nextRetryAt
      ? Date.parse(session.nextRetryAt)
      : null;
  const next = retryAt !== null && Number.isFinite(retryAt) && retryAt > 0
    ? `下次重连 ${formatTime(retryAt)}`
    : null;
  return [attempt > 0 ? `重连尝试 ${attempt} 次` : "准备第 1 次重连", next]
    .filter(Boolean)
    .join(" · ");
}

const retryableSessionErrorCodes = new Set(["ssh", "timeout", "disconnected"]);

export function applySessionStatusEvent(p: SessionStatusEvent): void {
  const state = useUi.getState();
  const current = state.sessions.find((session) => session.id === p.sessionId);
  const attempt = p.attempt ?? current?.attempt ?? 0;
  const retryableFailed = p.status === "failed" &&
    (current?.status === "reconnecting" || attempt > 0) &&
    retryableSessionErrorCodes.has(p.errorCode ?? "");
  state.setSessions(state.sessions.map((session) => session.id === p.sessionId
    ? {
        ...session,
        status: p.status as SessionInfo["status"],
        error: p.error ?? null,
        errorCode: p.errorCode ?? null,
        attempt: p.attempt ?? 0,
        nextRetryAt: p.nextRetryAt ?? null,
      }
    : session));
  if (p.status !== "connecting" && p.status !== "reconnecting" && current?.assetId) {
    state.setConnectProgress(current.assetId, null);
  }
  if (p.status === "connecting" || p.status === "reconnecting" || p.status === "connected") {
    abnormalDisconnectNotices.delete(p.sessionId);
  }
  if (p.status === "failed" && !retryableFailed) {
    if (p.error) state.pushToast("error", `会话失败：${p.error}`);
    return;
  }
  if (p.status !== "disconnected" && !retryableFailed) return;
  if (!p.error && !p.errorCode) {
    abnormalDisconnectNotices.delete(p.sessionId);
    return;
  }
  if (abnormalDisconnectNotices.has(p.sessionId)) return;
  abnormalDisconnectNotices.add(p.sessionId);
  const name = current?.name ?? p.sessionId;
  const message = retryableFailed
    ? `会话「${name}」自动重连失败：${p.error ?? p.errorCode}`
    : `会话「${name}」异常断开：${p.error ?? p.errorCode}`;
  state.pushToast("error", message, {
    label: "重新连接",
    onSelect: () => void reconnectSessionAfterDisconnect(p.sessionId),
  });
}

async function reconnectSessionAfterDisconnect(sessionId: string): Promise<void> {
  const state = useUi.getState();
  const session = state.sessions.find((item) => item.id === sessionId);
  if (!session) {
    state.pushToast("error", "重连失败：会话已不存在，请刷新会话列表");
    return;
  }
  try {
    const fresh = await reconnectSessionAndWait(session);
    if (!fresh) return;
    const latest = useUi.getState();
    latest.setSessions([...latest.sessions.filter((item) => item.id !== fresh.id), fresh]);
  } catch (error) {
    useUi.getState().pushToast("error", `重连失败：${describeError(error)}`);
  }
}

export function sessionStatusText(status: SessionInfo["status"] | undefined): string {
  if (status === "connected") return "已连接";
  if (status === "reconnecting") return "重连中";
  if (status === "connecting") return "连接中";
  if (status === "failed") return "连接失败";
  return "已断开";
}

export function connectStageText(stage: string): string {
  if (stage === "auth") return "准备认证";
  if (stage === "dial") return "TCP 拨号";
  if (stage === "channel") return "经跳板打开通道";
  if (stage === "handshake") return "SSH 握手认证";
  return stage;
}

export function connectProgressText(progress: ConnectProgressEvent): string {
  const hop =
    progress.hop > 0 ? `跳板 ${progress.hop}/${progress.hops}` : "目标主机";
  return `${hop} · ${connectStageText(progress.stage)}`;
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
    if (decided !== undefined || action !== "kill") {
      const { ask } = await import("../ui/dialogs");
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
      const { askChoiceRemember } = await import("../ui/dialogs");
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

async function reconcileTerminalCloseState(ids: string[]): Promise<void> {
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
      const { askChoiceRemember } = await import("../ui/dialogs");
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
  const { ask } = await import("../ui/dialogs");
  const ok = await ask(`结束「${target.title}」里的进程？\n\n进程会被终止，无法恢复。`, {
    title: "结束进程",
    kind: "warning",
  });
  if (!ok) return;
  if (await st.closeTab(id, "kill")) {
    st.pushToast("success", `已结束「${target.title}」的进程`);
  }
}

export async function requestKillWorkspaceTerminals(workspaceId: string): Promise<void> {
  const st = useUi.getState();
  const ws = st.workspaces.find((w) => w.id === workspaceId);
  if (!ws) return;
  const live = ws.panes.flatMap((p) => p.tabs).filter(isRunningTerminal);
  if (live.length === 0) {
    st.pushToast("info", "这个工作区里没有正在运行的终端");
    return;
  }
  const { ask } = await import("../ui/dialogs");
  const ok = await ask(`结束「${ws.title}」里 ${live.length} 个终端的进程？\n\n进程会被终止，无法恢复。`, {
    title: "结束全部终端进程",
    kind: "warning",
  });
  if (!ok) return;
  const results = await Promise.allSettled(
    live.map((tab) => {
      const kind = st.sessions.find((session) => session.id === tab.sessionId)?.kind ?? ws.assetKind;
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

export function takePendingDirectory(storeTabId: string): string | null {
  const st = useUi.getState();
  for (const w of st.workspaces) {
    for (const p of w.panes) {
      const tab = p.tabs.find((t) => t.id === storeTabId);
      if (tab?.pendingDirectory) {
        const directory = tab.pendingDirectory;
        st.updateTab(storeTabId, { pendingDirectory: undefined });
        return directory;
      }
    }
  }
  return null;
}

export function findWritableTerminal(
  sessionId: string,
): { tabId: string; shell: "powershell" | "cmd" | "posix" | "unknown" } | null {
  const st = useUi.getState();
  for (const w of st.workspaces) {
    if (w.sessionId !== sessionId) continue;
    for (const p of w.panes) {
      const ordered = [
        ...p.tabs.filter((t) => t.id === p.activeTabId),
        ...p.tabs.filter((t) => t.id !== p.activeTabId),
      ];
      for (const t of ordered) {
        if (t.kind === "terminal" && t.tabId && !t.dead && !t.exited) {
          return { tabId: t.tabId, shell: t.shell ?? "unknown" };
        }
      }
    }
  }
  return null;
}

export function openFileTab(sessionId: string, path: string) {
  useUi.getState().addTab(fileTabSpec(sessionId, path, baseName(path)));
}

export function openFileTabInSplit(sessionId: string, path: string) {
  useUi.getState().openInLowerPane(fileTabSpec(sessionId, path, baseName(path)));
}

export function openLogTab(sessionId: string, path: string) {
  useUi.getState().addTab({
    id: `log-${sessionId}-${path}`,
    kind: "log",
    title: baseName(path),
    sessionId,
    path,
    closable: true,
  });
}

function fileTabSpec(sessionId: string, path: string, name: string): AppTab {
  return {
    id: nextTabId("file"),
    kind: "files",
    title: name,
    sessionId,
    path,
    closable: true,
  };
}

export function openCredentialsSidebar() {
  const st = useUi.getState();
  st.setLeftMode("credentials");
  st.setLeftOpen(true);
}

export function openCredentialsTab(credId?: string) {
  useUi.getState().addTab({
    id: "tab-credentials",
    kind: "credentials",
    title: "凭据",
    ...(credId !== undefined ? { credId } : {}),
    closable: true,
  });
}

export function useCredentialsTabId(): string | undefined {
  return useUi((s) => {
    for (const w of s.workspaces) {
      for (const p of w.panes) {
        const t = p.tabs.find((x) => x.id === "tab-credentials");
        if (t) return t.credId;
      }
    }
    return undefined;
  });
}

async function ensureVaultUnlocked(reason: string, epoch?: number): Promise<boolean> {
  let st: VaultStatus;
  try {
    st = await vaultApi.status();
  } catch {
    return epoch === undefined || currentConnectionEpoch(epoch);
  }
  if (epoch !== undefined && !currentConnectionEpoch(epoch)) return false;
  if (!st.initialized || st.unlocked) return true;
  if (st.passwordless) {
    try {
      await vaultApi.unlock("");
      if (epoch !== undefined && !currentConnectionEpoch(epoch)) return false;
      return true;
    } catch (e) {
      if (epoch === undefined || currentConnectionEpoch(epoch)) useUi.getState().pushToast("error", `解锁失败：${describeError(e)}`);
      return false;
    }
  }
  const { promptText } = await import("../ui/dialogs");
  if (epoch !== undefined && !currentConnectionEpoch(epoch)) return false;
  const pwd = await promptText(reason, "", { secret: true });
  if (pwd === null || (epoch !== undefined && !currentConnectionEpoch(epoch))) return false;
  try {
    await vaultApi.unlock(pwd);
    if (epoch !== undefined && !currentConnectionEpoch(epoch)) return false;
    return true;
  } catch (e) {
    if (epoch === undefined || currentConnectionEpoch(epoch)) useUi.getState().pushToast("error", `解锁失败：${describeError(e)}`);
    return false;
  }
}

async function ensureVaultReadyFor(asset: { name: string; credId?: string | null }, epoch?: number): Promise<boolean> {
  if (!asset.credId) return true;
  return ensureVaultUnlocked(`连接「${asset.name}」需要使用凭据，请先输入保护密码：`, epoch);
}

function sidebarsOverlayWorkspace(): boolean {
  if (typeof window === "undefined") return false;
  return workspaceViewport(window.innerWidth, false).overlaySidebars;
}

async function openConnectedAssetSession(
  info: SessionInfo,
  asset: { id: string; name: string; kind: string },
  options?: ConnectAssetOptions,
  quickTarget?: QuickWorkspaceTarget,
): Promise<void> {
  const { setSessions, sessions, workspaces } = useUi.getState();
  setSessions([...sessions.filter((s) => s.id !== info.id), info]);
  const restored = asset.id && (asset.kind === "ssh" || asset.kind === "device")
    ? workspaces.find((workspace) => workspace.kind === "session" && workspace.assetId === asset.id)
    : undefined;
  if (restored) {
    useUi.setState((state) => {
      const rebound = rebindWorkspaceSession(state.workspaces, restored.id, restored.sessionId, info.id);
      if (options?.preserveUserFocus) return { workspaces: rebound };
      return {
        activeWorkspaceId: restored.id,
        workspaces: rebound.map((workspace) => {
          if (workspace.id !== restored.id || asset.kind === "device" || workspace.panes.some((pane) => pane.tabs.some((tab) => tab.kind === "terminal"))) return workspace;
          const panes = workspace.panes.length > 0 ? workspace.panes : [makePane()];
          const activePane = panes.find((pane) => pane.id === workspace.activePaneId) ?? panes[0];
          const tab: AppTab = {
            id: nextTabId(`term-${info.id}`), kind: "terminal", title: "终端 1", sessionId: info.id, closable: true,
          };
          return {
            ...workspace,
            activePaneId: activePane.id,
            panes: panes.map((pane) => pane.id === activePane.id
              ? { ...pane, tabs: [...pane.tabs, tab], activeTabId: tab.id }
              : pane),
          };
        }),
      };
    });
    return;
  }
  if (options?.preserveUserFocus && workspaces.length > 0) return;
  const pane = makePane();
  if (asset.kind !== "device") {
    const tab: AppTab = asset.kind === "docker"
      ? { id: nextTabId(`docker-${info.id}`), kind: "docker", title: "容器", sessionId: info.id, closable: true }
      : { id: nextTabId(`term-${info.id}`), kind: "terminal", title: "终端 1", sessionId: info.id, closable: true };
    pane.tabs = [tab];
    pane.activeTabId = tab.id;
  }
  const workspace: Workspace = {
    id: nextTabId("ws-session"), kind: "session", sessionId: info.id,
    title: info.name, assetId: asset.id, assetKind: asset.kind, quickTarget,
    panes: [pane], activePaneId: pane.id, splitRatio: 0.5, closable: true,
  };
  const overlaySidebars = sidebarsOverlayWorkspace();
  useUi.setState((state) => ({
    workspaces: [...state.workspaces, workspace],
    activeWorkspaceId: workspace.id,
    leftMode: overlaySidebars ? state.leftMode : "files",
    leftOpen: overlaySidebars ? state.leftOpen : true,
    connectFocusRevision: state.connectFocusRevision + 1,
  }));
}

export interface ConnectAssetInput {
  id: string;
  name: string;
  kind: string;
  credId?: string | null;
}

export interface ConnectAssetOptions {
  silent?: boolean;
  preserveUserFocus?: boolean;
}

export type ConnectOutcome =
  | { ok: true }
  | { ok: false; error?: string; canceled?: boolean };

const inflightConnects = new Map<string, Promise<ConnectOutcome>>();
const connectingAssetCounts = new Map<string, number>();
const restoreWorkspaceIds = new WeakMap<Workspace, number>();
let restoreWorkspaceIdSeq = 0;

function currentConnectionEpoch(epoch: number): boolean {
  return useUi.getState().workspaceRuntimeEpoch === epoch;
}

async function discardStaleSession(session: SessionInfo): Promise<ConnectOutcome> {
  await sessionApi.disconnect(session.id).catch(() => undefined);
  return { ok: false, canceled: true };
}

async function discardStaleDatabase(connId: string): Promise<ConnectOutcome> {
  await dbApi.disconnect(connId).catch(() => undefined);
  return { ok: false, canceled: true };
}

function pendingWorkspaceKey(assetId: string): number | null {
  const workspace = useUi.getState().workspaces.find((item) => item.assetId === assetId && item.restorePending);
  if (!workspace) return null;
  let id = restoreWorkspaceIds.get(workspace);
  if (id === undefined) {
    id = ++restoreWorkspaceIdSeq;
    restoreWorkspaceIds.set(workspace, id);
  }
  return id;
}

function inflightAssetKey(assetId: string, epoch: number, options?: ConnectAssetOptions): string {
  return JSON.stringify([
    assetId,
    epoch,
    pendingWorkspaceKey(assetId),
    options?.silent === true,
    options?.preserveUserFocus === true,
  ]);
}

function setAssetConnecting(assetId: string, connecting: boolean, epoch: number): void {
  const key = JSON.stringify([epoch, assetId]);
  const current = connectingAssetCounts.get(key) ?? 0;
  const next = connecting ? current + 1 : Math.max(0, current - 1);
  if (next === 0) connectingAssetCounts.delete(key);
  else connectingAssetCounts.set(key, next);
  if (!currentConnectionEpoch(epoch)) return;
  useUi.setState((s) => ({
    connectingAssetIds:
      next > 0
        ? s.connectingAssetIds.includes(assetId)
          ? s.connectingAssetIds
          : [...s.connectingAssetIds, assetId]
        : s.connectingAssetIds.filter((id) => id !== assetId),
  }));
}

export function isAssetConnecting(assetId: string): boolean {
  return useUi.getState().connectingAssetIds.includes(assetId);
}

export function assetConnectionProblemKey(assetId: string): string {
  return `asset:${assetId}`;
}

export function quickConnectionProblemKey(target: QuickConnectTarget): string {
  const normalized = {
    host: target.host.trim().toLowerCase(),
    port: target.port,
    username: target.username?.trim() || null,
  };
  return `quick:${formatQuickConnectTarget(normalized)}`;
}

function findConnectionWorkspace(
  workspaces: Workspace[],
  problem: ConnectionProblemInput,
): Workspace | undefined {
  if (problem.workspaceId) {
    const explicit = workspaces.find((workspace) => workspace.id === problem.workspaceId);
    if (explicit) return explicit;
  }
  const connection = problem.connection;
  if (connection?.kind === "asset") {
    return workspaces.find((workspace) => workspace.assetId === connection.asset.id);
  }
  if (connection?.kind === "quick") {
    const key = quickConnectionProblemKey(connection.target);
    return workspaces.find(
      (workspace) => workspace.quickTarget && quickConnectionProblemKey(workspace.quickTarget) === key,
    );
  }
  return undefined;
}

type ConnectionProblemState = Pick<UiState, "connectionProblems" | "workspaceRuntime">;

export function selectConnectionProblem(
  state: ConnectionProblemState,
  connectionKey: string,
): WorkspaceProblem | undefined {
  const global = state.connectionProblems[connectionKey];
  if (global) return global;
  for (const runtime of Object.values(state.workspaceRuntime)) {
    const problem = runtime.problems?.find((item) => item.connectionKey === connectionKey);
    if (problem) return problem;
  }
  return undefined;
}

export function latestConnectionProblem(state: ConnectionProblemState): WorkspaceProblem | undefined {
  let latest: WorkspaceProblem | undefined;
  const consider = (problem: WorkspaceProblem) => {
    if (problem.connectionKey && (!latest || problem.id > latest.id)) latest = problem;
  };
  Object.values(state.connectionProblems).forEach(consider);
  for (const runtime of Object.values(state.workspaceRuntime)) {
    runtime.problems?.forEach(consider);
  }
  return latest;
}

export function useConnectionProblem(
  connectionKey: string | null,
  fallbackToLatest = false,
): WorkspaceProblem | undefined {
  return useUi((state) => {
    if (connectionKey) {
      const problem = selectConnectionProblem(state, connectionKey);
      if (problem) return problem;
    }
    return fallbackToLatest ? latestConnectionProblem(state) : undefined;
  });
}

interface ConnectionAttemptContext {
  resetGeneration: number;
}

function captureConnectionAttempt(): ConnectionAttemptContext {
  return { resetGeneration: workspaceProblemResetGeneration };
}

function settleConnectionOutcome(
  context: ConnectionAttemptContext,
  outcome: ConnectOutcome,
  operation: string,
  target: WorkspaceProblemTarget,
  connection: WorkspaceProblemConnection,
  workspaceId?: string,
): void {
  if (context.resetGeneration !== workspaceProblemResetGeneration) return;
  const connectionKey = connection.kind === "asset"
    ? assetConnectionProblemKey(connection.asset.id)
    : quickConnectionProblemKey(connection.target);
  if (outcome.ok) {
    useUi.getState().clearConnectionProblem(connectionKey);
    return;
  }
  if (outcome.canceled) return;
  const error = outcome.error ?? "连接失败";
  useUi.getState().recordConnectionProblem({
    connectionKey,
    workspaceId,
    resetGeneration: context.resetGeneration,
    createdAt: Date.now(),
    source: "connect",
    operation,
    target,
    summary: error,
    detail: error,
    canRetryRead: false,
    connection,
  });
}

function settleAssetConnectionOutcome(
  context: ConnectionAttemptContext,
  asset: ConnectAssetInput,
  outcome: ConnectOutcome,
  workspaceId?: string,
): void {
  const addressed = asset as ConnectAssetInput & { host?: unknown; port?: unknown };
  const host = typeof addressed.host === "string" ? addressed.host : "";
  const port = typeof addressed.port === "number" ? addressed.port : undefined;
  settleConnectionOutcome(
    context,
    outcome,
    "连接",
    {
      title: asset.name,
      identity: host ? `${asset.name} · ${host}${port ? `:${port}` : ""}` : asset.name,
      kind: asset.kind,
      assetId: asset.id,
    },
    { kind: "asset", asset: { ...asset } },
    workspaceId,
  );
}

function settleQuickConnectionOutcome(
  context: ConnectionAttemptContext,
  target: QuickConnectTarget,
  outcome: ConnectOutcome,
  mode: "temporary" | "save" = "temporary",
  workspaceId?: string,
): void {
  const label = formatQuickConnectTarget(target);
  settleConnectionOutcome(
    context,
    outcome,
    mode === "save" ? "保存并连接" : "临时连接",
    { title: label, identity: label, kind: "ssh" },
    { kind: "quick", mode, target: { ...target }, label },
    workspaceId,
  );
}

function settleWorkspaceConnectionOutcome(
  context: ConnectionAttemptContext,
  workspace: Pick<Workspace, "assetId" | "assetKind" | "title" | "quickTarget">,
  outcome: ConnectOutcome,
  workspaceId?: string,
): void {
  if (workspace.assetId) {
    settleAssetConnectionOutcome(
      context,
      { id: workspace.assetId, name: workspace.title, kind: workspace.assetKind ?? "ssh" },
      outcome,
      workspaceId,
    );
  } else if (workspace.quickTarget) {
    settleQuickConnectionOutcome(context, workspace.quickTarget, outcome, "temporary", workspaceId);
  }
}

export async function connectAsset(
  asset: ConnectAssetInput,
  options?: ConnectAssetOptions,
): Promise<ConnectOutcome> {
  const epoch = useUi.getState().workspaceRuntimeEpoch;
  const key = inflightAssetKey(asset.id, epoch, options);
  const inflight = inflightConnects.get(key);
  if (inflight) return inflight;
  const context = captureConnectionAttempt();
  const promise = runConnectAsset(asset, options, epoch).finally(() => {
    if (inflightConnects.get(key) === promise) inflightConnects.delete(key);
    setAssetConnecting(asset.id, false, epoch);
    if (currentConnectionEpoch(epoch) && !isAssetConnecting(asset.id)) useUi.getState().setConnectProgress(asset.id, null);
  });
  inflightConnects.set(key, promise);
  setAssetConnecting(asset.id, true, epoch);
  const outcome = await promise;
  if (currentConnectionEpoch(epoch)) settleAssetConnectionOutcome(context, asset, outcome);
  return outcome;
}

const LIVE_SESSION_STATUSES = new Set(["connecting", "connected", "reconnecting"]);

async function runConnectAsset(
  asset: ConnectAssetInput,
  options?: ConnectAssetOptions,
  epoch = useUi.getState().workspaceRuntimeEpoch,
): Promise<ConnectOutcome> {
  if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
  const { pushToast } = useUi.getState();

  const existing = useUi.getState().workspaces.find((w) => w.assetId === asset.id);
  if (existing?.restorePending) return reconnectRestoredWorkspace(existing.id);
  const restoreDatabaseWorkspace =
    existing?.kind === "db" &&
    !existing.panes.some((pane) => pane.tabs.some((tab) => tab.kind === "db"));
  if (existing) {
    const session = useUi.getState().sessions.find((s) => s.id === existing.sessionId);
    const live =
      !restoreDatabaseWorkspace &&
      (existing.kind === "db" ||
        (session !== undefined && LIVE_SESSION_STATUSES.has(session.status)));
    if (live) {
      const hasTerminal = existing.panes.some((p) => p.tabs.some((t) => t.kind === "terminal"));
      if (session && !hasTerminal) {
        void openTerminalTab(session);
      } else {
        useUi.getState().setActiveWorkspace(existing.id);
      }
      return { ok: true };
    }
    if (session) {
      if (!(await ensureVaultReadyFor(asset, epoch)) || !currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
      try {
        const fresh = await reconnectSessionAndWait(session);
        if (!currentConnectionEpoch(epoch)) return fresh ? discardStaleSession(fresh) : { ok: false, canceled: true };
        if (!fresh) {
          pushToast("info", "已取消连接");
          return { ok: false, canceled: true };
        }
        const { sessions, setSessions } = useUi.getState();
        setSessions([...sessions.filter((s) => s.id !== fresh.id), fresh]);
        useUi.getState().setActiveWorkspace(existing.id);
        useConnectHistory.getState().record(asset.id);
        return { ok: true };
      } catch (error) {
        if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
        if ((error as { code?: string } | null)?.code !== "not_found") {
          const message = describeError(error);
          pushToast("error", `连接失败：${message}`);
          return { ok: false, error: message };
        }
      }
    }
    if (!restoreDatabaseWorkspace && asset.kind !== "ssh" && asset.kind !== "device") {
      await useUi.getState().closeWorkspace(existing.id);
      if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
      if (useUi.getState().workspaces.some((w) => w.id === existing.id)) {
        return { ok: false, canceled: true };
      }
    }
  }

  const dbKind = dbKindOf(asset.kind);
  if (dbKind) {
    try {
      if (!(await ensureVaultReadyFor(asset, epoch)) || !currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
      const { connId } = await dbApi.connect(asset.id);
      if (!currentConnectionEpoch(epoch)) return discardStaleDatabase(connId);
      const title = `${asset.name} · ${DB_KIND_LABEL[dbKind]}`;
      const tab: AppTab = {
        id: nextTabId(`db-${connId}`),
        kind: "db",
        title: DB_KIND_LABEL[dbKind],
        connId,
        dbKind,
        closable: true,
      };
      if (restoreDatabaseWorkspace && existing) {
        useUi.setState((state) => {
          const runtime = state.workspaceRuntime[existing.id];
          return {
            activeWorkspaceId: existing.id,
            workspaces: state.workspaces.map((workspace) =>
              workspace.id === existing.id ? { ...workspace, connId, dbKind, title } : workspace,
            ),
            workspaceRuntime: runtime?.sqlDraft
              ? {
                  ...state.workspaceRuntime,
                  [existing.id]: {
                    ...runtime,
                    sqlDraft: { ...runtime.sqlDraft, connId },
                  },
                }
              : state.workspaceRuntime,
          };
        });
        useUi.getState().addTab(tab, existing.activePaneId);
      } else {
        const { addTab, ensureWorkspace } = useUi.getState();
        ensureWorkspace({ kind: "db", connId, dbKind, title, assetId: asset.id });
        addTab(tab);
      }
      useConnectHistory.getState().record(asset.id);
      return { ok: true };
    } catch (e) {
      if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
      if (!options?.silent) pushToast("error", describeError(e));
      return { ok: false, error: describeError(e) };
    }
  }

  try {
    if (!(await ensureVaultReadyFor(asset, epoch)) || !currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
    const info = await connectWithHostKeyConfirm(() => sessionApi.connect(asset.id), () => currentConnectionEpoch(epoch));
    if (!currentConnectionEpoch(epoch)) return info ? discardStaleSession(info) : { ok: false, canceled: true };
    if (!info) {
      pushToast("info", "已取消连接");
      return { ok: false, canceled: true };
    }
    await openConnectedAssetSession(info, asset, options);
    useConnectHistory.getState().record(asset.id);
    return { ok: true };
  } catch (e) {
    if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
    const err = e as { message?: string };
    const message = err.message || describeError(e);
    if (!options?.silent) pushToast("error", message);
    return { ok: false, error: message };
  }
}

// deviceAssetId is the pseudo asset id of a device session in the session system (matching the
// asset.ID of the backend session_connect_device); devices are not asset table records, so credential/host-key flows never apply.
export function deviceAssetId(deviceId: string): string {
  return `device/${deviceId}`;
}

// connectDevice opens the full workspace of a fleet device: the session is established via session_connect_device
// (the server enforces owner/revocation/terminal-switch authorization). Opening the workspace preserves its panes;
// a shell is created only when requested through the terminal action. Workspace dedup/reconnect mirrors connectAsset.
export async function connectDevice(
  device: { id: string; name: string },
  options?: ConnectAssetOptions,
): Promise<ConnectOutcome> {
  const epoch = useUi.getState().workspaceRuntimeEpoch;
  const assetId = deviceAssetId(device.id);
  const key = inflightAssetKey(assetId, epoch, options);
  const inflight = inflightConnects.get(key);
  if (inflight) return inflight;
  const context = captureConnectionAttempt();
  const promise = runConnectDevice(device, options, epoch).finally(() => {
    if (inflightConnects.get(key) === promise) inflightConnects.delete(key);
    setAssetConnecting(assetId, false, epoch);
  });
  inflightConnects.set(key, promise);
  setAssetConnecting(assetId, true, epoch);
  const outcome = await promise;
  if (currentConnectionEpoch(epoch)) {
    settleAssetConnectionOutcome(
      context,
      { id: assetId, name: device.name, kind: "device" },
      outcome,
    );
  }
  return outcome;
}

function activateDeviceWorkspace(workspaceId: string, options?: ConnectAssetOptions): void {
  if (options?.preserveUserFocus) return;
  const overlaySidebars = sidebarsOverlayWorkspace();
  useUi.setState((state) => ({
    activeWorkspaceId: workspaceId,
    leftMode: overlaySidebars ? state.leftMode : "files",
    leftOpen: overlaySidebars ? state.leftOpen : true,
    connectFocusRevision: state.connectFocusRevision + 1,
  }));
}

async function runConnectDevice(
  device: { id: string; name: string },
  options?: ConnectAssetOptions,
  epoch = useUi.getState().workspaceRuntimeEpoch,
): Promise<ConnectOutcome> {
  if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
  const { pushToast } = useUi.getState();
  const assetId = deviceAssetId(device.id);

  const existing = useUi.getState().workspaces.find((w) => w.assetId === assetId);
  if (existing?.restorePending) {
    const outcome = await reconnectRestoredWorkspace(existing.id);
    if (outcome.ok && currentConnectionEpoch(epoch)) activateDeviceWorkspace(existing.id, options);
    return outcome;
  }
  if (existing) {
    const session = useUi.getState().sessions.find((s) => s.id === existing.sessionId);
    const live = session !== undefined && LIVE_SESSION_STATUSES.has(session.status);
    if (live) {
      activateDeviceWorkspace(existing.id, options);
      return { ok: true };
    }
    if (session) {
      try {
        const fresh = await reconnectSessionAndWait(session);
        if (!currentConnectionEpoch(epoch)) return fresh ? discardStaleSession(fresh) : { ok: false, canceled: true };
        if (!fresh) {
          pushToast("info", "已取消连接");
          return { ok: false, canceled: true };
        }
        const { sessions, setSessions } = useUi.getState();
        setSessions([...sessions.filter((s) => s.id !== fresh.id), fresh]);
        activateDeviceWorkspace(existing.id, options);
        return { ok: true };
      } catch (error) {
        if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
        if ((error as { code?: string } | null)?.code !== "not_found") {
          const message = describeError(error);
          pushToast("error", `连接失败：${message}`);
          return { ok: false, error: message };
        }
      }
    }
  }

  try {
    const info = await sessionApi.connectDevice(device.id, device.name);
    if (!currentConnectionEpoch(epoch)) return discardStaleSession(info);
    await openConnectedAssetSession(info, { id: assetId, name: device.name, kind: "device" }, options);
    if (existing) activateDeviceWorkspace(existing.id, options);
    return { ok: true };
  } catch (e) {
    if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
    const err = e as { message?: string };
    const message = err.message || describeError(e);
    if (!options?.silent) pushToast("error", message);
    return { ok: false, error: message };
  }
}

// reconnectDeadHost establishes a fresh session for a workspace whose session was deleted on the server (the
// dead-session reconnect of the file panels and the terminal dead-tab overlay). A device workspace's assetId is the
// pseudo device/<deviceId> with no asset table record, so it reconnects through the same fleet device connect as the
// original open. Quick connections retain their non-secret target and request fresh credentials.
export async function reconnectDeadHost(
  ws: Pick<Workspace, "assetId" | "assetKind" | "title" | "quickTarget" | "deletedTarget">,
  options?: { isCurrent?: () => boolean },
): Promise<SessionInfo | null> {
  const epoch = useUi.getState().workspaceRuntimeEpoch;
  const context = captureConnectionAttempt();
  const isCurrent = () => currentConnectionEpoch(epoch) && (options?.isCurrent?.() ?? true);
  const finish = async (pending: Promise<SessionInfo | null>): Promise<SessionInfo | null> => {
    try {
      const session = await pending;
      if (isCurrent()) {
        if (session) settleWorkspaceConnectionOutcome(context, ws, { ok: true });
        return session;
      }
      if (session) await discardStaleSession(session);
      return null;
    } catch (error) {
      if (!isCurrent()) return null;
      settleWorkspaceConnectionOutcome(context, ws, { ok: false, error: describeError(error) });
      throw error;
    }
  };
  if (!isCurrent()) return null;
  if (ws.deletedTarget) throw new Error(`「${ws.title}」的资产已删除，请选择其他资产或关闭工作区`);
  if (ws.assetKind === "device" && ws.assetId) {
    return finish(sessionApi.connectDevice(ws.assetId.slice("device/".length), ws.title));
  }
  if (ws.assetId) return finish(connectWithHostKeyConfirm(() => sessionApi.connect(ws.assetId as string), isCurrent));
  if (ws.quickTarget) {
    const target = ws.quickTarget;
    const password = target.authKind === "password"
      ? await promptQuickConnectPassword(formatQuickConnectTarget(target))
      : "";
    if (!isCurrent()) return null;
    if (password === null) return null;
    if (target.authKind === "password" && !password) throw new Error("此快速连接需要重新输入密码");
    return finish(connectWithHostKeyConfirm(() => sessionApi.connectQuick({
      host: target.host,
      port: target.port,
      ...(target.username ? { username: target.username } : {}),
      authKind: target.authKind,
      ...(password ? { password } : {}),
    }), isCurrent));
  }
  if (ws.assetKind === "local") return finish(sessionApi.connectLocal());
  throw new Error(`「${ws.title}」缺少可恢复的主机目标，请从资产树重新连接`);
}

const inflightRestoreConnects = new Map<string, {
  workspace: Workspace;
  epoch: number;
  promise: Promise<ConnectOutcome>;
}>();

export interface RestoreWorkspaceOptions {
  preserveUserFocus?: boolean;
}

export function workspaceSessionIsDead(
  workspace: Pick<Workspace, "kind" | "sessionId">,
  sessions: SessionRuntimeInfo[],
): boolean {
  if (workspace.kind !== "session" || !workspace.sessionId) return false;
  const session = sessions.find((item) => item.id === workspace.sessionId);
  return !session || session.status === "disconnected" || session.status === "failed";
}

export function recordWorkspaceRestoreFailure(workspaceId: string, error: unknown): void {
  const workspace = useUi.getState().workspaces.find((item) => item.id === workspaceId);
  if (!workspace) return;
  settleWorkspaceConnectionOutcome(
    captureConnectionAttempt(),
    workspace,
    { ok: false, error: describeError(error) },
    workspaceId,
  );
}

export function reconnectRestoredWorkspace(
  workspaceId: string,
  options?: RestoreWorkspaceOptions,
): Promise<ConnectOutcome> {
  const state = useUi.getState();
  const epoch = state.workspaceRuntimeEpoch;
  const workspace = state.workspaces.find((item) => item.id === workspaceId);
  const inflight = inflightRestoreConnects.get(workspaceId);
  if (inflight && workspace === inflight.workspace && epoch === inflight.epoch) return inflight.promise;
  const context = captureConnectionAttempt();
  const attempt = connectRestoredWorkspace(workspaceId, options);
  const tracked = attempt.then((outcome) => {
    if (workspace && currentConnectionEpoch(epoch)) {
      settleWorkspaceConnectionOutcome(context, workspace, outcome, workspaceId);
    }
    return outcome;
  }).finally(() => {
    if (inflightRestoreConnects.get(workspaceId)?.promise === tracked) inflightRestoreConnects.delete(workspaceId);
  });
  if (workspace) inflightRestoreConnects.set(workspaceId, { workspace, epoch, promise: tracked });
  return tracked;
}

async function connectRestoredWorkspace(
  workspaceId: string,
  options?: RestoreWorkspaceOptions,
): Promise<ConnectOutcome> {
  const state = useUi.getState();
  const workspace = state.workspaces.find((item) => item.id === workspaceId);
  if (!workspace) return { ok: false, error: "工作区无需重新连接" };
  if (!workspace.restorePending) return reconnectDeadRestoredWorkspace(workspace, options);
  if (workspace.deletedTarget) return { ok: false, error: "原资产已删除，请从资产树选择其他目标" };
  const epoch = state.workspaceRuntimeEpoch;
  const stillPending = () => {
    const current = useUi.getState();
    return current.workspaceRuntimeEpoch === epoch &&
      current.workspaces.some((item) => item === workspace && item.restorePending &&
        item.assetId === workspace.assetId && item.quickTarget?.id === workspace.quickTarget?.id);
  };

  try {
    if (workspace.kind === "db") {
      if (!workspace.assetId) return { ok: false, error: "数据库目标已丢失，请从资产树重新连接" };
      const asset = await assetApi.get(workspace.assetId);
      if (!stillPending()) return { ok: false, canceled: true };
      if (!(await ensureVaultReadyFor(asset, epoch))) return { ok: false, canceled: true };
      if (!stillPending()) return { ok: false, canceled: true };
      const { connId } = await dbApi.connect(workspace.assetId);
      if (!stillPending()) {
        await dbApi.disconnect(connId).catch(() => undefined);
        return { ok: false, canceled: true };
      }
      useUi.setState((current) => ({
        ...(options?.preserveUserFocus ? {} : { activeWorkspaceId: workspaceId }),
        workspaces: current.workspaces.map((item) => item.id === workspaceId ? {
          ...item,
          connId,
          restorePending: undefined,
          panes: item.panes.map((pane) => ({
            ...pane,
            tabs: pane.tabs.map((tab) => ({
              ...tab,
              connId: tab.kind === "db" ? connId : tab.connId,
              restorePending: undefined,
            })),
          })),
        } : item),
      }));
      return { ok: true };
    }

    if (workspace.assetId && !workspace.assetId.startsWith("device/")) {
      const asset = await assetApi.get(workspace.assetId);
      if (!stillPending()) return { ok: false, canceled: true };
      if (!(await ensureVaultReadyFor(asset, epoch))) return { ok: false, canceled: true };
    }
    if (!stillPending()) return { ok: false, canceled: true };
    const session = await reconnectDeadHost(workspace, { isCurrent: stillPending });
    if (!session) return { ok: false, canceled: true };
    if (!stillPending()) {
      await sessionApi.disconnect(session.id).catch(() => undefined);
      return { ok: false, canceled: true };
    }
    useUi.setState((current) => ({
      ...(options?.preserveUserFocus ? {} : { activeWorkspaceId: workspaceId }),
      sessions: [...current.sessions.filter((item) => item.id !== session.id), session],
      workspaces: current.workspaces.map((item) => item.id === workspaceId ? {
        ...item,
        sessionId: session.id,
        restorePending: undefined,
        panes: item.panes.map((pane) => ({
          ...pane,
          tabs: pane.tabs.map((tab) => ({
            ...tab,
            sessionId: session.id,
            restorePending: tab.kind === "terminal" ? true : undefined,
          })),
        })),
      } : item),
    }));
    if (workspace.assetKind !== "device" && !workspace.panes.some((pane) => pane.tabs.some((tab) => tab.kind === "terminal"))) {
      if (options?.preserveUserFocus) addRestoredTerminalWithoutFocus(workspaceId, session);
      else await openTerminalTab(session);
    }
    return { ok: true };
  } catch (error) {
    if (!stillPending()) return { ok: false, canceled: true };
    return { ok: false, error: describeError(error) };
  }
}

async function reconnectDeadRestoredWorkspace(
  workspace: Workspace,
  options?: RestoreWorkspaceOptions,
): Promise<ConnectOutcome> {
  const state = useUi.getState();
  if (!workspaceSessionIsDead(workspace, state.sessions)) {
    return { ok: false, error: "工作区无需重新连接" };
  }
  if (workspace.deletedTarget) return { ok: false, error: "原资产已删除，请从资产树选择其他目标" };
  const epoch = state.workspaceRuntimeEpoch;
  const existing = state.sessions.find((item) => item.id === workspace.sessionId);
  const stillCurrent = () => {
    const current = useUi.getState();
    return current.workspaceRuntimeEpoch === epoch && current.workspaces.some(
      (item) => item === workspace && !item.restorePending && item.sessionId === workspace.sessionId &&
        item.assetId === workspace.assetId && item.quickTarget?.id === workspace.quickTarget?.id,
    );
  };
  try {
    if (workspace.assetId && !workspace.assetId.startsWith("device/")) {
      const asset = await assetApi.get(workspace.assetId);
      if (!stillCurrent()) return { ok: false, canceled: true };
      if (!(await ensureVaultReadyFor(asset, epoch))) return { ok: false, canceled: true };
    }
    if (!stillCurrent()) return { ok: false, canceled: true };
    if (existing) {
      const fresh = await reconnectSessionAndWait(existing, { isCurrent: stillCurrent });
      if (!fresh || !stillCurrent()) return { ok: false, canceled: true };
      useUi.setState((current) => ({
        ...(options?.preserveUserFocus ? {} : { activeWorkspaceId: workspace.id }),
        sessions: [...current.sessions.filter((item) => item.id !== fresh.id), fresh],
      }));
      return { ok: true };
    }

    const session = await reconnectDeadHost(workspace, { isCurrent: stillCurrent });
    if (!session) return { ok: false, canceled: true };
    if (!stillCurrent()) {
      await sessionApi.disconnect(session.id).catch(() => undefined);
      return { ok: false, canceled: true };
    }
    useUi.setState((current) => ({
      ...(options?.preserveUserFocus ? {} : { activeWorkspaceId: workspace.id }),
      sessions: [...current.sessions.filter((item) => item.id !== session.id), session],
      workspaces: current.workspaces.map((item) => item.id === workspace.id ? {
        ...item,
        sessionId: session.id,
        panes: item.panes.map((pane) => ({
          ...pane,
          tabs: pane.tabs.map((tab) => {
            if (tab.kind !== "terminal") return { ...tab, sessionId: session.id, restorePending: undefined };
            if (tab.containerId) {
              return {
                ...tab,
                sessionId: session.id,
                tabId: undefined,
                restorePending: true,
                dead: undefined,
                exited: true,
              };
            }
            return { ...tab, sessionId: session.id, restorePending: undefined, dead: false };
          }),
        })),
      } : item),
    }));
    if (workspace.assetKind !== "device" && !workspace.panes.some((pane) => pane.tabs.some((tab) => tab.kind === "terminal"))) {
      if (options?.preserveUserFocus) addRestoredTerminalWithoutFocus(workspace.id, session);
      else await openTerminalTab(session);
    }
    return { ok: true };
  } catch (error) {
    if (!stillCurrent()) return { ok: false, canceled: true };
    return { ok: false, error: describeError(error) };
  }
}

function addRestoredTerminalWithoutFocus(workspaceId: string, session: SessionInfo): void {
  useUi.setState((current) => ({
    workspaces: current.workspaces.map((workspace) => {
      if (
        workspace.id !== workspaceId || workspace.sessionId !== session.id ||
        workspace.panes.some((pane) => pane.tabs.some((tab) => tab.kind === "terminal"))
      ) {
        return workspace;
      }
      const panes = workspace.panes.length > 0 ? workspace.panes : [makePane()];
      const activePane = panes.find((pane) => pane.id === workspace.activePaneId) ?? panes[0];
      const tab: AppTab = {
        id: nextTabId(`term-${session.id}`),
        kind: "terminal",
        title: "终端 1",
        sessionId: session.id,
        closable: true,
      };
      return {
        ...workspace,
        panes: panes.map((pane) => pane.id === activePane.id
          ? { ...pane, tabs: [...pane.tabs, tab] }
          : pane),
      };
    }),
  }));
}

const inflightQuickConnects = new Map<string, Promise<ConnectOutcome>>();

async function promptQuickConnectPassword(display: string): Promise<string | null> {
  const { promptText } = await import("../ui/dialogs");
  return promptText(`输入 ${display} 的登录密码（留空则使用 SSH agent）`, "", {
    secret: true,
  });
}

export async function connectQuickTarget(
  target: QuickConnectTarget,
  options?: ConnectAssetOptions,
): Promise<ConnectOutcome> {
  const epoch = useUi.getState().workspaceRuntimeEpoch;
  const key = JSON.stringify([epoch, quickConnectionProblemKey(target)]);
  const inflight = inflightQuickConnects.get(key);
  if (inflight) return inflight;
  const context = captureConnectionAttempt();
  const promise = runQuickConnectTarget(target, options, epoch).finally(() => {
    if (inflightQuickConnects.get(key) === promise) inflightQuickConnects.delete(key);
  });
  inflightQuickConnects.set(key, promise);
  const outcome = await promise;
  if (currentConnectionEpoch(epoch)) settleQuickConnectionOutcome(context, target, outcome);
  return outcome;
}

async function runQuickConnectTarget(
  target: QuickConnectTarget,
  options?: ConnectAssetOptions,
  epoch = useUi.getState().workspaceRuntimeEpoch,
): Promise<ConnectOutcome> {
  if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
  const { pushToast } = useUi.getState();
  const display = formatQuickConnectTarget(target);
  const password = await promptQuickConnectPassword(display);
  if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
  if (password === null) return { ok: false, canceled: true };
  try {
    const info = await connectWithHostKeyConfirm(() =>
      sessionApi.connectQuick({
        host: target.host,
        port: target.port,
        ...(target.username ? { username: target.username } : {}),
        authKind: password ? "password" : "agent",
        ...(password ? { password } : {}),
      }),
      () => currentConnectionEpoch(epoch),
    );
    if (!currentConnectionEpoch(epoch)) return info ? discardStaleSession(info) : { ok: false, canceled: true };
    if (!info) {
      pushToast("info", "已取消连接");
      return { ok: false, canceled: true };
    }
    await openConnectedAssetSession(info, { id: "", name: info.name, kind: "ssh" }, options, {
      id: nextTabId("quick-target"),
      host: target.host,
      port: target.port,
      username: target.username,
      authKind: password ? "password" : "agent",
    });
    return { ok: true };
  } catch (e) {
    if (!currentConnectionEpoch(epoch)) return { ok: false, canceled: true };
    const err = e as { message?: string };
    const message = err.message || describeError(e);
    if (!options?.silent) pushToast("error", message);
    return { ok: false, error: message };
  }
}

export type SaveQuickConnectOutcome = ConnectOutcome & {
  asset?: Awaited<ReturnType<typeof assetApi.create>>;
  problemKey: string;
};

interface SaveQuickConnectAttempt {
  outcome: ConnectOutcome;
  asset?: SaveQuickConnectOutcome["asset"];
}

export async function saveQuickConnectAsset(
  target: QuickConnectTarget,
  options?: ConnectAssetOptions,
): Promise<SaveQuickConnectOutcome> {
  const epoch = useUi.getState().workspaceRuntimeEpoch;
  const context = captureConnectionAttempt();
  const quickKey = quickConnectionProblemKey(target);
  const attempt = await runSaveQuickConnectAsset(target, options, epoch);
  if (currentConnectionEpoch(epoch) && !attempt.asset) {
    settleQuickConnectionOutcome(context, target, attempt.outcome, "save");
  }
  return {
    ...attempt.outcome,
    ...(attempt.asset ? { asset: attempt.asset } : {}),
    problemKey: attempt.asset ? assetConnectionProblemKey(attempt.asset.id) : quickKey,
  };
}

async function runSaveQuickConnectAsset(
  target: QuickConnectTarget,
  options: ConnectAssetOptions | undefined,
  epoch: number,
): Promise<SaveQuickConnectAttempt> {
  const { pushToast } = useUi.getState();
  const display = formatQuickConnectTarget(target);
  let created: SaveQuickConnectOutcome["asset"];
  const finish = (outcome: ConnectOutcome): SaveQuickConnectAttempt => ({
    outcome,
    ...(created ? { asset: created } : {}),
  });
  const password = await promptQuickConnectPassword(display);
  if (!currentConnectionEpoch(epoch)) return finish({ ok: false, canceled: true });
  if (password === null) return finish({ ok: false, canceled: true });
  try {
    // The server assembly disabled session_quick_connect_user: in web mode, do not backfill the server OS user.
    const username = target.username ?? (DESKTOP ? await sessionApi.quickConnectDefaultUser() : "");
    if (!currentConnectionEpoch(epoch)) return finish({ ok: false, canceled: true });
    let credId: string | null = null;
    if (password) {
      if (!(await ensureVaultUnlocked("保存凭据前需要解锁凭据库", epoch)) || !currentConnectionEpoch(epoch)) {
        return finish({ ok: false, canceled: true });
      }
      const res = await vaultApi.setCredential(display, "password", password);
      credId = res.id;
      if (!currentConnectionEpoch(epoch)) return finish({ ok: false, canceled: true });
    }
    try {
      created = await assetApi.create({
        kind: "ssh",
        name: display,
        host: target.host,
        port: target.port,
        username,
        authKind: password ? "password" : "agent",
        keyPath: null,
        credId,
      });
      if (!currentConnectionEpoch(epoch)) return finish({ ok: false, canceled: true });
    } catch (e) {
      if (credId && currentConnectionEpoch(epoch)) await vaultApi.deleteCredential(credId).catch(() => undefined);
      throw e;
    }
    useUi.getState().clearConnectionProblem(quickConnectionProblemKey(target));
    return finish(await connectAsset(created, options));
  } catch (e) {
    if (!currentConnectionEpoch(epoch)) return finish({ ok: false, canceled: true });
    const err = e as { message?: string };
    const message = err.message || describeError(e);
    if (!options?.silent) pushToast("error", message);
    return finish({ ok: false, error: message });
  }
}

const inflightReconnects = new Map<string, Promise<SessionInfo | null>>();

// SESSION_RECONNECT_WAIT_MS aligns with the backend reconnect lifecycle: internal/session defaults to 10 attempts with
// backoff 1/2/4/8/16/30s (181s total), each SSH dial defaults to 15s, so 6 minutes covers the worst case;
// terminal connected/failed states end the wait early, the timeout is only a fallback.
export const SESSION_RECONNECT_WAIT_MS = 360_000;

export function isSessionReconnectPending(sessionId: string): boolean {
  return inflightReconnects.has(JSON.stringify([useUi.getState().workspaceRuntimeEpoch, sessionId]));
}

export async function reconnectWorkspaceSession(
  workspace: Pick<Workspace, "assetId" | "assetKind" | "title" | "quickTarget"> & { id?: string },
  session: SessionInfo,
  options?: { timeoutMs?: number; intervalMs?: number; isCurrent?: () => boolean },
): Promise<SessionInfo | null> {
  const epoch = useUi.getState().workspaceRuntimeEpoch;
  const context = captureConnectionAttempt();
  const isCurrent = () => currentConnectionEpoch(epoch) && (options?.isCurrent?.() ?? true);
  try {
    const fresh = await reconnectSessionAndWait(session, { ...options, isCurrent });
    if (!isCurrent()) return null;
    if (fresh) settleWorkspaceConnectionOutcome(context, workspace, { ok: true }, workspace.id);
    return fresh;
  } catch (error) {
    if (!isCurrent()) return null;
    settleWorkspaceConnectionOutcome(context, workspace, { ok: false, error: describeError(error) }, workspace.id);
    throw error;
  }
}

export function reconnectSessionAndWait(
  session: SessionInfo,
  options?: { timeoutMs?: number; intervalMs?: number; isCurrent?: () => boolean },
): Promise<SessionInfo | null> {
  const epoch = useUi.getState().workspaceRuntimeEpoch;
  const key = JSON.stringify([epoch, session.id]);
  const inflight = inflightReconnects.get(key);
  if (inflight) return inflight;
  const isCurrent = () => currentConnectionEpoch(epoch) && (options?.isCurrent?.() ?? true);
  const promise = runReconnectAndWait(
    session,
    options?.timeoutMs ?? SESSION_RECONNECT_WAIT_MS,
    options?.intervalMs ?? 600,
    isCurrent,
  ).finally(() => {
    if (inflightReconnects.get(key) === promise) inflightReconnects.delete(key);
  });
  inflightReconnects.set(key, promise);
  return promise;
}

export interface SessionReconnectError extends Error {
  code?: string;
  attempt?: number;
  nextRetryAt?: string | number | null;
}

function reconnectPollingError(session: SessionRuntimeInfo): SessionReconnectError {
  const error = new Error(session.error || `会话${sessionStatusText(session.status)}`) as SessionReconnectError;
  if (session.errorCode) error.code = session.errorCode;
  if (session.attempt !== undefined) error.attempt = session.attempt;
  if (session.nextRetryAt !== undefined) error.nextRetryAt = session.nextRetryAt;
  return error;
}

async function runReconnectAndWait(
  session: SessionInfo,
  timeoutMs: number,
  intervalMs: number,
  isCurrent: () => boolean,
): Promise<SessionInfo | null> {
  if (!isCurrent()) return null;
  try {
    if (!(await confirmHostKeyIfNeeded(session.assetId ?? "", session.kind, isCurrent))) return null;
  } catch (error) {
    if (!isCurrent()) return null;
    throw error;
  }
  if (!isCurrent()) return null;
  let started: boolean;
  try {
    started = await sessionApi.reconnect(session.id);
  } catch (error) {
    if (!isCurrent()) return null;
    throw error;
  }
  if (!isCurrent()) return null;
  if (!started) throw new Error("重连未能启动");
  const deadline = Date.now() + timeoutMs;
  // session_reconnect returns first and the backend goroutine sets reconnecting afterwards, so the first list may read the pre-start
  // stale disconnected/failed; only a terminal state seen after connecting/reconnecting counts as failure.
  let enteredReconnect = false;
  for (;;) {
    if (!isCurrent()) return null;
    let list: SessionRuntimeInfo[];
    try {
      list = await sessionApi.list();
    } catch (error) {
      if (!isCurrent()) return null;
      throw error;
    }
    if (!isCurrent()) return null;
    const current = list.find((s) => s.id === session.id);
    if (current?.status === "connected") return current;
    if (current?.status === "connecting" || current?.status === "reconnecting") {
      enteredReconnect = true;
    } else if (current && enteredReconnect) {
      throw reconnectPollingError(current);
    }
    if (Date.now() >= deadline) throw new Error("重连超时，请检查网络后重试");
    await new Promise((resolve) => setTimeout(resolve, intervalMs));
    if (!isCurrent()) return null;
  }
}
