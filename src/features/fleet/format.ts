import { t } from "../../i18n";
// Display formatting helpers for the device management view: bytes/duration/relative time.

export { formatTime, formatBytes } from "../../ui/format";

export function formatUptime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "—";
  const s = Math.floor(seconds % 60);
  const m = Math.floor((seconds / 60) % 60);
  const h = Math.floor((seconds / 3600) % 24);
  const d = Math.floor(seconds / 86400);
  if (d > 0) return t("{d} 天 {h} 小时", { d, h });
  if (h > 0) return t("{h} 小时 {m} 分", { h, m });
  if (m > 0) return t("{m} 分 {s} 秒", { m, s });
  return t("{s} 秒", { s });
}

export function relativeTime(ms: number, now: number): string {
  if (!ms) return t("从未");
  const delta = Math.max(0, now - ms);
  if (delta < 10_000) return t("刚刚");
  if (delta < 60_000) return t("{floor} 秒前", { floor: Math.floor(delta / 1000) });
  if (delta < 3_600_000) return t("{floor} 分钟前", { floor: Math.floor(delta / 60_000) });
  if (delta < 86_400_000) return t("{floor} 小时前", { floor: Math.floor(delta / 3_600_000) });
  return t("{floor} 天前", { floor: Math.floor(delta / 86_400_000) });
}

// Heartbeat threshold: the agent reports metrics every 60s by default; a heartbeat within 3 minutes counts as online.
export const ONLINE_THRESHOLD_MS = 180_000;

export function isOnline(lastSeenAt: number, now: number): boolean {
  return lastSeenAt > 0 && now - lastSeenAt <= ONLINE_THRESHOLD_MS;
}
