import { useEffect, useRef } from "react";

export interface PollingOptions {
  intervalMs: number;
  // Optional slower delay while the page is visible but the window is not focused. Off by default: webviews can report
  // no focus while the user is still looking at them, so only callers that tolerate staleness should opt in.
  blurredIntervalMs?: number;
  // Run once right away on start. Callers that already loaded their data pass false.
  immediate?: boolean;
}

// Resuming from hidden/blurred polls at once unless a run just happened.
const RESUME_MIN_GAP_MS = 1000;

// startPolling runs `run` repeatedly: the next run is due one interval after the previous one finished (no stacking),
// nothing runs while document.hidden, and becoming visible or focused again refreshes immediately. Returns a stop function.
export function startPolling(run: () => unknown, options: PollingOptions): () => void {
  const { intervalMs, blurredIntervalMs, immediate = true } = options;
  let stopped = false;
  let running = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let lastRunAt = Number.NEGATIVE_INFINITY;

  const clear = () => {
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
  };
  const delay = () =>
    blurredIntervalMs !== undefined && !document.hasFocus() ? blurredIntervalMs : intervalMs;
  const schedule = () => {
    clear();
    if (stopped || running || document.hidden) return;
    timer = setTimeout(() => void tick(), delay());
  };
  const tick = async () => {
    clear();
    if (stopped || running || document.hidden) return;
    running = true;
    lastRunAt = Date.now();
    try {
      await run();
    } catch {
      // A failed refresh is retried on the next tick.
    } finally {
      running = false;
      schedule();
    }
  };
  const resume = () => {
    if (document.hidden) {
      clear();
      return;
    }
    if (Date.now() - lastRunAt >= RESUME_MIN_GAP_MS) void tick();
    else schedule();
  };

  document.addEventListener("visibilitychange", resume);
  window.addEventListener("focus", resume);
  window.addEventListener("blur", schedule);
  if (immediate) void tick();
  else schedule();

  return () => {
    stopped = true;
    clear();
    document.removeEventListener("visibilitychange", resume);
    window.removeEventListener("focus", resume);
    window.removeEventListener("blur", schedule);
  };
}

// usePolling is the hook form: it follows `enabled` and always calls the latest `run`.
export function usePolling(
  run: () => unknown,
  options: PollingOptions & { enabled?: boolean },
): void {
  const runRef = useRef(run);
  runRef.current = run;
  const { enabled = true, intervalMs, blurredIntervalMs, immediate } = options;
  useEffect(() => {
    if (!enabled) return;
    return startPolling(() => runRef.current(), { intervalMs, blurredIntervalMs, immediate });
  }, [enabled, intervalMs, blurredIntervalMs, immediate]);
}
