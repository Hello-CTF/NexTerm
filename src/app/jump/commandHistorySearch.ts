import type { Asset } from "../../ipc/commands";
import type { Workspace } from "../store";
import { t } from "../../i18n";

export const COMMAND_SEARCH_PAGE = 100;
export const COMMAND_SEARCH_DEBOUNCE_MS = 200;

export type CommandRange = "all" | "24h" | "7d" | "30d";

export const COMMAND_RANGE_OPTIONS: { value: CommandRange; label: string }[] = [
  { value: "all", label: t("全部时间") },
  { value: "24h", label: t("最近 24 小时") },
  { value: "7d", label: t("最近 7 天") },
  { value: "30d", label: t("最近 30 天") },
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
  if (assetId.startsWith("device/")) return t("设备");
  return t("已删除的主机");
}

// How many newer entries a command from the active terminal's directory may move ahead of.
export const CWD_RANK_BOOST = 3;

export interface ActiveTerminal {
  // Backend terminal id, the key of its OSC 7 directory reports.
  tabId: string;
  assetId: string;
}

// activeTerminal resolves the terminal the user is looking at: the active tab of the active pane in the active workspace.
export function activeTerminal(state: {
  workspaces: Pick<Workspace, "id" | "assetId" | "activePaneId" | "panes">[];
  activeWorkspaceId: string | null;
}): ActiveTerminal | null {
  const ws = state.workspaces.find((w) => w.id === state.activeWorkspaceId);
  if (!ws?.assetId) return null;
  const pane = ws.panes.find((p) => p.id === ws.activePaneId) ?? ws.panes[0];
  const tab = pane?.tabs.find((candidate) => candidate.id === pane.activeTabId);
  if (!tab || tab.kind !== "terminal" || !tab.tabId) return null;
  return { tabId: tab.tabId, assetId: ws.assetId };
}

// rankByCwd gives commands that ran in the given directory of the given host a small lift over newer ones,
// keeping every other entry in its recency order. It ranks one page at a time so loading more never reshuffles
// the rows already shown.
export function rankByCwd<T extends { assetId: string; cwd: string }>(
  page: T[],
  here: { assetId: string; cwd: string } | null,
  boost = CWD_RANK_BOOST,
): T[] {
  if (!here || here.cwd === "") return page;
  return page
    .map((entry, index) => ({ entry, index, score: index - (entry.assetId === here.assetId && entry.cwd === here.cwd ? boost + 0.5 : 0) }))
    .sort((a, b) => a.score - b.score || a.index - b.index)
    .map(({ entry }) => entry);
}
