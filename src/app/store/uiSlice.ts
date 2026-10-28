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
import type { UiGet, UiSet, UiState } from "./types";

let toastSeq = 1;

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
      set((st) => ({ toasts: [...st.toasts, { id, kind, text, action }] }));
      // An error or a toast with a button needs time to read or act on.
      window.setTimeout(() => get().dismissToast(id), kind === "error" || action ? 8000 : 3500);
    },
    dismissToast: (id) =>
      set((st) => ({ toasts: st.toasts.filter((t) => t.id !== id) })),
  } satisfies Partial<UiState>;
}
