import type { SnippetStep } from "../../ipc/types";

// Waiting for terminal output between the steps of a multi-step snippet the user started (expect-style).

export const DEFAULT_WAIT_TIMEOUT_MS = 10_000;
export const MIN_WAIT_TIMEOUT_MS = 1_000;
export const MAX_WAIT_TIMEOUT_MS = 10 * 60 * 1000;
// Matches the store's bound, which counts UTF-8 bytes.
export const MAX_WAIT_FOR_BYTES = 1024;
// The matched text is the output since the previous step, bounded to its most recent part.
export const OUTPUT_TAIL_CHARS = 64 * 1024;
// A regex is re-checked over the new output plus this much before it (from a line start where possible), so a
// match may span lines but a flood of output never re-scans the whole tail.
export const REGEX_LOOKBACK_CHARS = 4096;
// Output arriving in quick succession is checked at most once per interval.
export const MATCH_CHECK_INTERVAL_MS = 16;

// Checks text, of which everything from `from` on has not been checked yet.
export type StepMatcher = (text: string, from: number) => boolean;

export function stepWaitTimeoutMs(step: Pick<SnippetStep, "timeoutMs">): number {
  const value = step.timeoutMs ?? 0;
  return value > 0 ? value : DEFAULT_WAIT_TIMEOUT_MS;
}

export function waitPatternBytes(pattern: string): number {
  return new TextEncoder().encode(pattern).length;
}

// Regex waits use RE2 (the syntax the store validates with Go's regexp), which runs in linear time, so no
// pattern can stall the interface the way a backtracking JavaScript RegExp can. It is loaded only when needed.
export type RegexEngine = typeof import("re2js").RE2JS;

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

// Returns why a regex wait pattern cannot be used, or null when it can. ^ and $ also match at line boundaries.
export function regexPatternError(engine: RegexEngine, pattern: string): string | null {
  try {
    engine.compile(pattern, engine.MULTILINE);
    return null;
  } catch (cause) {
    return cause instanceof Error ? cause.message : String(cause);
  }
}

export function createTextMatcher(pattern: string): StepMatcher {
  return (text, from) => text.indexOf(pattern, Math.max(0, from - pattern.length + 1)) >= 0;
}

export function createRegexMatcher(engine: RegexEngine, pattern: string): StepMatcher {
  const expression = engine.compile(pattern, engine.MULTILINE);
  return (text, from) => {
    const target = Math.max(0, from - REGEX_LOOKBACK_CHARS);
    if (target === 0) return expression.test(text);
    const lineStart = text.lastIndexOf("\n", target - 1) + 1;
    if (target - lineStart <= REGEX_LOOKBACK_CHARS) return expression.test(text.slice(lineStart));
    // Cut inside a very long line: a NUL (which stripped output never contains) keeps ^ from matching there.
    return expression.test("\u0000" + text.slice(target));
  };
}

const ESC = 0x1b;
const BEL = 0x07;

type StripState = "ground" | "escape" | "escapeIntermediate" | "csi" | "string" | "stringEscape";

