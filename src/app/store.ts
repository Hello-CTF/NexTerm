// 全局 UI 状态（zustand）：工作区 / 标签 / 面板 / 会话视图。
// 服务端数据（资产列表/容器列表）走 react-query，不放这里（§10）。
//
// **两级标签**：一级是「工作区」（≈ 一台连上的机器 / 一个数据库连接），
// 二级是工作区内部的「标签」（终端、编辑器、容器面板…）。
// 这么分是因为实际用起来一定是"同时开几台机器，每台上各有一堆终端和文件"，
// 扁平的一排标签很快就没法用了。
import { create } from "zustand";
import { dbApi, sessionApi, terminalApi, vaultApi, type SessionInfo } from "../ipc/commands";
import { describeError } from "../ui/errorText";

export type PaneKind =
  | "terminal"
  | "files"
  | "mount"
  | "forward"
  | "docker"
  | "db"
  | "credentials"
  | "settings"
  | "audit";

/** 左栏形态。资产列表是"管理"入口，文件树是"干活"入口。 */
export type LeftMode = "assets" | "files";

export interface AppTab {
  id: string;
  kind: PaneKind;
  title: string;
  sessionId?: string;
  tabId?: string; // 终端内核标签
  containerId?: string;
  connId?: string;
  /** db 标签特有的驱动类型（同一套面板，两种视图）。 */
  dbKind?: "mysql" | "redis";
  path?: string;
  /**
   * 标签落地后立刻执行的命令（不带换行）。
   *
   * 目前只用在右键「在终端打开」：新终端要 cd 到某个目录，但 attach 是异步的，
   * 创建标签这一刻还没有内核 tabId 可写 —— 于是先挂在这里，
   * 等 XtermView attach 成功后再取出来执行（见 takePendingCommand）。
   */
  pendingCommand?: string;
  closable: boolean;
}

export type WorkspaceKind = "session" | "db" | "tools";

/**
 * 一个「面板」（分屏格）。
 *
 * 标签是挂在 pane 上而不是直接挂在 workspace 上的：上下分屏（参考稿里
 * 编辑器在上、终端在下）要求**每一栏有自己的一排标签**，
 * 所以"工作区 → 面板 → 标签"是三层，不是两层。
 */
export interface Pane {
  id: string;
  tabs: AppTab[];
  activeTabId: string | null;
}

/** 一级标签：一个工作区装着一到两个面板，每个面板装着一组标签。 */
export interface Workspace {
  id: string;
  kind: WorkspaceKind;
  title: string;
  /** session 工作区绑定的会话；也是左栏文件树的依据。 */
  sessionId?: string;
  /** db 工作区绑定的连接。 */
  connId?: string;
  dbKind?: "mysql" | "redis";
  /** 会话工作区的资产类型：用于选图标。 */
  assetKind?: string;
  /** panes[0] 永远在上面；有第二个就是上下分屏。 */
  panes: Pane[];
  activePaneId: string;
  /** 上下分屏时上栏占的高度比例（0.15 ~ 0.85）。 */
  splitRatio: number;
  closable: boolean;
}

/** 建工作区时给的描述（panes / activePaneId 由 store 自己管）。 */
export interface WorkspaceSpec {
  kind: WorkspaceKind;
  title: string;
  sessionId?: string;
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
  /**
   * 强制使用多行输入框。
   *
   * 不传时由 PromptModal 按内容猜（消息里带换行 / 初始值够长）——
   * 但「AI 接管任务描述」恰好是反例：提示语很短、初始值为空，
   * 用户真要写的东西却有十几行，猜不出来。
   */
  multiLine?: boolean;
  /** 敏感输入（解锁凭据库）：单行打码，不进多行分支。 */
  secret?: boolean;
  resolve: (v: string | null) => void;
}

/**
 * 应用内确认 / 提示弹框的状态。替代原生 plugin-dialog / window.confirm：
 * 系统弹框的样式与图标和整套暗色 UI 不搭，UI 要覆盖全面。
 */
export interface AppDialogState {
  kind: "ask" | "confirm" | "message";
  message: string;
  title?: string;
  /** info = 蓝色信息图标 / primary 按钮；warning = 琥珀警示图标 / 危险按钮。 */
  level: "info" | "warning";
  resolve: (v: boolean) => void;
}

/** 接管模式全局状态（§8.6）：顶部横幅与「立即夺回」按钮都读它。 */
export interface TakeoverState {
  tabId: string;
  /** ai_takeover_run 返回的 jobId；用于「夺回」时同时取消模型请求。 */
  jobId?: string;
  task: string;
  /** false = 只读接管（send_keys 全拒），生产环境应强制 false。 */
  allowWrite: boolean;
  startedAt: number;
}

