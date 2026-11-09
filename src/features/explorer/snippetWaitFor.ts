import type { SnippetStep } from "../../ipc/types";

// Waiting for terminal output between the steps of a multi-step snippet the user started (expect-style).

export const DEFAULT_WAIT_TIMEOUT_MS = 10_000;
export const MIN_WAIT_TIMEOUT_MS = 1_000;
export const MAX_WAIT_TIMEOUT_MS = 10 * 60 * 1000;
export const MAX_WAIT_FOR_LENGTH = 1024;
// The matched text is the output since the previous step, bounded to its most recent part.
export const OUTPUT_TAIL_CHARS = 64 * 1024;

export type StepMatcher = (text: string) => boolean;

export function stepWaitTimeoutMs(step: Pick<SnippetStep, "timeoutMs">): number {
  const value = step.timeoutMs ?? 0;
  return value > 0 ? value : DEFAULT_WAIT_TIMEOUT_MS;
}

// Returns the reason a wait pattern cannot be used, or null when it can. A regex uses the multiline flag, so
// ^ and $ also match at line boundaries.
export function waitPatternError(pattern: string, regex: boolean): string | null {
  try {
    if (regex) new RegExp(pattern, "m");
    return null;
  } catch (cause) {
    return cause instanceof Error ? cause.message : String(cause);
  }
}

export function createStepMatcher(pattern: string, regex: boolean): StepMatcher {
  if (regex) {
    const expression = new RegExp(pattern, "m");
    return (text) => expression.test(text);
  }
  return (text) => text.includes(pattern);
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
  const add = (plain: string) => {
    if (!plain) return;
    text += plain;
    if (text.length > limit) text = text.slice(text.length - limit);
    for (const listener of [...listeners]) listener();
  };
  return {
    text: () => text,
    append: (chunk) => add(strip(chunk)),
    appendBytes: (bytes) => add(strip(decoder.decode(bytes, { stream: true }))),
    clear: () => {
      text = "";
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
// the signal aborts.
export function waitForOutput(tail: OutputTail, matcher: StepMatcher, timeoutMs: number, signal: AbortSignal): Promise<WaitOutcome> {
  if (signal.aborted) return Promise.resolve("cancelled");
  if (matcher(tail.text())) return Promise.resolve("matched");
  return new Promise((resolve) => {
    let settled = false;
    const finish = (outcome: WaitOutcome) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      unsubscribe();
      signal.removeEventListener("abort", onAbort);
      resolve(outcome);
    };
    const onAbort = () => finish("cancelled");
    const unsubscribe = tail.onAppend(() => {
      if (matcher(tail.text())) finish("matched");
    });
    const timer = setTimeout(() => finish("timeout"), timeoutMs);
    signal.addEventListener("abort", onAbort);
  });
}
