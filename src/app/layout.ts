import { layoutApi, terminalApi } from "../ipc/commands";
import { listenEvent } from "../ipc/events";
import { describeError } from "../ui/errorText";
import { isDirtyFileEditor } from "../features/files/editorGuards";
import {
  GLOBAL_AI_BOARD_KEY,
  LEFT_WIDTH_RANGE,
  RIGHT_WIDTH_RANGE,
  dbKindOf,
  isToolTabKind,
  migrateToolTabs,
  nextTabId,
  useUi,
  type AiBoard,
  type AiDockTab,
  type AppTab,
  type LeftMode,
  type Pane,
  type PaneKind,
  type Workspace,
  type WorkspaceKind,
  type WorkspaceRuntime,
} from "./store";

const LAYOUT_CHANGED = "layout://changed";

const SAVE_DEBOUNCE_MS = 600;

const RETRY_MS = 3000;
const MAX_RETRY = 3;

const LAYOUT_VERSION = 2;

export interface PersistedLayout {
  v: number;
  leftOpen: boolean;
  leftMode: LeftMode;
  rightOpen: boolean;
  leftWidth: number;
  rightWidth: number;
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
  aiBoards: Record<string, AiBoard>;
}

const PANE_KINDS = new Set<PaneKind>([
  "terminal",
  "files",
  "log",
  "mount",
  "forward",
  "docker",
  "db",
  "credentials",
  "settings",
  "audit",
  "devices",
  "background",
  "history",
]);

function isObj(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}
function optStr(v: unknown): string | undefined {
  return typeof v === "string" && v.length > 0 ? v : undefined;
}
function boolOr(v: unknown, d: boolean): boolean {
  return typeof v === "boolean" ? v : d;
}
function clampNum(v: unknown, min: number, max: number, d: number): number {
  if (typeof v !== "number" || !Number.isFinite(v)) return d;
  return Math.min(max, Math.max(min, Math.round(v)));
}
function clampFraction(v: unknown, min: number, max: number, d: number): number {
  if (typeof v !== "number" || !Number.isFinite(v)) return d;
  return Math.min(max, Math.max(min, v));
}

function sanitizeTab(raw: unknown): AppTab | null {
  if (!isObj(raw)) return null;
  const id = optStr(raw.id);
  const kind = raw.kind;
  if (!id || typeof kind !== "string" || !PANE_KINDS.has(kind as PaneKind)) return null;
  const tabId = optStr(raw.tabId);
  if (kind === "terminal" && !tabId) return null;
  return {
    id,
    kind: kind as PaneKind,
    title: typeof raw.title === "string" ? raw.title : "标签",
    sessionId: optStr(raw.sessionId),
    assetId: optStr(raw.assetId),
    tabId,
    containerId: optStr(raw.containerId),
    connId: optStr(raw.connId),
    dbKind: dbKindOf(typeof raw.dbKind === "string" ? raw.dbKind : "") ?? undefined,
    credId: optStr(raw.credId),
    path: optStr(raw.path),
    closable: boolOr(raw.closable, true),
    exited: raw.exited === true ? true : undefined,
    renamedByUser: raw.renamedByUser === true ? true : undefined,
  };
}

function sanitizePane(raw: unknown): Pane | null {
  if (!isObj(raw)) return null;
  const id = optStr(raw.id);
  if (!id) return null;
  const tabs = Array.isArray(raw.tabs)
    ? raw.tabs.map(sanitizeTab).filter((t): t is AppTab => t !== null)
    : [];
  const active = optStr(raw.activeTabId);
  const activeTabId =
    active && tabs.some((t) => t.id === active) ? active : (tabs[tabs.length - 1]?.id ?? null);
  return { id, tabs, activeTabId };
}

