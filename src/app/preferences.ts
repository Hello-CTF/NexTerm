import { useSyncExternalStore } from "react";
import { getResolvedTheme, subscribeTheme } from "./theme";

// 账号级偏好覆盖的存储接口;M140 提供 user_setting 后端后由其实现并注册。
// 注册前只有设备本地值,不冒充账号级覆盖。
export interface AccountPreferenceStore {
  getOverrides(): Promise<Record<string, unknown>>;
  putOverride(key: string, value: unknown): Promise<void>;
  deleteOverride(key: string): Promise<void>;
}

let accountStore: AccountPreferenceStore | null = null;
let accountOverrides: Record<string, unknown> = {};
const accountListeners = new Set<() => void>();

export function registerAccountPreferenceStore(store: AccountPreferenceStore | null): void {
  accountStore = store;
  accountOverrides = {};
  if (store) {
    void store
      .getOverrides()
      .then((overrides) => {
        accountOverrides = overrides;
        for (const listener of accountListeners) listener();
      })
      .catch(() => undefined);
  }
}

export function getAccountPreferenceStore(): AccountPreferenceStore | null {
  return accountStore;
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

function accountOverride<T>(key: string): Partial<T> | undefined {
  const value = accountOverrides[key];
  return value && typeof value === "object" ? (value as Partial<T>) : undefined;
}

export interface InputPrefs {
  selectionAutoCopy: boolean;
}

const STORAGE_KEY = "nexterm.inputPrefs.v1";
const INPUT_DEFAULTS: InputPrefs = { selectionAutoCopy: false };

function load(): InputPrefs {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    const local = raw ? (JSON.parse(raw) as Partial<InputPrefs>) : {};
    return mergeWithDefaults(INPUT_DEFAULTS, local, accountOverride<InputPrefs>("inputPrefs"));
  } catch {
    return { ...INPUT_DEFAULTS };
  }
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
    void accountStore.putOverride("inputPrefs", { selectionAutoCopy: value }).catch(() => undefined);
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

export type UiFontPreset = 12 | 13 | 14.5;
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
export const TERMINAL_FONT_SIZE_MIN = 12;
export const TERMINAL_FONT_SIZE_MAX = 18;

const APPEARANCE_KEY = "nexterm.appearance.v1";
const DEFAULT_APPEARANCE: AppearancePrefs = {
  uiFontPreset: 13,
  uiFontScale: 1,
  terminalFontSize: 13,
  terminalTheme: "dark",
};

function parsePreset(value: unknown): UiFontPreset {
  return UI_FONT_PRESETS.includes(value as UiFontPreset)
    ? (value as UiFontPreset)
    : DEFAULT_APPEARANCE.uiFontPreset;
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
  try {
    const raw = localStorage.getItem(APPEARANCE_KEY);
    const local = raw ? (JSON.parse(raw) as Record<string, unknown>) : {};
    const o = { ...local, ...accountOverride<Record<string, unknown>>("appearancePrefs") };
    return {
      uiFontPreset: parsePreset(o.uiFontPreset),
      uiFontScale: parseScale(o.uiFontScale),
      terminalFontSize: parseTerminalFontSize(o.terminalFontSize),
      terminalTheme: parseTerminalTheme(o.terminalTheme),
    };
  } catch {
    return { ...DEFAULT_APPEARANCE };
  }
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
  const changed = !appearanceEquals(appearance, next);
  appearance = next;
  try {
    localStorage.setItem(APPEARANCE_KEY, JSON.stringify(next));
  } catch {
  }
  // 账号级覆盖:注册后写往 user_setting 后端(M140),实现跨设备同步
  if (accountStore) {
    void accountStore.putOverride("appearancePrefs", next).catch(() => undefined);
  }
  applyAppearance();
  if (changed) notifyAppearance();
}

export function setUiFontPreset(value: UiFontPreset): void {
  if (appearance.uiFontPreset === value) return;
  commitAppearance({ ...appearance, uiFontPreset: value });
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

export function resetAppearancePrefs(): void {
  const changed = !appearanceEquals(appearance, DEFAULT_APPEARANCE);
  appearance = { ...DEFAULT_APPEARANCE };
  try {
    localStorage.removeItem(APPEARANCE_KEY);
  } catch {
  }
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
