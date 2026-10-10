export type ThemeMode = "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";

const STORAGE_KEY = "nexterm.theme.v1";
const LIGHT_QUERY = "(prefers-color-scheme: light)";

type ThemeListener = (mode: ThemeMode, resolved: ResolvedTheme) => void;

const listeners = new Set<ThemeListener>();

let localMode: ThemeMode | null = loadMode();
let defaultMode: ThemeMode | null = null;
let accountMode: ThemeMode | null = null;
let media: MediaQueryList | null = null;

function parseThemeMode(value: unknown): ThemeMode | null {
  return value === "light" || value === "dark" || value === "system" ? value : null;
}

function loadMode(): ThemeMode | null {
  try {
    return parseThemeMode(localStorage.getItem(STORAGE_KEY));
  } catch {
    return null;
  }
}

function currentMode(): ThemeMode {
  return accountMode ?? localMode ?? defaultMode ?? "system";
}

function systemTheme(): ResolvedTheme {
  if (typeof window === "undefined") return "dark";
  return window.matchMedia(LIGHT_QUERY).matches ? "light" : "dark";
}

function resolve(m: ThemeMode): ResolvedTheme {
  return m === "system" ? systemTheme() : m;
}

function apply(): ResolvedTheme {
  const resolved = resolve(currentMode());
  if (typeof document !== "undefined") {
    document.documentElement.dataset.nxTheme = resolved;
  }
  return resolved;
}

function emit(): void {
  const mode = currentMode();
  const resolved = resolve(mode);
  for (const listener of listeners) listener(mode, resolved);
}

export function getThemeMode(): ThemeMode {
  return currentMode();
}

export function getResolvedTheme(): ResolvedTheme {
  return resolve(currentMode());
}

export function setThemeMode(next: ThemeMode): void {
  localMode = next;
  accountMode = null;
  try {
    localStorage.setItem(STORAGE_KEY, next);
  } catch {
  }
  apply();
  emit();
}

export function setThemePreferenceLayers(defaultValue: unknown, accountValue: unknown): void {
  const nextDefault = parseThemeMode(defaultValue);
  const nextAccount = parseThemeMode(accountValue);
  if (nextDefault === defaultMode && nextAccount === accountMode) return;
  defaultMode = nextDefault;
  accountMode = nextAccount;
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
  if (typeof window === "undefined") return;
  if (media) return;
  media = window.matchMedia(LIGHT_QUERY);
  media.addEventListener("change", () => {
    if (currentMode() !== "system") return;
    apply();
    emit();
  });
}
