import { useSyncExternalStore } from "react";
import { isMac, modHint } from "./platform";
import { WEB } from "../ipc/env";
import { getAccountDefaults, getAccountOverrides, getAccountPreferenceStore, patchAccountOverrideCache, subscribeAccountOverrides } from "./preferences";

export type KeybindingActionId =
  | "commandPalette"
  | "quickConnect"
  | "newTerminal"
  | "nextWorkspace"
  | "previousWorkspace"
  | "nextTab"
  | "previousTab"
  | "focusPaneLeft"
  | "focusPaneRight"
  | "focusPaneUp"
  | "focusPaneDown"
  | "toggleSidebar"
  | "toggleAiSidebar"
  | "toggleSessionPanel"
  | "closeTab"
  | "splitRight"
  | "splitDown"
  | "closePane"
  | "switchTab"
  | "restoreClosedTab"
  | "terminalSearch"
  | "reclaimTakeover"
  | "lockVault";

export interface KeybindingAction {
  id: KeybindingActionId;
  label: string;
  defaultBinding: string;
  /** An extra chord that also triggers the action on macOS desktop while the default binding is in effect. */
  macDefaultBinding?: string;
  /** The default on macOS (desktop and web) in place of defaultBinding. */
  macBinding?: string;
}

export const KEYBINDING_ACTIONS: KeybindingAction[] = [
  { id: "commandPalette", label: "跳转面板", defaultBinding: "Mod+Shift+p" },
  { id: "quickConnect", label: "快速连接", defaultBinding: "Mod+Shift+k" },
  { id: "newTerminal", label: "新建终端标签", defaultBinding: "Mod+t" },
  { id: "nextWorkspace", label: "下一个工作区", defaultBinding: WEB ? "Ctrl+Alt+ArrowRight" : "Mod+Alt+ArrowRight" },
  { id: "previousWorkspace", label: "上一个工作区", defaultBinding: WEB ? "Ctrl+Alt+ArrowLeft" : "Mod+Alt+ArrowLeft" },
  { id: "nextTab", label: "下一个标签", defaultBinding: WEB ? "Ctrl+Alt+]" : "Ctrl+Tab", macDefaultBinding: "Mod+Shift+]" },
  { id: "previousTab", label: "上一个标签", defaultBinding: WEB ? "Ctrl+Alt+[" : "Ctrl+Shift+Tab", macDefaultBinding: "Mod+Shift+[" },
  { id: "focusPaneLeft", label: "聚焦左侧窗格", defaultBinding: WEB ? "Ctrl+Shift+Alt+ArrowLeft" : "Mod+Shift+Alt+ArrowLeft" },
  { id: "focusPaneRight", label: "聚焦右侧窗格", defaultBinding: WEB ? "Ctrl+Shift+Alt+ArrowRight" : "Mod+Shift+Alt+ArrowRight" },
  { id: "focusPaneUp", label: "聚焦上方窗格", defaultBinding: WEB ? "Ctrl+Alt+ArrowUp" : "Mod+Alt+ArrowUp" },
  { id: "focusPaneDown", label: "聚焦下方窗格", defaultBinding: WEB ? "Ctrl+Alt+ArrowDown" : "Mod+Alt+ArrowDown" },
  { id: "toggleSidebar", label: "收起 / 展开左栏", defaultBinding: "Mod+b" },
  { id: "toggleAiSidebar", label: "收起 / 展开 AI 侧栏", defaultBinding: "Mod+j" },
  // Ctrl+Shift+letter chords send nothing to the terminal, so the desktop toggle withholds no shell input. Browsers keep
  // Ctrl+Shift+E (Firefox's network monitor), so the web build uses Ctrl+Alt+E, as closePane does.
  { id: "toggleSessionPanel", label: "收起 / 展开会话侧栏", defaultBinding: WEB ? "Ctrl+Alt+e" : "Mod+Shift+e" },
  { id: "closeTab", label: "关闭当前标签", defaultBinding: "Mod+w" },
  { id: "splitDown", label: "向下分屏", defaultBinding: "Mod+\\" },
  { id: "splitRight", label: "向右分屏", defaultBinding: "Mod+Shift+\\" },
  // Browsers reserve Ctrl+Shift+W for closing the window, so the web build uses Ctrl+Alt+W.
  { id: "closePane", label: "关闭当前窗格（保留标签页）", defaultBinding: WEB ? "Ctrl+Alt+w" : "Mod+Shift+w" },
  { id: "switchTab", label: "跳到第 1-9 个标签", defaultBinding: "Mod+1-9" },
  { id: "restoreClosedTab", label: "恢复关闭的标签", defaultBinding: "Mod+Shift+t" },
  // Ctrl+F is readline's forward-char, so off macOS the search takes Ctrl+Shift+F and leaves Ctrl+F to the shell.
  { id: "terminalSearch", label: "终端内搜索", defaultBinding: "Ctrl+Shift+f", macBinding: "Primary+f" },
  { id: "reclaimTakeover", label: "AI 接管时收回控制权", defaultBinding: "Escape" },
  { id: "lockVault", label: "锁定凭据库", defaultBinding: "Mod+Shift+l" },
];

