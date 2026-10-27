import type {
  StateRuleDraftDTO,
  StateRuleImportRequest,
  StateRuleIntent,
  StateRuleSaveRequest,
  StateRuleScreenDTO,
} from "../../ipc/stateRuleApi";

// Limits mirrored from internal/terminal/staterule for checks before a round trip; the backend stays authoritative.
export const STATE_RULE_MAX_RULES = 64;
export const STATE_RULE_MAX_PATTERN_BYTES = 512;
export const STATE_RULE_MAX_LINES = 50;
export const STATE_RULE_DEFAULT_LINES = 5;
const MAX_LINE_BYTES = 256;
const MAX_TITLE_BYTES = 1024;
// The heuristic keeps this many characters of the line it quotes.
const HEURISTIC_TEXT_CHARS = 120;
// Rows of the screen bottom the AI draft is built from; mirrors stateRuleDraftRows in the backend.
export const STATE_RULE_DRAFT_ROWS = 30;
export const STATE_RULE_REDACTED_NOTE = "已隐去疑似密钥/密码";

export const STATE_RULE_EXPORT_FORMAT = "nexterm.state-rules";
export const STATE_RULE_EXPORT_VERSION = 1;

export type StateRuleTarget = "needs_me" | "running" | "idle" | "just_finished";

export const STATE_RULE_TARGETS: readonly { value: StateRuleTarget; label: string }[] = [
  { value: "needs_me", label: "需要我" },
  { value: "running", label: "运行中" },
  { value: "idle", label: "空闲" },
  { value: "just_finished", label: "刚结束" },
];

export function stateLabel(state: string): string {
  if (state === "unknown") return "未知";
  if (state === "exited") return "已退出";
  return STATE_RULE_TARGETS.find((target) => target.value === state)?.label ?? state;
}

// stateRuleTargets are the states a marking may set, as the backend checks an AI draft: "this is it" means needs_me,
// "not this" any state but the one shown.
export function stateRuleTargets(intent: StateRuleIntent, current: string): StateRuleTarget[] {
  if (intent === "this_is_it") return ["needs_me"];
  return STATE_RULE_TARGETS.map((target) => target.value).filter((state) => state !== current);
}

// The state a "not this" draft proposes first: a false needs_me is most often a program at work, anything else most
// often a quiet screen. The user can pick another.
export function defaultCorrection(intent: StateRuleIntent, current: string): StateRuleTarget {
  if (intent === "this_is_it") return "needs_me";
  return current === "needs_me" ? "running" : "idle";
}

const utf8 = new TextEncoder();

function byteLength(text: string): number {
  return utf8.encode(text).length;
}

// clipBytes keeps the leading characters of text that fit in limit UTF-8 bytes, as the helper reads each row.
function clipBytes(text: string, limit: number): string {
  if (byteLength(text) <= limit) return text;
  let used = 0;
  let out = "";
  for (const char of text) {
    const size = byteLength(char);
    if (used + size > limit) break;
    used += size;
    out += char;
  }
  return out;
}

// clipTail keeps the trailing characters of text that fit in limit UTF-8 bytes.
function clipTail(text: string, limit: number): string {
  if (byteLength(text) <= limit) return text;
  let used = 0;
  const kept: string[] = [];
  for (const char of Array.from(text).reverse()) {
    const size = byteLength(char);
    if (used + size > limit) break;
    used += size;
    kept.push(char);
  }
  return kept.reverse().join("");
}

// quoteRe2 escapes the characters RE2 treats specially, as Go's regexp.QuoteMeta does.
export function quoteRe2(text: string): string {
  return text.replace(/[\\.+*?()|[\]{}^$]/g, "\\$&");
}