function sanitizeWorkspace(raw: unknown): Workspace | null {
  if (!isObj(raw)) return null;
  const id = optStr(raw.id);
  const kind = raw.kind;
  if (!id || (kind !== "session" && kind !== "db" && kind !== "tools")) return null;
  const parsed = Array.isArray(raw.panes)
    ? raw.panes.map(sanitizePane).filter((p): p is Pane => p !== null)
    : [];
  const nonEmpty = parsed.filter((p) => p.tabs.length > 0);
  let panes =
    kind === "tools" ? parsed : nonEmpty.length > 0 ? nonEmpty : parsed.slice(0, 1);
  if (panes.length === 0) panes = [{ id: nextTabId("pane"), tabs: [], activeTabId: null }];
  const activePaneId = panes.some((p) => p.id === raw.activePaneId)
    ? (raw.activePaneId as string)
    : panes[0].id;
  return {
    id,
    kind: kind as WorkspaceKind,
    title: typeof raw.title === "string" ? raw.title : "工作区",
    sessionId: optStr(raw.sessionId),
    assetId: optStr(raw.assetId),
    connId: optStr(raw.connId),
    dbKind: dbKindOf(typeof raw.dbKind === "string" ? raw.dbKind : "") ?? undefined,
    assetKind: optStr(raw.assetKind),
    panes,
    activePaneId,
    splitRatio: clampFraction(raw.splitRatio, 0.15, 0.85, 0.5),
    closable: boolOr(raw.closable, true),
  };
}

function sanitizeAiDockTab(raw: unknown): AiDockTab | null {
  if (!isObj(raw)) return null;
  const id = optStr(raw.id);
  if (!id) return null;
  const title = typeof raw.title === "string" ? raw.title.trim().slice(0, 80) : "";
  return { id, title: title || "新会话", conversationId: optStr(raw.conversationId) };
}

function sanitizeAiBoard(raw: unknown): AiBoard | null {
  if (!isObj(raw) || !Array.isArray(raw.tabs)) return null;
  const tabs = raw.tabs.map(sanitizeAiDockTab).filter((t): t is AiDockTab => t !== null);
  if (tabs.length === 0) return null;
  const active = optStr(raw.activeTabId);
  return {
    tabs,
    activeTabId: active && tabs.some((t) => t.id === active) ? active : tabs[tabs.length - 1].id,
  };
}

function sanitizeAiBoards(
  raw: Record<string, unknown>,
  workspaces: Workspace[],
): Record<string, AiBoard> {
  const wsIds = new Set(workspaces.map((w) => w.id));
  const out: Record<string, AiBoard> = {};
  for (const [key, value] of Object.entries(raw)) {
    if (key !== GLOBAL_AI_BOARD_KEY && !wsIds.has(key)) continue;
    const board = sanitizeAiBoard(value);
    if (board) out[key] = board;
  }
  return out;
}

interface BackgroundCandidate {
  tab: AppTab;
  target: Workspace;
  sourceWorkspaceId: string;
}

function resolveBackgroundTarget(tab: AppTab, workspaces: Workspace[]): Workspace | null {
  const hosts = workspaces.filter((w) => w.kind === "session");
  if (tab.sessionId) {
    const matches = hosts.filter((w) => w.sessionId === tab.sessionId);
    if (matches.length !== 1) return null;
    const target = matches[0];
    if (tab.assetId && target.assetId !== tab.assetId) {
      const assetMatches = hosts.filter((w) => w.assetId === tab.assetId);
      if (target.assetId && target.assetId !== tab.assetId) return null;
      if (
        assetMatches.length > 1 ||
        (assetMatches.length === 1 && assetMatches[0] !== target)
      ) {
        return null;
      }
    }
    return target;
  }
  if (!tab.assetId) return null;
  const matches = hosts.filter((w) => w.assetId === tab.assetId);
  return matches.length === 1 && matches[0].sessionId ? matches[0] : null;
}

function normalizeChangedWorkspace(w: Workspace): Workspace {
  const withActiveTabs = w.panes.map((pane) => ({
    ...pane,
    activeTabId:
      pane.activeTabId && pane.tabs.some((tab) => tab.id === pane.activeTabId)
        ? pane.activeTabId
        : (pane.tabs[pane.tabs.length - 1]?.id ?? null),
  }));
  const nonEmpty = withActiveTabs.filter((pane) => pane.tabs.length > 0);
  let panes: Pane[] = nonEmpty.length > 0 ? nonEmpty : withActiveTabs.slice(0, 1);
  if (panes.length === 0) panes = [{ id: nextTabId("pane"), tabs: [], activeTabId: null }];
  return {
    ...w,
    panes,
    activePaneId: panes.some((pane) => pane.id === w.activePaneId)
      ? w.activePaneId
      : panes[0].id,
  };
}