interface UiState {
  // 布局
  leftOpen: boolean;
  /** 左栏形态：资产列表 / 当前工作区的文件树。 */
  leftMode: LeftMode;
  rightOpen: boolean;
  // 工作区（一级标签）
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
  // 会话
  sessions: SessionInfo[];
  // AI
  aiBusy: boolean;
  /** 非空即表示 AI 正在操作某个终端；顶部横幅据此渲染。 */
  takeover: TakeoverState | null;
  setTakeover: (t: TakeoverState | null) => void;
  // toast
  toasts: ToastItem[];
  // 全局文本输入弹窗
  textPrompt: TextPromptState | null;
  openTextPrompt: (s: TextPromptState) => void;
  closeTextPrompt: (v: string | null) => void;
  /**
   * 跨页预填的 AI 拦截规则草稿：AI 侧栏确认卡片点「加为拦截规则」时写入，
   * 设置页的规则库卡片读到后展开草稿行并清空。null = 没有待处理的跳转。
   */
  aiRulePrefill: string | null;
  setAiRulePrefill: (v: string | null) => void;
  /**
   * 应用内确认 / 提示弹框（替代原生 plugin-dialog / window.confirm——
   * 系统弹框的样式与图标和整套暗色 UI 不搭）。
   */
  appDialog: AppDialogState | null;
  openAppDialog: (d: AppDialogState) => void;
  closeAppDialog: (v: boolean) => void;

  setLeftOpen: (v: boolean) => void;
  setLeftMode: (m: LeftMode) => void;
  setRightOpen: (v: boolean) => void;

  /**
   * 两侧栏宽度（px）：拖拽调整，落盘到 localStorage。
   *
   * 放 store 而不是各自组件里的 useState —— 宽度是**布局级**状态，
   * 左栏在 FileTree / AssetTree 之间切换时不该让刚拖好的宽度跳回去。
   */
  leftWidth: number;
  rightWidth: number;
  setLeftWidth: (w: number) => void;
  setRightWidth: (w: number) => void;

  /** 建/取工作区：sessionId 或 connId 命中已有工作区就复用并激活，返回它的 id。 */
  ensureWorkspace: (spec: WorkspaceSpec) => string;
  setActiveWorkspace: (id: string) => void;
  closeWorkspace: (id: string) => Promise<void>;

  /** 上下分屏：给工作区再挂一个面板（会话工作区会给新面板开一个终端）。 */
  splitWorkspace: (workspaceId?: string) => void;
  /**
   * 在下方分屏面板里打开一个标签；还没有下方面板就先分屏。
   *
   * 与 splitWorkspace 的分工：这个方法**不预开终端** —— 「在下方编辑」的意思是
   * "上边留住原来的东西，下边给我个编辑器"，硬塞终端进去等于把下方占掉了。
   */
  openInLowerPane: (tab: AppTab, workspaceId?: string) => void;
  /** 取消分屏：关掉该面板（含里面的终端）。只剩一个面板时是空操作。 */
  unsplitWorkspace: (paneId?: string, workspaceId?: string) => Promise<void>;
  setActivePane: (paneId: string, workspaceId?: string) => void;
  setSplitRatio: (ratio: number, workspaceId?: string) => void;

  /** 以下四个都按"标签 id 全局唯一"工作，自动定位它所在的工作区与面板。 */
  setActiveTab: (id: string) => void;
  addTab: (tab: AppTab, paneId?: string) => void;
  closeTab: (id: string) => Promise<void>;
  updateTab: (id: string, patch: Partial<AppTab>) => void;

  setSessions: (s: SessionInfo[]) => void;
  setAiBusy: (v: boolean) => void;
  pushToast: (kind: ToastItem["kind"], text: string) => void;
  dismissToast: (id: number) => void;
}

let toastSeq = 1;
let tabSeq = 1;

/**
 * 生成标签 id。
 *
 * 不要用裸的 `Date.now()`：`reset_snapshot.py` 里那种批量开标签的场景
 * （或任何"点两下 + 号"）会在同一毫秒内连开两个，id 相同 →
 * 被 addTab 的 `t.id === tab.id` 判定成同一个标签，第二个直接消失。
 */
/* ── 侧栏宽度 ─────────────────────────────────────────────────────────── */

/** 左栏再窄就放不下文件名，右栏再窄对话就没法读了。 */
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

