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
// An answer with hosts still pending is asked again after this long, for hosts whose result arrives as no event
// (devices a desktop or phone lists through its server).
export const AGGREGATE_FOLLOW_UP_MS = 1000;

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
let inFlightLoad: Promise<void> | null = null;
// Events that arrive while a reload is in flight are applied after its reply, which may predate them.
let buffered: TerminalSessionsEvent[] | null = null;
let followUp: ReturnType<typeof setTimeout> | undefined;
// Sessions this client ended (forgetAggregateSession), newest last; a listing read before the end still names them.
const ENDED_LIMIT = 256;
const ended = new Set<string>();

function withoutEnded(sessions: readonly AggregateSession[]): readonly AggregateSession[] {
  return ended.size > 0 && sessions.some((session) => ended.has(session.tabId))
    ? sessions.filter((session) => !ended.has(session.tabId))
    : sessions;
}

// newer: the known state of a host was learned after the incoming one, which then must not replace it. Every answer
// (a listing, a failure, offline) carries when it was learned; a pending host carries none and replaces no answer.
function newer(a: AggregateHostStatus | undefined, b: AggregateHostStatus): boolean {
  return a !== undefined && (a.answeredAt ?? 0) > (b.answeredAt ?? 0);
}

// applyAggregateList replaces the listing with a reply. A host whose known listing was read after the reply's keeps
// it: an event delivered it while the request was in flight.
export function applyAggregateList(list: AggregateList): void {
  useSessionAggregate.setState((current) => {
    const hosts = new Map<string, AggregateHostStatus>();
    const sessionsByHost = new Map<string, readonly AggregateSession[]>();
    const replied = new Map<string, AggregateSession[]>();
    for (const session of list.sessions) {
      const group = replied.get(session.hostKey) ?? [];
      group.push(session);
      replied.set(session.hostKey, group);
    }
    for (const host of list.hosts) {
      const known = current.hosts.get(host.key);
      if (known && newer(known, host)) {
        hosts.set(host.key, known);
        sessionsByHost.set(host.key, current.sessionsByHost.get(host.key) ?? []);
        continue;
      }
      hosts.set(host.key, host);
      sessionsByHost.set(host.key, withoutEnded(replied.get(host.key) ?? []));
    }
    return { hosts, sessionsByHost, loaded: true };
  });
}

// applyAggregateUpdate replaces what is known of one host, unless what is known was read later.
export function applyAggregateUpdate(update: TerminalSessionsEvent): void {
  if (buffered) {
    buffered.push(update);
    return;
  }
  useSessionAggregate.setState((current) => {
    if (newer(current.hosts.get(update.host.key), update.host)) return current;
    const hosts = new Map(current.hosts);
    hosts.set(update.host.key, update.host);
    const sessionsByHost = new Map(current.sessionsByHost);
    sessionsByHost.set(update.host.key, withoutEnded(update.sessions));
    return { hosts, sessionsByHost, loaded: current.loaded };
  });
}

// forgetAggregateSession drops a session this client just ended. Terminal ids are never reused, so a reply or a
// buffered event that still lists it was read before the end (a host cache, or a reload in flight) and it stays out.
export function forgetAggregateSession(tabId: string): void {
  ended.add(tabId);
  if (ended.size > ENDED_LIMIT) ended.delete(ended.values().next().value as string);
  useSessionAggregate.setState((current) => {
    let sessionsByHost: Map<string, readonly AggregateSession[]> | undefined;
    for (const [key, sessions] of current.sessionsByHost) {
      if (!sessions.some((session) => session.tabId === tabId)) continue;
      sessionsByHost ??= new Map(current.sessionsByHost);
      sessionsByHost.set(key, sessions.filter((session) => session.tabId !== tabId));
    }
    return sessionsByHost ? { ...current, sessionsByHost } : current;
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

function clearFollowUp(): void {
  if (followUp !== undefined) clearTimeout(followUp);
  followUp = undefined;
}

// reloadSessionAggregate lists every host again. A call while a listing is in flight waits for that one instead.
export function reloadSessionAggregate(waitMs = AGGREGATE_WAIT_MS): Promise<void> {
  if (inFlight && inFlightLoad) return inFlightLoad;
  inFlightLoad = runReload(waitMs);
  return inFlightLoad;
}

async function runReload(waitMs: number): Promise<void> {
  if (inFlight) return;
  inFlight = true;
  clearFollowUp();
  const current = epoch;
  buffered = [];
  let list: AggregateList | undefined;
  try {
    list = await terminalApi.sessionsAggregate(waitMs);
  } catch {
    // The listing is a supplement; the inbox keeps what the awareness store knows.
  }
  if (current !== epoch) return;
  inFlight = false;
  const pending = buffered;
  buffered = null;
  if (list) applyAggregateList(list);
  for (const update of pending) applyAggregateUpdate(update);
  if (list && list.hosts.some((host) => host.status === "pending")) {
    followUp = setTimeout(() => {
      followUp = undefined;
      void reloadSessionAggregate();
    }, AGGREGATE_FOLLOW_UP_MS);
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

function resetState(): void {
  epoch += 1;
  inFlight = false;
  buffered = null;
  ended.clear();
  clearFollowUp();
  useSessionAggregate.setState(EMPTY);
}

// resetSessionAggregate drops everything listed for the previous account and lists the current one's.
export function resetSessionAggregate(): void {
  resetState();
  if (started) void reloadSessionAggregate();
}

export function resetSessionAggregateForTest(): void {
  resetState();
}
