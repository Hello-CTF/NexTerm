import { create } from "zustand";
import { assetApi, dbApi, sessionApi, terminalApi, vaultApi, type SessionInfo } from "../ipc/commands";
import { describeError } from "../ui/errorText";
import { dirtyFileEditors } from "../features/files/editorGuards";
import { splitAllowedForHeight } from "../features/terminal/workspaceLayout";
import {
  getResolvedTheme,
  getThemeMode,
  initTheme,
  setThemeMode as persistThemeMode,
  subscribeTheme,
  type ResolvedTheme,
  type ThemeMode,
} from "./theme";

export type PaneKind =
  | "terminal"
  | "files"
  | "mount"
  | "forward"
  | "docker"
  | "db"
  | "credentials"
  | "credentialsText"
  | "settings"
  | "audit"
  | "background";

export type LeftMode = "assets" | "files" | "credentials";

export interface AppTab {
  id: string;
  kind: PaneKind;
  title: string;
  sessionId?: string;
  tabId?: string;
  containerId?: string;
  connId?: string;
  dbKind?: "mysql" | "redis";
  credId?: string;
  credView?: "text" | "json";
  path?: string;
  pendingCommand?: string;
  closable: boolean;
  dead?: boolean;
  exited?: boolean;
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
  dbKind?: "mysql" | "redis";
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
  dbKind?: "mysql" | "redis";
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

interface UiState {
  leftOpen: boolean;
  leftMode: LeftMode;
  rightOpen: boolean;
  themeMode: ThemeMode;
  resolvedTheme: ResolvedTheme;
  setThemeMode: (m: ThemeMode) => void;
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
  sessions: SessionInfo[];
  aiBusy: boolean;
  takeover: TakeoverState | null;
  setTakeover: (t: TakeoverState | null) => void;
  modelProfilesRevision: number;
  bumpModelProfilesRevision: () => void;
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

