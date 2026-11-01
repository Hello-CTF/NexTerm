import type { Asset } from "../../ipc/commands";
import type { ConnectHistoryEntries } from "../../features/explorer/connectHistory";
import { isInventoryOnlyKind } from "../../features/explorer/inventory";

export const RECENT_LIMIT = 5;

// recentConnections joins the connect history the app already keeps with the stored assets: newest connection first,
// skipping assets that no longer exist, are hidden, cannot be connected, or (on mobile) are the local terminal.
export function recentConnections(
  assets: readonly Asset[],
  history: ConnectHistoryEntries,
  options: { hiddenIds: readonly string[]; mobile: boolean; limit?: number },
): Array<{ asset: Asset; at: number }> {
  const hidden = new Set(options.hiddenIds);
  const rows: Array<{ asset: Asset; at: number }> = [];
  for (const asset of assets) {
    const entry = history[asset.id];
    if (!entry || hidden.has(asset.id)) continue;
    if (isInventoryOnlyKind(asset.kind) || asset.kind === "device") continue;
    if (options.mobile && asset.kind === "local") continue;
    rows.push({ asset, at: entry.at });
  }
  rows.sort((a, b) => b.at - a.at);
  return rows.slice(0, options.limit ?? RECENT_LIMIT);
}

export function assetKindName(kind: string): string {
  switch (kind) {
    case "ssh":
      return "SSH";
    case "winrm":
      return "WinRM";
    case "local":
      return "本机";
    case "docker":
    case "docker-context":
      return "Docker";
    case "kubernetes":
      return "Kubernetes";
    case "mysql":
      return "MySQL";
    case "postgres":
      return "PostgreSQL";
    case "redis":
      return "Redis";
    case "rdp":
      return "RDP";
    case "vnc":
      return "VNC";
    default:
      return kind;
  }
}