/** 拖拽手柄要用的边界与复位值（和上面的 setter 共用一份，别在两处各写一遍）。 */
export const LEFT_WIDTH_RANGE = { min: LEFT_MIN, max: LEFT_MAX, default: LEFT_DEFAULT };
export const RIGHT_WIDTH_RANGE = { min: RIGHT_MIN, max: RIGHT_MAX, default: RIGHT_DEFAULT };

/**
 * 读回上次拖好的宽度。
 *
 * 整体包在 try 里：无痕模式 / 禁用存储时 `localStorage` 会直接抛错。
 * 布局偏好丢了无所谓，但绝不能让应用起不来。
 */
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
    /* 存不下就算了，不影响使用 */
  }
}

export function nextTabId(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}-${tabSeq++}`;
}

const initialLayout = loadLayout();

export const useUi = create<UiState>((set, get) => ({
  leftOpen: true,
  leftMode: "assets",
  rightOpen: true,
  leftWidth: initialLayout.leftWidth,
  rightWidth: initialLayout.rightWidth,
  workspaces: [],
  activeWorkspaceId: null,
  sessions: [],
  aiBusy: false,
  takeover: null,
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
    const { workspaces } = get();
    const exists = workspaces.find(
      (w) =>
        (spec.sessionId !== undefined && w.sessionId === spec.sessionId) ||
        (spec.connId !== undefined && w.connId === spec.connId),
    );
    if (exists) {
      set({ activeWorkspaceId: exists.id });
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
    // 先把里面（所有面板）的终端内核标签全部回收，否则远端的 shell 会一直挂着
    await Promise.all(
      target.panes
        .flatMap((p) => p.tabs)
        .filter((t) => t.tabId)
        .map((t) => terminalApi.closeTab(t.tabId as string).catch(() => undefined)),
    );
    const next = workspaces.filter((w) => w.id !== id);
    // 会话本身不主动断开：工作区是"视图"，断连是另一个明确动作
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
    // 会话工作区分屏后直接给新面板一个终端：分屏的动机基本都是
    // "一边敲命令，一边看着日志/另一个 shell"，空面板等于还要多点一次。
    const session = st.sessions.find((x) => x.id === w.sessionId);
    if (session) void openTerminalTab(session, undefined, pane.id);
  },

  openInLowerPane: (tab, workspaceId) => {
    const st = get();
    const id = workspaceId ?? st.activeWorkspaceId ?? st.workspaces[st.workspaces.length - 1]?.id;
    if (!id) return;
    const w = st.workspaces.find((x) => x.id === id);
    if (!w) return;
    // 已经分屏了：直接用下方面板，别再多挂一个
    if (w.panes.length >= 2) {
      get().addTab(tab, w.panes[1].id);
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
    // 关面板等于关掉它里面的所有标签，终端要先回收
    await Promise.all(
      target.tabs
        .filter((t) => t.tabId)
        .map((t) => terminalApi.closeTab(t.tabId as string).catch(() => undefined)),
    );
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

    // 1) 已经有同一个标签 → 激活它，并把标题之类的字段刷新过来
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

    // 2) 落到哪个面板：显式指定 > 正在用的面板；编辑器标签优先跟已有编辑器同栏
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

  closeTab: async (id) => {
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
    if (!target || !wsId || !paneId) return;
    if (target.tabId) await terminalApi.closeTab(target.tabId).catch(() => undefined);
    set((s2) => ({
      workspaces: s2.workspaces.map((w) => {
        if (w.id !== wsId) return w;
        const pane = w.panes.find((p) => p.id === paneId);
        if (!pane) return w;
        const tabs = pane.tabs.filter((t) => t.id !== id);
        // 分屏状态下把一个面板的最后一个标签关掉 = 取消分屏，
        // 否则会留下一块空的死格子（VS Code 也是这个行为）
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

/**
 * 决定一个标签该落在哪个工作区。
 *
 * 优先级刻意做成"按身份找"而不是"按当前窗口放"：
 * 容器面板这类页面背后的异步回调可能在用户已经切到别的工作区之后才开标签，
 * 按 sessionId / connId 归属才不会被开到错误的机器上。
 */
function resolveWorkspaceId(
  tab: AppTab,
  st: { workspaces: Workspace[]; sessions: SessionInfo[]; activeWorkspaceId: string | null },
): string {
  const { ensureWorkspace } = useUi.getState();
  // 1) 已经有对应工作区
  if (tab.sessionId) {
    const hit = st.workspaces.find((w) => w.sessionId === tab.sessionId);
    if (hit) return hit.id;
  }
  if (tab.connId) {
    const hit = st.workspaces.find((w) => w.connId === tab.connId);
    if (hit) return hit.id;
  }
  // 2) 有自己的会话/连接但还没工作区：补建一个（工作区是"一台机器"的视图）
  if (tab.sessionId) {
    const s = st.sessions.find((x) => x.id === tab.sessionId);
    return ensureWorkspace({
      kind: "session",
      sessionId: tab.sessionId,
      title: s?.name ?? "会话",
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
  // 3) 全局工具标签（设置 / 审计）：落在当前工作区；一个工作区都没有就建「工具」
  if (st.activeWorkspaceId) return st.activeWorkspaceId;
  return ensureWorkspace({ kind: "tools", title: "工具" });
}

/** 新建一个空面板。 */
function makePane(): Pane {
  return { id: nextTabId("pane"), tabs: [], activeTabId: null };
}

/**
 * 标签复用规则。
 *
 * 终端标签**永不复用**：同一个会话上必须能开多个独立 shell，
 * 否则刚开第二个终端时它还没有内核 tabId，会被误判成同一个而合并掉。
 * 其余面板（文件 / 挂载 / 容器）按 kind+session+path 复用；
 * 数据库面板还要比 connId —— 同一台机器上可以同时连 MySQL 和 Redis。
 */
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

/**
 * 新标签落到哪个面板。
 *
 * 默认跟着"正在用的那一栏"走（所见即所得）；唯一的例外是编辑器标签：
 * 它会聚到已经有编辑器的栏里，免得分屏之后一栏塞满终端、另一栏塞满编辑器。
 */
function pickPaneForTab(panes: Pane[], activePaneId: string, tab: AppTab): Pane {
  const active = panes.find((p) => p.id === activePaneId) ?? panes[0];
  if (tab.kind !== "files" || !tab.path) return active;
  return panes.find((p) => p.tabs.some((t) => t.kind === "files" && t.path)) ?? active;
}

/** 当前工作区（找不到 activeWorkspaceId 时回退到最后一个）。 */
export function useActiveWorkspace(): Workspace | null {
  return useUi(
    (s) =>
      s.workspaces.find((w) => w.id === s.activeWorkspaceId) ??
      s.workspaces[s.workspaces.length - 1] ??
      null,
  );
}

/**
 * 便捷：建立终端标签（连接复用，§7）。
 *
 * `paneId` 用于指定落到哪一栏（分屏时"给新面板开一个终端"要用）。
 * `options.command` 是「落地即执行」的命令（右键「在终端打开」的 cd 走这里）。
 */
export async function openTerminalTab(
  session: SessionInfo,
  title?: string,
  paneId?: string,
  options?: { command?: string },
) {
  const wsId = useUi.getState().ensureWorkspace({
    kind: "session",
    sessionId: session.id,
    title: session.name,
    assetKind: session.kind,
  });
  const ws = useUi.getState().workspaces.find((w) => w.id === wsId);
  // 二级标签自动编号（终端 1 / 终端 2 …）：一级标签已经写着机器名了，
  // 二级再叫一遍机器名没有任何信息量。编号按整个工作区数，跨面板不重复。
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

/**
 * 取出并清空某标签的「落地即执行」命令。
 *
 * 取出即清空（而不是读一遍）：XtermView 的 attach effect 在开发模式下会被
 * StrictMode 跑两轮，读而不清的话同一条 `cd` 会往远端发两遍。
 */
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

/**
 * 生成一条 `cd <path>` 命令。
 *
 * 引号按路径形态选：`C:\...` / `C:/...` 是 Windows 目标（PowerShell 里单引号
 * 用 `''` 转义），其余按 POSIX shell（`'` 用 `'\''` 收尾再开）。
 * 不做转义的话，路径里一个空格就会把命令拆成两条 —— 这是最容易踩的坑。
 */
export function cdCommandFor(path: string): string {
  if (/^[A-Za-z]:[\\/]/.test(path)) {
    return `cd '${path.replace(/'/g, "''")}'`;
  }
  return `cd '${path.replace(/'/g, "'\\''")}'`;
}

