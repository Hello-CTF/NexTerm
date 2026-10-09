import {
  assetApi,
  sessionApi,
  terminalApi,
  type Asset,
  type LiveTabInfo,
  type SessionInfo,
} from "../ipc/commands";
import { splitAllowedForHeight } from "../features/terminal/workspaceLayout";
import { ask, askChoice, promptText } from "../ui/dialogs";
import {
  connectAssetSession,
  connectDbAsset,
  dbKindOf,
  nextTabId,
  reclaimWorkspaceTabs,
  useUi,
  type AppTab,
  type DbKind,
  type PaneKind,
  type Workspace,
  type WorkspaceKind,
} from "./store";

// 预设只保存挂在资产会话上的标签; 全局工具标签(设置/审计/后台会话等)不进预设。
const PRESET_TAB_KINDS: ReadonlySet<PaneKind> = new Set<PaneKind>([
  "terminal",
  "files",
  "log",
  "mount",
  "forward",
  "docker",
  "db",
]);

export interface LayoutPresetTab {
  kind: PaneKind;
  title: string;
  sessionId?: string;
  tabId?: string;
  containerId?: string;
  dbKind?: DbKind;
  path?: string;
}

export interface LayoutPresetPane {
  tabs: LayoutPresetTab[];
  activeTabIndex: number;
}

export interface LayoutPresetWorkspace {
  kind: WorkspaceKind;
  title: string;
  sessionId?: string;
  assetId?: string;
  assetKind?: string;
  dbKind?: DbKind;
  splitRatio: number;
  panes: LayoutPresetPane[];
  activePaneIndex: number;
}

export interface LayoutPreset {
  id: string;
  name: string;
  createdAt: number;
  updatedAt: number;
  workspace: LayoutPresetWorkspace;
}

