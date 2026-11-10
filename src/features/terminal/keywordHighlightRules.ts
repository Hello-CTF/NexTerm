import { useSyncExternalStore } from "react";
import { createSyncedPreference, type SyncedPreference } from "../../app/syncedPreference";
import { t } from "../../i18n";

// Keyword highlight rules: the stored settings, the built-in presets, validation and matching. The renderer that
// applies them to a terminal is keywordHighlight.ts. internal/terminal/highlight validates the same settings.

export const HIGHLIGHT_COLORS = ["red", "orange", "yellow", "green", "cyan", "blue", "magenta", "gray"] as const;
export type HighlightColor = (typeof HIGHLIGHT_COLORS)[number];

export interface KeywordHighlightRule {
  id: string;
  pattern: string;
  /** The pattern is a regular expression (the RE2-compatible subset of JavaScript); otherwise plain text. */
  regex: boolean;
  caseSensitive: boolean;
  /** A match that begins or ends with a letter, digit or underscore may not continue a word. */
  wholeWord: boolean;
  foreground: HighlightColor | "";
  background: HighlightColor | "";
  underline: boolean;
  enabled: boolean;
  /** Limits the rule to sessions of these assets; absent applies it to every session. */
  assetIds?: string[];
}

export interface KeywordHighlightSettings {
  /** Enabled built-in presets. */
  presets: string[];
  rules: KeywordHighlightRule[];
}

// The limits internal/terminal/highlight enforces.
export const KEYWORD_HIGHLIGHT_LIMITS = {
  rules: 50,
  patternBytes: 256,
  assets: 32,
  totalBytes: 48 * 1024,
} as const;

export const KEYWORD_HIGHLIGHTS_PREFERENCE_KEY = "terminal.keywordHighlights";
const STORAGE_KEY = "nexterm.keywordHighlights.v1";

export interface HighlightStyle {
  foreground?: HighlightColor;
  background?: HighlightColor;
  underline: boolean;
}

export interface KeywordHighlightPreset {
  id: string;
  label: string;
  description: string;
  rules: Omit<KeywordHighlightRule, "id" | "enabled" | "assetIds">[];
}

const presetRule = (
  pattern: string,
  style: Pick<KeywordHighlightRule, "foreground" | "background" | "underline">,
  flags: Partial<Pick<KeywordHighlightRule, "caseSensitive" | "wholeWord">> = {},
): KeywordHighlightPreset["rules"][number] => ({
  pattern,
  regex: true,
  caseSensitive: flags.caseSensitive ?? false,
  wholeWord: flags.wholeWord ?? true,
  ...style,
});

/** Built-in presets. They are off until the user enables them, and apply after the user's own rules. */
export const KEYWORD_HIGHLIGHT_PRESETS: readonly KeywordHighlightPreset[] = [
  {
    id: "errors",
    label: t("错误"),
    description: "ERROR, FAIL, FATAL, PANIC, DENIED",
    rules: [
      presetRule("(?:errors?|fail(?:ed|ure|s)?|fatal|critical|panic|denied|exception)", { foreground: "red", background: "", underline: false }),
    ],
  },
  {
    id: "warnings",
    label: t("警告"),
    description: "WARN, WARNING, DEPRECATED",
    rules: [presetRule("(?:warn(?:ing|ings)?|deprecated)", { foreground: "yellow", background: "", underline: false })],
  },
  {
    id: "success",
    label: t("成功"),
    description: "OK, SUCCESS, PASSED, DONE",
    rules: [
      presetRule("OK", { foreground: "green", background: "", underline: false }, { caseSensitive: true }),
      presetRule("(?:success(?:ful)?|succeeded|passed)", { foreground: "green", background: "", underline: false }),
    ],
  },
  {
    id: "ipv4",
    label: t("IPv4 地址"),
    description: "192.168.1.10",
    rules: [
      presetRule("(?:(?:25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)\\.){3}(?:25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)", {
        foreground: "cyan",
        background: "",
        underline: false,
      }),
    ],
  },
  {
    id: "uuid",
    label: "UUID",
    description: "123e4567-e89b-12d3-a456-426614174000",
    rules: [
      presetRule("[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}", {
        foreground: "magenta",
        background: "",
        underline: false,
      }),
    ],
  },
];

const encoder = new TextEncoder();
const CONTROL_CHARS = /[\u0000-\u001f\u007f-\u009f]/;

