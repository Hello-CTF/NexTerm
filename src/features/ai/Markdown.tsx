// AI 回答的 Markdown 渲染。
//
// 为什么自己写，而不引 react-markdown / marked：
//   · **安全比体积更重要**：模型输出全部走 React 元素组装，**从不拼 HTML 字符串**，
//     也就不需要再配一套 sanitizer —— 结构上就没有注入面；
//   · 只需要八种语法（代码块 / 标题 / 列表 / 引用 / 分割线 / 表格 / 行内码 / 强调），
//     为这点需求引一个解析器 + 一套净化规则不划算，也和项目"零依赖小工具"的习惯一致。
//
// 刻意不做的事：脚注、HTML 透传、嵌套列表。AI 排障回答里基本用不到，
// 真需要时再加，而不是先写好再等它烂掉。
//
// 表格**做了** —— 本来按上面这条原则砍掉的，但真机上模型给的回答立刻就用上了
// （`| 服务 | 说明 |`），不渲染的话用户看到的是一堆竖线。教训：判断"用不用得到"
// 要看**不渲染时的后果**，而不是看它实现起来麻不麻烦。
import type { ReactNode } from "react";
import { IconCopy } from "../../ui/icons";
import { useUi } from "../../app/store";
import { safeWebUrl } from "./urlSafety";

/* ── 块级解析 ─────────────────────────────────────────────────────────── */

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

/** 表格分隔行（`|---|:--:|`）。**必须含 `|`**，免得把 `---` 分割线抢过来。 */
function isTableSep(line: string): boolean {
  const t = line.trim();
  return t.includes("-") && t.includes("|") && /^\|?[\s:|-]+\|?$/.test(t);
}

/** 把 `| a | b |` 拆成 `["a","b"]`；首尾竖线可有可无。 */
function splitRow(line: string): string[] {
  let t = line.trim();
  if (t.startsWith("|")) t = t.slice(1);
  if (t.endsWith("|")) t = t.slice(0, -1);
  return t.split("|").map((c) => c.trim());
}

/** 这一行是不是表格开头：本行含竖线，且**下一行是分隔行**。 */
function isTableStart(lines: string[], i: number): boolean {
  return i + 1 < lines.length && lines[i].includes("|") && isTableSep(lines[i + 1]);
}

/**
 * 把整段文本切成块。
 *
 * 未闭合的 ``` 按"到文末都是代码"处理 —— 流式输出时用户会长期看到半截代码块，
 * 成对才渲染的话那段内容会先以普通文本形式乱跳一遍。
 */
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
      i++; // 吃掉收尾的 ```（没有也照样前进一格，等价于到文末）
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
      i += 2; // 表头 + 分隔行一起吃掉（分隔行只用来判定，不渲染）
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

    // 段落：连着读到空行、或撞上另一个块的开头为止。
    // 行内换行保留（模型常把"结论 + 说明"写成相邻两行，压成一行反而难读）。
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
    // 保险：上面 while 的条件理论上不会全落空，但真落空也不能死循环
    if (!para.length) {
      para.push(lines[i++]);
    }
    out.push({ kind: "p", text: para.join("\n") });
  }
  return out;
}

/* ── 行内解析 ─────────────────────────────────────────────────────────── */

/**
 * 行内标记。
 *
 * **行内代码优先于强调**：`` `**x**` `` 里的星号是字面量，先切代码段能让它
 * 天然免疫后续解析，不必在正则里写一堆否定断言。
 */
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

/** 粗体 / 斜体 / 链接。`**` 必须排在 `*` 前面，否则粗体会被斜体先吃掉。 */
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

/* ── 渲染 ─────────────────────────────────────────────────────────────── */

/** 代码块：带语言标签与复制按钮（模型给的命令，用户多半要拿去粘贴）。 */
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

/** 表格：窄侧栏放不下就横向滚动，别把列压成一字一行。 */
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
