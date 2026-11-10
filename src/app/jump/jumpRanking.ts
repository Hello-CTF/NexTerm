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

// subsequencePositions returns the code-unit index of each needle character in text, matched greedily left to
// right, or null when needle is not a subsequence of text.
function subsequencePositions(text: string, needle: string): number[] | null {
  if (needle.length === 0) return [];
  const positions: number[] = [];
  let index = 0;
  for (const char of text) {
    if (char === needle[positions.length]) positions.push(index);
    if (positions.length === needle.length) return positions;
    index += char.length;
  }
  return null;
}

export type MatchRange = readonly [start: number, end: number];

interface TokenMatch {
  tier: number;
  // ranges are the matched spans of the label; a match found in a field has none.
  ranges: MatchRange[];
}

function tokenMatch(label: string, keywords: string[], token: string): TokenMatch | null {
  if (label.startsWith(token)) return { tier: 0, ranges: [[0, token.length]] };
  const at = label.indexOf(token);
  if (at >= 0) return { tier: 1, ranges: [[at, at + token.length]] };
  if (keywords.some((keyword) => keyword.includes(token))) return { tier: 2, ranges: [] };
  const positions = subsequencePositions(label, token);
  if (positions) return { tier: 3, ranges: positions.map((position) => [position, position + 1] as const) };
  if (keywords.some((keyword) => subsequencePositions(keyword, token) !== null)) return { tier: 4, ranges: [] };
  return null;
}

function queryTokens(query: string): string[] {
  return query.trim().toLowerCase().split(/\s+/).filter(Boolean);
}

function lowerKeywords(candidate: Pick<JumpCandidate, "keywords">): string[] {
  return (candidate.keywords ?? [])
    .filter((keyword): keyword is string => Boolean(keyword))
    .map((keyword) => keyword.toLowerCase());
}

// jumpMatchTier scores a candidate against a query; lower is better and null means no match. Every
// whitespace-separated token must match, by name prefix, name substring, field substring, then as a
// fuzzy subsequence of the name or of a field. The worst token decides the tier.
export function jumpMatchTier(candidate: JumpCandidate, query: string): number | null {
  const tokens = queryTokens(query);
  if (tokens.length === 0) return 0;
  const label = candidate.label.toLowerCase();
  const keywords = lowerKeywords(candidate);
  let worst = 0;
  for (const token of tokens) {
    const match = tokenMatch(label, keywords, token);
    if (match === null) return null;
    worst = Math.max(worst, match.tier);
  }
  return worst;
}

// jumpMatchRanges returns the spans of the label that the query matched, by the same rules as jumpMatchTier,
// sorted and merged. Tokens matched only in a field contribute nothing; a non-matching query returns none.
export function jumpMatchRanges(candidate: Pick<JumpCandidate, "label" | "keywords">, query: string): MatchRange[] {
  const tokens = queryTokens(query);
  const label = candidate.label.toLowerCase();
  // Lowercasing can change the length of a few characters, which would shift the offsets.
  if (tokens.length === 0 || label.length !== candidate.label.length) return [];
  const keywords = lowerKeywords(candidate);
  const ranges: MatchRange[] = [];
  for (const token of tokens) {
    const match = tokenMatch(label, keywords, token);
    if (match === null) return [];
    ranges.push(...match.ranges);
  }
  ranges.sort((a, b) => a[0] - b[0] || a[1] - b[1]);
  const merged: [number, number][] = [];
  for (const [start, end] of ranges) {
    const last = merged[merged.length - 1];
    if (last && start <= last[1]) last[1] = Math.max(last[1], end);
    else merged.push([start, end]);
  }
  return merged;
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
