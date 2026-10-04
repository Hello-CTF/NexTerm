import type { ReactNode } from "react";
import { IconCopy } from "../../ui/icons";
import { useUi } from "../../app/store";
import { safeWebUrl } from "./urlSafety";

type Block =
  | { kind: "code"; lang: string; code: string }
  | { kind: "heading"; level: number; text: string }
  | { kind: "ul"; items: string[] }
  | { kind: "ol"; items: string[] }
  | { kind: "quote"; text: string }
  | { kind: "hr" }
  | { kind: "table"; header: string[]; rows: string[][] }
  | { kind: "p"; text: string };

const FENCE = /^\s*```\s*([\w+-]*)\s*$/;
const HEADING = /^(#{1,6})\s+(.*)$/;
const UL = /^\s*[-*+]\s+(.*)$/;
const OL = /^\s*\d+[.)]\s+(.*)$/;
const HR = /^\s*([-*_])\1{2,}\s*$/;

function isTableSep(line: string): boolean {
  const t = line.trim();
  return t.includes("-") && t.includes("|") && /^\|?[\s:|-]+\|?$/.test(t);
}

function splitRow(line: string): string[] {
  let t = line.trim();
  if (t.startsWith("|")) t = t.slice(1);
  if (t.endsWith("|")) t = t.slice(0, -1);
  return t.split("|").map((c) => c.trim());
}

function isTableStart(lines: string[], i: number): boolean {
  return i + 1 < lines.length && lines[i].includes("|") && isTableSep(lines[i + 1]);
}

export function parseBlocks(text: string): Block[] {
  const out: Block[] = [];
  const lines = text.split("\n");
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];

    const fence = FENCE.exec(line);
    if (fence) {
      const lang = fence[1] ?? "";
      const body: string[] = [];
      i++;
      while (i < lines.length && !FENCE.test(lines[i])) {
        body.push(lines[i]);
        i++;
      }
      i++;
      out.push({ kind: "code", lang, code: body.join("\n").replace(/\s+$/, "") });
      continue;
    }

    if (!line.trim()) {
      i++;
      continue;
    }
    if (HR.test(line)) {
      out.push({ kind: "hr" });
      i++;
      continue;
    }

    if (isTableStart(lines, i)) {
      const header = splitRow(lines[i]);
      i += 2;
      const rows: string[][] = [];
      while (i < lines.length && lines[i].trim() && lines[i].includes("|")) {
        rows.push(splitRow(lines[i]));
        i++;
      }
      out.push({ kind: "table", header, rows });
      continue;
    }

    const h = HEADING.exec(line);
    if (h) {
      out.push({ kind: "heading", level: h[1].length, text: h[2].trim() });
      i++;
      continue;
    }

    if (UL.test(line)) {
      const items: string[] = [];
      while (i < lines.length && UL.test(lines[i])) {
        items.push(UL.exec(lines[i])![1]);
        i++;
      }
      out.push({ kind: "ul", items });
      continue;
    }

    if (OL.test(line)) {
      const items: string[] = [];
      while (i < lines.length && OL.test(lines[i])) {
        items.push(OL.exec(lines[i])![1]);
        i++;
      }
      out.push({ kind: "ol", items });
      continue;
    }

    if (line.startsWith(">")) {
      const quote: string[] = [];
      while (i < lines.length && lines[i].startsWith(">")) {
        quote.push(lines[i].replace(/^>\s?/, ""));
        i++;
      }
      out.push({ kind: "quote", text: quote.join("\n") });
      continue;
    }

    const para: string[] = [];
    while (
      i < lines.length &&
      lines[i].trim() &&
      !FENCE.test(lines[i]) &&
      !HEADING.test(lines[i]) &&
      !UL.test(lines[i]) &&
      !OL.test(lines[i]) &&
      !HR.test(lines[i]) &&
      !isTableStart(lines, i) &&
      !lines[i].startsWith(">")
    ) {
      para.push(lines[i]);
      i++;
    }
    if (!para.length) {
      para.push(lines[i++]);
    }
    out.push({ kind: "p", text: para.join("\n") });
  }
  return out;
}

function renderInline(text: string, keyBase: string): ReactNode[] {
  const out: ReactNode[] = [];
  let rest = text;
  let n = 0;

  while (rest.length) {
    const m = /`([^`]+)`/.exec(rest);
    if (!m) {
      out.push(...renderEmphasis(rest, `${keyBase}e${n}`));
      break;
    }
    if (m.index > 0) out.push(...renderEmphasis(rest.slice(0, m.index), `${keyBase}e${n}`));
    out.push(
      <code key={`${keyBase}c${n++}`} className="nx-md-code">
        {m[1]}
      </code>,
    );
    rest = rest.slice(m.index + m[0].length);
  }
  return out;
}

