// Per-conversation roster snapshot shared between the roster panel's own
// loads and the tool cards' roster join. Every entry comes from a real
// ai_roster_list through the production adapter — a failed or stale refresh
// never fabricates rows, and a response for a conversation that is no longer
// current is discarded.

import { useSyncExternalStore } from "react";
import type { AgentRosterApi } from "./agentApi";
import type { AgentRosterEntry } from "./agentTypes";

interface RosterSnapshot {
  conversationId: string;
  entries: AgentRosterEntry[];
  version: number;
}

let snapshot: RosterSnapshot | null = null;
let requestedConversationId: string | null = null;
let inflight: Promise<void> | null = null;
let inflightFor: string | null = null;
let inflightToken: symbol | null = null;
const listeners = new Set<() => void>();

function notify(): void {
  for (const listener of listeners) listener();
}

export function agentRosterSnapshotEntries(conversationId: string | undefined): AgentRosterEntry[] | null {
  if (conversationId === undefined || snapshot === null || snapshot.conversationId !== conversationId) {
    return null;
  }
  return snapshot.entries;
}

export function useAgentRosterSnapshot(conversationId: string | undefined): AgentRosterEntry[] | null {
  return useSyncExternalStore(
    (onChange) => {
      listeners.add(onChange);
      return () => listeners.delete(onChange);
    },
    () => agentRosterSnapshotEntries(conversationId),
  );
}

// refreshAgentRosterSnapshot reloads the conversation's roster through the
// production adapter. Loads for a superseded conversation never land, a
// failed load keeps the previous snapshot (the cards degrade to unknown
// rather than claiming rows), and concurrent callers for the same
// conversation share one in-flight load.
export function refreshAgentRosterSnapshot(api: AgentRosterApi, conversationId: string): Promise<void> {
  requestedConversationId = conversationId;
  if (inflightFor === conversationId && inflight) return inflight;
  const load: Promise<void> = (async () => {
    const token = Symbol(conversationId);
    inflightToken = token;
    try {
      const entries = await api.list();
      if (requestedConversationId !== conversationId) return;
      snapshot = {
        conversationId,
        entries,
        version: (snapshot?.version ?? 0) + 1,
      };
      notify();
    } catch {
      // A failed refresh leaves the previous snapshot untouched; the roster
      // panel surfaces its own load error when open.
    } finally {
      if (inflightToken === token) {
        inflight = null;
        inflightFor = null;
        inflightToken = null;
      }
    }
  })();
  inflight = load;
  inflightFor = conversationId;
  return load;
}

export function clearAgentRosterSnapshots(): void {
  snapshot = null;
  requestedConversationId = null;
  notify();
}
