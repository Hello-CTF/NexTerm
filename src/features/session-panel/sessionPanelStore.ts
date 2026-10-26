import { create } from "zustand";
import type { SessionPanelContext } from "./sessionContext";

// The session side panel's device-local state. Open state, width and the selected tab are a per-device preference
// (not synced): each screen decides whether it has room for the panel.
const STORAGE_KEY = "nexterm.sessionPanel.v1";

export const SESSION_PANEL_WIDTH = { min: 240, max: 640, default: 300 };

export type SessionPanelTabId = "files";

export const SESSION_PANEL_TAB_IDS: readonly SessionPanelTabId[] = ["files"];

interface PersistedPanel {
  open: boolean;
  width: number;
  tab: SessionPanelTabId;
  // userClosed remembers that the user closed the panel by hand; connecting a host then leaves it closed.
  userClosed: boolean;
}

export interface SessionPanelState extends PersistedPanel {
  // locked pins the panel to one terminal; focus changes are ignored until unlocked.
  locked: SessionPanelContext | null;
  // last is the most recent focused terminal, shown while a non-terminal tab has focus.
  last: SessionPanelContext | null;
  // detached lists the sessions whose file view the user navigated away from the terminal directory.
  detached: readonly string[];
  // focusRequest counts explicit opens (toolbar button, shortcut); each one moves focus into the panel.
  focusRequest: number;
  setOpen: (open: boolean) => void;
  setWidth: (width: number) => void;
  setTab: (tab: SessionPanelTabId) => void;
  lock: (context: SessionPanelContext) => void;
  unlock: () => void;
  noteFocused: (context: SessionPanelContext) => void;
  setDetached: (sessionId: string, detached: boolean) => void;
}

function clampWidth(width: unknown): number {
  if (typeof width !== "number" || !Number.isFinite(width)) return SESSION_PANEL_WIDTH.default;
  return Math.min(SESSION_PANEL_WIDTH.max, Math.max(SESSION_PANEL_WIDTH.min, Math.round(width)));
}

function load(): PersistedPanel {
  const fallback: PersistedPanel = { open: false, width: SESSION_PANEL_WIDTH.default, tab: "files", userClosed: false };
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return fallback;
    const parsed = JSON.parse(raw) as Partial<PersistedPanel> | null;
    if (!parsed || typeof parsed !== "object") return fallback;
    return {
      open: parsed.open === true,
      width: clampWidth(parsed.width),
      tab: SESSION_PANEL_TAB_IDS.includes(parsed.tab as SessionPanelTabId) ? (parsed.tab as SessionPanelTabId) : "files",
      userClosed: parsed.userClosed === true,
    };
  } catch {
    return fallback;
  }
}

function save(state: PersistedPanel): void {
  try {
    window.localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ open: state.open, width: state.width, tab: state.tab, userClosed: state.userClosed }),
    );
  } catch {
    // Storage can be unavailable (private mode); the panel still works for this run.
  }
}

export const useSessionPanel = create<SessionPanelState>((set, get) => {
  const persist = () => save(get());
  return {
    ...load(),
    locked: null,
    last: null,
    detached: [],
    focusRequest: 0,
    setOpen: (open) => {
      if (get().open === open) return;
      set({ open });
      persist();
    },
    setWidth: (width) => {
      set({ width: clampWidth(width) });
      persist();
    },
    setTab: (tab) => {
      set({ tab });
      persist();
    },
    lock: (context) => set({ locked: context }),
    unlock: () => set({ locked: null }),
    noteFocused: (context) => {
      if (get().last?.key === context.key && get().last?.sessionId === context.sessionId) return;
      set({ last: context });
    },
    setDetached: (sessionId, detached) => {
      const current = get().detached;
      const has = current.includes(sessionId);
      if (has === detached) return;
      set({ detached: detached ? [...current, sessionId] : current.filter((id) => id !== sessionId) });
    },
  };
});

// showSessionPanel is the user's open or close (toolbar button, shortcut, close button): it always wins, is remembered
// for later connects, and an open moves focus into the panel.
export function showSessionPanel(open: boolean): void {
  useSessionPanel.getState().setOpen(open);
  if (useSessionPanel.getState().userClosed !== !open) {
    useSessionPanel.setState({ userClosed: !open });
    save(useSessionPanel.getState());
  }
  if (open) useSessionPanel.setState((s) => ({ focusRequest: s.focusRequest + 1 }));
}

// openSessionPanelOnConnect shows the new host's files after a connect, unless the panel would not fit beside the open
// docks or the user closed it by hand. It never moves focus away from the new terminal.
export function openSessionPanelOnConnect(fits: boolean): void {
  if (!fits || useSessionPanel.getState().userClosed) return;
  useSessionPanel.getState().setOpen(true);
}

export function resetSessionPanelForTest(): void {
  useSessionPanel.setState({ ...load(), locked: null, last: null, detached: [], focusRequest: 0 });
}
