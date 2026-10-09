import { useCallback, useEffect, useRef, useState } from "react";
import { assetApi } from "../../ipc/commands";
import type { AuditEntryDto } from "../../ipc/types";
import { fleetApi, type FleetDevice } from "../../ipc/fleetApi";
import { useAuth } from "../auth/store";
import { WEB } from "../../ipc/env";
import { describeError } from "../../ui/errorText";
import { IconHistory, IconRefresh } from "../../ui/icons";
import { CommandLogView } from "./CommandLogView";

interface AuditEntry {
  id: number;
  ts: number;
  sessionId: string | null;
  assetId: string | null;
  source: string;
  kind: string;
  payload: unknown;
  exitCode: number | null;
  durationMs: number | null;
}

type SourceFilter = "" | "user" | "ai" | "fleet";

const FILTERS: { value: SourceFilter; label: string }[] = [
  { value: "", label: "全部" },
  { value: "user", label: "用户" },
  { value: "ai", label: "AI" },
  { value: "fleet", label: "设备" },
];

// fleet 设备审计 kind 的中文标签 (audit_log.asset_id 落的是设备 ID)。
const KIND_LABELS: Record<string, string> = {
  device_enroll: "设备接入",
  device_online: "设备上线",
  device_offline: "设备离线",
  device_revoke: "设备吊销",
  device_autostart: "设备自启动",
  device_failover: "设备切换接入",
  device_terminal: "设备终端",
};

function sourceLabel(source: string): string {
  if (source === "ai") return "AI";
  if (source === "fleet") return "设备";
  return "用户";
}

const PAGE_SIZE = 100;
const SKELETON_ROWS = 8;

function toEntry(r: AuditEntryDto): AuditEntry {
  return {
    id: r.id,
    ts: r.ts,
    sessionId: r.sessionId,
    assetId: r.assetId,
    source: r.source,
    kind: r.kind,
    payload: r.payload,
    exitCode: r.exitCode,
    durationMs: r.durationMs,
  };
}

function mergeEntries(prev: AuditEntry[], rows: AuditEntryDto[]): AuditEntry[] {
  if (prev.length === 0) return rows.map(toEntry);
  const seen = new Set(prev.map((e) => e.id));
  const added: AuditEntry[] = [];
  for (const r of rows) {
    if (seen.has(r.id)) continue;
    seen.add(r.id);
    added.push(toEntry(r));
  }
  return [...prev, ...added];
}

function AuditSkeletonRows() {
  return (
    <>
      {Array.from({ length: SKELETON_ROWS }, (_, i) => (
        <tr key={i} aria-hidden="true">
          <td>
            <span className="nx-skeleton w-4/5" />
          </td>
          <td>
            <span className="nx-skeleton nx-skeleton-chip w-3/5" />
          </td>
          <td>
            <span className="nx-skeleton w-3/5" />
          </td>
          <td>
            <span className="nx-skeleton w-3/4" />
          </td>
          <td>
            <span className="nx-skeleton ml-auto w-1/3" />
          </td>
          <td>
            <span className="nx-skeleton ml-auto w-1/2" />
          </td>
        </tr>
      ))}
    </>
  );
}

