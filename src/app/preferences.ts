import { useSyncExternalStore } from "react";
import { getResolvedTheme, setThemePreferenceLayers, subscribeTheme, type ThemeMode } from "./theme";
import { DESKTOP } from "../ipc/env";
import { syncApi } from "../ipc/commands";

const THEME_MODE_PREFERENCE_KEY = "appearance.themeMode";

// 账号级偏好的存储接口; Web 走 /auth/preferences, 桌面走同步 IPC。
// 键名与 internal/account/preferences.go 的扁平白名单逐一对齐(如 input.selectionAutoCopy、
// appearance.terminalFontSize、keybinding.newTerminal);注册前只有设备本地值,不冒充账号级覆盖。
export interface AccountPreferenceView {
  defaults: Record<string, unknown>;
  overrides: Record<string, unknown>;
}

export interface AccountPreferenceStore {
  getView(): Promise<AccountPreferenceView>;
  putOverrides(set: Record<string, unknown>): Promise<void>;
  deleteOverrides(keys: string[]): Promise<void>;
}

// createHttpPreferenceStore 是生产用的账号偏好存储; Web 动态引入 authApi,
// 避免 keybindings→preferences→authApi 的静态链把账号模块拉进无关测试的 env mock。
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

// registerAccountPreferenceStore 在登录/注册/初始化完成后注册账号偏好存储,登出/401 时注销。
// 异步读取带代次保护:旧账号晚到的响应不得覆盖新账号已经加载的状态。
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

// getAccountOverrides 返回线上扁平键的账号覆盖记录(如 keybinding.newTerminal)。
export function getAccountOverrides(): Record<string, unknown> {
  return accountOverrides;
}

// getAccountDefaults 返回线上扁平键的服务端全局默认记录(超管 /admin/preferences)。
export function getAccountDefaults(): Record<string, unknown> {
  return accountDefaults;
}

// patchAccountOverrideCache 在写回/清除后同步本地缓存,不必等下一次全量加载。
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

// mergeWithDefaults 把安全全局默认与覆盖合并:账号覆盖优先于设备本地值。
export function mergeWithDefaults<T extends object>(defaults: T, ...layers: (Partial<T> | undefined)[]): T {
  const out = { ...defaults };
  for (const layer of layers) {
    if (layer) Object.assign(out, layer);
  }
  return out;
}

// preferenceValue 按 内置默认 < 服务端全局默认 < 设备本地 < 账号覆盖 的次序解析单个扁平键。
function preferenceValue<T>(key: string, local: T | undefined, builtin: T): T {
  if (key in accountOverrides) return accountOverrides[key] as T;
  if (local !== undefined) return local;
  if (key in accountDefaults) return accountDefaults[key] as T;
  return builtin;
}

// rebuildSnapshots 在账号覆盖加载/清除后,按合并值重建 input/appearance 快照并应用外观。
function rebuildSnapshots(): void {
  prefs = load();
  appearance = loadAppearance();
  applyAppearance();
  for (const listener of listeners) listener();
  for (const listener of appearanceListeners) listener();
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
  // 账号级覆盖:注册后写往 user_setting 后端(M140),实现跨设备同步
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
    merged[field] = preferenceValue(APPEARANCE_PREF_KEYS[field], local[field], DEFAULT_APPEARANCE[field]);
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
  // 账号级覆盖:注册后写往 user_setting 后端(M140),实现跨设备同步
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

// resetAppearancePrefs 清设备本地值并清除账号外观覆盖,回退到内置/服务端全局默认。
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
