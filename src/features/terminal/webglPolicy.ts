// WebGL renderer policy for terminals: which mode loads WebGL, how a pane recovers from GPU context
// loss, and how many hidden panes may keep a GL context. Everything that touches the browser (addon
// import, timers, the renderer probe) is injected so the policy runs under jsdom, which has no WebGL.

import type { TerminalGpuAcceleration } from "../../app/preferences";

export type GpuAccelerationMode = TerminalGpuAcceleration;

// A pane that loses its context this many times inside the window stays on the DOM renderer.
export const WEBGL_CONTEXT_LOSS_LIMIT = 3;
export const WEBGL_CONTEXT_LOSS_WINDOW_MS = 60_000;
// Re-attach delay after the n-th recent loss: base * 2^(n-1).
export const WEBGL_RETRY_BASE_MS = 1_000;
// Hidden panes (inactive tab or workspace) that may keep a live GL context; browsers cap contexts
// per page and each one holds a texture atlas in GPU memory.
export const MAX_HIDDEN_WEBGL_CONTEXTS = 6;

const SOFTWARE_RENDERER_PATTERN =
  /\b(swiftshader|llvmpipe|lavapipe|softpipe|software rasterizer|software adapter|basic render|virgl|svga3d)\b/i;

export function isSoftwareRenderer(identity: string): boolean {
  return SOFTWARE_RENDERER_PATTERN.test(identity);
}

export interface RendererProbe {
  webgl: boolean;
  software: boolean;
  renderer: string | null;
}

interface ProbeContext {
  getExtension(name: string): unknown;
  getParameter(parameter: number): unknown;
}

interface ProbeCanvas {
  getContext(kind: "webgl2"): unknown;
}

// A runtime without WebGL2 (the WebGL addon's requirement) gets no canvas probe at all.
function defaultProbeCanvas(): ProbeCanvas | null {
  if (typeof WebGL2RenderingContext === "undefined" || typeof document === "undefined") return null;
  return document.createElement("canvas");
}

// probeRenderer reads the unmasked GPU identity once. A browser that hides it is treated as hardware:
// WebGL then still loads and the context-loss budget is the remaining safety net.
export function probeRenderer(createCanvas: () => ProbeCanvas | null = defaultProbeCanvas): RendererProbe {
  try {
    const gl = createCanvas()?.getContext("webgl2") as ProbeContext | null | undefined;
    if (!gl) return { webgl: false, software: false, renderer: null };
    const info = gl.getExtension("WEBGL_debug_renderer_info") as
      | { UNMASKED_RENDERER_WEBGL: number; UNMASKED_VENDOR_WEBGL: number }
      | null;
    const renderer = info ? String(gl.getParameter(info.UNMASKED_RENDERER_WEBGL) ?? "") : "";
    const vendor = info ? String(gl.getParameter(info.UNMASKED_VENDOR_WEBGL) ?? "") : "";
    const lose = gl.getExtension("WEBGL_lose_context") as { loseContext(): void } | null;
    lose?.loseContext();
    return { webgl: true, software: isSoftwareRenderer(`${vendor} ${renderer}`), renderer: renderer || null };
  } catch {
    return { webgl: false, software: false, renderer: null };
  }
}

let cachedProbe: RendererProbe | null = null;

// cachedRendererProbe probes at most once per app run.
export function cachedRendererProbe(): RendererProbe {
  cachedProbe ??= probeRenderer();
  return cachedProbe;
}

export function resetRendererProbeCache(): void {
  cachedProbe = null;
}

export function shouldLoadWebgl(mode: GpuAccelerationMode, probe: () => RendererProbe): boolean {
  if (mode === "off") return false;
  if (mode === "on") return true;
  const result = probe();
  return result.webgl && !result.software;
}

// ContextLossBudget counts context losses inside a rolling window.
export class ContextLossBudget {
  private losses: number[] = [];

  constructor(
    private readonly limit = WEBGL_CONTEXT_LOSS_LIMIT,
    private readonly windowMs = WEBGL_CONTEXT_LOSS_WINDOW_MS,
  ) {}

  // record adds a loss and returns the number of losses inside the window, including this one.
  record(now: number): number {
    this.losses = this.losses.filter((at) => at > now - this.windowMs);
    this.losses.push(now);
    return this.losses.length;
  }

  exhausted(count: number): boolean {
    return count >= this.limit;
  }

  reset(): void {
    this.losses = [];
  }
}

// HiddenWebglRetention is an LRU of hidden panes that still hold a GL context. Map insertion order is
// the hide order, so the first entry is the pane that has been hidden longest.
export class HiddenWebglRetention {
  private readonly entries = new Map<object, () => void>();

  constructor(private readonly capacity = MAX_HIDDEN_WEBGL_CONTEXTS) {}

  retain(owner: object, evict: () => void): void {
    this.entries.delete(owner);
    this.entries.set(owner, evict);
    while (this.entries.size > this.capacity) {
      const [oldest, evictOldest] = this.entries.entries().next().value as [object, () => void];
      this.entries.delete(oldest);
      evictOldest();
    }
  }

  release(owner: object): void {
    this.entries.delete(owner);
  }

  get size(): number {
    return this.entries.size;
  }
}

export const hiddenWebglRetention = new HiddenWebglRetention();

export interface WebglAddonLike {
  dispose(): void;
  onContextLoss?: (listener: () => void) => { dispose(): void };
  clearTextureAtlas?: () => void;
}

export type WebglAddonConstructor = new () => WebglAddonLike;