export function AuditView() {
  const user = useAuth((s) => s.user);
  const [view, setView] = useState<"audit" | "command">("audit");
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [source, setSource] = useState<SourceFilter>("");
  const [devices, setDevices] = useState<FleetDevice[] | null>(null);
  const [deviceId, setDeviceId] = useState("");
  const [total, setTotal] = useState<number | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [moreError, setMoreError] = useState<string | null>(null);
  const [lastPageFull, setLastPageFull] = useState(false);
  const loadSeq = useRef(0);
  const fetchedRef = useRef(0);

  // 设备清单只用于"按设备过滤"下拉 (WEB 账号模式); 桌面端/未登录
  // 一律不请求, 过滤入口整体隐藏。401 会触发全局会话过期事件, 未登录绝不能发。
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

  // 选中设备时按 assetId 查该设备的完整时间线 (来源筛选让位);
  // 未选中时维持按来源筛选。
  const queryArgs = useCallback(
    (src: SourceFilter, device: string): Record<string, unknown> =>
      device ? { assetId: device } : { source: src || undefined },
    [],
  );

  const loadFirstPage = useCallback(async (src: SourceFilter, device: string) => {
    const seq = ++loadSeq.current;
    fetchedRef.current = 0;
    setEntries([]);
    setTotal(null);
    setError(null);
    setMoreError(null);
    setLastPageFull(false);
    setLoadingMore(false);
    setLoading(true);
    void (async () => {
      try {
        const count = await assetApi.auditCount(queryArgs(src, device));
        if (seq === loadSeq.current) setTotal(count.total);
      } catch {
        if (seq === loadSeq.current) setTotal(null);
      }
    })();
    try {
      const rows = await assetApi.auditQuery({ ...queryArgs(src, device), limit: PAGE_SIZE, offset: 0 });
      if (seq !== loadSeq.current) return;
      fetchedRef.current = rows.length;
      setLastPageFull(rows.length === PAGE_SIZE);
      setEntries(mergeEntries([], rows));
      setError(null);
    } catch (e) {
      if (seq === loadSeq.current) setError(describeError(e));
    } finally {
      if (seq === loadSeq.current) setLoading(false);
    }
  }, [queryArgs]);

  useEffect(() => {
    void loadFirstPage(source, deviceId);
  }, [source, deviceId, loadFirstPage]);

  if (view === "command") {
    return <CommandLogView onShowAudit={() => setView("audit")} />;
  }

  const knownTotal = total !== null ? total : lastPageFull ? null : entries.length;
  const hasMore = lastPageFull && (knownTotal === null || entries.length < knownTotal);

  const loadMore = async () => {
    if (loading || loadingMore || !hasMore) return;
    const seq = loadSeq.current;
    const offset = fetchedRef.current;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const rows = await assetApi.auditQuery({ ...queryArgs(source, deviceId), limit: PAGE_SIZE, offset });
      if (seq !== loadSeq.current) return;
      fetchedRef.current += rows.length;
      setLastPageFull(rows.length === PAGE_SIZE);
      setEntries((prev) => mergeEntries(prev, rows));
    } catch (e) {
      if (seq === loadSeq.current) setMoreError(describeError(e));
    } finally {
      if (seq === loadSeq.current) setLoadingMore(false);
    }
  };

  return (
    <div className="nx-pane">
      <div className="nx-toolbar flex-wrap">
        <IconHistory size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">审计日志</span>
        <div className="nx-segment">
          <button className="nx-segment-item is-active" disabled>
            审计
          </button>
          <button className="nx-segment-item" onClick={() => setView("command")}>
            命令
          </button>
        </div>
        <span className="nx-hint">
          {error && entries.length === 0
            ? "审计记录加载失败"
            : knownTotal !== null
              ? `共 ${knownTotal} 条`
              : `已加载 ${entries.length} 条`}
        </span>
        {error && entries.length > 0 && (
          <span className="nx-hint text-red-300">刷新失败 · {error}</span>
        )}
        <div className="nx-spacer" />
        {devices !== null && devices.length > 0 && (
          <select
            className="nx-select shrink-0"
            style={{ width: 140 }}
            aria-label="按设备过滤"
            value={deviceId}
            onChange={(e) => setDeviceId(e.target.value)}
          >
            <option value="">全部设备</option>
            {devices.map((d) => (
              <option key={d.id} value={d.id}>
                {d.name}
              </option>
            ))}
          </select>
        )}
        <div className="nx-segment">
          {FILTERS.map((f) => (
            <button
              key={f.value}
              className={`nx-segment-item ${source === f.value ? "is-active" : ""}`}
              onClick={() => setSource(f.value)}
              disabled={deviceId !== ""}
              title={deviceId !== "" ? "按设备过滤时已覆盖来源筛选" : undefined}
            >
              {f.label}
            </button>
          ))}
        </div>
        <button
          className="nx-btn nx-btn-ghost"
          onClick={() => void loadFirstPage(source, deviceId)}
          disabled={loading}
        >
          <IconRefresh size={13} className={loading ? "animate-spin" : ""} />
          刷新
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        <table className="nx-table nx-table-fixed">
          <thead>
            <tr>
              <th style={{ width: 168 }}>时间</th>
              <th style={{ width: 72 }}>来源</th>
              <th style={{ width: 140 }}>动作</th>
              <th>详情</th>
              <th style={{ width: 84 }} className="nx-right">
                退出码
              </th>
              <th style={{ width: 84 }} className="nx-right">
                耗时
              </th>
            </tr>
          </thead>
          <tbody>
            {entries.map((e) => (
              <tr key={e.id}>
                <td className="nx-mono whitespace-nowrap">{new Date(e.ts).toLocaleString()}</td>
                <td>
                  <span className={`nx-badge ${e.source === "ai" ? "nx-badge-purple" : e.source === "fleet" ? "nx-badge-green" : "nx-badge-blue"}`}>
                    {sourceLabel(e.source)}
                  </span>
                </td>
                <td className="font-mono text-[11.5px] text-neutral-200" title={e.kind}>
                  {KIND_LABELS[e.kind] ?? e.kind}
                </td>
                <td className="nx-mono max-w-[520px] truncate" title={JSON.stringify(e.payload)}>
                  {JSON.stringify(e.payload)}
                </td>
                <td className="nx-right">
                  {e.exitCode === null ? (
                    <span className="text-neutral-600">—</span>
                  ) : e.exitCode === 0 ? (
                    <span className="text-green-300" title="成功">
                      ✓ 0
                    </span>
                  ) : (
                    <span className="text-red-300" title="失败">
                      ✗ {e.exitCode}
                    </span>
                  )}
                </td>
                <td className="nx-right nx-mono">
                  {e.durationMs === null ? <span className="text-neutral-600">—</span> : `${e.durationMs}ms`}
                </td>
              </tr>
            ))}
            {entries.length === 0 && loading ? (
              <AuditSkeletonRows />
            ) : entries.length === 0 && error ? (
              <tr>
                <td colSpan={6} className="nx-table-empty">
                  <span className="text-red-300">审计记录加载失败 · {error}</span>
                  <button
                    className="nx-btn nx-btn-ghost nx-btn-sm ml-2"
                    onClick={() => void loadFirstPage(source, deviceId)}
                    disabled={loading}
                  >
                    <IconRefresh size={12} />
                    重试
                  </button>
                </td>
              </tr>
            ) : entries.length === 0 ? (
              <tr>
                <td colSpan={6} className="nx-table-empty">
                  暂无记录：连接主机、执行命令或让 AI 动手之后，这里会逐条记下来。
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>

      {entries.length > 0 && (
        <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-t border-neutral-800/60 bg-neutral-950/40 px-3 py-1.5 text-[11px] text-neutral-500">
          <span className="nx-hint min-w-0 truncate">
            {knownTotal !== null ? `已显示 ${entries.length} / ${knownTotal} 条` : `已显示 ${entries.length} 条`}
          </span>
          <div className="nx-spacer" />
          {moreError ? (
            <>
              <span className="min-w-0 break-words text-red-300">加载更多失败 · {moreError}</span>
              <button
                className="nx-btn nx-btn-ghost nx-btn-xs"
                onClick={() => void loadMore()}
                disabled={loadingMore}
              >
                {loadingMore ? <IconRefresh size={11} className="animate-spin" /> : null}
                重试
              </button>
            </>
          ) : hasMore ? (
            <button
              className="nx-btn nx-btn-ghost nx-btn-xs"
              onClick={() => void loadMore()}
              disabled={loadingMore}
            >
              {loadingMore ? <IconRefresh size={11} className="animate-spin" /> : null}
              加载更多
            </button>
          ) : (
            <span>已加载全部</span>
          )}
        </div>
      )}

      <div className="flex shrink-0 items-center gap-3 border-t border-neutral-800/60 bg-neutral-950/40 px-3 py-1.5 text-[11px] text-neutral-500">
        <span>会话命令、AI 动作、文件写操作与设备上下线都会落库，逐条标注来源；有退出码的记录会一并展示。</span>
      </div>
    </div>
  );
}
