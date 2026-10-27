// Rate limiting for the terminal's automatic query replies (DA/DSR/CPR/window
// reports, DCS and OSC colour replies) that xterm emits through onData.
//
// Threat: a hostile or broken host emits queries in a tight loop and the client
// answers each one upstream, flooding the channel and the remote PTY input.
// Invariant: a well-behaved program always gets exactly one reply per query,
// immediately and in the order xterm produced them, no matter how long the
// session runs.
//
// Design: one byte-denominated token bucket per terminal. Its burst covers the
// largest realistic query burst (a TUI start-up, a full 256-entry palette
// probe) and it refills continuously, so ordinary use never touches the limit
// and a long session never exhausts it. While the bucket has tokens and nothing
// is queued, a reply is written synchronously. Once it is empty, replies queue
// in arrival order and drain as tokens refill; a queued reply is replaced by a
// newer reply with the same coalescing key (CPR/window size/OSC colour per
// index reflect the latest state, DA/DSR/DCS replies collapse only when
// identical), and the queue is bounded in bytes, so a flood costs at most the
// refill rate in upstream bytes. Replies queued for an attachment generation
// are discarded when the generation changes and are never sent into a new one.
// Suppression is never shown to the user; it is counted in stats() and logged
// once per generation for diagnostics.
//
// User input deliberately bypasses the bucket and the queue: Ctrl+C or a
// typed command must never wait behind a hostile query flood. Replies keep
// their order among themselves, but a reply deferred during a flood can reach
// the remote after input typed later. That only happens while the host is
// flooding, never for a well-behaved program, and each reply is still written
// whole, so it cannot split typed bytes.

export type TerminalResponseType =
  | "da"
  | "dsr"
  | "cpr"
  | "window"
  | "dcs"
  | "osc-color"
  | "mode"
  | "kitty-keyboard";

