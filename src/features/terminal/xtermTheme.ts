import type { ITheme } from "@xterm/xterm";
import type { ResolvedTerminalTheme, TerminalContrast, TerminalFontFamily } from "../../app/preferences";
import { BUNDLED_SCHEMES, mixHex, schemeKind, type CustomTerminalScheme, type SchemeKind, type SchemePalette } from "./terminalSchemes";

// Colour emoji families follow the generic family as glyph fallbacks only. The UI's
// "NexTerm Emoji" alias (styles/base.css) is deliberately absent: terminal programs lay out
// text-default symbols such as check marks, warning signs, arrows and boxes as one-cell
// glyphs, which the primary monospace font must keep drawing.
export const EMOJI_FONT_FALLBACKS = "'Apple Color Emoji', 'Segoe UI Emoji', 'Segoe UI Symbol', 'Noto Color Emoji'";

function terminalStack(families: string): string {
  return `${families}, ${EMOJI_FONT_FALLBACKS}`;
}

export const TERMINAL_FONT_STACKS: Record<TerminalFontFamily, string> = {
  cascadia: terminalStack("'Cascadia Mono', 'Cascadia Code', Consolas, 'Courier New', monospace"),
  system: terminalStack("ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, 'Liberation Mono', 'Courier New', monospace"),
  "jetbrains-mono": terminalStack("'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, Consolas, 'Courier New', monospace"),
  "fira-code": terminalStack("'Fira Code', ui-monospace, SFMono-Regular, Menlo, Consolas, 'Courier New', monospace"),
  "source-code-pro": terminalStack("'Source Code Pro', ui-monospace, SFMono-Regular, Menlo, Consolas, 'Courier New', monospace"),
  menlo: terminalStack("Menlo, Monaco, ui-monospace, 'Courier New', monospace"),
  consolas: terminalStack("Consolas, 'Courier New', monospace"),
};

export const XTERM_DARK_THEME = {
  background: "#101217",
  foreground: "#c6cbd6",
  cursorAccent: "#101217",
  selectionBackground: "#2c3e5d",
  scrollbarSliderBackground: "#31363f",
  scrollbarSliderHoverBackground: "#3e444f",
  scrollbarSliderActiveBackground: "#4d5563",
  black: "#101217",
  brightBlack: "#5c6472",
  red: "#e87b7b",
  brightRed: "#f0a0a0",
  green: "#7fd6a4",
  brightGreen: "#a3e6bd",
  yellow: "#e9c489",
  brightYellow: "#f0d6a4",
  blue: "#79a8f8",
  brightBlue: "#9dc0fb",
  magenta: "#b79ce8",
  brightMagenta: "#cdb9f0",
  cyan: "#7fc7d6",
  brightCyan: "#a3dbe6",
  white: "#c6cbd6",
  brightWhite: "#eef1f6",
};

export const XTERM_LIGHT_THEME = {
  background: "#f4f6fa",
  foreground: "#2f3642",
  cursorAccent: "#f4f6fa",
  selectionBackground: "#c9d6f2",
  scrollbarSliderBackground: "#ccd3de",
  scrollbarSliderHoverBackground: "#aeb7c5",
  scrollbarSliderActiveBackground: "#8a94a6",
  black: "#2f3642",
  brightBlack: "#5d6572",
  red: "#c23636",
  brightRed: "#a12222",
  green: "#1a6b41",
  brightGreen: "#226e46",
  yellow: "#8a5a00",
  brightYellow: "#96660a",
  blue: "#3c59cc",
  brightBlue: "#324a9e",
  magenta: "#6440b8",
  brightMagenta: "#4c2f96",
  cyan: "#0e5a9e",
  brightCyan: "#0b4a80",
  white: "#5d6572",
  brightWhite: "#10141b",
};

export const XTERM_DARK_HIGH_CONTRAST_THEME = {
  ...XTERM_DARK_THEME,
  background: "#08090c",
  foreground: "#f1f3f6",
  cursorAccent: "#08090c",
  black: "#08090c",
  brightBlack: "#b7bfca",
  red: "#ee9f9f",
  brightRed: "#f7caca",
  green: "#aae4c3",
  brightGreen: "#cff2dc",
  yellow: "#edd0a0",
  brightYellow: "#f6e7ca",
  blue: "#94baf9",
  brightBlue: "#c4d9fd",
  magenta: "#bda4ea",
  brightMagenta: "#dbcdf4",
  cyan: "#aadae4",
  brightCyan: "#cfecf2",
  white: "#f1f3f6",
  brightWhite: "#ffffff",
};