function isObj(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function optStr(v: unknown): string | undefined {
  return typeof v === "string" && v.length > 0 ? v : undefined;
}

function optNum(v: unknown): number {
  return typeof v === "number" && Number.isFinite(v) ? v : 0;
}

function clampIndex(v: unknown, length: number): number {
  if (typeof v !== "number" || !Number.isFinite(v)) return Math.max(0, length - 1);
  return Math.min(length - 1, Math.max(0, Math.round(v)));
}

function clampFraction(v: unknown, min: number, max: number, d: number): number {
  if (typeof v !== "number" || !Number.isFinite(v)) return d;
  return Math.min(max, Math.max(min, v));
}

function sanitizePresetTab(raw: unknown): LayoutPresetTab | null {
  if (!isObj(raw)) return null;
  const kind = raw.kind;
  if (typeof kind !== "string" || !PRESET_TAB_KINDS.has(kind as PaneKind)) return null;
  const k = kind as PaneKind;
  const sessionId = optStr(raw.sessionId);
  // db 标签靠工作区 assetId 重连拿新 connId, 其余标签恢复都需要会话。
  if (k !== "db" && !sessionId) return null;
  return {
    kind: k,
    title: optStr(raw.title) ?? "标签",
    sessionId,
    tabId: optStr(raw.tabId),
    containerId: optStr(raw.containerId),
    dbKind: dbKindOf(typeof raw.dbKind === "string" ? raw.dbKind : "") ?? undefined,
    path: optStr(raw.path),
  };
}

function sanitizePresetPane(raw: unknown): LayoutPresetPane | null {
  if (!isObj(raw) || !Array.isArray(raw.tabs)) return null;
  const tabs = raw.tabs.map(sanitizePresetTab).filter((t): t is LayoutPresetTab => t !== null);
  if (tabs.length === 0) return null;
  return { tabs, activeTabIndex: clampIndex(raw.activeTabIndex, tabs.length) };
}

function sanitizePresetWorkspace(raw: unknown): LayoutPresetWorkspace | null {
  if (!isObj(raw) || !Array.isArray(raw.panes)) return null;
  const panes = raw.panes
    .map(sanitizePresetPane)
    .filter((p): p is LayoutPresetPane => p !== null);
  if (panes.length === 0) return null;
  const kind = raw.kind;
  return {
    kind: kind === "db" || kind === "tools" ? kind : "session",
    title: optStr(raw.title) ?? "工作区",
    sessionId: optStr(raw.sessionId),
    assetId: optStr(raw.assetId),
    assetKind: optStr(raw.assetKind),
    dbKind: dbKindOf(typeof raw.dbKind === "string" ? raw.dbKind : "") ?? undefined,
    splitRatio: clampFraction(raw.splitRatio, 0.15, 0.85, 0.5),
    panes,
    activePaneIndex: clampIndex(raw.activePaneIndex, panes.length),
  };
}

export function sanitizeLayoutPresets(raw: unknown): LayoutPreset[] {
  if (!Array.isArray(raw)) return [];
  const out: LayoutPreset[] = [];
  for (const item of raw) {
    if (!isObj(item)) continue;
    const id = optStr(item.id);
    const name = optStr(item.name);
    const workspace = sanitizePresetWorkspace(item.workspace);
    if (!id || !name || !workspace) continue;
    out.push({
      id,
      name,
      createdAt: optNum(item.createdAt),
      updatedAt: optNum(item.updatedAt),
      workspace,
    });
  }
  return out;
}

function toPresetTab(t: AppTab): LayoutPresetTab {
  return {
    kind: t.kind,
    title: t.title,
    sessionId: t.sessionId,
    tabId: t.tabId,
    containerId: t.containerId,
    dbKind: t.dbKind,
    path: t.path,
  };
}

export function captureWorkspacePreset(ws: Workspace): LayoutPresetWorkspace | null {
  const panes: LayoutPresetPane[] = [];
  let activePaneIndex = 0;
  for (const p of ws.panes) {
    const tabs: LayoutPresetTab[] = [];
    let activeTabIndex = -1;
    for (const t of p.tabs) {
      if (!PRESET_TAB_KINDS.has(t.kind)) continue;
      if (t.id === p.activeTabId) activeTabIndex = tabs.length;
      tabs.push(toPresetTab(t));
    }
    if (tabs.length === 0) continue;
    if (activeTabIndex < 0) activeTabIndex = tabs.length - 1;
    if (p.id === ws.activePaneId) activePaneIndex = panes.length;
    panes.push({ tabs, activeTabIndex });
  }
  if (panes.length === 0) return null;
  return {
    kind: ws.kind,
    title: ws.title,
    sessionId: ws.sessionId,
    assetId: ws.assetId,
    assetKind: ws.assetKind,
    dbKind: ws.dbKind,
    splitRatio: ws.splitRatio,
    panes,
    activePaneIndex: Math.min(activePaneIndex, panes.length - 1),
  };
}

export async function saveLayoutPresetFromWorkspace(workspaceId?: string): Promise<void> {
  const st = useUi.getState();
  const ws = workspaceId
    ? st.workspaces.find((w) => w.id === workspaceId)
    : (st.workspaces.find((w) => w.id === st.activeWorkspaceId) ??
      st.workspaces[st.workspaces.length - 1]);
  if (!ws) {
    st.pushToast("info", "先连接一台主机，再保存布局预设");
    return;
  }
  const snapshot = captureWorkspacePreset(ws);
  if (!snapshot) {
    st.pushToast("info", `「${ws.title}」里没有可保存的标签`);
    return;
  }
  const name = await promptText("预设名称：", ws.title);
  if (name === null) return;
  const trimmed = name.trim().slice(0, 40);
  if (!trimmed) return;
  const presets = useUi.getState().layoutPresets;
  const existing = presets.find((p) => p.name === trimmed);
  const now = Date.now();
  const preset: LayoutPreset = {
    id: existing?.id ?? nextTabId("preset"),
    name: trimmed,
    createdAt: existing?.createdAt ?? now,
    updatedAt: now,
    workspace: snapshot,
  };
  useUi
    .getState()
    .setLayoutPresets(
      existing
        ? presets.map((p) => (p.id === existing.id ? preset : p))
        : [...presets, preset],
    );
  useUi
    .getState()
    .pushToast("success", existing ? `已覆盖预设「${trimmed}」` : `已保存预设「${trimmed}」`);
}

export async function deleteLayoutPreset(id: string): Promise<void> {
  const st = useUi.getState();
  const preset = st.layoutPresets.find((p) => p.id === id);
  if (!preset) return;
  if (!(await ask(`删除预设「${preset.name}」？\n当前打开的工作区不受影响。`, {
    title: "删除布局预设",
    kind: "warning",
  }))) {
    return;
  }
  useUi.getState().setLayoutPresets(useUi.getState().layoutPresets.filter((p) => p.id !== id));
  useUi.getState().pushToast("info", `已删除预设「${preset.name}」`);
}

export type LayoutPresetTarget = "current" | "new";

interface RestoreContext {
  session: SessionInfo | null;
  connId: string | null;
  liveByTabId: Map<string, LiveTabInfo>;
}

function buildRestoredTab(t: LayoutPresetTab, ctx: RestoreContext): AppTab | null {
  if (t.kind === "terminal") {
    const live = t.tabId ? ctx.liveByTabId.get(t.tabId) : undefined;
    // 守护终端仍存活: 直接接管原 tabId, 不新开 shell。
    if (live && !live.exited) {
      return {
        id: nextTabId(`term-${live.sessionId}`),
        kind: "terminal",
        title: t.title,
        sessionId: live.sessionId,
        tabId: t.tabId,
        containerId: t.containerId,
        closable: true,
      };
    }
    if (!ctx.session) return null;
    return {
      id: nextTabId(`term-${ctx.session.id}`),
      kind: "terminal",
      title: t.title,
      sessionId: ctx.session.id,
      containerId: t.containerId,
      closable: true,
    };
  }
  if (t.kind === "db") {
    if (!ctx.connId) return null;
    return {
      id: `db-${ctx.connId}`,
      kind: "db",
      title: t.title,
      connId: ctx.connId,
      dbKind: t.dbKind,
      closable: true,
    };
  }
  if (!ctx.session) return null;
  return {
    id: t.path ? `${t.kind}-${ctx.session.id}-${t.path}` : nextTabId(`${t.kind}-${ctx.session.id}`),
    kind: t.kind,
    title: t.title,
    sessionId: ctx.session.id,
    path: t.path,
    closable: true,
  };
}

async function fetchPresetAsset(assetId: string, presetName: string): Promise<Asset | null> {
  try {
    return await assetApi.get(assetId);
  } catch {
    useUi.getState().pushToast("error", `预设「${presetName}」引用的资产已不存在`);
    return null;
  }
}

function placeTab(workspaceId: string, paneId: string, tab: AppTab): void {
  useUi.setState((s) => ({
    workspaces: s.workspaces.map((w) =>
      w.id === workspaceId
        ? {
            ...w,
            panes: w.panes.map((p) =>
              p.id === paneId ? { ...p, tabs: [...p.tabs, tab], activeTabId: tab.id } : p,
            ),
          }
        : w,
    ),
  }));
}

const inflightApplies = new Map<string, Promise<void>>();

export function applyLayoutPreset(id: string, target: LayoutPresetTarget): Promise<void> {
  const inflight = inflightApplies.get(id);
  if (inflight) return inflight;
  const promise = runApplyLayoutPreset(id, target).finally(() => {
    inflightApplies.delete(id);
  });
  inflightApplies.set(id, promise);
  return promise;
}

async function runApplyLayoutPreset(id: string, target: LayoutPresetTarget): Promise<void> {
  const preset = useUi.getState().layoutPresets.find((p) => p.id === id);
  if (!preset) return;
  const pw = preset.workspace;
  const { pushToast } = useUi.getState();

  const liveByTabId = new Map<string, LiveTabInfo>();
  try {
    for (const t of await terminalApi.listLive()) liveByTabId.set(t.tabId, t);
  } catch {
    // 列表不可用时按没有守护终端处理, 走重连。
  }
  let sessions = useUi.getState().sessions;
  try {
    sessions = await sessionApi.list();
    useUi.getState().setSessions(sessions);
  } catch {
    // 沿用本地已有的会话列表。
  }

  let session = pw.sessionId
    ? (sessions.find((s) => s.id === pw.sessionId && s.status === "connected") ?? null)
    : null;
  if (!session) {
    const liveHit = pw.panes
      .flatMap((p) => p.tabs)
      .find(
        (t) =>
          t.kind === "terminal" &&
          !!t.tabId &&
          liveByTabId.has(t.tabId) &&
          !liveByTabId.get(t.tabId)?.exited,
      );
    const live = liveHit?.tabId ? liveByTabId.get(liveHit.tabId) : undefined;
    if (live) session = sessions.find((s) => s.id === live.sessionId) ?? null;
  }
  if (!session && pw.assetId && pw.kind !== "db") {
    const asset = await fetchPresetAsset(pw.assetId, preset.name);
    if (asset) session = await connectAssetSession(asset);
  }
  let connId: string | null = null;
  const needsDb =
    pw.kind === "db" || pw.panes.some((p) => p.tabs.some((t) => t.kind === "db"));
  if (needsDb) {
    if (pw.assetId) {
      const asset = await fetchPresetAsset(pw.assetId, preset.name);
      if (asset) connId = await connectDbAsset(asset);
    } else {
      pushToast("error", `预设「${preset.name}」缺少数据库资产信息，无法恢复`);
    }
  }

  const st1 = useUi.getState();
  let ws: Workspace | null = null;
  if (target === "current") {
    ws =
      st1.workspaces.find((w) => w.id === st1.activeWorkspaceId) ??
      st1.workspaces[st1.workspaces.length - 1] ??
      null;
    // 工具工作区是全局标签的固定载体, 不当预设的落地目标。
    if (ws?.kind === "tools") ws = null;
  } else {
    // 新工作区遵守「一个会话一个工作区」的既有约定: 会话已打开时就地重建。
    ws =
      (session && st1.workspaces.find((w) => w.sessionId === session.id)) ||
      (connId && st1.workspaces.find((w) => w.connId === connId)) ||
      null;
  }
  if (ws) {
    const tabs = ws.panes.flatMap((p) => p.tabs);
    if (tabs.length > 0 && !(await reclaimWorkspaceTabs(tabs, ws.title, ws.assetKind))) return;
  }

  let panePresets = pw.panes;
  if (
    panePresets.length > 1 &&
    !splitAllowedForHeight(typeof window === "undefined" ? Number.POSITIVE_INFINITY : window.innerHeight)
  ) {
    const active = panePresets[Math.min(pw.activePaneIndex, panePresets.length - 1)];
    const merged = panePresets.flatMap((p) => p.tabs);
    const activeTab = active?.tabs[Math.min(active.activeTabIndex, active.tabs.length - 1)];
    panePresets = [
      { tabs: merged, activeTabIndex: Math.max(0, activeTab ? merged.indexOf(activeTab) : merged.length - 1) },
    ];
    pushToast("info", "窗口高度不足，预设已在单栏打开");
  }

  const paneIds = panePresets.map(() => nextTabId("pane"));
  const activePaneIdx = Math.min(pw.activePaneIndex, paneIds.length - 1);
  const workspaceId = ws?.id ?? nextTabId(`ws-${pw.kind}`);
  useUi.setState((s) => {
    const panes = paneIds.map((pid) => ({ id: pid, tabs: [], activeTabId: null }));
    if (ws) {
      return {
        activeWorkspaceId: workspaceId,
        workspaces: s.workspaces.map((w) =>
          w.id === workspaceId
            ? {
                ...w,
                kind: pw.kind,
                title: pw.title,
                assetId: pw.assetId,
                assetKind: pw.assetKind,
                dbKind: pw.dbKind,
                sessionId: session?.id,
                connId: connId ?? undefined,
                panes,
                activePaneId: paneIds[activePaneIdx],
                splitRatio: pw.splitRatio,
              }
            : w,
        ),
      };
    }
    return {
      activeWorkspaceId: workspaceId,
      workspaces: [
        ...s.workspaces,
        {
          id: workspaceId,
          kind: pw.kind,
          title: pw.title,
          assetId: pw.assetId,
          assetKind: pw.assetKind,
          dbKind: pw.dbKind,
          sessionId: session?.id,
          connId: connId ?? undefined,
          panes,
          activePaneId: paneIds[activePaneIdx],
          splitRatio: pw.splitRatio,
          closable: true,
        },
      ],
    };
  });

  const ctx: RestoreContext = { session, connId, liveByTabId };
  let skipped = 0;
  const activeTabIds: (string | null)[] = [];
  panePresets.forEach((pp, paneIdx) => {
    const paneId = paneIds[paneIdx];
    let activeTabId: string | null = null;
    pp.tabs.forEach((t, tabIdx) => {
      const tab = buildRestoredTab(t, ctx);
      if (!tab) {
        skipped += 1;
        return;
      }
      placeTab(workspaceId, paneId, tab);
      if (tabIdx === Math.min(pp.activeTabIndex, pp.tabs.length - 1)) activeTabId = tab.id;
    });
    activeTabIds.push(activeTabId);
  });
  useUi.setState((s) => ({
    workspaces: s.workspaces.map((w) =>
      w.id === workspaceId
        ? {
            ...w,
            activePaneId: paneIds[activePaneIdx] ?? w.activePaneId,
            panes: w.panes.map((p, i) =>
              activeTabIds[i] ? { ...p, activeTabId: activeTabIds[i] } : p,
            ),
          }
        : w,
    ),
  }));
  if (skipped > 0) {
    pushToast("info", `预设「${preset.name}」有 ${skipped} 个标签未能恢复（资产不可用或未连接）`);
  } else {
    pushToast("success", `已应用预设「${preset.name}」`);
  }
}

// choosePresetTargetAndApply 给命令面板这类只有一个动作位的入口用: 先问落到哪里。
export async function choosePresetTargetAndApply(id: string): Promise<void> {
  const preset = useUi.getState().layoutPresets.find((p) => p.id === id);
  if (!preset) return;
  const hasWorkspace = useUi.getState().workspaces.some((w) => w.kind !== "tools");
  if (!hasWorkspace) {
    await applyLayoutPreset(id, "new");
    return;
  }
  const choice = await askChoice(`把预设「${preset.name}」应用到哪里？`, {
    title: "应用布局预设",
    choices: [
      { key: "new", label: "应用到新工作区", hint: "为预设另开工作区", primary: true },
      { key: "current", label: "应用到当前工作区", hint: "替换当前工作区的分屏与标签" },
    ],
  });
  if (choice === "new" || choice === "current") await applyLayoutPreset(id, choice);
}