const PATTERNS: [TerminalResponseType, RegExp][] = [
  ["da", /^\x1b\[\?[\d;]*c$/],
  ["da", /^\x1b\[[>=][\d;]*c$/],
  ["da", /^\x1b\/Z$/],
  ["dsr", /^\x1b\[\d+n$/],
  ["cpr", /^\x1b\[\??\d+;\d+R$/],
  ["window", /^\x1b\[8;\d+;\d+t$/],
  // DECRQM reply: CSI ? Ps ; Pm $ y (DEC private) or CSI Ps ; Pm $ y (ANSI).
  ["mode", /^\x1b\[\??\d+;\d+\$y$/],
  // Kitty keyboard protocol flags reply: CSI ? flags u.
  ["kitty-keyboard", /^\x1b\[\?\d+u$/],
  ["dcs", /^\x1bP[ -~]*\x1b\\$/],
  [
    "osc-color",
    /^\x1b\](?:4|1\d);(?:\d+;)?(?:rgb:[0-9a-fA-F]+\/[0-9a-fA-F]+\/[0-9a-fA-F]+|#[0-9a-fA-F]{3,8})(?:\x07|\x1b\\)$/,
  ],
];

export function classifyTerminalResponse(data: string): TerminalResponseType | null {
  for (const [type, pattern] of PATTERNS) {
    if (pattern.test(data)) return type;
  }
  return null;
}

// The key under which a queued reply may be replaced by a newer one.
export function terminalResponseKey(type: TerminalResponseType, data: string): string {
  switch (type) {
    case "cpr":
      // CPR and DECXCPR report the current cursor position: latest wins.
      return data.startsWith("\x1b[?") ? "cpr?" : "cpr";
    case "window":
      return "window";
    case "mode": {
      // A mode's state report: latest wins per mode.
      const match = /^\x1b\[(\??\d+);/.exec(data);
      return `mode:${match?.[1] ?? ""}`;
    }
    case "kitty-keyboard":
      return "kitty-keyboard";
    case "osc-color": {
      // OSC 4;<index> per palette entry, OSC 10/11/12... per dynamic colour.
      const match = /^\x1b\](\d+);(?:(\d+);)?/.exec(data);
      return match?.[1] === "4" ? `osc:4:${match[2] ?? ""}` : `osc:${match?.[1] ?? ""}`;
    }
    default:
      // DA, DSR and DCS replies answer distinct questions: only identical
      // replies collapse.
      return `${type}:${data}`;
  }
}

export const RESPONSE_BURST_BYTES = 16 * 1024;
export const RESPONSE_REFILL_BYTES_PER_SECOND = 4 * 1024;
// Bytes of replies that may wait for tokens; a full 256-entry OSC 4 palette
// probe (about 7 KiB) plus fences such as DA1 fits several times over.
export const RESPONSE_MAX_QUEUED_BYTES = 32 * 1024;

export interface TerminalResponseStats {
  generation: number;
  // Replies written upstream and their bytes in this generation.
  sent: number;
  sentBytes: number;
  // Replies that waited for tokens before being written.
  deferred: number;
  // Queued replies replaced by a newer reply with the same key.
  coalesced: number;
  // Replies discarded: queue overflow, generation change or disposal.
  dropped: number;
  pending: number;
}

export interface TerminalResponseLimiterOptions {
  send: (data: string) => void;
  burstBytes?: number;
  refillBytesPerSecond?: number;
  maxQueuedBytes?: number;
  now?: () => number;
  // Diagnostics sink, called at most once per generation when replies were
  // deferred, coalesced or dropped.
  log?: (message: string, stats: TerminalResponseStats) => void;
}

export type TerminalResponseOutcome = "input" | "sent" | "queued" | "coalesced" | "dropped";

export interface TerminalResponseLimiter {
  submit: (data: string) => TerminalResponseOutcome;
  setGeneration: (generation: number) => void;
  stats: () => TerminalResponseStats;
  dispose: () => void;
}

const defaultLog = (message: string, stats: TerminalResponseStats) => console.warn(message, stats);

export function createTerminalResponseLimiter(options: TerminalResponseLimiterOptions): TerminalResponseLimiter {
  const burst = options.burstBytes ?? RESPONSE_BURST_BYTES;
  const rate = options.refillBytesPerSecond ?? RESPONSE_REFILL_BYTES_PER_SECOND;
  const maxQueuedBytes = options.maxQueuedBytes ?? RESPONSE_MAX_QUEUED_BYTES;
  // Read the clock on each call rather than capturing Date.now once, so a replaced clock applies.
  const now = options.now ?? (() => Date.now());
  const log = options.log ?? defaultLog;

  let tokens = burst;
  let refilledAt = now();
  // Insertion-ordered: a replaced reply moves to the tail with its new payload.
  let queue = new Map<string, string>();
  let queuedBytes = 0;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let disposed = false;
  let generation = 0;
  let logged = false;
  let counters = { sent: 0, sentBytes: 0, deferred: 0, coalesced: 0, dropped: 0 };

  const refill = () => {
    const at = now();
    if (at > refilledAt) {
      tokens = Math.min(burst, tokens + ((at - refilledAt) * rate) / 1000);
    }
    refilledAt = at;
  };

  const stats = (): TerminalResponseStats => ({ generation, ...counters, pending: queue.size });

  const report = () => {
    if (logged) return;
    if (counters.deferred === 0 && counters.coalesced === 0 && counters.dropped === 0) return;
    logged = true;
    log("[terminal] automatic query replies were rate-limited", stats());
  };

  // A reply larger than the whole burst would otherwise wait forever: it may
  // go once the bucket is full and leaves the balance negative.
  const affordable = (data: string) => tokens >= Math.min(data.length, burst);

  const write = (data: string) => {
    tokens -= data.length;
    counters.sent += 1;
    counters.sentBytes += data.length;
    options.send(data);
  };

  const clearTimer = () => {
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
  };

  const drain = () => {
    timer = null;
    if (disposed) return;
    refill();
    for (const [key, data] of queue) {
      if (!affordable(data)) break;
      queue.delete(key);
      queuedBytes -= data.length;
      write(data);
    }
    if (queue.size === 0) {
      // The deferral episode is over: its counts are complete enough to log.
      report();
      return;
    }
    const head = queue.values().next().value as string;
    const wait = Math.max(1, Math.ceil(((Math.min(head.length, burst) - tokens) * 1000) / rate));
    timer = setTimeout(drain, wait);
  };

  const submit = (data: string): TerminalResponseOutcome => {
    const type = classifyTerminalResponse(data);
    if (type === null) {
      options.send(data);
      return "input";
    }
    if (disposed) return "dropped";
    refill();
    if (queue.size === 0 && affordable(data)) {
      write(data);
      return "sent";
    }
    const key = terminalResponseKey(type, data);
    const replaced = queue.get(key);
    const nextBytes = queuedBytes - (replaced?.length ?? 0) + data.length;
    if (nextBytes > maxQueuedBytes) {
      counters.dropped += 1;
      return "dropped";
    }
    let outcome: TerminalResponseOutcome = "queued";
    if (replaced !== undefined) {
      queue.delete(key);
      counters.coalesced += 1;
      outcome = "coalesced";
    } else {
      counters.deferred += 1;
    }
    queue.set(key, data);
    queuedBytes = nextBytes;
    if (timer === null) drain();
    return outcome;
  };

  const discardQueue = () => {
    clearTimer();
    counters.dropped += queue.size;
    queue = new Map();
    queuedBytes = 0;
  };

  const setGeneration = (next: number) => {
    if (next === generation) return;
    // Replies answer queries from the previous attachment generation: they
    // must never reach the new one.
    discardQueue();
    report();
    generation = next;
    logged = false;
    counters = { sent: 0, sentBytes: 0, deferred: 0, coalesced: 0, dropped: 0 };
  };

  const dispose = () => {
    if (disposed) return;
    discardQueue();
    report();
    disposed = true;
  };

  return { submit, setGeneration, stats, dispose };
}
