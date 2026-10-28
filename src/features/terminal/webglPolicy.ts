// WebGL renderer policy for terminals: which mode loads WebGL, how a pane recovers from GPU context
// loss, and how many hidden panes may keep a GL context. Everything that touches the browser (addon
// import, timers, the renderer probe) is injected so the policy runs under jsdom, which has no WebGL.

import type { TerminalGpuAcceleration } from "../../app/preferences";

export type GpuAccelerationMode = TerminalGpuAcceleration;

// A pane that loses its context this many times inside the window stays on the DOM renderer. The
// addon itself waits 3 s for the browser to restore a lost context before it reports the loss; a
// restore inside that grace period reinitialises the renderer without reaching this budget. A
// reported loss therefore means ~3 s of a stale canvas, then the DOM renderer at once, then a
// re-attach attempt after the backoff below.
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
  getContext(kind: "webgl2", attributes?: { failIfMajorPerformanceCaveat?: boolean }): unknown;
}

const GL_VENDOR = 0x1f00;
const GL_RENDERER = 0x1f01;

function releaseContext(gl: ProbeContext | null | undefined): void {
  try {
    (gl?.getExtension("WEBGL_lose_context") as { loseContext(): void } | null | undefined)?.loseContext();
  } catch {
    // The context is garbage collected either way.
  }
}

// A runtime without WebGL2 (the WebGL addon's requirement) gets no canvas probe at all.
function defaultProbeCanvas(): ProbeCanvas | null {
  if (typeof WebGL2RenderingContext === "undefined" || typeof document === "undefined") return null;
  return document.createElement("canvas");
}

// probeRenderer reads the GPU identity once. The unmasked name comes from WEBGL_debug_renderer_info,
// falling back to the plain RENDERER string when the browser hides the extension; a name that still
// says nothing is treated as hardware and the context-loss budget is the remaining safety net. A
// context refused with failIfMajorPerformanceCaveat while a plain one succeeds is the browser's own
// verdict that the GPU path is slow (software rasterisation), so it also counts as software.
export function probeRenderer(createCanvas: () => ProbeCanvas | null = defaultProbeCanvas): RendererProbe {
  let fast: ProbeContext | null | undefined;
  let gl: ProbeContext | null | undefined;
  try {
    fast = createCanvas()?.getContext("webgl2", { failIfMajorPerformanceCaveat: true }) as ProbeContext | null | undefined;
    gl = fast ?? (createCanvas()?.getContext("webgl2") as ProbeContext | null | undefined);
    if (!gl) return { webgl: false, software: false, renderer: null };
    const info = gl.getExtension("WEBGL_debug_renderer_info") as
      | { UNMASKED_RENDERER_WEBGL: number; UNMASKED_VENDOR_WEBGL: number }
      | null;
    const renderer = String(gl.getParameter(info ? info.UNMASKED_RENDERER_WEBGL : GL_RENDERER) ?? "");
    const vendor = String(gl.getParameter(info ? info.UNMASKED_VENDOR_WEBGL : GL_VENDOR) ?? "");
    return {
      webgl: true,
      software: !fast || isSoftwareRenderer(`${vendor} ${renderer}`),
      renderer: renderer || null,
    };
  } catch {
    return { webgl: false, software: false, renderer: null };
  } finally {
    releaseContext(gl);
    if (fast !== gl) releaseContext(fast);
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
  readonly element?: { querySelectorAll(selectors: string): Iterable<WebglCanvas> } | null;
}

export interface WebglCanvas {
  readonly isConnected: boolean;
  getContext(kind: "webgl2"): unknown;
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

  const screenCanvases = (): WebglCanvas[] => {
    try {
      return [...(host.element?.querySelectorAll(".xterm-screen canvas") ?? [])];
    } catch {
      return [];
    }
  };

  // detach disposes the addon; xterm then renders with the DOM renderer. The addon removes its canvas
  // but never loses the GL context, which would stay allocated until garbage collection, so a context
  // that is still alive (eviction, setting change) is released explicitly. After a context loss there
  // is nothing to release.
  const detach = (releaseGpu: boolean) => {
    const current = addon;
    if (!current) return;
    addon = null;
    lossListener?.dispose();
    lossListener = null;
    retention.release(owner);
    const canvases = releaseGpu ? screenCanvases() : [];
    try {
      current.dispose();
    } catch {
      // xterm already restored the DOM renderer or the terminal is gone.
    }
    for (const canvas of canvases) {
      // Only canvases the addon just removed. getContext returns a canvas's existing context (null for
      // the 2D link layer), so this never creates a new one.
      if (canvas.isConnected) continue;
      try {
        releaseContext(canvas.getContext("webgl2") as ProbeContext | null);
      } catch {
        // Already released.
      }
    }
    repaint();
  };

  const retainWhileHidden = () => {
    if (!addon || visible) return;
    retention.retain(owner, () => detach(true));
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
    // A hidden pane never creates a context: a burst of background tabs at startup would exceed the
    // browser's live-context cap and get contexts force-lost. It attaches when revealed.
    visible &&
    retryTimer === null &&
    shouldLoadWebgl(mode, probe);

  const onContextLoss = (lost: WebglAddonLike) => {
    if (lost !== addon || disposed) return;
    detach(false);
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
      // A retained renderer kept its atlas, and xterm applies font, theme and DPR changes to it while
      // hidden, so only a re-attached renderer clears the shared atlas.
      retention.release(owner);
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
        detach(true);
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