// Patterns are RE2, the syntax internal/terminal/highlight validates with Go's regexp, and are matched with re2js,
// which runs in time linear in the text: no pattern can stall the terminal the way a backtracking JavaScript
// RegExp can. The engine is loaded on first use.
export type RegexEngine = typeof import("re2js").RE2JS;
type CompiledPattern = ReturnType<RegexEngine["compile"]>;

let regexEngine: RegexEngine | null = null;
let regexEngineLoad: Promise<RegexEngine> | null = null;

export function loadRegexEngine(): Promise<RegexEngine> {
  regexEngineLoad ??= import("re2js").then(
    (module) => (regexEngine = module.RE2JS),
    (cause: unknown) => {
      regexEngineLoad = null;
      throw cause;
    },
  );
  return regexEngineLoad;
}

export function loadedRegexEngine(): RegexEngine | null {
  return regexEngine;
}

function compilePattern(engine: RegexEngine, rule: Pick<KeywordHighlightRule, "pattern" | "regex" | "caseSensitive">): CompiledPattern {
  const source = rule.regex ? rule.pattern : engine.quote(rule.pattern);
  return engine.compile(source, rule.caseSensitive ? 0 : engine.CASE_INSENSITIVE);
}

/** Returns why a rule's pattern cannot be used, or null when it is valid. Go also caps the compiled program size. */
export function highlightPatternError(
  engine: RegexEngine,
  rule: Pick<KeywordHighlightRule, "pattern" | "regex" | "caseSensitive">,
): string | null {
  if (!rule.pattern.trim()) return t("请填写匹配内容");
  if (encoder.encode(rule.pattern).length > KEYWORD_HIGHLIGHT_LIMITS.patternBytes) {
    return t("匹配内容不能超过 {bytes} 字节", { bytes: KEYWORD_HIGHLIGHT_LIMITS.patternBytes });
  }
  if (CONTROL_CHARS.test(rule.pattern)) return t("匹配内容不能包含换行或控制字符");
  let compiled: CompiledPattern;
  try {
    compiled = compilePattern(engine, rule);
  } catch (cause) {
    return t("正则表达式无效：{error}", { error: cause instanceof Error ? cause.message : String(cause) });
  }
  if (compiled.matcher("").find()) return t("匹配内容不能匹配空文本");
  return null;
}

/** Returns why a rule cannot be saved, or null when it is valid. */
export function highlightRuleError(engine: RegexEngine, rule: KeywordHighlightRule): string | null {
  const patternError = highlightPatternError(engine, rule);
  if (patternError) return patternError;
  if (!rule.foreground && !rule.background && !rule.underline) return t("请至少选择一种样式");
  if ((rule.assetIds?.length ?? 0) > KEYWORD_HIGHLIGHT_LIMITS.assets) {
    return t("最多选择 {count} 台主机", { count: KEYWORD_HIGHLIGHT_LIMITS.assets });
  }
  return null;
}

export function highlightSettingsError(engine: RegexEngine, settings: KeywordHighlightSettings): string | null {
  if (settings.rules.length > KEYWORD_HIGHLIGHT_LIMITS.rules) {
    return t("最多保存 {count} 条高亮规则", { count: KEYWORD_HIGHLIGHT_LIMITS.rules });
  }
  for (const rule of settings.rules) {
    const error = highlightRuleError(engine, rule);
    if (error) return `「${rule.pattern}」：${error}`;
  }
  if (encoder.encode(JSON.stringify(settings)).length > KEYWORD_HIGHLIGHT_LIMITS.totalBytes) {
    return t("高亮规则总大小超过 48 KiB，请删减规则");
  }
  return null;
}

const isColor = (value: unknown): value is HighlightColor | "" =>
  value === "" || (typeof value === "string" && (HIGHLIGHT_COLORS as readonly string[]).includes(value));

/**
 * Reads a stored value. Rules with a known shape are kept even when their pattern no longer validates, so the
 * editor can show them as invalid instead of dropping them silently; such rules are not applied.
 */