const APP_ACTION_ORDER: KeybindingActionId[] = [
  "commandPalette",
  "quickConnect",
  "newTerminal",
  "nextWorkspace",
  "previousWorkspace",
  "nextTab",
  "previousTab",
  "focusPaneLeft",
  "focusPaneRight",
  "focusPaneUp",
  "focusPaneDown",
  "toggleSidebar",
  "toggleAiSidebar",
  "toggleSessionPanel",
  "closeTab",
  "splitRight",
  "splitDown",
  "closePane",
  "switchTab",
  "restoreClosedTab",
  "lockVault",
];

// Actions that repeat while held: moving between tabs, workspaces and panes. Every other app shortcut runs once per
// press, so holding it cannot open, close or split repeatedly.
const REPEATABLE_ACTIONS: ReadonlySet<KeybindingActionId> = new Set<KeybindingActionId>([
  "nextTab",
  "previousTab",
  "nextWorkspace",
  "previousWorkspace",
  "focusPaneLeft",
  "focusPaneRight",
  "focusPaneUp",
  "focusPaneDown",
]);

export function keybindingRepeats(id: KeybindingActionId): boolean {
  return REPEATABLE_ACTIONS.has(id);
}

/** The built-in default binding of an action on the current (or given) platform. */
export function defaultBindingFor(action: KeybindingAction, mac = isMac()): string {
  return mac && action.macBinding ? action.macBinding : action.defaultBinding;
}