function backgroundNoticeKey(tab: AppTab): string {
  return JSON.stringify([tab.id, tab.sessionId ?? null, tab.assetId ?? null]);
}

function normalizeBackgroundTabs(workspaces: Workspace[]): {
  workspaces: Workspace[];
  targets: Map<AppTab, Workspace>;
  dropped: string[];
} {
  const candidates: BackgroundCandidate[] = [];
  const targets = new Map<AppTab, Workspace>();
  const dropped: string[] = [];
  for (const w of workspaces) {
    for (const pane of w.panes) {
      for (const tab of pane.tabs) {
        if (tab.kind !== "background") continue;
        const target = resolveBackgroundTarget(tab, workspaces);
        if (!target) {
          const key = backgroundNoticeKey(tab);
          if (!dropped.includes(key)) dropped.push(key);
          continue;
        }
        candidates.push({ tab, target, sourceWorkspaceId: w.id });
        targets.set(tab, target);
      }
    }
  }
  if (candidates.length === 0 && dropped.length === 0) return { workspaces, targets, dropped };

  const selected = new Map<string, BackgroundCandidate>();
  for (const candidate of candidates) {
    const current = selected.get(candidate.target.id);
    if (
      !current ||
      (current.sourceWorkspaceId !== candidate.target.id &&
        candidate.sourceWorkspaceId === candidate.target.id)
    ) {
      selected.set(candidate.target.id, candidate);
    }
  }
  const replacements = new Map<AppTab, { targetId: string; tab: AppTab }>();
  for (const candidate of selected.values()) {
    replacements.set(candidate.tab, {
      targetId: candidate.target.id,
      tab: {
        ...candidate.tab,
        id: `background-${candidate.target.id}`,
        sessionId: candidate.target.sessionId,
        ...(candidate.target.assetId ? { assetId: candidate.target.assetId } : {}),
      },
    });
  }

  const placed = new Set<AppTab>();
  const next = workspaces.map((w) => {
    let workspaceChanged = false;
    const panes = w.panes.map((pane) => {
      let changed = false;
      let activeTabId = pane.activeTabId;
      const tabs: AppTab[] = [];
      for (const tab of pane.tabs) {
        if (tab.kind !== "background") {
          tabs.push(tab);
          continue;
        }
        changed = true;
        workspaceChanged = true;
        const replacement = replacements.get(tab);
        if (!replacement || replacement.targetId !== w.id) continue;
        tabs.push(replacement.tab);
        placed.add(tab);
        if (activeTabId === tab.id) activeTabId = replacement.tab.id;
      }
      return changed ? { ...pane, tabs, activeTabId } : pane;
    });
    return workspaceChanged ? normalizeChangedWorkspace({ ...w, panes }) : w;
  });

  for (const candidate of selected.values()) {
    if (placed.has(candidate.tab)) continue;
    const replacement = replacements.get(candidate.tab)?.tab;
    const index = next.findIndex((w) => w.id === candidate.target.id);
    if (!replacement || index < 0) continue;
    const w = next[index];
    const paneId = w.panes.some((pane) => pane.id === w.activePaneId)
      ? w.activePaneId
      : w.panes[0].id;
    const panes = w.panes.map((pane) =>
      pane.id === paneId ? { ...pane, tabs: [...pane.tabs, replacement] } : pane,
    );
    next[index] = normalizeChangedWorkspace({ ...w, panes });
  }
  return { workspaces: next, targets, dropped };
}

interface TabLocation {
  workspace: Workspace;
  pane: Pane;
  tab: AppTab;
}

function findLayoutTab(
  workspaces: Workspace[],
  predicate: (tab: AppTab) => boolean,
  workspaceId?: string,
): TabLocation | null {
  for (const workspace of workspaces) {
    if (workspaceId !== undefined && workspace.id !== workspaceId) continue;
    for (const pane of workspace.panes) {
      const tab = pane.tabs.find(predicate);
      if (tab) return { workspace, pane, tab };
    }
  }
  return null;
}

