import { describe, expect, it, vi } from "vitest";
import type { AuditTimelineItem, AuditTimelinePage } from "../ipc/auditTimelineApi";
import { collectTimelineExport, timelineToCsv, timelineToJson } from "../features/settings/auditTimelineExport";

function item(over: Partial<AuditTimelineItem> = {}): AuditTimelineItem {
  return {
    stream: "command",
    id: 1,
    ts: Date.UTC(2026, 0, 2, 3, 4, 5),
    origin: "user",
    source: "user",
    kind: "command",
    assetId: "a-1",
    sessionId: "s-1",
    command: "ls",
    payload: null,
    exitCode: 0,
    durationMs: 12,
    ...over,
  };
}

function parseCsv(csv: string): string[][] {
  const text = csv.replace(/^﻿/, "");
  const rows: string[][] = [];
  let row: string[] = [];
  let cell = "";
  let quoted = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (quoted) {
      if (ch === '"' && text[i + 1] === '"') {
        cell += '"';
        i++;
      } else if (ch === '"') quoted = false;
      else cell += ch;
    } else if (ch === '"') quoted = true;
    else if (ch === ",") {
      row.push(cell);
      cell = "";
    } else if (ch === "\r" && text[i + 1] === "\n") {
      row.push(cell);
      rows.push(row);
      row = [];
      cell = "";
      i++;
    } else cell += ch;
  }
  return rows;
}

describe("timelineToCsv", () => {
  it("starts with a BOM and the fixed header, rows end with CRLF", () => {
    const csv = timelineToCsv([item()], (id) => (id === "a-1" ? "web-01" : undefined));
    expect(csv.startsWith("﻿")).toBe(true);
    expect(csv.endsWith("\r\n")).toBe(true);
    const rows = parseCsv(csv);
    expect(rows[0]).toEqual(["时间", "来源", "主机", "类型", "命令/操作", "退出码", "耗时 ms", "会话", "详情 JSON"]);
    expect(rows[1][0]).toBe("2026-01-02T03:04:05.000Z");
    expect(rows[1][2]).toBe("web-01");
    expect(rows[1][4]).toBe("ls");
    expect(rows[1][5]).toBe("0");
    expect(rows[1][6]).toBe("12");
  });

  it("escapes commas, quotes and newlines per RFC 4180", () => {
    const csv = timelineToCsv([item({ command: 'echo "a,b"\nnext' })]);
    expect(csv).toContain('"echo ""a,b""\nnext"');
    expect(parseCsv(csv)[1][4]).toBe('echo "a,b"\nnext');
  });

  it("prefixes formula-looking cells with a single quote", () => {
    const risky = ["=cmd|' /C calc'!A0", "+1", "-2+3", "@SUM(1)", "\t=1", "\r=1"];
    for (const command of risky) {
      const cell = parseCsv(timelineToCsv([item({ command })]))[1][4];
      expect(cell).toBe(`'${command}`);
    }
    for (const command of ["ls -la", "a=b", "echo hi"]) {
      expect(parseCsv(timelineToCsv([item({ command })]))[1][4]).toBe(command);
    }
  });

  it("also prefixes negative exit codes (accepted trade-off)", () => {
    expect(parseCsv(timelineToCsv([item({ exitCode: -1 })]))[1][5]).toBe("'-1");
  });
});

describe("timelineToJson", () => {
  it("round-trips items unchanged with count and exportedAt", () => {
    const items = [item(), item({ stream: "device", id: 2, payload: { a: 1 }, exitCode: null, durationMs: null })];
    const parsed = JSON.parse(timelineToJson(items));
    expect(parsed.items).toEqual(items);
    expect(parsed.count).toBe(2);
    expect(typeof parsed.exportedAt).toBe("string");
  });
});

describe("collectTimelineExport", () => {
  const page = (start: number, n: number, next: boolean): AuditTimelinePage => ({
    items: Array.from({ length: n }, (_, i) => item({ id: start + i })),
    nextCursor: next ? { ts: start, stream: "command", id: start + n } : null,
  });

  it("follows cursors until exhausted and reports not truncated", async () => {
    const fetchPage = vi
      .fn<(r: { cursor?: unknown; limit?: number }) => Promise<AuditTimelinePage>>()
      .mockResolvedValueOnce(page(1, 500, true))
      .mockResolvedValueOnce(page(501, 10, false));
    const out = await collectTimelineExport(fetchPage, { origins: ["ai"] });
    expect(out.items).toHaveLength(510);
    expect(out.truncated).toBe(false);
    expect(fetchPage).toHaveBeenCalledTimes(2);
    expect(fetchPage.mock.calls[0][0]).toMatchObject({ origins: ["ai"], limit: 500 });
    expect(fetchPage.mock.calls[1][0].cursor).toEqual({ ts: 1, stream: "command", id: 501 });
  });

  it("stops at the cap and marks truncated without fetching more", async () => {
    const fetchPage = vi.fn(async (r: { cursor?: { id: number } | null }) => page((r.cursor?.id ?? 1), 500, true));
    const out = await collectTimelineExport(fetchPage, {}, 1000);
    expect(out.items).toHaveLength(1000);
    expect(out.truncated).toBe(true);
    expect(fetchPage).toHaveBeenCalledTimes(2);
  });
});
