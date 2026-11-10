import type { IBuffer, IDecoration, IDisposable, IMarker, Terminal } from "@xterm/xterm";
import { getResolvedTerminalTheme, subscribeAppearancePrefs } from "../../app/preferences";
import { cellMap } from "./terminalLinkProvider";
import {
  compileKeywordHighlights,
  getKeywordHighlights,
  loadedRegexEngine,
  loadRegexEngine,
  subscribeKeywordHighlights,
  type KeywordHighlightSettings,
  highlightBackground,
  highlightForeground,
  matchKeywordHighlights,
  type CompiledHighlight,
  type HighlightStyle,
} from "./keywordHighlightRules";

// Keyword highlighting as xterm decorations. Only the rows in the viewport (extended to whole soft-wrapped
// lines) are scanned, after output settles (debounced), and lines whose text did not change keep their
// decorations. Decorations ride on markers, so xterm drops them with their rows when the scrollback trims or the
// buffer is cleared or reset. A burst of output (yes, cat of a large file) pauses scanning until it quiets down.
// The alternate screen (full-screen programs) is not highlighted.

export const HIGHLIGHT_DEBOUNCE_MS = 60;
/** More line feeds than this within BURST_WINDOW_MS pause highlighting. */
export const BURST_LINES = 400;
export const BURST_WINDOW_MS = 250;
/** Highlighting resumes after this long without a burst. */
export const BURST_QUIET_MS = 400;
/** Decorations one terminal holds at most. */
export const MAX_HIGHLIGHT_DECORATIONS = 400;
/** Rows of one soft-wrapped line looked at beyond the viewport. */
const MAX_WRAP_ROWS = 64;

export interface KeywordHighlightOptions {
  rules: () => readonly CompiledHighlight[];
  theme: () => "dark" | "light";
  /** Notifies when the rules or the theme change. */
  subscribe?: (listener: () => void) => () => void;
  /** Hidden terminals are not scanned; refresh() after showing one. */
  visible?: () => boolean;
  now?: () => number;
}

export interface RenderedHighlight {
  line: number;
  x: number;
  width: number;
  foreground?: string;
  background?: string;
  underline: boolean;
}

export interface KeywordHighlighter {
  /** Schedules a scan of the viewport. */
  refresh(): void;
  /** Scans now; tests use it instead of waiting for the debounce. */
  scanNow(): void;
  /** Whether a burst of output has paused highlighting. */
  paused(): boolean;
  /** The decorations currently held, for tests and diagnostics. */
  rendered(): RenderedHighlight[];
  dispose(): void;
}

interface Entry {
  marker: IMarker;
  signature: string;
  markers: IMarker[];
  decorations: { decoration: IDecoration; info: RenderedHighlight }[];
}

const NOOP: KeywordHighlighter = {
  refresh() {},
  scanNow() {},
  paused: () => false,
  rendered: () => [],
  dispose() {},
};

const UNDERLINE_DEFAULT = { dark: "#e5e5e5", light: "#262626" } as const;

