import { formatQuickConnectTarget, type QuickConnectTarget } from "../quickConnectTarget";
import type {
  ConnectionProblemInput,
  UiState,
  Workspace,
  WorkspaceProblem,
  WorkspaceProblemInput,
  WorkspaceRuntime,
} from "./types";

let workspaceProblemSeq = 0;
let workspaceProblemResetGeneration = 0;

export const SQL_EXECUTION_HISTORY_LIMIT = 50;
const SQL_EXECUTION_TEXT_BYTES = 32 * 1024;
export const WORKSPACE_PROBLEM_LIMIT = 50;
const WORKSPACE_PROBLEM_DETAIL_BYTES = 16 * 1024;
const PROBLEM_REDACTED = "[REDACTED]";
const PRIVATE_KEY_BLOCK = /-----BEGIN [^-]+-----[\s\S]*?-----END [^-]+-----/g;
const BEARER_TOKEN = /\b(Bearer\s+)([^\s]+)/gi;
const SENSITIVE_ASSIGNMENT =
  /(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential|authorization)(\s*["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)/gi;
const SENSITIVE_CLI_OPTION =
  /((?:--(?:password|passwd|pwd|secret|token|api-key|access-key)|-p)(?:=|\s+)?)("[^"]*"|'[^']*'|[^\s,;]+)/gi;
const ENV_ASSIGNMENT = /^(\s*(?:export\s+)?)([A-Za-z_][A-Za-z0-9_]*)(\s*=\s*)(.*)$/gm;
const SENSITIVE_ENV_NAME = /password|passwd|pwd|secret|token|credential|authorization|api[_-]?key|access[_-]?key|private[_-]?key/i;

function isWhitespace(char: string): boolean {
  // Full JavaScript \s semantics, including Unicode spaces such as NBSP.
  return /\s/.test(char);
}

// Linear replacement for the old quadratic URL credential pattern: scheme positions are found
// with indexOf, and the scheme token is the longest suffix of the contiguous scheme-character
// run that starts at a letter — the old unanchored regex matched valid scheme suffixes after
// digits and punctuation too (1http://, .http://, -http://, +http:// all redacted).
function redactUrlCredentials(text: string): string {
  let result = "";
  let last = 0;
  let searchFrom = 0;
  for (;;) {
    const schemeEnd = text.indexOf("://", searchFrom);
    if (schemeEnd < 0) break;
    let runStart = schemeEnd - 1;
    while (runStart >= 0 && /[a-z0-9+.-]/i.test(text[runStart] as string)) runStart--;
    runStart++;
    let schemeStart = runStart;
    while (schemeStart < schemeEnd && !/[a-z]/i.test(text[schemeStart] as string)) schemeStart++;
    if (schemeStart >= schemeEnd) {
      searchFrom = schemeEnd + 3;
      continue;
    }
    const userStart = schemeEnd + 3;
    let colon = -1;
    for (let i = userStart; i < text.length; i++) {
      const c = text[i] as string;
      if (c === ":") {
        colon = i;
        break;
      }
      if (c === "/" || c === "@" || isWhitespace(c)) break;
    }
    if (colon < 0) {
      searchFrom = schemeEnd + 3;
      continue;
    }
    let at = -1;
    for (let i = colon + 1; i < text.length; i++) {
      const c = text[i] as string;
      if (c === "@") {
        at = i;
        break;
      }
      if (c === "/" || isWhitespace(c)) break;
    }
    if (at < 0) {
      searchFrom = schemeEnd + 3;
      continue;
    }
    result += text.slice(last, colon + 1) + PROBLEM_REDACTED;
    last = at;
    searchFrom = at;
  }
  return result + text.slice(last);
}

// Linear replacement for the old quadratic env-assignment pattern: each maximal identifier run is
// visited once, and a match is only possible at its first character. Values follow the original
// quoted/unquoted alternatives, including empty quoted strings.
function redactEnvAssignments(text: string): string {
  let result = "";
  let last = 0;
  let i = 0;
  while (i < text.length) {
    const start = text[i] as string;
    if (!/[A-Za-z_]/.test(start)) {
      i++;
      continue;
    }
    let j = i + 1;
    while (j < text.length && /[A-Za-z0-9_]/.test(text[j] as string)) j++;
    let k = j;
    while (k < text.length && isWhitespace(text[k] as string)) k++;
    if (text[k] !== "=") {
      i = j;
      continue;
    }
    k++;
    while (k < text.length && isWhitespace(text[k] as string)) k++;
    const quote = text[k];
    let end = k;
    if (quote === "\"" || quote === "'") {
      const close = text.indexOf(quote, k + 1);
      if (close >= 0) {
        end = close + 1;
      } else {
        // No closing quote: the original unquoted alternative takes over from the opening
        // quote and stops at the first whitespace/comma/semicolon, never consuming to EOF.
        while (end < text.length && !/[\s,;]/.test(text[end] as string)) end++;
      }
    } else {
      while (end < text.length && !/[\s,;]/.test(text[end] as string)) end++;
    }
    if (end === k) {
      i = j;
      continue;
    }
    result += text.slice(last, k) + PROBLEM_REDACTED;
    last = end;
    i = end;
  }
  return result + text.slice(last);
}

function boundedUtf8(text: string, maxBytes: number): { text: string; truncated: boolean } {
  let bytes = 0;
  let end = 0;
  for (const char of text) {
    const codePoint = char.codePointAt(0) as number;
    const charBytes = codePoint <= 0x7f ? 1 : codePoint <= 0x7ff ? 2 : codePoint <= 0xffff ? 3 : 4;
    if (bytes + charBytes > maxBytes) break;
    bytes += charBytes;
    end += char.length;
  }
  const truncated = end !== text.length;
  return { text: truncated ? text.slice(0, end) : text, truncated };
}

export function boundedSqlExecutionText(text: string): { text: string; truncated: boolean } {
  return boundedUtf8(text, SQL_EXECUTION_TEXT_BYTES);
}

export function getWorkspaceProblemResetGeneration(): number {
  return workspaceProblemResetGeneration;
}

export function sanitizeWorkspaceProblemText(text: string): string {
  const allEnv = text.toLowerCase().includes(".env");
  const sanitized = redactUrlCredentials(
    text
      .replace(PRIVATE_KEY_BLOCK, PROBLEM_REDACTED)
      .replace(BEARER_TOKEN, (_match, prefix: string) => `${prefix}${PROBLEM_REDACTED}`)
      .replace(
        SENSITIVE_ASSIGNMENT,
        (_match, name: string, separator: string) => `${name}${separator}${PROBLEM_REDACTED}`,
      )
      .replace(
        SENSITIVE_CLI_OPTION,
        (_match, option: string) => `${option}${PROBLEM_REDACTED}`,
      ),
  );
  if (allEnv) {
    return redactEnvAssignments(sanitized);
  }
  return sanitized.replace(
    ENV_ASSIGNMENT,
    (line, prefix: string, name: string, separator: string) =>
      SENSITIVE_ENV_NAME.test(name)
        ? `${prefix}${name}${separator}${PROBLEM_REDACTED}`
        : line,
  );
}

export function makeWorkspaceProblem(problem: WorkspaceProblemInput): WorkspaceProblem {
  const detail = boundedUtf8(sanitizeWorkspaceProblemText(problem.detail), WORKSPACE_PROBLEM_DETAIL_BYTES);
  const sql = problem.sql === undefined ? undefined : boundedSqlExecutionText(problem.sql);
  const connection = problem.connection
    ? problem.connection.kind === "asset"
      ? { ...problem.connection, asset: { ...problem.connection.asset } }
      : { ...problem.connection, target: { ...problem.connection.target } }
    : undefined;
  return {
    ...problem,
    id: ++workspaceProblemSeq,
    target: {
      ...problem.target,
      title: sanitizeWorkspaceProblemText(problem.target.title),
      identity: sanitizeWorkspaceProblemText(problem.target.identity),
    },
    summary: boundedUtf8(sanitizeWorkspaceProblemText(problem.summary), 240).text,
    detail: detail.text,
    detailTruncated: detail.truncated,
    sql: sql?.text,
    sqlTruncated: sql?.truncated ?? false,
    connection,
  };
}

export function withoutConnectionProblem(
  runtime: Record<string, WorkspaceRuntime>,
  connectionKey: string,
): Record<string, WorkspaceRuntime> {
  let changed = false;
  const next: Record<string, WorkspaceRuntime> = {};
  for (const [workspaceId, entry] of Object.entries(runtime)) {
    if (!entry.problems?.some((problem) => problem.connectionKey === connectionKey)) {
      next[workspaceId] = entry;
      continue;
    }
    changed = true;
    next[workspaceId] = {
      ...entry,
      problems: entry.problems.filter((problem) => problem.connectionKey !== connectionKey),
    };
  }
  return changed ? next : runtime;
}

export function assetConnectionProblemKey(assetId: string): string {
  return `asset:${assetId}`;
}

export function quickConnectionProblemKey(target: QuickConnectTarget): string {
  const normalized = {
    host: target.host.trim().toLowerCase(),
    port: target.port,
    username: target.username?.trim() || null,
  };
  return `quick:${formatQuickConnectTarget(normalized)}`;
}

export function findConnectionWorkspace(
  workspaces: Workspace[],
  problem: ConnectionProblemInput,
): Workspace | undefined {
  if (problem.workspaceId) {
    const explicit = workspaces.find((workspace) => workspace.id === problem.workspaceId);
    if (explicit) return explicit;
  }
  const connection = problem.connection;
  if (connection?.kind === "asset") {
    return workspaces.find((workspace) => workspace.assetId === connection.asset.id);
  }
  if (connection?.kind === "quick") {
    const key = quickConnectionProblemKey(connection.target);
    return workspaces.find(
      (workspace) => workspace.quickTarget && quickConnectionProblemKey(workspace.quickTarget) === key,
    );
  }
  return undefined;
}

type ConnectionProblemState = Pick<UiState, "connectionProblems" | "workspaceRuntime">;

export function selectConnectionProblem(
  state: ConnectionProblemState,
  connectionKey: string,
): WorkspaceProblem | undefined {
  const global = state.connectionProblems[connectionKey];
  if (global) return global;
  for (const runtime of Object.values(state.workspaceRuntime)) {
    const problem = runtime.problems?.find((item) => item.connectionKey === connectionKey);
    if (problem) return problem;
  }
  return undefined;
}

export function latestConnectionProblem(state: ConnectionProblemState): WorkspaceProblem | undefined {
  let latest: WorkspaceProblem | undefined;
  const consider = (problem: WorkspaceProblem) => {
    if (problem.connectionKey && (!latest || problem.id > latest.id)) latest = problem;
  };
  Object.values(state.connectionProblems).forEach(consider);
  for (const runtime of Object.values(state.workspaceRuntime)) {
    runtime.problems?.forEach(consider);
  }
  return latest;
}

export function advanceWorkspaceProblemResetGeneration(): void {
  workspaceProblemResetGeneration += 1;
}