function activateLayoutTab(workspaces: Workspace[], location: TabLocation): Workspace[] {
  return workspaces.map((w) =>
    w.id !== location.workspace.id
      ? w
      : {
          ...w,
          activePaneId: location.pane.id,
          panes: w.panes.map((pane) =>
            pane.id === location.pane.id ? { ...pane, activeTabId: location.tab.id } : pane,
          ),
        },
  );
}

const layoutMigrationNotices = new WeakMap<PersistedLayout, { droppedBackgrounds: string[] }>();
const notifiedInvalidBackgrounds = new Set<string>();

export function sanitizeLayout(raw: unknown): PersistedLayout | null {
  if (!isObj(raw)) return null;
  if (raw.v !== LAYOUT_VERSION) return null;
  if (!Array.isArray(raw.workspaces)) return null;
  if (!isObj(raw.aiBoards)) return null;
  const sanitized = raw.workspaces
    .map(sanitizeWorkspace)
    .filter((w): w is Workspace => w !== null);
  const activeWorkspace = sanitized.find((w) => w.id === raw.activeWorkspaceId);
  const activePane = activeWorkspace?.panes.find((pane) => pane.id === activeWorkspace.activePaneId);
  const activeTabs = activePane?.tabs.filter((tab) => tab.id === activePane.activeTabId) ?? [];
  const activeTab = activeTabs.length === 1 ? activeTabs[0] : undefined;
  const backgrounds = normalizeBackgroundTabs(sanitized);
  let workspaces = migrateToolTabs(backgrounds.workspaces, raw.aiBoards);

  let activeWorkspaceId: string | null = null;
  if (activeTab?.kind === "background") {
    const target = backgrounds.targets.get(activeTab);
    const location = target
      ? findLayoutTab(workspaces, (tab) => tab.id === `background-${target.id}`, target.id)
      : null;
    if (location) {
      activeWorkspaceId = location.workspace.id;
      workspaces = activateLayoutTab(workspaces, location);
    }
  } else if (activeTab && isToolTabKind(activeTab.kind)) {
    const location = findLayoutTab(workspaces, (tab) => tab.kind === activeTab.kind);
    if (location) {
      activeWorkspaceId = location.workspace.id;
      workspaces = activateLayoutTab(workspaces, location);
    }
  } else if (activeTab) {
    const original = findLayoutTab(workspaces, (tab) => tab.id === activeTab.id, activeWorkspace?.id);
    if (!original) {
      const moved = findLayoutTab(workspaces, (tab) => tab.id === activeTab.id);
      if (moved) {
        activeWorkspaceId = moved.workspace.id;
        workspaces = activateLayoutTab(workspaces, moved);
      }
    }
  }
  if (activeWorkspaceId === null) {
    const fallbackWorkspaceId = activeWorkspace?.kind === "tools"
      ? (workspaces.find((w) => w.kind === "tools")?.id ?? workspaces[0]?.id ?? null)
      : (workspaces[0]?.id ?? null);
    activeWorkspaceId = workspaces.some((w) => w.id === raw.activeWorkspaceId)
      ? (raw.activeWorkspaceId as string)
      : fallbackWorkspaceId;
  }

  const result: PersistedLayout = {
    v: LAYOUT_VERSION,
    leftOpen: boolOr(raw.leftOpen, true),
    leftMode:
      raw.leftMode === "files" || raw.leftMode === "credentials" ? raw.leftMode : "assets",
    rightOpen: boolOr(raw.rightOpen, true),
    leftWidth: clampNum(raw.leftWidth, LEFT_WIDTH_RANGE.min, LEFT_WIDTH_RANGE.max, LEFT_WIDTH_RANGE.default),
    rightWidth: clampNum(
      raw.rightWidth,
      RIGHT_WIDTH_RANGE.min,
      RIGHT_WIDTH_RANGE.max,
      RIGHT_WIDTH_RANGE.default,
    ),
    workspaces,
    activeWorkspaceId,
    aiBoards: sanitizeAiBoards(raw.aiBoards, workspaces),
  };
  layoutMigrationNotices.set(result, { droppedBackgrounds: backgrounds.dropped });
  return result;
}

interface LayoutSource {
  leftOpen: boolean;
  leftMode: LeftMode;
  rightOpen: boolean;
  leftWidth: number;
  rightWidth: number;
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
  aiBoards?: Record<string, AiBoard>;
}