export function parseKeywordHighlights(value: unknown): KeywordHighlightSettings | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const { presets, rules } = value as Record<string, unknown>;
  const out: KeywordHighlightSettings = { presets: [], rules: [] };
  if (Array.isArray(presets)) {
    for (const preset of presets) if (typeof preset === "string" && !out.presets.includes(preset)) out.presets.push(preset);
  }
  if (Array.isArray(rules)) {
    for (const entry of rules) {
      if (!entry || typeof entry !== "object") continue;
      const r = entry as Record<string, unknown>;
      if (typeof r.id !== "string" || typeof r.pattern !== "string" || !isColor(r.foreground ?? "") || !isColor(r.background ?? "")) continue;
      const rule: KeywordHighlightRule = {
        id: r.id,
        pattern: r.pattern,
        regex: r.regex === true,
        caseSensitive: r.caseSensitive === true,
        wholeWord: r.wholeWord === true,
        foreground: (r.foreground ?? "") as HighlightColor | "",
        background: (r.background ?? "") as HighlightColor | "",
        underline: r.underline === true,
        enabled: r.enabled === true,
      };
      if (Array.isArray(r.assetIds)) {
        const assetIds = r.assetIds.filter((id): id is string => typeof id === "string");
        if (assetIds.length > 0) rule.assetIds = assetIds;
      }
      out.rules.push(rule);
      if (out.rules.length === KEYWORD_HIGHLIGHT_LIMITS.rules) break;
    }
  }
  return out;
}

export const emptyKeywordHighlights = (): KeywordHighlightSettings => ({ presets: [], rules: [] });

// Created on first use, so importing the terminal view does not subscribe to the account preferences by itself.
let preference: SyncedPreference<KeywordHighlightSettings> | null = null;
const store = (): SyncedPreference<KeywordHighlightSettings> =>
  (preference ??= createSyncedPreference<KeywordHighlightSettings>({
    key: KEYWORD_HIGHLIGHTS_PREFERENCE_KEY,
    localKey: STORAGE_KEY,
    parse: parseKeywordHighlights,
    fallback: emptyKeywordHighlights,
  }));
const subscribe = (listener: () => void) => store().subscribe(listener);
const get = () => store().get();

export function getKeywordHighlights(): KeywordHighlightSettings {
  return get();
}

export function subscribeKeywordHighlights(listener: () => void): () => void {
  return subscribe(listener);
}

export function useKeywordHighlights(): KeywordHighlightSettings {
  return useSyncExternalStore(subscribe, get);
}

/** Saves to the account (or the device without one); rejects an invalid value or a failed account write. */
export async function saveKeywordHighlights(settings: KeywordHighlightSettings): Promise<void> {
  const error = highlightSettingsError(await loadRegexEngine(), settings);
  if (error) throw new Error(error);
  await store().set({
    presets: [...settings.presets],
    rules: settings.rules.map((rule) => {
      const { assetIds, ...rest } = rule;
      return assetIds && assetIds.length > 0 ? { ...rest, assetIds: [...assetIds] } : rest;
    }),
  });
}

