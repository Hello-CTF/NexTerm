import type { IDisposable, IMarker, Terminal } from "@xterm/xterm";
import { findTab, openFileTab, useUi } from "../../app/store";
import { matchKeybinding } from "../../app/keybindings";
import { getTerminalPrefs } from "../../app/preferences";
import { fsApi } from "../../ipc/commands";
import { copyText } from "../../ui/clipboard";
import { describeError } from "../../ui/errorText";
import { isImeKeyEvent } from "../../ui/imeKeyEvent";
import { insertRisk } from "../explorer/snippetInsert";
import { requestFileReveal } from "../files/fileReveal";
import { isEditableFile } from "../files/fileTypes";
import { t } from "../../i18n";
import { terminalLinkDeps } from "./terminalLinkActions";
import { cellMap, type TextRow } from "./terminalLinkProvider";
import { createTerminalLinkController, fileLinkHostMatches } from "./terminalLinks";
import { createTerminalPathLinkProvider } from "./terminalPathLinkProvider";
import { normalizeCwd, resolveTerminalPath, type PathDetectOptions, type PathLink } from "./terminalPathLinks";
import { describePathTarget, openTerminalPath, type PathOpenDeps, type PathTarget } from "./terminalPathOpen";
import {
  createQuickSelectInput,
  findQuickSelectMatches,
  labelQuickSelectMatches,
  quoteForShell,
  type QuickSelectShell,
  type LabeledMatch,
} from "./quickSelect";
import "./quickSelect.css";

// Path links and quick select for one terminal view. Both read the shell's
// working directory from OSC 7 as the view parses it, so a relative path is
// resolved against the directory current when it is found.

export interface TerminalTargetsOptions {
  sessionId: string;
  containerId?: string;
  /** The view's stable tab key (the workspace store tab id). */
  viewKey: () => string;
  /** Types text at the prompt as a paste, without Enter. */
  insert: (text: string) => void;
}

export interface TerminalTargets {
  startQuickSelect(): void;
  dispose(): void;
}

const quickSelectStarters = new Map<string, () => void>();

/** Starts quick select in the terminal view with this store tab id (from a menu, for touch screens). */
export function startTerminalQuickSelect(viewKey: string): boolean {
  const start = quickSelectStarters.get(viewKey);
  start?.();
  return start !== undefined;
}

const MAX_REMEMBERED_TARGETS = 64;
const MAX_CWD_MARKS = 200;

function terminalIsWindows(sessionId: string, viewKey: string): boolean {
  if (useUi.getState().sessions.find((session) => session.id === sessionId)?.kind === "winrm") return true;
  const shell = findTab(viewKey)?.tab.shell;
  return shell === "powershell" || shell === "cmd";
}