export function serializeLayout(s: LayoutSource): PersistedLayout | null {
  return sanitizeLayout({
    v: LAYOUT_VERSION,
    leftOpen: s.leftOpen,
    leftMode: s.leftMode,
    rightOpen: s.rightOpen,
    leftWidth: s.leftWidth,
    rightWidth: s.rightWidth,
    workspaces: s.workspaces,
    activeWorkspaceId: s.activeWorkspaceId,
    aiBoards: s.aiBoards ?? {},
  });
}

let started = false;
let resolveBootstrap: () => void = () => {};
export const layoutBootstrapped: Promise<void> = new Promise((r) => {
  resolveBootstrap = r;
});

let accountEpoch = 0;
let revision = 0;
let lastSynced = "";
let applyingRemote = false;
let saveTimer: number | null = null;
let retryTimer: number | null = null;
let retryCount = 0;
let saveErrorNotified = false;
let fetchInFlight = false;
let writing = false;
let dirty = false;
let remoteEventDuringWrite = false;
let pendingRemoteRev: number | null = null;

function clearSaveTimer() {
  if (saveTimer !== null) {
    window.clearTimeout(saveTimer);
    saveTimer = null;
  }
}
function clearRetryTimer() {
  if (retryTimer !== null) {
    window.clearTimeout(retryTimer);
    retryTimer = null;
  }
}

function mergeLocalTabs(
  next: PersistedLayout,
  current: Workspace[],
  shouldMerge: (tab: AppTab) => boolean,
  restoreMissingWorkspaces: boolean,
): Workspace[] {
  const byWs = new Map<string, Map<string, AppTab[]>>();
  for (const w of current) {
    for (const p of w.panes) {
      for (const t of p.tabs) {
        if (!shouldMerge(t)) continue;
        let panes = byWs.get(w.id);
        if (!panes) {
          panes = new Map();
          byWs.set(w.id, panes);
        }
        const list = panes.get(p.id);
        if (list) list.push(t);
        else panes.set(p.id, [t]);
      }
    }
  }
  if (byWs.size === 0) return next.workspaces;

  let changed = false;
  const workspaces = next.workspaces.map((w) => {
    const paneMap = byWs.get(w.id);
    if (!paneMap) return w;
    let wsChanged = false;
    const panes = w.panes.map((p) => {
      const cand = paneMap.get(p.id);
      if (!cand) return p;
      const existing = new Set(p.tabs.map((t) => t.id));
      const add = cand.filter((t) => !existing.has(t.id));
      if (add.length === 0) return p;
      wsChanged = true;
      const tabs = [...p.tabs, ...add];
      const activeAlive = p.activeTabId !== null && tabs.some((t) => t.id === p.activeTabId);
      return {
        ...p,
        tabs,
        activeTabId: activeAlive ? p.activeTabId : add[add.length - 1].id,
      };
    });
    const missing: Pane[] = [];
    for (const [paneId, cand] of paneMap) {
      if (w.panes.some((p) => p.id === paneId)) continue;
      wsChanged = true;
      missing.push({ id: paneId, tabs: [...cand], activeTabId: cand[cand.length - 1].id });
    }
    if (!wsChanged) return w;
    changed = true;
    return { ...w, panes: missing.length > 0 ? [...panes, ...missing] : panes };
  });

  if (restoreMissingWorkspaces) {
    for (const [workspaceId, paneMap] of byWs) {
      if (next.workspaces.some((w) => w.id === workspaceId)) continue;
      const source = current.find((w) => w.id === workspaceId);
      if (!source) continue;
      const panes = [...paneMap].map(([id, tabs]) => ({
        id,
        tabs: [...tabs],
        activeTabId: tabs[tabs.length - 1].id,
      }));
      if (panes.length === 0) continue;
      changed = true;
      workspaces.push({
        ...source,
        panes,
        activePaneId: panes.some((p) => p.id === source.activePaneId)
          ? source.activePaneId
          : panes[0].id,
      });
    }
  }
  return changed ? workspaces : next.workspaces;
}

export function mergeDeadTabs(next: PersistedLayout, current: Workspace[]): Workspace[] {
  return mergeLocalTabs(next, current, (tab) => tab.kind === "terminal" && tab.dead === true, false);
}

