import { buildLogicalLines, findLinksInRows, segmentsOf, type LinkRowSegment, type StitchedLink, type TextRow } from "./terminalLinkProvider";

// File paths printed by programs: `src/a.ts:12:5`, `/etc/nginx/nginx.conf`,
// compiler and grep locations (`path:line:col: message`, `path(line,col)`),
// Python tracebacks (`File "x.py", line 12`) and, in Windows sessions, drive
// paths such as `C:\src\App.cs(12,5)`. Detection is text-only and offline: a
// candidate is checked against the file system only when it is activated.

export interface PathMatch {
  /** The path as printed, without the location suffix. */
  path: string;
  line?: number;
  col?: number;
  /** The matched text: the path plus its location suffix. */
  text: string;
  /** Range of `text` in the scanned line. */
  start: number;
  end: number;
}

export interface PathLink extends Omit<PathMatch, "start" | "end"> {
  segments: LinkRowSegment[];
}

export interface PathDetectOptions {
  windows: boolean;
  /** Whether relative paths can be resolved (the working directory is known). */
  relative: boolean;
}

// Characters that end a path: whitespace, quotes, brackets, list separators,
// glob characters, box drawing and full-width punctuation. The colon ends a
// path so `file:line` splits; drive letters are rejoined below.
const POSIX_RUN = /[^\s"'`<>|(){}[\],;:*?\\\u2500-\u257f\u3000-\u303f\uff00-\uff60]+/g;
const WINDOWS_RUN = /[^\s"'`<>|(){}[\],;:*?\u2500-\u257f\u3000-\u303f\uff00-\uff60]+/g;
const COLON_LOCATION = /^:(\d{1,7})(?::(\d{1,5}))?(?!\d)/;
const PAREN_LOCATION = /^\((\d{1,7})(?:,\s?(\d{1,5}))?\)/;
const PYTHON_FRAME = /File "([^"\n]{1,1024})", line (\d{1,7})/g;
const DIFF_HEADER = /^(?:diff --git |--- |\+\+\+ )/;
const MAX_PATH_LENGTH = 1024;

// Extensions a bare `name.ext:line` must carry to count as a file.
const KNOWN_EXTENSIONS = new Set(
  (
    "go mod ts tsx mts cts js jsx mjs cjs vue svelte astro py pyi rs c h cc cpp cxx hh hpp hxx m mm cs fs vb java kt kts " +
    "scala groovy gradle swift dart rb erb php pl pm lua r jl ex exs erl hrl hs elm clj zig nim v sv vhd " +
    "css scss sass less html htm xml xaml json jsonc json5 yaml yml toml ini cfg conf properties env " +
    "md mdx rst txt log csv tsv sh bash zsh fish ps1 psm1 bat cmd sql graphql gql proto tf tfvars hcl nix " +
    "dockerfile mk cmake lock patch diff ipynb tex"
  ).split(" "),
);

// Top-level directories that make a one-segment absolute path (`/etc`) plausible.
const ROOT_DIRECTORIES = new Set(
  "etc usr var tmp home opt srv root dev proc sys mnt media bin sbin lib lib64 boot run nix data workspace Users Volumes Applications Library private".split(" "),
);

const EXTENSION = /\.([A-Za-z0-9_+-]{1,12})$/;

function extensionOf(name: string): string | null {
  const m = EXTENSION.exec(name);
  return m && /[A-Za-z]/.test(m[1]) ? m[1] : null;
}

function lastSegment(path: string): string {
  const parts = path.split(/[\\/]/).filter(Boolean);
  return parts[parts.length - 1] ?? "";
}

function hasLetter(text: string): boolean {
  return /[A-Za-z\u00c0-\uffff]/.test(text);
}

/** Whether `path` (without location) reads as a file path rather than prose, a fraction or a date. */
export function looksLikePath(path: string, hasLocation: boolean, options: PathDetectOptions): boolean {
  if (path.length < 2 || path.length > MAX_PATH_LENGTH || !hasLetter(path)) return false;
  const sep = options.windows ? /[\\/]/ : /\//;
  if (/^[\\/]{2}/.test(path)) return false;
  if (options.windows && /^[A-Za-z]:[\\/]/.test(path)) return path.length > 3;
  const home = options.windows ? /^~[\\/]/ : /^~\//;
  if (home.test(path)) return path.length > 2;
  const dotted = options.windows ? /^\.{1,2}[\\/]/ : /^\.{1,2}\//;
  if (dotted.test(path)) return options.relative && path.replace(dotted, "").length > 0 && /[^./\\]/.test(path);
  if (path.startsWith("/")) {
    const segments = path.split("/").filter(Boolean);
    if (segments.length === 0) return false;
    if (segments.length >= 2) return true;
    return hasLocation || extensionOf(segments[0]) !== null || ROOT_DIRECTORIES.has(segments[0]);
  }
  if (!options.relative) return false;
  if (sep.test(path)) {
    if (/^[\\/]|[\\/]{2}/.test(path)) return false;
    return hasLocation || extensionOf(lastSegment(path)) !== null;
  }
  const ext = extensionOf(path);
  return hasLocation && ext !== null && KNOWN_EXTENSIONS.has(ext.toLowerCase()) && path.length > ext.length + 1;
}

