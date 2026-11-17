import { WEB } from "../../ipc/env";

// The browser-mode backend runs elsewhere, so timestamps need the viewer's zone; the desktop backend's local zone is the viewer's.
export function viewerTimeZone(): { timeZone?: string; utcOffsetMinutes?: number } {
  if (!WEB) return {};
  let timeZone: string | undefined;
  try {
    timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone || undefined;
  } catch {
    timeZone = undefined;
  }
  return { timeZone, utcOffsetMinutes: -new Date().getTimezoneOffset() };
}

const pad = (value: number) => String(value).padStart(2, "0");

export function sanitizeFileName(name: string): string {
  return name.replace(/[\\/:*?"<>|]/g, "_");
}

// <host>-<YYYY-MM-DD-HHmmss>-<last 6 id chars>.<ext> in the viewer's local time, so every transcript gets its own name.
// The batch archive (transcript_export_archive) names its entries the same way.
export function exportFileName(summary: { id: string; assetName: string; startedAt: number }, extension: string): string {
  const started = new Date(summary.startedAt);
  const stamp = `${started.getFullYear()}-${pad(started.getMonth() + 1)}-${pad(started.getDate())}-` +
    `${pad(started.getHours())}${pad(started.getMinutes())}${pad(started.getSeconds())}`;
  return sanitizeFileName(`${summary.assetName || "transcript"}-${stamp}-${summary.id.slice(-6)}.${extension}`);
}

export function compactDate(date: Date): string {
  return `${date.getFullYear()}${pad(date.getMonth() + 1)}${pad(date.getDate())}`;
}

// isoDate is the value of an <input type="date"> for the local day of date.
export function isoDate(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

// localDayStart parses an <input type="date"> value as the start of that local day.
export function localDayStart(value: string): Date | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) return null;
  const date = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]));
  return Number.isNaN(date.getTime()) ? null : date;
}