export function mergeDirtyEditorTabs(next: PersistedLayout, current: Workspace[]): Workspace[] {
  return mergeLocalTabs(next, current, isDirtyFileEditor, true);
}

export function mergeExitedTabs(next: PersistedLayout, current: Workspace[]): Workspace[] {
  const exitedById = new Map<string, string>();
  for (const w of current) {
    for (const p of w.panes) {
      for (const t of p.tabs) {
        if (t.kind === "terminal" && t.exited === true && t.tabId) exitedById.set(t.id, t.tabId);
      }
    }
  }
  if (exitedById.size === 0) return next.workspaces;
  let changed = false;
  const workspaces = next.workspaces.map((w) => ({
    ...w,
    panes: w.panes.map((p) => ({
      ...p,
      tabs: p.tabs.map((t) => {
        if (
          t.kind === "terminal" &&
          !t.exited &&
          t.tabId &&
          exitedById.get(t.id) === t.tabId
        ) {
          changed = true;
          return { ...t, exited: true };
        }
        return t;
      }),
    })),
  }));
  return changed ? workspaces : next.workspaces;
}

function mergeTerminalRuntimeTabs(next: PersistedLayout, current: Workspace[]): Workspace[] {
  const currentById = new Map<string, AppTab>();
  for (const w of current) {
    for (const p of w.panes) {
      for (const t of p.tabs) {
        if (t.kind === "terminal" && t.tabId) currentById.set(t.id, t);
      }
    }
  }
  if (currentById.size === 0) return next.workspaces;
  let changed = false;
  const workspaces = next.workspaces.map((w) => ({
    ...w,
    panes: w.panes.map((p) => ({
      ...p,
      tabs: p.tabs.map((t) => {
        const existing = t.kind === "terminal" && t.tabId ? currentById.get(t.id) : undefined;
        if (!existing || existing.tabId !== t.tabId) return t;
        changed = true;
        return {
          ...t,
          shell: existing.shell,
          pendingDirectory: existing.pendingDirectory,
          dead: existing.dead,
          retainable: existing.retainable,
          unread: existing.unread,
          closeError: existing.closeError,
        };
      }),
    })),
  }));
  return changed ? workspaces : next.workspaces;
}

function mergeSqlDraftWorkspaces(
  next: PersistedLayout,
  current: Workspace[],
  runtime: Record<string, WorkspaceRuntime>,
): { workspaces: Workspace[]; workspaceRuntime: Record<string, WorkspaceRuntime> } {
  const mergedIds = new Set(next.workspaces.map((w) => w.id));
  const preserved = current.filter(
    (w) => !mergedIds.has(w.id) && Boolean(runtime[w.id]?.sqlDraft?.text.trim()),
  );
  const workspaces = preserved.length > 0 ? [...next.workspaces, ...preserved] : next.workspaces;
  const workspaceIds = new Set(workspaces.map((w) => w.id));
  const workspaceRuntime = Object.fromEntries(
    Object.entries(runtime).filter(
      ([id]) => workspaceIds.has(id) || Boolean(runtime[id]?.sqlDraft?.text.trim()),
    ),
  ) as Record<string, WorkspaceRuntime>;
  return { workspaces, workspaceRuntime };
}

function preserveAiDrafts(
  next: Record<string, AiBoard>,
  current: Record<string, AiBoard>,
): Record<string, AiBoard> {
  const out: Record<string, AiBoard> = {};
  for (const [key, board] of Object.entries(next)) {
    const currentBoard = current[key];
    out[key] = {
      ...board,
      tabs: board.tabs.map((tab) => {
        const currentTab = currentBoard?.tabs.find((candidate) => candidate.id === tab.id);
        if (!currentTab || currentTab.conversationId !== tab.conversationId || currentTab.draft === undefined) {
          return tab;
        }
        return { ...tab, draft: currentTab.draft };
      }),
    };
  }
  return out;
}

