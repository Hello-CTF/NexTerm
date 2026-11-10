// A position to show once a file editor has the file loaded: the editor opened
// from a terminal path link (or already open for that file) moves its cursor
// there and scrolls it into view.

export interface FileRevealPosition {
  line: number;
  col?: number;
}

// A request that no editor picked up (the open failed or was cancelled) must not move a later, unrelated open.
const REVEAL_TTL_MS = 30_000;
const pending = new Map<string, { position: FileRevealPosition; at: number }>();
const listeners = new Set<() => void>();

function key(sessionId: string, path: string): string {
  return `${sessionId}\u0000${path}`;
}

export function requestFileReveal(sessionId: string, path: string, position: FileRevealPosition): void {
  pending.set(key(sessionId, path), { position, at: Date.now() });
  for (const listener of [...listeners]) listener();
}

/** Takes the pending position for this file, if any; each request is delivered once. */
export function takeFileReveal(sessionId: string, path: string): FileRevealPosition | null {
  const k = key(sessionId, path);
  const entry = pending.get(k);
  pending.delete(k);
  return entry && Date.now() - entry.at <= REVEAL_TTL_MS ? entry.position : null;
}

export function subscribeFileReveal(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** The document offset of a 1-based line and column, clamped to the document. */
export function revealOffset(
  doc: { lines: number; line(n: number): { from: number; length: number } },
  position: FileRevealPosition,
): number {
  const line = doc.line(Math.min(Math.max(1, Math.floor(position.line)), doc.lines));
  const col = Math.max(1, Math.floor(position.col ?? 1));
  return line.from + Math.min(col - 1, line.length);
}
