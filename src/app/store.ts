import { create } from "zustand";
import { assetApi, dbApi, sessionApi, terminalApi, vaultApi, type SessionInfo, type VaultStatus } from "../ipc/commands";
import { DESKTOP } from "../ipc/env";
import { describeError } from "../ui/errorText";
import { connectWithHostKeyConfirm, confirmHostKeyIfNeeded } from "./hostKeys";
import { formatQuickConnectTarget, type QuickConnectTarget } from "./quickConnectTarget";
import { dirtyFileEditors } from "../features/files/editorGuards";
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
import type { LayoutPreset } from "./layoutPresets";

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
  | "deviceTerminal"
  | "background"
  | "history";

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
  tabId?: string;
  containerId?: string;
  connId?: string;
  dbKind?: DbKind;
  credId?: string;
  path?: string;
  deviceId?: string;
  pendingCommand?: string;
  closable: boolean;
  dead?: boolean;
  exited?: boolean;
  renamedByUser?: boolean;
}

export type WorkspaceKind = "session" | "db" | "tools";

// 全局工具标签统一落入唯一的「工具」工作区, 不寄生主机/数据库工作区。
export const TOOL_TAB_KINDS: ReadonlySet<PaneKind> = new Set<PaneKind>([
  "settings",
  "audit",
  "devices",
  "deviceTerminal",
  "background",
  "history",
  "credentials",
]);

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
}

export interface ToastItem {
  id: number;
  kind: "info" | "error" | "success";
  text: string;
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
  resolve: (v: string | null) => void;
}

export interface TakeoverState {
  tabId: string;
  jobId?: string;
  token: string;
  task: string;
  allowWrite: boolean;
  startedAt: number;
}

// AI 看板按工作区分组: 每个工作区一组标签, 每个标签绑定一个独立会话。
export interface AiDockTab {
  id: string;
  title: string;
  conversationId?: string;
}

export interface AiBoard {
  tabs: AiDockTab[];
  activeTabId: string | null;
}

// 没有远程主机工作区(无工作区或「工具」工作区)时, AI 看板落在全局分组。
export const GLOBAL_AI_BOARD_KEY = "global";

export interface BroadcastState {
  workspaceId: string;
  targetIds: string[];
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
  layoutPresets: LayoutPreset[];
  setLayoutPresets: (presets: LayoutPreset[]) => void;
  sessions: SessionInfo[];
  aiBusy: boolean;
  takeover: TakeoverState | null;
  setTakeover: (t: TakeoverState | null) => void;
  aiBoards: Record<string, AiBoard>;
  ensureAiBoard: (key: string) => AiBoard;
  addAiTab: (key: string) => string;
  closeAiTab: (key: string, tabId: string) => void;
  setActiveAiTab: (key: string, tabId: string) => void;
  updateAiTab: (key: string, tabId: string, patch: Partial<AiDockTab>) => void;
  broadcast: BroadcastState | null;
  setBroadcast: (b: BroadcastState | null) => void;
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
  closeWorkspace: (id: string) => Promise<void>;

  splitWorkspace: (workspaceId?: string) => void;
  openInLowerPane: (tab: AppTab, workspaceId?: string) => void;
  unsplitWorkspace: (paneId?: string, workspaceId?: string) => Promise<void>;
  setActivePane: (paneId: string, workspaceId?: string) => void;
  setSplitRatio: (ratio: number, workspaceId?: string) => void;

  setActiveTab: (id: string) => void;
  addTab: (tab: AppTab, paneId?: string) => void;
  closeTab: (id: string, mode?: "kill" | "detach") => Promise<boolean>;
  updateTab: (id: string, patch: Partial<AppTab>) => void;
  updateWorkspaceSession: (workspaceId: string, sessionId: string) => void;

  setSessions: (s: SessionInfo[]) => void;
  resyncSessions: (options?: { silent?: boolean }) => Promise<void>;
  resetSessions: () => void;
  sessionResyncFailed: boolean;
  setAiBusy: (v: boolean) => void;
  pushToast: (kind: ToastItem["kind"], text: string) => void;
  dismissToast: (id: number) => void;

  connectingAssetIds: string[];
}

let toastSeq = 1;
let tabSeq = 1;

// 会话列表的 auth 守卫: 登录/登出切换账号时, 在途的旧请求可能迟到, 不允许回写覆盖新会话的数据。
let sessionsEpoch = 0;

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

function loadLayout(): { leftWidth: number; rightWidth: number } {
  const fallback = { leftWidth: LEFT_DEFAULT, rightWidth: RIGHT_DEFAULT };
  try {
    const raw = localStorage.getItem(LAYOUT_KEY);
    if (!raw) return fallback;
    const o = JSON.parse(raw) as { leftWidth?: number; rightWidth?: number };
    return {
      leftWidth: clampWidth(o.leftWidth ?? LEFT_DEFAULT, LEFT_MIN, LEFT_MAX),
      rightWidth: clampWidth(o.rightWidth ?? RIGHT_DEFAULT, RIGHT_MIN, RIGHT_MAX),
    };
  } catch {
    return fallback;
  }
}

