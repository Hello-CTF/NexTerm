import { useCallback, useEffect, useRef, useState } from "react";
import { Terminal, type IDisposable } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";

import { getAppearancePrefs, getResolvedTerminalTheme } from "../../app/preferences";
import { useTerminalHistoryView } from "../../app/terminalViewStore";
import { terminalApi } from "../../ipc/commands";
import type { TerminalHistoryChunk } from "../../ipc/types";
import { copyText } from "../../ui/clipboard";
import { describeError } from "../../ui/errorText";
import { IconAlert, IconArrowUp, IconClose, IconCopy, IconHistory, IconRefresh } from "../../ui/icons";
import {
  applyEarlierHistoryPage,
  canLoadEarlierHistory,
  capEarlierHistoryLines,
  EARLIER_HISTORY_MAX_BYTES,
  EARLIER_HISTORY_PAGE_BYTES,
  EARLIER_HISTORY_SCROLLBACK,
  earlierHistoryBytes,
  earlierHistoryOffer,
  formatHistoryBytes,
  initialEarlierHistory,
  nextEarlierHistoryRead,
  type EarlierHistoryEnd,
  type EarlierHistoryState,
  type TerminalHistoryInfo,
} from "./earlierHistory";
import { TERMINAL_FONT_STACKS, xtermThemeFor } from "./xtermTheme";

// Alternate screen switches (DECSET/DECRST 47, 1047, 1049) are ignored so a
// full-screen program in the history draws into the scrollable buffer instead of
// hiding everything behind an alternate screen.
const ALT_SCREEN_MODES = new Set([47, 1047, 1049]);

export const EARLIER_HISTORY_PANEL_ATTRIBUTE = "data-earlier-history";

type HistoryRead = (tabId: string, before: EarlierHistoryEnd, maxBytes?: number) => Promise<TerminalHistoryChunk>;

// The replay terminal: input is disabled, so xterm never emits replies (device
// attributes, cursor reports) and nothing subscribes to its data; no OSC handler
// is registered (clipboard, notifications, titles), links are inert, and the
// bytes never reach the live stream, so unread state does not change. It uses
// the live terminal's font and theme.
export function createReplayTerminal(cols: number, rows: number): { terminal: Terminal; dispose: () => void } {
  const appearance = getAppearancePrefs();
  const terminal = new Terminal({
    cols,
    rows,
    disableStdin: true,
    cursorBlink: false,
    scrollback: EARLIER_HISTORY_SCROLLBACK,
    fontFamily: TERMINAL_FONT_STACKS[appearance.terminalFontFamily],
    fontSize: appearance.terminalFontSize,
    theme: xtermThemeFor(getResolvedTerminalTheme(), appearance.terminalContrast),
    linkHandler: { activate: () => undefined },
  });
  const ignoreAltScreen = (params: (number | number[])[]) =>
    params.some((param) => typeof param === "number" && ALT_SCREEN_MODES.has(param));
  const handlers: IDisposable[] = [
    terminal.parser.registerCsiHandler({ prefix: "?", final: "h" }, ignoreAltScreen),
    terminal.parser.registerCsiHandler({ prefix: "?", final: "l" }, ignoreAltScreen),
  ];
  return {
    terminal,
    dispose: () => {
      for (const handler of handlers) handler.dispose();
      terminal.dispose();
    },
  };
}

// The replay terminal's scrollback is full: an earlier page would push loaded
// output out of it.
function replayTerminalFull(terminal: Terminal): boolean {
  return terminal.buffer.active.length >= terminal.rows + EARLIER_HISTORY_SCROLLBACK;
}

