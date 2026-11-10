export function progressPercent(event: { transferred: number; total: number }): number {
  if (event.total <= 0) return 0;
  return Math.min(100, Math.max(0, Math.round((event.transferred / event.total) * 100)));
}

// formatEta renders a remaining time in seconds as m:ss, or h:mm:ss from one hour.
export function formatEta(seconds: number): string {
  const total = Math.max(0, Math.round(seconds));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = String(total % 60).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
}
