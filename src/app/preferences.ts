import { useSyncExternalStore } from "react";
import { getResolvedTheme, setThemePreferenceLayers, subscribeTheme, type ThemeMode } from "./theme";
import { DESKTOP } from "../ipc/env";
import { syncApi } from "../ipc/commands";

const THEME_MODE_PREFERENCE_KEY = "appearance.themeMode";

// Storage interface for account-level preferences; Web goes through /auth/preferences, desktop through sync IPC.
// Keys align one by one with the flat whitelist in internal/account/preferences.go (e.g. input.selectionAutoCopy,
// appearance.terminalFontSize, keybinding.newTerminal); before registration only device-local values exist, no fake account-level overrides.
export interface AccountPreferenceView {
  defaults: Record<string, unknown>;
  overrides: Record<string, unknown>;
}

export interface AccountPreferenceStore {
  getView(): Promise<AccountPreferenceView>;
  putOverrides(set: Record<string, unknown>): Promise<void>;
  deleteOverrides(keys: string[]): Promise<void>;
}

// createHttpPreferenceStore is the production account preference store; Web dynamically imports authApi,
// so the static chain keybindings→preferences→authApi does not pull the account module into unrelated tests' env mocks.
export function createHttpPreferenceStore(): AccountPreferenceStore {
  return {
    async getView() {
      if (DESKTOP) {
        const status = await syncApi.status();
        if (!status.userId) return { defaults: {}, overrides: {} };
        return syncApi.preferencesGet();
      }
      const { preferencesApi } = await import("../ipc/authApi");
      const view = await preferencesApi.get();
      return { defaults: view.defaults, overrides: view.overrides };
    },
    async putOverrides(set) {
      if (DESKTOP) {
        await syncApi.preferencesUpdate({ set });
        return;
      }
      const { preferencesApi } = await import("../ipc/authApi");
      await preferencesApi.put({ set });
    },
    async deleteOverrides(keys) {
      if (DESKTOP) {
        await syncApi.preferencesUpdate({ clear: keys });
        return;
      }
      const { preferencesApi } = await import("../ipc/authApi");
      await preferencesApi.put({ clear: keys });
    },
  };
}

let accountStore: AccountPreferenceStore | null = null;
let accountDefaults: Record<string, unknown> = {};
let accountOverrides: Record<string, unknown> = {};
let registerGeneration = 0;
const accountListeners = new Set<() => void>();

function syncThemePreferenceLayers(): void {
  setThemePreferenceLayers(accountDefaults[THEME_MODE_PREFERENCE_KEY], accountOverrides[THEME_MODE_PREFERENCE_KEY]);
}

// registerAccountPreferenceStore registers the account preference store after login/register/init completes, and unregisters on logout/401.
// Async reads carry generation protection: late responses from an old account must not overwrite the new account's loaded state.
export function registerAccountPreferenceStore(store: AccountPreferenceStore | null): void {
  registerGeneration += 1;
  const generation = registerGeneration;
  accountStore = store;
  accountDefaults = {};
  accountOverrides = {};
  syncThemePreferenceLayers();
  rebuildSnapshots();
  for (const listener of accountListeners) listener();
  if (!store) return;
  void store
    .getView()
    .then((view) => {
      if (generation !== registerGeneration || store !== accountStore) return;
      accountDefaults = view.defaults;
      accountOverrides = view.overrides;
      syncThemePreferenceLayers();
      rebuildSnapshots();
      for (const listener of accountListeners) listener();
    })
    .catch(() => undefined);
}

export function getAccountPreferenceStore(): AccountPreferenceStore | null {
  return accountStore;
}

// getAccountOverrides returns the account override record of wire flat keys (e.g. keybinding.newTerminal).
export function getAccountOverrides(): Record<string, unknown> {
  return accountOverrides;
}

// getAccountDefaults returns the server global default record of wire flat keys (superadmin /admin/preferences).
export function getAccountDefaults(): Record<string, unknown> {
  return accountDefaults;
}

// patchAccountOverrideCache syncs the local cache after write-back/clear, without waiting for the next full load.
export function patchAccountOverrideCache(set: Record<string, unknown>, clear: string[]): void {
  for (const key of clear) delete accountOverrides[key];
  Object.assign(accountOverrides, set);
  syncThemePreferenceLayers();
}

export function saveThemeModePreference(mode: ThemeMode): void {
  if (!accountStore) return;
  patchAccountOverrideCache({ [THEME_MODE_PREFERENCE_KEY]: mode }, []);
  void accountStore.putOverrides({ [THEME_MODE_PREFERENCE_KEY]: mode }).catch(() => undefined);
}

