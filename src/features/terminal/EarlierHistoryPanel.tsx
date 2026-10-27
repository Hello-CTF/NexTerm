import { useCallback, useEffect, useRef, useState } from "react";
import { Terminal, type IDisposable } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";

import { useTerminalAtTop } from "../../app/terminalViewStore";
import { terminalApi } from "../../ipc/commands";
import type { TerminalHistoryChunk } from "../../ipc/types";
import { describeError } from "../../ui/errorText";
import { IconAlert, IconArrowUp, IconClose, IconHistory, IconRefresh } from "../../ui/icons";
import {
  applyEarlierHistoryPage,
  canLoadEarlierHistory,
  EARLIER_HISTORY_MAX_BYTES,
  EARLIER_HISTORY_PAGE_BYTES,
  earlierHistoryBytes,
  earlierHistoryOffer,
  formatHistoryBytes,
  initialEarlierHistory,
  type EarlierHistoryState,
  type TerminalHistoryInfo,
} from "./earlierHistory";

const HISTORY_THEME = {
  background: "#101217",
  foreground: "#c6cbd6",
  cursor: "#101217",
  selectionBackground: "#2c3e5d",
};

// Alternate screen switches (DECSET/DECRST 47, 1047, 1049) are ignored so a
// full-screen program in the history draws into the scrollable buffer instead of
// hiding everything behind an alternate screen.
const ALT_SCREEN_MODES = new Set([47, 1047, 1049]);

type HistoryRead = (tabId: string, before: number, maxBytes?: number) => Promise<TerminalHistoryChunk>;

// The replay terminal: input is disabled, so xterm never emits replies (device
// attributes, cursor reports) and nothing subscribes to its data; no OSC handler
// is registered (clipboard, notifications, titles), links are inert, and the
// bytes never reach the live stream, so unread state does not change.
export function createReplayTerminal(cols: number, rows: number): { terminal: Terminal; dispose: () => void } {
  const terminal = new Terminal({
    cols,
    rows,
    disableStdin: true,
    cursorBlink: false,
    scrollback: 100_000,
    fontSize: 12,
    theme: HISTORY_THEME,
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

export function EarlierHistoryPanel({
  tabId,
  end,
  cols,
  rows,
  onClose,
  read = terminalApi.historyRead,
}: {
  tabId: string;
  // Helper offset of the live terminal's first rendered byte: history ends here.
  end: number;
  cols: number;
  rows: number;
  onClose: () => void;
  read?: HistoryRead;
}) {
  const [state, setState] = useState<EarlierHistoryState>(() => initialEarlierHistory(end));
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
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
      const chunk = await read(tabId, current.start, EARLIER_HISTORY_PAGE_BYTES);
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
  // so the view stays on the same output after a page lands on top.
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
      if (first) {
        terminal.scrollToBottom();
        return;
      }
      terminal.scrollToLine(Math.max(0, terminal.buffer.active.length - keepFromBottom));
    });
  }, [state]);

  useEffect(() => {
    loadButtonRef.current?.focus();
  }, []);

  const canLoad = canLoadEarlierHistory(state) && !loading;
  let status: string;
  if (error) status = `加载失败：${error}`;
  else if (loading && state.bytes === 0) status = "正在加载更早历史…";
  else if (state.truncated) status = "早期历史已截断：更早的输出超出持久终端历史上限，已被丢弃";
  else if (state.capped) status = `已加载 ${formatHistoryBytes(state.bytes)}，达到单次查看上限`;
  else if (state.exhausted) status = state.bytes === 0 ? "没有更早的输出" : "已到会话开头";
  else status = `已加载 ${formatHistoryBytes(state.bytes)}`;

  return (
    <div
      role="dialog"
      aria-label="更早历史"
      data-testid="earlier-history"
      className="absolute inset-0 z-20 flex min-h-0 flex-col bg-neutral-950"
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          event.stopPropagation();
          onClose();
        }
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
        <button type="button" className="nx-icon-btn nx-icon-btn-sm" aria-label="关闭更早历史" title="关闭" onClick={onClose}>
          <IconClose size={12} />
        </button>
      </div>
      <div className="relative min-h-0 flex-1 overflow-hidden" style={{ background: HISTORY_THEME.background }}>
        <div ref={containerRef} className="h-full w-full px-2 py-1" />
      </div>
    </div>
  );
}

// The entry at the top of a durable tab's scrollback: when the live terminal is
// scrolled to the top and recorded output precedes its first rendered byte, it
// offers the earlier history; when the history limit already dropped that output,
// it says so instead.
export function EarlierHistory({
  viewKey,
  tabId,
  info,
  dimensions,
  read,
}: {
  // The pane's stable view key the live terminal reports its scroll position under.
  viewKey: string;
  // The kernel tab, or null before the attach.
  tabId: string | null;
  info: TerminalHistoryInfo;
  dimensions: () => { cols: number; rows: number } | undefined;
  read?: HistoryRead;
}) {
  const atTop = useTerminalAtTop(viewKey);
  const [open, setOpen] = useState<{ tabId: string; end: number; cols: number; rows: number } | null>(null);
  const offer = earlierHistoryOffer(info);
  if (open && open.tabId === tabId) {
    return (
      <EarlierHistoryPanel
        tabId={open.tabId}
        end={open.end}
        cols={open.cols}
        rows={open.rows}
        read={read}
        onClose={() => setOpen(null)}
      />
    );
  }
  if (!tabId || !atTop || offer === "none") return null;
  if (offer === "truncated") {
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
        setOpen({ tabId, end: info.from, cols: size?.cols ?? 80, rows: size?.rows ?? 24 });
      }}
    >
      <IconHistory size={12} />
      加载更早历史
    </button>
  );
}
