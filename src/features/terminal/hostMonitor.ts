import { useEffect, useSyncExternalStore } from "react";
import { hostMonitorApi, type HostMonitorSnapshot } from "../../ipc/hostMonitorApi";
import { describeError } from "../../ui/errorText";

export const DEFAULT_INTERVAL_MS = 3_000;
export const MAX_RETRY_MS = 60_000;
export const HISTORY_LIMIT = 40;

export interface HostMonitorPoint {
  ts: number;
  cpu: number | null;
  mem: number | null;
  // Bytes per second per interface name.
  net: Record<string, { rx: number | null; tx: number | null }>;
}

export interface HostMonitorState {
  snapshot: HostMonitorSnapshot | null;
  history: readonly HostMonitorPoint[];
  // The request itself failed (session gone, not allowed); the host's own failures arrive in the snapshot.
  error: string | null;
  // When the next request runs after a failure, in epoch milliseconds.
  retryAt: number | null;
}

const EMPTY: HostMonitorState = { snapshot: null, history: [], error: null, retryAt: null };

interface Poller {
  sessionId: string;
  refs: number;
  timer: ReturnType<typeof setTimeout> | null;
  failures: number;
  released: boolean;
  inflight: boolean;
  // When the next sample is due, in epoch milliseconds; null once the host is not asked again.
  dueAt: number | null;
  lastRunAt: number;
  state: HostMonitorState;
}

// Resuming from a hidden page samples at once unless a sample just ran.
const RESUME_MIN_GAP_MS = 1_000;

const pollers = new Map<string, Poller>();
const subscribers = new Map<string, Set<() => void>>();

function notify(sessionId: string) {
  for (const listener of [...(subscribers.get(sessionId) ?? [])]) listener();
}

function update(p: Poller, patch: Partial<HostMonitorState>) {
  p.state = { ...p.state, ...patch };
  notify(p.sessionId);
}

function pointOf(snapshot: HostMonitorSnapshot): HostMonitorPoint {
  const memory = snapshot.memory;
  const net: HostMonitorPoint["net"] = {};
  for (const iface of snapshot.network) net[iface.name] = { rx: iface.rxRate ?? null, tx: iface.txRate ?? null };
  return {
    ts: snapshot.sampledAt,
    cpu: snapshot.cpuPercent ?? null,
    mem: memory && memory.totalBytes > 0 ? (memory.usedBytes / memory.totalBytes) * 100 : null,
    net,
  };
}

function clearTimer(p: Poller) {
  if (p.timer) clearTimeout(p.timer);
  p.timer = null;
}

// Nothing runs on the host while the page is hidden (minimized window, background tab); the due time is kept so
// becoming visible again resumes the poller, and a backoff still waits out its remaining delay.
function arm(p: Poller) {
  clearTimer(p);
  if (p.released || p.inflight || p.dueAt === null || document.hidden) return;
  p.timer = setTimeout(() => void poll(p), Math.max(0, p.dueAt - Date.now()));
}

function schedule(p: Poller, delay: number) {
  p.dueAt = Date.now() + delay;
  arm(p);
}

function resumeAll() {
  for (const p of pollers.values()) {
    if (document.hidden) {
      clearTimer(p);
      continue;
    }
    if (p.released || p.inflight || p.dueAt === null || p.timer) continue;
    // A poller that fell due while hidden samples now, unless it ran a moment ago.
    if (p.dueAt <= Date.now() && Date.now() - p.lastRunAt < RESUME_MIN_GAP_MS) p.dueAt = p.lastRunAt + RESUME_MIN_GAP_MS;
    arm(p);
  }
}

let listening = false;

function listen(on: boolean) {
  if (on === listening || typeof document === "undefined") return;
  listening = on;
  const method = on ? "addEventListener" : "removeEventListener";
  document[method]("visibilitychange", resumeAll);
  window[method]("focus", resumeAll);
}

export function retryDelay(failures: number): number {
  return Math.min(DEFAULT_INTERVAL_MS * 2 ** failures, MAX_RETRY_MS);
}

async function poll(p: Poller) {
  p.timer = null;
  if (p.released || p.inflight) return;
  if (document.hidden) return;
  p.inflight = true;
  p.lastRunAt = Date.now();
  try {
    const snapshot = await hostMonitorApi.sample(p.sessionId);
    p.inflight = false;
    if (p.released) return;
    p.failures = 0;
    if (snapshot.status === "ok") {
      const history = [...p.state.history, pointOf(snapshot)].slice(-HISTORY_LIMIT);
      update(p, { snapshot, history, error: null, retryAt: null });
      schedule(p, snapshot.intervalMs > 0 ? snapshot.intervalMs : DEFAULT_INTERVAL_MS);
      return;
    }
    if (snapshot.status === "unsupported" || snapshot.intervalMs <= 0) {
      // An unsupported host is never asked again while the monitor stays open.
      p.dueAt = null;
      update(p, { snapshot, error: null, retryAt: null });
      return;
    }
    const wait = snapshot.retryAfterMs && snapshot.retryAfterMs > 0 ? snapshot.retryAfterMs : snapshot.intervalMs;
    update(p, { snapshot, error: null, retryAt: Date.now() + wait });
    schedule(p, wait);
  } catch (error) {
    p.inflight = false;
    if (p.released) return;
    p.failures += 1;
    const wait = retryDelay(p.failures);
    update(p, { error: describeError(error), retryAt: Date.now() + wait });
    schedule(p, wait);
  }
}

// One poller serves every view of a session; the last release stops it and drops its history.
export function acquireHostMonitor(sessionId: string): () => void {
  let p = pollers.get(sessionId);
  if (!p) {
    p = { sessionId, refs: 0, timer: null, failures: 0, released: false, inflight: false, dueAt: Date.now(), lastRunAt: 0, state: EMPTY };
    pollers.set(sessionId, p);
    listen(true);
    arm(p);
  }
  p.refs += 1;
  const owner = p;
  let released = false;
  return () => {
    if (released) return;
    released = true;
    owner.refs -= 1;
    if (owner.refs > 0) return;
    owner.released = true;
    clearTimer(owner);
    if (pollers.get(sessionId) === owner) pollers.delete(sessionId);
    if (pollers.size === 0) listen(false);
    owner.state = EMPTY;
    notify(sessionId);
  };
}

function subscribe(sessionId: string, listener: () => void): () => void {
  let set = subscribers.get(sessionId);
  if (!set) subscribers.set(sessionId, (set = new Set()));
  set.add(listener);
  return () => {
    set.delete(listener);
    if (set.size === 0 && subscribers.get(sessionId) === set) subscribers.delete(sessionId);
  };
}

export function getHostMonitorState(sessionId: string): HostMonitorState {
  return pollers.get(sessionId)?.state ?? EMPTY;
}

// Samples only while active: the monitor is on, the sidebar is shown and the session is connected.
export function useHostMonitor(sessionId: string | undefined, active: boolean): HostMonitorState {
  const running = active && Boolean(sessionId);
  useEffect(() => (running && sessionId ? acquireHostMonitor(sessionId) : undefined), [running, sessionId]);
  return useSyncExternalStore(
    (listener) => (sessionId ? subscribe(sessionId, listener) : () => undefined),
    () => (running && sessionId ? getHostMonitorState(sessionId) : EMPTY),
  );
}
