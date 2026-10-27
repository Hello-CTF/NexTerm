import { create } from "zustand";
import type { TerminalAttentionEvent } from "../ipc/events";

// The attention state of a terminal tab, as the backend reports it. Helper tabs carry the helper's state; other tabs
// a state the backend derived from their output (source "observed"). "unknown" is never a guess: the tab is not
// connected or its helper reports nothing.
export type AwarenessState = "running" | "needs_me" | "just_finished" | "idle" | "exited" | "unknown";

export interface SessionAwareness {
  state: AwarenessState;
  // An inferred state came from the screen shape rather than a terminal signal; it is shown lighter.
  inferred: boolean;
  source?: string;
  reason?: string;
  since?: string;
  seen: boolean;
  notable: boolean;
  exitCode?: number | null;
  version: number;
}

const STATES: ReadonlySet<string> = new Set(["running", "needs_me", "just_finished", "idle", "exited", "unknown"]);

interface AwarenessStoreState {
  // Keyed by the backend terminal tab id.
  byTabId: ReadonlyMap<string, SessionAwareness>;
}

// A store of its own: attention changes arrive often and must not re-render the UI store's subscribers.
export const useAwarenessStore = create<AwarenessStoreState>(() => ({ byTabId: new Map() }));

export function toAwareness(event: TerminalAttentionEvent): SessionAwareness {
  return {
    // A state this client does not know is shown as unknown rather than guessed.
    state: STATES.has(event.state) ? (event.state as AwarenessState) : "unknown",
    inferred: event.confidence === "heuristic",
    source: event.source,
    reason: event.reason,
    since: event.since,
    seen: event.seen === true,
    notable: event.notable === true,
    exitCode: event.exitCode,
    version: event.version,
  };
}

// applyAwareness keeps, per tab, the event with the highest version: events and list replies may arrive out of order.
// A list entry without a version (a tab whose state was never published) only fills a tab nothing is known about.
// Versions restart with the backend process, so a full reload starts from an empty store (resetAwarenessStore).
export function applyAwareness(events: readonly TerminalAttentionEvent[]): void {
  if (events.length === 0) return;
  useAwarenessStore.setState((current) => {
    let next: Map<string, SessionAwareness> | undefined;
    for (const event of events) {
      if (!event.tabId) continue;
      const known = (next ?? current.byTabId).get(event.tabId);
      if (known && (event.version < known.version || (event.version === known.version && event.version > 0))) continue;
      next ??= new Map(current.byTabId);
      next.set(event.tabId, toAwareness(event));
    }
    return next ? { byTabId: next } : current;
  });
}

// markAwarenessSeenLocally marks a state seen on this device only, for a helper that cannot share the mark. The next
// state the backend reports replaces it.
export function markAwarenessSeenLocally(tabId: string, since: string): void {
  useAwarenessStore.setState((current) => {
    const known = current.byTabId.get(tabId);
    if (!known || known.seen || known.since !== since) return current;
    const next = new Map(current.byTabId);
    next.set(tabId, { ...known, seen: true });
    return { byTabId: next };
  });
}

export function resetAwarenessStore(): void {
  useAwarenessStore.setState((current) => (current.byTabId.size === 0 ? current : { byTabId: new Map() }));
}

export function useTabAwareness(tabId: string | undefined): SessionAwareness | undefined {
  return useAwarenessStore((state) => (tabId ? state.byTabId.get(tabId) : undefined));
}

export function tabAwareness(tabId: string): SessionAwareness | undefined {
  return useAwarenessStore.getState().byTabId.get(tabId);
}

// needsAttention: the user has not yet seen a state that asks for them.
export function needsAttention(awareness: SessionAwareness | undefined): boolean {
  return (
    awareness !== undefined &&
    !awareness.seen &&
    (awareness.state === "needs_me" || awareness.state === "just_finished")
  );
}

export type AwarenessBadgeKind = "needs_me" | "just_finished" | "running" | "unknown";

export interface AwarenessBadge {
  kind: AwarenessBadgeKind;
  inferred: boolean;
  label: string;
  // outcome is set on just_finished only: "failed" for a nonzero exit code, "ok" for exit 0 or an unknown code.
  outcome?: "ok" | "failed";
}

// awarenessBadge decides what a tab shows. Idle and exited tabs show nothing (an exited tab is marked already), and a
// needs_me or just_finished state that was seen on any device shows nothing either.
export function awarenessBadge(awareness: SessionAwareness | undefined): AwarenessBadge | null {
  if (!awareness) return null;
  switch (awareness.state) {
    case "needs_me":
      if (awareness.seen) return null;
      return awareness.inferred
        ? { kind: "needs_me", inferred: true, label: "可能需要你处理（根据屏幕推测）" }
        : { kind: "needs_me", inferred: false, label: "需要你处理" };
    case "just_finished": {
      if (awareness.seen) return null;
      const code = awareness.exitCode;
      const result = typeof code === "number" ? (code === 0 ? "，成功" : `，退出码 ${code}`) : "";
      const outcome = typeof code === "number" && code !== 0 ? "failed" : "ok";
      return { kind: "just_finished", inferred: awareness.inferred, label: `命令刚结束${result}`, outcome };
    }
    case "running":
      return { kind: "running", inferred: awareness.inferred, label: "运行中" };
    case "unknown":
      return { kind: "unknown", inferred: false, label: "状态未知：未连接或无法获取" };
    default:
      return null;
  }
}
