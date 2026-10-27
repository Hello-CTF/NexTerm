export type JumpSection = "session" | "host" | "command";

// JumpAttention mirrors the session awareness states the jump palette ranks by; unknown ranks as "other".
export type JumpAttention = "needsMe" | "justEnded" | "other";

export interface JumpCandidate {
  section: JumpSection;
  label: string;
  // keywords are extra searchable fields, such as host, user, tags or a hint.
  keywords?: readonly (string | null | undefined)[];
  // recency is larger for more recent use; 0 means unknown or never used.
  recency?: number;
}

export interface JumpRankOptions<T> {
  // attentionOf looks up an item's attention state; without it, items rank by recency alone.
  attentionOf?: (item: T) => JumpAttention | undefined;
}

const SECTION_ORDER: Record<JumpSection, number> = { session: 0, host: 1, command: 2 };
const ATTENTION_ORDER: Record<JumpAttention, number> = { needsMe: 0, justEnded: 1, other: 2 };

function isSubsequence(text: string, needle: string): boolean {
  let at = 0;
  for (const char of text) {
    if (char === needle[at]) at += 1;
    if (at === needle.length) return true;
  }
  return needle.length === 0;
}

function tokenTier(label: string, keywords: string[], token: string): number | null {
  if (label.startsWith(token)) return 0;
  if (label.includes(token)) return 1;
  if (keywords.some((keyword) => keyword.includes(token))) return 2;
  if (isSubsequence(label, token)) return 3;
  if (keywords.some((keyword) => isSubsequence(keyword, token))) return 4;
  return null;
}

// jumpMatchTier scores a candidate against a query; lower is better and null means no match. Every
// whitespace-separated token must match, by name prefix, name substring, field substring, then as a
// fuzzy subsequence of the name or of a field. The worst token decides the tier.
export function jumpMatchTier(candidate: JumpCandidate, query: string): number | null {
  const tokens = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (tokens.length === 0) return 0;
  const label = candidate.label.toLowerCase();
  const keywords = (candidate.keywords ?? [])
    .filter((keyword): keyword is string => Boolean(keyword))
    .map((keyword) => keyword.toLowerCase());
  let worst = 0;
  for (const token of tokens) {
    const tier = tokenTier(label, keywords, token);
    if (tier === null) return null;
    worst = Math.max(worst, tier);
  }
  return worst;
}

// rankJumpItems filters and orders palette candidates. An empty query groups by section (sessions,
// hosts, commands); a query orders by match quality first. Ties go to items that need attention when a
// lookup is given, then to more recent items, then keep the input order.
export function rankJumpItems<T extends JumpCandidate>(
  items: readonly T[],
  query: string,
  options: JumpRankOptions<T> = {},
): T[] {
  const hasQuery = query.trim().length > 0;
  const scored: { item: T; tier: number; index: number; attention: number }[] = [];
  items.forEach((item, index) => {
    const tier = jumpMatchTier(item, query);
    if (tier === null) return;
    scored.push({ item, tier, index, attention: ATTENTION_ORDER[options.attentionOf?.(item) ?? "other"] });
  });
  scored.sort(
    (a, b) =>
      (hasQuery ? a.tier - b.tier : 0) ||
      SECTION_ORDER[a.item.section] - SECTION_ORDER[b.item.section] ||
      a.attention - b.attention ||
      (b.item.recency ?? 0) - (a.item.recency ?? 0) ||
      a.index - b.index,
  );
  return scored.map((entry) => entry.item);
}

export interface JumpHostLike {
  id: string;
  name: string;
  host: string | null;
  username: string | null;
  tags: string;
  kind: string;
}

// hostCandidates turns assets into host candidates: searchable by name, host, user and tags, ranked by
// connection history, and in name order when nothing else separates them.
export function hostCandidates<T extends JumpHostLike>(
  assets: readonly T[],
  recencyOf: (asset: T) => number,
): (JumpCandidate & { asset: T })[] {
  return [...assets]
    .sort((a, b) => (a.name === b.name ? 0 : a.name < b.name ? -1 : 1))
    .map((asset) => ({
      section: "host",
      label: asset.name,
      keywords: [asset.host, asset.username, asset.tags, asset.kind],
      recency: recencyOf(asset),
      asset,
    }));
}

// freezeOrder keeps the rows already shown in their shown order, drops the ones that are gone and appends new
// ones in ranked order. Without a previous order it returns the ranked order.
export function freezeOrder(previous: readonly string[] | null, ranked: readonly string[]): string[] {
  if (!previous) return [...ranked];
  const present = new Set(ranked);
  const kept = previous.filter((key) => present.has(key));
  const seen = new Set(kept);
  return [...kept, ...ranked.filter((key) => !seen.has(key))];
}
