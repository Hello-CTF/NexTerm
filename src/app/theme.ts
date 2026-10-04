export type ThemeMode = "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";

const STORAGE_KEY = "nexterm.theme.v1";
const LIGHT_QUERY = "(prefers-color-scheme: light)";

type ThemeListener = (mode: ThemeMode, resolved: ResolvedTheme) => void;

const listeners = new Set<ThemeListener>();

let mode: ThemeMode = loadMode();
let media: MediaQueryList | null = null;

function loadMode(): ThemeMode {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    return saved === "light" || saved === "dark" || saved === "system" ? saved : "system";
  } catch {
    return "system";
  }
}

function systemTheme(): ResolvedTheme {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return "dark";
  return window.matchMedia(LIGHT_QUERY).matches ? "light" : "dark";
}

function resolve(m: ThemeMode): ResolvedTheme {
  return m === "system" ? systemTheme() : m;
}

function apply(): ResolvedTheme {
  const resolved = resolve(mode);
  if (typeof document !== "undefined") {
    document.documentElement.dataset.nxTheme = resolved;
  }
  return resolved;
}

function emit(): void {
  const resolved = resolve(mode);
  for (const listener of listeners) listener(mode, resolved);
}

export function getThemeMode(): ThemeMode {
  return mode;
}

export function getResolvedTheme(): ResolvedTheme {
  return resolve(mode);
}

export function setThemeMode(next: ThemeMode): void {
  mode = next;
  try {
    localStorage.setItem(STORAGE_KEY, next);
  } catch {
  }
  apply();
  emit();
}

export function subscribeTheme(listener: ThemeListener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function initTheme(): void {
  apply();
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
  if (media) return;
  media = window.matchMedia(LIGHT_QUERY);
  media.addEventListener("change", () => {
    if (mode !== "system") return;
    apply();
    emit();
  });
}
