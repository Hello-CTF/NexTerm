import { layoutApi, terminalApi } from "../ipc/commands";
import { listenEvent } from "../ipc/events";
import { describeError } from "../ui/errorText";
import { isDirtyFileEditor } from "../features/files/editorGuards";
import { sanitizeLayoutPresets, type LayoutPreset } from "./layoutPresets";
import {
  GLOBAL_AI_BOARD_KEY,
  LEFT_WIDTH_RANGE,
  RIGHT_WIDTH_RANGE,
  dbKindOf,
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
  presets: LayoutPreset[];
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
  "deviceTerminal",
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
  const deviceId = optStr(raw.deviceId);
  if (kind === "deviceTerminal" && !deviceId) return null;
  return {
    id,
    kind: kind as PaneKind,
    title: typeof raw.title === "string" ? raw.title : "标签",
    sessionId: optStr(raw.sessionId),
    tabId,
    containerId: optStr(raw.containerId),
    connId: optStr(raw.connId),
    dbKind: dbKindOf(typeof raw.dbKind === "string" ? raw.dbKind : "") ?? undefined,
    credId: optStr(raw.credId),
    path: optStr(raw.path),
    deviceId,
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
  let panes = nonEmpty.length > 0 ? nonEmpty : parsed.slice(0, 1);
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

export function sanitizeLayout(raw: unknown): PersistedLayout | null {
  if (!isObj(raw)) return null;
  if (raw.v !== LAYOUT_VERSION) return null;
  if (!Array.isArray(raw.workspaces)) return null;
  if (!isObj(raw.aiBoards)) return null;
  const workspaces = raw.workspaces
    .map(sanitizeWorkspace)
    .filter((w): w is Workspace => w !== null);
  const activeWorkspaceId = workspaces.some((w) => w.id === raw.activeWorkspaceId)
    ? (raw.activeWorkspaceId as string)
    : (workspaces[0]?.id ?? null);
  return {
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
    presets: sanitizeLayoutPresets(raw.presets),
    aiBoards: sanitizeAiBoards(raw.aiBoards, workspaces),
  };
}

interface LayoutSource {
  leftOpen: boolean;
  leftMode: LeftMode;
  rightOpen: boolean;
  leftWidth: number;
  rightWidth: number;
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
  layoutPresets: LayoutPreset[];
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
    presets: s.layoutPresets,
    aiBoards: s.aiBoards ?? {},
  });
}

let started = false;
let resolveBootstrap: () => void = () => {};
export const layoutBootstrapped: Promise<void> = new Promise((r) => {
  resolveBootstrap = r;
});

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

function applyToStore(l: PersistedLayout) {
  applyingRemote = true;
  clearSaveTimer();
  try {
    const current = useUi.getState().workspaces;
    const withDead = mergeDeadTabs(l, current);
    const withExited = mergeExitedTabs({ ...l, workspaces: withDead }, current);
    const merged = mergeDirtyEditorTabs({ ...l, workspaces: withExited }, current);
    useUi.setState({
      leftOpen: l.leftOpen,
      leftMode: l.leftMode,
      rightOpen: l.rightOpen,
      leftWidth: l.leftWidth,
      rightWidth: l.rightWidth,
      workspaces: merged,
      activeWorkspaceId: l.activeWorkspaceId,
      layoutPresets: l.presets,
      aiBoards: l.aiBoards,
    });
  } finally {
    applyingRemote = false;
  }
}

async function pullLatest(
  reason: "boot" | "remote" | "conflict",
  apply = true,
): Promise<PersistedLayout | null> {
  if (fetchInFlight) return null;
  fetchInFlight = true;
  let result: PersistedLayout | null = null;
  let failed = false;
  try {
    const dto = await layoutApi.get();
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
  } catch (e) {
    failed = true;
    console.warn("[NexTerm] 拉取服务端布局失败", e);
  } finally {
    fetchInFlight = false;
  }
  if (failed) return result;
  if (pendingRemoteRev !== null && pendingRemoteRev > revision) {
    pendingRemoteRev = null;
    return pullLatest("remote", apply);
  }
  pendingRemoteRev = null;
  return result;
}

async function putLayout(json: string, allowTakeover = true): Promise<void> {
  try {
    const res = await layoutApi.put(json, revision);
    if (res.conflict) {
      const server = await pullLatest("conflict", false);
      if (server !== null && JSON.stringify(server) === json) {
        lastSynced = json;
        retryCount = 0;
        saveErrorNotified = false;
        return;
      }
      if (allowTakeover) {
        await putLayout(json, false);
        return;
      }
      await pullLatest("conflict", true);
      return;
    }
    revision = res.revision;
    lastSynced = json;
    retryCount = 0;
    saveErrorNotified = false;
  } catch (e) {
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

export function resetLayoutSyncForTest(): void {
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
