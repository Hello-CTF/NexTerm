import { useEffect, useId, useRef, useState } from "react";
import { EditorView, placeholder } from "@codemirror/view";
import { basicSetup } from "codemirror";
import { EditorState } from "@codemirror/state";
import { sql as sqlLang } from "@codemirror/lang-sql";
import { ask, promptText } from "../../ui/dialogs";
import { isImeKeyEvent } from "../../ui/DialogHost";
import { nxHighlight } from "../../ui/editorTheme";
import { DEMO } from "../../demo";
import { dbApi, type QueryResult } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import {
  IconChevronUp,
  IconDatabase,
  IconLayers,
  IconList,
  IconLoader,
  IconPlay,
  IconRefresh,
  IconSearch,
  IconSettings,
  IconTable,
  IconTerminal,
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
          EditorView.contentAttributes.of({ "aria-label": "SQL 编辑器" }),
          placeholder("在这里写 SQL，然后点右上角「运行」（一次只执行一条语句）"),
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
        <span className="nx-hint hidden min-[560px]:inline">张表</span>
        <div className="nx-spacer" />
        <button
          className="nx-btn nx-btn-primary nx-btn-sm sticky right-0"
          disabled={running}
          onClick={() => void run()}
          title="运行整段 SQL，或只运行选中部分（一次只执行一条语句）"
        >
          {running ? <IconRefresh size={13} className="animate-spin" /> : <IconPlay size={12} />}
          {running ? "运行中…" : "运行"}
          <span className="nx-kbd border-white/30 text-white/75">Ctrl ↵</span>
        </button>
      </div>

      <div
        ref={hostRef}
        className="h-[188px] max-h-[45%] shrink overflow-auto border-b border-neutral-800/60 bg-term"
        onKeyDown={(e) => {
          if (isImeKeyEvent(e)) return;
          if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
            e.preventDefault();
            void run();
          }
        }}
      />

      <div className="min-h-[48px] flex-1 overflow-auto" role="status">
        {result ? <ResultTable result={result} /> : <div className="nx-empty">写一条 SQL，Ctrl+Enter 运行。一次只执行一条语句，直接在远端库执行并立即生效</div>}
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
  const { pushToast } = useUi();
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
        <span className="nx-badge nx-badge-green mr-2">运行成功</span>
        {result.rowsAffected} 行受影响 · 耗时 {result.durationMs}ms
      </div>
    );
  }
  const copyCsv = async () => {
    try {
      await copyAsCsv(result);
      pushToast("success", "CSV 已复制");
    } catch (e) {
      pushToast("error", `复制失败：${describeError(e)}`);
    }
  };
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
        {result.truncated && <span className="text-amber-300">超过 5000 行，仅显示前 5000 行</span>}
        <span className="nx-spacer" />
        <button className="nx-link" onClick={() => void copyCsv()}>
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

