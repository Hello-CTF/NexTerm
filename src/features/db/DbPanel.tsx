import { useCallback, useEffect, useId, useRef, useState } from "react";
import { EditorView, placeholder } from "@codemirror/view";
import { basicSetup } from "codemirror";
import { EditorState } from "@codemirror/state";
import { sql as sqlLang } from "@codemirror/lang-sql";
import { ask, promptText } from "../../ui/dialogs";
import { isImeKeyEvent } from "../../ui/DialogHost";
import { nxHighlight } from "../../ui/editorTheme";
import { dbApi, type QueryResult, type TableDescribe } from "../../ipc/commands";
import {
  DB_KIND_LABEL,
  getWorkspaceProblemResetGeneration,
  sanitizeWorkspaceProblemText,
  useUi,
  type DbKind,
  type SqlExecutionOutcome,
  type SqlExecutionRecord,
  type WorkspaceProblem,
  type WorkspaceProblemInput,
} from "../../app/store";
import { WorkspaceProblems } from "../problems/WorkspaceProblems";
import { describeError } from "../../ui/errorText";
import { copyText } from "../../ui/clipboard";
import {
  IconChevronUp,
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
  IconTerminal,
  IconXCircle,
} from "../../ui/icons";

export function DbPanel({
  connId,
  kind,
  workspaceId,
}: {
  connId: string;
  kind: DbKind;
  workspaceId?: string;
}) {
  return kind === "redis" ? (
    <RedisView connId={connId} />
  ) : (
    <SqlView connId={connId} dialect={kind} workspaceId={workspaceId} />
  );
}

const SQL_SYSTEM_SCHEMAS: Record<string, string[]> = {
  mysql: ["information_schema", "mysql", "performance_schema", "sys"],
  postgres: ["information_schema", "pg_catalog", "pg_toast"],
};

const UNKNOWN_SQL_RESULT = "结果未知，请先确认数据库状态";

function isUnknownSqlExecutionError(error: unknown): boolean {
  if (typeof error !== "object" || error === null || !("code" in error) || !("message" in error)) {
    return false;
  }
  return (
    typeof error.message === "string" &&
    (error.code === "timeout" || error.code === "disconnected")
  );
}

function unknownSqlResultText(error: unknown): string {
  const detail = describeError(error);
  return detail.includes(UNKNOWN_SQL_RESULT) ? detail : `${UNKNOWN_SQL_RESULT}：${detail}`;
}