function renderEmphasis(text: string, keyBase: string): ReactNode[] {
  const out: ReactNode[] = [];
  const re = /\*\*([^*\n]+)\*\*|\*([^*\n]+)\*|\[([^\]\n]+)\]\(([^)\s]+)\)/g;
  let last = 0;
  let n = 0;
  let m: RegExpExecArray | null;

  while ((m = re.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const key = `${keyBase}m${n++}`;
    if (m[1] !== undefined) {
      out.push(
        <strong key={key} className="nx-md-bold">
          {m[1]}
        </strong>,
      );
    } else if (m[2] !== undefined) {
      out.push(
        <em key={key} className="nx-md-em">
          {m[2]}
        </em>,
      );
    } else {
      const href = safeWebUrl(m[4]);
      if (href) {
        out.push(
          <a key={key} className="nx-md-link" href={href} target="_blank" rel="noreferrer">
            {m[3]}
          </a>,
        );
      } else {
        out.push(m[0]);
      }
    }
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

function CodeBlock({ lang, code }: { lang: string; code: string }) {
  const pushToast = useUi((s) => s.pushToast);
  return (
    <div className="nx-md-pre">
      <div className="nx-md-pre-bar">
        <span className="nx-md-pre-lang">{lang || "text"}</span>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="复制这段"
          onClick={() => {
            void navigator.clipboard.writeText(code).then(
              () => pushToast("success", "已复制到剪贴板"),
              () => pushToast("error", "复制失败：剪贴板不可用"),
            );
          }}
        >
          <IconCopy size={11} />
        </button>
      </div>
      <pre className="nx-md-pre-body">
        <code>{code}</code>
      </pre>
    </div>
  );
}

const HEAD_CLASS: Record<number, string> = {
  1: "nx-md-h1",
  2: "nx-md-h2",
  3: "nx-md-h3",
  4: "nx-md-h4",
  5: "nx-md-h4",
  6: "nx-md-h4",
};

function Table({ header, rows }: { header: string[]; rows: string[][] }) {
  const cols = Math.max(header.length, ...rows.map((r) => r.length), 1);
  return (
    <div className="nx-md-table-wrap">
      <table className="nx-md-table">
        <thead>
          <tr>
            {Array.from({ length: cols }, (_, c) => (
              <th key={c}>{renderInline(header[c] ?? "", `th${c}`)}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i}>
              {Array.from({ length: cols }, (_, c) => (
                <td key={c}>{renderInline(r[c] ?? "", `td${i}-${c}`)}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function Markdown({ text, className = "" }: { text: string; className?: string }) {
  const blocks = parseBlocks(text);
  return (
    <div className={`nx-md ${className}`}>
      {blocks.map((b, i) => {
        const k = `b${i}`;
        switch (b.kind) {
          case "code":
            return <CodeBlock key={k} lang={b.lang} code={b.code} />;
          case "heading":
            return (
              <div key={k} className={HEAD_CLASS[b.level] ?? "nx-md-h4"}>
                {renderInline(b.text, k)}
              </div>
            );
          case "hr":
            return <hr key={k} className="nx-md-hr" />;
          case "quote":
            return (
              <blockquote key={k} className="nx-md-quote">
                {renderInline(b.text, k)}
              </blockquote>
            );
          case "ul":
            return (
              <ul key={k} className="nx-md-ul">
                {b.items.map((it, j) => (
                  <li key={j}>{renderInline(it, `${k}-${j}`)}</li>
                ))}
              </ul>
            );
          case "ol":
            return (
              <ol key={k} className="nx-md-ol">
                {b.items.map((it, j) => (
                  <li key={j}>{renderInline(it, `${k}-${j}`)}</li>
                ))}
              </ol>
            );
          case "table":
            return <Table key={k} header={b.header} rows={b.rows} />;
          default:
            return (
              <p key={k} className="nx-md-p">
                {renderInline(b.text, k)}
              </p>
            );
        }
      })}
    </div>
  );
}
