import type { Asset } from "../../ipc/commands";

export const COMMAND_SEARCH_PAGE = 100;
export const COMMAND_SEARCH_DEBOUNCE_MS = 200;

export type CommandRange = "all" | "24h" | "7d" | "30d";

export const COMMAND_RANGE_OPTIONS: { value: CommandRange; label: string }[] = [
  { value: "all", label: "全部时间" },
  { value: "24h", label: "最近 24 小时" },
  { value: "7d", label: "最近 7 天" },
  { value: "30d", label: "最近 30 天" },
];

const RANGE_MS: Record<Exclude<CommandRange, "all">, number> = {
  "24h": 24 * 3600 * 1000,
  "7d": 7 * 24 * 3600 * 1000,
  "30d": 30 * 24 * 3600 * 1000,
};

// rangeStart is the inclusive lower bound in milliseconds, or null for no bound.
export function rangeStart(range: CommandRange, now = Date.now()): number | null {
  return range === "all" ? null : now - RANGE_MS[range];
}

// historyQueryText returns the search text when the palette input is a command history query, otherwise null.
export function historyQueryText(input: string): string | null {
  const trimmed = input.trimStart();
  return trimmed.startsWith(">") ? trimmed.slice(1).trim() : null;
}

export function hostLabelFor(assetId: string, assets: Pick<Asset, "id" | "name">[]): string {
  const asset = assets.find((candidate) => candidate.id === assetId);
  if (asset) return asset.name;
  if (assetId.startsWith("device/")) return "设备";
  return "已删除的主机";
}