export interface WebglHost {
  loadAddon(addon: WebglAddonLike): void;
  refresh(start: number, end: number): void;
  readonly rows: number;
}

export interface WebglClock {
  now(): number;
  setTimeout(callback: () => void, ms: number): unknown;
  clearTimeout(handle: unknown): void;
}

const realClock: WebglClock = {
  now: () => Date.now(),
  setTimeout: (callback, ms) => setTimeout(callback, ms),
  clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

export interface WebglControllerOptions {
  host: WebglHost;
  load: () => Promise<WebglAddonConstructor>;
  mode: GpuAccelerationMode;
  visible?: boolean;
  clock?: WebglClock;
  probe?: () => RendererProbe;
  retention?: HiddenWebglRetention;
  // Called once when repeated context loss pins this pane to the DOM renderer.
  onFallback?: () => void;
}

export interface WebglController {
  start(): void;
  setVisible(visible: boolean): void;
  setMode(mode: GpuAccelerationMode): void;
  dispose(): void;
}

// createWebglController owns one terminal's WebGL addon. The DOM renderer is the baseline: every
// failure path disposes the addon, which hands rendering back to xterm's DOM renderer at once.
export function createWebglController(options: WebglControllerOptions): WebglController {
  const { host, load } = options;
  const clock = options.clock ?? realClock;
  const probe = options.probe ?? cachedRendererProbe;
  const retention = options.retention ?? hiddenWebglRetention;
  const budget = new ContextLossBudget();
  const owner = {};
  let mode = options.mode;
  let visible = options.visible !== false;
  let started = false;
  let disposed = false;
  let latched = false;
  let unavailable = false;
  let evicted = false;
  let notified = false;
  let attachCount = 0;
  let addon: WebglAddonLike | null = null;
  let lossListener: { dispose(): void } | null = null;
  let loadGeneration = 0;
  let loading = false;
  let retryTimer: unknown = null;

  const repaint = () => {
    try {
      host.refresh(0, Math.max(0, host.rows - 1));
    } catch {
      // A terminal torn down mid-callback has nothing to repaint.
    }
  };

  const clearAtlas = (target: WebglAddonLike) => {
    try {
      target.clearTextureAtlas?.();
    } catch {
      // The atlas is rebuilt lazily; a failed clear only risks a stale glyph until the next one.
    }
    repaint();
  };

  const detach = () => {
    const current = addon;
    if (!current) return;
    addon = null;
    lossListener?.dispose();
    lossListener = null;
    retention.release(owner);
    try {
      current.dispose();
    } catch {
      // xterm already restored the DOM renderer or the terminal is gone.
    }
    repaint();
  };

  const retainWhileHidden = () => {
    if (!addon || visible) return;
    retention.retain(owner, () => {
      evicted = true;
      detach();
    });
  };

  const cancelRetry = () => {
    if (retryTimer === null) return;
    clock.clearTimeout(retryTimer);
    retryTimer = null;
  };

  const wanted = () =>
    started &&
    !disposed &&
    !latched &&
    !unavailable &&
    !evicted &&
    retryTimer === null &&
    shouldLoadWebgl(mode, probe);

  const onContextLoss = (lost: WebglAddonLike) => {
    if (lost !== addon || disposed) return;
    detach();
    const count = budget.record(clock.now());
    if (budget.exhausted(count)) {
      latched = true;
      if (!notified) {
        notified = true;
        options.onFallback?.();
      }
      return;
    }
    retryTimer = clock.setTimeout(() => {
      retryTimer = null;
      attach();
    }, WEBGL_RETRY_BASE_MS * 2 ** (count - 1));
  };

  const attach = () => {
    if (addon || loading || !wanted()) return;
    loading = true;
    const generation = ++loadGeneration;
    load().then(
      (Addon) => {
        if (generation !== loadGeneration) return;
        loading = false;
        if (addon || !wanted()) return;
        let next: WebglAddonLike | null = null;
        try {
          next = new Addon();
          host.loadAddon(next);
        } catch {
          // No usable GL context: stay on the DOM renderer until the setting changes.
          unavailable = true;
          try {
            next?.dispose();
          } catch {
            // Nothing was attached.
          }
          return;
        }
        const attached = next;
        addon = attached;
        lossListener = attached.onContextLoss?.(() => onContextLoss(attached)) ?? null;
        // Re-attached renderers share xterm's atlas cache; clear it so earlier glyphs are redrawn.
        if (attachCount > 0) clearAtlas(attached);
        attachCount += 1;
        retainWhileHidden();
      },
      () => {
        if (generation !== loadGeneration) return;
        loading = false;
        unavailable = true;
      },
    );
  };

  const abandonLoad = () => {
    loadGeneration += 1;
    loading = false;
  };

  return {
    start() {
      started = true;
      attach();
    },
    setVisible(next) {
      if (next === visible) return;
      visible = next;
      if (!visible) {
        retainWhileHidden();
        return;
      }
      retention.release(owner);
      if (addon) {
        clearAtlas(addon);
        return;
      }
      evicted = false;
      attach();
    },
    setMode(next) {
      if (next === mode) return;
      mode = next;
      latched = false;
      unavailable = false;
      notified = false;
      budget.reset();
      cancelRetry();
      abandonLoad();
      if (!shouldLoadWebgl(mode, probe)) {
        detach();
        return;
      }
      attach();
    },
    dispose() {
      disposed = true;
      cancelRetry();
      abandonLoad();
      retention.release(owner);
      lossListener?.dispose();
      lossListener = null;
    },
  };
}
