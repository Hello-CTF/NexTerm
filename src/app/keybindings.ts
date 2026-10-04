import { useSyncExternalStore } from "react";
import { isMac, modHint } from "./platform";

export type KeybindingActionId =
  | "commandPalette"
  | "globalSearch"
  | "quickConnect"
  | "newTerminal"
  | "toggleSidebar"
  | "toggleAiSidebar"
  | "closeTab"
  | "toggleSplit"
  | "switchTab"
  | "terminalSearch"
  | "reclaimTakeover";

export interface KeybindingAction {
  id: KeybindingActionId;
  label: string;
  defaultBinding: string;
}

export const KEYBINDING_ACTIONS: KeybindingAction[] = [
  { id: "commandPalette", label: "命令面板", defaultBinding: "Mod+Shift+p" },
  { id: "globalSearch", label: "全局搜索", defaultBinding: "Mod+k" },
  { id: "quickConnect", label: "快速连接", defaultBinding: "Mod+Shift+k" },
  { id: "newTerminal", label: "新建本地终端", defaultBinding: "Mod+t" },
  { id: "toggleSidebar", label: "收起 / 展开资产树", defaultBinding: "Mod+b" },
  { id: "toggleAiSidebar", label: "收起 / 展开 AI 侧栏", defaultBinding: "Mod+j" },
  { id: "closeTab", label: "关闭当前标签", defaultBinding: "Mod+w" },
  { id: "toggleSplit", label: "上下分屏 / 取消分屏", defaultBinding: "Mod+\\" },
  { id: "switchTab", label: "跳到第 1-9 个标签", defaultBinding: "Mod+1-9" },
  { id: "terminalSearch", label: "终端内搜索", defaultBinding: "Primary+f" },
  { id: "reclaimTakeover", label: "AI 接管中一键夺回", defaultBinding: "Escape" },
];

const APP_ACTION_ORDER: KeybindingActionId[] = [
  "commandPalette",
  "globalSearch",
  "quickConnect",
  "newTerminal",
  "toggleSidebar",
  "toggleAiSidebar",
  "closeTab",
  "toggleSplit",
  "switchTab",
];

export interface KeyEventLike {
  key: string;
  code?: string;
  ctrlKey: boolean;
  metaKey: boolean;
  shiftKey: boolean;
  altKey: boolean;
}

export interface ParsedBinding {
  mod: boolean;
  primary: boolean;
  ctrl: boolean;
  meta: boolean;
  shift: boolean;
  alt: boolean;
  key: string;
}

const MODIFIER_ORDER = ["Mod", "Primary", "Ctrl", "Meta", "Shift", "Alt"] as const;

const NAMED_KEYS = new Set([
  "Enter",
  "Escape",
  "Tab",
  "Space",
  "Backspace",
  "Delete",
  "Home",
  "End",
  "PageUp",
  "PageDown",
  "ArrowUp",
  "ArrowDown",
  "ArrowLeft",
  "ArrowRight",
]);

const DISPLAY_KEYS: Record<string, string> = {
  Escape: "Esc",
  "1-9": "1…9",
};

function displayKey(key: string): string {
  if (DISPLAY_KEYS[key]) return DISPLAY_KEYS[key];
  if (/^[a-z]$/.test(key)) return key.toUpperCase();
  return key;
}

function isSingleBindableChar(key: string): boolean {
  return key.length === 1 && key.charCodeAt(0) >= 0x21 && key.charCodeAt(0) <= 0x7e && key !== "+";
}

function normalizeKey(raw: string): string | null {
  if (raw === "1-9") return raw;
  if (NAMED_KEYS.has(raw)) return raw;
  if (/^F([1-9]|1[0-9]|2[0-4])$/.test(raw)) return raw;
  if (isSingleBindableChar(raw)) return raw.toLowerCase();
  return null;
}