function locationAt(text: string, from: number): { line?: number; col?: number; length: number } {
  const rest = text.slice(from);
  const m = COLON_LOCATION.exec(rest) ?? PAREN_LOCATION.exec(rest);
  if (!m) return { length: 0 };
  const line = Number(m[1]);
  if (line < 1) return { length: 0 };
  const col = m[2] === undefined ? undefined : Number(m[2]);
  return { line, ...(col !== undefined && col >= 1 ? { col } : {}), length: m[0].length };
}

const BOUNDARY = /[\s"'`<>|(){}[\],;=\u2500-\u257f\u3000-\u303f\uff00-\uff60]/;

/** Finds the file paths in one logical line of terminal text. */
export function findPathsInLine(text: string, options: PathDetectOptions): PathMatch[] {
  if (!/[./\\]/.test(text)) return [];
  const found: PathMatch[] = [];
  const taken: Array<[number, number]> = [];
  const free = (start: number, end: number) => !taken.some(([s, e]) => start < e && s < end);
  for (const m of text.matchAll(PYTHON_FRAME)) {
    const path = m[1];
    const start = m.index + 'File "'.length;
    if (/[<>]/.test(path) || !looksLikePath(path, true, options)) continue;
    found.push({ path, line: Number(m[2]), text: path, start, end: start + path.length });
    taken.push([m.index, m.index + m[0].length]);
  }
  const run = options.windows ? WINDOWS_RUN : POSIX_RUN;
  const diff = DIFF_HEADER.test(text);
  for (const m of text.matchAll(run)) {
    let start = m.index;
    let body = m[0];
    const before = text[start - 1];
    if (before === ":") {
      const drive = text[start - 2];
      const ahead = text[start - 3];
      if (options.windows && drive && /[A-Za-z]/.test(drive) && (ahead === undefined || BOUNDARY.test(ahead)) && /^[\\/]/.test(body)) {
        start -= 2;
        body = text.slice(start, m.index) + body;
      } else if (start >= 2 && /[\w.@\]-]/.test(text[start - 2])) {
        // host:/path (scp), scheme:/path and port:/path name something other than this machine's files.
        continue;
      }
    }
    let end = start + body.length;
    while (end > start && ".!".includes(text[end - 1])) end--;
    if (end === start) continue;
    let pathStart = start;
    if (diff && /^[ab]\//.test(text.slice(start, end))) pathStart += 2;
    const path = text.slice(pathStart, end);
    // A trailing dot is sentence punctuation unless a location follows it directly.
    const location = end === start + body.length ? locationAt(text, end) : { length: 0 };
    if (!looksLikePath(path, location.length > 0, options)) continue;
    const matchEnd = end + location.length;
    if (!free(pathStart, matchEnd)) continue;
    found.push({
      path,
      ...(location.line !== undefined ? { line: location.line } : {}),
      ...(location.col !== undefined ? { col: location.col } : {}),
      text: text.slice(pathStart, matchEnd),
      start: pathStart,
      end: matchEnd,
    });
    taken.push([pathStart, matchEnd]);
  }
  return found.sort((a, b) => a.start - b.start);
}

function overlapsUrl(segments: LinkRowSegment[], urls: StitchedLink[]): boolean {
  return segments.some((a) => urls.some((u) => u.segments.some((s) => s.row === a.row && a.start < s.end && s.start < a.end)));
}

/** Path links in a window of rows; soft-wrapped rows are joined, and text already detected as a URL is skipped. */
export function findPathLinksInRows(rows: TextRow[], cols: number, options: PathDetectOptions, urls?: StitchedLink[]): PathLink[] {
  const urlLinks = urls ?? findLinksInRows(rows, cols);
  const out: PathLink[] = [];
  buildLogicalLines(rows).forEach((line, index) => {
    for (const match of findPathsInLine(line.text, options)) {
      const segments = segmentsOf(line, match.start, match.end, index);
      if (segments.length === 0 || overlapsUrl(segments, urlLinks)) continue;
      const { start: _start, end: _end, ...rest } = match;
      out.push({ ...rest, segments });
    }
  });
  return out;
}

export interface PathResolveContext {
  /** The shell's working directory (OSC 7 path), or null when unknown. */
  cwd: string | null;
  home?: string | null;
  windows: boolean;
}

/** Normalizes an OSC 7 path: `/C:/dir` is the Windows drive path `C:/dir`. */
export function normalizeCwd(path: string): string {
  return /^\/[A-Za-z]:(\/|$)/.test(path) ? path.slice(1) : path;
}

function joinNormalized(base: string, rel: string): string {
  const drive = /^[A-Za-z]:/.exec(base)?.[0] ?? "";
  const parts: string[] = [];
  for (const part of `${base.slice(drive.length)}/${rel}`.split("/")) {
    if (part === "" || part === ".") continue;
    if (part === "..") parts.pop();
    else parts.push(part);
  }
  return `${drive}/${parts.join("/")}`;
}

/** The absolute path a printed path names, or null when it is relative and the directory is unknown. */
export function resolveTerminalPath(printed: string, context: PathResolveContext): string | null {
  const path = context.windows ? printed.replace(/\\/g, "/") : printed;
  if (context.windows && /^[A-Za-z]:\//.test(path)) return joinNormalized(path.slice(0, 2), path.slice(2));
  if (path.startsWith("/")) return joinNormalized("", path);
  if (path === "~" || path.startsWith("~/")) {
    return context.home ? joinNormalized(normalizeCwd(context.home), path.slice(1)) : null;
  }
  if (!context.cwd) return null;
  return joinNormalized(normalizeCwd(context.cwd), path);
}