function applyToStore(l: PersistedLayout) {
  applyingRemote = true;
  clearSaveTimer();
  try {
    const state = useUi.getState();
    const current = state.workspaces;
    const withDead = mergeDeadTabs(l, current);
    const withExited = mergeExitedTabs({ ...l, workspaces: withDead }, current);
    const withTabRuntime = mergeTerminalRuntimeTabs({ ...l, workspaces: withExited }, current);
    const merged = mergeDirtyEditorTabs({ ...l, workspaces: withTabRuntime }, current);
    const withRuntime = mergeSqlDraftWorkspaces(
      { ...l, workspaces: merged },
      current,
      state.workspaceRuntime,
    );
    useUi.setState({
      leftOpen: l.leftOpen,
      leftMode: l.leftMode,
      rightOpen: l.rightOpen,
      leftWidth: l.leftWidth,
      rightWidth: l.rightWidth,
      workspaces: withRuntime.workspaces,
      activeWorkspaceId: l.activeWorkspaceId,
      aiBoards: preserveAiDrafts(l.aiBoards, state.aiBoards),
      workspaceRuntime: withRuntime.workspaceRuntime,
    });
    const droppedBackgrounds = layoutMigrationNotices.get(l)?.droppedBackgrounds ?? [];
    const newDrops = droppedBackgrounds.filter((key) => !notifiedInvalidBackgrounds.has(key));
    if (newDrops.length > 0) {
      for (const key of newDrops) notifiedInvalidBackgrounds.add(key);
      useUi.getState().pushToast("info", "请从目标主机重新打开后台会话");
    }
    if (droppedBackgrounds.length === 0) notifiedInvalidBackgrounds.clear();
  } finally {
    applyingRemote = false;
  }
}

async function pullLatest(
  reason: "boot" | "remote" | "conflict",
  apply = true,
): Promise<PersistedLayout | null> {
  if (fetchInFlight) return null;
  const epoch = accountEpoch;
  fetchInFlight = true;
  let result: PersistedLayout | null = null;
  let failed = false;
  try {
    const dto = await layoutApi.get();
    if (epoch === accountEpoch) {
      revision = dto.revision;
      const parsed = sanitizeLayout(dto.data);
      if (parsed) {
        result = parsed;
        if (apply) {
          const removed = reason === "remote" ? collectRemovedTerminals(parsed) : [];
          lastSynced = JSON.stringify(parsed);
          applyToStore(parsed);
          if (reason === "conflict") {
            useUi.getState().pushToast("info", "布局已在其他设备上更新，已刷新为最新版本");
          }
          if (removed.length > 0) void notifyBackgroundedTerminals(removed);
        }
      }
    }
  } catch (e) {
    failed = true;
    if (epoch === accountEpoch) console.warn("[NexTerm] Failed to fetch server layout", e);
  } finally {
    fetchInFlight = false;
  }
  const nextRev = pendingRemoteRev;
  pendingRemoteRev = null;
  if (epoch !== accountEpoch) {
    return nextRev === null ? null : pullLatest("remote", apply);
  }
  if (nextRev !== null && (failed || nextRev > revision)) {
    return pullLatest("remote", apply);
  }
  return result;
}

async function putLayout(json: string, allowTakeover = true, epoch = accountEpoch): Promise<void> {
  try {
    const res = await layoutApi.put(json, revision);
    if (epoch !== accountEpoch) return;
    if (res.conflict) {
      const server = await pullLatest("conflict", false);
      if (epoch !== accountEpoch) return;
      if (server !== null && JSON.stringify(server) === json) {
        lastSynced = json;
        retryCount = 0;
        saveErrorNotified = false;
        return;
      }
      if (allowTakeover) {
        await putLayout(json, false, epoch);
        if (epoch !== accountEpoch) return;
        return;
      }
      await pullLatest("conflict", true);
      if (epoch !== accountEpoch) return;
      return;
    }
    revision = res.revision;
    lastSynced = json;
    retryCount = 0;
    saveErrorNotified = false;
  } catch (e) {
    if (epoch !== accountEpoch) return;
    if (!saveErrorNotified) {
      saveErrorNotified = true;
      useUi.getState().pushToast("error", `布局同步失败：${describeError(e)}`);
    }
    if (retryCount < MAX_RETRY) {
      retryCount += 1;
      clearRetryTimer();
      retryTimer = window.setTimeout(() => {
        retryTimer = null;
        void flushLayout();
      }, RETRY_MS);
    }
  }
}