export function subscribeAccountOverrides(listener: () => void): () => void {
  accountListeners.add(listener);
  return () => {
    accountListeners.delete(listener);
  };
}

// mergeWithDefaults merges the safe global defaults with overrides: account overrides take precedence over device-local values.
export function mergeWithDefaults<T extends object>(defaults: T, ...layers: (Partial<T> | undefined)[]): T {
  const out = { ...defaults };
  for (const layer of layers) {
    if (layer) Object.assign(out, layer);
  }
  return out;
}

// preferenceValue resolves a single flat key in the order builtin default < server global default < device local < account override.
function preferenceValue<T>(key: string, local: T | undefined, builtin: T): T {
  if (key in accountOverrides) return accountOverrides[key] as T;
  if (local !== undefined) return local;
  if (key in accountDefaults) return accountDefaults[key] as T;
  return builtin;
}

// rebuildSnapshots rebuilds the input/appearance/terminal snapshots from merged values after account overrides load/clear, and applies appearance.
function rebuildSnapshots(): void {
  prefs = load();
  appearance = loadAppearance();
  terminalPrefs = loadTerminal();
  applyAppearance();
  for (const listener of listeners) listener();
  for (const listener of appearanceListeners) listener();
  for (const listener of terminalListeners) listener();
}

export interface InputPrefs {
  selectionAutoCopy: boolean;
}

const STORAGE_KEY = "nexterm.inputPrefs.v1";
const INPUT_DEFAULTS: InputPrefs = { selectionAutoCopy: false };
const INPUT_SELECTION_AUTO_COPY_KEY = "input.selectionAutoCopy";

function load(): InputPrefs {
  let local: Partial<InputPrefs> = {};
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    local = raw ? (JSON.parse(raw) as Partial<InputPrefs>) : {};
  } catch {
    local = {};
  }
  const value = preferenceValue<unknown>(INPUT_SELECTION_AUTO_COPY_KEY, local.selectionAutoCopy, INPUT_DEFAULTS.selectionAutoCopy);
  return { selectionAutoCopy: value === true };
}

let prefs: InputPrefs = load();
const listeners = new Set<() => void>();

export function getInputPrefs(): InputPrefs {
  return prefs;
}

export function setSelectionAutoCopy(value: boolean): void {
  if (prefs.selectionAutoCopy === value) return;
  prefs = { ...prefs, selectionAutoCopy: value };
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(prefs));
  } catch {
  }
  // Account-level override: after registration, written to the user_setting backend (M140) for cross-device sync
  if (accountStore) {
    void accountStore.putOverrides({ [INPUT_SELECTION_AUTO_COPY_KEY]: value }).catch(() => undefined);
    patchAccountOverrideCache({ [INPUT_SELECTION_AUTO_COPY_KEY]: value }, []);
  }
  for (const listener of listeners) listener();
}

