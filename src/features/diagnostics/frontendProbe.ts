// Performance probe for the debug capture. It runs only while a capture is active (see
// startFrontendProbeIfCapturing) and reports small numeric windows to the backend every few seconds.
// React's Profiler is a no-op in production builds, so render churn is measured through DOM mutations.
import { diagnosticsApi, type DiagnosticsNamedValue, type FrontendPerfReport } from "../../ipc/diagnosticsApi";
import { WEB } from "../../ipc/env";
import { setProbeSinks } from "../../ipc/probeHooks";
import { useUi } from "../../app/store";

export const PROBE_WINDOW_MS = 5000;
const LAG_TICK_MS = 100;
const MAX_FAILURES = 3;
const TOP = 8;

function topN(counts: Map<string, number>, limit = TOP): DiagnosticsNamedValue[] {
  return [...counts.entries()]
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .slice(0, limit)
    .map(([name, value]) => ({ name: name.slice(0, 64), value }));
}

export function percentile(values: number[], p: number): number {
  if (values.length === 0) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  const index = Math.min(sorted.length - 1, Math.max(0, Math.ceil((p / 100) * sorted.length) - 1));
  return sorted[index];
}

const round = (value: number) => Math.round(value * 10) / 10;

export interface WindowExtras {
  windowMs: number;
  hidden: boolean;
  domNodes?: number;
  xterms?: number;
  animations?: number;
  heapUsedMB?: number;
}

// WindowStats accumulates one window of observations and turns them into a report.
export class WindowStats {
  private lags: number[] = [];
  private frames = 0;
  private maxFrameGap = 0;
  private lastFrame: number | null = null;
  private mutations = 0;
  private churn = new Map<string, number>();
  private ipc = new Map<string, number>();
  private events = new Map<string, number>();
  private storeSets = 0;
  private storeKeys = new Map<string, number>();
  private longTasks = 0;
  private longTaskMax = 0;

  noteLag(ms: number): void {
    this.lags.push(ms);
  }

  noteFrame(timestamp: number): void {
    this.frames += 1;
    if (this.lastFrame !== null) this.maxFrameGap = Math.max(this.maxFrameGap, timestamp - this.lastFrame);
    this.lastFrame = timestamp;
  }

  // A hidden page produces no frames; do not report the gap across the hidden period.
  resetFrameClock(): void {
    this.lastFrame = null;
  }

  noteMutations(count: number, label?: string): void {
    this.mutations += count;
    if (label) this.churn.set(label, (this.churn.get(label) ?? 0) + count);
  }

  noteIpc(name: string): void {
    this.ipc.set(name, (this.ipc.get(name) ?? 0) + 1);
  }

  noteEvent(name: string): void {
    this.events.set(name, (this.events.get(name) ?? 0) + 1);
  }

  noteStoreSet(changedKeys: string[]): void {
    this.storeSets += 1;
    for (const key of changedKeys) this.storeKeys.set(key, (this.storeKeys.get(key) ?? 0) + 1);
  }

  noteLongTask(ms: number): void {
    this.longTasks += 1;
    this.longTaskMax = Math.max(this.longTaskMax, ms);
  }

  // snapshot returns the window as a report and starts a fresh window.
  snapshot(extras: WindowExtras): FrontendPerfReport {
    const seconds = Math.max(extras.windowMs, 1) / 1000;
    const report: FrontendPerfReport = {
      kind: "window",
      windowMs: Math.round(extras.windowMs),
      lagMaxMs: round(this.lags.length ? Math.max(...this.lags) : 0),
      lagP95Ms: round(percentile(this.lags, 95)),
      fps: round(this.frames / seconds),
      maxFrameGapMs: round(this.maxFrameGap),
      mutations: this.mutations,
      domChurn: topN(this.churn),
      ipc: topN(this.ipc),
      events: topN(this.events),
      storeSets: this.storeSets,
      storeKeys: topN(this.storeKeys),
      longTasks: this.longTasks,
      longTaskMsMax: round(this.longTaskMax),
      hidden: extras.hidden,
    };
    if (extras.domNodes !== undefined) report.domNodes = extras.domNodes;
    if (extras.xterms !== undefined) report.xterms = extras.xterms;
    if (extras.animations !== undefined) report.animations = extras.animations;
    if (extras.heapUsedMB !== undefined) report.heapUsedMB = round(extras.heapUsedMB);
    this.lags = [];
    this.frames = 0;
    this.maxFrameGap = 0;
    this.mutations = 0;
    this.churn = new Map();
    this.ipc = new Map();
    this.events = new Map();
    this.storeSets = 0;
    this.storeKeys = new Map();
    this.longTasks = 0;
    this.longTaskMax = 0;
    return report;
  }
}

