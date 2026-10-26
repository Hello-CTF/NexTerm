import type { ResolvedTerminalTheme, TerminalContrast, TerminalFontFamily } from "../../app/preferences";

// Colour emoji fallbacks follow the generic family so they never supply ASCII glyphs or
// change the cell metrics measured from the primary font.
export const EMOJI_FONT_FALLBACKS = "'Apple Color Emoji', 'Segoe UI Emoji', 'Segoe UI Symbol', 'Noto Color Emoji'";

export const TERMINAL_FONT_STACKS: Record<TerminalFontFamily, string> = {
  cascadia: `'Cascadia Mono', 'Cascadia Code', Consolas, 'Courier New', monospace, ${EMOJI_FONT_FALLBACKS}`,
  system: `ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, 'Liberation Mono', 'Courier New', monospace, ${EMOJI_FONT_FALLBACKS}`,
  "jetbrains-mono": `'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, Consolas, 'Courier New', monospace, ${EMOJI_FONT_FALLBACKS}`,
  "fira-code": `'Fira Code', ui-monospace, SFMono-Regular, Menlo, Consolas, 'Courier New', monospace, ${EMOJI_FONT_FALLBACKS}`,
  "source-code-pro": `'Source Code Pro', ui-monospace, SFMono-Regular, Menlo, Consolas, 'Courier New', monospace, ${EMOJI_FONT_FALLBACKS}`,
  menlo: `Menlo, Monaco, ui-monospace, 'Courier New', monospace, ${EMOJI_FONT_FALLBACKS}`,
  consolas: `Consolas, 'Courier New', monospace, ${EMOJI_FONT_FALLBACKS}`,
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