function SqlView({
  connId,
  dialect,
  workspaceId,
}: {
  connId: string;
  dialect: "mysql" | "postgres";
  workspaceId?: string;
}) {
  const pushToast = useUi((s) => s.pushToast);
  const executionHistory = useUi((state) =>
    workspaceId ? state.workspaceRuntime[workspaceId]?.sqlExecutionHistory : undefined,
  );
  const workspaceProblems = useUi((state) =>
    workspaceId ? state.workspaceRuntime[workspaceId]?.problems : undefined,
  );
  const [schema, setSchema] = useState("");
  const [schemas, setSchemas] = useState<string[]>([]);
  const [schemasStatus, setSchemasStatus] = useState<"loading" | "error" | "ready">("loading");
  const [schemasError, setSchemasError] = useState<string | null>(null);
  const [tables, setTables] = useState<string[]>([]);
  const [tablesStatus, setTablesStatus] = useState<"loading" | "error" | "ready">("loading");
  const [tablesError, setTablesError] = useState<string | null>(null);
  const [tableFilter, setTableFilter] = useState("");
  const [tablesExpanded, setTablesExpanded] = useState(false);
  const [result, setResult] = useState<QueryResult | null>(null);
  const [unknownResult, setUnknownResult] = useState<string | null>(null);
  const [transportError, setTransportError] = useState<string | null>(null);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [problemsOpen, setProblemsOpen] = useState(false);
  const [structure, setStructure] = useState<(TableDescribe & { table: string }) | null>(null);
  const [runningToken, setRunningToken] = useState<{ connId: string; seq: number } | null>(null);
  const [browse, setBrowse] = useState<"tables" | "columns">("tables");
  const [activeTable, setActiveTable] = useState<string | null>(null);
  const historyPanelId = useId();
  const problemPanelId = useId();
  const viewRef = useRef<EditorView | null>(null);
  const hostRef = useRef<HTMLDivElement>(null);
  const schemaRef = useRef("");
  const schemasRequestRef = useRef(0);
  const tablesRequestRef = useRef(0);
  const contentRequestRef = useRef(0);
  const runSeqRef = useRef(0);
  const activeRunsRef = useRef(new Map<string, number>());
  const aliveRef = useRef(false);
  const draftGenerationRef = useRef<number | undefined>(undefined);
  const generatedQueryRef = useRef<string | null>(null);
  const restoredSchemaRef = useRef<string | null>(null);
  const connIdRef = useRef(connId);
  const dialectRef = useRef(dialect);
  connIdRef.current = connId;
  dialectRef.current = dialect;
  const running = runningToken?.connId === connId;

  const captureTarget = () => {
    const targetWorkspace = workspaceId
      ? useUi.getState().workspaces.find((workspace) => workspace.id === workspaceId)
      : undefined;
    return {
      connId,
      dbKind: dialect,
      title: targetWorkspace?.title ?? connId,
      ...(targetWorkspace?.assetId !== undefined ? { assetId: targetWorkspace.assetId } : {}),
    };
  };

  const problemGeneration = () => {
    const runtime = workspaceId
      ? useUi.getState().ensureWorkspaceRuntime(workspaceId)
      : undefined;
    return runtime
      ? { generation: runtime.generation, resetGeneration: getWorkspaceProblemResetGeneration() }
      : undefined;
  };

  const recordProblem = (
    generation: { generation: number; resetGeneration: number } | undefined,
    problem: Omit<WorkspaceProblemInput, "generation" | "resetGeneration">,
  ) => {
    if (!workspaceId || generation === undefined) return;
    useUi.getState().recordWorkspaceProblem(workspaceId, { ...problem, ...generation });
  };

  const copyProblemDetail = async (detail: string) => {
    try {
      await copyText(sanitizeWorkspaceProblemText(detail));
      pushToast("success", "问题详情已复制");
    } catch (error) {
      pushToast("error", `复制失败：${describeError(error)}`);
    }
  };

  const problemTargetAvailable = (problem: WorkspaceProblem): boolean => {
    if (problem.target.connId !== undefined && problem.target.connId !== connId) return false;
    if (problem.source === "sql-tables" && problem.target.schema !== schemaRef.current) return false;
    return true;
  };

  const retryProblemRead = (problem: WorkspaceProblem) => {
    if (problem.source === "sql-schemas") void loadSchemas();
    if (problem.source === "sql-tables") void loadTables(problem.target.schema ?? "");
  };

  const persistDraft = useCallback((state: EditorState, targetWorkspaceId = workspaceId) => {
    const generation = draftGenerationRef.current;
    if (!targetWorkspaceId || generation === undefined) return;
    const selection = state.selection.main;
    const draft = {
      text: state.doc.toString(),
      selection: { from: selection.from, to: selection.to },
      schema: schemaRef.current,
      connId: connIdRef.current,
      dbKind: dialectRef.current,
    };
    queueMicrotask(() => {
      useUi.getState().updateSqlDraft(targetWorkspaceId, draft, generation);
    });
  }, [workspaceId]);

  const SYSTEM_SCHEMAS = SQL_SYSTEM_SCHEMAS[dialect];

  const loadTables = async (next: string) => {
    const request = ++tablesRequestRef.current;
    const generation = problemGeneration();
    const target = captureTarget();
    const changed = schemaRef.current !== next;
    schemaRef.current = next;
    setSchema(next);
    const view = viewRef.current;
    if (view) persistDraft(view.state);
    if (changed) {
      setActiveTable(null);
      setStructure(null);
      setTableFilter("");
      setTablesExpanded(false);
    }
    setTablesStatus("loading");
    setTablesError(null);
    try {
      const list = await dbApi.tables(connId, next);
      if (request !== tablesRequestRef.current) return;
      setTables(list);
      setTablesStatus("ready");
    } catch (e) {
      if (request !== tablesRequestRef.current) return;
      recordProblem(generation, {
        createdAt: Date.now(),
        source: "sql-tables",
        operation: "读取表列表",
        target: {
          ...target,
          identity: `${target.title} · ${target.connId}`,
          kind: target.dbKind,
          schema: next,
        },
        summary: describeError(e),
        detail: describeError(e),
        canRetryRead: true,
      });
      setTablesStatus("error");
      setTablesError(describeError(e));
    }
  };

  const loadSchemas = async () => {
    const request = ++schemasRequestRef.current;
    const generation = problemGeneration();
    const target = captureTarget();
    const tableRequest = tablesRequestRef.current;
    setSchemasStatus("loading");
    setSchemasError(null);
    try {
      const list = await dbApi.schemas(connId);
      if (request !== schemasRequestRef.current) return;
      setSchemas(list);
      setSchemasStatus("ready");
      if (tableRequest !== tablesRequestRef.current) return;
      const preferred = restoredSchemaRef.current ?? schemaRef.current;
      const pick = list.includes(preferred)
        ? preferred
        : (list.find((s) => !SYSTEM_SCHEMAS.includes(s)) ?? list[0] ?? "");
      restoredSchemaRef.current = null;
      if (pick) {
        await loadTables(pick);
      } else {
        schemaRef.current = "";
        setSchema("");
        const view = viewRef.current;
        if (view) persistDraft(view.state);
        setTables([]);
        setTablesStatus("ready");
      }
    } catch (e) {
      if (request !== schemasRequestRef.current) return;
      recordProblem(generation, {
        createdAt: Date.now(),
        source: "sql-schemas",
        operation: "读取 schema 列表",
        target: {
          ...target,
          identity: `${target.title} · ${target.connId}`,
          kind: target.dbKind,
          schema: "",
        },
        summary: describeError(e),
        detail: describeError(e),
        canRetryRead: true,
      });
      setSchemasStatus("error");
      setSchemasError(describeError(e));
    }
  };

  const loadSchemasRef = useRef(loadSchemas);
  useEffect(() => {
    loadSchemasRef.current = loadSchemas;
  });

  useEffect(() => {
    aliveRef.current = true;
    const activeRun = activeRunsRef.current.get(connId);
    setRunningToken(activeRun === undefined ? null : { connId, seq: activeRun });
    void loadSchemasRef.current();
    return () => {
      aliveRef.current = false;
      schemasRequestRef.current += 1;
      tablesRequestRef.current += 1;
      contentRequestRef.current += 1;
    };
  }, [connId, workspaceId]);

  useEffect(() => {
    if (!hostRef.current || viewRef.current) return;
    const runtime = workspaceId
      ? useUi.getState().getWorkspaceRuntime(workspaceId)
      : undefined;
    const draft = runtime?.sqlDraft;
    if (workspaceId && !runtime) {
      useUi.getState().updateSqlDraft(workspaceId, {
        text: "",
        selection: { from: 0, to: 0 },
        schema: "",
        connId: connIdRef.current,
        dbKind: dialectRef.current,
      });
    }
    draftGenerationRef.current = workspaceId
      ? useUi.getState().getWorkspaceRuntime(workspaceId)?.generation
      : undefined;
    generatedQueryRef.current = null;
    restoredSchemaRef.current = draft?.schema ?? null;
    schemaRef.current = draft?.schema ?? "";
    setSchema(schemaRef.current);
    const view = new EditorView({
      state: EditorState.create({
        doc: draft?.text ?? "",
        selection: draft
          ? { anchor: draft.selection.from, head: draft.selection.to }
          : undefined,
        extensions: [
          basicSetup,
          sqlLang(),
          nxHighlight,
          EditorView.contentAttributes.of({ "aria-label": "SQL 编辑器" }),
          placeholder("输入 SQL；草稿仅保留到当前工作区关闭或应用退出"),
          EditorView.updateListener.of((update) => {
            if (!update.docChanged && !update.selectionSet) return;
            if (
              update.docChanged &&
              update.state.doc.toString() !== generatedQueryRef.current
            ) {
              generatedQueryRef.current = null;
            }
            persistDraft(update.state);
          }),
        ],
      }),
      parent: hostRef.current,
    });
    viewRef.current = view;
    return () => {
      view.destroy();
      if (viewRef.current === view) viewRef.current = null;
    };
  }, [workspaceId, persistDraft]);

  useEffect(() => {
    const view = viewRef.current;
    if (view) persistDraft(view.state);
  }, [connId, dialect, workspaceId, persistDraft]);

  const run = async () => {
    const view = viewRef.current;
    if (!view || activeRunsRef.current.has(connId)) return;
    const runSeq = ++runSeqRef.current;
    activeRunsRef.current.set(connId, runSeq);
    const sel = view.state.sliceDoc(view.state.selection.main.from, view.state.selection.main.to);
    const text = sel.trim() || view.state.doc.toString();
    const targetWorkspaceId = workspaceId;
    const generation = draftGenerationRef.current;
    const problemContext = problemGeneration();
    const targetSchema = schemaRef.current;
    const target = captureTarget();
    const startedAt = Date.now();
    const request = ++contentRequestRef.current;
    const finish = (
      outcome: SqlExecutionOutcome,
      endedAt: number,
      durationMs: number,
      rowsAffected: number | null,
      error: string | null,
    ) => {
      if (!targetWorkspaceId || generation === undefined) return;
      useUi.getState().recordSqlExecution(targetWorkspaceId, {
        generation,
        target,
        sql: text,
        schema: targetSchema,
        startedAt,
        endedAt,
        durationMs,
        rowsAffected,
        error,
        outcome,
      });
      if (outcome !== "success" && error !== null) {
        recordProblem(problemContext, {
          createdAt: endedAt,
          source: "sql-query",
          operation: "执行 SQL",
          target: {
            ...target,
            identity: `${target.title} · ${target.connId}`,
            kind: target.dbKind,
            schema: targetSchema,
          },
          summary: error,
          detail: error,
          canRetryRead: false,
          sql: text,
          outcome,
        });
      }
    };
    setRunningToken({ connId, seq: runSeq });
    setStructure(null);
    setUnknownResult(null);
    setTransportError(null);
    try {
      const nextResult = await dbApi.query(connId, text);
      const endedAt = Date.now();
      const resultError = nextResult.error || null;
      const unknown = resultError !== null && isUnknownSqlExecutionError(resultError);
      const outcome: SqlExecutionOutcome =
        resultError === null ? "success" : unknown ? "unknown" : "error";
      finish(
        outcome,
        endedAt,
        nextResult.durationMs,
        unknown ? null : nextResult.rowsAffected,
        resultError === null ? null : unknown ? unknownSqlResultText(resultError) : resultError,
      );
      if (contentRequestRef.current !== request) return;
      if (unknown) {
        setResult(null);
        setUnknownResult(unknownSqlResultText(resultError));
      } else {
        setResult(nextResult);
        if (resultError === null && schemaRef.current) await loadTables(schemaRef.current);
      }
    } catch (e) {
      const endedAt = Date.now();
      const unknown = isUnknownSqlExecutionError(e);
      const message = unknown ? unknownSqlResultText(e) : describeError(e);
      finish(
        unknown ? "unknown" : "error",
        endedAt,
        Math.max(0, endedAt - startedAt),
        null,
        message,
      );
      if (contentRequestRef.current === request) {
        if (unknown) {
          setResult(null);
          setTransportError(null);
          setUnknownResult(message);
        } else {
          setTransportError(message);
        }
        pushToast("error", message);
      }
    } finally {
      if (activeRunsRef.current.get(connId) === runSeq) {
        activeRunsRef.current.delete(connId);
        if (aliveRef.current) {
          setRunningToken((current) =>
            current?.connId === connId && current.seq === runSeq ? null : current,
          );
        }
      }
    }
  };

  const replaceDraft = async (sql: string, generated: boolean, incomplete = false): Promise<boolean> => {
    const view = viewRef.current;
    if (!view) return false;
    const current = view.state.doc.toString();
    const replacing = current.trim() !== "" && current !== generatedQueryRef.current;
    if (incomplete || replacing) {
      const replace = await ask(
        incomplete
          ? `此历史 SQL 已截断，载入的只是不完整副本，不是原始完整 SQL。${
              replacing ? "\n\n当前 SQL 草稿也会被覆盖。" : ""
            }\n\n取消：保留当前草稿\n确定：载入不完整副本`
          : "当前 SQL 草稿会被覆盖。\n\n取消：保留草稿并取消\n确定：替换查询",
        { title: incomplete ? "载入不完整 SQL 副本？" : "替换 SQL 草稿？", kind: "warning" },
      );
      if (
        !replace ||
        viewRef.current !== view ||
        view.state.doc.toString() !== current ||
        connIdRef.current !== connId ||
        dialectRef.current !== dialect ||
        schemaRef.current !== schema
      ) {
        return false;
      }
    }
    generatedQueryRef.current = generated ? sql : null;
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: sql } });
    view.focus();
    return true;
  };

  const fillQuery = async (t: string) => {
    const quote = dialect === "postgres" ? '"' : "`";
    const table = [schema, t]
      .map((part) => `${quote}${part.replaceAll(quote, quote + quote)}${quote}`)
      .join(".");
    await replaceDraft(`SELECT * FROM ${table} LIMIT 200;`, true);
  };

  const loadProblemSql = async (problem: WorkspaceProblem) => {
    if (problem.sql === undefined) return;
    if (await replaceDraft(problem.sql, false, problem.sqlTruncated)) setProblemsOpen(false);
  };

  const showColumns = async (t: string) => {
    const request = ++contentRequestRef.current;
    setActiveTable(t);
    setUnknownResult(null);
    setTransportError(null);
    try {
      const description = await dbApi.columns(connId, t, schema);
      if (contentRequestRef.current !== request) return;
      setResult(null);
      setStructure({ table: t, ...description });
    } catch (e) {
      if (contentRequestRef.current === request) pushToast("error", describeError(e));
    }
  };

  const normalizedFilter = tableFilter.toLowerCase();
  const filteredTables = normalizedFilter
    ? tables.filter((t) => t.toLowerCase().includes(normalizedFilter))
    : tables;
  const visibleTables =
    tablesExpanded || normalizedFilter ? filteredTables : filteredTables.slice(0, 60);
  const showingProblems = problemsOpen && (workspaceProblems?.length ?? 0) > 0;

  return (
    <div className="nx-pane">
      <div className="nx-toolbar">
        <IconDatabase size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">{dialect === "postgres" ? "PostgreSQL" : "MySQL / MariaDB"}</span>
        <select
          className="nx-select nx-input-sm w-[168px]"
          value={schema}
          aria-label={dialect === "postgres" ? "选择 schema" : "选择数据库"}
          onChange={(e) => void loadTables(e.target.value)}
        >
          {schemas.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <span className="nx-count">{tablesStatus === "ready" ? tables.length : "—"}</span>
        <span className="nx-hint hidden min-[560px]:inline">
          {dialect === "postgres" ? "张表和视图" : "张表"}
        </span>
        <div className="nx-spacer" />
        {(workspaceProblems?.length ?? 0) > 0 && (
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm"
            aria-label="打开问题列表"
            aria-expanded={showingProblems}
            aria-controls={problemPanelId}
            onClick={() => {
              setHistoryOpen(false);
              setProblemsOpen((open) => !open);
            }}
          >
            问题
          </button>
        )}
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm"
          aria-expanded={historyOpen}
          aria-controls={historyPanelId}
          onClick={() => {
            setProblemsOpen(false);
            setHistoryOpen((open) => !open);
          }}
          title="查看执行历史；选择记录仅载入 SQL 草稿"
        >
          <IconHistory size={12} />
          历史
        </button>
        <button
          className="nx-btn nx-btn-primary nx-btn-sm sticky right-0"
          disabled={running}
          onClick={() => void run()}
          title="运行整段 SQL，或只运行选中部分（一次只执行一条语句）"
        >
          {running ? <IconRefresh size={13} className="animate-spin" /> : <IconPlay size={12} />}
          {running ? "正在运行…" : "运行"}
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

      <div
        id={showingProblems ? problemPanelId : historyPanelId}
        className="min-h-[48px] flex-1 overflow-auto"
        role={showingProblems || historyOpen ? "region" : "status"}
        aria-label={showingProblems ? "本次问题" : historyOpen ? "SQL 执行历史" : undefined}
      >
        {showingProblems && workspaceId ? (
          <WorkspaceProblems
            workspaceId={workspaceId}
            onRetryRead={retryProblemRead}
            onLoadSql={(problem) => void loadProblemSql(problem)}
            targetAvailable={problemTargetAvailable}
          />
        ) : historyOpen ? (
          <SqlExecutionHistory
            records={executionHistory ?? []}
            onLoad={(record) => void replaceDraft(record.sql, false, record.sqlTruncated)}
          />
        ) : structure ? (
          <StructureView structure={structure} />
        ) : unknownResult ? (
          <SqlErrorView
            label="结果未知"
            error={unknownResult}
            onCopy={() => void copyProblemDetail(unknownResult)}
            onShowProblems={() => setProblemsOpen(true)}
          />
        ) : transportError ? (
          <SqlErrorView
            label="连接错误"
            error={transportError}
            onCopy={() => void copyProblemDetail(transportError)}
            onShowProblems={() => setProblemsOpen(true)}
          />
        ) : result ? (
          <ResultTable
            result={result}
            onCopyError={(error) => void copyProblemDetail(error)}
            onShowProblems={() => setProblemsOpen(true)}
          />
        ) : (
          <div className="nx-empty">按 Ctrl+Enter 运行，一次只执行一条语句</div>
        )}
      </div>

      <div className="flex shrink-0 items-center gap-2 border-t border-neutral-800/60 bg-neutral-950/40 px-2.5 py-1.5">
        <div className="nx-segment">
          <button
            className={`nx-segment-item ${browse === "tables" ? "is-active" : ""}`}
            onClick={() => setBrowse("tables")}
            title="选择表名，将 SELECT 查询填入编辑器"
          >
            <IconTable size={12} />
            表
          </button>
          <button
            className={`nx-segment-item ${browse === "columns" ? "is-active" : ""}`}
            onClick={() => setBrowse("columns")}
            title="选择表名查看字段结构"
          >
            <IconList size={12} />
            结构
          </button>
        </div>
        <input
          className="nx-input nx-input-sm w-[120px] shrink-0"
          value={tableFilter}
          onChange={(e) => setTableFilter(e.target.value)}
          placeholder="搜索全部表"
          aria-label="搜索全部表"
        />
        <button
          className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
          disabled={!schema || tablesStatus === "loading"}
          onClick={() => void loadTables(schema)}
          title="刷新表列表"
        >
          <IconRefresh size={11} />
          刷新
        </button>
        <div
          className={`flex min-w-0 flex-1 items-center gap-1 ${
            tablesExpanded || normalizedFilter
              ? "max-h-48 flex-wrap overflow-y-auto"
              : "overflow-x-auto"
          }`}
        >
          {schemasStatus === "error" ? (
            <>
              <span className="nx-hint shrink-0 text-red-300">
                {dialect === "postgres" ? "未能加载 schema 列表" : "未能加载数据库列表"}：{schemasError}
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
            <span className="nx-hint shrink-0">正在加载表列表…</span>
          ) : tablesStatus === "error" ? (
            <>
              <span className="nx-hint shrink-0 text-red-300">未能加载表列表：{tablesError}</span>
              <button
                className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
                onClick={() => void loadTables(schema)}
              >
                <IconRefresh size={11} />
                重试
              </button>
            </>
          ) : tables.length === 0 ? (
            <span className="nx-hint">
              {dialect === "postgres" ? "此 schema 中没有表或视图" : "此数据库中没有表"}
            </span>
          ) : (
            <>
              {visibleTables.map((t) => (
                <button
                  key={t}
                  className={`nx-chip shrink-0 font-mono ${
                    activeTable === t && browse === "columns" ? "nx-chip-accent" : ""
                  }`}
                  onClick={() => (browse === "tables" ? void fillQuery(t) : void showColumns(t))}
                  title={browse === "tables" ? `填入 SELECT * FROM ${t}` : `查看 ${t} 的字段`}
                >
                  {t}
                </button>
              ))}
              {filteredTables.length === 0 && <span className="nx-hint">没有匹配的表</span>}
              {!normalizedFilter && tables.length > 60 && (
                <button
                  className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
                  onClick={() => setTablesExpanded((expanded) => !expanded)}
                >
                  {tablesExpanded ? "收起" : "展开全部"}
                </button>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function SqlExecutionHistory({
  records,
  onLoad,
}: {
  records: SqlExecutionRecord[];
  onLoad: (record: SqlExecutionRecord) => void;
}) {
  return (
    <div>
      <div className="border-b border-neutral-800/60 px-3 py-2 text-[11px] text-neutral-500">
        仅保留最近 50 条；工作区关闭或应用退出后清理
      </div>
      {records.length === 0 ? (
        <div className="nx-empty">暂无执行记录</div>
      ) : (
        <ul aria-label="执行记录" className="divide-y divide-neutral-800/60">
          {records.map((record) => {
            const startedAt = new Date(record.startedAt);
            const outcomeLabel =
              record.outcome === "success" ? "成功" : record.outcome === "unknown" ? "结果未知" : "SQL 错误";
            const outcomeClass =
              record.outcome === "success"
                ? "text-green-300"
                : record.outcome === "unknown"
                  ? "text-amber-300"
                  : "text-red-300";
            return (
              <li key={record.id}>
                <button
                  type="button"
                  className="flex w-full min-w-0 flex-col gap-1 px-3 py-2 text-left hover:bg-neutral-800/40"
                  onClick={() => onLoad(record)}
                  title={
                    record.sqlTruncated
                      ? "只载入不完整的 SQL 副本，不会自动执行"
                      : "只载入 SQL 草稿，不会自动执行"
                  }
                >
                  <span className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-neutral-400">
                    <time dateTime={startedAt.toISOString()}>{startedAt.toLocaleString()}</time>
                    <span>
                      {record.target.title} · {record.target.connId} · {DB_KIND_LABEL[record.target.dbKind]}
                      {record.target.assetId !== undefined ? ` · ${record.target.assetId}` : ""}
                    </span>
                    <span className={outcomeClass}>{outcomeLabel}</span>
                  </span>
                  <span className="text-[11px] text-neutral-500">
                    schema {record.schema || "默认"} · 耗时 {record.durationMs}ms ·{" "}
                    {record.rowsAffected === null ? "影响行数未知" : `${record.rowsAffected} 行受影响`}
                  </span>
                  <span className="w-full truncate font-mono text-xs text-neutral-300" title={record.sql}>
                    {record.sql}
                  </span>
                  {record.sqlTruncated && (
                    <span className="text-[11px] text-amber-300">SQL 已截断；载入的只是不完整副本</span>
                  )}
                  {record.errorTruncated && (
                    <span className="text-[11px] text-amber-300">错误已截断</span>
                  )}
                  {record.error && (
                    <span className={`max-h-10 overflow-hidden break-all text-xs ${outcomeClass}`}>
                      {record.error}
                    </span>
                  )}
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

function StructureView({ structure }: { structure: TableDescribe & { table: string } }) {
  return (
    <div className="min-w-max p-2">
      <div className="nx-hint px-1 py-1 font-mono">{structure.table}</div>
      <section aria-label="字段">
        <h3 className="nx-hint px-1 py-1">字段</h3>
        <table className="nx-table" aria-label="字段列表">
          <thead>
            <tr>
              {["字段", "类型", "可空", "键", "默认值", "额外"].map((name) => (
                <th key={name}>{name}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {structure.columns.map((column) => (
              <tr key={column.name}>
                <td className="nx-mono">{column.name}</td>
                <td className="nx-mono">{column.type}</td>
                <td>{column.nullable ? "YES" : "NO"}</td>
                <td>{column.key || "—"}</td>
                <td className="nx-mono">{column.default ?? "NULL"}</td>
                <td>{column.extra || "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
      <section aria-label="索引">
        <h3 className="nx-hint px-1 pb-1 pt-3">索引</h3>
        {structure.indexes.length ? (
          <table className="nx-table" aria-label="索引列表">
            <thead>
              <tr>
                {["索引", "类型", "字段", "顺序"].map((name) => (
                  <th key={name}>{name}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {structure.indexes.map((index) => (
                <tr key={`${index.name}:${index.column}:${index.seq}`}>
                  <td className="nx-mono">{index.name}</td>
                  <td>{index.unique ? "UNIQUE" : "INDEX"}</td>
                  <td className="nx-mono">{index.column}</td>
                  <td>{index.seq}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <div className="nx-hint px-1">没有索引</div>
        )}
      </section>
    </div>
  );
}

function SqlErrorView({
  label,
  error,
  onCopy,
  onShowProblems,
}: {
  label: string;
  error: string;
  onCopy: () => void;
  onShowProblems: () => void;
}) {
  const unknown = label === "结果未知";
  const summary = unknown
    ? "无法确认该 SQL 是否已生效。请先在数据库中确认结果，避免重复执行。"
    : label === "连接错误"
      ? "无法连接数据库，该 SQL 未执行。请检查连接后重新运行。"
      : "该 SQL 执行失败。请根据详细信息修改后重新运行。";
  return (
    <div className={`p-3 text-xs leading-relaxed ${unknown ? "text-amber-300" : "text-red-300"}`}>
      <div>
        <span className={`nx-badge ${unknown ? "" : "nx-badge-red"} mr-2`}>{label}</span>
        {summary}
      </div>
      <details className="mt-1.5 font-mono">
        <summary className="cursor-pointer font-sans text-neutral-400">查看详细信息</summary>
        <div className="mt-1 whitespace-pre-wrap break-words">{error}</div>
      </details>
      <div className="mt-2 flex items-center gap-2 font-sans">
        <button type="button" className="nx-link" onClick={onCopy}>
          复制
        </button>
        <button type="button" className="nx-link" onClick={onShowProblems}>
          查看问题
        </button>
      </div>
    </div>
  );
}

function ResultTable({
  result,
  onCopyError,
  onShowProblems,
}: {
  result: QueryResult;
  onCopyError: (error: string) => void;
  onShowProblems: () => void;
}) {
  const pushToast = useUi((s) => s.pushToast);
  if (result.error) {
    return (
      <SqlErrorView
        label="SQL 错误"
        error={result.error}
        onCopy={() => onCopyError(result.error as string)}
        onShowProblems={onShowProblems}
      />
    );
  }
  if (!result.columns.length) {
    return (
      <div className="p-3 text-xs text-neutral-400">
        <span className="nx-badge nx-badge-green mr-2">运行成功</span>
        {result.rowsAffected} 行受影响，耗时 {result.durationMs}ms
      </div>
    );
  }
  if (result.rows.length === 0) {
    return (
      <div className="p-3 text-xs text-neutral-400">
        <span className="nx-badge nx-badge-green mr-2">运行成功</span>
        查询未返回数据（{result.columns.length} 列，0 行），耗时 {result.durationMs}ms
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
                  {cell === null ? <span className="text-neutral-500">NULL</span> : String(cell)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <div className="flex items-center gap-3 px-3 py-2 text-[11px] text-neutral-500">
        <span>
          {result.rows.length} 行，耗时 {result.durationMs}ms
        </span>
        {result.truncated && <span className="text-amber-300">结果超过 5000 行，仅显示前 5000 行。可添加 LIMIT 缩小范围</span>}
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
  await copyText(lines.join("\n"));
}

const DESTRUCTIVE_REDIS_WARNINGS: Record<string, string> = {
  FLUSHALL: "删除 Redis 中的所有键，且无法恢复",
  FLUSHDB: "删除当前数据库中的所有键，且无法恢复",
  SHUTDOWN: "停止 Redis 服务，并断开所有客户端",
  DEL: "删除指定的键，且无法恢复",
  UNLINK: "异步删除指定的键，且无法恢复",
  CONFIG: "CONFIG SET 会修改服务器配置，可能导致服务异常；CONFIG REWRITE 会将修改写入配置文件",
  DEBUG: "DEBUG 用于服务器内部排障，DEBUG SEGFAULT 会导致 Redis 进程崩溃",
  EVAL: "EVAL 会执行任意 Lua 脚本，可读取、修改或删除任意数据",
  EVALSHA: "EVALSHA 会执行已缓存的 Lua 脚本，可读取、修改或删除任意数据",
  FCALL: "FCALL 会调用已加载的函数，可读取、修改或删除任意数据",
};

function RedisView({ connId }: { connId: string }) {
  const pushToast = useUi((s) => s.pushToast);
  const [pattern, setPattern] = useState("*");
  const [committedPattern, setCommittedPattern] = useState("*");
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
  const scanRequestRef = useRef(0);
  const activeScanRef = useRef<{ id: number; cursor: number; pattern: string } | null>(null);
  const inspectRequestRef = useRef(0);

  useEffect(() => {
    const el = consoleScrollRef.current;
    if (el && cmdOut) el.scrollTop = el.scrollHeight;
  }, [cmdOut]);

  const doScan = async (c: number) => {
    const requestedPattern = c === 0 ? pattern : committedPattern;
    const active = activeScanRef.current;
    if (active && (c !== 0 || (active.cursor === 0 && active.pattern === requestedPattern))) {
      return;
    }
    const request = { id: ++scanRequestRef.current, cursor: c, pattern: requestedPattern };
    activeScanRef.current = request;
    setPageError(null);
    if (c === 0) {
      setScanStatus("loading");
      setScanError(null);
      setCommittedPattern(pattern);
    }
    try {
      const [next, ks] = await dbApi.redisScan(connId, c, requestedPattern, 200);
      if (activeScanRef.current?.id !== request.id) return;
      setCursor(next);
      setKeys((prev) => [...new Set(c === 0 ? ks : [...prev, ...ks])]);
      setScanStatus("ready");
    } catch (e) {
      if (activeScanRef.current?.id !== request.id) return;
      if (c === 0) {
        setScanStatus("error");
        setScanError(describeError(e));
      } else {
        setPageError(describeError(e));
      }
    } finally {
      if (activeScanRef.current?.id === request.id) activeScanRef.current = null;
    }
  };

  const doScanRef = useRef(doScan);
  useEffect(() => {
    doScanRef.current = doScan;
  });

  useEffect(() => {
    void doScanRef.current(0);
    return () => {
      scanRequestRef.current += 1;
      activeScanRef.current = null;
      inspectRequestRef.current += 1;
    };
  }, [connId]);

  const inspect = async (key: string) => {
    const request = ++inspectRequestRef.current;
    setSelected(key);
    setView(null);
    setViewError(null);
    setViewPending(true);
    try {
      const next = await dbApi.redisInspect(connId, key);
      if (request !== inspectRequestRef.current) return;
      setView(next);
    } catch (e) {
      if (request !== inspectRequestRef.current) return;
      setViewError(describeError(e));
    } finally {
      if (request === inspectRequestRef.current) setViewPending(false);
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
            <div className="nx-hint p-3">正在加载键列表…</div>
          ) : scanStatus === "error" ? (
            <div className="p-3">
              <div className="nx-hint text-red-300">未能加载键列表：{scanError}</div>
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
              未能加载下一页：{pageError}
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
                  修改过期时间
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
              <div className="text-[12.5px] text-red-300">未能加载键内容：{viewError}</div>
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
              正在加载键内容…
            </div>
          ) : (
            <div className="nx-empty">
              <span className="nx-empty-icon">
                <IconLayers size={18} />
              </span>
              请选择左侧的键以查看内容
            </div>
          )}
        </div>

        {consoleOpen ? (
          <div id={consolePanelId} className="nx-redis-console flex min-h-[74px] flex-col border-t border-neutral-800/60 bg-neutral-950/40 p-2.5">
            <div ref={consoleScrollRef} className="min-h-0 flex-1 overflow-auto">
              <div className="mb-1.5 text-[11px] text-neutral-500">
                <div>控制台 · 使用 Redis 命令协议，不支持 SQL</div>
                <div>危险命令执行前需要确认，其余命令直接执行</div>
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
            <span className="shrink-0 text-[11.5px] text-neutral-300">控制台</span>
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
