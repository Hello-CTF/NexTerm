import { awarenessBadge, tabAwareness } from "../sessionAwarenessStore";
import type { JumpAttention } from "./jumpRanking";
import type { JumpSessionEntry } from "./jumpSessions";

// jumpAttentionOf reads the session's awareness state for ranking. Only unseen needs_me and just_finished states
// rank ahead; everything else, including unknown, is "other".
export function jumpAttentionOf(entry: JumpSessionEntry): JumpAttention | undefined {
  if (!entry.terminalId) return undefined;
  const badge = awarenessBadge(tabAwareness(entry.terminalId));
  if (badge?.kind === "needs_me") return "needsMe";
  if (badge?.kind === "just_finished") return "justEnded";
  return undefined;
}