export function subscribeInputPrefs(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useInputPrefs(): InputPrefs {
  return useSyncExternalStore(subscribeInputPrefs, getInputPrefs);
}

export type TerminalCloseAction = "ask" | "kill" | "detach";

export interface TerminalPrefs {
  closeAction: TerminalCloseAction;
}

const TERMINAL_CLOSE_STORAGE_KEY = "nexterm.terminalClose.v1";
const TERMINAL_CLOSE_ACTION_KEY = "terminal.closeAction";
const TERMINAL_DEFAULTS: TerminalPrefs = { closeAction: "ask" };

function loadTerminal(): TerminalPrefs {
  let local: Partial<TerminalPrefs> = {};
  try {
    const raw = localStorage.getItem(TERMINAL_CLOSE_STORAGE_KEY);
    local = raw ? (JSON.parse(raw) as Partial<TerminalPrefs>) : {};
  } catch {
    local = {};
  }
  const value = preferenceValue<unknown>(TERMINAL_CLOSE_ACTION_KEY, local.closeAction, TERMINAL_DEFAULTS.closeAction);
  return { closeAction: value === "kill" || value === "detach" ? value : TERMINAL_DEFAULTS.closeAction };
}

let terminalPrefs: TerminalPrefs = loadTerminal();
const terminalListeners = new Set<() => void>();

export function getTerminalPrefs(): TerminalPrefs {
  return terminalPrefs;
}

export function setTerminalCloseAction(value: TerminalCloseAction): void {
  if (terminalPrefs.closeAction === value) return;
  terminalPrefs = { ...terminalPrefs, closeAction: value };
  try {
    localStorage.setItem(TERMINAL_CLOSE_STORAGE_KEY, JSON.stringify(terminalPrefs));
  } catch {
  }
  // Account-level override: after registration, written to the user_setting backend for cross-device sync
  if (accountStore) {
    void accountStore.putOverrides({ [TERMINAL_CLOSE_ACTION_KEY]: value }).catch(() => undefined);
    patchAccountOverrideCache({ [TERMINAL_CLOSE_ACTION_KEY]: value }, []);
  }
  for (const listener of terminalListeners) listener();
}

export function subscribeTerminalPrefs(listener: () => void): () => void {
  terminalListeners.add(listener);
  return () => {
    terminalListeners.delete(listener);
  };
}

export function useTerminalPrefs(): TerminalPrefs {
  return useSyncExternalStore(subscribeTerminalPrefs, getTerminalPrefs);
}

export type UiFontPreset = number;
export type TerminalThemePref = "dark" | "light" | "interface";
export type ResolvedTerminalTheme = "dark" | "light";

export interface AppearancePrefs {
  uiFontPreset: UiFontPreset;
  uiFontScale: number;
  terminalFontSize: number;
  terminalTheme: TerminalThemePref;
}

export const UI_FONT_PRESETS: readonly UiFontPreset[] = [12, 13, 14.5];
export const UI_FONT_SCALE_STEPS: readonly number[] = [1, 1.25, 1.5, 1.75, 2];
export const UI_FONT_SIZE_MIN = 8;
export const UI_FONT_SIZE_MAX = 32;
export const TERMINAL_FONT_SIZE_MIN = 8;
export const TERMINAL_FONT_SIZE_MAX = 32;

const APPEARANCE_KEY = "nexterm.appearance.v1";
const DEFAULT_APPEARANCE: AppearancePrefs = {
  uiFontPreset: 13,
  uiFontScale: 1,
  terminalFontSize: 13,
  terminalTheme: "dark",
};

const MOBILE_DEFAULT_TERMINAL_FONT_SIZE = 12;

// Same detection as isMobile in platform.ts; platform is not imported because several terminal tests partially mock platform without exporting isMobile.
function isMobileDevice(): boolean {
  return typeof navigator !== "undefined" && /iPhone|iPad|Android/i.test(navigator.userAgent);
}

// builtinAppearance returns the builtin default of a field; the mobile terminal default font size is one step smaller than desktop.
function builtinAppearance(field: keyof AppearancePrefs): number | string {
  if (field === "terminalFontSize" && isMobileDevice()) return MOBILE_DEFAULT_TERMINAL_FONT_SIZE;
  return DEFAULT_APPEARANCE[field];
}

const APPEARANCE_PREF_KEYS: Record<keyof AppearancePrefs, string> = {
  uiFontPreset: "appearance.uiFontPreset",
  uiFontScale: "appearance.uiFontScale",
  terminalFontSize: "appearance.terminalFontSize",
  terminalTheme: "appearance.terminalTheme",
};

function parsePreset(value: unknown): UiFontPreset {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return DEFAULT_APPEARANCE.uiFontPreset;
  }
  const clamped = Math.min(UI_FONT_SIZE_MAX, Math.max(UI_FONT_SIZE_MIN, value));
  return Math.round(clamped * 2) / 2;
}

function parseScale(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return DEFAULT_APPEARANCE.uiFontScale;
  }
  const clamped = Math.min(2, Math.max(1, value));
  return UI_FONT_SCALE_STEPS.reduce((best, step) =>
    Math.abs(step - clamped) < Math.abs(best - clamped) ? step : best,
  );
}

function parseTerminalFontSize(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return DEFAULT_APPEARANCE.terminalFontSize;
  }
  return Math.min(
    TERMINAL_FONT_SIZE_MAX,
    Math.max(TERMINAL_FONT_SIZE_MIN, Math.round(value)),
  );
}

function parseTerminalTheme(value: unknown): TerminalThemePref {
  return value === "dark" || value === "light" || value === "interface"
    ? value
    : DEFAULT_APPEARANCE.terminalTheme;
}

function loadAppearance(): AppearancePrefs {
  let local: Record<string, unknown> = {};
  try {
    const raw = localStorage.getItem(APPEARANCE_KEY);
    local = raw ? (JSON.parse(raw) as Record<string, unknown>) : {};
  } catch {
    local = {};
  }
  const merged = {} as Record<keyof AppearancePrefs, unknown>;
  for (const field of Object.keys(APPEARANCE_PREF_KEYS) as (keyof AppearancePrefs)[]) {
    merged[field] = preferenceValue(APPEARANCE_PREF_KEYS[field], local[field], builtinAppearance(field));
  }
  return {
    uiFontPreset: parsePreset(merged.uiFontPreset),
    uiFontScale: parseScale(merged.uiFontScale),
    terminalFontSize: parseTerminalFontSize(merged.terminalFontSize),
    terminalTheme: parseTerminalTheme(merged.terminalTheme),
  };
}

