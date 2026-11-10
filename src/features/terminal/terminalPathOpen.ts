import { fileLinkHostMatches, type TerminalLinkEntry } from "./terminalLinks";
import { resolveTerminalPath } from "./terminalPathLinks";
import { t } from "../../i18n";

export interface PathTarget {
  /** The path as printed. */
  path: string;
  line?: number;
  col?: number;
  /** The working directory when the path was found (OSC 7), and the host that reported it. */
  cwd: string | null;
  cwdHost: string | null;
  windows: boolean;
}

export interface PathOpenDeps {
  sessionId: string;
  // The session host's name, or null when unknown.
  sessionHost: () => string | null | Promise<string | null>;
  homePath: () => Promise<string | null>;
  listDirectory: (sessionId: string, dir: string) => Promise<TerminalLinkEntry[]>;
  isEditableFile: (name: string) => boolean;
  openFile: (sessionId: string, path: string, position?: { line: number; col?: number }) => void;
  openDirectory: (sessionId: string, path: string) => void;
  notify: (kind: "info" | "error", text: string) => void;
  confirm: (message: string, title: string) => Promise<boolean>;
  describeError: (error: unknown) => string;
}

/** `path:line:col` for people, with the location the link carries. */
export function describePathTarget(path: string, line?: number, col?: number): string {
  return `${path}${line ? `:${line}` : ""}${line && col ? `:${col}` : ""}`;
}

function splitParent(path: string): { parent: string; name: string } | null {
  const trimmed = path.replace(/\/+$/, "");
  const index = trimmed.lastIndexOf("/");
  if (index < 0 || trimmed === "" || /^[A-Za-z]:$/.test(trimmed)) return null;
  const name = trimmed.slice(index + 1);
  let parent = index === 0 ? "/" : trimmed.slice(0, index);
  if (/^[A-Za-z]:$/.test(parent)) parent += "/";
  return { parent, name };
}

/**
 * Opens a path found in terminal output in the file editor. This is the only
 * step that touches the file system: the parent directory is listed once to
 * learn whether the file exists and what it is.
 */
export async function openTerminalPath(target: PathTarget, deps: PathOpenDeps): Promise<void> {
  const shown = describePathTarget(target.path, target.line, target.col);
  if (target.cwdHost) {
    const matches = fileLinkHostMatches(target.cwdHost, await deps.sessionHost());
    if (matches === false) {
      deps.notify("info", t("终端当前位于其他主机 {host}，未打开：{shown}", { host: target.cwdHost, shown }));
      return;
    }
    if (matches === null) {
      const ok = await deps.confirm(
        t("终端当前位于主机 {host}，无法确认它就是当前会话的主机。\n\n{shown}\n\n要在当前会话中打开它吗？", { host: target.cwdHost, shown }),
        t("打开文件路径"),
      );
      if (!ok) return;
    }
  }
  const needsHome = target.path === "~" || /^~[\\/]/.test(target.path);
  let home: string | null = null;
  if (needsHome) {
    try {
      home = await deps.homePath();
    } catch {
      home = null;
    }
  }
  const resolved = resolveTerminalPath(target.path, { cwd: target.cwd, home, windows: target.windows });
  if (!resolved) {
    deps.notify("info", t("无法确定 {shown} 所在的目录", { shown }));
    return;
  }
  const split = splitParent(resolved);
  if (!split) {
    deps.openDirectory(deps.sessionId, resolved);
    return;
  }
  let entries: TerminalLinkEntry[];
  try {
    entries = await deps.listDirectory(deps.sessionId, split.parent);
  } catch (error) {
    deps.notify("error", t("无法打开 {path}：{describeError}", { path: resolved, describeError: deps.describeError(error) }));
    return;
  }
  const same = target.windows
    ? (a: string) => a.toLowerCase() === split.name.toLowerCase()
    : (a: string) => a === split.name;
  const entry = entries.find((candidate) => same(candidate.name));
  if (!entry) {
    deps.notify("error", t("文件不存在：{path}", { path: resolved }));
    return;
  }
  const path = entry.path || resolved;
  if (entry.kind === "dir") {
    deps.openDirectory(deps.sessionId, path);
    return;
  }
  if (!deps.isEditableFile(entry.name)) {
    deps.notify("info", t("「{name}」不是文本文件，不能在线编辑；可以下载到本机后打开", { name: entry.name }));
    return;
  }
  deps.openFile(deps.sessionId, path, target.line ? { line: target.line, ...(target.col ? { col: target.col } : {}) } : undefined);
}