export function parseBinding(input: string): ParsedBinding | null {
  if (typeof input !== "string" || !input) return null;
  const parts = input.split("+");
  const rawKey = parts[parts.length - 1];
  const key = normalizeKey(rawKey);
  if (key === null) return null;
  const parsed: ParsedBinding = {
    mod: false,
    primary: false,
    ctrl: false,
    meta: false,
    shift: false,
    alt: false,
    key,
  };
  const seen = new Set<string>();
  for (const part of parts.slice(0, -1)) {
    if (!(MODIFIER_ORDER as readonly string[]).includes(part)) return null;
    if (seen.has(part)) return null;
    seen.add(part);
    if (part === "Mod") parsed.mod = true;
    else if (part === "Primary") parsed.primary = true;
    else if (part === "Ctrl") parsed.ctrl = true;
    else if (part === "Meta") parsed.meta = true;
    else if (part === "Shift") parsed.shift = true;
    else if (part === "Alt") parsed.alt = true;
  }
  const modLikes = [parsed.mod, parsed.primary, parsed.ctrl, parsed.meta].filter(Boolean).length;
  if (modLikes > 1) return null;
  if (modLikes === 0 && !parsed.shift && !parsed.alt) {
    const bareAllowed = NAMED_KEYS.has(key) || /^F([1-9]|1[0-9]|2[0-4])$/.test(key);
    if (!bareAllowed) return null;
  }
  return parsed;
}

export function normalizeBinding(input: string): string | null {
  const parsed = parseBinding(input);
  if (!parsed) return null;
  const parts: string[] = [];
  if (parsed.mod) parts.push("Mod");
  if (parsed.primary) parts.push("Primary");
  if (parsed.ctrl) parts.push("Ctrl");
  if (parsed.meta) parts.push("Meta");
  if (parsed.shift) parts.push("Shift");
  if (parsed.alt) parts.push("Alt");
  parts.push(parsed.key);
  return parts.join("+");
}

export function formatBinding(binding: string | null): string {
  if (binding === null) return "未绑定";
  const parsed = parseBinding(binding);
  if (!parsed) return binding;
  const parts: string[] = [];
  if (parsed.mod || parsed.primary) parts.push(modHint());
  if (parsed.ctrl) parts.push("Ctrl");
  if (parsed.meta) parts.push("Meta");
  if (parsed.shift) parts.push("Shift");
  if (parsed.alt) parts.push("Alt");
  parts.push(displayKey(parsed.key));
  return parts.join("+");
}

export function matchBinding(event: KeyEventLike, binding: string): boolean {
  const parsed = parseBinding(binding);
  if (!parsed) return false;
  if (event.shiftKey !== parsed.shift) return false;
  if (event.altKey !== parsed.alt) return false;
  if (parsed.mod) {
    if (!event.ctrlKey && !event.metaKey) return false;
  } else if (parsed.primary) {
    const primaryDown = isMac() ? event.metaKey : event.ctrlKey;
    const otherDown = isMac() ? event.ctrlKey : event.metaKey;
    if (!primaryDown || otherDown) return false;
  } else if (parsed.ctrl) {
    if (!event.ctrlKey) return false;
  } else if (parsed.meta) {
    if (!event.metaKey) return false;
  } else if (event.ctrlKey || event.metaKey) {
    return false;
  }
  const key = parsed.key;
  if (key === "1-9") return /^[1-9]$/.test(event.key);
  if (key === "Space") return event.key === " ";
  if (isSingleBindableChar(key)) {
    return event.key.length === 1 && event.key.toLowerCase() === key;
  }
  return event.key === key;
}

export type CaptureResult =
  | { kind: "modifier" }
  | { kind: "invalid"; reason: string }
  | { kind: "binding"; binding: string };

export function captureBinding(event: KeyEventLike): CaptureResult {
  if (["Control", "Shift", "Alt", "Meta"].includes(event.key)) return { kind: "modifier" };
  let key: string | null = null;
  const code = event.code ?? "";
  const codeLetter = /^Key([A-Z])$/.exec(code);
  const codeDigit = /^Digit([0-9])$/.exec(code);
  if (codeLetter) key = codeLetter[1].toLowerCase();
  else if (codeDigit) key = codeDigit[1];
  else if (/^F([1-9]|1[0-9]|2[0-4])$/.test(code)) key = code;
  else if (NAMED_KEYS.has(event.key)) key = event.key;
  else if (isSingleBindableChar(event.key)) key = event.key.toLowerCase();
  if (key === null) return { kind: "invalid", reason: `无法识别按键 ${event.key}` };
  const mod = event.ctrlKey || event.metaKey;
  const named = NAMED_KEYS.has(key) || /^F([1-9]|1[0-9]|2[0-4])$/.test(key);
  if (!mod && !event.shiftKey && !event.altKey && !named) {
    return { kind: "invalid", reason: "单个按键需要至少一个修饰键（Ctrl / Shift / Alt）" };
  }
  const parts: string[] = [];
  if (mod) parts.push("Mod");
  if (event.shiftKey) parts.push("Shift");
  if (event.altKey) parts.push("Alt");
  parts.push(key);
  return { kind: "binding", binding: parts.join("+") };
}

