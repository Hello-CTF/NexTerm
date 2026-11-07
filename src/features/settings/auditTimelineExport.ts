import type { AuditTimelineItem, AuditTimelinePage, AuditTimelineRequest } from "../../ipc/auditTimelineApi";
import { t } from "../../i18n";

export const TIMELINE_EXPORT_MAX = 10000;
const EXPORT_PAGE_SIZE = 500;

const HEADER = [t("时间"), t("来源"), t("主机"), t("类型"), t("命令/操作"), t("退出码"), t("耗时 ms"), t("会话"), t("详情 JSON")];

function cell(value: string): string {
  // Spreadsheet apps evaluate cells that start with these characters as formulas.
  const safe = /^[=+\-@\t\r]/.test(value) ? `'${value}` : value;
  return /[",\r\n]/.test(safe) ? `"${safe.replace(/"/g, '""')}"` : safe;
}

export function timelineToCsv(items: AuditTimelineItem[], hostName?: (assetId: string) => string | undefined): string {
  const lines = [HEADER.map(cell).join(",")];
  for (const it of items) {
    const host = it.assetId ? (hostName?.(it.assetId) ?? it.assetId) : "";
    const payload = it.payload === null || it.payload === undefined ? "" : JSON.stringify(it.payload);
    lines.push(
      [
        new Date(it.ts).toISOString(),
        it.origin,
        host,
        it.kind,
        it.command ?? "",
        it.exitCode === null ? "" : String(it.exitCode),
        it.durationMs === null ? "" : String(it.durationMs),
        it.sessionId ?? "",
        payload,
      ]
        .map(cell)
        .join(","),
    );
  }
  return `﻿${lines.join("\r\n")}\r\n`;
}

export function timelineToJson(items: AuditTimelineItem[]): string {
  return JSON.stringify({ exportedAt: new Date().toISOString(), count: items.length, items }, null, 2);
}

export async function collectTimelineExport(
  fetchPage: (request: AuditTimelineRequest) => Promise<AuditTimelinePage>,
  filters: Omit<AuditTimelineRequest, "limit" | "cursor">,
  max = TIMELINE_EXPORT_MAX,
): Promise<{ items: AuditTimelineItem[]; truncated: boolean }> {
  const items: AuditTimelineItem[] = [];
  let cursor: AuditTimelineRequest["cursor"] = null;
  for (;;) {
    const page: AuditTimelinePage = await fetchPage({ ...filters, limit: EXPORT_PAGE_SIZE, cursor });
    items.push(...page.items);
    cursor = page.nextCursor;
    if (!cursor) return { items, truncated: false };
    if (items.length >= max) return { items: items.slice(0, max), truncated: true };
  }
}