export function attachKeywordHighlight(term: Terminal, options: KeywordHighlightOptions): KeywordHighlighter {
  // Partial terminal doubles in tests have no decoration support.
  if (typeof term.registerDecoration !== "function" || typeof term.registerMarker !== "function") return NOOP;
  const now = options.now ?? (() => Date.now());
  const disposables: IDisposable[] = [];
  let entries: Entry[] = [];
  let timer: ReturnType<typeof setTimeout> | null = null;
  let disposed = false;
  let generation = 0;
  let burstStart = 0;
  let burstLines = 0;
  let pausedUntil = 0;
  let isPaused = false;

  const disposeEntry = (entry: Entry) => {
    for (const { decoration } of entry.decorations) decoration.dispose();
    for (const marker of entry.markers) marker.dispose();
  };
  const clear = () => {
    for (const entry of entries) disposeEntry(entry);
    entries = [];
  };

  const schedule = (delay = HIGHLIGHT_DEBOUNCE_MS) => {
    if (disposed || timer !== null) return;
    timer = setTimeout(() => {
      timer = null;
      run();
    }, delay);
  };

  const run = () => {
    if (disposed) return;
    if (isPaused) {
      const wait = pausedUntil - now();
      if (wait > 0) {
        schedule(wait);
        return;
      }
      isPaused = false;
    }
    if (options.visible && !options.visible()) return;
    scan();
  };

  const markerAt = (buffer: IBuffer, line: number): IMarker | undefined =>
    term.registerMarker(line - (buffer.baseY + buffer.cursorY));

  const scan = () => {
    const buffer = term.buffer.active;
    const rules = options.rules();
    if (buffer.type !== "normal" || rules.length === 0) {
      clear();
      return;
    }
    const theme = options.theme();
    const top = buffer.viewportY;
    const bottom = Math.min(buffer.length - 1, top + term.rows - 1);
    let first = top;
    while (first > 0 && top - first < MAX_WRAP_ROWS && buffer.getLine(first)?.isWrapped) first--;
    const byLine = new Map<number, Entry>();
    for (const entry of entries) {
      if (entry.marker.isDisposed || entry.marker.line < 0) disposeEntry(entry);
      else byLine.set(entry.marker.line, entry);
    }
    const kept: Entry[] = [];
    let budget = MAX_HIGHLIGHT_DECORATIONS;
    for (let y = first; y <= bottom; ) {
      const rows: string[] = [];
      let end = y;
      for (;;) {
        rows.push(buffer.getLine(end)?.translateToString(true) ?? "");
        if (end + 1 >= buffer.length || end + 1 - y >= MAX_WRAP_ROWS || !buffer.getLine(end + 1)?.isWrapped) break;
        end++;
      }
      const signature = `${generation}\u0000${rows.join("\u0000")}`;
      const existing = byLine.get(y);
      byLine.delete(y);
      if (existing && existing.signature === signature) {
        kept.push(existing);
        budget -= existing.decorations.length;
      } else {
        if (existing) disposeEntry(existing);
        const entry = budget > 0 ? decorate(buffer, y, rows, rules, theme, signature, budget) : null;
        if (entry) {
          kept.push(entry);
          budget -= entry.decorations.length;
        }
      }
      y = end + 1;
    }
    // Lines that left the viewport give their decorations back.
    for (const entry of byLine.values()) disposeEntry(entry);
    entries = kept;
  };

  const decorate = (
    buffer: IBuffer,
    y: number,
    rows: string[],
    rules: readonly CompiledHighlight[],
    theme: "dark" | "light",
    signature: string,
    budget: number,
  ): Entry | null => {
    const matches = matchKeywordHighlights(rows.join(""), rules);
    if (matches.length === 0) return null;
    const head = markerAt(buffer, y);
    if (!head) return null;
    const entry: Entry = { marker: head, signature, markers: [head], decorations: [] };
    const rowMarkers = new Map<number, IMarker>([[0, head]]);
    const maps = new Map<number, ReturnType<typeof cellMap>>();
    let offset = 0;
    for (let slot = 0; slot < rows.length && entry.decorations.length < budget; slot++) {
      const rowStart = offset;
      const rowEnd = offset + rows[slot].length;
      offset = rowEnd;
      for (const match of matches) {
        const start = Math.max(match.start, rowStart);
        const stop = Math.min(match.end, rowEnd);
        if (stop <= start) continue;
        let map = maps.get(slot);
        if (!map) {
          const line = buffer.getLine(y + slot);
          if (!line) break;
          map = cellMap(line);
          maps.set(slot, map);
        }
        const x = map.col[start - rowStart] ?? start - rowStart;
        const last = stop - rowStart - 1;
        const width = (map.col[last] ?? last) + (map.width[last] ?? 1) - x;
        if (width <= 0) continue;
        let marker = rowMarkers.get(slot);
        if (!marker) {
          marker = markerAt(buffer, y + slot);
          if (!marker) continue;
          rowMarkers.set(slot, marker);
          entry.markers.push(marker);
        }
        const rendered = register(marker, x, width, match.style, theme);
        if (rendered) entry.decorations.push(rendered);
        if (entry.decorations.length >= budget) break;
      }
    }
    if (entry.decorations.length === 0) {
      disposeEntry(entry);
      return null;
    }
    return entry;
  };

  const register = (marker: IMarker, x: number, width: number, style: HighlightStyle, theme: "dark" | "light") => {
    const foreground = style.foreground ? highlightForeground(style.foreground, theme) : undefined;
    const background = style.background ? highlightBackground(style.background, theme) : undefined;
    const decoration = term.registerDecoration({
      marker,
      x,
      width,
      ...(foreground ? { foregroundColor: foreground } : {}),
      ...(background ? { backgroundColor: background } : {}),
      layer: style.underline ? "top" : "bottom",
    });
    if (!decoration) return null;
    const underline = style.underline ? (foreground ?? UNDERLINE_DEFAULT[theme]) : null;
    decoration.onRender((element) => {
      // The element only carries the underline; it must never take the pointer from selection or links.
      element.style.pointerEvents = "none";
      if (underline) {
        element.style.boxSizing = "border-box";
        element.style.borderBottom = `1px solid ${underline}`;
      }
    });
    return {
      decoration,
      info: {
        line: marker.line,
        x,
        width,
        ...(foreground ? { foreground } : {}),
        ...(background ? { background } : {}),
        underline: style.underline,
      },
    };
  };

  const noteLineFeed = () => {
    const at = now();
    if (at - burstStart > BURST_WINDOW_MS) {
      burstStart = at;
      burstLines = 0;
    }
    burstLines++;
    if (burstLines > BURST_LINES) {
      isPaused = true;
      pausedUntil = at + BURST_QUIET_MS;
    }
  };

  const restart = () => {
    generation++;
    clear();
    schedule();
  };

  // Each event is optional so partial terminal doubles keep working.
  const listen = (subscribe: () => IDisposable | undefined) => {
    try {
      const disposable = subscribe();
      if (disposable) disposables.push(disposable);
    } catch {
      // The terminal does not offer this event.
    }
  };
  listen(() => term.onLineFeed?.(noteLineFeed));
  listen(() => term.onWriteParsed?.(() => schedule()));
  listen(() => term.onScroll?.(() => schedule()));
  listen(() => term.onResize?.(restart));
  listen(() =>
    term.buffer?.onBufferChange?.(() => {
      clear();
      schedule();
    }),
  );
  const unsubscribe = options.subscribe?.(restart);
  schedule();

  return {
    refresh: () => schedule(),
    scanNow: () => {
      if (timer !== null) {
        clearTimeout(timer);
        timer = null;
      }
      run();
    },
    paused: () => isPaused && pausedUntil > now(),
    rendered: () =>
      entries.flatMap((entry) =>
        entry.decorations.filter(({ decoration }) => !decoration.isDisposed).map(({ decoration, info }) => ({
          ...info,
          line: decoration.marker.line,
        })),
      ),
    dispose() {
      if (disposed) return;
      disposed = true;
      if (timer !== null) clearTimeout(timer);
      timer = null;
      unsubscribe?.();
      for (const disposable of disposables) disposable.dispose();
      clear();
    },
  };
}