export function attachTerminalTargets(term: Terminal, host: HTMLElement, options: TerminalTargetsOptions): TerminalTargets {
  // The first OSC 7 host is the login shell's (see XtermView); the latest
  // report says where the shell is now, which a nested ssh changes.
  let firstHost: string | null = null;
  let cwd: string | null = null;
  let cwdHost: string | null = null;
  let knownSessionHost: string | null | undefined;
  let linkDepsValue: ReturnType<typeof terminalLinkDeps> | null = null;
  const linkDeps = () => (linkDepsValue ??= terminalLinkDeps(options.sessionId, options.containerId, () => firstHost));
  const learnSessionHost = () => {
    if (knownSessionHost !== undefined) return;
    knownSessionHost = null;
    void Promise.resolve(linkDeps().sessionHost()).then(
      (name) => {
        knownSessionHost = name;
      },
      () => undefined,
    );
  };
  // The directory in force where each OSC 7 report was printed, so a relative
  // path resolves against where it was printed, not where the shell is now.
  const cwdMarks: Array<{ marker: IMarker; cwd: string | null; cwdHost: string | null }> = [];
  const markCwd = () => {
    if (typeof term.registerMarker !== "function") return;
    const marker = term.registerMarker(0);
    if (!marker) return;
    const last = cwdMarks[cwdMarks.length - 1];
    if (last && last.marker.line === marker.line) {
      last.marker.dispose();
      cwdMarks.pop();
    }
    cwdMarks.push({ marker, cwd, cwdHost });
    while (cwdMarks.length > MAX_CWD_MARKS) cwdMarks.shift()!.marker.dispose();
  };
  /** The directory and host in force at a buffer row; unknown when the row predates every report still tracked. */
  const cwdAt = (row: number): { cwd: string | null; cwdHost: string | null } => {
    if (cwdMarks.length === 0) return { cwd, cwdHost };
    let found: { cwd: string | null; cwdHost: string | null } = { cwd: null, cwdHost: null };
    for (const mark of cwdMarks) {
      if (mark.marker.isDisposed || mark.marker.line < 0) continue;
      if (mark.marker.line <= row) found = mark;
    }
    return found;
  };
  const osc7: IDisposable = term.parser.registerOscHandler(7, (data) => {
    try {
      const url = new URL(data);
      if (url.protocol === "file:") {
        const path = decodeURIComponent(url.pathname);
        cwd = path ? normalizeCwd(path) : null;
        cwdHost = url.hostname || null;
        markCwd();
        firstHost ??= cwdHost;
        if (cwdHost) learnSessionHost();
      }
    } catch {
      // Not a file URL: nothing to learn.
    }
    return false;
  });

  const shellKind = (): QuickSelectShell => {
    const shell = findTab(options.viewKey())?.tab.shell;
    if (shell === "cmd") return "cmd";
    if (shell === "powershell" || useUi.getState().sessions.find((session) => session.id === options.sessionId)?.kind === "winrm") return "powershell";
    return "posix";
  };
  const windows = () => terminalIsWindows(options.sessionId, options.viewKey());
  // The session's file service cannot open a container's files, and paths
  // printed on another host (a nested ssh) do not name this host's files.
  const pathOptions = (): PathDetectOptions | null => {
    if (options.containerId) return null;
    if (cwdHost && knownSessionHost && fileLinkHostMatches(cwdHost, knownSessionHost) === false) return null;
    return { windows: windows(), relative: cwd !== null || cwdMarks.length > 0 };
  };

  const pathDeps: PathOpenDeps = {
    sessionId: options.sessionId,
    sessionHost: () => linkDeps().sessionHost(),
    homePath: async () => (await fsApi.roots(options.sessionId)).homePath,
    listDirectory: (id, dir) => fsApi.list(id, dir),
    isEditableFile,
    openFile: (id, path, position) => {
      if (position) requestFileReveal(id, path, position);
      openFileTab(id, path);
    },
    openDirectory: (id, path) => linkDeps().openDirectory(id, path),
    notify: (kind, text) => linkDeps().notify(kind, text),
    confirm: (message, title) => linkDeps().confirm(message, title),
    describeError,
  };

  const targets = new Map<string, PathTarget>();
  const remember = (link: PathLink, row: number): string => {
    const at = cwdAt(row);
    const target: PathTarget = { path: link.path, line: link.line, col: link.col, cwd: at.cwd, cwdHost: at.cwdHost, windows: windows() };
    const resolved = resolveTerminalPath(link.path, { cwd: at.cwd, windows: target.windows }) ?? link.path;
    const key = describePathTarget(resolved, link.line, link.col);
    targets.delete(key);
    targets.set(key, target);
    while (targets.size > MAX_REMEMBERED_TARGETS) targets.delete(targets.keys().next().value!);
    return key;
  };
  const controller = createTerminalLinkController({
    tooltipHost: () => host,
    cursorHost: host,
    activation: () => getTerminalPrefs().linkActivation,
    mouseTracking: () => term.modes.mouseTrackingMode !== "none",
    open: (key) => {
      const target = targets.get(key);
      if (target) void openTerminalPath(target, pathDeps);
    },
    onInactiveClick: (message) => useUi.getState().pushToast("info", message),
    hasSelection: () => term.hasSelection(),
  });
  const provider =
    typeof term.registerLinkProvider === "function"
      ? term.registerLinkProvider(
          createTerminalPathLinkProvider(term, {
            options: pathOptions,
            // A relative path needs the directory in force where it was printed.
            accept: (link, row) => resolveTerminalPath(link.path, { cwd: cwdAt(row).cwd, home: "/", windows: windows() }) !== null,
            activate: (event, link, row) => controller.webLinkHandler(event, remember(link, row)),
            hover: (event, link, row) => controller.webLinkOptions.hover(event, remember(link, row)),
            leave: (event, link, row) => controller.webLinkOptions.leave(event, remember(link, row)),
          }),
        )
      : null;

  let quick: QuickSelectOverlay | null = null;
  const endQuickSelect = () => {
    quick?.dispose();
    quick = null;
  };
  const startQuickSelect = () => {
    if (quick) {
      endQuickSelect();
      return;
    }
    const snapshot = visibleRows(term);
    const labeled = labelQuickSelectMatches(findQuickSelectMatches(snapshot.rows, term.cols, pathOptions()));
    if (labeled.length === 0) {
      useUi.getState().pushToast("info", t("屏幕上没有可快速选择的内容"));
      return;
    }
    quick = showQuickSelect(term, host, snapshot.first, labeled, (match, insert) => {
      endQuickSelect();
      term.focus();
      if (insert) insertMatch(match.text);
      else copyMatch(match.text);
    }, () => {
      endQuickSelect();
      term.focus();
    });
  };
  const insertMatch = (text: string) => {
    const tab = findTab(options.viewKey())?.tab;
    if (tab?.dead || tab?.exited) {
      useUi.getState().pushToast("info", t("当前终端已断开或已结束"));
      return;
    }
    if (insertRisk(text) !== "none") {
      useUi.getState().pushToast("info", t("内容包含换行或控制字符，未填入终端"));
      return;
    }
    options.insert(quoteForShell(text, shellKind()));
  };
  const copyMatch = (text: string) => {
    void copyText(text).then(
      () => useUi.getState().pushToast("success", t("已复制：{text}", { text: text.length > 80 ? `${text.slice(0, 80)}…` : text })),
      (error: unknown) => useUi.getState().pushToast("error", t("复制失败：{value}", { value: describeError(error) })),
    );
  };

  const onKey = (event: KeyboardEvent) => {
    if (event.repeat || isImeKeyEvent(event) || !matchKeybinding(event, "terminalQuickSelect")) return;
    event.preventDefault();
    event.stopPropagation();
    startQuickSelect();
  };
  host.addEventListener("keydown", onKey, true);
  const scroll = term.onScroll(() => endQuickSelect());
  // Partial terminal doubles in tests may lack onResize.
  const resize = typeof term.onResize === "function" ? term.onResize(() => endQuickSelect()) : null;
  let starterKey = options.viewKey();
  quickSelectStarters.set(starterKey, startQuickSelect);

  return {
    startQuickSelect,
    dispose: () => {
      endQuickSelect();
      host.removeEventListener("keydown", onKey, true);
      scroll.dispose();
      resize?.dispose();
      osc7.dispose();
      for (const mark of cwdMarks) mark.marker.dispose();
      cwdMarks.length = 0;
      provider?.dispose();
      controller.dispose();
      if (quickSelectStarters.get(starterKey) === startQuickSelect) quickSelectStarters.delete(starterKey);
      starterKey = "";
    },
  };
}

