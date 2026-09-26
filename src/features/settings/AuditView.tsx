// 审计日志视图（M2-T8 回放界面）：用户与 AI 的所有关键动作。
import { useEffect, useState } from "react";
import { assetApi } from "../../ipc/commands";
import { IconHistory, IconRefresh } from "../../ui/icons";

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

const FILTERS: { value: "" | "user" | "ai"; label: string }[] = [
  { value: "", label: "全部" },
  { value: "user", label: "用户" },
  { value: "ai", label: "AI" },
];

export function AuditView() {
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [source, setSource] = useState<"" | "user" | "ai">("");
  const [loading, setLoading] = useState(false);

  const load = async () => {
    setLoading(true);
    try {
      const rows = await assetApi.auditQuery({ source: source || undefined, limit: 300 });
      setEntries(
        rows.map((r) => ({
          id: r.id,
          ts: r.ts,
          sessionId: r.sessionId,
          assetId: r.assetId,
          source: r.source,
          kind: r.kind,
          payload: r.payload,
          exitCode: r.exitCode,
          durationMs: r.durationMs,
        })),
      );
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, [source]);

  const aiCount = entries.filter((e) => e.source === "ai").length;

  return (
    <div className="nx-pane">
      <div className="nx-toolbar">
        <IconHistory size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">审计日志</span>
        <span className="nx-hint">
          共 {entries.length} 条 · AI 发起 {aiCount} 条
        </span>
        <div className="nx-spacer" />
        <div className="nx-segment">
          {FILTERS.map((f) => (
            <button
              key={f.value}
              className={`nx-segment-item ${source === f.value ? "is-active" : ""}`}
              onClick={() => setSource(f.value)}
            >
              {f.label}
            </button>
          ))}
        </div>
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void load()} disabled={loading}>
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
                  <span className={`nx-badge ${e.source === "ai" ? "nx-badge-purple" : "nx-badge-blue"}`}>
                    {e.source === "ai" ? "AI" : "用户"}
                  </span>
                </td>
                <td className="font-mono text-[11.5px] text-neutral-200">{e.kind}</td>
                <td className="nx-mono max-w-[520px] truncate" title={JSON.stringify(e.payload)}>
                  {JSON.stringify(e.payload)}
                </td>
                <td className="nx-right">
                  {e.exitCode === null ? (
                    <span className="text-neutral-600">—</span>
                  ) : e.exitCode === 0 ? (
                    <span className="text-green-300">0</span>
                  ) : (
                    <span className="text-red-300">{e.exitCode}</span>
                  )}
                </td>
                <td className="nx-right nx-mono">
                  {e.durationMs === null ? <span className="text-neutral-600">—</span> : `${e.durationMs}ms`}
                </td>
              </tr>
            ))}
            {!loading && entries.length === 0 && (
              <tr>
                <td colSpan={6} className="nx-table-empty">
                  暂无记录 —— 连接主机、执行命令或让 AI 动手之后，这里会逐条记下来。
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="flex shrink-0 items-center gap-3 border-t border-neutral-800/60 bg-neutral-950/40 px-3 py-1.5 text-[11px] text-neutral-500">
        <span>所有会话命令、AI 动作、文件写操作都会落库，来源与退出码齐全，可导出（§5.10）</span>
      </div>
    </div>
  );
}
