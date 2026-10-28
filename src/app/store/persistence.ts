import type { TakeoverState } from "./types";

export const LEFT_MIN = 180;
export const LEFT_MAX = 560;
export const RIGHT_MIN = 300;
export const RIGHT_MAX = 760;
const LEFT_DEFAULT = 248;
const RIGHT_DEFAULT = 352;
const LAYOUT_KEY = "nexterm.layout.v1";
const TERMINAL_PREFS_KEY = "nexterm.terminal.v1";

interface TerminalPrefs {
  osc52: boolean;
}

function loadTerminalPrefs(): TerminalPrefs {
  try {
    const raw = localStorage.getItem(TERMINAL_PREFS_KEY);
    if (!raw) return { osc52: false };
    const o = JSON.parse(raw) as { osc52?: unknown };
    return { osc52: o.osc52 === true };
  } catch {
    return { osc52: false };
  }
}

export function saveTerminalPrefs(p: TerminalPrefs): void {
  try {
    localStorage.setItem(TERMINAL_PREFS_KEY, JSON.stringify(p));
  } catch {
  }
}

export function clampWidth(w: number, min: number, max: number): number {
  if (!Number.isFinite(w)) return min;
  return Math.min(max, Math.max(min, Math.round(w)));
}

export const LEFT_WIDTH_RANGE = { min: LEFT_MIN, max: LEFT_MAX, default: LEFT_DEFAULT };
export const RIGHT_WIDTH_RANGE = { min: RIGHT_MIN, max: RIGHT_MAX, default: RIGHT_DEFAULT };

interface LocalLayout {
  leftWidth: number;
  rightWidth: number;
  rightOpen: boolean;
}

// Touch tablets below this width keep the AI dock closed on first run so the terminal is not squeezed.
const AI_DOCK_DEFAULT_MIN_TOUCH_WIDTH = 1280;

export function defaultRightOpen(width: number, coarsePointer: boolean): boolean {
  return !(coarsePointer && width < AI_DOCK_DEFAULT_MIN_TOUCH_WIDTH);
}

function initialRightOpen(): boolean {
  try {
    return defaultRightOpen(window.innerWidth, window.matchMedia("(pointer: coarse)").matches);
  } catch {
    return true;
  }
}

function loadLayout(): LocalLayout {
  const fallback = { leftWidth: LEFT_DEFAULT, rightWidth: RIGHT_DEFAULT, rightOpen: initialRightOpen() };
  try {
    const raw = localStorage.getItem(LAYOUT_KEY);
    if (!raw) return fallback;
    const o = JSON.parse(raw) as Partial<LocalLayout>;
    return {
      leftWidth: clampWidth(o.leftWidth ?? LEFT_DEFAULT, LEFT_MIN, LEFT_MAX),
      rightWidth: clampWidth(o.rightWidth ?? RIGHT_DEFAULT, RIGHT_MIN, RIGHT_MAX),
      rightOpen: typeof o.rightOpen === "boolean" ? o.rightOpen : initialRightOpen(),
    };
  } catch {
    return fallback;
  }
}

export function saveLayout(w: LocalLayout): void {
  try {
    localStorage.setItem(LAYOUT_KEY, JSON.stringify(w));
  } catch {
  }
}

const TAKEOVER_KEY = "nexterm.takeover.v1";

function loadPersistedTakeover(): TakeoverState | null {
  try {
    const raw = localStorage.getItem(TAKEOVER_KEY);
    if (!raw) return null;
    const o = JSON.parse(raw) as Partial<TakeoverState>;
    if (
      typeof o.tabId !== "string" ||
      typeof o.token !== "string" ||
      typeof o.task !== "string" ||
      typeof o.allowWrite !== "boolean" ||
      typeof o.startedAt !== "number"
    ) {
      localStorage.removeItem(TAKEOVER_KEY);
      return null;
    }
    return {
      tabId: o.tabId,
      jobId: typeof o.jobId === "string" ? o.jobId : undefined,
      token: o.token,
      task: o.task,
      allowWrite: o.allowWrite,
      startedAt: o.startedAt,
    };
  } catch {
    return null;
  }
}

let restoredTakeover: TakeoverState | null = loadPersistedTakeover();

export function consumeRestoredTakeover(): TakeoverState | null {
  const current = restoredTakeover;
  restoredTakeover = null;
  return current;
}

export function persistTakeover(t: TakeoverState | null): void {
  try {
    if (t) localStorage.setItem(TAKEOVER_KEY, JSON.stringify(t));
    else localStorage.removeItem(TAKEOVER_KEY);
  } catch {
  }
}

export const initialLayout = loadLayout();
export const initialTerminalPrefs = loadTerminalPrefs();

export function peekRestoredTakeover(): TakeoverState | null {
  return restoredTakeover;
}

export function clearRestoredTakeover(): void {
  restoredTakeover = null;
}
