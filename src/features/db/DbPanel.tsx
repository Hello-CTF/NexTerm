// DB 面板（M3）：MySQL（SQL 编辑器 + 结果表格 + 库表浏览）+ Redis（SCAN + 查看器 + 命令台 + TTL）。
import { useRef, useState } from "react";
import { EditorView } from "@codemirror/view";
import { basicSetup } from "codemirror";
import { EditorState } from "@codemirror/state";
import { sql as sqlLang } from "@codemirror/lang-sql";
import { promptText } from "../../ui/dialogs";
import { dbApi, type QueryResult } from "../../ipc/commands";
import { useUi } from "../../app/store";

export function DbPanel({ connId, kind }: { connId: string; kind: "mysql" | "redis" }) {
  return kind === "mysql" ? <MysqlView connId={connId} /> : <RedisView connId={connId} />;
}

// ───────────────── MySQL ─────────────────

function MysqlView({ connId }: { connId: string }) {
  const { pushToast } = useUi();
  const [schema, setSchema] = useState<string>("");
  const [tables, setTables] = useState<string[]>([]);
  const [result, setResult] = useState<QueryResult | null>(null);
  const [running, setRunning] = useState(false);
  const viewRef = useRef<EditorView | null>(null);
  const hostRef = useRef<HTMLDivElement>(null);

  const loadSchemas = async () => {
    try {
      const schemas = await dbApi.schemas(connId);
      const pick = schemas.find((s) => !["information_schema", "mysql", "performance_schema", "sys"].includes(s)) ?? schemas[0] ?? "";
      setSchema(pick);
      setTables(pick ? await dbApi.tables(connId, pick) : []);
    } catch (e) {
      pushToast("error", String(e));
    }
  };
  void loadSchemas();

  if (!viewRef.current && hostRef.current) {
    viewRef.current = new EditorView({
      state: EditorState.create({
        doc: "SELECT 1\n",
        extensions: [
          basicSetup,
          sqlLang(),
          EditorView.theme({ "&": { fontSize: "13px", backgroundColor: "#12141a", color: "#d7dae0" } }),
        ],
      }),
      parent: hostRef.current,
    });
  }

  const run = async () => {
    const view = viewRef.current;
    if (!view) return;
    const selection = view.state.sliceDoc(view.state.selection.main.from, view.state.selection.main.to);
    const text = selection.trim() || view.state.doc.toString();
    setRunning(true);
    try {
      const r = await dbApi.query(connId, text);
      setResult(r);
    } catch (e) {
      pushToast("error", String(e));
    } finally {
      setRunning(false);
    }
  };

  return (
    <div className="flex h-full flex-col bg-neutral-900 text-sm text-neutral-300">
      <div className="flex items-center gap-2 border-b border-neutral-800 px-3 py-1.5 text-xs">
        <span className="font-medium text-neutral-200">MySQL</span>
        <select
          className="rounded bg-neutral-800 px-2 py-0.5 outline-none"
          value={schema}
          onChange={async (e) => {
            setSchema(e.target.value);
            setTables(await dbApi.tables(connId, e.target.value));
          }}
        >
          {(tables.length || schema) && <option value={schema}>{schema || "选择库"}</option>}
        </select>
        <span className="text-neutral-500">{tables.length} 表</span>
        <div className="flex-1" />
        <button
          className="rounded bg-green-600 px-3 py-0.5 text-white hover:bg-green-500 disabled:opacity-50"
          disabled={running}
          onClick={() => void run()}
        >
          {running ? "执行中…" : "运行 (Ctrl+Enter)"}
        </button>
      </div>
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="h-40 shrink-0 border-b border-neutral-800" ref={hostRef} />
        {result && (
          <ResultTable result={result} />
        )}
      </div>
      <TableSidebar connId={connId} tables={tables} />
    </div>
  );
}

function TableSidebar({ connId, tables }: { connId: string; tables: string[] }) {
  const { pushToast } = useUi();
  const [ddl, setDdl] = useState<string | null>(null);
  return (
    <>
      {tables.length > 0 && (
        <div className="flex flex-wrap gap-1 border-t border-neutral-800 px-3 py-1.5">
          {tables.slice(0, 40).map((t) => (
            <button
              key={t}
              className="rounded bg-neutral-800 px-2 py-0.5 font-mono text-[11px] hover:bg-neutral-700"
              onClick={async () => {
                try {
                  const d = await dbApi.columns(connId, t);
                  setDdl(JSON.stringify(d, null, 2));
                } catch (e) {
                  pushToast("error", String(e));
                }
              }}
            >
              {t}
            </button>
          ))}
        </div>
      )}
      {ddl && (
        <div className="max-h-40 overflow-auto border-t border-neutral-800 bg-[#12141a] p-2 font-mono text-[11px] text-neutral-300">
          <pre>{ddl}</pre>
        </div>
      )}
    </>
  );
}