let appearance: AppearancePrefs = loadAppearance();
const appearanceListeners = new Set<() => void>();

export function getAppearancePrefs(): AppearancePrefs {
  return appearance;
}

export function getResolvedTerminalTheme(): ResolvedTerminalTheme {
  return appearance.terminalTheme === "interface"
    ? getResolvedTheme()
    : appearance.terminalTheme;
}

export function uiTextFactor(prefs: AppearancePrefs = appearance): number {
  return (prefs.uiFontScale * prefs.uiFontPreset) / 13;
}

function applyAppearance(): void {
  if (typeof document === "undefined") return;
  const root = document.documentElement;
  root.style.setProperty("--nx-ui-font-size", `${appearance.uiFontPreset}px`);
  root.style.setProperty("--nx-ui-scale", String(appearance.uiFontScale));
  root.style.setProperty("--nx-ui-text-factor", String(uiTextFactor()));
  root.dataset.nxTermTheme = getResolvedTerminalTheme();
}

function notifyAppearance(): void {
  for (const listener of appearanceListeners) listener();
}

function appearanceEquals(a: AppearancePrefs, b: AppearancePrefs): boolean {
  return (
    a.uiFontPreset === b.uiFontPreset &&
    a.uiFontScale === b.uiFontScale &&
    a.terminalFontSize === b.terminalFontSize &&
    a.terminalTheme === b.terminalTheme
  );
}

function commitAppearance(next: AppearancePrefs): void {
  const changedFields = (Object.keys(APPEARANCE_PREF_KEYS) as (keyof AppearancePrefs)[]).filter(
    (field) => next[field] !== appearance[field],
  );
  appearance = next;
  try {
    localStorage.setItem(APPEARANCE_KEY, JSON.stringify(next));
  } catch {
  }
  // Account-level override: after registration, written to the user_setting backend (M140) for cross-device sync
  if (accountStore && changedFields.length > 0) {
    const set: Record<string, unknown> = {};
    for (const field of changedFields) set[APPEARANCE_PREF_KEYS[field]] = next[field];
    void accountStore.putOverrides(set).catch(() => undefined);
    patchAccountOverrideCache(set, []);
  }
  applyAppearance();
  if (changedFields.length > 0) notifyAppearance();
}

export function setUiFontPreset(value: UiFontPreset): void {
  const next = parsePreset(value);
  if (appearance.uiFontPreset === next) return;
  commitAppearance({ ...appearance, uiFontPreset: next });
}

export function setUiFontScale(value: number): void {
  const next = parseScale(value);
  if (appearance.uiFontScale === next) return;
  commitAppearance({ ...appearance, uiFontScale: next });
}

export function setTerminalFontSize(value: number): void {
  const next = parseTerminalFontSize(value);
  if (appearance.terminalFontSize === next) return;
  commitAppearance({ ...appearance, terminalFontSize: next });
}

export function setTerminalTheme(value: TerminalThemePref): void {
  if (appearance.terminalTheme === value) return;
  commitAppearance({ ...appearance, terminalTheme: value });
}

// resetAppearancePrefs clears device-local values and removes account appearance overrides, falling back to builtin/server global defaults.
export function resetAppearancePrefs(): void {
  try {
    localStorage.removeItem(APPEARANCE_KEY);
  } catch {
  }
  const keys = [...Object.values(APPEARANCE_PREF_KEYS), THEME_MODE_PREFERENCE_KEY];
  if (accountStore) {
    void accountStore.deleteOverrides(keys).catch(() => undefined);
  }
  patchAccountOverrideCache({}, keys);
  const next = loadAppearance();
  const changed = !appearanceEquals(appearance, next);
  appearance = next;
  applyAppearance();
  if (changed) notifyAppearance();
}

export function subscribeAppearancePrefs(listener: () => void): () => void {
  appearanceListeners.add(listener);
  return () => {
    appearanceListeners.delete(listener);
  };
}

export function useAppearancePrefs(): AppearancePrefs {
  return useSyncExternalStore(subscribeAppearancePrefs, getAppearancePrefs);
}

export function useResolvedTerminalTheme(): ResolvedTerminalTheme {
  return useSyncExternalStore(subscribeAppearancePrefs, getResolvedTerminalTheme);
}

applyAppearance();
subscribeTheme(() => {
  if (appearance.terminalTheme !== "interface") return;
  applyAppearance();
  notifyAppearance();
});