function visibleRows(term: Terminal): { first: number; rows: TextRow[] } {
  const buf = term.buffer.active;
  const first = buf.viewportY;
  const rows: TextRow[] = [];
  for (let r = first; r < first + term.rows; r++) {
    const line = buf.getLine(r);
    rows.push({ text: line?.translateToString(true) ?? "", isWrapped: r > first && line?.isWrapped === true });
  }
  return { first, rows };
}

interface QuickSelectOverlay {
  dispose(): void;
}

/**
 * Draws the labels in a DOM layer over the terminal screen, so it is the
 * same over the WebGL canvas and the DOM renderer, and takes the keyboard
 * until a label is chosen or Esc cancels. Labels are buttons for touch.
 */
function showQuickSelect(
  term: Terminal,
  host: HTMLElement,
  firstRow: number,
  matches: LabeledMatch[],
  onSelect: (match: LabeledMatch, insert: boolean) => void,
  onCancel: () => void,
): QuickSelectOverlay {
  const screen = term.element?.querySelector<HTMLElement>(".xterm-screen") ?? term.element ?? host;
  const screenRect = screen.getBoundingClientRect();
  const hostRect = host.getBoundingClientRect();
  const cellWidth = screenRect.width / Math.max(1, term.cols);
  const cellHeight = screenRect.height / Math.max(1, term.rows);
  const originX = screenRect.left - hostRect.left;
  const originY = screenRect.top - hostRect.top;

  const layer = document.createElement("div");
  layer.className = "nx-quick-select xterm-hover";
  layer.setAttribute("role", "dialog");
  layer.setAttribute("aria-label", t("快速选择"));
  const input = createQuickSelectInput(matches);
  let tapInsert = false;

  const bar = document.createElement("div");
  bar.className = "nx-quick-select-bar";
  const hint = document.createElement("span");
  hint.textContent = t("按标签复制，Shift+标签插入到提示符，Esc 取消");
  const mode = document.createElement("button");
  mode.type = "button";
  mode.className = "nx-quick-select-mode";
  const renderMode = () => {
    mode.textContent = tapInsert ? t("点按：插入") : t("点按：复制");
    mode.setAttribute("aria-pressed", tapInsert ? "true" : "false");
  };
  renderMode();
  mode.addEventListener("click", (event) => {
    event.stopPropagation();
    tapInsert = !tapInsert;
    renderMode();
  });
  const close = document.createElement("button");
  close.type = "button";
  close.className = "nx-quick-select-close";
  close.textContent = t("取消");
  close.addEventListener("click", (event) => {
    event.stopPropagation();
    onCancel();
  });
  bar.append(hint, mode, close);

  const labels: Array<{ match: LabeledMatch; element: HTMLButtonElement }> = [];
  const maps = new Map<number, ReturnType<typeof cellMap>>();
  const mapOf = (row: number) => {
    let map = maps.get(row);
    if (!map) {
      const line = term.buffer.active.getLine(firstRow + row);
      map = line ? cellMap(line) : { col: [], width: [] };
      maps.set(row, map);
    }
    return map;
  };
  for (const match of matches) {
    match.segments.forEach((seg, i) => {
      const map = mapOf(seg.row);
      const startCol = map.col[seg.start] ?? seg.start;
      const lastIdx = seg.end - 1;
      const endCol = (map.col[lastIdx] ?? lastIdx) + (map.width[lastIdx] ?? 1);
      const mark = document.createElement("div");
      mark.className = "nx-quick-select-mark";
      mark.style.left = `${originX + startCol * cellWidth}px`;
      mark.style.top = `${originY + seg.row * cellHeight}px`;
      mark.style.width = `${(endCol - startCol) * cellWidth}px`;
      mark.style.height = `${cellHeight}px`;
      layer.append(mark);
      if (i !== 0) return;
      const label = document.createElement("button");
      label.type = "button";
      label.className = "nx-quick-select-label";
      label.textContent = match.label;
      label.title = match.text;
      label.dataset.label = match.label;
      label.style.left = `${originX + startCol * cellWidth}px`;
      label.style.top = `${originY + seg.row * cellHeight}px`;
      label.style.minHeight = `${cellHeight}px`;
      label.addEventListener("click", (event) => {
        event.stopPropagation();
        onSelect(match, tapInsert || event.shiftKey);
      });
      labels.push({ match, element: label });
    });
  }
  for (const { element } of labels) layer.append(element);
  layer.append(bar);
  layer.addEventListener("click", (event) => {
    if (event.target === layer) onCancel();
  });

  const showTyped = (typed: string) => {
    for (const { match, element } of labels) {
      element.dataset.dim = typed && !match.label.startsWith(typed) ? "true" : "false";
      element.dataset.typed = typed && match.label.startsWith(typed) ? "true" : "false";
    }
  };
  const onKey = (event: KeyboardEvent) => {
    if (isImeKeyEvent(event) || ["Shift", "Control", "Meta", "Alt", "CapsLock"].includes(event.key)) return;
    event.preventDefault();
    event.stopPropagation();
    if (matchKeybinding(event, "terminalQuickSelect")) {
      onCancel();
      return;
    }
    const step = input.press(event);
    if (step.kind === "cancel") onCancel();
    else if (step.kind === "select") onSelect(step.match, step.insert);
    else if (step.kind === "pending") showTyped(step.typed);
  };
  window.addEventListener("keydown", onKey, true);
  host.append(layer);
  return {
    dispose: () => {
      window.removeEventListener("keydown", onKey, true);
      layer.remove();
    },
  };
}