const STORAGE_KEY = "nexterm.keybindings.v1";

type Overrides = Partial<Record<KeybindingActionId, string | null>>;
export type KeybindingSnapshot = Readonly<Record<KeybindingActionId, string | null>>;

const ACTION_IDS = new Set<string>(KEYBINDING_ACTIONS.map((a) => a.id));
const DEFAULTS = new Map(KEYBINDING_ACTIONS.map((a) => [a.id, a.defaultBinding]));

function loadOverrides(): Overrides {
  let raw: string | null = null;
  try {
    raw = localStorage.getItem(STORAGE_KEY);
  } catch {
    return {};
  }
  if (!raw) return {};
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return {};
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
  const out: Overrides = {};
  for (const [id, value] of Object.entries(parsed as Record<string, unknown>)) {
    if (!ACTION_IDS.has(id)) continue;
    if (value === null) {
      out[id as KeybindingActionId] = null;
      continue;
    }
    if (typeof value !== "string") continue;
    const normalized = normalizeBinding(value);
    if (normalized !== null) out[id as KeybindingActionId] = normalized;
  }
  return out;
}

function buildSnapshot(overrides: Overrides): KeybindingSnapshot {
  const out = {} as Record<KeybindingActionId, string | null>;
  for (const action of KEYBINDING_ACTIONS) {
    out[action.id] = action.id in overrides ? (overrides[action.id] ?? null) : action.defaultBinding;
  }
  return out;
}

let overrides: Overrides = loadOverrides();
let snapshot: KeybindingSnapshot = buildSnapshot(overrides);
const listeners = new Set<() => void>();

function persist(): void {
  try {
    if (Object.keys(overrides).length === 0) localStorage.removeItem(STORAGE_KEY);
    else localStorage.setItem(STORAGE_KEY, JSON.stringify(overrides));
  } catch {
  }
}

function emit(): void {
  snapshot = buildSnapshot(overrides);
  for (const listener of listeners) listener();
}

export function getKeybindings(): KeybindingSnapshot {
  return snapshot;
}

export function getKeybinding(id: KeybindingActionId): string | null {
  return snapshot[id];
}

export function setKeybinding(id: KeybindingActionId, binding: string | null): void {
  const normalized = binding === null ? null : normalizeBinding(binding);
  if (binding !== null && normalized === null) return;
  if (normalized === DEFAULTS.get(id)) delete overrides[id];
  else overrides[id] = normalized;
  persist();
  emit();
}

export function resetKeybinding(id: KeybindingActionId): void {
  if (!(id in overrides)) return;
  delete overrides[id];
  persist();
  emit();
}

export function resetAllKeybindings(): void {
  if (Object.keys(overrides).length === 0) return;
  overrides = {};
  persist();
  emit();
}

export function loadKeybindings(): void {
  overrides = loadOverrides();
  emit();
}

export function subscribeKeybindings(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useKeybindings(): KeybindingSnapshot {
  return useSyncExternalStore(subscribeKeybindings, getKeybindings);
}

export function matchKeybinding(event: KeyEventLike, id: KeybindingActionId): boolean {
  const binding = snapshot[id];
  return binding !== null && matchBinding(event, binding);
}

export interface AppKeybindingHit {
  action: KeybindingActionId;
  digit: number | null;
}

export function matchAppKeybinding(event: KeyEventLike): AppKeybindingHit | null {
  for (const id of APP_ACTION_ORDER) {
    const binding = snapshot[id];
    if (binding === null || !matchBinding(event, binding)) continue;
    const parsed = parseBinding(binding);
    let digit: number | null = null;
    if (parsed && (parsed.key === "1-9" || /^[1-9]$/.test(parsed.key)) && /^[1-9]$/.test(event.key)) {
      digit = Number(event.key);
    }
    return { action: id, digit };
  }
  return null;
}

export interface KeybindingConflict {
  binding: string;
  actions: KeybindingAction[];
}

export function bindingConflicts(
  bindings: KeybindingSnapshot = snapshot,
): KeybindingConflict[] {
  const byBinding = new Map<string, KeybindingAction[]>();
  for (const action of KEYBINDING_ACTIONS) {
    const binding = bindings[action.id];
    if (!binding) continue;
    const list = byBinding.get(binding);
    if (list) list.push(action);
    else byBinding.set(binding, [action]);
  }
  return [...byBinding.entries()]
    .filter(([, actions]) => actions.length > 1)
    .map(([binding, actions]) => ({ binding, actions }));
}