function ResultTable({ result }: { result: QueryResult }) {
  if (result.error) {
    return (
      <div className="min-h-0 flex-1 overflow-auto p-3 font-mono text-xs text-red-400">
        SQL 错误：{result.error}
      </div>
    );
  }
  if (!result.columns.length) {
    return (
      <div className="min-h-0 flex-1 overflow-auto p-3 text-xs text-neutral-500">
        执行成功，{result.rowsAffected} 行受影响，耗时 {result.durationMs}ms
      </div>
    );
  }
  return (
    <div className="min-h-0 flex-1 overflow-auto">
      <table className="w-full text-xs">
        <thead className="sticky top-0 bg-neutral-800 text-neutral-300">
          <tr>
            {result.columns.map((c) => (
              <th key={c} className="px-2 py-1 text-left font-medium">
                {c}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {result.rows.map((row, i) => (
            <tr key={i} className="border-t border-neutral-800/50 hover:bg-neutral-800/40">
              {row.map((cell, j) => (
                <td key={j} className="max-w-64 truncate px-2 py-1 font-mono text-neutral-400">
                  {cell === null ? "NULL" : String(cell)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <div className="px-3 py-1 text-[11px] text-neutral-500">
        {result.rows.length} 行 · {result.durationMs}ms
        {result.truncated && <span className="ml-2 text-amber-400">结果已截断</span>}
      </div>
    </div>
  );
}

// ───────────────── Redis ─────────────────

function RedisView({ connId }: { connId: string }) {
  const { pushToast } = useUi();
  const [pattern, setPattern] = useState("*");
  const [keys, setKeys] = useState<string[]>([]);
  const [cursor, setCursor] = useState(0);
  const [view, setView] = useState<import("../../ipc/types").RedisKeyViewDto | null>(null);
  const [cmdText, setCmdText] = useState("PING");
  const [cmdOut, setCmdOut] = useState("");

  const doScan = async (c: number) => {
    try {
      const [next, ks] = await dbApi.redisScan(connId, c, pattern, 200);
      setCursor(next);
      setKeys((prev) => (c === 0 ? ks : [...prev, ...ks]));
    } catch (e) {
      pushToast("error", String(e));
    }
  };

  const inspect = async (key: string) => {
    try {
      setView(await dbApi.redisInspect(connId, key));
    } catch (e) {
      pushToast("error", String(e));
    }
  };

  const runCmd = async () => {
    try {
      const args = cmdText.trim().split(/\s+/);
      setCmdOut(await dbApi.redisCommand(connId, args));
    } catch (e) {
      setCmdOut(`(error) ${String(e)}`);
    }
  };

  const editTtl = async () => {
    if (!view) return;
    const input = await promptText(`设置 TTL 秒数（当前 ${String(view.ttl)}，-1 持久化）`, String(view.ttl));
    if (input === null) return;
    await dbApi.redisSetTtl(connId, String(view.key), Number(input));
    pushToast("success", "TTL 已更新");
    void inspect(String(view.key));
  };

  return (
    <div className="flex h-full bg-neutral-900 text-sm text-neutral-300">
      <div className="flex w-72 min-w-0 flex-col border-r border-neutral-800">
        <div className="flex items-center gap-1 border-b border-neutral-800 p-2">
          <input
            className="min-w-0 flex-1 rounded bg-neutral-800 px-2 py-1 font-mono text-xs outline-none"
            value={pattern}
            onChange={(e) => setPattern(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && void doScan(0)}
          />
          <button className="rounded bg-neutral-800 px-2 py-1 text-xs hover:bg-neutral-700" onClick={() => void doScan(0)}>
            SCAN
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-auto p-1">
          {keys.map((k) => (
            <div
              key={k}
              className="cursor-pointer truncate rounded px-2 py-1 font-mono text-xs hover:bg-neutral-800"
              onClick={() => void inspect(k)}
            >
              {k}
            </div>
          ))}
        </div>
        {cursor > 0 && (
          <button className="border-t border-neutral-800 px-2 py-1 text-xs text-blue-400 hover:bg-neutral-800" onClick={() => void doScan(cursor)}>
            下一页 (cursor={cursor})
          </button>
        )}
      </div>
      <div className="flex min-w-0 flex-1 flex-col">
        {view ? (
          <div className="min-h-0 flex-1 overflow-auto p-3">
            <div className="mb-2 flex items-center gap-2 text-xs">
              <span className="rounded bg-blue-500/20 px-2 py-0.5 text-blue-300">{String(view.keyType)}</span>
              <span className="font-mono text-neutral-300">{String(view.key)}</span>
              <span className="text-neutral-500">TTL {String(view.ttl)}</span>
              <button className="rounded px-2 py-0.5 hover:bg-neutral-800" onClick={() => void editTtl()}>
                改 TTL
              </button>
            </div>
            <pre className="whitespace-pre-wrap rounded bg-[#12141a] p-2 font-mono text-xs text-neutral-300">
              {JSON.stringify(view.value, null, 2)}
            </pre>
          </div>
        ) : (
          <div className="flex flex-1 items-center justify-center text-xs text-neutral-600">SCAN 后点击键查看</div>
        )}
        <div className="border-t border-neutral-800 p-2">
          <div className="mb-1 text-xs text-neutral-500">命令台（写命令会弹确认）</div>
          <div className="flex gap-1">
            <input
              className="min-w-0 flex-1 rounded bg-neutral-800 px-2 py-1 font-mono text-xs outline-none"
              value={cmdText}
              onChange={(e) => setCmdText(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && void runCmd()}
            />
            <button className="rounded bg-neutral-700 px-3 py-1 text-xs hover:bg-neutral-600" onClick={() => void runCmd()}>
              执行
            </button>
          </div>
          {cmdOut && (
            <pre className="mt-1 max-h-32 overflow-auto whitespace-pre-wrap rounded bg-[#12141a] p-2 font-mono text-[11px] text-neutral-300">
              {cmdOut}
            </pre>
          )}
        </div>
      </div>
    </div>
  );
}
