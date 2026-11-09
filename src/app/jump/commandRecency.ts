// Command use for the jump palette's ranking, kept per device: commands differ between machines and the
// order is a convenience, so nothing syncs and a storage failure only loses the ordering.
const STORAGE_KEY = "nexterm.commandRecency.v1";
const HALF_LIFE_MS = 7 * 24 * 60 * 60 * 1000;
const MAX_ENTRIES = 200;

interface CommandUse {
  count: number;
  at: number;
}

type CommandUses = Record<string, CommandUse>;

function isUse(value: unknown): value is CommandUse {
  if (!value || typeof value !== "object") return false;
  const { count, at } = value as Record<string, unknown>;
  return typeof count === "number" && Number.isFinite(count) && count > 0 && typeof at === "number" && Number.isFinite(at);
}

function readUses(): CommandUses {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const uses: CommandUses = {};
    for (const [id, use] of Object.entries(parsed as Record<string, unknown>)) {
      if (isUse(use)) uses[id] = { count: use.count, at: use.at };
    }
    return uses;
  } catch {
    return {};
  }
}

// recordCommandUse notes that a palette command ran; the oldest entries are dropped beyond a fixed limit.
export function recordCommandUse(id: string, now = Date.now()): void {
  const uses = readUses();
  const previous = uses[id];
  uses[id] = { count: (previous?.count ?? 0) + 1, at: now };
  const kept = Object.entries(uses)
    .sort((a, b) => b[1].at - a[1].at)
    .slice(0, MAX_ENTRIES);
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(Object.fromEntries(kept)));
  } catch {
    // Remembering the order is a convenience; failing to store it is harmless.
  }
}

// commandRecencyScores reads the stored uses once and scores commands by frecency: recent use decays by half each
// week and repeated use adds a little, capped. Unused commands score 0.
export function commandRecencyScores(now = Date.now()): (id: string) => number {
  const uses = readUses();
  return (id) => {
    const use = uses[id];
    if (!use) return 0;
    const age = Math.max(0, now - use.at);
    return Math.pow(0.5, age / HALF_LIFE_MS) + Math.min(use.count, 10) * 0.05;
  };
}
