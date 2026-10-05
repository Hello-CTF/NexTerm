import { useSyncExternalStore } from "react";
import { getResolvedTheme, subscribeTheme } from "./theme";

export interface InputPrefs {
  selectionAutoCopy: boolean;
}

const STORAGE_KEY = "nexterm.inputPrefs.v1";

function load(): InputPrefs {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return { selectionAutoCopy: false };
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") return { selectionAutoCopy: false };
    return { selectionAutoCopy: (parsed as { selectionAutoCopy?: unknown }).selectionAutoCopy === true };
  } catch {
    return { selectionAutoCopy: false };
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
    if (!raw) return { ...DEFAULT_APPEARANCE };
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") return { ...DEFAULT_APPEARANCE };
    const o = parsed as Record<string, unknown>;
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