// mutationLabel names the region a DOM change happened in without ever reading text or attribute values:
// the nearest element id, else its first nx- class, else the tag.
export function mutationLabel(node: Node | null): string {
  let element: Element | null = node && node.nodeType === 1 ? (node as Element) : (node?.parentElement ?? null);
  for (let depth = 0; element && depth < 8; depth += 1, element = element.parentElement) {
    // Only plain identifier-style ids are used as labels; an id built from user content (paths, host names) is skipped.
    if (element.id && /^[A-Za-z][A-Za-z0-9_-]{0,40}$/.test(element.id)) return `#${element.id}`;
    const token = [...element.classList].find((name) => name.startsWith("nx-") || name.startsWith("xterm"));
    if (token) return `${element.tagName.toLowerCase()}.${token}`.slice(0, 64);
  }
  return "body";
}

export function shortAgent(userAgent: string): string {
  const webkit = /AppleWebKit\/[\d.]+/.exec(userAgent)?.[0] ?? "";
  const version = /Version\/[\d.]+/.exec(userAgent)?.[0] ?? "";
  const chrome = /Chrome\/[\d.]+/.exec(userAgent)?.[0] ?? "";
  return [webkit, version || chrome].filter(Boolean).join(" ").slice(0, 64);
}

export interface ProbeEnv {
  report: (report: FrontendPerfReport) => Promise<unknown>;
  now: () => number;
  hidden: () => boolean;
  domNodes: () => number | undefined;
  xterms: () => number | undefined;
  animations: () => number | undefined;
  heapUsedMB: () => number | undefined;
  viewport: () => { width: number; height: number };
  userAgent: () => string;
  requestFrame: (callback: (timestamp: number) => void) => number;
  cancelFrame: (id: number) => void;
  observeMutations: (onRecords: (records: MutationRecord[]) => void) => (() => void) | null;
  observeLongTasks: (onTask: (ms: number) => void) => (() => void) | null;
  subscribeStore: (onSet: (changedKeys: string[]) => void) => () => void;
}

function browserEnv(): ProbeEnv {
  return {
    report: (report) => diagnosticsApi.reportFrontend(report),
    now: () => performance.now(),
    hidden: () => document.hidden,
    domNodes: () => document.getElementsByTagName("*").length,
    xterms: () => document.querySelectorAll(".xterm").length,
    animations: () => (typeof document.getAnimations === "function" ? document.getAnimations().length : undefined),
    heapUsedMB: () => {
      const memory = (performance as unknown as { memory?: { usedJSHeapSize?: number } }).memory;
      return memory?.usedJSHeapSize ? memory.usedJSHeapSize / (1024 * 1024) : undefined;
    },
    viewport: () => ({ width: window.innerWidth, height: window.innerHeight }),
    userAgent: () => navigator.userAgent,
    requestFrame: (callback) => window.requestAnimationFrame(callback),
    cancelFrame: (id) => window.cancelAnimationFrame(id),
    observeMutations: (onRecords) => {
      if (typeof MutationObserver === "undefined") return null;
      const observer = new MutationObserver(onRecords);
      observer.observe(document.body, { childList: true, subtree: true, attributes: true, characterData: true });
      return () => observer.disconnect();
    },
    observeLongTasks: (onTask) => {
      const supported = typeof PerformanceObserver !== "undefined" ? PerformanceObserver.supportedEntryTypes : undefined;
      if (!supported?.includes("longtask")) return null;
      const observer = new PerformanceObserver((list) => {
        for (const entry of list.getEntries()) onTask(entry.duration);
      });
      observer.observe({ entryTypes: ["longtask"] });
      return () => observer.disconnect();
    },
    subscribeStore: (onSet) =>
      useUi.subscribe((state, previous) => {
        const changed: string[] = [];
        const current = state as unknown as Record<string, unknown>;
        const before = previous as unknown as Record<string, unknown>;
        for (const key of Object.keys(current)) {
          if (current[key] !== before[key] && typeof current[key] !== "function") changed.push(key);
        }
        onSet(changed);
      }),
  };
}