/** Highlights a session's terminal with the user's keyword highlight settings and the terminal theme. */
export function attachSessionKeywordHighlight(
  term: Terminal,
  session: { assetId?: string; visible?: () => boolean },
): KeywordHighlighter {
  let source: KeywordHighlightSettings | null = null;
  let compiled: CompiledHighlight[] = [];
  let changed: (() => void) | null = null;
  let loading = false;
  return attachKeywordHighlight(term, {
    rules: () => {
      const settings = getKeywordHighlights();
      if (settings.presets.length === 0 && !settings.rules.some((rule) => rule.enabled)) return [];
      const engine = loadedRegexEngine();
      if (!engine) {
        // The engine is fetched only once a rule is on; the terminal is scanned again when it arrives.
        if (!loading) {
          loading = true;
          void loadRegexEngine().then(
            () => changed?.(),
            () => {
              loading = false;
            },
          );
        }
        return [];
      }
      if (settings !== source) {
        source = settings;
        compiled = compileKeywordHighlights(engine, settings, session.assetId);
      }
      return compiled;
    },
    theme: getResolvedTerminalTheme,
    visible: session.visible,
    subscribe: (listener) => {
      changed = listener;
      const offRules = subscribeKeywordHighlights(listener);
      let theme = getResolvedTerminalTheme();
      const offTheme = subscribeAppearancePrefs(() => {
        const next = getResolvedTerminalTheme();
        if (next === theme) return;
        theme = next;
        listener();
      });
      return () => {
        changed = null;
        offRules();
        offTheme();
      };
    },
  });
}
