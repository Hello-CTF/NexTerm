// Foreground-resume detection. Terminals probe their connections the moment the app returns to the
// foreground instead of waiting for the next heartbeat or reconnect backoff. Signals:
// - visibilitychange to visible (tab switch, window restore, mobile app resume in the webview);
// - pageshow from the back/forward cache;
// - online (the network came back);
// - a timer gap: page timers stop while the machine sleeps, so a tick that arrives far later than
//   scheduled means the system just woke up (desktop lid open), even when no page event fires.
// Triggers within the coalescing window collapse into one, and nothing fires while the browser
// reports itself offline; the online event covers that case.

export type ForegroundResumeReason = "visible" | "pageshow" | "online" | "wake";

export interface ForegroundResumeEnvironment {
  document: Pick<Document, "visibilityState" | "addEventListener" | "removeEventListener">;
  window: Pick<Window, "addEventListener" | "removeEventListener">;
  isOnline: () => boolean;
  now: () => number;
  setInterval: (callback: () => void, ms: number) => unknown;
  clearInterval: (handle: unknown) => void;
}

export interface ForegroundResumeOptions {
  // Triggers closer together than this are merged into the first one.
  coalesceMs?: number;
  // Period of the wake-detection timer.
  tickMs?: number;
  // A tick later than tickMs + wakeGapMs reads as a wake from sleep.
  wakeGapMs?: number;
}

export const FOREGROUND_COALESCE_MS = 1000;
export const FOREGROUND_TICK_MS = 5000;
export const FOREGROUND_WAKE_GAP_MS = 25000;

function browserEnvironment(): ForegroundResumeEnvironment {
  return {
    document,
    window,
    isOnline: () => typeof navigator === "undefined" || navigator.onLine !== false,
    now: () => Date.now(),
    setInterval: (callback, ms) => window.setInterval(callback, ms),
    clearInterval: (handle) => window.clearInterval(handle as number),
  };
}

export function installForegroundResume(
  onResume: (reason: ForegroundResumeReason) => void,
  options: ForegroundResumeOptions = {},
  env: ForegroundResumeEnvironment = browserEnvironment(),
): () => void {
  const coalesceMs = options.coalesceMs ?? FOREGROUND_COALESCE_MS;
  const tickMs = options.tickMs ?? FOREGROUND_TICK_MS;
  const wakeGapMs = options.wakeGapMs ?? FOREGROUND_WAKE_GAP_MS;
  let lastFired = Number.NEGATIVE_INFINITY;
  let lastTick = env.now();

  const fire = (reason: ForegroundResumeReason) => {
    if (!env.isOnline()) return;
    const now = env.now();
    if (now - lastFired < coalesceMs) return;
    lastFired = now;
    onResume(reason);
  };
  const onVisibility = () => {
    if (env.document.visibilityState === "visible") fire("visible");
  };
  const onPageShow = (event: Event) => {
    if ((event as PageTransitionEvent).persisted) fire("pageshow");
  };
  const onOnline = () => fire("online");
  const timer = env.setInterval(() => {
    const now = env.now();
    const gap = now - lastTick;
    lastTick = now;
    if (gap > tickMs + wakeGapMs) fire("wake");
  }, tickMs);

  env.document.addEventListener("visibilitychange", onVisibility);
  env.window.addEventListener("pageshow", onPageShow);
  env.window.addEventListener("online", onOnline);
  return () => {
    env.document.removeEventListener("visibilitychange", onVisibility);
    env.window.removeEventListener("pageshow", onPageShow);
    env.window.removeEventListener("online", onOnline);
    env.clearInterval(timer);
  };
}
