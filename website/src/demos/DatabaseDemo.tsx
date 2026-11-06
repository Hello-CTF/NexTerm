import { useState, type KeyboardEvent } from "react";
import { tableSelectSql } from "../../../src/features/db/tableSelectSql";
import { IconDatabase, IconHistory, IconList, IconPlay, IconTable } from "../../../src/ui/icons";
import { copyToClipboard, errorText, pick, useToasts, type DemoProps } from "./common";

const SCHEMA = "public";

const copy = {
  zh: {
    schema: "选择 schema", tablesHint: "张表和视图", history: "历史", historyTitle: "查看执行历史；选择记录仅载入 SQL 草稿", historyLabel: "SQL 执行历史",
    run: "运行", runTitle: "运行整段 SQL，或只运行选中部分（一次只执行一条语句）", empty: "按 Ctrl+Enter 运行，一次只执行一条语句",
    rows: (n: number, ms: number) => `${n} 行，耗时 ${ms}ms`, copyCsv: "复制 CSV", csvCopied: "CSV 已复制", copyFailed: (reason: string) => `复制失败：${reason}`,
    tables: "表", structure: "结构", tablesTitle: "选择表名，将 SELECT 查询填入编辑器", structureTitle: "选择表名查看字段结构",
    fill: (table: string) => `填入 SELECT * FROM ${table}`, columns: (table: string) => `查看 ${table} 的字段`,
    structureColumns: ["字段", "类型", "可空", "键", "默认值", "额外"], fields: "字段", noHistory: "暂无执行记录",
    sampleNote: "演示只运行示例表的 SELECT 查询，不会修改任何数据。",
  },
  en: {
    schema: "Choose schema", tablesHint: "tables and views", history: "History", historyTitle: "Show run history; picking an entry only loads its SQL", historyLabel: "SQL run history",
    run: "Run", runTitle: "Run the whole SQL or just the selection (one statement at a time)", empty: "Press Ctrl+Enter to run one statement at a time",
    rows: (n: number, ms: number) => `${n} rows in ${ms}ms`, copyCsv: "Copy CSV", csvCopied: "CSV copied", copyFailed: (reason: string) => `Copy failed: ${reason}`,
    tables: "Tables", structure: "Structure", tablesTitle: "Pick a table to fill a SELECT query into the editor", structureTitle: "Pick a table to see its columns",
    fill: (table: string) => `Fill SELECT * FROM ${table}`, columns: (table: string) => `Show columns of ${table}`,
    structureColumns: ["Column", "Type", "Nullable", "Key", "Default", "Extra"], fields: "Columns", noHistory: "No runs yet",
    sampleNote: "This demo only runs SELECT queries on the sample tables and never changes data.",
  },
};

interface Column { name: string; type: string; nullable: boolean; key?: string; def?: string; extra?: string }
interface Table { columns: Column[]; rows: string[][]; ms: number }

const tables: Record<string, Table> = {
  orders: { ms: 4, columns: [
    { name: "id", type: "bigint", nullable: false, key: "PRI", def: "nextval('orders_id_seq')" },
    { name: "customer", type: "text", nullable: false },
    { name: "total", type: "numeric(10,2)", nullable: false },
    { name: "status", type: "text", nullable: false, def: "'pending'" },
  ], rows: [["1042", "acme", "1280.00", "paid"], ["1041", "globex", "860.00", "shipped"], ["1040", "initech", "2400.00", "paid"], ["1039", "acme", "320.00", "refunded"], ["1038", "umbrella", "95.50", "pending"]] },
  users: { ms: 2, columns: [
    { name: "id", type: "integer", nullable: false, key: "PRI" },
    { name: "name", type: "text", nullable: false },
    { name: "role", type: "text", nullable: false },
    { name: "last_login", type: "timestamptz", nullable: true },
  ], rows: [["7", "alice", "admin", "2026-11-05 22:14"], ["8", "bob", "ops", "2026-11-06 08:02"], ["9", "carol", "dev", "2026-11-04 17:40"]] },
  products: { ms: 3, columns: [
    { name: "sku", type: "text", nullable: false, key: "PRI" },
    { name: "name", type: "text", nullable: false },
    { name: "stock", type: "integer", nullable: false, def: "0" },
  ], rows: [["NX-01", "Terminal Pro", "128"], ["NX-02", "Sync Seat", "64"], ["NX-03", "Device Agent", "512"]] },
};

