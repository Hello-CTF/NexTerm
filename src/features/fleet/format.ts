// 设备管理视图的展示格式化助手: 字节/时长/相对时间。

export { formatTime, formatBytes } from "../../ui/format";

export function formatUptime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "—";
  const s = Math.floor(seconds % 60);
  const m = Math.floor((seconds / 60) % 60);
  const h = Math.floor((seconds / 3600) % 24);
  const d = Math.floor(seconds / 86400);
  if (d > 0) return `${d} 天 ${h} 小时`;
  if (h > 0) return `${h} 小时 ${m} 分`;
  if (m > 0) return `${m} 分 ${s} 秒`;
  return `${s} 秒`;
}

export function relativeTime(ms: number, now: number): string {
  if (!ms) return "从未";
  const delta = Math.max(0, now - ms);
  if (delta < 10_000) return "刚刚";
  if (delta < 60_000) return `${Math.floor(delta / 1000)} 秒前`;
  if (delta < 3_600_000) return `${Math.floor(delta / 60_000)} 分钟前`;
  if (delta < 86_400_000) return `${Math.floor(delta / 3_600_000)} 小时前`;
  return `${Math.floor(delta / 86_400_000)} 天前`;
}

// 心跳阈值: agent 默认 60s 上报一次指标, 3 分钟内有心跳即视为在线。
export const ONLINE_THRESHOLD_MS = 180_000;

export function isOnline(lastSeenAt: number, now: number): boolean {
  return lastSeenAt > 0 && now - lastSeenAt <= ONLINE_THRESHOLD_MS;
}
