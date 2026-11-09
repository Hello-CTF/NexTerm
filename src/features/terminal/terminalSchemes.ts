// Terminal color schemes: the bundled third-party palettes, the stored shape of imported custom schemes, and the
// importers for iTerm2 (.itermcolors), Windows Terminal (JSON) and Ghostty/kitty (key/value conf) files.
// The custom scheme limits mirror normalizeTerminalCustomSchemes in internal/account/preferences.go.

export type SchemeKind = "dark" | "light";

export interface SchemePalette {
  background: string;
  foreground: string;
  cursor?: string;
  selection?: string;
  // ANSI 0-15: black, red, green, yellow, blue, magenta, cyan, white, then the bright variants.
  ansi: string[];
}

export interface CustomTerminalScheme extends SchemePalette {
  id: string;
  name: string;
}

export interface BundledTerminalScheme {
  id: string;
  name: string;
  palette: SchemePalette;
}

export const MAX_CUSTOM_SCHEMES = 20;
export const MAX_SCHEME_NAME_LENGTH = 48;
export const MAX_SCHEME_IMPORT_BYTES = 256 << 10;
const CUSTOM_SCHEME_ID_PATTERN = /^custom-[a-z0-9]{1,24}$/;
export const SCHEME_ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,47}$/;
const HEX_PATTERN = /^#[0-9a-fA-F]{6}$/;

function scheme(id: string, name: string, background: string, foreground: string, cursor: string, selection: string, ansi: string[]): BundledTerminalScheme {
  return { id, name, palette: { background, foreground, cursor, selection, ansi } };
}

// Palettes follow each project's published terminal ports (kitty / Windows Terminal / iTerm2 files).
export const BUNDLED_SCHEMES: readonly BundledTerminalScheme[] = [
  scheme("catppuccin-mocha", "Catppuccin Mocha", "#1e1e2e", "#cdd6f4", "#f5e0dc", "#585b70", [
    "#45475a", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#bac2de",
    "#585b70", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#a6adc8",
  ]),
  scheme("catppuccin-latte", "Catppuccin Latte", "#eff1f5", "#4c4f69", "#dc8a78", "#acb0be", [
    "#5c5f77", "#d20f39", "#40a02b", "#df8e1d", "#1e66f5", "#ea76cb", "#179299", "#acb0be",
    "#6c6f85", "#d20f39", "#40a02b", "#df8e1d", "#1e66f5", "#ea76cb", "#179299", "#bcc0cc",
  ]),
  scheme("tokyo-night", "Tokyo Night", "#1a1b26", "#c0caf5", "#c0caf5", "#283457", [
    "#15161e", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#a9b1d6",
    "#414868", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#c0caf5",
  ]),
  scheme("tokyo-night-day", "Tokyo Night Day", "#e1e2e7", "#3760bf", "#3760bf", "#b7c1e3", [
    "#e9e9ed", "#f52a65", "#587539", "#8c6c3e", "#2e7de9", "#9854f1", "#007197", "#6172b0",
    "#a1a6c5", "#f52a65", "#587539", "#8c6c3e", "#2e7de9", "#9854f1", "#007197", "#3760bf",
  ]),
  scheme("solarized-dark", "Solarized Dark", "#002b36", "#839496", "#93a1a1", "#073642", [
    "#073642", "#dc322f", "#859900", "#b58900", "#268bd2", "#d33682", "#2aa198", "#eee8d5",
    "#002b36", "#cb4b16", "#586e75", "#657b83", "#839496", "#6c71c4", "#93a1a1", "#fdf6e3",
  ]),
  scheme("solarized-light", "Solarized Light", "#fdf6e3", "#657b83", "#586e75", "#eee8d5", [
    "#073642", "#dc322f", "#859900", "#b58900", "#268bd2", "#d33682", "#2aa198", "#eee8d5",
    "#002b36", "#cb4b16", "#586e75", "#657b83", "#839496", "#6c71c4", "#93a1a1", "#fdf6e3",
  ]),
  scheme("nord", "Nord", "#2e3440", "#d8dee9", "#d8dee9", "#434c5e", [
    "#3b4252", "#bf616a", "#a3be8c", "#ebcb8b", "#81a1c1", "#b48ead", "#88c0d0", "#e5e9f0",
    "#4c566a", "#bf616a", "#a3be8c", "#ebcb8b", "#81a1c1", "#b48ead", "#8fbcbb", "#eceff4",
  ]),
  scheme("gruvbox-dark", "Gruvbox Dark", "#282828", "#ebdbb2", "#ebdbb2", "#504945", [
    "#282828", "#cc241d", "#98971a", "#d79921", "#458588", "#b16286", "#689d6a", "#a89984",
    "#928374", "#fb4934", "#b8bb26", "#fabd2f", "#83a598", "#d3869b", "#8ec07c", "#ebdbb2",
  ]),
  scheme("gruvbox-light", "Gruvbox Light", "#fbf1c7", "#3c3836", "#3c3836", "#d5c4a1", [
    "#fbf1c7", "#cc241d", "#98971a", "#d79921", "#458588", "#b16286", "#689d6a", "#7c6f64",
    "#928374", "#9d0006", "#79740e", "#b57614", "#076678", "#8f3f71", "#427b58", "#3c3836",
  ]),
  scheme("dracula", "Dracula", "#282a36", "#f8f8f2", "#f8f8f2", "#44475a", [
    "#21222c", "#ff5555", "#50fa7b", "#f1fa8c", "#bd93f9", "#ff79c6", "#8be9fd", "#f8f8f2",
    "#6272a4", "#ff6e6e", "#69ff94", "#ffffa5", "#d6acff", "#ff92df", "#a4ffff", "#ffffff",
  ]),
  scheme("one-half-dark", "One Half Dark", "#282c34", "#dcdfe4", "#a3b3cc", "#474e5d", [
    "#282c34", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#dcdfe4",
    "#5a6374", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#dcdfe4",
  ]),
  scheme("one-half-light", "One Half Light", "#fafafa", "#383a42", "#4f525d", "#bfceff", [
    "#383a42", "#e45649", "#50a14f", "#c18301", "#0184bc", "#a626a4", "#0997b3", "#fafafa",
    "#4f525d", "#df6c75", "#98c379", "#e4c07a", "#61afef", "#c577dd", "#56b5c1", "#ffffff",
  ]),
  scheme("github-dark", "GitHub Dark", "#0d1117", "#e6edf3", "#2f81f7", "#264f78", [
    "#484f58", "#ff7b72", "#3fb950", "#d29922", "#58a6ff", "#bc8cff", "#39c5cf", "#b1bac4",
    "#6e7681", "#ffa198", "#56d364", "#e3b341", "#79c0ff", "#d2a8ff", "#56d4dd", "#ffffff",
  ]),
  scheme("github-light", "GitHub Light", "#ffffff", "#1f2328", "#0969da", "#add6ff", [
    "#24292f", "#cf222e", "#116329", "#4d2d00", "#0969da", "#8250df", "#1b7c83", "#6e7781",
    "#57606a", "#a40e26", "#1a7f37", "#633c01", "#218bff", "#a475f9", "#3192aa", "#8c959f",
  ]),
];