export function newHighlightRuleId(): string {
  return `kh-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

export interface CompiledHighlight {
  re: CompiledPattern;
  wholeWord: boolean;
  style: HighlightStyle;
}

function styleOf(rule: Pick<KeywordHighlightRule, "foreground" | "background" | "underline">): HighlightStyle {
  return {
    ...(rule.foreground ? { foreground: rule.foreground } : {}),
    ...(rule.background ? { background: rule.background } : {}),
    underline: rule.underline,
  };
}

function compileRule(engine: RegexEngine, rule: Omit<KeywordHighlightRule, "id" | "enabled">): CompiledHighlight | null {
  if (highlightPatternError(engine, rule) !== null) return null;
  if (!rule.foreground && !rule.background && !rule.underline) return null;
  return { re: compilePattern(engine, rule), wholeWord: rule.wholeWord, style: styleOf(rule) };
}

/** The rules that apply to a session of assetId, in priority order: the user's enabled rules, then the presets. */
export function compileKeywordHighlights(
  engine: RegexEngine,
  settings: KeywordHighlightSettings,
  assetId?: string,
): CompiledHighlight[] {
  const out: CompiledHighlight[] = [];
  for (const rule of settings.rules) {
    if (!rule.enabled) continue;
    if (rule.assetIds && rule.assetIds.length > 0 && (!assetId || !rule.assetIds.includes(assetId))) continue;
    const compiled = compileRule(engine, rule);
    if (compiled) out.push(compiled);
  }
  for (const preset of KEYWORD_HIGHLIGHT_PRESETS) {
    if (!settings.presets.includes(preset.id)) continue;
    for (const rule of preset.rules) {
      const compiled = compileRule(engine, rule);
      if (compiled) out.push(compiled);
    }
  }
  return out;
}

export interface HighlightMatch {
  start: number;
  end: number;
  style: HighlightStyle;
}

/** Text beyond this many UTF-16 units of a logical line is not matched. */
export const MAX_HIGHLIGHT_LINE_CHARS = 2000;
export const MAX_MATCHES_PER_LINE = 64;
/** Searches one rule makes on one line at most, counting matches refused as partial words. */
const MAX_SEARCHES_PER_RULE = 128;

const WORD = /[A-Za-z0-9_]/;
const isWord = (ch: string | undefined) => ch !== undefined && WORD.test(ch);

/**
 * Matches the rules on one logical line. An earlier rule wins where matches overlap. Whole-word matching only
 * checks boundaries at letters, numbers and underscores, so a CJK keyword matches inside CJK text.
 */
export function matchKeywordHighlights(
  text: string,
  rules: readonly CompiledHighlight[],
  limit = MAX_MATCHES_PER_LINE,
): HighlightMatch[] {
  const line = text.length > MAX_HIGHLIGHT_LINE_CHARS ? text.slice(0, MAX_HIGHLIGHT_LINE_CHARS) : text;
  const found: HighlightMatch[] = [];
  if (!line.trim()) return found;
  const overlaps = (start: number, end: number) => found.some((m) => start < m.end && m.start < end);
  const after = (at: number) => at + ((line.codePointAt(at) ?? 0) > 0xffff ? 2 : 1);
  for (const rule of rules) {
    const matcher = rule.re.matcher(line);
    for (let from = 0, searches = 0; from <= line.length && searches < MAX_SEARCHES_PER_RULE && matcher.find(from); searches++) {
      const start = matcher.start();
      const end = matcher.end();
      if (end <= start) {
        from = after(start);
        continue;
      }
      if (rule.wholeWord && ((isWord(line[start]) && isWord(line[start - 1])) || (isWord(line[end - 1]) && isWord(line[end])))) {
        from = after(start);
        continue;
      }
      if (!overlaps(start, end)) {
        found.push({ start, end, style: rule.style });
        if (found.length >= limit) return found.sort((a, b) => a.start - b.start);
      }
      from = end;
    }
  }
  return found.sort((a, b) => a.start - b.start);
}

// Palette colors per terminal theme: foregrounds read on the theme's background, backgrounds keep the theme's
// default text readable.
const FOREGROUND: Record<"dark" | "light", Record<HighlightColor, string>> = {
  dark: {
    red: "#f87171",
    orange: "#fb923c",
    yellow: "#facc15",
    green: "#4ade80",
    cyan: "#22d3ee",
    blue: "#60a5fa",
    magenta: "#e879f9",
    gray: "#a3a3a3",
  },
  light: {
    red: "#b91c1c",
    orange: "#c2410c",
    yellow: "#a16207",
    green: "#15803d",
    cyan: "#0e7490",
    blue: "#1d4ed8",
    magenta: "#a21caf",
    gray: "#525252",
  },
};
const BACKGROUND: Record<"dark" | "light", Record<HighlightColor, string>> = {
  dark: {
    red: "#7f1d1d",
    orange: "#7c2d12",
    yellow: "#713f12",
    green: "#14532d",
    cyan: "#164e63",
    blue: "#1e3a8a",
    magenta: "#701a75",
    gray: "#404040",
  },
  light: {
    red: "#fecaca",
    orange: "#fed7aa",
    yellow: "#fef08a",
    green: "#bbf7d0",
    cyan: "#a5f3fc",
    blue: "#bfdbfe",
    magenta: "#f5d0fe",
    gray: "#e5e5e5",
  },
};

export function highlightForeground(color: HighlightColor, theme: "dark" | "light"): string {
  return FOREGROUND[theme][color];
}

export function highlightBackground(color: HighlightColor, theme: "dark" | "light"): string {
  return BACKGROUND[theme][color];
}

export function highlightColorLabel(color: HighlightColor): string {
  const labels: Record<HighlightColor, string> = {
    red: t("红色"),
    orange: t("橙色"),
    yellow: t("黄色"),
    green: t("绿色"),
    cyan: t("青色"),
    blue: t("蓝色"),
    magenta: t("品红"),
    gray: t("灰色"),
  };
  return labels[color];
}
