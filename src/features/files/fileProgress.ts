export function progressPercent(event: { transferred: number; total: number }): number {
  if (event.total <= 0) return 0;
  return Math.min(100, Math.max(0, Math.round((event.transferred / event.total) * 100)));
}