const DESTRUCTIVE_REDIS_WARNINGS: Record<string, string> = {
  FLUSHALL: "该操作会删除 Redis 里的所有键，立即生效且不可恢复。",
  FLUSHDB: "该操作会删除当前数据库的所有键，立即生效且不可恢复。",
  SHUTDOWN: "该操作会停止 Redis 服务，正在使用它的应用会立即断连。",
  DEL: "该操作会立即删除指定的键，不可恢复。",
  UNLINK: "该操作会立即删除指定的键（异步版 DEL），不可恢复。",
  CONFIG: "CONFIG SET 会立即改动服务器配置，可能导致服务异常；CONFIG REWRITE 会把改动写入配置文件。",
  DEBUG: "DEBUG 用于服务器内部排障，DEBUG SEGFAULT 会直接让 Redis 进程崩溃。",
  EVAL: "EVAL 会立即执行任意 Lua 脚本，脚本可以读写、删除任意数据。",
  EVALSHA: "EVALSHA 会立即执行已缓存的 Lua 脚本，脚本可以读写、删除任意数据。",
  FCALL: "FCALL 会立即调用已加载的函数，函数可以读写、删除任意数据。",
};

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
  const [consoleOpen, setConsoleOpen] = useState(false);
  const consoleScrollRef = useRef<HTMLDivElement>(null);
  const consolePanelId = useId();

  useEffect(() => {
    const el = consoleScrollRef.current;
    if (el && cmdOut) el.scrollTop = el.scrollHeight;
  }, [cmdOut]);

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
    const text = cmdText.trim();
    if (!text) return;
    const args = text.split(/\s+/);
    const command = args[0].toUpperCase();
    const warning = DESTRUCTIVE_REDIS_WARNINGS[command];
    if (warning) {
      const go = await ask(`执行 Redis ${command}？\n\n${warning}`, { kind: "warning" });
      if (!go) return;
    }
    try {
      setCmdOut(await dbApi.redisCommand(connId, args));
    } catch (e) {
      setCmdOut(`(error) ${describeError(e)}`);
    }
  };

  const editTtl = async () => {
    if (!view) return;
    const current = String(view.ttl) === "-1" ? "永不过期" : `${String(view.ttl)} 秒`;
    const input = await promptText(
      `设置过期时间（秒，填 -1 表示永不过期，填 0 会立即删除该键）。当前：${current}`,
      String(view.ttl),
    );
    if (input === null) return;
    const seconds = Number(input.trim());
    if (!/^(?:-1|\d+)$/.test(input.trim()) || !Number.isSafeInteger(seconds)) {
      pushToast("error", "过期时间必须是整数秒（-1 表示永不过期）");
      return;
    }
    try {
      await dbApi.redisSetTtl(connId, String(view.key), seconds);
    } catch (e) {
      pushToast("error", describeError(e));
      return;
    }
    pushToast("success", "过期时间已更新");
    void inspect(String(view.key));
  };

  return (
    <div className="nx-pane nx-redis-pane flex-col min-[560px]:flex-row">
      <div className="flex w-full min-h-0 max-h-[45%] flex-col border-b border-neutral-800/60 min-[560px]:w-[272px] min-[560px]:max-h-none min-[560px]:shrink-0 min-[560px]:border-b-0 min-[560px]:border-r">
        <div className="flex h-[38px] shrink-0 items-center gap-1.5 px-2.5">
          <IconSearch size={13} className="shrink-0 text-neutral-500" />
          <input
            className="nx-input nx-input-sm font-mono"
            value={pattern}
            onChange={(e) => setPattern(e.target.value)}
            onKeyDown={(e) => {
              if (isImeKeyEvent(e)) return;
              if (e.key === "Enter") void doScan(0);
            }}
            placeholder="匹配模式，如 products:*"
            aria-label="键匹配模式"
          />
          <button className="nx-btn nx-btn-sm px-3" title="按当前模式重新扫描" onClick={() => void doScan(0)}>
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
                className={`nx-row min-h-[32px] font-mono text-[11.5px] ${selected === k ? "is-selected" : ""}`}
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
            下一页
          </button>
        )}
      </div>

      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
          {view ? (
            <>
              <div className="nx-toolbar">
                <span className="nx-badge nx-badge-blue">{String(view.keyType)}</span>
                <span className="nx-toolbar-title min-w-0 flex-1 truncate font-mono">{String(view.key)}</span>
                <span className="nx-hint">
                  {String(view.ttl) === "-1" ? "永不过期" : `${String(view.ttl)} 秒后过期`}
                </span>
                <div className="nx-spacer" />
                <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void editTtl()}>
                  <IconSettings size={13} />
                  改过期时间
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
        </div>

        {consoleOpen ? (
          <div id={consolePanelId} className="nx-redis-console flex min-h-[74px] flex-col border-t border-neutral-800/60 bg-neutral-950/40 p-2.5">
            <div ref={consoleScrollRef} className="min-h-0 flex-1 overflow-auto">
              <div className="mb-1.5 text-[11px] text-neutral-500">
                <div>命令台 · FLUSHALL / FLUSHDB / DEL / UNLINK 会删除数据，SHUTDOWN 会停止服务，CONFIG / DEBUG / EVAL / EVALSHA / FCALL 可改动服务或执行任意脚本；这些命令执行前会要求确认，其余命令立即执行</div>
                <div>参数按空白切分，不支持引号包裹（如 SET k "a b" 会被拆成 3 个参数）</div>
              </div>
              {cmdOut && (
                <pre className="nx-pre text-[11px]" role="status">
                  {cmdOut}
                </pre>
              )}
            </div>
            <div className="flex shrink-0 items-center gap-1.5">
              <input
                className="nx-input nx-input-sm font-mono"
                value={cmdText}
                onChange={(e) => setCmdText(e.target.value)}
                onKeyDown={(e) => {
                  if (isImeKeyEvent(e)) return;
                  if (e.key === "Enter") void runCmd();
                }}
                aria-label="Redis 命令"
              />
              <button className="nx-btn nx-btn-sm" onClick={() => void runCmd()}>
                执行
              </button>
              <button
                className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
                aria-expanded={consoleOpen}
                aria-controls={consolePanelId}
                onClick={() => setConsoleOpen(false)}
              >
                收起
              </button>
            </div>
          </div>
        ) : (
          <button
            type="button"
            className="flex shrink-0 items-center gap-2 border-t border-neutral-800/60 bg-neutral-950/40 px-2.5 py-1.5 text-left"
            aria-expanded={consoleOpen}
            aria-controls={consolePanelId}
            onClick={() => setConsoleOpen(true)}
          >
            <IconTerminal size={12} className="shrink-0 text-neutral-500" />
            <span className="shrink-0 text-[11.5px] text-neutral-300">命令台</span>
            <span className="min-w-0 flex-1 truncate text-[11px] text-neutral-500">
              {cmdOut.trim()
                ? cmdOut.trim().split("\n").slice(-1)[0]
                : "执行任意 Redis 命令；FLUSHALL / DEL 等危险命令会先要求确认"}
            </span>
            <IconChevronUp size={11} className="shrink-0 rotate-180 text-neutral-500" />
          </button>
        )}
      </div>
    </div>
  );
}
