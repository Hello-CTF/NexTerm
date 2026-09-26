// 审计日志视图（M2-T8 回放界面）：用户与 AI 的所有关键动作。
import { useEffect, useState } from "react";
import { assetApi } from "../../ipc/commands";

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

export function AuditView() {
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [source, setSource] = useState("");
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

  return (
    <div className="flex h-full flex-col bg-neutral-900 text-sm text-neutral-300">
      <div className="flex items-center gap-2 border-b border-neutral-800 px-3 py-2">
        <span className="font-medium">审计日志</span>
        {(["", "user", "ai"] as const).map((s) => (
          <button
            key={s}
            className={`rounded px-2 py-0.5 text-xs ${
              source === s ? "bg-neutral-700" : "bg-neutral-800 hover:bg-neutral-700"
            }`}
            onClick={() => setSource(s)}
          >
            {s === "" ? "全部" : s === "user" ? "用户" : "AI"}
          </button>
        ))}
        <div className="flex-1" />
        <button className="rounded px-2 py-0.5 text-xs hover:bg-neutral-800" onClick={() => void load()}>
          刷新
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        <table className="w-full text-xs">
          <thead className="sticky top-0 bg-neutral-800 text-neutral-400">
            <tr>
              <th className="px-3 py-1.5 text-left">时间</th>
              <th className="px-2 py-1.5 text-left">来源</th>
              <th className="px-2 py-1.5 text-left">动作</th>
              <th className="px-2 py-1.5 text-left">详情</th>
              <th className="px-2 py-1.5 text-right">退出码</th>
            </tr>
          </thead>
          <tbody>
            {entries.map((e) => (
              <tr key={e.id} className="border-t border-neutral-800/60 hover:bg-neutral-800/40">
                <td className="px-3 py-1.5 whitespace-nowrap text-neutral-500">
                  {new Date(e.ts).toLocaleString()}
                </td>
                <td className="px-2 py-1.5">
                  <span
                    className={`rounded px-1.5 py-0.5 text-[10px] ${
                      e.source === "ai" ? "bg-purple-500/20 text-purple-300" : "bg-blue-500/20 text-blue-300"
                    }`}
                  >
                    {e.source}
                  </span>
                </td>
                <td className="px-2 py-1.5">{e.kind}</td>
                <td className="max-w-[480px] truncate px-2 py-1.5 font-mono text-neutral-500">
                  {JSON.stringify(e.payload)}
                </td>
                <td className="px-2 py-1.5 text-right">
                  {e.exitCode === null ? "-" : e.exitCode}
                </td>
              </tr>
            ))}
            {!loading && entries.length === 0 && (
              <tr>
                <td colSpan={5} className="px-4 py-8 text-center text-neutral-600">
                  暂无记录
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