  setSessions: (s: SessionInfo[]) => void;
  setAiBusy: (v: boolean) => void;
  pushToast: (kind: ToastItem["kind"], text: string) => void;
  dismissToast: (id: number) => void;
}

let toastSeq = 1;
let tabSeq = 1;

const LEFT_MIN = 180;
const LEFT_MAX = 560;
const RIGHT_MIN = 300;
const RIGHT_MAX = 760;
const LEFT_DEFAULT = 248;
const RIGHT_DEFAULT = 352;
const LAYOUT_KEY = "nexterm.layout.v1";

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

export function nextTabId(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}-${tabSeq++}`;
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
  sessions: [],
  aiBusy: false,
  takeover: null,
  modelProfilesRevision: 0,
  bumpModelProfilesRevision: () =>
    set((state) => ({ modelProfilesRevision: state.modelProfilesRevision + 1 })),
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

  ensureWorkspace: (spec) => {
    const { workspaces, activeWorkspaceId } = get();
    const exists = workspaces.find(
      (w) =>
        (spec.sessionId !== undefined && w.sessionId === spec.sessionId) ||
        (spec.connId !== undefined && w.connId === spec.connId),
    );
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
    if (!(await confirmDirtyEditors(tabs, `关闭「${target.title}」`))) return;
    if (!(await reclaimTerminals(tabs, "这个工作区", `关闭「${target.title}」`, target.assetKind))) return;
    const next = workspaces.filter((w) => w.id !== id);
    set({
      workspaces: next,
      activeWorkspaceId:
        activeWorkspaceId === id ? (next.length ? next[next.length - 1].id : null) : activeWorkspaceId,
    });
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
    }
    set((s2) => ({
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
    }));
    return true;
  },

  updateTab: (id, patch) =>
    set((st) => ({
      workspaces: st.workspaces.map((w) => ({
        ...w,
        panes: w.panes.map((p) =>
          p.tabs.some((t) => t.id === id)
            ? { ...p, tabs: p.tabs.map((t) => (t.id === id ? { ...t, ...patch } : t)) }
            : p,
        ),
      })),
    })),

  setSessions: (s) => set({ sessions: s }),
  setAiBusy: (v) => set({ aiBusy: v }),
  setTakeover: (t) => set({ takeover: t }),

  pushToast: (kind, text) => {
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

function isRunningTerminal(t: AppTab): boolean {
  return t.kind === "terminal" && Boolean(t.tabId) && !t.dead && !t.exited;
}

function detachBlockOf(
  t: AppTab,
  sessions: SessionInfo[],
  assetKind?: string,
): "exec" | "winrm" | "unknown" | null {
  if (t.containerId) return "exec";
  const kind = t.sessionId ? sessions.find((s) => s.id === t.sessionId)?.kind : undefined;
  if (kind === "winrm") return "winrm";
  if (kind) return null;
  if (assetKind === "winrm") return "winrm";
  if (assetKind) return null;
  return "unknown";
}

function detachBlockLabel(block: "exec" | "winrm" | "unknown"): string {
  if (block === "exec") return "容器 exec";
  if (block === "winrm") return "WinRM 非交互";
  return "类型未知";
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
          .filter((b): b is "exec" | "winrm" | "unknown" => b !== null),
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
      `${failed.length}/${live.length + cleanup.length} 个终端回收失败：${describeError((failed[0] as PromiseRejectedResult).reason)}`,
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

export function closeActionHint(t: AppTab): string | undefined {
  if (t.kind !== "terminal" || !t.tabId || t.dead || t.exited) return undefined;
  const block = detachBlockOf(t, useUi.getState().sessions, assetKindForTab(t));
  if (block === "exec") return "结束容器 exec 进程";
  if (block === "winrm") return "结束 WinRM 非交互进程";
  if (block === "unknown") return "结束进程（类型未知）";
  return "转入后台运行";
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
        if (t.kind === "terminal" && t.tabId) return t.tabId;
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

export function openCredentialsViewTab(view: "text" | "json" = "text") {
  useUi.getState().addTab({
    id: "tab-credentials-view",
    kind: "credentialsText",
    title: "凭据视图",
    credView: view,
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

async function ensureVaultReadyFor(asset: { name: string; credId?: string | null }): Promise<boolean> {
  if (!asset.credId) return true;
  try {
    const st = await vaultApi.status();
    if (!st.initialized || st.unlocked) return true;
    const { promptText } = await import("../ui/dialogs");
    const pwd = await promptText(
      `连接「${asset.name}」需要使用凭据，请先输入保护密码：`,
      "",
      { secret: true },
    );
    if (pwd === null) return false;
    await vaultApi.unlock(pwd);
    return true;
  } catch {
    return true;
  }
}

async function openConnectedAssetSession(
  info: SessionInfo,
  asset: { id: string; name: string; kind: string },
): Promise<void> {
  const { setSessions, sessions, addTab, ensureWorkspace, setLeftMode, setLeftOpen } =
    useUi.getState();
  setSessions([...sessions.filter((s) => s.id !== info.id), info]);
  ensureWorkspace({
    kind: "session",
    sessionId: info.id,
    title: info.name,
    assetId: asset.id,
    assetKind: asset.kind,
  });
  setLeftMode("files");
  setLeftOpen(true);
  if (asset.kind === "docker") {
    addTab({
      id: nextTabId(`docker-${info.id}`),
      kind: "docker",
      title: `容器 · ${info.name}`,
      sessionId: info.id,
      closable: true,
    });
    return;
  }
  await openTerminalTab(info);
}

type hostKeyPendingDetail = {
  host?: string;
  port?: number;
  keyType?: string;
  fingerprint?: string;
  changed?: boolean;
  known?: { keyType?: string; fingerprint?: string }[];
};

function hostKeyQuestion(detail: hostKeyPendingDetail | undefined): string {
  if (detail?.changed) {
    const previous = (detail.known ?? [])
      .map((known) => known.fingerprint)
      .filter((fingerprint): fingerprint is string => Boolean(fingerprint))
      .join("、");
    return `主机密钥已变更 ${detail.host}:${detail.port}\n原指纹(SHA256): ${previous}\n新指纹(SHA256): ${detail.fingerprint}\n主机密钥类型: ${detail.keyType}\n信任新指纹并继续？如非预期变更，请取消并核实服务器。`;
  }
  return `首次连接 ${detail?.host}:${detail?.port}\n主机密钥类型: ${detail?.keyType}\n指纹(SHA256): ${detail?.fingerprint}\n信任并继续？`;
}

export async function connectAsset(asset: {
  id: string;
  name: string;
  kind: string;
  credId?: string | null;
}): Promise<void> {
  const { pushToast } = useUi.getState();

  if (asset.kind === "mysql" || asset.kind === "redis") {
    try {
      if (!(await ensureVaultReadyFor(asset))) return;
      const { connId } = await dbApi.connect(asset.id);
      const kind = asset.kind === "redis" ? "redis" : "mysql";
      const title = `${asset.name} · ${kind === "mysql" ? "SQL" : "Redis"}`;
      const { addTab, ensureWorkspace } = useUi.getState();
      ensureWorkspace({ kind: "db", connId, dbKind: kind, title });
      addTab({
        id: nextTabId(`db-${connId}`),
        kind: "db",
        title,
        connId,
        dbKind: kind,
        closable: true,
      });
    } catch (e) {
      pushToast("error", describeError(e));
    }
    return;
  }

  try {
    if (!(await ensureVaultReadyFor(asset))) return;
    const info = await sessionApi.connect(asset.id);
    await openConnectedAssetSession(info, asset);
  } catch (e) {
    const err = e as { code?: string; message: string; detail?: Record<string, unknown> };
    if (err.code === "host_key_pending") {
      const detail = err.detail as hostKeyPendingDetail | undefined;
      const { ask } = await import("../ui/dialogs");
      const ok = await ask(hostKeyQuestion(detail), {
        title: detail?.changed ? "主机密钥变更警告" : "确认主机指纹",
        kind: "warning",
      });
      if (!ok) {
        pushToast("info", "已取消连接");
        return;
      }
      if (!detail?.host || !detail?.port || !detail?.keyType || !detail?.fingerprint) {
        pushToast("error", err.message || describeError(e));
        return;
      }
      try {
        await assetApi.knownHostAccept(detail.host, detail.port, detail.keyType, detail.fingerprint);
        const info = await sessionApi.connect(asset.id);
        await openConnectedAssetSession(info, asset);
        return;
      } catch (e2) {
        pushToast("error", describeError(e2));
        return;
      }
    }
    pushToast("error", err.message || describeError(e));
  }
}