export async function flushLayout(): Promise<void> {
  if (writing) {
    dirty = true;
    return;
  }
  const layout = serializeLayout(useUi.getState());
  if (!layout) return;
  const json = JSON.stringify(layout);
  if (json === lastSynced) return;
  writing = true;
  try {
    await putLayout(json);
  } finally {
    writing = false;
  }
  if (dirty) {
    dirty = false;
    await flushLayout();
  }
  if (remoteEventDuringWrite) {
    remoteEventDuringWrite = false;
    if (fetchInFlight) {
      pendingRemoteRev = Number.MAX_SAFE_INTEGER;
    } else {
      await pullLatest("remote");
    }
  }
}

export function collectRemovedTerminals(next: PersistedLayout): { tabId: string; title: string }[] {
  const before = new Map<string, string>();
  for (const w of useUi.getState().workspaces) {
    for (const p of w.panes) {
      for (const t of p.tabs) {
        if (t.kind === "terminal" && t.tabId) before.set(t.tabId, t.title);
      }
    }
  }
  const after = new Set<string>();
  for (const w of next.workspaces) {
    for (const p of w.panes) {
      for (const t of p.tabs) {
        if (t.kind === "terminal" && t.tabId) after.add(t.tabId);
      }
    }
  }
  const out: { tabId: string; title: string }[] = [];
  for (const [tabId, title] of before) {
    if (!after.has(tabId)) out.push({ tabId, title });
  }
  return out;
}

export async function notifyBackgroundedTerminals(
  removed: { tabId: string; title: string }[],
): Promise<void> {
  let live: Awaited<ReturnType<typeof terminalApi.listLive>>;
  try {
    live = await terminalApi.listLive();
  } catch {
    return;
  }
  for (const r of removed) {
    const hit = live.find((t) => t.tabId === r.tabId);
    if (!hit || hit.exited) continue;
    const text =
      hit.viewers > 1
        ? `终端「${r.title}」的标签已被其他设备关闭，进程仍在运行，且仍有其他设备正在观看，可在「后台会话」里接管`
        : `终端「${r.title}」的标签已被其他设备关闭，进程仍在后台运行，可在「后台会话」里接管`;
    useUi.getState().pushToast("info", text);
  }
}

export function advanceLayoutAccount(): void {
  accountEpoch += 1;
  revision = 0;
  lastSynced = "";
  dirty = false;
  remoteEventDuringWrite = false;
  pendingRemoteRev = null;
  retryCount = 0;
  saveErrorNotified = false;
  clearSaveTimer();
  clearRetryTimer();
}

export function resetLayoutSyncForTest(): void {
  accountEpoch = 0;
  revision = 0;
  lastSynced = "";
  applyingRemote = false;
  retryCount = 0;
  saveErrorNotified = false;
  fetchInFlight = false;
  writing = false;
  dirty = false;
  remoteEventDuringWrite = false;
  pendingRemoteRev = null;
  notifiedInvalidBackgrounds.clear();
  clearSaveTimer();
  clearRetryTimer();
}

function onStoreChange() {
  if (applyingRemote) return;
  clearSaveTimer();
  saveTimer = window.setTimeout(() => {
    saveTimer = null;
    void flushLayout();
  }, SAVE_DEBOUNCE_MS);
}

export function onRemoteChange(payload: { revision?: number } | null) {
  const rev = payload?.revision;
  if (typeof rev === "number" && rev <= revision) return;
  if (writing) {
    remoteEventDuringWrite = true;
    return;
  }
  if (fetchInFlight) {
    if (typeof rev === "number") {
      pendingRemoteRev = pendingRemoteRev === null ? rev : Math.max(pendingRemoteRev, rev);
    } else {
      pendingRemoteRev = Number.MAX_SAFE_INTEGER;
    }
    return;
  }
  void pullLatest("remote");
}

export function startLayoutSync(): void {
  if (started) return;
  started = true;
  void (async () => {
    try {
      await pullLatest("boot");
    } finally {
      resolveBootstrap();
    }
    useUi.subscribe(onStoreChange);
    await listenEvent<{ revision?: number }>(LAYOUT_CHANGED, onRemoteChange);
  })();
}
