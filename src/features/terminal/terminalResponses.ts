// Coalescing for the terminal's automatic query responses (DA/DSR/CPR/window
// reports, DCS and OSC color replies). A hostile or broken host can emit
// thousands of queries per second; answering each with its own upstream write
// floods the channel. Responses are grouped by attachment generation and
// response type: the first few per type pass through immediately (synchronous
// request/reply pairs keep working), further ones inside a coalescing window
// collapse into a single write carrying the latest payload, and a per-type
// per-generation budget caps the total. Budget overruns are dropped and
// reported through onOverflow so suppressed responses stay visible instead of
// silently disappearing. User input never enters this path.

export type TerminalResponseType = "da" | "dsr" | "cpr" | "window" | "dcs" | "osc-color";

const PATTERNS: [TerminalResponseType, RegExp][] = [
  ["da", /^\x1b\[\?[\d;]*c$/],
  ["da", /^\x1b\[[>=][\d;]*c$/],
  ["da", /^\x1b\/Z$/],
  ["dsr", /^\x1b\[\d+n$/],
  ["cpr", /^\x1b\[\??\d+;\d+R$/],
  ["window", /^\x1b\[8;\d+;\d+t$/],
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

export const RESPONSE_DEFAULT_WINDOW_MS = 200;
export const RESPONSE_DEFAULT_IMMEDIATE = 4;
export const RESPONSE_DEFAULT_BUDGETS: Record<TerminalResponseType, number> = {
  da: 32,
  dsr: 32,
  cpr: 32,
  window: 16,
  dcs: 8,
  "osc-color": 32,
};

export interface TerminalResponseOverflow {
  type: TerminalResponseType;
  generation: number;
  dropped: number;
  budget: number;
}

export interface TerminalResponseStats {
  generation: number;
  pending: number;
  sent: number;
  coalesced: number;
  dropped: number;
}

export interface TerminalResponseCoalescerOptions {
  send: (data: string) => void;
  windowMs?: number;
  immediate?: number;
  budgets?: Partial<Record<TerminalResponseType, number>>;
  onOverflow?: (info: TerminalResponseOverflow) => void;
}

export interface TerminalResponseCoalescer {
  submit: (data: string) => "input" | "sent" | "coalesced" | "dropped";
  setGeneration: (generation: number) => void;
  flush: () => void;
  stats: () => TerminalResponseStats;
}

interface PendingEntry {
  data: string;
}

export function createTerminalResponseCoalescer(
  options: TerminalResponseCoalescerOptions,
): TerminalResponseCoalescer {
  const windowMs = options.windowMs ?? RESPONSE_DEFAULT_WINDOW_MS;
  const immediate = options.immediate ?? RESPONSE_DEFAULT_IMMEDIATE;
  const budgets = { ...RESPONSE_DEFAULT_BUDGETS, ...options.budgets };
  let generation = 0;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let pending = new Map<TerminalResponseType, PendingEntry>();
  let sentByType = new Map<TerminalResponseType, number>();
  let droppedByType = new Map<TerminalResponseType, number>();
  let sent = 0;
  let coalesced = 0;
  let dropped = 0;

  const flush = () => {
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
    if (pending.size === 0) return;
    const entries = pending;
    pending = new Map();
    for (const [type, entry] of entries) {
      options.send(entry.data);
      sentByType.set(type, (sentByType.get(type) ?? 0) + 1);
      sent += 1;
    }
  };

  const submit = (data: string): "input" | "sent" | "coalesced" | "dropped" => {
    const type = classifyTerminalResponse(data);
    if (type === null) {
      options.send(data);
      return "input";
    }
    const budget = budgets[type];
    const alreadySent = sentByType.get(type) ?? 0;
    const entry = pending.get(type);
    if (entry) {
      entry.data = data;
      coalesced += 1;
      return "coalesced";
    }
    const outstanding = alreadySent + 1;
    if (outstanding > budget) {
      const count = (droppedByType.get(type) ?? 0) + 1;
      droppedByType.set(type, count);
      dropped += 1;
      options.onOverflow?.({ type, generation, dropped: count, budget });
      return "dropped";
    }
    if (alreadySent < immediate) {
      options.send(data);
      sentByType.set(type, alreadySent + 1);
      sent += 1;
      return "sent";
    }
    pending.set(type, { data });
    coalesced += 1;
    if (timer === null) {
      timer = setTimeout(() => {
        timer = null;
        flush();
      }, windowMs);
    }
    return "coalesced";
  };

  const setGeneration = (next: number) => {
    if (next === generation) return;
    // Responses already coalesced belong to queries the still-running shell
    // is waiting on: deliver them before the budget window moves on.
    flush();
    generation = next;
    sentByType = new Map();
    droppedByType = new Map();
    sent = 0;
    coalesced = 0;
    dropped = 0;
  };

  const stats = (): TerminalResponseStats => ({
    generation,
    pending: pending.size,
    sent,
    coalesced,
    dropped,
  });

  return { submit, setGeneration, flush, stats };
}