// Mirrors internal/ai/tools.RedactText, which the backend applies before the screen reaches the AI model.
const REDACT_PATTERNS: readonly [RegExp, string][] = [
  [/(authorization\s*[:=]\s*(?:bearer\s+)?)([^\s,;]+)/gi, "$1<redacted>"],
  [/(bearer\s+)([^\s,;]+)/gi, "$1<redacted>"],
  [/(mysql(?:\.exe)?\s+[^\r\n]*?-p)([^\s]+)/gi, "$1<redacted>"],
  [/(password|passwd|token|secret|api[_-]?key)(\s*[:=]\s*|\s+)([^\s,;]+)/gi, "$1$2<redacted>"],
];
const REDACTED_MARK = "<redacted>";

export function redactSecrets(text: string): string {
  return REDACT_PATTERNS.reduce((value, [pattern, replacement]) => value.replace(pattern, replacement), text);
}

function blank(line: string): boolean {
  return line.trim() === "";
}

// regionEnd is the last row of a bottom region: the lower of the cursor row and the last non-blank row.
function regionEnd(screen: StateRuleScreenDTO): number {
  const end = Math.min(screen.cursorRow, screen.lines.length - 1);
  for (let i = screen.lines.length - 1; i > end; i--) {
    if (!blank(screen.lines[i])) return i;
  }
  return end;
}

// draftRegionText is the text an AI draft is built from, as the backend picks it: the bottom rows of the screen and the
// window title.
export function draftRegionText(screen: StateRuleScreenDTO): { rows: string; title: string } {
  const end = regionEnd(screen);
  if (end < 0) return { rows: "", title: clipTail(screen.title ?? "", MAX_TITLE_BYTES) };
  const start = Math.max(0, end - STATE_RULE_DRAFT_ROWS + 1);
  return {
    rows: screen.lines
      .slice(start, end + 1)
      .map((line) => clipBytes(line, MAX_LINE_BYTES))
      .join("\n"),
    title: clipTail(screen.title ?? "", MAX_TITLE_BYTES),
  };
}

// heuristicDraft quotes the last non-empty line of the screen, for use without an AI model. Suspected secrets in the
// line are masked, and the draft says so in redacted. It returns null for a screen without text.
export function heuristicDraft(screen: StateRuleScreenDTO, intent: StateRuleIntent, current: string): StateRuleDraftDTO | null {
  const end = regionEnd(screen);
  for (let row = end; row >= 0; row--) {
    const visible = clipBytes(screen.lines[row] ?? "", MAX_LINE_BYTES).trim();
    if (!visible) continue;
    const masked = redactSecrets(visible);
    const redacted = masked !== visible;
    const text = Array.from(masked).slice(0, HEURISTIC_TEXT_CHARS).join("").trim();
    return {
      name: Array.from(text).slice(0, 60).join(""),
      region: "bottom",
      lines: Math.min(STATE_RULE_MAX_LINES, Math.max(STATE_RULE_DEFAULT_LINES, end - row + 1)),
      // A masked value matches any word, so the rule still recognizes the line.
      pattern: quoteRe2(text).split(REDACTED_MARK).join("\\S+"),
      state: defaultCorrection(intent, current),
      ...(redacted ? { redacted } : {}),
    };
  }
  return null;
}

export function screenFromSnapshot(snapshot: { lines: string[]; cursorRow: number }, title?: string): StateRuleScreenDTO {
  return { lines: snapshot.lines, cursorRow: snapshot.cursorRow, ...(title ? { title } : {}) };
}

// The rule as the editor holds it.
export interface StateRuleDraftForm {
  id?: string;
  name: string;
  scope: "global" | "host";
  assetId: string;
  region: "bottom" | "title";
  lines: number;
  pattern: string;
  state: StateRuleTarget;
  keys: string;
  enabled: boolean;
}

// A pattern this short or this open matches almost any screen, so the rule would fire everywhere.
const MIN_SPECIFIC_PATTERN_CHARS = 4;

// patternWarning is a hint, not an error, for a pattern that is probably too broad. It is null for an empty pattern.
export function patternWarning(pattern: string): string | null {
  const trimmed = pattern.trim();
  if (!trimmed) return null;
  if (/^\.[*+]$/.test(trimmed)) return "这个模式几乎匹配任何内容，规则会在所有屏幕上生效。";
  if (Array.from(trimmed).length < MIN_SPECIFIC_PATTERN_CHARS) return "匹配模式很短，可能误匹配其他屏幕，建议写得更具体。";
  return null;
}