export const XTERM_DARK_MAXIMUM_CONTRAST_THEME = {
  ...XTERM_DARK_THEME,
  background: "#000000",
  foreground: "#ffffff",
  cursorAccent: "#000000",
  black: "#000000",
  brightBlack: "#e0e4ea",
  red: "#f4c2c2",
  brightRed: "#fbe5e5",
  green: "#c9eed9",
  brightGreen: "#e7f8ee",
  yellow: "#f4e1c3",
  brightYellow: "#fbf3e5",
  blue: "#bbd3fb",
  brightBlue: "#e2ecfe",
  magenta: "#d5c5f2",
  brightMagenta: "#ede6fa",
  cyan: "#c9e7ee",
  brightCyan: "#e7f5f8",
  white: "#ffffff",
  brightWhite: "#ffffff",
};

export const XTERM_LIGHT_HIGH_CONTRAST_THEME = {
  ...XTERM_LIGHT_THEME,
  background: "#ffffff",
  foreground: "#171a21",
  cursorAccent: "#ffffff",
  black: "#171a21",
  brightBlack: "#3f4652",
  red: "#601b1b",
  brightRed: "#4c1010",
  green: "#18623c",
  brightGreen: "#16462d",
  yellow: "#7a5000",
  brightYellow: "#563b06",
  blue: "#192861",
  brightBlue: "#162146",
  magenta: "#31205b",
  brightMagenta: "#231646",
  cyan: "#0a4070",
  brightCyan: "#073155",
  white: "#3f4652",
  brightWhite: "#000000",
};

export const XTERM_LIGHT_MAXIMUM_CONTRAST_THEME = {
  ...XTERM_LIGHT_THEME,
  background: "#ffffff",
  foreground: "#000000",
  cursorAccent: "#ffffff",
  black: "#000000",
  brightBlack: "#1d232c",
  red: "#381010",
  brightRed: "#2a0909",
  green: "#0e3923",
  brightGreen: "#0c2719",
  yellow: "#472f00",
  brightYellow: "#302103",
  blue: "#0f1739",
  brightBlue: "#0c1227",
  magenta: "#1d1235",
  brightMagenta: "#140c27",
  cyan: "#062542",
  brightCyan: "#041b2f",
  white: "#1d232c",
  brightWhite: "#000000",
};

const XTERM_THEMES = {
  dark: {
    standard: XTERM_DARK_THEME,
    high: XTERM_DARK_HIGH_CONTRAST_THEME,
    maximum: XTERM_DARK_MAXIMUM_CONTRAST_THEME,
  },
  light: {
    standard: XTERM_LIGHT_THEME,
    high: XTERM_LIGHT_HIGH_CONTRAST_THEME,
    maximum: XTERM_LIGHT_MAXIMUM_CONTRAST_THEME,
  },
};

export function xtermThemeFor(resolved: ResolvedTerminalTheme, contrast: TerminalContrast = "standard") {
  return XTERM_THEMES[resolved][contrast];
}

export const DEFAULT_DARK_SCHEME_ID = "nexterm-dark";
export const DEFAULT_LIGHT_SCHEME_ID = "nexterm-light";

export interface TerminalScheme {
  id: string;
  name: string;
  kind: SchemeKind;
  palette: SchemePalette;
  custom: boolean;
}

const ANSI_THEME_KEYS = [
  "black", "red", "green", "yellow", "blue", "magenta", "cyan", "white",
  "brightBlack", "brightRed", "brightGreen", "brightYellow", "brightBlue", "brightMagenta", "brightCyan", "brightWhite",
] as const;

function nextermScheme(id: string, kind: SchemeKind): TerminalScheme {
  const theme = XTERM_THEMES[kind].standard;
  return {
    id,
    name: kind === "dark" ? "NexTerm Dark" : "NexTerm Light",
    kind,
    palette: {
      background: theme.background,
      foreground: theme.foreground,
      selection: theme.selectionBackground,
      ansi: ANSI_THEME_KEYS.map((key) => theme[key]),
    },
    custom: false,
  };
}