/**
 * 找某会话下「可以被写命令」的终端：优先该栏的激活标签，其次是这一栏里
 * 任意一个已 attach 完成的终端。返回 null 表示这个会话还没有可用的终端。
 *
 * 为什么按"激活标签优先、但不强求"来排：用户右键一个目录时，多半就是想
 * 在眼前这个终端里跳过去；但若当前激活的是文件编辑器标签，仍应退而用
 * 同一栏里那个终端，而不是干巴巴地报"没有终端"。
 */
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

/**
 * 打开文件编辑标签（左栏文件树与宽幅文件浏览器双击共用）。
 *
 * 标题用文件名而不是全路径——标签栏放不下路径，完整路径在编辑器工具条里。
 * 同一路径重复打开会被 addTab 的去重规则合并（kind+session+path 相同即复用）。
 */
export function openFileTab(sessionId: string, path: string) {
  const name = path.split("/").filter(Boolean).pop() ?? path;
  useUi.getState().addTab(fileTabSpec(sessionId, path, name));
}

/**
 * 在下方分屏面板里打开编辑器（「在下方编辑」）。
 *
 * 和 openFileTab 用**同一个标签 id**：同一个文件不该因为"这次想在下面看"
 * 就出现两个编辑器标签，同时改两遍。已经开着时 addTab 会把它激活。
 */
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

