import { create } from "zustand";
import { terminalApi } from "../ipc/commands";
import { EVENTS, listenEvent, type TerminalSessionsEvent } from "../ipc/events";
import type { AggregateHostStatus, AggregateList, AggregateSession } from "../ipc/generated/types";
import { onEventsResync } from "../ipc/webTransport";
import { startPolling } from "./usePolling";

// The durable sessions of every host this client can reach, as the backend aggregates them: connected SSH and device
// sessions, the local helper, and the account's devices through the server even when no workspace here holds them.
// It covers sessions the awareness store does not know, so the inbox and the background list can show a session
// that needs the user on a host this client never opened.

// The first answer waits this long for slow hosts; the rest arrive as terminal://sessions events.
export const AGGREGATE_WAIT_MS = 1500;
export const AGGREGATE_REFRESH_MS = 30_000;

export interface AggregateRow {
  session: AggregateSession;
  host: AggregateHostStatus;
}

interface SessionAggregateState {
  // Keyed by host key, in the backend's order.
  hosts: ReadonlyMap<string, AggregateHostStatus>;
  sessionsByHost: ReadonlyMap<string, readonly AggregateSession[]>;
  loaded: boolean;
}

const EMPTY: SessionAggregateState = { hosts: new Map(), sessionsByHost: new Map(), loaded: false };

export const useSessionAggregate = create<SessionAggregateState>(() => EMPTY);

let started = false;
// epoch discards replies an account reset or a newer reload has overtaken.
let epoch = 0;
let inFlight = false;

export function applyAggregateList(list: AggregateList): void {
  const hosts = new Map<string, AggregateHostStatus>();
  const sessionsByHost = new Map<string, AggregateSession[]>();
  for (const host of list.hosts) hosts.set(host.key, host);
  for (const session of list.sessions) {
    const group = sessionsByHost.get(session.hostKey) ?? [];
    group.push(session);
    sessionsByHost.set(session.hostKey, group);
  }
  useSessionAggregate.setState({ hosts, sessionsByHost, loaded: true });
}

// applyAggregateUpdate replaces what is known of one host. A host the current listing no longer has is ignored:
// its session or device went away since.
export function applyAggregateUpdate(update: TerminalSessionsEvent): void {
  useSessionAggregate.setState((current) => {
    if (!current.hosts.has(update.host.key)) return current;
    const hosts = new Map(current.hosts);
    hosts.set(update.host.key, update.host);
    const sessionsByHost = new Map(current.sessionsByHost);
    sessionsByHost.set(update.host.key, update.sessions);
    return { hosts, sessionsByHost, loaded: current.loaded };
  });
}

// aggregateRows lists every aggregated session with its host, each session once.
export function aggregateRows(state: Pick<SessionAggregateState, "hosts" | "sessionsByHost">): AggregateRow[] {
  const rows: AggregateRow[] = [];
  const seen = new Set<string>();
  for (const [key, host] of state.hosts) {
    for (const session of state.sessionsByHost.get(key) ?? []) {
      if (seen.has(session.tabId)) continue;
      seen.add(session.tabId);
      rows.push({ session, host });
    }
  }
  return rows;
}

export async function reloadSessionAggregate(waitMs = AGGREGATE_WAIT_MS): Promise<void> {
  if (inFlight) return;
  inFlight = true;
  const current = epoch;
  try {
    const list = await terminalApi.sessionsAggregate(waitMs);
    if (current === epoch) applyAggregateList(list);
  } catch {
    // The listing is a supplement; the inbox keeps what the awareness store knows.
  } finally {
    inFlight = false;
  }
}

export function startSessionAggregateTracking(): void {
  if (started) return;
  started = true;
  void listenEvent<TerminalSessionsEvent>(EVENTS.terminalSessions, applyAggregateUpdate);
  onEventsResync(() => void reloadSessionAggregate());
  // Polling stops while the app is hidden and reloads at once when it comes back, as a phone does when opened.
  startPolling(() => reloadSessionAggregate(), { intervalMs: AGGREGATE_REFRESH_MS });
}

// resetSessionAggregate drops everything listed for the previous account and lists the current one's.
export function resetSessionAggregate(): void {
  epoch += 1;
  inFlight = false;
  useSessionAggregate.setState(EMPTY);
  if (started) void reloadSessionAggregate();
}

export function resetSessionAggregateForTest(): void {
  epoch += 1;
  inFlight = false;
  useSessionAggregate.setState(EMPTY);
}