export function relativeLuminance(hex: string): number {
  const n = parseInt(hex.slice(1), 16);
  const channels = [(n >> 16) & 255, (n >> 8) & 255, n & 255].map((v) => {
    const s = v / 255;
    return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  });
  return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
}

export function contrastRatio(a: string, b: string): number {
  const la = relativeLuminance(a);
  const lb = relativeLuminance(b);
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

// schemeKind classifies a palette by whether white or black text reads better on its background, so each
// scheme is offered in the matching light or dark slot.
export function schemeKind(palette: Pick<SchemePalette, "background">): SchemeKind {
  return contrastRatio("#ffffff", palette.background) >= contrastRatio("#000000", palette.background) ? "dark" : "light";
}

export function mixHex(fg: string, bg: string, alpha: number): string {
  const f = parseInt(fg.slice(1), 16);
  const b = parseInt(bg.slice(1), 16);
  const channels = [16, 8, 0].map((shift) => Math.round(((f >> shift) & 255) * alpha + ((b >> shift) & 255) * (1 - alpha)));
  return `#${channels.map((v) => v.toString(16).padStart(2, "0")).join("")}`;
}

function normalizeHex(value: unknown): string | null {
  return typeof value === "string" && HEX_PATTERN.test(value) ? value.toLowerCase() : null;
}

function validSchemeName(value: unknown): value is string {
  if (typeof value !== "string" || value.trim() === "" || [...value].length > MAX_SCHEME_NAME_LENGTH) return false;
  return !/[\u0000-\u001f\u007f-\u009f]/.test(value);
}

// normalizeSchemeName trims an imported or typed name and cuts it to the stored limit; control characters become spaces.
export function normalizeSchemeName(value: string): string {
  const cleaned = value.replace(/[\u0000-\u001f\u007f-\u009f]/g, " ").replace(/\s+/g, " ").trim();
  return [...cleaned].slice(0, MAX_SCHEME_NAME_LENGTH).join("").trim();
}

function parseCustomScheme(value: unknown): CustomTerminalScheme | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  if (typeof record.id !== "string" || !CUSTOM_SCHEME_ID_PATTERN.test(record.id) || !validSchemeName(record.name)) return null;
  const background = normalizeHex(record.background);
  const foreground = normalizeHex(record.foreground);
  if (!background || !foreground || !Array.isArray(record.ansi) || record.ansi.length !== 16) return null;
  const ansi = record.ansi.map(normalizeHex);
  if (ansi.some((color) => color === null)) return null;
  const out: CustomTerminalScheme = { id: record.id, name: record.name, background, foreground, ansi: ansi as string[] };
  for (const key of ["cursor", "selection"] as const) {
    if (record[key] === undefined) continue;
    const color = normalizeHex(record[key]);
    if (!color) return null;
    out[key] = color;
  }
  return out;
}

