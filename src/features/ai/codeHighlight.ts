import { StringStream } from "@codemirror/language";
import type { StreamParser } from "@codemirror/language";

export interface HighlightSpan {
  text: string;
  style: string | null;
}

type ModeLoader = () => Promise<StreamParser<unknown>>;

const MODE_LOADERS: Record<string, ModeLoader> = {
  bash: async () => (await import("@codemirror/legacy-modes/mode/shell")).shell,
  c: async () => (await import("@codemirror/legacy-modes/mode/clike")).c,
  cpp: async () => (await import("@codemirror/legacy-modes/mode/clike")).cpp,
  csharp: async () => (await import("@codemirror/legacy-modes/mode/clike")).csharp,
  css: async () => (await import("@codemirror/legacy-modes/mode/css")).css,
  diff: async () => (await import("@codemirror/legacy-modes/mode/diff")).diff,
  dockerfile: async () => (await import("@codemirror/legacy-modes/mode/dockerfile")).dockerFile,
  go: async () => (await import("@codemirror/legacy-modes/mode/go")).go,
  html: async () => (await import("@codemirror/legacy-modes/mode/xml")).html,
  java: async () => (await import("@codemirror/legacy-modes/mode/clike")).java,
  javascript: async () => (await import("@codemirror/legacy-modes/mode/javascript")).javascript,
  json: async () => (await import("@codemirror/legacy-modes/mode/javascript")).json,
  kotlin: async () => (await import("@codemirror/legacy-modes/mode/clike")).kotlin,
  lua: async () => (await import("@codemirror/legacy-modes/mode/lua")).lua,
  perl: async () => (await import("@codemirror/legacy-modes/mode/perl")).perl,
  powershell: async () => (await import("@codemirror/legacy-modes/mode/powershell")).powerShell,
  python: async () => (await import("@codemirror/legacy-modes/mode/python")).python,
  r: async () => (await import("@codemirror/legacy-modes/mode/r")).r,
  ruby: async () => (await import("@codemirror/legacy-modes/mode/ruby")).ruby,
  rust: async () => (await import("@codemirror/legacy-modes/mode/rust")).rust,
  sql: async () => (await import("@codemirror/legacy-modes/mode/sql")).standardSQL,
  swift: async () => (await import("@codemirror/legacy-modes/mode/swift")).swift,
  toml: async () => (await import("@codemirror/legacy-modes/mode/toml")).toml,
  typescript: async () => (await import("@codemirror/legacy-modes/mode/javascript")).typescript,
  xml: async () => (await import("@codemirror/legacy-modes/mode/xml")).xml,
  yaml: async () => (await import("@codemirror/legacy-modes/mode/yaml")).yaml,
};

const LANG_ALIASES: Record<string, string> = {
  bash: "bash",
  sh: "bash",
  shell: "bash",
  zsh: "bash",
  console: "bash",
  c: "c",
  h: "c",
  cpp: "cpp",
  "c++": "cpp",
  cc: "cpp",
  cxx: "cpp",
  hpp: "cpp",
  hxx: "cpp",
  csharp: "csharp",
  cs: "csharp",
  "c#": "csharp",
  css: "css",
  diff: "diff",
  patch: "diff",
  dockerfile: "dockerfile",
  docker: "dockerfile",
  go: "go",
  golang: "go",
  html: "html",
  vue: "html",
  svg: "html",
  xhtml: "html",
  java: "java",
  javascript: "javascript",
  js: "javascript",
  jsx: "javascript",
  node: "javascript",
  nodejs: "javascript",
  json: "json",
  jsonc: "json",
  json5: "json",
  kotlin: "kotlin",
  kt: "kotlin",
  lua: "lua",
  perl: "perl",
  powershell: "powershell",
  ps1: "powershell",
  pwsh: "powershell",
  python: "python",
  py: "python",
  python3: "python",
  r: "r",
  ruby: "ruby",
  rb: "ruby",
  rust: "rust",
  rs: "rust",
  sql: "sql",
  swift: "swift",
  toml: "toml",
  typescript: "typescript",
  ts: "typescript",
  xml: "xml",
  yaml: "yaml",
  yml: "yaml",
};

const SPAN_CACHE_LIMIT = 200;
const MAX_HIGHLIGHT_CHARS = 200_000;

const parserCache = new Map<string, StreamParser<unknown>>();
const loadCache = new Map<string, Promise<StreamParser<unknown> | null>>();
const spanCache = new Map<string, HighlightSpan[]>();

export function normalizeFenceLang(lang: string): string | null {
  const key = lang.trim().toLowerCase();
  if (!key) return null;
  const canonical = LANG_ALIASES[key];
  if (!canonical || !MODE_LOADERS[canonical]) return null;
  return canonical;
}

export function peekParser(lang: string): StreamParser<unknown> | null {
  return parserCache.get(lang) ?? null;
}

export function loadParser(lang: string): Promise<StreamParser<unknown> | null> {
  const cached = loadCache.get(lang);
  if (cached) return cached;
  const loader = MODE_LOADERS[lang];
  if (!loader) return Promise.resolve(null);
  const promise = loader()
    .then((parser) => {
      parserCache.set(lang, parser);
      return parser;
    })
    .catch(() => {
      loadCache.delete(lang);
      return null;
    });
  loadCache.set(lang, promise);
  return promise;
}

function pushSpan(spans: HighlightSpan[], text: string, style: string | null): void {
  if (!text) return;
  const last = spans[spans.length - 1];
  if (last && last.style === style) {
    last.text += text;
    return;
  }
  spans.push({ text, style });
}

export function tokenizeCode(parser: StreamParser<unknown>, code: string): HighlightSpan[] {
  const spans: HighlightSpan[] = [];
  const state = parser.startState ? parser.startState(2) : undefined;
  const lines = code.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const stream = new StringStream(line, 4, 2);
    while (!stream.eol()) {
      stream.start = stream.pos;
      let style: string | null = null;
      try {
        style = parser.token(stream, state);
      } catch {
        style = null;
      }
      if (stream.pos <= stream.start) stream.pos = stream.start + 1;
      pushSpan(spans, line.slice(stream.start, stream.pos), style);
    }
    if (!line && parser.blankLine) parser.blankLine(state, 2);
    if (i < lines.length - 1) pushSpan(spans, "\n", null);
  }
  return spans;
}

export function highlightSpans(
  lang: string,
  code: string,
  parser: StreamParser<unknown>,
): HighlightSpan[] | null {
  if (code.length > MAX_HIGHLIGHT_CHARS) return null;
  const key = `${lang}\n${code}`;
  const cached = spanCache.get(key);
  if (cached) return cached;
  const spans = tokenizeCode(parser, code);
  if (spanCache.size >= SPAN_CACHE_LIMIT) {
    const oldest = spanCache.keys().next();
    if (!oldest.done) spanCache.delete(oldest.value);
  }
  spanCache.set(key, spans);
  return spans;
}

export function spanClasses(style: string): string {
  return style
    .split(/[\s.]+/)
    .filter(Boolean)
    .map((part) => `nx-tok-${part.replace(/[^\w-]/g, "")}`)
    .join(" ");
}
