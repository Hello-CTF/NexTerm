export function reorderIndex(
  length: number,
  from: number,
  to: number,
): { from: number; to: number } | null {
  if (!Number.isInteger(from) || !Number.isInteger(to)) return null;
  if (from < 0 || from >= length) return null;
  const clamped = Math.min(Math.max(to, 0), Math.max(length - 1, 0));
  if (clamped === from) return null;
  return { from, to: clamped };
}

export function moveItem<T>(items: T[], from: number, to: number): T[] {
  const move = reorderIndex(items.length, from, to);
  if (!move) return items;
  const next = items.slice();
  const [item] = next.splice(move.from, 1);
  next.splice(move.to, 0, item);
  return next;
}
