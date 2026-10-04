import { useEffect, useRef, useState } from "react";
import { EditorView, placeholder } from "@codemirror/view";
import { basicSetup } from "codemirror";
import { EditorState } from "@codemirror/state";
import { sql as sqlLang } from "@codemirror/lang-sql";
import { promptText } from "../../ui/dialogs";
import { nxHighlight } from "../../ui/editorTheme";
import { DEMO } from "../../demo";
import { dbApi, type QueryResult } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import {
  IconDatabase,
  IconHistory,
  IconLayers,
  IconList,
  IconLoader,
  IconPlay,
  IconRefresh,
  IconSearch,
  IconSettings,
  IconTable,
  IconXCircle,
} from "../../ui/icons";

export function DbPanel({ connId, kind }: { connId: string; kind: "mysql" | "redis" }) {
  return kind === "mysql" ? <MysqlView connId={connId} /> : <RedisView connId={connId} />;
}

function MysqlView({ connId }: { connId: string }) {
  const { pushToast } = useUi();
  const [schema, setSchema] = useState("");
  const [schemas, setSchemas] = useState<string[]>([]);
  const [schemasStatus, setSchemasStatus] = useState<"loading" | "error" | "ready">("loading");
  const [schemasError, setSchemasError] = useState<string | null>(null);
  const [tables, setTables] = useState<string[]>([]);
  const [tablesStatus, setTablesStatus] = useState<"loading" | "error" | "ready">("loading");
  const [tablesError, setTablesError] = useState<string | null>(null);
  const [result, setResult] = useState<QueryResult | null>(null);
  const [running, setRunning] = useState(false);
  const [browse, setBrowse] = useState<"tables" | "columns">("tables");
  const [activeTable, setActiveTable] = useState<string | null>(null);
  const viewRef = useRef<EditorView | null>(null);
  const hostRef = useRef<HTMLDivElement>(null);

  const SYSTEM_SCHEMAS = ["information_schema", "mysql", "performance_schema", "sys"];

  const loadTables = async (next: string) => {
    setSchema(next);
    setTablesStatus("loading");
    setTablesError(null);
    try {
      setTables(await dbApi.tables(connId, next));
      setTablesStatus("ready");
    } catch (e) {
      setTablesStatus("error");
      setTablesError(describeError(e));
    }
  };

  const loadSchemas = async () => {
    setSchemasStatus("loading");
    setSchemasError(null);
    try {
      const list = await dbApi.schemas(connId);
      setSchemas(list);
      setSchemasStatus("ready");
      const pick = list.find((s) => !SYSTEM_SCHEMAS.includes(s)) ?? list[0] ?? "";
      if (pick) {
        await loadTables(pick);
      } else {
        setTables([]);
        setTablesStatus("ready");
      }
    } catch (e) {
      setSchemasStatus("error");
      setSchemasError(describeError(e));
    }
  };

  useEffect(() => {
    void loadSchemas();
  }, [connId]);

  useEffect(() => {
    if (!hostRef.current || viewRef.current) return;
    const view = new EditorView({
      state: EditorState.create({
        doc: DEMO
          ? "-- 演示模式：数据都是假的。试试：\nSELECT channel, COUNT(*) AS orders\nFROM orders GROUP BY channel;\n"
          : "",
        extensions: [
          basicSetup,
          sqlLang(),
          nxHighlight,
          placeholder("在这里写 SQL，然后点右上角「执行」"),
        ],
      }),
      parent: hostRef.current,
    });
    viewRef.current = view;
    return () => {
      view.destroy();
      viewRef.current = null;
    };
  }, []);

  const run = async () => {
    const view = viewRef.current;
    if (!view) return;
    const sel = view.state.sliceDoc(view.state.selection.main.from, view.state.selection.main.to);
    const text = sel.trim() || view.state.doc.toString();
    setRunning(true);
    try {
      setResult(await dbApi.query(connId, text));
    } catch (e) {
      pushToast("error", describeError(e));
    } finally {
      setRunning(false);
    }
  };

  const fillQuery = (t: string) => {
    const view = viewRef.current;
    if (!view) return;
    const sql = `SELECT * FROM ${t} LIMIT 200;`;
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: sql } });
    view.focus();
  };

  const showColumns = async (t: string) => {
    setActiveTable(t);
    try {
      const { columns, indexes } = await dbApi.columns(connId, t, schema);
      setResult({
        columns: ["字段", "类型", "可空", "键", "默认值", "额外"],
        rows: [
          ...columns.map((c) => [
            c.name,
            c.type,
            c.nullable ? "YES" : "NO",
            c.key || "—",
            c.default ?? "NULL",
            c.extra || "—",
          ]),
          ...indexes.map((ix) => [
            `↳ ${ix.name}`,
            ix.unique ? "UNIQUE" : "INDEX",
            "—",
            `seq ${ix.seq}`,
            "—",
            ix.column,
          ]),
        ],
        rowsAffected: 0,
        durationMs: 8,
        truncated: false,
        error: null,
      });
    } catch (e) {
      pushToast("error", describeError(e));
    }
  };

  return (
    <div className="nx-pane">
      <div className="nx-toolbar">
        <IconDatabase size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">MySQL</span>
        <select
          className="nx-select nx-input-sm w-[168px]"
          value={schema}
          aria-label="选择数据库"
          onChange={(e) => void loadTables(e.target.value)}
        >
          {schemas.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <span className="nx-count">{tablesStatus === "ready" ? tables.length : "—"}</span>
        <span className="nx-hint">张表</span>
        <div className="nx-spacer" />
        <button className="nx-btn nx-btn-ghost nx-btn-sm" title="查询历史（M3 规划中）" disabled>
          <IconHistory size={13} />
          历史
        </button>
        <button
          className="nx-btn nx-btn-primary nx-btn-sm"
          disabled={running}
          onClick={() => void run()}
          title="执行整段 SQL，或只执行选中部分"
        >
          {running ? <IconRefresh size={13} className="animate-spin" /> : <IconPlay size={12} />}
          {running ? "执行中…" : "运行"}
          <span className="nx-kbd border-white/30 text-white/75">Ctrl ↵</span>
        </button>
      </div>

      <div
        ref={hostRef}
        className="h-[188px] shrink-0 overflow-auto border-b border-neutral-800/60 bg-term"
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
            e.preventDefault();
            void run();
          }
        }}
      />

      <div className="min-h-0 flex-1 overflow-auto">
        {result ? <ResultTable result={result} /> : <div className="nx-empty">写一条 SQL，Ctrl+Enter 运行</div>}
      </div>

      <div className="flex shrink-0 items-center gap-2 border-t border-neutral-800/60 bg-neutral-950/40 px-2.5 py-1.5">
        <div className="nx-segment">
          <button
            className={`nx-segment-item ${browse === "tables" ? "is-active" : ""}`}
            onClick={() => setBrowse("tables")}
            title="点表名把 SELECT 填进编辑器"
          >
            <IconTable size={12} />
            表
          </button>
          <button
            className={`nx-segment-item ${browse === "columns" ? "is-active" : ""}`}
            onClick={() => setBrowse("columns")}
            title="点表名查看字段结构"
          >
            <IconList size={12} />
            结构
          </button>
        </div>
        <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
          {schemasStatus === "error" ? (
            <>
              <span className="nx-hint shrink-0 text-red-300">
                数据库列表加载失败 · {schemasError}
              </span>
              <button
                className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
                onClick={() => void loadSchemas()}
              >
                <IconRefresh size={11} />
                重试
              </button>
            </>
          ) : schemasStatus === "loading" || tablesStatus === "loading" ? (
            <span className="nx-hint shrink-0">表列表加载中…</span>
          ) : tablesStatus === "error" ? (
            <>
              <span className="nx-hint shrink-0 text-red-300">表列表加载失败 · {tablesError}</span>
              <button
                className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
                onClick={() => void loadTables(schema)}
              >
                <IconRefresh size={11} />
                重试
              </button>
            </>
          ) : tables.length === 0 ? (
            <span className="nx-hint">这个库里没有表</span>
          ) : (
            <>
              {tables.slice(0, 60).map((t) => (
                <button
                  key={t}
                  className={`nx-chip shrink-0 font-mono ${
                    activeTable === t && browse === "columns" ? "nx-chip-accent" : ""
                  }`}
                  onClick={() => (browse === "tables" ? fillQuery(t) : void showColumns(t))}
                  title={browse === "tables" ? `填入 SELECT * FROM ${t}` : `查看 ${t} 的字段`}
                >
                  {t}
                </button>
              ))}
              {tables.length > 60 && <span className="nx-hint shrink-0">+{tables.length - 60}</span>}
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function ResultTable({ result }: { result: QueryResult }) {
  if (result.error) {
    return (
      <div className="p-3 font-mono text-xs leading-relaxed text-red-300">
        <span className="nx-badge nx-badge-red mr-2">SQL 错误</span>
        {result.error}
      </div>
    );
  }
  if (!result.columns.length) {
    return (
      <div className="p-3 text-xs text-neutral-400">
        <span className="nx-badge nx-badge-green mr-2">执行成功</span>
        {result.rowsAffected} 行受影响 · 耗时 {result.durationMs}ms
      </div>
    );
  }
  return (
    <div>
      <table className="nx-table">
        <thead>
          <tr>
            {result.columns.map((c) => (
              <th key={c}>{c}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {result.rows.map((row, i) => (
            <tr key={i}>
              {row.map((cell, j) => (
                <td key={j} className="nx-mono max-w-64 truncate" title={String(cell ?? "NULL")}>
                  {cell === null ? <span className="text-neutral-600">NULL</span> : String(cell)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <div className="flex items-center gap-3 px-3 py-2 text-[11px] text-neutral-500">
        <span>
          {result.rows.length} 行 · {result.durationMs}ms
        </span>
        {result.truncated && <span className="text-amber-300">结果已截断</span>}
        <span className="nx-spacer" />
        <button className="nx-link" onClick={() => void copyAsCsv(result)}>
          复制 CSV
        </button>
      </div>
    </div>
  );
}

async function copyAsCsv(result: QueryResult) {
  const lines = [
    result.columns.join(","),
    ...result.rows.map((r) =>
      r.map((c) => (c === null ? "" : `"${String(c).replace(/"/g, '""')}"`)).join(","),
    ),
  ];
  await navigator.clipboard.writeText(lines.join("\n"));
}

function RedisView({ connId }: { connId: string }) {
  const { pushToast } = useUi();
  const [pattern, setPattern] = useState("*");
  const [keys, setKeys] = useState<string[]>([]);
  const [cursor, setCursor] = useState(0);
  const [scanStatus, setScanStatus] = useState<"loading" | "error" | "ready">("loading");
  const [scanError, setScanError] = useState<string | null>(null);
  const [pageError, setPageError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [view, setView] = useState<import("../../ipc/types").RedisKeyViewDto | null>(null);
  const [viewPending, setViewPending] = useState(false);
  const [viewError, setViewError] = useState<string | null>(null);
  const [cmdText, setCmdText] = useState("INFO memory");
  const [cmdOut, setCmdOut] = useState("");

  const doScan = async (c: number) => {
    setPageError(null);
    if (c === 0) {
      setScanStatus("loading");
      setScanError(null);
    }
    try {
      const [next, ks] = await dbApi.redisScan(connId, c, pattern, 200);
      setCursor(next);
      setKeys((prev) => (c === 0 ? ks : [...prev, ...ks]));
      setScanStatus("ready");
    } catch (e) {
      if (c === 0) {
        setScanStatus("error");
        setScanError(describeError(e));
      } else {
        setPageError(describeError(e));
      }
    }
  };

  useEffect(() => {
    void doScan(0);
  }, [connId]);

  const inspect = async (key: string) => {
    setSelected(key);
    setView(null);
    setViewError(null);
    setViewPending(true);
    try {
      setView(await dbApi.redisInspect(connId, key));
    } catch (e) {
      setViewError(describeError(e));
    } finally {
      setViewPending(false);
    }
  };

  const runCmd = async () => {
    if (!cmdText.trim()) return;
    try {
      setCmdOut(await dbApi.redisCommand(connId, cmdText.trim().split(/\s+/)));
    } catch (e) {
      setCmdOut(`(error) ${describeError(e)}`);
    }
  };

  const editTtl = async () => {
    if (!view) return;
    const input = await promptText(`设置 TTL 秒数（当前 ${String(view.ttl)}，-1 表示持久化）`, String(view.ttl));
    if (input === null) return;
    await dbApi.redisSetTtl(connId, String(view.key), Number(input));
    pushToast("success", "TTL 已更新");
    void inspect(String(view.key));
  };

  return (
    <div className="nx-pane flex-row">
      <div className="flex w-[272px] shrink-0 flex-col border-r border-neutral-800/60">
        <div className="flex h-[38px] shrink-0 items-center gap-1.5 px-2.5">
          <IconSearch size={13} className="shrink-0 text-neutral-500" />
          <input
            className="nx-input nx-input-sm font-mono"
            value={pattern}
            onChange={(e) => setPattern(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && void doScan(0)}
            placeholder="匹配模式，如 products:*"
            aria-label="键匹配模式"
          />
          <button className="nx-btn nx-btn-sm" title="按 SCAN 分页拉取" onClick={() => void doScan(0)}>
            SCAN
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-auto px-1.5 pb-2">
          {scanStatus === "loading" ? (
            <div className="nx-hint p-3">键列表加载中…</div>
          ) : scanStatus === "error" ? (
            <div className="p-3">
              <div className="nx-hint text-red-300">键列表加载失败 · {scanError}</div>
              <button
                className="nx-btn nx-btn-ghost nx-btn-xs mt-1.5"
                onClick={() => void doScan(0)}
              >
                <IconRefresh size={11} />
                重试
              </button>
            </div>
          ) : keys.length === 0 && cursor === 0 ? (
            <div className="nx-hint p-3">没有匹配的键</div>
          ) : keys.length === 0 ? (
            <div className="nx-hint p-3">本页没有匹配的键，继续下一页…</div>
          ) : (
            keys.map((k) => (
              <div
                key={k}
                className={`nx-row font-mono text-[11.5px] ${selected === k ? "is-selected" : ""}`}
                onClick={() => void inspect(k)}
              >
                <span className="min-w-0 flex-1 truncate">{k}</span>
              </div>
            ))
          )}
        </div>
        {pageError && (
          <div className="flex shrink-0 items-center gap-1.5 border-t border-neutral-800/60 px-2.5 py-1.5">
            <span className="nx-hint min-w-0 flex-1 truncate text-red-300">
              下一页加载失败 · {pageError}
            </span>
            <button
              className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
              onClick={() => void doScan(cursor)}
            >
              <IconRefresh size={11} />
              重试
            </button>
          </div>
        )}
        {cursor > 0 && (
          <button
            className="shrink-0 border-t border-neutral-800/60 px-2.5 py-1.5 text-left text-[11.5px] text-blue-300 hover:bg-neutral-800/50"
            onClick={() => void doScan(cursor)}
          >
            下一页（cursor={cursor}）
          </button>
        )}
      </div>

      <div className="flex min-w-0 flex-1 flex-col">
        {view ? (
          <>
            <div className="nx-toolbar">
              <span className="nx-badge nx-badge-blue">{String(view.keyType)}</span>
              <span className="nx-toolbar-title truncate font-mono">{String(view.key)}</span>
              <span className="nx-hint">
                TTL {String(view.ttl) === "-1" ? "持久化" : `${String(view.ttl)}s`}
              </span>
              <div className="nx-spacer" />
              <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void editTtl()}>
                <IconSettings size={13} />
                改 TTL
              </button>
            </div>
            <pre className="nx-pre min-h-0 flex-1 overflow-auto rounded-none bg-term">
              {JSON.stringify(view.value, null, 2)}
            </pre>
          </>
        ) : viewError && selected ? (
          <div className="nx-empty">
            <span className="nx-empty-icon">
              <IconXCircle size={18} />
            </span>
            <div className="text-[12.5px] text-red-300">键内容加载失败 · {viewError}</div>
            <button
              className="nx-btn nx-btn-ghost nx-btn-sm"
              onClick={() => void inspect(selected)}
            >
              <IconRefresh size={12} />
              重试
            </button>
          </div>
        ) : viewPending && selected ? (
          <div className="nx-empty">
            <span className="nx-empty-icon">
              <IconLoader size={18} className="animate-spin" />
            </span>
            键内容加载中…
          </div>
        ) : (
          <div className="nx-empty">
            <span className="nx-empty-icon">
              <IconLayers size={18} />
            </span>
            从左侧选一个键查看内容
          </div>
        )}

        <div className="shrink-0 border-t border-neutral-800/60 bg-neutral-950/40 p-2.5">
          <div className="mb-1.5 text-[11px] text-neutral-500">
            命令台 · 写命令会弹确认（§6.5 安全护栏）
          </div>
          <div className="flex items-center gap-1.5">
            <input
              className="nx-input nx-input-sm font-mono"
              value={cmdText}
              onChange={(e) => setCmdText(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && void runCmd()}
            />
            <button className="nx-btn nx-btn-sm" onClick={() => void runCmd()}>
              执行
            </button>
          </div>
          {cmdOut && (
            <pre className="nx-pre mt-1.5 max-h-40 overflow-auto text-[11px]">{cmdOut}</pre>
          )}
        </div>
      </div>
    </div>
  );
}