export interface KeyEventLike {
  key: string;
  code?: string;
  ctrlKey: boolean;
  metaKey: boolean;
  shiftKey: boolean;
  altKey: boolean;
  getModifierState?: (key: string) => boolean;
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

function logicalEventKey(event: KeyEventLike): string | null {
  const code = event.code ?? "";
  const codeLetter = /^Key([A-Z])$/.exec(code);
  if (codeLetter) return codeLetter[1].toLowerCase();
  const codeDigit = /^Digit([0-9])$/.exec(code);
  if (codeDigit) return codeDigit[1];
  if (code === "BracketLeft") return "[";
  if (code === "BracketRight") return "]";
  // Shift turns the key into "|" on most layouts; the physical key keeps Mod+Shift+\ bindable.
  if (code === "Backslash") return "\\";
  if (/^F([1-9]|1[0-9]|2[0-4])$/.test(code)) return code;
  if (event.key === " ") return "Space";
  if (NAMED_KEYS.has(event.key)) return event.key;
  if (/^F([1-9]|1[0-9]|2[0-4])$/.test(event.key)) return event.key;
  if (isSingleBindableChar(event.key)) return event.key.toLowerCase();
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

function ariaKey(key: string): string {
  if (key === "Escape") return "Escape";
  if (/^[a-z]$/.test(key)) return key.toUpperCase();
  return key;
}

export function formatBindingAria(binding: string | null): string | null {
  if (!binding) return null;
  const parsed = parseBinding(binding);
  if (!parsed) return null;
  const parts: string[] = [];
  if (parsed.mod || parsed.primary) parts.push(isMac() ? "Meta" : "Control");
  if (parsed.ctrl) parts.push("Control");
  if (parsed.meta) parts.push("Meta");
  if (parsed.shift) parts.push("Shift");
  if (parsed.alt) parts.push("Alt");
  parts.push(ariaKey(parsed.key));
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
  const logical = logicalEventKey(event);
  if (logical === null) return false;
  if (parsed.key === "1-9") return /^[1-9]$/.test(logical);
  return logical === parsed.key;
}

export type CaptureResult =
  | { kind: "modifier" }
  | { kind: "invalid"; reason: string }
  | { kind: "binding"; binding: string };

export function captureBinding(event: KeyEventLike): CaptureResult {
  if (["Control", "Shift", "Alt", "Meta"].includes(event.key)) return { kind: "modifier" };
  const key = logicalEventKey(event);
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

export function captureBindingForAction(
  event: KeyEventLike,
  id: KeybindingActionId,
): CaptureResult {
  const result = captureBinding(event);
  if (result.kind !== "binding" || id !== "switchTab") return result;
  const parsed = parseBinding(result.binding);
  if (!parsed || !/^[0-9]$/.test(parsed.key)) {
    return { kind: "invalid", reason: "标签跳转只支持数字键（保持修饰键，1-9 通用）" };
  }
  const parts = result.binding.split("+");
  parts[parts.length - 1] = "1-9";
  return { kind: "binding", binding: parts.join("+") };
}

const STORAGE_KEY = "nexterm.keybindings.v1";

type Overrides = Partial<Record<KeybindingActionId, string | null>>;
export type KeybindingSnapshot = Readonly<Record<KeybindingActionId, string | null>>;

const ACTION_IDS = new Set<string>(KEYBINDING_ACTIONS.map((a) => a.id));
const ACTIONS_BY_ID = new Map(KEYBINDING_ACTIONS.map((a) => [a.id, a]));

function builtinDefault(id: KeybindingActionId): string | undefined {
  const action = ACTIONS_BY_ID.get(id);
  return action ? defaultBindingFor(action) : undefined;
}

// keybindingPrefKey is the wire flat key for account-level keybinding overrides (whitelist in internal/account/preferences.go).
function keybindingPrefKey(id: string): string {
  return `keybinding.${id}`;
}

function parseOverrides(raw: unknown): Overrides {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  const out: Overrides = {};
  for (const [id, value] of Object.entries(raw as Record<string, unknown>)) {
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

// loadLocalOverrides reads only the device-local layer; the account layer is provided separately by the wire flat overrides from preferences.
function loadLocalOverrides(): Overrides {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return parseOverrides(raw ? JSON.parse(raw) : null);
  } catch {
    return {};
  }
}

// parseAccountLayer parses a keybinding layer from wire flat records (server defaults and account overrides share the same shape, e.g. keybinding.newTerminal = "Mod+Shift+t").
function parseAccountLayer(flat: Record<string, unknown>): Overrides {
  const out: Overrides = {};
  for (const action of KEYBINDING_ACTIONS) {
    const key = keybindingPrefKey(action.id);
    if (!(key in flat)) continue;
    const value = flat[key];
    if (value === null) {
      out[action.id] = null;
      continue;
    }
    if (typeof value !== "string") continue;
    const normalized = normalizeBinding(value);
    if (normalized !== null) out[action.id] = normalized;
  }
  return out;
}

// After server defaults / account overrides load or clear, preferences notifies back here: only the snapshot is rebuilt; the device-local layer and written-back account keys are untouched.
export function applyAccountKeybindingLayers(defaults: Record<string, unknown>, overrides: Record<string, unknown>): void {
  defaultsLayer = parseAccountLayer(defaults);
  accountLayer = parseAccountLayer(overrides);
  emit();
}

function buildSnapshot(overrides: Overrides): KeybindingSnapshot {
  const out = {} as Record<KeybindingActionId, string | null>;
  for (const action of KEYBINDING_ACTIONS) {
    out[action.id] = action.id in overrides ? (overrides[action.id] ?? null) : defaultBindingFor(action);
  }
  return out;
}

// The device-local layer (localStorage), the server global default layer (/admin/preferences) and the account override layer (wire flat keys) are kept separate;
// the snapshot merges as builtin default < server default < device local < account override.
let localOverrides: Overrides = loadLocalOverrides();
let defaultsLayer: Overrides = {};
let accountLayer: Overrides = {};
let snapshot: KeybindingSnapshot = buildSnapshot({ ...defaultsLayer, ...localOverrides, ...accountLayer });
// Platform defaults (macBinding) depend on isMac(), which the backend can still settle after this module loads.
let snapshotMac = isMac();
const listeners = new Set<() => void>();

function persistLocal(): void {
  try {
    if (Object.keys(localOverrides).length === 0) localStorage.removeItem(STORAGE_KEY);
    else localStorage.setItem(STORAGE_KEY, JSON.stringify(localOverrides));
  } catch {
  }
}

function emit(): void {
  snapshot = buildSnapshot({ ...defaultsLayer, ...localOverrides, ...accountLayer });
  snapshotMac = isMac();
  for (const listener of listeners) listener();
}

function currentSnapshot(): KeybindingSnapshot {
  if (snapshotMac !== isMac()) {
    snapshot = buildSnapshot({ ...defaultsLayer, ...localOverrides, ...accountLayer });
    snapshotMac = isMac();
  }
  return snapshot;
}

export function getKeybindings(): KeybindingSnapshot {
  return currentSnapshot();
}

export function getKeybinding(id: KeybindingActionId): string | null {
  return currentSnapshot()[id];
}

export function setKeybinding(id: KeybindingActionId, binding: string | null): void {
  const normalized = binding === null ? null : normalizeBinding(binding);
  if (binding !== null && normalized === null) return;
  const store = getAccountPreferenceStore();
  const key = keybindingPrefKey(id);
  // Matching the effective default (server default takes precedence over builtin default) means back to default: clear the device-local value and the account override.
  if (normalized === (id in defaultsLayer ? defaultsLayer[id] : builtinDefault(id))) {
    delete localOverrides[id];
    delete accountLayer[id];
    persistLocal();
    patchAccountOverrideCache({}, [key]);
    if (store) void store.deleteOverrides([key]).catch(() => undefined);
  } else {
    localOverrides[id] = normalized;
    accountLayer[id] = normalized;
    persistLocal();
    patchAccountOverrideCache({ [key]: normalized }, []);
    if (store) void store.putOverrides({ [key]: normalized }).catch(() => undefined);
  }
  emit();
}

export function resetKeybinding(id: KeybindingActionId): void {
  if (!(id in localOverrides) && !(id in accountLayer)) return;
  delete localOverrides[id];
  delete accountLayer[id];
  persistLocal();
  const key = keybindingPrefKey(id);
  patchAccountOverrideCache({}, [key]);
  const store = getAccountPreferenceStore();
  if (store) void store.deleteOverrides([key]).catch(() => undefined);
  emit();
}

export function resetAllKeybindings(): void {
  const accountKeys = KEYBINDING_ACTIONS.map((a) => keybindingPrefKey(a.id)).filter(
    (key) => key in getAccountOverrides(),
  );
  if (Object.keys(localOverrides).length === 0 && accountKeys.length === 0) return;
  localOverrides = {};
  accountLayer = {};
  persistLocal();
  patchAccountOverrideCache({}, accountKeys);
  const store = getAccountPreferenceStore();
  if (store && accountKeys.length > 0) void store.deleteOverrides(accountKeys).catch(() => undefined);
  emit();
}

export function loadKeybindings(): void {
  localOverrides = loadLocalOverrides();
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

function matchActionBinding(event: KeyEventLike, id: KeybindingActionId): boolean {
  const binding = currentSnapshot()[id];
  if (binding !== null && matchBinding(event, binding)) return true;
  const action = KEYBINDING_ACTIONS.find((candidate) => candidate.id === id);
  return Boolean(
    action?.macDefaultBinding &&
    binding === action.defaultBinding &&
    !WEB &&
    isMac() &&
    matchBinding(event, action.macDefaultBinding),
  );
}

export function matchKeybinding(event: KeyEventLike, id: KeybindingActionId): boolean {
  return matchActionBinding(event, id);
}

export interface AppKeybindingHit {
  action: KeybindingActionId;
  digit: number | null;
}

// AltGr reports Ctrl+Alt on Windows; an AltGr chord types a character and is never an app shortcut.
function isAltGraph(event: KeyEventLike): boolean {
  return event.getModifierState?.("AltGraph") === true;
}

export function matchAppKeybinding(event: KeyEventLike): AppKeybindingHit | null {
  if (isAltGraph(event)) return null;
  for (const id of APP_ACTION_ORDER) {
    if (!matchActionBinding(event, id)) continue;
    const binding = currentSnapshot()[id];
    const parsed = binding === null ? null : parseBinding(binding);
    let digit: number | null = null;
    if (parsed && (parsed.key === "1-9" || /^[1-9]$/.test(parsed.key))) {
      const logical = logicalEventKey(event);
      if (logical !== null && /^[1-9]$/.test(logical) && (parsed.key === "1-9" || logical === parsed.key)) {
        digit = Number(logical);
      }
    }
    return { action: id, digit };
  }
  return null;
}

/**
 * Actions whose chords a focused terminal never receives: the app-level shortcuts, plus terminal search, which the
 * terminal pane intercepts before xterm. Listed in match precedence order.
 */
export const TERMINAL_WITHHELD_ACTIONS: readonly KeybindingActionId[] = [...APP_ACTION_ORDER, "terminalSearch"];

/** matchAppKeybinding widened to every chord a focused terminal withholds from the program running in it. */
export function matchTerminalWithheldKeybinding(event: KeyEventLike): AppKeybindingHit | null {
  const hit = matchAppKeybinding(event);
  if (hit) return hit;
  return !isAltGraph(event) && matchActionBinding(event, "terminalSearch") ? { action: "terminalSearch", digit: null } : null;
}

export interface KeybindingConflict {
  actions: [KeybindingAction, KeybindingAction];
  bindings: [string, string];
}

function modifiersOverlap(a: ParsedBinding, b: ParsedBinding, mac: boolean): boolean {
  if (a.shift !== b.shift) return false;
  if (a.alt !== b.alt) return false;
  const kinds = (p: ParsedBinding): string => {
    if (p.mod) return "mod";
    if (p.primary) return "primary";
    if (p.ctrl) return "ctrl";
    if (p.meta) return "meta";
    return "none";
  };
  const ka = kinds(a);
  const kb = kinds(b);
  if (ka === kb) return true;
  const pair = new Set([ka, kb]);
  if (pair.has("none")) return false;
  if (pair.has("mod")) return true;
  if (pair.has("primary") && pair.has("ctrl")) return !mac;
  if (pair.has("primary") && pair.has("meta")) return mac;
  if (pair.has("ctrl") && pair.has("meta")) return true;
  return false;
}

function keysOverlap(a: string, b: string): boolean {
  if (a === b) return true;
  if (a === "1-9") return /^[1-9]$/.test(b);
  if (b === "1-9") return /^[1-9]$/.test(a);
  return false;
}

export function bindingsConflict(bindingA: string, bindingB: string, mac: boolean): boolean {
  const a = parseBinding(bindingA);
  const b = parseBinding(bindingB);
  if (!a || !b) return false;
  return modifiersOverlap(a, b, mac) && keysOverlap(a.key, b.key);
}

export function bindingConflicts(
  bindings: KeybindingSnapshot = currentSnapshot(),
  mac = isMac(),
): KeybindingConflict[] {
  const parsed = KEYBINDING_ACTIONS.flatMap((action) => {
    const binding = bindings[action.id];
    const candidates = [
      binding,
      !WEB && mac && binding === action.defaultBinding ? action.macDefaultBinding : undefined,
    ];
    return candidates
      .filter((candidate): candidate is string => Boolean(candidate))
      .map((candidate) => ({ action, binding: candidate }));
  });
  const conflicts: KeybindingConflict[] = [];
  for (let i = 0; i < parsed.length; i++) {
    for (let j = i + 1; j < parsed.length; j++) {
      if (parsed[i].action.id === parsed[j].action.id) continue;
      if (!bindingsConflict(parsed[i].binding, parsed[j].binding, mac)) continue;
      conflicts.push({
        actions: [parsed[i].action, parsed[j].action],
        bindings: [parsed[i].binding, parsed[j].binding],
      });
    }
  }
  return conflicts;
}

// After account overrides load/clear, the keybinding snapshot is rebuilt automatically (linked to the preferences account override events).
subscribeAccountOverrides(() => {
  applyAccountKeybindingLayers(getAccountDefaults(), getAccountOverrides());
});