interface Result { table: string; columns: string[]; rows: string[][]; ms: number }

// runSql answers SELECT queries on the sample tables: optional column list, ORDER BY one column and LIMIT.
function runSql(sql: string): Result | null {
  const match = /^\s*select\s+(.+?)\s+from\s+(?:"?public"?\.)?"?(\w+)"?(?:\s+order\s+by\s+"?(\w+)"?(?:\s+(asc|desc))?)?(?:\s+limit\s+(\d+))?\s*;?\s*$/is.exec(sql);
  if (!match) return null;
  const table = tables[match[2].toLowerCase()];
  if (!table) return null;
  const all = table.columns.map((c) => c.name);
  const picked = match[1].trim() === "*" ? all : match[1].split(",").map((c) => c.trim().replace(/"/g, "").toLowerCase());
  if (picked.some((c) => !all.includes(c))) return null;
  let rows = table.rows.slice();
  if (match[3]) {
    const index = all.indexOf(match[3].toLowerCase());
    if (index < 0) return null;
    const numeric = rows.every((row) => /^-?\d+(\.\d+)?$/.test(row[index]));
    rows.sort((a, b) => (numeric ? Number(a[index]) - Number(b[index]) : a[index].localeCompare(b[index])));
    if (match[4]?.toLowerCase() === "desc") rows.reverse();
  }
  if (match[5]) rows = rows.slice(0, Number(match[5]));
  const indexes = picked.map((c) => all.indexOf(c));
  return { table: match[2].toLowerCase(), columns: picked, rows: rows.map((row) => indexes.map((i) => row[i])), ms: table.ms };
}

const initialSql = 'SELECT * FROM "public"."orders" ORDER BY id DESC LIMIT 200;';

export default function DatabaseDemo({ language }: DemoProps) {
  const t = pick(language, copy);
  const { push: pushToast, view: toastView } = useToasts(language);
  const [sql, setSql] = useState(initialSql);
  const [result, setResult] = useState<Result | null>(() => runSql(initialSql));
  const [warning, setWarning] = useState(false);
  const [browse, setBrowse] = useState<"tables" | "columns">("tables");
  const [structure, setStructure] = useState<string | null>(null);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [history, setHistory] = useState<{ sql: string; at: string }[]>([{ sql: initialSql, at: "15:12:04" }]);

  const run = () => {
    setHistoryOpen(false);
    setStructure(null);
    const next = runSql(sql);
    if (!next) {
      setWarning(true);
      return;
    }
    setWarning(false);
    setResult(next);
    const now = new Date();
    const at = [now.getHours(), now.getMinutes(), now.getSeconds()].map((v) => String(v).padStart(2, "0")).join(":");
    setHistory((list) => [{ sql, at }, ...list].slice(0, 20));
  };
  const onKey = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
      event.preventDefault();
      run();
    }
  };
  const pickTable = (name: string) => {
    if (browse === "columns") {
      setHistoryOpen(false);
      setStructure(name);
      return;
    }
    setSql(tableSelectSql("postgres", SCHEMA, name));
  };
  const copyCsv = async () => {
    if (!result) return;
    const lines = [result.columns.join(","), ...result.rows.map((row) => row.map((cell) => `"${cell.replace(/"/g, '""')}"`).join(","))];
    try {
      await copyToClipboard(lines.join("\n"));
      pushToast("success", t.csvCopied);
    } catch (error) {
      pushToast("error", t.copyFailed(errorText(error)));
    }
  };

  const described = structure ? tables[structure] : null;
  return <div className="nx-pane demo-db">
    <div className="nx-toolbar">
      <IconDatabase size={14} className="demo-muted" />
      <span className="nx-toolbar-title">PostgreSQL</span>
      <select className="nx-input demo-select demo-schema" aria-label={t.schema} value={SCHEMA} onChange={() => undefined}><option value={SCHEMA}>{SCHEMA}</option></select>
      <span className="nx-count">{Object.keys(tables).length}</span>
      <span className="nx-hint demo-hide-narrow">{t.tablesHint}</span>
      <span className="nx-spacer" />
      <button type="button" className={`nx-btn nx-btn-ghost nx-btn-sm${historyOpen ? " is-active" : ""}`} aria-expanded={historyOpen} title={t.historyTitle} onClick={() => setHistoryOpen((open) => !open)}><IconHistory size={12} />{t.history}</button>
      <button type="button" className="nx-btn nx-btn-primary nx-btn-sm demo-sticky-run" title={t.runTitle} onClick={run}><IconPlay size={12} />{t.run}<span className="nx-kbd demo-kbd-on-primary">Ctrl ↵</span></button>
    </div>
    <textarea className="demo-sql" aria-label="SQL" value={sql} spellCheck={false} onChange={(event) => setSql(event.target.value)} onKeyDown={onKey} />
    <div className="demo-db-output" role={historyOpen ? "region" : "status"} aria-label={historyOpen ? t.historyLabel : undefined}>
      {historyOpen ? <div className="demo-db-history">
        {history.length === 0 && <div className="nx-hint">{t.noHistory}</div>}
        {history.map((entry, index) => <button key={index} type="button" onClick={() => { setSql(entry.sql); setHistoryOpen(false); }}><span className="nx-hint">{entry.at}</span><code>{entry.sql}</code></button>)}
      </div> : described && structure ? <div className="demo-db-structure">
        <div className="nx-hint demo-mono">{structure}</div>
        <table className="nx-table" aria-label={t.fields}>
          <thead><tr>{t.structureColumns.map((c) => <th key={c}>{c}</th>)}</tr></thead>
          <tbody>{described.columns.map((c) => <tr key={c.name}><td className="nx-mono">{c.name}</td><td className="nx-mono">{c.type}</td><td>{c.nullable ? "YES" : "NO"}</td><td>{c.key || "—"}</td><td className="nx-mono">{c.def ?? "NULL"}</td><td>{c.extra || "—"}</td></tr>)}</tbody>
        </table>
      </div> : warning ? <div className="demo-db-warning">{t.sampleNote}</div> : result ? <div>
        <table className="nx-table">
          <thead><tr>{result.columns.map((c) => <th key={c}>{c}</th>)}</tr></thead>
          <tbody>{result.rows.map((row, i) => <tr key={i}>{row.map((cell, j) => <td key={j} className="nx-mono">{cell}</td>)}</tr>)}</tbody>
        </table>
        <div className="demo-db-summary"><span>{t.rows(result.rows.length, result.ms)}</span><span className="nx-spacer" /><button type="button" className="nx-link" onClick={() => void copyCsv()}>{t.copyCsv}</button></div>
      </div> : <div className="nx-hint demo-db-empty">{t.empty}</div>}
    </div>
    <div className="demo-db-browse">
      <div className="nx-segment">
        <button type="button" className={`nx-segment-item${browse === "tables" ? " is-active" : ""}`} aria-pressed={browse === "tables"} title={t.tablesTitle} onClick={() => setBrowse("tables")}><IconTable size={12} />{t.tables}</button>
        <button type="button" className={`nx-segment-item${browse === "columns" ? " is-active" : ""}`} aria-pressed={browse === "columns"} title={t.structureTitle} onClick={() => setBrowse("columns")}><IconList size={12} />{t.structure}</button>
      </div>
      <div className="demo-chips">
        {Object.keys(tables).map((name) => <button key={name} type="button" className={`nx-chip demo-mono${structure === name && browse === "columns" ? " nx-chip-accent" : ""}`} title={browse === "tables" ? t.fill(name) : t.columns(name)} onClick={() => pickTable(name)}>{name}</button>)}
      </div>
    </div>
    {toastView}
  </div>;
}