const NEXTERM_SCHEMES: readonly TerminalScheme[] = [
  nextermScheme(DEFAULT_DARK_SCHEME_ID, "dark"),
  nextermScheme(DEFAULT_LIGHT_SCHEME_ID, "light"),
];

const BUILTIN_SCHEMES: readonly TerminalScheme[] = [
  ...NEXTERM_SCHEMES,
  ...BUNDLED_SCHEMES.map((item) => ({ ...item, kind: schemeKind(item.palette), custom: false })),
];

export function isDefaultScheme(id: string): boolean {
  return id === DEFAULT_DARK_SCHEME_ID || id === DEFAULT_LIGHT_SCHEME_ID;
}

// terminalSchemeCatalog lists the NexTerm defaults, the bundled schemes and the imported ones, in that order.
export function terminalSchemeCatalog(custom: readonly CustomTerminalScheme[] = []): TerminalScheme[] {
  return [
    ...BUILTIN_SCHEMES,
    ...custom.map(({ id, name, ...palette }) => ({ id, name, kind: schemeKind(palette), palette, custom: true })),
  ];
}

// resolveTerminalScheme picks the scheme selected for a light or dark terminal; an unknown id, a deleted custom
// scheme or one of the other kind falls back to the NexTerm default of that kind.
export function resolveTerminalScheme(kind: SchemeKind, id: string, custom: readonly CustomTerminalScheme[] = []): TerminalScheme {
  const found = terminalSchemeCatalog(custom).find((item) => item.id === id && item.kind === kind);
  return found ?? NEXTERM_SCHEMES[kind === "dark" ? 0 : 1];
}

export interface TerminalSchemePrefs {
  terminalContrast: TerminalContrast;
  terminalSchemeDark: string;
  terminalSchemeLight: string;
  terminalCustomSchemes: readonly CustomTerminalScheme[];
}

export function activeTerminalScheme(resolved: ResolvedTerminalTheme, prefs: TerminalSchemePrefs): TerminalScheme {
  return resolveTerminalScheme(
    resolved,
    resolved === "dark" ? prefs.terminalSchemeDark : prefs.terminalSchemeLight,
    prefs.terminalCustomSchemes,
  );
}

function paletteTheme(palette: SchemePalette): ITheme {
  const theme: ITheme = {
    background: palette.background,
    foreground: palette.foreground,
    cursorAccent: palette.background,
    selectionBackground: palette.selection ?? mixHex(palette.foreground, palette.background, 0.25),
    scrollbarSliderBackground: mixHex(palette.foreground, palette.background, 0.2),
    scrollbarSliderHoverBackground: mixHex(palette.foreground, palette.background, 0.3),
    scrollbarSliderActiveBackground: mixHex(palette.foreground, palette.background, 0.4),
  };
  ANSI_THEME_KEYS.forEach((key, i) => {
    theme[key] = palette.ansi[i];
  });
  if (palette.cursor) theme.cursor = palette.cursor;
  return theme;
}

export function accentColor(): string {
  if (typeof document === "undefined") return "#516cd6";
  const v = getComputedStyle(document.documentElement).getPropertyValue("--color-accent").trim();
  return v || "#516cd6";
}

// xtermThemeForScheme maps a scheme to xterm options. The contrast setting picks the variants of the NexTerm
// defaults; other schemes keep their published palette. A scheme without its own cursor uses the app accent.
export function xtermThemeForScheme(scheme: TerminalScheme, contrast: TerminalContrast = "standard"): ITheme {
  const theme: ITheme = isDefaultScheme(scheme.id) ? { ...XTERM_THEMES[scheme.kind][contrast] } : paletteTheme(scheme.palette);
  return { ...theme, cursor: theme.cursor ?? accentColor() };
}

export function activeXtermTheme(resolved: ResolvedTerminalTheme, prefs: TerminalSchemePrefs): ITheme {
  return xtermThemeForScheme(activeTerminalScheme(resolved, prefs), prefs.terminalContrast);
}
