import type { Terminal } from "@xterm/xterm";
import type { SearchAddon } from "@xterm/addon-search";

type LinkHandler = (event: MouseEvent, uri: string) => void;

/**
 * Optional xterm addons are fetched with dynamic import() after the terminal is mounted so they stay
 * out of the first-load closure. The terminal is fully usable (DOM renderer, no links, no search)
 * while they arrive, and a failed import leaves that baseline in place.
 */

export interface LazySearch {
  findNext(term: string): void;
  findPrevious(term: string): void;
}

export function attachLazyAddons(
  term: Terminal,
  linkHandler: LinkHandler,
  isDisposed: () => boolean,
): LazySearch {
  let search: SearchAddon | null = null;
  let searchPromise: Promise<SearchAddon | null> | null = null;
  const loadSearch = (): Promise<SearchAddon | null> => {
    searchPromise ??= import("@xterm/addon-search").then(
      ({ SearchAddon }) => {
        if (isDisposed()) return null;
        search = new SearchAddon();
        term.loadAddon(search);
        return search;
      },
      () => null,
    );
    return searchPromise;
  };
  void loadSearch();
  void import("@xterm/addon-web-links").then(
    ({ WebLinksAddon }) => {
      if (isDisposed()) return;
      term.loadAddon(new WebLinksAddon(linkHandler));
    },
    () => undefined,
  );
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
    findNext: (text) => run((addon) => addon.findNext(text)),
    findPrevious: (text) => run((addon) => addon.findPrevious(text)),
  };
}

/**
 * Loads the WebGL renderer after first paint, when the browser is idle. Returns a cancel function.
 * Failure to import or to create a GL context keeps the DOM renderer.
 */
export function scheduleWebglRenderer(term: Terminal, isDisposed: () => boolean): () => void {
  let cancelled = false;
  let frame: number | null = null;
  let idle: number | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;
  const load = () => {
    idle = null;
    timer = null;
    if (cancelled || isDisposed()) return;
    void import("@xterm/addon-webgl").then(
      ({ WebglAddon }) => {
        if (cancelled || isDisposed()) return;
        try {
          term.loadAddon(new WebglAddon());
        } catch {
          // Keep the DOM renderer when WebGL is unavailable.
        }
      },
      () => undefined,
    );
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
  return () => {
    cancelled = true;
    if (frame !== null) window.cancelAnimationFrame(frame);
    if (idle !== null && typeof window.cancelIdleCallback === "function") window.cancelIdleCallback(idle);
    if (timer !== null) clearTimeout(timer);
  };
}
