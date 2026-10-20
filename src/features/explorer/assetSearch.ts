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
    const key = tag.toLocaleLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    tags.push(tag);
  }
  return tags.join(", ");
}
