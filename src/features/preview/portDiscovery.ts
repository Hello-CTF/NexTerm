import { useEffect, useSyncExternalStore } from "react";
import { useTerminalPrefs } from "../../app/preferences";
import { useSessionById } from "../../app/store";
import { hostPortsApi, type HostListener } from "../../ipc/hostPortsApi";

export const POLL_INTERVAL_MS = 10_000;
export const QUIET_MS = 2_000;
export const MAX_FAILURES = 3;

export interface DiscoveredPort {
  address: string;
  port: number;
}

export interface PortDiscoverySnapshot {
  // New listeners seen since the baseline, minus the ones the user dismissed.
  fresh: readonly DiscoveredPort[];
  // Every listener of the latest poll, used to choose the dial address.
  all: readonly DiscoveredPort[];
}

const EMPTY: PortDiscoverySnapshot = { fresh: [], all: [] };

interface Poller {
  refs: number;
  timer: ReturnType<typeof setInterval> | null;
  inflight: boolean;
  failures: number;
  stopped: boolean;
  baseline: Set<string> | null;
  newKeys: Set<string>;
  dismissedPorts: Set<number>;
  latest: readonly DiscoveredPort[];
  snapshot: PortDiscoverySnapshot;
  sessionId: string;
}

const pollers = new Map<string, Poller>();
const subscribers = new Map<string, Set<() => void>>();

function notify(sessionId: string) {
  for (const listener of [...(subscribers.get(sessionId) ?? [])]) listener();
}

const keyOf = (l: DiscoveredPort) => `${l.address}|${l.port}`;

function rebuild(p: Poller) {
  p.snapshot = {
    fresh: p.latest.filter((l) => p.newKeys.has(keyOf(l)) && !p.dismissedPorts.has(l.port)),
    all: p.latest,
  };
  notify(p.sessionId);
}

function apply(p: Poller, listeners: readonly HostListener[]) {
  const current = listeners.map((l) => ({ address: l.address, port: l.port }));
  const keys = new Set(current.map(keyOf));
  if (p.baseline === null) {
    p.baseline = keys;
  } else {
    for (const k of keys) if (!p.baseline.has(k)) p.newKeys.add(k);
    for (const k of [...p.newKeys]) if (!keys.has(k)) p.newKeys.delete(k);
    // A port that closed and comes back is announced again.
    const open = new Set(current.map((l) => l.port));
    for (const port of [...p.dismissedPorts]) if (!open.has(port)) p.dismissedPorts.delete(port);
  }
  p.latest = current;
  rebuild(p);
}

async function poll(sessionId: string, p: Poller) {
  if (p.inflight || p.stopped) return;
  p.inflight = true;
  try {
    const result = await hostPortsApi.list({ sessionId, quietMs: QUIET_MS });
    if (pollers.get(sessionId) !== p) return;
    p.failures = 0;
    if (!result.skipped) apply(p, result.listeners ?? []);
  } catch {
    if (pollers.get(sessionId) !== p) return;
    p.failures += 1;
    if (p.failures >= MAX_FAILURES) {
      p.stopped = true;
      if (p.timer) clearInterval(p.timer);
      p.timer = null;
    }
  } finally {
    p.inflight = false;
  }
}

// One poller serves every terminal of a session; the last release stops it and drops its state.
export function acquirePortDiscovery(sessionId: string): () => void {
  let p = pollers.get(sessionId);
  if (!p) {
    p = {
      refs: 0,
      timer: null,
      inflight: false,
      failures: 0,
      stopped: false,
      baseline: null,
      newKeys: new Set(),
      dismissedPorts: new Set(),
      latest: [],
      snapshot: EMPTY,
      sessionId,
    };
    pollers.set(sessionId, p);
    const created = p;
    created.timer = setInterval(() => void poll(sessionId, created), POLL_INTERVAL_MS);
    void poll(sessionId, created);
  }
  p.refs += 1;
  const owner = p;
  let released = false;
  return () => {
    if (released) return;
    released = true;
    owner.refs -= 1;
    if (owner.refs > 0) return;
    if (owner.timer) clearInterval(owner.timer);
    owner.timer = null;
    if (pollers.get(sessionId) === owner) pollers.delete(sessionId);
    owner.snapshot = EMPTY;
    notify(sessionId);
  };
}

export function dismissDiscoveredPorts(sessionId: string, ports: readonly number[]) {
  const p = pollers.get(sessionId);
  if (!p) return;
  for (const port of ports) p.dismissedPorts.add(port);
  rebuild(p);
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

export function getPortDiscoverySnapshot(sessionId: string): PortDiscoverySnapshot {
  return pollers.get(sessionId)?.snapshot ?? EMPTY;
}

// Polls only while the preference is on and the session is connected.
export function usePortDiscovery(sessionId: string): PortDiscoverySnapshot {
  const enabled = useTerminalPrefs().portDiscovery === "on";
  const connected = useSessionById(sessionId)?.status === "connected";
  const active = enabled && connected;
  useEffect(() => (active ? acquirePortDiscovery(sessionId) : undefined), [active, sessionId]);
  return useSyncExternalStore(
    (listener) => subscribe(sessionId, listener),
    () => (active ? getPortDiscoverySnapshot(sessionId) : EMPTY),
  );
}
