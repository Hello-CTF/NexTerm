import type { Terminal } from "@xterm/xterm";
import type { SearchAddon } from "@xterm/addon-search";
import { createTerminalLinkProvider, type TerminalLinkProviderHandlers } from "./terminalLinkProvider";
import { createWebglController, type GpuAccelerationMode, type WebglHost } from "./webglPolicy";

export type WebLinks = TerminalLinkProviderHandlers;

/**
 * The search addon is fetched with dynamic import() after the terminal is mounted so it stays
 * out of the first-load closure. The terminal is fully usable (DOM renderer, no links, no search)
 * while they arrive, and a failed import leaves that baseline in place.
 */

export interface LazySearch {
  findNext(term: string): void;
  findPrevious(term: string): void;
}

export function attachLazyAddons(
  term: Terminal,
  links: WebLinks,
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
    findNext: (text) => run((addon) => addon.findNext(text)),
    findPrevious: (text) => run((addon) => addon.findPrevious(text)),
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
