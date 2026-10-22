export function assetTagValues(raw: string): string[] {
  return raw
    .replaceAll("，", ",")
    .split(",")
    .map((tag) => tag.trim())
    .filter(Boolean);
}

export function normalizeAssetTags(raw: string): string {
  const seen = new Set<string>();
  const tags: string[] = [];
  for (const tag of assetTagValues(raw)) {
    const canonical = tag.toLowerCase();
    if (seen.has(canonical)) continue;
    seen.add(canonical);
    tags.push(canonical);
  }
  return tags.join(", ");
}
