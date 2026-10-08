import type { ResolvedTerminalTheme } from "../../app/preferences";

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

export function xtermThemeFor(resolved: ResolvedTerminalTheme) {
  return resolved === "light" ? XTERM_LIGHT_THEME : XTERM_DARK_THEME;
}
