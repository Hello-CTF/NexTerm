import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { assetApi, type Asset } from "../../ipc/commands";
import { fleetApi, type FleetDevice } from "../../ipc/fleetApi";
import {
  auditTimelineApi,
  type AuditTimelineCursor,
  type AuditTimelineItem,
  type AuditTimelineOrigin,
  type AuditTimelineRequest,
} from "../../ipc/auditTimelineApi";
import { logTailApi } from "../../ipc/logTailApi";
import { WEB } from "../../ipc/env";
import { saveBrowserBlob } from "../../ipc/webFiles";
import { useUi } from "../../app/store";
import { pickSavePath } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { Select } from "../../ui/Select";
import { IconDownload, IconHistory, IconRefresh } from "../../ui/icons";
import { useAuth } from "../auth/store";
import { KIND_LABELS } from "./auditLabels";
import { collectTimelineExport, timelineToCsv, timelineToJson, TIMELINE_EXPORT_MAX } from "./auditTimelineExport";
import { useCompactWidth } from "./useCompactWidth";

const PAGE_SIZE = 100;
const DAY_MS = 24 * 60 * 60 * 1000;

type OriginFilter = "" | AuditTimelineOrigin;
type RangePreset = "all" | "24h" | "7d" | "30d" | "custom";

const ORIGIN_FILTERS: { value: OriginFilter; label: string }[] = [
  { value: "", label: "全部" },
  { value: "user", label: "用户" },
  { value: "ai", label: "AI" },
  { value: "device", label: "设备" },
];

const RANGE_PRESETS: { value: RangePreset; label: string }[] = [
  { value: "all", label: "全部时间" },
  { value: "24h", label: "24 小时" },
  { value: "7d", label: "7 天" },
  { value: "30d", label: "30 天" },
];

function originLabel(origin: string): string {
  if (origin === "ai") return "AI";
  if (origin === "device") return "设备";
  return "用户";
}

function originBadge(origin: string): string {
  return origin === "ai" ? "nx-badge-purple" : origin === "device" ? "nx-badge-green" : "nx-badge-blue";
}

function dayStart(value: string): number | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!m) return null;
  return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3])).getTime();
}

interface Filters {
  assetId: string;
  origin: OriginFilter;
  range: RangePreset;
  fromDate: string;
  toDate: string;
}

function toRequest(f: Filters): Omit<AuditTimelineRequest, "limit" | "cursor"> {
  const req: Omit<AuditTimelineRequest, "limit" | "cursor"> = {};
  if (f.assetId) req.assetId = f.assetId;
  if (f.origin) req.origins = [f.origin];
  const now = Date.now();
  if (f.range === "24h") req.from = now - DAY_MS;
  else if (f.range === "7d") req.from = now - 7 * DAY_MS;
  else if (f.range === "30d") req.from = now - 30 * DAY_MS;
  else if (f.range === "custom") {
    const from = dayStart(f.fromDate);
    const end = dayStart(f.toDate);
    if (from !== null) req.from = from;
    // The end day is inclusive, so the exclusive bound is the next local midnight.
    if (end !== null) {
      const next = new Date(end);
      next.setDate(next.getDate() + 1);
      req.to = next.getTime();
    }
  }
  return req;
}

function payloadText(payload: unknown): string {
  return payload === null || payload === undefined ? "" : (JSON.stringify(payload) ?? "");
}