/**
 * 打开「凭据」标签页（左栏底部入口调用）。
 *
 * 固定 id：已开着就激活它，不重复开。凭据是全局资源（不属于任何会话），
 * 按 resolveWorkspaceId 的规则落在当前工作区，一个都没有就建「工具」。
 */
export function openCredentialsTab() {
  useUi.getState().addTab({
    id: "tab-credentials",
    kind: "credentials",
    title: "凭据",
    closable: true,
  });
}

/**
 * 连接前确保凭据库可用：资产绑了凭据且库处于锁定态时，先弹一次解锁框，
 * 而不是等连接失败再报错。取消解锁 = 取消连接。
 */
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
    // 状态查询或解锁失败都不拦连接 —— 让后续连接流程给出具体报错
    return true;
  }
}

export async function connectAsset(asset: {
  id: string;
  name: string;
  kind: string;
  credId?: string | null;
}): Promise<void> {
  const { pushToast, setSessions, sessions, addTab, ensureWorkspace, setLeftMode, setLeftOpen } =
    useUi.getState();

  // 数据库资产：不开终端，直接开数据库工作台（复用一条 DB 连接）
  if (asset.kind === "mysql" || asset.kind === "redis") {
    try {
      if (!(await ensureVaultReadyFor(asset))) return;
      const { connId } = await dbApi.connect(asset.id);
      const kind = asset.kind === "redis" ? "redis" : "mysql";
      const title = `${asset.name} · ${kind === "mysql" ? "SQL" : "Redis"}`;
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
    // 一律按资产连接：包括 kind === "local"（「当前设备」）。
    // 以前本地走的是 `session_connect_local` —— 内核侧现造一个 id 并不存在于
    // 库里的合成资产，于是资产名（用户改过的名字）、options（shell / 起始目录）
    // 全部丢掉，审计的归属也挂在一个查不到的 id 上。现在「当前设备」是一台
    // 真正的本地资产，走同一条路就够了。
    const info = await sessionApi.connect(asset.id);
    setSessions([...sessions.filter((s) => s.id !== info.id), info]);
    // 每个连上的机器 = 一个工作区（一级标签）
    ensureWorkspace({
      kind: "session",
      sessionId: info.id,
      title: info.name,
      assetKind: asset.kind,
    });
    // 连上机器就把左栏从"资产列表"切到"这台机器的文件树"：
    // 这个产品的实际节奏是"连一台机器，然后一直在这台机器上干活"，
    // 连完之后还要自己去点一次「文件」是多余的一步。
    setLeftMode("files");
    setLeftOpen(true);
    // Docker 主机：连上后直接开容器面板，比先开终端更贴近实际用法
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
  } catch (e) {
    const err = e as { code?: string; message: string; detail?: Record<string, unknown> };
    if (err.code === "host_key_pending") {
      const detail = err.detail as
        | { host?: string; port?: number; keyType?: string; fingerprint?: string }
        | undefined;
      // 首次连接：弹指纹确认（§12.1 Strict）—— Tauri 禁用 window.confirm，走 plugin-dialog
      const { ask } = await import("../ui/dialogs");
      const ok = await ask(
        `首次连接 ${detail?.host}:${detail?.port}\n主机密钥类型: ${detail?.keyType}\n指纹(SHA256): ${detail?.fingerprint}\n信任并继续？`,
        { title: "确认主机指纹", kind: "warning" },
      );
      if (ok) {
        try {
          const info = await sessionApi.connect(asset.id, true);
          setSessions([...sessions.filter((s) => s.id !== info.id), info]);
          ensureWorkspace({
            kind: "session",
            sessionId: info.id,
            title: info.name,
            assetKind: asset.kind,
          });
          setLeftMode("files");
          setLeftOpen(true);
          await openTerminalTab(info);
          return;
        } catch (e2) {
          pushToast("error", describeError(e2));
          return;
        }
      }
    }
    // err.message 为空串时不能把空串弹出去（等于什么都没说），退回统一格式化。
    pushToast("error", err.message || describeError(e));
  }
}