// Removes escape sequences (CSI, OSC, DCS, SOS, PM, APC and two-character escapes) and control characters
// other than newline and tab from a stream of decoded text. A sequence may be split across chunks.
export function createAnsiStripper(): (chunk: string) => string {
  let state: StripState = "ground";
  return (chunk) => {
    let out = "";
    for (const ch of chunk) {
      const c = ch.codePointAt(0) ?? 0;
      switch (state) {
        case "ground":
          if (c === ESC) state = "escape";
          else if (c === 0x9b) state = "csi";
          else if (c === 0x9d || c === 0x90 || c === 0x98 || c === 0x9e || c === 0x9f) state = "string";
          else if (c === 0x0a || c === 0x09) out += ch;
          else if (c < 0x20 || c === 0x7f || (c >= 0x80 && c <= 0x9f)) {
            // Carriage returns and other control characters carry no text.
          } else out += ch;
          break;
        case "escape":
          if (c === 0x5b) state = "csi";
          else if (c === 0x5d || c === 0x50 || c === 0x58 || c === 0x5e || c === 0x5f) state = "string";
          else if (c >= 0x20 && c <= 0x2f) state = "escapeIntermediate";
          else if (c !== ESC) state = "ground";
          break;
        case "escapeIntermediate":
          if (c === ESC) state = "escape";
          else if (c < 0x20 || c > 0x2f) state = "ground";
          break;
        case "csi":
          if (c === ESC) state = "escape";
          else if (c >= 0x40 && c <= 0x7e) state = "ground";
          break;
        case "string":
          if (c === BEL || c === 0x9c) state = "ground";
          else if (c === ESC) state = "stringEscape";
          break;
        case "stringEscape":
          // ESC \ ends the string; any other escape aborts it and starts a new sequence.
          if (c === 0x5c) state = "ground";
          else if (c === 0x5b) state = "csi";
          else if (c === 0x5d || c === 0x50 || c === 0x58 || c === 0x5e || c === 0x5f) state = "string";
          else if (c === ESC) state = "escape";
          else state = "ground";
          break;
      }
    }
    return out;
  };
}

export interface OutputTail {
  text: () => string;
  // Characters appended since the last clear, including those trimmed off the front.
  total: () => number;
  // Appends decoded text that may contain escape sequences.
  append: (chunk: string) => void;
  appendBytes: (bytes: Uint8Array) => void;
  // Forgets the output so far; the next wait only sees output that arrives afterwards.
  clear: () => void;
  onAppend: (listener: () => void) => () => void;
}

export function createOutputTail(limit = OUTPUT_TAIL_CHARS): OutputTail {
  const strip = createAnsiStripper();
  const decoder = new TextDecoder();
  const listeners = new Set<() => void>();
  let text = "";
  let total = 0;
  const add = (plain: string) => {
    if (!plain) return;
    text += plain;
    total += plain.length;
    if (text.length > limit) text = text.slice(text.length - limit);
    for (const listener of [...listeners]) listener();
  };
  return {
    text: () => text,
    total: () => total,
    append: (chunk) => add(strip(chunk)),
    appendBytes: (bytes) => add(strip(decoder.decode(bytes, { stream: true }))),
    clear: () => {
      text = "";
      total = 0;
    },
    onAppend: (listener) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}

export type WaitOutcome = "matched" | "timeout" | "cancelled";

// Resolves once the tail matches (checking what it already holds first), when the timeout passes, or when
// the signal aborts. New output is checked at most once per MATCH_CHECK_INTERVAL_MS.
export function waitForOutput(tail: OutputTail, matcher: StepMatcher, timeoutMs: number, signal: AbortSignal): Promise<WaitOutcome> {
  if (signal.aborted) return Promise.resolve("cancelled");
  if (matcher(tail.text(), 0)) return Promise.resolve("matched");
  let checked = tail.total();
  return new Promise((resolve) => {
    let settled = false;
    let checkTimer: ReturnType<typeof setTimeout> | null = null;
    const finish = (outcome: WaitOutcome) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      if (checkTimer !== null) clearTimeout(checkTimer);
      unsubscribe();
      signal.removeEventListener("abort", onAbort);
      resolve(outcome);
    };
    const check = () => {
      checkTimer = null;
      const text = tail.text();
      const from = Math.max(0, text.length - (tail.total() - checked));
      checked = tail.total();
      if (matcher(text, from)) finish("matched");
    };
    const onAbort = () => finish("cancelled");
    const unsubscribe = tail.onAppend(() => {
      checkTimer ??= setTimeout(check, MATCH_CHECK_INTERVAL_MS);
    });
    const timer = setTimeout(() => {
      // Output that arrived just before the deadline still counts.
      if (checkTimer !== null) {
        clearTimeout(checkTimer);
        check();
      }
      finish("timeout");
    }, timeoutMs);
    signal.addEventListener("abort", onAbort);
  });
}