function mergeItems(prev: AuditTimelineItem[], rows: AuditTimelineItem[]): AuditTimelineItem[] {
  const seen = new Set(prev.map((e) => `${e.stream}:${e.id}`));
  const added = rows.filter((r) => {
    const key = `${r.stream}:${r.id}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
  return [...prev, ...added];
}

function timestampForFile(): string {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`;
}

export function AuditTimelineView({ onShowAudit, onShowCommand }: { onShowAudit: () => void; onShowCommand: () => void }) {
  const [bodyRef, compact] = useCompactWidth(640);
  const user = useAuth((s) => s.user);
  const pushToast = useUi((s) => s.pushToast);
  const [filters, setFilters] = useState<Filters>({ assetId: "", origin: "", range: "all", fromDate: "", toDate: "" });
  const [assets, setAssets] = useState<Asset[] | null>(null);
  const [devices, setDevices] = useState<FleetDevice[] | null>(null);
  const [items, setItems] = useState<AuditTimelineItem[]>([]);
  const [cursor, setCursor] = useState<AuditTimelineCursor | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [moreError, setMoreError] = useState<string | null>(null);
  const [exporting, setExporting] = useState(false);
  const [exportNote, setExportNote] = useState<{ tone: "ok" | "warn" | "error"; text: string } | null>(null);
  const loadSeq = useRef(0);
  // The request of the loaded first page; relative presets resolve Date.now() once so paging keeps one time window.
  const pageRequest = useRef<Omit<AuditTimelineRequest, "limit" | "cursor">>({});

  useEffect(() => {
    let cancelled = false;
    assetApi
      .list()
      .then((list) => {
        if (!cancelled) setAssets(list);
      })
      .catch(() => {
        if (!cancelled) setAssets(null);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // Devices are only requested for a logged-in WEB account; a 401 would raise the global session-expired event.
  useEffect(() => {
    if (!WEB || !user) return;
    let cancelled = false;
    fleetApi
      .devices()
      .then((r) => {
        if (!cancelled) setDevices(r.devices);
      })
      .catch(() => {
        if (!cancelled) setDevices(null);
      });
    return () => {
      cancelled = true;
    };
  }, [user]);

  const hostName = useCallback(
    (id: string): string | undefined =>
      devices?.find((d) => d.id === id)?.name ?? assets?.find((a) => a.id === id)?.name,
    [assets, devices],
  );

  const loadFirstPage = useCallback(async (f: Filters) => {
    const seq = ++loadSeq.current;
    setItems([]);
    setCursor(null);
    setError(null);
    setMoreError(null);
    setLoadingMore(false);
    setLoading(true);
    const req = toRequest(f);
    pageRequest.current = req;
    try {
      const page = await auditTimelineApi.page({ ...req, limit: PAGE_SIZE });
      if (seq !== loadSeq.current) return;
      setItems(mergeItems([], page.items));
      setCursor(page.nextCursor);
    } catch (e) {
      if (seq === loadSeq.current) setError(describeError(e));
    } finally {
      if (seq === loadSeq.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadFirstPage(filters);
  }, [filters, loadFirstPage]);

  const loadMore = async () => {
    if (loading || loadingMore || !cursor) return;
    const seq = loadSeq.current;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await auditTimelineApi.page({ ...pageRequest.current, limit: PAGE_SIZE, cursor });
      if (seq !== loadSeq.current) return;
      setItems((prev) => mergeItems(prev, page.items));
      setCursor(page.nextCursor);
    } catch (e) {
      if (seq === loadSeq.current) setMoreError(describeError(e));
    } finally {
      if (seq === loadSeq.current) setLoadingMore(false);
    }
  };

  const exportTimeline = async (format: "csv" | "json") => {
    if (exporting) return;
    setExporting(true);
    setExportNote(null);
    try {
      // Export resolves the current filters afresh instead of reusing the paged window.
      const { items: all, truncated } = await collectTimelineExport(auditTimelineApi.page, toRequest(filters), TIMELINE_EXPORT_MAX);
      const name = `nexterm-audit-timeline-${timestampForFile()}.${format}`;
      const text = format === "csv" ? timelineToCsv(all, hostName) : `${timelineToJson(all)}\n`;
      const mime = format === "csv" ? "text/csv;charset=utf-8" : "application/json";
      let saved = true;
      if (WEB) {
        saved = await saveBrowserBlob(name, new Blob([text], { type: mime }));
      } else {
        const path = await pickSavePath(name);
        if (!path) saved = false;
        else await logTailApi.save(path, text);
      }
      if (!saved) return;
      if (truncated) {
        setExportNote({ tone: "warn", text: `已导出 ${all.length} 条。仅导出最新 ${TIMELINE_EXPORT_MAX} 条，请缩小时间范围或筛选条件` });
      } else {
        setExportNote({ tone: "ok", text: `已导出 ${all.length} 条` });
        pushToast("success", `已导出 ${all.length} 条时间线记录`);
      }
    } catch (e) {
      setExportNote({ tone: "error", text: `导出失败：${describeError(e)}` });
    } finally {
      setExporting(false);
    }
  };

  const hostOptions = useMemo(
    () => [
      { value: "", label: "全部主机" },
      ...(assets ?? []).map((a) => ({ value: a.id, label: a.name })),
      ...(devices ?? []).map((d) => ({ value: d.id, label: d.name })),
    ],
    [assets, devices],
  );

  const patch = (p: Partial<Filters>) => setFilters((f) => ({ ...f, ...p }));
  const hostLabel = (e: AuditTimelineItem) => (e.assetId ? (hostName(e.assetId) ?? e.assetId) : "—");
  const action = (e: AuditTimelineItem) => (e.stream === "command" ? (e.command ?? "") : (KIND_LABELS[e.kind] ?? e.kind));

  const exitCell = (e: AuditTimelineItem) =>
    e.exitCode === null ? (
      <span className="text-neutral-500">—</span>
    ) : e.exitCode === 0 ? (
      <span className="text-green-300" title="成功">✓ 0</span>
    ) : (
      <span className="text-red-300" title="失败">✗ {e.exitCode}</span>
    );

  const scopeText = !WEB ? "显示本机全部记录" : user ? "只显示你账户下的记录" : "显示本服务器的全部记录";

  const retry = (
    <button className="nx-btn nx-btn-ghost nx-btn-sm ml-2" onClick={() => void loadFirstPage(filters)} disabled={loading}>
      <IconRefresh size={12} />
      重试
    </button>
  );
  const emptyText = "暂无记录。连接主机、执行命令或运行 AI 操作后，记录将显示在此处。";

  return (
    <div className="nx-pane">
      <div className="nx-toolbar flex-wrap">
        <IconHistory size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">审计时间线</span>
        <div className="nx-segment">
          <button className="nx-segment-item" onClick={onShowAudit}>审计</button>
          <button className="nx-segment-item" onClick={onShowCommand}>命令</button>
          <button className="nx-segment-item is-active" disabled>时间线</button>
        </div>
        <span className="nx-hint">
          {error && items.length === 0 ? "未能加载时间线" : `已加载 ${items.length} 条`}
        </span>
        <div className="nx-spacer" />
        <Select
          className="w-[140px] shrink-0"
          ariaLabel="按主机过滤"
          value={filters.assetId}
          onChange={(v) => patch({ assetId: v })}
          options={hostOptions}
        />
        <div
          className="nx-segment"
          role="group"
          aria-label="来源过滤"
          title="命令日志没有 AI 来源标记，AI 发起的终端命令显示为用户"
        >
          {ORIGIN_FILTERS.map((f) => (
            <button
              key={f.value}
              className={`nx-segment-item ${filters.origin === f.value ? "is-active" : ""}`}
              onClick={() => patch({ origin: f.value })}
            >
              {f.label}
            </button>
          ))}
        </div>
        <div className="nx-segment" role="group" aria-label="时间范围">
          {RANGE_PRESETS.map((r) => (
            <button
              key={r.value}
              className={`nx-segment-item ${filters.range === r.value ? "is-active" : ""}`}
              onClick={() => patch({ range: r.value })}
            >
              {r.label}
            </button>
          ))}
        </div>
        <input
          type="date"
          className="nx-input w-[130px]"
          aria-label="开始日期"
          value={filters.fromDate}
          onChange={(e) => patch({ fromDate: e.target.value, range: "custom" })}
        />
        <input
          type="date"
          className="nx-input w-[130px]"
          aria-label="结束日期"
          value={filters.toDate}
          onChange={(e) => patch({ toDate: e.target.value, range: "custom" })}
        />
        <button className="nx-btn nx-btn-ghost" onClick={() => void loadFirstPage(filters)} disabled={loading}>
          <IconRefresh size={13} className={loading ? "animate-spin" : ""} />
          刷新
        </button>
        <button className="nx-btn nx-btn-ghost" onClick={() => void exportTimeline("csv")} disabled={exporting}>
          <IconDownload size={13} />
          导出 CSV
        </button>
        <button className="nx-btn nx-btn-ghost" onClick={() => void exportTimeline("json")} disabled={exporting}>
          <IconDownload size={13} />
          导出 JSON
        </button>
        <span className="nx-hint min-w-0 break-words">导出文件可能包含命令行中的敏感内容</span>
      </div>

      <div ref={bodyRef} className="min-h-0 flex-1 overflow-auto">
        {compact ? (
          <ul className="m-0 flex list-none flex-col p-0" aria-label="审计时间线" data-testid="timeline-cards">
            {items.map((e) => (
              <li key={`${e.stream}:${e.id}`} className="flex flex-col gap-1 border-b border-neutral-800/60 px-3 py-2.5">
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <span className={`nx-badge ${originBadge(e.origin)}`}>{originLabel(e.origin)}</span>
                  <span className="min-w-0 flex-1 break-words text-[12.5px] font-medium text-neutral-200">{action(e)}</span>
                  {e.exitCode !== null && <span className="text-[12px]">{exitCell(e)}</span>}
                  {e.durationMs !== null && <span className="nx-mono text-[11px] text-neutral-500">{e.durationMs}ms</span>}
                </div>
                <div className="nx-mono text-[11px] text-neutral-500">
                  {new Date(e.ts).toLocaleString()} · {hostLabel(e)}
                </div>
                <div className="nx-mono line-clamp-3 break-all text-[11.5px] text-neutral-400">{payloadText(e.payload)}</div>
              </li>
            ))}
            {items.length === 0 && loading && <li className="nx-hint px-3 py-4 text-center">正在加载…</li>}
            {items.length === 0 && !loading && error && (
              <li className="px-3 py-4 text-center text-[12px]">
                <span className="break-words text-red-300">未能加载时间线：{error}</span>
                {retry}
              </li>
            )}
            {items.length === 0 && !loading && !error && <li className="nx-hint px-3 py-4 text-center">{emptyText}</li>}
          </ul>
        ) : (
          <table className="nx-table nx-table-fixed">
            <thead>
              <tr>
                <th style={{ width: 168 }}>时间</th>
                <th style={{ width: 72 }}>来源</th>
                <th style={{ width: 140 }}>主机</th>
                <th style={{ width: 200 }}>操作</th>
                <th>详情</th>
                <th style={{ width: 84 }} className="nx-right">退出码</th>
                <th style={{ width: 84 }} className="nx-right">耗时</th>
              </tr>
            </thead>
            <tbody>
              {items.map((e) => (
                <tr key={`${e.stream}:${e.id}`}>
                  <td className="nx-mono whitespace-nowrap">{new Date(e.ts).toLocaleString()}</td>
                  <td>
                    <span className={`nx-badge ${originBadge(e.origin)}`}>{originLabel(e.origin)}</span>
                  </td>
                  <td className="truncate" title={e.assetId ?? undefined}>{hostLabel(e)}</td>
                  <td className="truncate font-mono text-[11.5px] text-neutral-200" title={action(e)}>{action(e)}</td>
                  <td className="nx-mono max-w-[520px] truncate" title={payloadText(e.payload)}>{payloadText(e.payload)}</td>
                  <td className="nx-right">{exitCell(e)}</td>
                  <td className="nx-right nx-mono">
                    {e.durationMs === null ? <span className="text-neutral-500">—</span> : `${e.durationMs}ms`}
                  </td>
                </tr>
              ))}
              {items.length === 0 && loading ? (
                <tr>
                  <td colSpan={7} className="nx-table-empty">正在加载…</td>
                </tr>
              ) : items.length === 0 && error ? (
                <tr>
                  <td colSpan={7} className="nx-table-empty">
                    <span className="text-red-300">未能加载时间线：{error}</span>
                    {retry}
                  </td>
                </tr>
              ) : items.length === 0 ? (
                <tr>
                  <td colSpan={7} className="nx-table-empty">{emptyText}</td>
                </tr>
              ) : null}
            </tbody>
          </table>
        )}
      </div>

      {items.length > 0 && (
        <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-t border-neutral-800/60 bg-neutral-950/40 px-3 py-1.5 text-[11px] text-neutral-500">
          <span className="nx-hint min-w-0 truncate">已显示 {items.length} 条</span>
          <div className="nx-spacer" />
          {moreError ? (
            <>
              <span className="min-w-0 break-words text-red-300">未能加载更多：{moreError}</span>
              <button className="nx-btn nx-btn-ghost nx-btn-xs" onClick={() => void loadMore()} disabled={loadingMore}>
                重试
              </button>
            </>
          ) : cursor ? (
            <button className="nx-btn nx-btn-ghost nx-btn-xs" onClick={() => void loadMore()} disabled={loadingMore}>
              {loadingMore ? <IconRefresh size={11} className="animate-spin" /> : null}
              加载更多
            </button>
          ) : (
            <span>已加载全部</span>
          )}
        </div>
      )}

      {exportNote && (
        <div
          role="status"
          className={`shrink-0 border-t border-neutral-800/60 px-3 py-1.5 text-[11.5px] ${
            exportNote.tone === "error" ? "text-red-300" : exportNote.tone === "warn" ? "text-amber-300" : "text-green-300"
          }`}
        >
          {exportNote.text}
        </div>
      )}

      <div className="flex shrink-0 items-center gap-3 border-t border-neutral-800/60 bg-neutral-950/40 px-3 py-1.5 text-[11px] text-neutral-500">
        <span className="min-w-0 break-words">
          时间线合并审计、命令和设备上下线记录，{scopeText}。命令日志没有 AI 来源标记，AI 发起的终端命令显示为用户。
        </span>
      </div>
    </div>
  );
}
