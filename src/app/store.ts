// 全局 UI 状态（zustand）：标签/面板/会话视图。
// 服务端数据（资产列表/容器列表）走 react-query，不放这里（§10）。
import { create } from "zustand";
import { sessionApi, terminalApi, type SessionInfo } from "../ipc/commands";

export type PaneKind = "terminal" | "files" | "mount" | "docker" | "db" | "settings" | "audit";

export interface AppTab {
  id: string;
  kind: PaneKind;
  title: string;
  sessionId?: string;
  tabId?: string; // 终端内核标签
  containerId?: string;
  connId?: string;
  path?: string;
  closable: boolean;
}

export interface ToastItem {
  id: number;
  kind: "info" | "error" | "success";
  text: string;
}

export interface TextPromptState {
  message: string;
  value: string;
  resolve: (v: string | null) => void;
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
  rightOpen: boolean;
  // 标签
  tabs: AppTab[];
  activeTabId: string | null;
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

  setLeftOpen: (v: boolean) => void;
  setRightOpen: (v: boolean) => void;
  setActiveTab: (id: string) => void;
  addTab: (tab: AppTab) => void;
  closeTab: (id: string) => Promise<void>;
  updateTab: (id: string, patch: Partial<AppTab>) => void;
  setSessions: (s: SessionInfo[]) => void;
  setAiBusy: (v: boolean) => void;
  pushToast: (kind: ToastItem["kind"], text: string) => void;
  dismissToast: (id: number) => void;
}

let toastSeq = 1;

export const useUi = create<UiState>((set, get) => ({
  leftOpen: true,
  rightOpen: true,
  tabs: [],
  activeTabId: null,
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

  setLeftOpen: (v) => set({ leftOpen: v }),
  setRightOpen: (v) => set({ rightOpen: v }),
  setActiveTab: (id) => set({ activeTabId: id }),

  addTab: (tab) => {
    const { tabs } = get();
    // 同一 (kind, tabId/sessionId+kind) 复用
    const exists = tabs.find(
      (t) =>
        t.id === tab.id ||
        (t.tabId && tab.tabId && t.tabId === tab.tabId) ||
        // 终端标签**永不复用**：同一个会话上必须能开多个独立 shell。
        // （否则刚开第二个终端时它还没有内核 tabId，会被误判成同一个标签而合并掉。）
        // 其余面板（文件 / Docker / 挂载）按 kind+session+path 复用，避免重复开同一页。
        (tab.kind !== "terminal" &&
          t.kind === tab.kind &&
          t.sessionId === tab.sessionId &&
          !t.tabId &&
          !tab.tabId &&
          t.path === tab.path),
    );
    if (exists) {
      set({ activeTabId: exists.id });
      get().updateTab(exists.id, tab);
      return;
    }
    set({ tabs: [...tabs, tab], activeTabId: tab.id });
  },

  closeTab: async (id) => {
    const { tabs, activeTabId } = get();
    const target = tabs.find((t) => t.id === id);
    if (target?.tabId) {
      await terminalApi.closeTab(target.tabId).catch(() => undefined);
    }
    const next = tabs.filter((t) => t.id !== id);
    const newActive =
      activeTabId === id ? (next.length ? next[next.length - 1].id : null) : activeTabId;
    set({ tabs: next, activeTabId: newActive });
  },

  updateTab: (id, patch) =>
    set((st) => ({
      tabs: st.tabs.map((t) => (t.id === id ? { ...t, ...patch } : t)),
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

/** 便捷：建立终端标签（连接复用，§7）。 */
export async function openTerminalTab(session: SessionInfo, title?: string) {
  const { addTab } = useUi.getState();
  const id = `term-${session.id}-${Date.now()}`;
  addTab({
    id,
    kind: "terminal",
    title: title ?? session.name,
    sessionId: session.id,
    closable: true,
  });
}

export async function connectAsset(asset: {
  id: string;
  name: string;
  kind: string;
}): Promise<void> {
  const { pushToast, setSessions, sessions } = useUi.getState();
  try {
    const info =
      asset.kind === "local"
        ? await sessionApi.connectLocal()
        : await sessionApi.connect(asset.id);
    setSessions([...sessions.filter((s) => s.id !== info.id), info]);
    await openTerminalTab(info, info.name);
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
          await openTerminalTab(info, info.name);
          return;
        } catch (e2) {
          pushToast("error", String(e2));
          return;
        }
      }
    }
    pushToast(
      "error",
      err.message ??
        (typeof e === "object" ? JSON.stringify(e) : String(e)),
    );
  }
}