// parseCustomSchemes reads the stored custom scheme list, skipping invalid or duplicate entries and those over the
// limit, so a tampered value never breaks the appearance settings.
export function parseCustomSchemes(value: unknown): CustomTerminalScheme[] {
  if (!Array.isArray(value)) return [];
  const out: CustomTerminalScheme[] = [];
  const seen = new Set<string>();
  for (const item of value) {
    const parsed = parseCustomScheme(item);
    if (!parsed || seen.has(parsed.id)) continue;
    seen.add(parsed.id);
    out.push(parsed);
    if (out.length >= MAX_CUSTOM_SCHEMES) break;
  }
  return out;
}

export function newCustomSchemeId(existing: readonly { id: string }[]): string {
  const taken = new Set(existing.map((item) => item.id));
  for (;;) {
    const bytes = new Uint8Array(6);
    crypto.getRandomValues(bytes);
    const id = `custom-${[...bytes].map((b) => (b % 36).toString(36)).join("")}`;
    if (!taken.has(id)) return id;
  }
}

export class SchemeImportError extends Error {}

export interface ImportedScheme {
  name: string;
  palette: SchemePalette;
}

function componentHex(value: number): string {
  return Math.round(Math.min(1, Math.max(0, value)) * 255).toString(16).padStart(2, "0");
}

function completePalette(colors: Partial<Record<string, string>>, label: string): SchemePalette {
  const background = colors.background;
  const foreground = colors.foreground;
  const ansi = Array.from({ length: 16 }, (_, i) => colors[`ansi${i}`]);
  if (!background || !foreground || ansi.some((color) => !color)) {
    throw new SchemeImportError(label);
  }
  const palette: SchemePalette = { background, foreground, ansi: ansi as string[] };
  if (colors.cursor) palette.cursor = colors.cursor;
  if (colors.selection) palette.selection = colors.selection;
  return palette;
}

const ITERM_KEYS: Record<string, string> = {
  "Background Color": "background",
  "Foreground Color": "foreground",
  "Cursor Color": "cursor",
  "Selection Color": "selection",
};

function parseItermColors(text: string): SchemePalette {
  const doc = new DOMParser().parseFromString(text, "application/xml");
  if (doc.getElementsByTagName("parsererror").length > 0) throw new SchemeImportError("plist");
  const root = doc.querySelector("plist > dict");
  if (!root) throw new SchemeImportError("plist");
  const colors: Record<string, string> = {};
  const children = [...root.children];
  for (let i = 0; i + 1 < children.length; i += 2) {
    const keyEl = children[i];
    const dict = children[i + 1];
    if (keyEl.tagName !== "key" || dict.tagName !== "dict") continue;
    const name = keyEl.textContent?.trim() ?? "";
    const ansi = /^Ansi (\d{1,2}) Color$/.exec(name);
    const field = ansi && Number(ansi[1]) < 16 ? `ansi${Number(ansi[1])}` : ITERM_KEYS[name];
    if (!field) continue;
    const components: Record<string, number> = {};
    const entries = [...dict.children];
    for (let j = 0; j + 1 < entries.length; j += 2) {
      const component = entries[j].textContent?.trim() ?? "";
      const raw = Number(entries[j + 1].textContent?.trim());
      if (Number.isFinite(raw)) components[component] = raw;
    }
    const red = components["Red Component"];
    const green = components["Green Component"];
    const blue = components["Blue Component"];
    if (red === undefined || green === undefined || blue === undefined) continue;
    colors[field] = `#${componentHex(red)}${componentHex(green)}${componentHex(blue)}`;
  }
  return completePalette(colors, "itermcolors");
}

