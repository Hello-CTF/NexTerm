import type { Terminal } from "@xterm/xterm";
import type { ISearchOptions, ISearchResultChangeEvent, SearchAddon } from "@xterm/addon-search";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import type { ResolvedTerminalTheme } from "../../app/preferences";
import { createTerminalLinkProvider, type TerminalLinkProviderHandlers } from "./terminalLinkProvider";
import { createWebglController, type GpuAccelerationMode, type WebglHost } from "./webglPolicy";

export type WebLinks = TerminalLinkProviderHandlers;

/**
 * Measures characters with the Unicode 11 table instead of xterm's default Unicode 6 one, which draws most emoji in
 * one cell while shells and the server screen model (internal/terminal/width.go) give them two. Every terminal that
 * shows session output calls this before its first write; it needs the allowProposedApi option.
 */
export function activateUnicode11(term: Terminal): void {
  // Partial test doubles have no unicode handling and keep their defaults.
  if (!("unicode" in term)) return;
  term.loadAddon(new Unicode11Addon());
  term.unicode.activeVersion = "11";
}

/**
 * The search addon is fetched with dynamic import() after the terminal is mounted so it stays
 * out of the first-load closure. The terminal is fully usable (DOM renderer, no links, no search)
 * while they arrive, and a failed import leaves that baseline in place.
 */

export interface TerminalSearchFlags {
  caseSensitive: boolean;
  wholeWord: boolean;
  regex: boolean;
}

export type TerminalSearchResults = ISearchResultChangeEvent;

// The addon highlights and counts at most this many matches; resultIndex is -1 past it.
export const SEARCH_HIGHLIGHT_LIMIT = 1000;

// Decoration backgrounds must be #RRGGBB. Matches sit under the text, the active match on top of the selection.
export function searchDecorationsFor(theme: ResolvedTerminalTheme): NonNullable<ISearchOptions["decorations"]> {
  return theme === "light"
    ? {
        matchBackground: "#f6e3a1",
        matchBorder: "#c99a2e",
        matchOverviewRuler: "#c99a2e",
        activeMatchBackground: "#f2b84b",
        activeMatchBorder: "#8a5a00",
        activeMatchColorOverviewRuler: "#8a5a00",
      }
    : {
        matchBackground: "#4a3f22",
        matchBorder: "#9c8448",
        matchOverviewRuler: "#9c8448",
        activeMatchBackground: "#8a6a1f",
        activeMatchBorder: "#f0d27a",
        activeMatchColorOverviewRuler: "#f0d27a",
      };
}

export interface LazySearch {
  findNext(term: string, options?: ISearchOptions): void;
  findPrevious(term: string, options?: ISearchOptions): void;
  clear(): void;
  onResults(listener: (results: TerminalSearchResults) => void): () => void;
}

export function attachLazyAddons(
  term: Terminal,
  links: WebLinks,
  isDisposed: () => boolean,
): LazySearch {
  let search: SearchAddon | null = null;
  const resultListeners = new Set<(results: TerminalSearchResults) => void>();
  let searchPromise: Promise<SearchAddon | null> | null = null;
  const loadSearch = (): Promise<SearchAddon | null> => {
    searchPromise ??= import("@xterm/addon-search").then(
      ({ SearchAddon }) => {
        if (isDisposed()) return null;
        search = new SearchAddon({ highlightLimit: SEARCH_HIGHLIGHT_LIMIT });
        term.loadAddon(search);
        // Partial test doubles may not carry the event.
        search.onDidChangeResults?.((results) => {
          for (const listener of resultListeners) listener(results);
        });
        return search;
      },
      () => null,
    );
    return searchPromise;
  };
  void loadSearch();
  // Links are detected by our own provider (wrapped lines, box borders), registered up front.
  // A terminal without link support (partial test doubles) simply has no links.
  if (typeof term.registerLinkProvider === "function") {
    term.registerLinkProvider(createTerminalLinkProvider(term, links));
  }
  const run = (action: (addon: SearchAddon) => void) => {
    if (search) {
      action(search);
      return;
    }
    // A search issued before the addon arrives runs as soon as it does.
    void loadSearch().then((addon) => {
      if (addon && !isDisposed()) action(addon);
    });
  };
  return {
    findNext: (text, options) => run((addon) => addon.findNext(text, options)),
    findPrevious: (text, options) => run((addon) => addon.findPrevious(text, options)),
    // Clearing before the addon arrives has nothing to clear; partial test doubles may lack the method.
    clear: () => search?.clearDecorations?.(),
    onResults: (listener) => {
      resultListeners.add(listener);
      return () => resultListeners.delete(listener);
    },
  };
}

export interface WebglRendererOptions {
  mode?: GpuAccelerationMode;
  visible?: boolean;
  onFallback?: () => void;
}

export interface WebglRenderer {
  cancel(): void;
  setVisible(visible: boolean): void;
  setMode(mode: GpuAccelerationMode): void;
}

const loadWebglAddon = () => import("@xterm/addon-webgl").then(({ WebglAddon }) => WebglAddon);

/**
 * Loads the WebGL renderer after first paint, when the browser is idle. Failure to import or to create
 * a GL context keeps the DOM renderer; context loss, software renderers and hidden-pane limits are
 * handled by the policy in webglPolicy.ts.
 */
export function scheduleWebglRenderer(
  term: Terminal,
  isDisposed: () => boolean,
  options: WebglRendererOptions = {},
): WebglRenderer {
  let cancelled = false;
  let frame: number | null = null;
  let idle: number | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;
  const controller = createWebglController({
    host: term as unknown as WebglHost,
    load: loadWebglAddon,
    mode: options.mode ?? "auto",
    visible: options.visible,
    onFallback: options.onFallback,
  });
  const load = () => {
    idle = null;
    timer = null;
    if (cancelled || isDisposed()) return;
    controller.start();
  };
  const afterPaint = () => {
    frame = null;
    if (cancelled) return;
    if (typeof window.requestIdleCallback === "function") {
      idle = window.requestIdleCallback(load, { timeout: 1500 });
    } else {
      timer = setTimeout(load, 200);
    }
  };
  if (typeof window.requestAnimationFrame === "function") {
    frame = window.requestAnimationFrame(afterPaint);
  } else {
    afterPaint();
  }
  return {
    cancel() {
      cancelled = true;
      controller.dispose();
      if (frame !== null) window.cancelAnimationFrame(frame);
      if (idle !== null && typeof window.cancelIdleCallback === "function") window.cancelIdleCallback(idle);
      if (timer !== null) clearTimeout(timer);
    },
    setVisible: (visible) => controller.setVisible(visible),
    setMode: (mode) => controller.setMode(mode),
  };
}
