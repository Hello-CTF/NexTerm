import {
  getResolvedTheme,
  getThemeMode,
  setThemeMode as persistThemeMode,
} from "../theme";
import { saveThemeModePreference } from "../preferences";
import {
  clampWidth,
  initialLayout,
  initialTerminalPrefs,
  LEFT_MAX,
  LEFT_MIN,
  RIGHT_MAX,
  RIGHT_MIN,
  saveTerminalPrefs,
} from "./persistence";
import { detailForText } from "../../ui/errorText";
import type { UiGet, UiSet, UiState } from "./types";

let toastSeq = 1;

const TOAST_LIMIT = 4;
interface ToastTimer {
  handle: number | undefined;
  remaining: number;
  startedAt: number;
  onExpire: () => void;
}
const toastTimers = new Map<number, ToastTimer>();

function clearToastTimer(id: number): void {
  const timer = toastTimers.get(id);
  if (timer?.handle !== undefined) window.clearTimeout(timer.handle);
  toastTimers.delete(id);
}

function startToastTimer(id: number, ms: number, onExpire: () => void): void {
  const handle = window.setTimeout(onExpire, ms);
  toastTimers.set(id, { handle, remaining: ms, startedAt: Date.now(), onExpire });
}

export function createUiSlice(set: UiSet, get: UiGet) {
  return {
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
    cancelAccountDialogs: () => {
      // Settle, don't just clear: an account switch must resolve pending ask/choice/prompt
      // promises with a cancel result so their callers never mutate across the boundary.
      get().closeTextPrompt(null);
      get().closeAppDialog(false);
      get().closeAppChoice(null);
    },
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
    leftOverride: null,
    setLeftOverride: (m) => set({ leftOverride: m }),
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
    pushToast: (kind, text, action) => {
      const existing = get().toasts.find((t) => t.kind === kind && t.text === text);
      if (existing) get().dismissToast(existing.id);
      const id = toastSeq++;
      set((st) => ({ toasts: [...st.toasts, { id, kind, text, action, detail: kind === "error" ? detailForText(text) ?? undefined : undefined }].slice(-TOAST_LIMIT) }));
      for (const key of [...toastTimers.keys()]) {
        if (!get().toasts.some((t) => t.id === key)) clearToastTimer(key);
      }
      // An error or a toast with a button needs time to read or act on.
      startToastTimer(id, kind === "error" || action ? 8000 : 3500, () => get().dismissToast(id));
    },
    dismissToast: (id) => {
      clearToastTimer(id);
      set((st) => ({ toasts: st.toasts.filter((t) => t.id !== id) }));
    },
    holdToast: (id) => {
      const timer = toastTimers.get(id);
      if (!timer || timer.handle === undefined) return;
      window.clearTimeout(timer.handle);
      timer.handle = undefined;
      timer.remaining = Math.max(1500, timer.remaining - (Date.now() - timer.startedAt));
    },
    releaseToast: (id) => {
      const timer = toastTimers.get(id);
      if (!timer || timer.handle !== undefined) return;
      startToastTimer(id, timer.remaining, timer.onExpire);
    },
  } satisfies Partial<UiState>;
}
