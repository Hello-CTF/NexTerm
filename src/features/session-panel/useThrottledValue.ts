import { useEffect, useRef, useState } from "react";

// useThrottledValue passes a changing value through at most once per interval: the first change shows at once, later
// changes inside the interval collapse into one trailing update with the latest value. A new resetKey (another
// terminal) passes its value through immediately, so the previous terminal's value never shows for the new one.
export function useThrottledValue<T>(value: T, intervalMs: number, resetKey: string): T {
  const [state, setState] = useState({ value, key: resetKey });
  const lastEmit = useRef(0);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const latest = useRef(value);
  latest.current = value;

  const keyChanged = state.key !== resetKey;
  if (keyChanged) {
    // Adjust during render so the stale value is never committed for the new key.
    setState({ value, key: resetKey });
  }

  useEffect(() => {
    if (keyChanged) return;
    if (Object.is(value, state.value)) return;
    const now = Date.now();
    const wait = lastEmit.current + intervalMs - now;
    if (wait <= 0) {
      lastEmit.current = now;
      setState({ value, key: resetKey });
      return;
    }
    if (timer.current !== null) return;
    timer.current = setTimeout(() => {
      timer.current = null;
      lastEmit.current = Date.now();
      setState({ value: latest.current, key: resetKey });
    }, wait);
  }, [value, state.value, intervalMs, resetKey, keyChanged]);

  useEffect(() => {
    if (timer.current !== null) clearTimeout(timer.current);
    timer.current = null;
    // A new terminal starts unthrottled: its first directory change shows at once.
    lastEmit.current = 0;
  }, [resetKey]);

  useEffect(
    () => () => {
      if (timer.current !== null) clearTimeout(timer.current);
    },
    [],
  );

  return keyChanged ? value : state.value;
}