export function emptyRuleForm(): StateRuleDraftForm {
  return { name: "", scope: "global", assetId: "", region: "bottom", lines: STATE_RULE_DEFAULT_LINES, pattern: "", state: "needs_me", keys: "", enabled: true };
}

function asTarget(state: string): StateRuleTarget {
  return STATE_RULE_TARGETS.some((target) => target.value === state) ? (state as StateRuleTarget) : "needs_me";
}

export function formFromDraft(draft: StateRuleDraftDTO, assetId?: string): StateRuleDraftForm {
  return {
    ...emptyRuleForm(),
    name: draft.name,
    region: draft.region === "title" ? "title" : "bottom",
    lines: draft.lines && draft.lines > 0 ? draft.lines : STATE_RULE_DEFAULT_LINES,
    pattern: draft.pattern,
    state: asTarget(draft.state),
    // A marking concerns one host; the user can widen it.
    scope: assetId ? "host" : "global",
    assetId: assetId ?? "",
  };
}

export function formFromRule(rule: {
  id: string;
  name: string;
  scope: string;
  assetId?: string;
  region: string;
  lines?: number;
  pattern: string;
  state: string;
  keys?: string;
  enabled: boolean;
}): StateRuleDraftForm {
  return {
    id: rule.id,
    name: rule.name,
    scope: rule.scope === "host" ? "host" : "global",
    assetId: rule.assetId ?? "",
    region: rule.region === "title" ? "title" : "bottom",
    lines: rule.lines && rule.lines > 0 ? rule.lines : STATE_RULE_DEFAULT_LINES,
    pattern: rule.pattern,
    state: asTarget(rule.state),
    keys: rule.keys ?? "",
    enabled: rule.enabled,
  };
}

export function saveRequestFromForm(form: StateRuleDraftForm): StateRuleSaveRequest {
  return {
    ...(form.id ? { id: form.id } : {}),
    name: form.name.trim(),
    scope: form.scope,
    ...(form.scope === "host" ? { assetId: form.assetId } : {}),
    region: form.region,
    ...(form.region === "bottom" ? { lines: form.lines } : {}),
    pattern: form.pattern,
    state: form.state,
    ...(form.state === "needs_me" && form.keys ? { keys: form.keys } : {}),
    enabled: form.enabled,
  };
}

// parseRuleFile reads an exported rules file and checks its shape and the limits the backend would reject, so a bad
// file is reported before anything is sent. The backend validates every rule again.
export function parseRuleFile(text: string): StateRuleImportRequest {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    throw new Error("不是有效的 JSON");
  }
  if (typeof value !== "object" || value === null) throw new Error("不是状态规则文件");
  const file = value as Partial<StateRuleImportRequest>;
  if (file.format !== STATE_RULE_EXPORT_FORMAT || file.version !== STATE_RULE_EXPORT_VERSION || !Array.isArray(file.rules)) {
    throw new Error("不是可识别的状态规则文件");
  }
  if (file.rules.length > STATE_RULE_MAX_RULES) throw new Error(`一次最多导入 ${STATE_RULE_MAX_RULES} 条状态规则`);
  file.rules.forEach((rule, index) => {
    if (typeof rule !== "object" || rule === null || typeof rule.pattern !== "string" || typeof rule.state !== "string" || typeof rule.region !== "string") {
      throw new Error(`第 ${index + 1} 条规则格式不正确`);
    }
    if (byteLength(rule.pattern) > STATE_RULE_MAX_PATTERN_BYTES) {
      throw new Error(`第 ${index + 1} 条规则的匹配模式超过 ${STATE_RULE_MAX_PATTERN_BYTES} 字节`);
    }
  });
  return { format: file.format, version: file.version, rules: file.rules };
}
