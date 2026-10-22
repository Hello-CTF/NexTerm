// Per-conversation roster snapshots shared between each roster panel's own
// loads and the tool cards' roster join. Every conversation gets an
// independent snapshot and in-flight slot: the AiSidebar keeps every AI tab
// mounted (only hidden when inactive), so one conversation's refresh must
// never evict another's rows. Every entry comes from a real ai_roster_list
// through the production adapter — a failed load leaves the slot empty
// (unknown) rather than fabricating rows.

import { useSyncExternalStore } from "react";
import type { AgentRosterApi } from "./agentApi";
import type { AgentRosterEntry } from "./agentTypes";

interface RosterSnapshot {
  entries: AgentRosterEntry[];
  version: number;
}

const snapshots = new Map<string, RosterSnapshot>();
const inflight = new Map<string, { promise: Promise<void>; token: symbol }>();
const listeners = new Set<() => void>();

function notify(): void {
  for (const listener of listeners) listener();
}

function entriesOf(conversationId: string | undefined): AgentRosterEntry[] | null {
  if (conversationId === undefined) return null;
  return snapshots.get(conversationId)?.entries ?? null;
}

export function useAgentRosterSnapshot(conversationId: string | undefined): AgentRosterEntry[] | null {
  return useSyncExternalStore(
    (onChange) => {
      listeners.add(onChange);
      return () => listeners.delete(onChange);
    },
    () => entriesOf(conversationId),
  );
}

// refreshAgentRosterSnapshot reloads one conversation's roster through the
// production adapter into that conversation's own slot; concurrent callers
// for the same conversation share the in-flight load.
export function refreshAgentRosterSnapshot(api: AgentRosterApi, conversationId: string): Promise<void> {
  const existing = inflight.get(conversationId);
  if (existing) return existing.promise;
  const token = Symbol(conversationId);
  const promise = (async () => {
    try {
      const entries = await api.list();
      snapshots.set(conversationId, {
        entries,
        version: (snapshots.get(conversationId)?.version ?? 0) + 1,
      });
      notify();
    } catch {
      // A failed refresh leaves the slot as it was (possibly empty): the
      // cards degrade to unknown rather than claiming rows, and the roster
      // panel surfaces its own load error when open.
    } finally {
      if (inflight.get(conversationId)?.token === token) inflight.delete(conversationId);
    }
  })();
  inflight.set(conversationId, { promise, token });
  return promise;
}

export function clearAgentRosterSnapshots(): void {
  snapshots.clear();
  inflight.clear();
  notify();
}