// startFrontendProbe begins sampling and returns a stop function. It sends one start line, then one report
// per window; after repeated send failures (for example the capture ended) it stops itself.
export function startFrontendProbe(overrides: Partial<ProbeEnv> = {}): () => void {
  const env: ProbeEnv = { ...browserEnv(), ...overrides };
  const stats = new WindowStats();
  const cleanup: Array<() => void> = [];
  let stopped = false;
  let failures = 0;

  const stop = () => {
    if (stopped) return;
    stopped = true;
    setProbeSinks(null, null);
    for (const fn of cleanup.splice(0)) fn();
  };

  const send = (report: FrontendPerfReport) => {
    void env.report(report).then(
      () => {
        failures = 0;
      },
      () => {
        failures += 1;
        if (failures >= MAX_FAILURES) stop();
      },
    );
  };

  const view = env.viewport();
  send({ kind: "start", agent: shortAgent(env.userAgent()), viewWidth: view.width, viewHeight: view.height });

  setProbeSinks(
    (name) => {
      if (!name.startsWith("diagnostics_")) stats.noteIpc(name);
    },
    (name) => stats.noteEvent(name),
  );
  cleanup.push(() => setProbeSinks(null, null));

  let expected = env.now() + LAG_TICK_MS;
  const lagTimer = setInterval(() => {
    const now = env.now();
    stats.noteLag(Math.max(0, now - expected));
    expected = now + LAG_TICK_MS;
  }, LAG_TICK_MS);
  cleanup.push(() => clearInterval(lagTimer));

  let frameId = 0;
  const onFrame = (timestamp: number) => {
    if (stopped) return;
    if (env.hidden()) stats.resetFrameClock();
    else stats.noteFrame(timestamp);
    frameId = env.requestFrame(onFrame);
  };
  frameId = env.requestFrame(onFrame);
  cleanup.push(() => env.cancelFrame(frameId));

  const stopMutations = env.observeMutations((records) => {
    // Label a sample of large bursts only; the count itself stays exact.
    const step = records.length > 200 ? Math.ceil(records.length / 200) : 1;
    let counted = 0;
    for (let index = 0; index < records.length; index += step) {
      stats.noteMutations(Math.min(step, records.length - index), mutationLabel(records[index].target));
      counted += Math.min(step, records.length - index);
    }
    if (counted < records.length) stats.noteMutations(records.length - counted);
  });
  if (stopMutations) cleanup.push(stopMutations);
  const stopLongTasks = env.observeLongTasks((ms) => stats.noteLongTask(ms));
  if (stopLongTasks) cleanup.push(stopLongTasks);
  cleanup.push(env.subscribeStore((keys) => stats.noteStoreSet(keys)));

  let windowStart = env.now();
  const windowTimer = setInterval(() => {
    const now = env.now();
    send(
      stats.snapshot({
        windowMs: now - windowStart,
        hidden: env.hidden(),
        domNodes: env.domNodes(),
        xterms: env.xterms(),
        animations: env.animations(),
        heapUsedMB: env.heapUsedMB(),
      }),
    );
    windowStart = now;
  }, PROBE_WINDOW_MS);
  cleanup.push(() => clearInterval(windowTimer));

  return stop;
}

// startFrontendProbeIfCapturing costs one status call at launch; nothing else runs unless a capture is active.
// The web build is skipped: its status call would run before login and must not disturb the auth flow.
export async function startFrontendProbeIfCapturing(): Promise<(() => void) | null> {
  if (WEB) return null;
  try {
    const status = await diagnosticsApi.status();
    return status.active ? startFrontendProbe() : null;
  } catch {
    return null;
  }
}