function isCopyShortcut(event: { key: string; ctrlKey: boolean; metaKey: boolean; altKey: boolean }): boolean {
  return (event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "c";
}

export function EarlierHistoryPanel({
  tabId,
  end,
  overlap,
  cols,
  rows,
  onClose,
  read = terminalApi.historyRead,
}: {
  tabId: string;
  // Where the history ends: the live view's first rendered byte, or the newest
  // output when the view's own scrollback was trimmed.
  end: EarlierHistoryEnd;
  // The history ends at the newest output and repeats what the view shows.
  overlap: boolean;
  cols: number;
  rows: number;
  onClose: () => void;
  read?: HistoryRead;
}) {
  const [state, setState] = useState<EarlierHistoryState>(() => initialEarlierHistory(end));
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copyNote, setCopyNote] = useState<string | null>(null);
  const containerRef = useRef<HTMLDivElement | null>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const stateRef = useRef(state);
  stateRef.current = state;
  const loadingRef = useRef(false);
  const mountedRef = useRef(true);
  const renderedPagesRef = useRef(0);
  const loadButtonRef = useRef<HTMLButtonElement | null>(null);

  useEffect(() => {
    mountedRef.current = true;
    const container = containerRef.current;
    if (!container) return;
    const { terminal, dispose } = createReplayTerminal(Math.max(2, cols), Math.max(1, rows));
    terminal.open(container);
    terminalRef.current = terminal;
    renderedPagesRef.current = 0;
    return () => {
      mountedRef.current = false;
      terminalRef.current = null;
      dispose();
    };
    // The replay terminal keeps the live terminal's size from when it opened.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const loadMore = useCallback(async () => {
    const current = stateRef.current;
    if (loadingRef.current || !canLoadEarlierHistory(current)) return;
    loadingRef.current = true;
    setLoading(true);
    setError(null);
    try {
      const chunk = await read(tabId, nextEarlierHistoryRead(current), EARLIER_HISTORY_PAGE_BYTES);
      if (!mountedRef.current) return;
      const next = applyEarlierHistoryPage(stateRef.current, chunk, EARLIER_HISTORY_MAX_BYTES);
      stateRef.current = next;
      setState(next);
    } catch (cause) {
      if (mountedRef.current) setError(describeError(cause));
    } finally {
      loadingRef.current = false;
      if (mountedRef.current) setLoading(false);
    }
  }, [read, tabId]);

  useEffect(() => {
    void loadMore();
  }, [loadMore]);

  // Earlier pages are prepended by re-rendering the whole loaded range: xterm
  // cannot insert above its scrollback. The lines below the viewport are kept,
  // so the view stays on the same output after a page lands on top. Once the
  // replay terminal's scrollback is full, loading stops.
  useEffect(() => {
    const terminal = terminalRef.current;
    if (!terminal || state.pages.length === renderedPagesRef.current) return;
    const first = renderedPagesRef.current === 0;
    renderedPagesRef.current = state.pages.length;
    const before = terminal.buffer.active;
    const keepFromBottom = first ? 0 : before.length - before.viewportY;
    terminal.reset();
    terminal.write(earlierHistoryBytes(state), () => {
      if (terminalRef.current !== terminal) return;
      if (first) terminal.scrollToBottom();
      else terminal.scrollToLine(Math.max(0, terminal.buffer.active.length - keepFromBottom));
      if (replayTerminalFull(terminal)) {
        const capped = capEarlierHistoryLines(stateRef.current);
        if (capped !== stateRef.current) {
          stateRef.current = capped;
          setState(capped);
        }
      }
    });
  }, [state]);

  useEffect(() => {
    loadButtonRef.current?.focus();
  }, []);

  const copySelection = useCallback(() => {
    const text = terminalRef.current?.getSelection() ?? "";
    if (!text) {
      setCopyNote("没有选中的内容");
      return;
    }
    copyText(text).then(
      () => mountedRef.current && setCopyNote("已复制"),
      (cause: unknown) => mountedRef.current && setCopyNote(`复制失败：${describeError(cause)}`),
    );
  }, []);

  // The copy outcome is feedback about a finished action: success clears sooner than a failure.
  useEffect(() => {
    if (!copyNote) return;
    const timer = window.setTimeout(() => setCopyNote(null), copyNote.startsWith("复制失败") ? 8000 : 3500);
    return () => window.clearTimeout(timer);
  }, [copyNote]);

  const canLoad = canLoadEarlierHistory(state) && !loading;
  let status: string;
  if (error) status = `加载失败：${error}`;
  else if (loading && state.bytes === 0) status = "正在加载更早历史…";
  else if (state.capped === "lines") status = `已加载 ${formatHistoryBytes(state.bytes)}，达到回放行数上限，更早的输出不再加载`;
  else if (state.truncated) status = "早期历史已截断：更早的输出超出持久终端历史上限，已被丢弃";
  else if (state.capped === "bytes") status = `已加载 ${formatHistoryBytes(state.bytes)}，达到单次查看上限`;
  else if (state.exhausted) status = state.bytes === 0 ? "没有更早的输出" : "已到会话开头";
  else status = `已加载 ${formatHistoryBytes(state.bytes)}`;

  // Keys and the context menu stay inside the panel: the live terminal's menu,
  // shortcuts and search act on the live terminal, not on this replay.
  return (
    <div
      role="dialog"
      aria-label="更早历史"
      data-testid="earlier-history"
      {...{ [EARLIER_HISTORY_PANEL_ATTRIBUTE]: "" }}
      className="absolute inset-0 z-20 flex min-h-0 flex-col bg-neutral-950"
      onKeyDown={(event) => {
        // Only the panel's own keys are consumed; every other key still
        // reaches the app-level shortcuts.
        if (event.key === "Escape") {
          event.stopPropagation();
          event.preventDefault();
          onClose();
        } else if (isCopyShortcut(event) && terminalRef.current?.hasSelection()) {
          event.stopPropagation();
          event.preventDefault();
          copySelection();
        }
      }}
      onContextMenu={(event) => {
        event.stopPropagation();
        event.preventDefault();
      }}
    >
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-neutral-800/60 px-3 py-1.5">
        <IconHistory size={13} className="text-neutral-400" />
        <span className="text-[12px] font-semibold text-neutral-200">更早历史</span>
        <span className="nx-badge">只读回放</span>
        <span
          role="status"
          aria-live="polite"
          data-testid="earlier-history-status"
          className={`min-w-0 flex-1 truncate text-[11.5px] ${
            error ? "text-red-300" : state.truncated ? "text-amber-300" : "text-neutral-500"
          }`}
        >
          {error && <IconAlert size={12} className="mr-1 inline" />}
          {status}
        </span>
        <button
          ref={loadButtonRef}
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm"
          disabled={!canLoad}
          onClick={() => void loadMore()}
        >
          {loading ? <IconRefresh size={12} className="animate-spin" /> : <IconArrowUp size={12} />}
          {error ? "重试" : "加载更早"}
        </button>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={copySelection}>
          <IconCopy size={12} />
          复制所选
        </button>
        <button type="button" className="nx-icon-btn nx-icon-btn-sm" aria-label="关闭更早历史" title="关闭" onClick={onClose}>
          <IconClose size={12} />
        </button>
      </div>
      <div className="relative min-h-0 flex-1 overflow-hidden">
        <div ref={containerRef} className="h-full w-full px-2 py-1" />
      </div>
      {(overlap || copyNote) && (
        <div className="flex shrink-0 gap-3 border-t border-neutral-800/60 px-3 py-1 text-[11px] text-neutral-500">
          {overlap && <span data-testid="earlier-history-overlap">可能与当前终端内容有重叠</span>}
          {copyNote && <span role="status">{copyNote}</span>}
        </div>
      )}
    </div>
  );
}