const WINDOWS_TERMINAL_ANSI = [
  "black", "red", "green", "yellow", "blue", "purple", "cyan", "white",
  "brightBlack", "brightRed", "brightGreen", "brightYellow", "brightBlue", "brightPurple", "brightCyan", "brightWhite",
];

function parseWindowsTerminalScheme(value: unknown): ImportedScheme {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new SchemeImportError("json");
  const record = value as Record<string, unknown>;
  const colors: Record<string, string> = {};
  const set = (field: string, raw: unknown) => {
    const color = normalizeHex(raw);
    if (color) colors[field] = color;
  };
  set("background", record.background);
  set("foreground", record.foreground);
  set("cursor", record.cursorColor);
  set("selection", record.selectionBackground);
  WINDOWS_TERMINAL_ANSI.forEach((key, i) => set(`ansi${i}`, record[key]));
  return { name: typeof record.name === "string" ? record.name : "", palette: completePalette(colors, "json") };
}

function parseWindowsTerminal(text: string): ImportedScheme[] {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    throw new SchemeImportError("json");
  }
  if (value && typeof value === "object" && !Array.isArray(value) && Array.isArray((value as { schemes?: unknown }).schemes)) {
    value = (value as { schemes: unknown[] }).schemes;
  }
  const list = Array.isArray(value) ? value : [value];
  if (list.length === 0) throw new SchemeImportError("json");
  return list.map(parseWindowsTerminalScheme);
}

const CONF_KEYS: Record<string, string> = {
  background: "background",
  foreground: "foreground",
  cursor: "cursor",
  "cursor-color": "cursor",
  selection_background: "selection",
  "selection-background": "selection",
};

// parseKeyValueConf reads Ghostty (`palette = 1=#rrggbb`, `background = #rrggbb`) and kitty (`color1 #rrggbb`)
// theme files; other settings in the file are ignored.
function parseKeyValueConf(text: string): SchemePalette {
  const colors: Record<string, string> = {};
  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (line === "" || line.startsWith("#")) continue;
    const match = /^([A-Za-z0-9_-]+)\s*(?:=\s*|\s+)(.+)$/.exec(line);
    if (!match) continue;
    const key = match[1].toLowerCase();
    const value = match[2].trim();
    if (key === "palette") {
      const entry = /^(\d{1,3})\s*=\s*(#?[0-9a-fA-F]{6})$/.exec(value);
      const index = entry ? Number(entry[1]) : -1;
      const color = entry ? normalizeHex(entry[2].startsWith("#") ? entry[2] : `#${entry[2]}`) : null;
      if (color && index >= 0 && index < 16) colors[`ansi${index}`] = color;
      continue;
    }
    const color = normalizeHex(value.startsWith("#") ? value : `#${value}`);
    if (!color) continue;
    const ansi = /^color(\d{1,2})$/.exec(key);
    if (ansi && Number(ansi[1]) < 16) colors[`ansi${Number(ansi[1])}`] = color;
    else if (CONF_KEYS[key]) colors[CONF_KEYS[key]] = color;
  }
  return completePalette(colors, "conf");
}

function baseName(fileName: string): string {
  const name = fileName.split(/[\\/]/).pop() ?? fileName;
  return name.replace(/\.(itermcolors|json|conf|toml|txt)$/i, "");
}

// importSchemeFile parses one scheme file by its content: plist XML for iTerm2, JSON for Windows Terminal (one
// scheme, a list, or a settings file with "schemes"), otherwise Ghostty/kitty key/value lines.
export function importSchemeFile(fileName: string, text: string): ImportedScheme[] {
  if (text.length > MAX_SCHEME_IMPORT_BYTES) throw new SchemeImportError("size");
  const trimmed = text.replace(/^﻿/, "").trimStart();
  const fallbackName = normalizeSchemeName(baseName(fileName)) || "Imported";
  let imported: ImportedScheme[];
  if (trimmed.startsWith("<")) {
    imported = [{ name: "", palette: parseItermColors(trimmed) }];
  } else if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
    imported = parseWindowsTerminal(trimmed);
  } else {
    imported = [{ name: "", palette: parseKeyValueConf(trimmed) }];
  }
  return imported.map((item) => ({ name: normalizeSchemeName(item.name) || fallbackName, palette: item.palette }));
}
