// Free of imports so the website demo can use it.
export type SqlDialect = "mysql" | "postgres";

interface StatementRange {
  from: number;
  // Exclusive end, including the terminating ';' when there is one.
  to: number;
  // False when the range holds only whitespace and comments.
  hasCode: boolean;
}

function isSpace(c: string): boolean {
  return c === " " || c === "\t" || c === "\n" || c === "\r" || c === "\f" || c === "\v";
}

function isWordChar(c: string | undefined): boolean {
  return c !== undefined && /[\p{L}\p{N}_$]/u.test(c);
}

// skipQuoted returns the index after the literal or identifier opened at start.
// A doubled quote escapes the quote; backslash escapes apply only when enabled.
function skipQuoted(doc: string, start: number, backslash: boolean): number {
  const quote = doc[start];
  let i = start + 1;
  while (i < doc.length) {
    const c = doc[i];
    if (backslash && c === "\\") {
      i += 2;
      continue;
    }
    if (c === quote) {
      if (doc[i + 1] === quote) {
        i += 2;
        continue;
      }
      return i + 1;
    }
    i++;
  }
  return doc.length;
}

// sqlStatementRanges splits doc on top-level ';', skipping quotes, comments,
// MySQL backticks and PostgreSQL dollar quotes.
function sqlStatementRanges(doc: string, dialect: SqlDialect): StatementRange[] {
  const postgres = dialect === "postgres";
  const ranges: StatementRange[] = [];
  let from = 0;
  let hasCode = false;
  let i = 0;
  while (i < doc.length) {
    const c = doc[i];
    const next = doc[i + 1];
    if (c === "/" && next === "*") {
      let depth = 1;
      i += 2;
      while (i < doc.length && depth > 0) {
        if (postgres && doc[i] === "/" && doc[i + 1] === "*") {
          depth++;
          i += 2;
        } else if (doc[i] === "*" && doc[i + 1] === "/") {
          depth--;
          i += 2;
        } else {
          i++;
        }
      }
      continue;
    }
    if ((!postgres && c === "#") || (c === "-" && next === "-" && (postgres || i + 2 >= doc.length || isSpace(doc[i + 2])))) {
      const end = doc.indexOf("\n", i);
      i = end < 0 ? doc.length : end + 1;
      continue;
    }
    if (isSpace(c)) {
      i++;
      continue;
    }
    if (c === ";") {
      ranges.push({ from, to: i + 1, hasCode });
      from = i + 1;
      hasCode = false;
      i++;
      continue;
    }
    hasCode = true;
    if (c === "'" || c === '"' || (!postgres && c === "`")) {
      i = skipQuoted(doc, i, !postgres && c !== "`");
      continue;
    }
    if (postgres && c === "$") {
      const match = /^\$(?:[\p{L}_][\p{L}\p{N}_]*)?\$/u.exec(doc.slice(i, i + 256));
      if (match) {
        const tag = match[0];
        const end = doc.indexOf(tag, i + tag.length);
        i = end < 0 ? doc.length : end + tag.length;
        continue;
      }
      i++;
      continue;
    }
    if (isWordChar(c)) {
      let end = i;
      while (end < doc.length && isWordChar(doc[end])) end++;
      const word = doc.slice(i, end);
      if (postgres && (word === "E" || word === "e") && doc[end] === "'") {
        i = skipQuoted(doc, end, true);
      } else {
        i = end;
      }
      continue;
    }
    i++;
  }
  ranges.push({ from, to: doc.length, hasCode });
  return ranges;
}

// statementAtCursor returns the statement under pos, trimmed and including its
// terminating ';'. A cursor right after a ';' belongs to the statement it ends,
// and a cursor in trailing whitespace or comments picks the statement before.
export function statementAtCursor(doc: string, pos: number, dialect: SqlDialect): string {
  const ranges = sqlStatementRanges(doc, dialect);
  const at = Math.max(0, Math.min(pos, doc.length));
  let index = ranges.findIndex((range) => at <= range.to);
  if (index < 0) index = ranges.length - 1;
  while (index > 0 && !ranges[index].hasCode) index--;
  if (!ranges[index].hasCode) {
    index = ranges.findIndex((range) => range.hasCode);
    if (index < 0) return "";
  }
  const range = ranges[index];
  return doc.slice(range.from, range.to).trim();
}