// The entry at the top of a durable tab's scrollback: when the live terminal is
// scrolled to the top and recorded output precedes what it holds, it offers the
// earlier history; when the history limit already dropped that output, it says
// so instead.
export function EarlierHistory({
  viewKey,
  tabId,
  info,
  dimensions,
  onClosed,
  read,
}: {
  // The pane's stable view key the live terminal reports its history view under.
  viewKey: string;
  // The kernel tab, or null before the attach.
  tabId: string | null;
  info: TerminalHistoryInfo;
  dimensions: () => { cols: number; rows: number } | undefined;
  // Called after the panel closes, to return focus to the live terminal.
  onClosed?: () => void;
  read?: HistoryRead;
}) {
  const view = useTerminalHistoryView(viewKey);
  const [open, setOpen] = useState<{
    tabId: string;
    end: EarlierHistoryEnd;
    overlap: boolean;
    cols: number;
    rows: number;
  } | null>(null);
  useEffect(() => {
    setOpen(null);
  }, [tabId]);
  if (open && open.tabId === tabId) {
    return (
      <EarlierHistoryPanel
        tabId={open.tabId}
        end={open.end}
        overlap={open.overlap}
        cols={open.cols}
        rows={open.rows}
        read={read}
        onClose={() => {
          setOpen(null);
          onClosed?.();
        }}
      />
    );
  }
  const offer = earlierHistoryOffer(info, view);
  if (!tabId || !view.atTop || offer.kind === "none") return null;
  if (offer.kind === "truncated") {
    return (
      <div
        role="status"
        data-testid="earlier-history-entry"
        className="pointer-events-none absolute left-1/2 top-2 z-10 max-w-[92%] -translate-x-1/2 rounded-md border border-amber-400/30 bg-neutral-900/95 px-3 py-1 text-[11.5px] text-amber-300 shadow-lg"
      >
        早期历史已截断：更早的输出超出持久终端历史上限，已被丢弃
      </div>
    );
  }
  return (
    <button
      type="button"
      data-testid="earlier-history-entry"
      className="nx-btn nx-btn-sm pointer-coarse:min-h-11 absolute left-1/2 top-2 z-10 -translate-x-1/2 border border-neutral-700/80 bg-neutral-800/95 text-neutral-100 shadow-lg"
      onClick={() => {
        const size = dimensions();
        setOpen({ tabId, end: offer.end, overlap: offer.overlap, cols: size?.cols ?? 80, rows: size?.rows ?? 24 });
      }}
    >
      <IconHistory size={12} />
      加载更早历史
    </button>
  );
}