function saveLayout(w: { leftWidth: number; rightWidth: number }): void {
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

const PLACEHOLDER_WS_TITLES = new Set(["工作区", "会话", "标签"]);

function workspaceFieldPatch(
  w: Workspace,
  spec: WorkspaceSpec,
): Partial<Pick<Workspace, "title" | "assetId" | "assetKind">> | null {
  const patch: Partial<Pick<Workspace, "title" | "assetId" | "assetKind">> = {};
  let changed = false;
  if (!w.assetId && spec.assetId) {
    patch.assetId = spec.assetId;
    changed = true;
  }
  if (!w.assetKind && spec.assetKind) {
    patch.assetKind = spec.assetKind;
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

export const useUi = create<UiState>((set, get) => ({
  leftOpen: true,
  leftMode: "assets",
  rightOpen: true,
  themeMode: getThemeMode(),
  resolvedTheme: getResolvedTheme(),
  setThemeMode: (m) => {
    persistThemeMode(m);
    set({ themeMode: m, resolvedTheme: getResolvedTheme() });
  },
  leftWidth: initialLayout.leftWidth,
  rightWidth: initialLayout.rightWidth,
  workspaces: [],
  activeWorkspaceId: null,
  layoutPresets: [],
  setLayoutPresets: (presets) => set({ layoutPresets: presets }),
  sessions: [],
  aiBusy: false,
  takeover: restoredTakeover,
  aiBoards: {},
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
    cur?.resolve(v);
    set({ appChoice: null });
  },

  setLeftOpen: (v) => set({ leftOpen: v }),
  setLeftMode: (m) => set({ leftMode: m }),
  setRightOpen: (v) => set({ rightOpen: v }),
  setLeftWidth: (w) => {
    const next = clampWidth(w, LEFT_MIN, LEFT_MAX);
    set({ leftWidth: next });
    saveLayout({ leftWidth: next, rightWidth: get().rightWidth });
  },
  setRightWidth: (w) => {
    const next = clampWidth(w, RIGHT_MIN, RIGHT_MAX);
    set({ rightWidth: next });
    saveLayout({ leftWidth: get().leftWidth, rightWidth: next });
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

  setActiveWorkspace: (id) => set({ activeWorkspaceId: id }),

  closeWorkspace: async (id) => {
    const { workspaces, activeWorkspaceId } = get();
    const target = workspaces.find((w) => w.id === id);
    if (!target) return;
    const tabs = target.panes.flatMap((p) => p.tabs);
    const summary = collectWorkspaceClose(tabs, target.assetKind);
    if (!(await confirmWorkspaceClose(summary, target.title))) return;
    const outcome = await executeWorkspaceClose(summary);
    if (outcome.dbFailed > 0) {
      get().pushToast(
        "error",
        `「${target.title}」保持打开：${outcome.dbFailed}/${summary.dbConnIds.length} 个数据库断开失败：${outcome.dbError}`,
      );
      return;
    }
    const next = workspaces.filter((w) => w.id !== id);
    set({
      workspaces: next,
      activeWorkspaceId:
        activeWorkspaceId === id ? (next.length ? next[next.length - 1].id : null) : activeWorkspaceId,
      ...(get().broadcast?.workspaceId === id ? { broadcast: null } : {}),
      ...(get().aiBoards[id] ? { aiBoards: omitAiBoard(get().aiBoards, id) } : {}),
    });
    pushWorkspaceCloseToast(target.title, outcome);
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
    const st = get();
    const id = workspaceId ?? st.activeWorkspaceId ?? st.workspaces[st.workspaces.length - 1]?.id;
    if (!id) return;
    const w = st.workspaces.find((x) => x.id === id);
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
    set((s2) => ({
      workspaces: s2.workspaces.map((x) =>
        x.id === id ? { ...x, panes: keep, activePaneId: keep[0].id } : x,
      ),
    }));
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

  setActiveTab: (id) =>
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
                    panes: x.panes.map((q) => (q.id === p.id ? { ...q, activeTabId: id } : q)),
                  }
                : x,
            ),
          };
        }
      }
      return {};
    }),

  addTab: (tab, paneId) => {
    const wsId = resolveWorkspaceId(tab, get());
    const w = get().workspaces.find((x) => x.id === wsId);
    if (!w) return;
    const panes = w.panes.length ? w.panes : [makePane()];

    for (const p of panes) {
      const exists = p.tabs.find((t) => sameTab(t, tab));
      if (exists) {
        set((st) => ({
          activeWorkspaceId: wsId,
          workspaces: st.workspaces.map((x) =>
            x.id === wsId
              ? {
                  ...x,
                  activePaneId: p.id,
                  panes: x.panes.map((q) =>
                    q.id === p.id
                      ? {
                          ...q,
                          tabs: q.tabs.map((t) =>
                            t.id === exists.id ? { ...t, ...tab, id: exists.id } : t,
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
    }

    const target = paneId
      ? (panes.find((p) => p.id === paneId) ?? panes[0])
      : pickPaneForTab(panes, w.activePaneId, tab);
    set((st) => ({
      activeWorkspaceId: wsId,
      workspaces: st.workspaces.map((x) =>
        x.id === wsId
          ? {
              ...x,
              activePaneId: target.id,
              panes: x.panes.map((q) =>
                q.id === target.id ? { ...q, tabs: [...q.tabs, tab], activeTabId: tab.id } : q,
              ),
            }
          : x,
      ),
    }));
  },

  closeTab: async (id, mode) => {
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
        await terminalApi.closeTab(target.tabId, mode);
      } catch (e) {
        get().pushToast("error", `终端操作失败：${describeError(e)}`);
        return false;
      }
    } else if (target.kind === "db" && target.connId) {
      try {
        await dbApi.disconnect(target.connId);
      } catch (e) {
        get().pushToast("error", `数据库断开失败：${describeError(e)}`);
        return false;
      }
    }
    set((s2) => {
      const bc = s2.broadcast;
      let broadcastPatch: Partial<Pick<UiState, "broadcast">> = {};
      if (bc && bc.targetIds.includes(id)) {
        const remaining = bc.targetIds.filter((t) => t !== id);
        broadcastPatch = {
          broadcast: remaining.length > 0 ? { workspaceId: bc.workspaceId, targetIds: remaining } : null,
        };
      }
      return {
        workspaces: s2.workspaces.map((w) => {
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
        }),
        ...broadcastPatch,
      };
    });
    return true;
  },

  updateTab: (id, patch) =>
    set((st) => {
      let from: string | undefined;
      for (const w of st.workspaces) {
        for (const p of w.panes) {
          const t = p.tabs.find((x) => x.id === id);
          if (t) from = t.sessionId;
        }
      }
      const to =
        typeof patch.sessionId === "string" && from !== undefined && patch.sessionId !== from
          ? patch.sessionId
          : undefined;
      return {
        workspaces: st.workspaces.map((w) => ({
          ...w,
          ...(to !== undefined &&
          w.sessionId === from &&
          w.panes.some((p) => p.tabs.some((t) => t.id === id))
            ? { sessionId: to }
            : {}),
          panes: w.panes.map((p) =>
            p.tabs.some((t) => t.id === id)
              ? { ...p, tabs: p.tabs.map((t) => (t.id === id ? { ...t, ...patch } : t)) }
              : p,
          ),
        })),
      };
    }),

  updateWorkspaceSession: (workspaceId, sessionId) =>
    set((st) => ({
      workspaces: st.workspaces.map((w) => (w.id === workspaceId ? { ...w, sessionId } : w)),
    })),

  setSessions: (s) => set({ sessions: s }),
  sessionResyncFailed: false,
  resyncSessions: async (options) => {
    const epoch = sessionsEpoch;
    try {
      const list = await sessionApi.list();
      if (epoch !== sessionsEpoch) return;
      set({ sessions: list, sessionResyncFailed: false });
    } catch (e) {
      console.error("[NexTerm] 刷新会话列表失败", e);
      if (epoch !== sessionsEpoch || options?.silent) return;
      if (get().sessionResyncFailed) return;
      set({ sessionResyncFailed: true });
      get().pushToast("error", `刷新会话列表失败：${describeError(e)}`);
    }
  },
  resetSessions: () => {
    sessionsEpoch += 1;
    set({ sessions: [], sessionResyncFailed: false });
  },
  setAiBusy: (v) => set({ aiBusy: v }),
  setTakeover: (t) => {
    restoredTakeover = null;
    persistTakeover(t);
    set({ takeover: t });
  },
  setBroadcast: (b) => set({ broadcast: b }),

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
  closeAiTab: (key, tabId) =>
    set((s) => {
      const board = s.aiBoards[key];
      if (!board) return s;
      if (board.tabs.length <= 1) {
        if (board.tabs[0]?.id !== tabId) return s;
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
  updateAiTab: (key, tabId, patch) =>
    set((s) => {
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

  pushToast: (kind, text) => {
    const existing = get().toasts.find((t) => t.kind === kind && t.text === text);
    if (existing) get().dismissToast(existing.id);
    const id = toastSeq++;
    set((st) => ({ toasts: [...st.toasts, { id, kind, text }] }));
    window.setTimeout(() => get().dismissToast(id), kind === "error" ? 8000 : 3500);
  },
  dismissToast: (id) =>
    set((st) => ({ toasts: st.toasts.filter((t) => t.id !== id) })),
}));

initTheme();
subscribeTheme((m, resolved) => {
  useUi.setState({ themeMode: m, resolvedTheme: resolved });
});

function resolveWorkspaceId(
  tab: AppTab,
  st: { workspaces: Workspace[]; sessions: SessionInfo[]; activeWorkspaceId: string | null },
): string {
  const { ensureWorkspace } = useUi.getState();
  if (TOOL_TAB_KINDS.has(tab.kind)) {
    return ensureWorkspace({ kind: "tools", title: "工具" });
  }
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
  if (st.activeWorkspaceId) return st.activeWorkspaceId;
  return ensureWorkspace({ kind: "tools", title: "工具" });
}

function makePane(): Pane {
  return { id: nextTabId("pane"), tabs: [], activeTabId: null };
}

function sameTab(t: AppTab, tab: AppTab): boolean {
  if (t.id === tab.id) return true;
  if (t.tabId && tab.tabId && t.tabId === tab.tabId) return true;
  if (t.kind === "deviceTerminal" && tab.kind === "deviceTerminal") {
    return t.deviceId === tab.deviceId;
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
  return useUi(
    (s) =>
      s.workspaces.find((w) => w.id === s.activeWorkspaceId) ??
      s.workspaces[s.workspaces.length - 1] ??
      null,
  );
}

export async function openTerminalTab(
  session: SessionInfo,
  title?: string,
  paneId?: string,
  options?: { command?: string },
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
      pendingCommand: options?.command,
      closable: true,
    },
    paneId,
  );
}

// openDeviceTerminalTab 从设备管理打开一台设备的远程终端标签 (FLEET156);
// 同一设备重复打开会聚焦已有标签 (sameTab 按 deviceId 去重)。
export function openDeviceTerminalTab(device: { id: string; name: string }): void {
  useUi.getState().addTab({
    id: nextTabId(`devterm-${device.id}`),
    kind: "deviceTerminal",
    title: `终端 · ${device.name}`,
    deviceId: device.id,
    closable: true,
  });
}

async function confirmDirtyEditors(tabs: AppTab[], title: string): Promise<boolean> {
  const dirty = dirtyFileEditors(tabs);
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

function isRunningTerminal(t: AppTab): boolean {
  return t.kind === "terminal" && Boolean(t.tabId) && !t.dead && !t.exited;
}

function detachBlockOf(
  t: AppTab,
  sessions: SessionInfo[],
  assetKind?: string,
): "exec" | "winrm" | "local" | "unknown" | null {
  if (t.containerId) return "exec";
  const kind = t.sessionId ? sessions.find((s) => s.id === t.sessionId)?.kind : undefined;
  if (kind === "winrm") return "winrm";
  if (kind === "local") return "local";
  if (kind) return null;
  if (assetKind === "winrm") return "winrm";
  if (assetKind === "local") return "local";
  if (assetKind) return null;
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
  const st = useUi.getState();
  const live = tabs.filter(isRunningTerminal);
  const cleanup = tabs.filter(
    (t) => t.kind === "terminal" && t.tabId && !t.dead && t.exited,
  );
  if (live.length === 0 && cleanup.length === 0) return true;
  const blocked = live.filter((t) => detachBlockOf(t, st.sessions, assetKind) !== null);
  const detachable = live.filter((t) => detachBlockOf(t, st.sessions, assetKind) === null);
  if (blocked.length > 0) {
    const { ask } = await import("../ui/dialogs");
    const kinds = [
      ...new Set(
        blocked
          .map((t) => detachBlockOf(t, st.sessions, assetKind))
          .filter((b): b is "exec" | "winrm" | "local" | "unknown" => b !== null),
      ),
    ].map(detachBlockLabel).join("、");
    const ok = await ask(
      `${scope}里有 ${blocked.length} 个${kinds}终端，不支持转入后台。\n关闭会结束这些进程，无法恢复。仍要继续？`,
      { title, kind: "warning" },
    );
    if (!ok) return false;
  }
  const results = await Promise.allSettled([
    ...detachable.map((t) => terminalApi.closeTab(t.tabId as string, "detach")),
    ...blocked.map((t) => terminalApi.closeTab(t.tabId as string, "kill")),
    ...cleanup.map((t) => terminalApi.closeTab(t.tabId as string, "kill")),
  ]);
  const failed = results.filter((r) => r.status === "rejected");
  const { pushToast } = useUi.getState();
  if (failed.length) {
    pushToast(
      "error",
      `${failed.length}/${live.length + cleanup.length} 个终端关闭失败：${describeError((failed[0] as PromiseRejectedResult).reason)}`,
    );
  }
  const detachedOk = results.filter((r, i) => r.status === "fulfilled" && i < detachable.length).length;
  const blockedOk = results.filter(
    (r, i) =>
      r.status === "fulfilled" && i >= detachable.length && i < detachable.length + blocked.length,
  ).length;
  if (detachedOk) {
    pushToast("info", `${detachedOk} 个终端已转入后台，可在「后台会话」接管`);
  }
  if (blockedOk) {
    pushToast("info", `${blockedOk} 个不支持后台的终端已结束`);
  }
  return true;
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

interface WorkspaceCloseSummary {
  dirty: AppTab[];
  detachable: AppTab[];
  blocked: AppTab[];
  blockedKinds: string;
  cleanup: AppTab[];
  dbConnIds: string[];
}

interface WorkspaceCloseOutcome {
  detached: number;
  killed: number;
  failed: number;
  failureText?: string;
  dbFailed: number;
  dbError?: string;
}

function collectWorkspaceClose(tabs: AppTab[], assetKind?: string): WorkspaceCloseSummary {
  const { sessions } = useUi.getState();
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
  return {
    dirty: dirtyFileEditors(tabs),
    detachable: live.filter((t) => detachBlockOf(t, sessions, assetKind) === null),
    blocked,
    blockedKinds,
    cleanup: tabs.filter((t) => t.kind === "terminal" && t.tabId && !t.dead && t.exited),
    dbConnIds: [
      ...new Set(tabs.filter((t) => t.kind === "db" && t.connId).map((t) => t.connId as string)),
    ],
  };
}

// confirmWorkspaceClose 把 dirty 编辑器与 blocked 终端合并成一次汇总确认;
// 取消时不发出任何 IPC, 也不关闭任何标签。
async function confirmWorkspaceClose(
  summary: WorkspaceCloseSummary,
  title: string,
): Promise<boolean> {
  if (summary.dirty.length === 0 && summary.blocked.length === 0) return true;
  const lines: string[] = [];
  if (summary.dirty.length > 0) {
    lines.push(
      `· ${summary.dirty.length} 个文件尚未保存，关闭后会丢失改动：${summary.dirty.map((t) => t.title).join("、")}`,
    );
  }
  if (summary.blocked.length > 0) {
    lines.push(`· ${summary.blocked.length} 个${summary.blockedKinds}终端不支持转入后台，将结束进程`);
  }
  if (summary.detachable.length > 0) {
    lines.push(`· ${summary.detachable.length} 个运行中的终端将转入后台`);
  }
  const { ask } = await import("../ui/dialogs");
  return ask(`${lines.join("\n")}\n仍要继续？`, { title: `关闭「${title}」`, kind: "warning" });
}

async function executeWorkspaceClose(summary: WorkspaceCloseSummary): Promise<WorkspaceCloseOutcome> {
  const results = await Promise.allSettled([
    ...summary.detachable.map((t) => terminalApi.closeTab(t.tabId as string, "detach")),
    ...summary.blocked.map((t) => terminalApi.closeTab(t.tabId as string, "kill")),
    ...summary.cleanup.map((t) => terminalApi.closeTab(t.tabId as string, "kill")),
  ]);
  const dbResults = await Promise.allSettled(summary.dbConnIds.map((connId) => dbApi.disconnect(connId)));
  const failed = results.filter((r): r is PromiseRejectedResult => r.status === "rejected");
  const dbFailed = dbResults.filter((r): r is PromiseRejectedResult => r.status === "rejected");
  const killStart = summary.detachable.length;
  return {
    detached: results.filter((r, i) => r.status === "fulfilled" && i < killStart).length,
    killed: results.filter(
      (r, i) => r.status === "fulfilled" && i >= killStart && i < killStart + summary.blocked.length,
    ).length,
    failed: failed.length,
    failureText: failed[0] ? describeError(failed[0].reason) : undefined,
    dbFailed: dbFailed.length,
    dbError: dbFailed[0] ? describeError(dbFailed[0].reason) : undefined,
  };
}

// pushWorkspaceCloseToast 一次关闭只发一条汇总 toast: 有失败发错误, 否则有终端动向才发一条信息。
function pushWorkspaceCloseToast(title: string, outcome: WorkspaceCloseOutcome): void {
  const { pushToast } = useUi.getState();
  const parts: string[] = [];
  if (outcome.detached > 0) parts.push(`${outcome.detached} 个终端转入后台，可在「后台会话」接管`);
  if (outcome.killed > 0) parts.push(`${outcome.killed} 个进程已结束`);
  if (outcome.failed > 0) {
    pushToast(
      "error",
      `关闭「${title}」：${outcome.failed} 个终端操作失败：${outcome.failureText}${
        parts.length > 0 ? `（${parts.join("，")}）` : ""
      }`,
    );
    return;
  }
  if (parts.length === 0) return;
  pushToast("info", `已关闭「${title}」：${parts.join("，")}`);
}

// reclaimWorkspaceTabs 汇总确认后收回标签占用(终端转后台/结束、数据库断开)但不关闭工作区;
// 供布局预设「应用到当前工作区」在重建分屏前复用同一套确认与收回语义。
export async function reclaimWorkspaceTabs(
  tabs: AppTab[],
  title: string,
  assetKind?: string,
): Promise<boolean> {
  const summary = collectWorkspaceClose(tabs, assetKind);
  if (!(await confirmWorkspaceClose(summary, title))) return false;
  const outcome = await executeWorkspaceClose(summary);
  if (outcome.dbFailed > 0) {
    useUi.getState().pushToast(
      "error",
      `「${title}」保持打开：${outcome.dbFailed}/${summary.dbConnIds.length} 个数据库断开失败：${outcome.dbError}`,
    );
    return false;
  }
  pushWorkspaceCloseToast(title, outcome);
  return true;
}

export function closeActionHint(t: AppTab): string | undefined {
  if (t.kind !== "terminal" || !t.tabId || t.dead || t.exited) return undefined;
  const block = detachBlockOf(t, useUi.getState().sessions, assetKindForTab(t));
  if (block === "exec") return "结束容器 exec 进程";
  if (block === "winrm") return "结束 WinRM 非交互进程";
  if (block === "local") return "结束本地进程";
  if (block === "unknown") return "结束进程";
  return "转入后台运行";
}

export function sessionStatusText(status: SessionInfo["status"] | undefined): string {
  if (status === "connected") return "已连接";
  if (status === "reconnecting") return "重连中";
  if (status === "connecting") return "连接中";
  if (status === "failed") return "连接失败";
  return "已断开";
}

export function closeTabHint(t: AppTab): string {
  if (t.kind !== "terminal" || !t.tabId || t.dead) return "关闭标签";
  if (t.exited) return "关闭标签（进程已结束）";
  const action = closeActionHint(t);
  return action ? `关闭标签（${action}）` : "关闭标签";
}

export async function requestCloseTab(id: string): Promise<void> {
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
    await st.closeTab(id, "kill");
    return;
  }
  const block = detachBlockOf(target, st.sessions, ws.assetKind);
  if (block) {
    const { ask } = await import("../ui/dialogs");
    const ok = await ask(
      `「${target.title}」是${detachBlockLabel(block)}终端，不支持转入后台。\n关闭会结束这个进程，仍要关闭？`,
      { title: `关闭「${target.title}」`, kind: "warning" },
    );
    if (!ok) return;
    await st.closeTab(id, "kill");
    return;
  }
  if (await st.closeTab(id, "detach")) {
    st.pushToast("info", `「${target.title}」已转入后台，可在「后台会话」接管`);
  }
}

export async function requestCloseTabs(ids: string[]): Promise<void> {
  for (const id of ids) {
    await requestCloseTab(id);
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
    live.map((t) => terminalApi.closeTab(t.tabId as string, "kill")),
  );
  const failed = results.filter((r) => r.status === "rejected").length;
  if (failed) {
    st.pushToast("error", `${failed}/${live.length} 个终端结束失败`);
  } else {
    st.pushToast("success", `已结束 ${live.length} 个终端进程`);
  }
  live.forEach((t, i) => {
    if (results[i].status === "fulfilled") st.updateTab(t.id, { exited: true });
  });
}

export function takePendingCommand(storeTabId: string): string | null {
  const st = useUi.getState();
  for (const w of st.workspaces) {
    for (const p of w.panes) {
      const tab = p.tabs.find((t) => t.id === storeTabId);
      if (tab?.pendingCommand) {
        const cmd = tab.pendingCommand;
        st.updateTab(storeTabId, { pendingCommand: undefined });
        return cmd;
      }
    }
  }
  return null;
}

export function cdCommandFor(path: string): string {
  if (/^[A-Za-z]:[\\/]/.test(path)) {
    return `cd '${path.replace(/'/g, "''")}'`;
  }
  return `cd '${path.replace(/'/g, "'\\''")}'`;
}

export function findWritableTerminal(sessionId: string): string | null {
  const st = useUi.getState();
  for (const w of st.workspaces) {
    if (w.sessionId !== sessionId) continue;
    for (const p of w.panes) {
      const ordered = [
        ...p.tabs.filter((t) => t.id === p.activeTabId),
        ...p.tabs.filter((t) => t.id !== p.activeTabId),
      ];
      for (const t of ordered) {
        if (t.kind === "terminal" && t.tabId && !t.dead && !t.exited) return t.tabId;
      }
    }
  }
  return null;
}

export function openFileTab(sessionId: string, path: string) {
  const name = path.split("/").filter(Boolean).pop() ?? path;
  useUi.getState().addTab(fileTabSpec(sessionId, path, name));
}

export function openFileTabInSplit(sessionId: string, path: string) {
  const name = path.split("/").filter(Boolean).pop() ?? path;
  useUi.getState().openInLowerPane(fileTabSpec(sessionId, path, name));
}

export function openLogTab(sessionId: string, path: string) {
  const name = path.split("/").filter(Boolean).pop() ?? path;
  useUi.getState().addTab({
    id: `log-${sessionId}-${path}`,
    kind: "log",
    title: name,
    sessionId,
    path,
    closable: true,
  });
}

function fileTabSpec(sessionId: string, path: string, name: string): AppTab {
  return {
    id: `file-${sessionId}-${path}`,
    kind: "files",
    title: name,
    sessionId,
    path,
    closable: true,
  };
}

function hasTab(id: string): boolean {
  return useUi
    .getState()
    .workspaces.some((w) => w.panes.some((p) => p.tabs.some((t) => t.id === id)));
}

export function openCredentialsSidebar() {
  const st = useUi.getState();
  st.setLeftMode("credentials");
  st.setLeftOpen(true);
}

export function openCredentialsTab(credId?: string) {
  const st = useUi.getState();
  if (credId === undefined) {
    if (hasTab("tab-credentials")) st.setActiveTab("tab-credentials");
    else st.addTab({ id: "tab-credentials", kind: "credentials", title: "凭据", closable: true });
    return;
  }
  st.addTab({
    id: "tab-credentials",
    kind: "credentials",
    title: "凭据",
    credId,
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

async function ensureVaultUnlocked(reason: string): Promise<boolean> {
  let st: VaultStatus;
  try {
    st = await vaultApi.status();
  } catch {
    return true;
  }
  if (!st.initialized || st.unlocked) return true;
  if (st.passwordless) {
    try {
      await vaultApi.unlock("");
      return true;
    } catch (e) {
      useUi.getState().pushToast("error", `解锁失败：${describeError(e)}`);
      return false;
    }
  }
  const { promptText } = await import("../ui/dialogs");
  const pwd = await promptText(reason, "", { secret: true });
  if (pwd === null) return false;
  try {
    await vaultApi.unlock(pwd);
    return true;
  } catch (e) {
    useUi.getState().pushToast("error", `解锁失败：${describeError(e)}`);
    return false;
  }
}

async function ensureVaultReadyFor(asset: { name: string; credId?: string | null }): Promise<boolean> {
  if (!asset.credId) return true;
  return ensureVaultUnlocked(`连接「${asset.name}」需要使用凭据，请先输入保护密码：`);
}

function sidebarsOverlayWorkspace(): boolean {
  if (typeof window === "undefined") return false;
  return workspaceViewport(window.innerWidth, false).overlaySidebars;
}

async function openConnectedAssetSession(
  info: SessionInfo,
  asset: { id: string; name: string; kind: string },
): Promise<void> {
  const {
    setSessions,
    sessions,
    addTab,
    ensureWorkspace,
    setLeftMode,
    setLeftOpen,
    bumpConnectFocusRevision,
  } = useUi.getState();
  setSessions([...sessions.filter((s) => s.id !== info.id), info]);
  ensureWorkspace({
    kind: "session",
    sessionId: info.id,
    title: info.name,
    assetId: asset.id,
    assetKind: asset.kind,
  });
  if (!sidebarsOverlayWorkspace()) {
    setLeftMode("files");
    setLeftOpen(true);
  }
  bumpConnectFocusRevision();
  if (asset.kind === "docker") {
    addTab({
      id: nextTabId(`docker-${info.id}`),
      kind: "docker",
      title: "容器",
      sessionId: info.id,
      closable: true,
    });
    return;
  }
  await openTerminalTab(info);
}

export interface ConnectAssetInput {
  id: string;
  name: string;
  kind: string;
  credId?: string | null;
}

export interface ConnectAssetOptions {
  silent?: boolean;
}

// connectAssetSession 只建立会话并登记进会话列表, 不打开任何标签;
// 供布局预设恢复等「先连上再按预设摆标签」的入口使用。
export async function connectAssetSession(
  asset: ConnectAssetInput,
): Promise<SessionInfo | null> {
  const { pushToast } = useUi.getState();
  try {
    if (!(await ensureVaultReadyFor(asset))) return null;
    const info = await connectWithHostKeyConfirm(() => sessionApi.connect(asset.id));
    if (!info) {
      pushToast("info", "已取消连接");
      return null;
    }
    const { sessions, setSessions } = useUi.getState();
    setSessions([...sessions.filter((s) => s.id !== info.id), info]);
    return info;
  } catch (e) {
    const err = e as { message?: string };
    pushToast("error", err.message || describeError(e));
    return null;
  }
}

// connectDbAsset 建立数据库连接并返回 connId, 不打开标签; 布局预设恢复数据库标签时使用。
export async function connectDbAsset(asset: ConnectAssetInput): Promise<string | null> {
  try {
    if (!(await ensureVaultReadyFor(asset))) return null;
    const { connId } = await dbApi.connect(asset.id);
    return connId;
  } catch (e) {
    useUi.getState().pushToast("error", describeError(e));
    return null;
  }
}

export type ConnectOutcome =
  | { ok: true }
  | { ok: false; error?: string; canceled?: boolean };

const inflightConnects = new Map<string, Promise<ConnectOutcome>>();

function setAssetConnecting(assetId: string, connecting: boolean): void {
  useUi.setState((s) => ({
    connectingAssetIds: connecting
      ? s.connectingAssetIds.includes(assetId)
        ? s.connectingAssetIds
        : [...s.connectingAssetIds, assetId]
      : s.connectingAssetIds.filter((id) => id !== assetId),
  }));
}

export function isAssetConnecting(assetId: string): boolean {
  return useUi.getState().connectingAssetIds.includes(assetId);
}

export async function connectAsset(
  asset: ConnectAssetInput,
  options?: ConnectAssetOptions,
): Promise<ConnectOutcome> {
  const inflight = inflightConnects.get(asset.id);
  if (inflight) return inflight;
  const promise = runConnectAsset(asset, options).finally(() => {
    inflightConnects.delete(asset.id);
    setAssetConnecting(asset.id, false);
  });
  inflightConnects.set(asset.id, promise);
  setAssetConnecting(asset.id, true);
  return promise;
}

const LIVE_SESSION_STATUSES = new Set(["connecting", "connected", "reconnecting"]);

async function runConnectAsset(
  asset: ConnectAssetInput,
  options?: ConnectAssetOptions,
): Promise<ConnectOutcome> {
  const { pushToast } = useUi.getState();

  const existing = useUi.getState().workspaces.find((w) => w.assetId === asset.id);
  if (existing) {
    const session = useUi.getState().sessions.find((s) => s.id === existing.sessionId);
    const live =
      existing.kind === "db" ||
      (session !== undefined && LIVE_SESSION_STATUSES.has(session.status));
    if (live) {
      useUi.getState().setActiveWorkspace(existing.id);
      return { ok: true };
    }
    if (session) {
      if (!(await ensureVaultReadyFor(asset))) return { ok: false, canceled: true };
      try {
        const fresh = await reconnectSessionAndWait(session);
        if (!fresh) {
          pushToast("info", "已取消连接");
          return { ok: false, canceled: true };
        }
        const { sessions, setSessions } = useUi.getState();
        setSessions([...sessions.filter((s) => s.id !== fresh.id), fresh]);
        useUi.getState().setActiveWorkspace(existing.id);
        useConnectHistory.getState().record(asset.id);
        return { ok: true };
      } catch {
        // 会话已在服务端删除或重连失败: 落到下方清残留工作区走全新连接
      }
    }
    await useUi.getState().closeWorkspace(existing.id);
    if (useUi.getState().workspaces.some((w) => w.id === existing.id)) {
      return { ok: false, canceled: true };
    }
  }

  const dbKind = dbKindOf(asset.kind);
  if (dbKind) {
    try {
      if (!(await ensureVaultReadyFor(asset))) return { ok: false, canceled: true };
      const { connId } = await dbApi.connect(asset.id);
      const title = `${asset.name} · ${DB_KIND_LABEL[dbKind]}`;
      const { addTab, ensureWorkspace } = useUi.getState();
      ensureWorkspace({ kind: "db", connId, dbKind, title, assetId: asset.id });
      addTab({
        id: nextTabId(`db-${connId}`),
        kind: "db",
        title: DB_KIND_LABEL[dbKind],
        connId,
        dbKind,
        closable: true,
      });
      useConnectHistory.getState().record(asset.id);
      return { ok: true };
    } catch (e) {
      if (!options?.silent) pushToast("error", describeError(e));
      return { ok: false, error: describeError(e) };
    }
  }

  try {
    if (!(await ensureVaultReadyFor(asset))) return { ok: false, canceled: true };
    const info = await connectWithHostKeyConfirm(() => sessionApi.connect(asset.id));
    if (!info) {
      pushToast("info", "已取消连接");
      return { ok: false, canceled: true };
    }
    await openConnectedAssetSession(info, asset);
    useConnectHistory.getState().record(asset.id);
    return { ok: true };
  } catch (e) {
    const err = e as { message?: string };
    const message = err.message || describeError(e);
    if (!options?.silent) pushToast("error", message);
    return { ok: false, error: message };
  }
}

const inflightQuickConnects = new Map<string, Promise<ConnectOutcome>>();

async function promptQuickConnectPassword(display: string): Promise<string | null> {
  const { promptText } = await import("../ui/dialogs");
  return promptText(`输入 ${display} 的登录密码（留空则使用 SSH agent）`, "", {
    secret: true,
  });
}

export function connectQuickTarget(
  target: QuickConnectTarget,
  options?: ConnectAssetOptions,
): Promise<ConnectOutcome> {
  const key = formatQuickConnectTarget(target);
  const inflight = inflightQuickConnects.get(key);
  if (inflight) return inflight;
  const promise = runQuickConnectTarget(target, options).finally(() => {
    inflightQuickConnects.delete(key);
  });
  inflightQuickConnects.set(key, promise);
  return promise;
}

async function runQuickConnectTarget(
  target: QuickConnectTarget,
  options?: ConnectAssetOptions,
): Promise<ConnectOutcome> {
  const { pushToast } = useUi.getState();
  const display = formatQuickConnectTarget(target);
  const password = await promptQuickConnectPassword(display);
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
    );
    if (!info) {
      pushToast("info", "已取消连接");
      return { ok: false, canceled: true };
    }
    await openConnectedAssetSession(info, { id: "", name: info.name, kind: "ssh" });
    return { ok: true };
  } catch (e) {
    const err = e as { message?: string };
    const message = err.message || describeError(e);
    if (!options?.silent) pushToast("error", message);
    return { ok: false, error: message };
  }
}

export async function saveQuickConnectAsset(
  target: QuickConnectTarget,
  options?: ConnectAssetOptions,
): Promise<ConnectOutcome> {
  const { pushToast } = useUi.getState();
  const display = formatQuickConnectTarget(target);
  const password = await promptQuickConnectPassword(display);
  if (password === null) return { ok: false, canceled: true };
  try {
    // 服务端装配禁用了 session_quick_connect_user: web 模式下不回填服务器 OS 用户。
    const username = target.username ?? (DESKTOP ? await sessionApi.quickConnectDefaultUser() : "");
    let credId: string | null = null;
    if (password) {
      if (!(await ensureVaultUnlocked("保存凭据前需要解锁凭据库"))) {
        return { ok: false, canceled: true };
      }
      const res = await vaultApi.setCredential(display, "password", password);
      credId = res.id;
    }
    let created: Awaited<ReturnType<typeof assetApi.create>>;
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
    } catch (e) {
      if (credId) await vaultApi.deleteCredential(credId).catch(() => undefined);
      throw e;
    }
    return await connectAsset(created, options);
  } catch (e) {
    const err = e as { message?: string };
    const message = err.message || describeError(e);
    if (!options?.silent) pushToast("error", message);
    return { ok: false, error: message };
  }
}

const inflightReconnects = new Map<string, Promise<SessionInfo | null>>();

// SESSION_RECONNECT_WAIT_MS 对齐后端重连生命周期: internal/session 默认 10 次尝试、
// 退避 1/2/4/8/16/30s(合计 181s), 每次拨号 SSH 默认 15s, 6 分钟覆盖最坏情况;
// connected/failed 终态会提前结束等待, 超时只是兜底。
export const SESSION_RECONNECT_WAIT_MS = 360_000;

export function isSessionReconnectPending(sessionId: string): boolean {
  return inflightReconnects.has(sessionId);
}

export function reconnectSessionAndWait(
  session: SessionInfo,
  options?: { timeoutMs?: number; intervalMs?: number },
): Promise<SessionInfo | null> {
  const inflight = inflightReconnects.get(session.id);
  if (inflight) return inflight;
  const promise = runReconnectAndWait(
    session,
    options?.timeoutMs ?? SESSION_RECONNECT_WAIT_MS,
    options?.intervalMs ?? 600,
  ).finally(() => {
    inflightReconnects.delete(session.id);
  });
  inflightReconnects.set(session.id, promise);
  return promise;
}

async function runReconnectAndWait(
  session: SessionInfo,
  timeoutMs: number,
  intervalMs: number,
): Promise<SessionInfo | null> {
  if (!(await confirmHostKeyIfNeeded(session.assetId ?? "", session.kind))) return null;
  const started = await sessionApi.reconnect(session.id);
  if (!started) throw new Error("重连未能启动");
  const deadline = Date.now() + timeoutMs;
  // session_reconnect 先返回、后端协程后置 reconnecting, 首次 list 可能读到启动前的
  // 旧 disconnected/failed; 只有见过 connecting/reconnecting 之后的终态才算失败。
  let enteredReconnect = false;
  for (;;) {
    const list = await sessionApi.list();
    const current = list.find((s) => s.id === session.id);
    if (current?.status === "connected") return current;
    if (current?.status === "connecting" || current?.status === "reconnecting") {
      enteredReconnect = true;
    } else if (current && enteredReconnect) {
      throw new Error(`会话${sessionStatusText(current.status)}`);
    }
    if (Date.now() >= deadline) throw new Error("重连超时，请检查网络后重试");
    await new Promise((resolve) => setTimeout(resolve, intervalMs));
  }
}
