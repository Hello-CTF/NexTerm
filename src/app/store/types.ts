import type { StoreApi } from "zustand";
import type { assetApi, RecordingStatus, SessionInfo } from "../../ipc/commands";
import type { ConnectProgressEvent, HelperProgressEvent, SessionStatusEvent as BaseSessionStatusEvent } from "../../ipc/events";
import type { HelperOperationKind, HelperOperationState } from "../../features/terminal/helperOperationState";
import type { QuickConnectTarget } from "../quickConnectTarget";
import type { ResolvedTheme, ThemeMode } from "../theme";
import type { ProjectFocus, ProjectInventory } from "../projectFocus";
import type { FocusDirection, SplitNode } from "../splitTree";
import type { PaneEdge } from "../tabDropZone";

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
  | "execBatch"
  | "host";

// The file tree moved from the left dock to the session side panel; a stored "files" mode reads as "assets".
export type LeftMode = "assets" | "credentials";

export type DbKind = "mysql" | "postgres" | "redis";

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
  // A file browser tab opened at a directory (a terminal file link); persisted with the layout.
  directory?: string;
  shell?: "powershell" | "cmd" | "posix" | "unknown";
  pendingDirectory?: string;
  closable: boolean;
  dead?: boolean;
  exited?: boolean;
  retainable?: boolean;
  // durable is reported by the backend once the terminal attaches; SSH tabs
  // can only move to the background when they run on the durable helper.
  durable?: boolean;
  // regularReason marks a terminal that was opened as a regular terminal although durable terminals are preferred
  // (the helper was still preparing or unavailable); the tab shows it so the degradation is never silent.
  regularReason?: string;
  // renamedByUser locks the title against OSC updates; customTitleAt is when the title was
  // last decided explicitly (a rename or a clear, by hand, by the AI, or as a backend custom
  // title) and orders those decisions across devices. A lock without a timestamp is a legacy
  // layout entry that may hold an OSC title, so it does not count as a custom title.
  // Times are wall-clock Unix ms from the deciding device (the backend clamps far-future
  // ones), so ordering across devices is best effort and only as good as their clocks.
  renamedByUser?: boolean;
  customTitleAt?: number;
  closeError?: string;
  restorePending?: boolean;
}

export type WorkspaceKind = "session" | "db" | "tools";

// Where a split puts the new pane relative to the pane being split.
export type SplitDirection = "right" | "down";

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
  // Pane contents; their placement on screen comes from layoutTree.
  panes: Pane[];
  activePaneId: string;
  // Nested split layout over the panes. Absent until the workspace first changes its layout, which
  // reads as the panes stacked top to bottom; workspaceLayoutTree() resolves either form.
  layoutTree?: SplitNode;
  // Wall-clock milliseconds of the last layout change on the device that made it; merges use it to
  // pick a side when two devices reshape the same workspace.
  layoutUpdatedAt?: number;
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
  // "suspect" while a connected session's heartbeats go unanswered.
  liveness?: string | null;
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

export interface UiState {
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
  accountEpoch: number;
  advanceAccountEpoch: () => void;
  // knownAssetIds is the last authoritative asset-ID snapshot of the current
  // account epoch; knownAssetsEpoch tracks which epoch it belongs to.
  knownAssetIds: string[];
  knownAssetsEpoch: number;
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
  cancelAccountDialogs: () => void;

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
  reconcileDeletedTargets: (assets: readonly { id: string }[], epoch: number) => void;

  splitPane: (direction: SplitDirection, paneId?: string, workspaceId?: string) => string | null;
  openInLowerPane: (tab: AppTab, workspaceId?: string) => void;
  closePane: (paneId?: string, workspaceId?: string) => void;
  setActivePane: (paneId: string, workspaceId?: string) => void;
  focusPane: (direction: FocusDirection, workspaceId?: string) => string | null;
  setSplitRatio: (splitId: string, ratio: number, workspaceId?: string) => void;
  /** Moves a tab into another pane of its workspace, at `index` of its tabs (default: the end). */
  moveTabToPane: (tabId: string, paneId: string, index?: number) => void;
  /**
   * Splits `paneId` on `edge` and moves the tab into the new pane in one step; returns the new pane id,
   * or null (with a toast) when the split is refused.
   */
  splitPaneWithTab: (tabId: string, paneId: string, edge: PaneEdge, workspaceId?: string) => string | null;

  setActiveTab: (id: string) => void;
  addTab: (tab: AppTab, paneId?: string) => void;
  closeTab: (id: string, mode?: "kill" | "detach", suppressErrorToast?: boolean) => Promise<boolean>;
  updateTab: (id: string, patch: Partial<AppTab>) => void;
  migrateFileEditorLocators: (sessionId: string, from: string, to: string, kind: string) => void;
  resolveUnknownFileEditorRename: (sessionId: string, path: string, renamed: boolean) => void;
  moveTabInPane: (paneId: string, fromIndex: number, toIndex: number) => void;
  moveWorkspace: (fromIndex: number, toIndex: number) => void;
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
  helperOperations: HelperOperationState;
  helperOperationTabs: Record<string, string>;
  helperOperationIntents: Record<string, { kind: HelperOperationKind; originalTabId?: string; sequence: number }>;
  helperRetries: Record<string, { tabId: string; operationId: string }>;
  registerHelperOperationIntent: (tabId: string, kind: HelperOperationKind, originalTabId?: string) => void;
  setHelperProgress: (progress: HelperProgressEvent | null) => void;
  cancelHelperOperation: (tabId: string) => Promise<void>;
  retryHelperOperation: (tabId: string) => Promise<void>;
  continueHelperOrdinary: (tabId: string, remember?: boolean) => Promise<void>;
  failHelperRestore: (tabId: string, message?: string) => void;
  dismissHelperOperation: (tabId: string, operationId?: string) => void;
}

export interface SessionStatusEvidence {
  version: number;
  status: SessionInfo["status"];
  error: string | null;
  errorCode: string | null;
  attempt: number | undefined;
  nextRetryAt: string | number | null | undefined;
  engagedVersion: number;
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
  // openTerminal opens a terminal with the new workspace; quick connections use it, host clicks do not.
  openTerminal?: boolean;
}

export type ConnectOutcome =
  | { ok: true }
  | { ok: false; error?: string; canceled?: boolean };

export interface RestoreWorkspaceOptions {
  preserveUserFocus?: boolean;
}

export type SaveQuickConnectOutcome = ConnectOutcome & {
  asset?: Awaited<ReturnType<typeof assetApi.create>>;
  problemKey: string;
};

export interface SessionReconnectError extends Error {
  code?: string;
  attempt?: number;
  nextRetryAt?: string | number | null;
}

export type UiSet = StoreApi<UiState>["setState"];
export type UiGet = StoreApi<UiState>["getState"];
