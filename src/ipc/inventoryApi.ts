import { call } from "./commands";
import { notifyAssetDataChanged } from "./assetEvents";
import type { InventorySSHWatchRequest } from "./generated/types";

export type InventorySource = "ssh-config" | "docker-context" | "kubernetes";
export type {
  InventoryCandidateDTO,
  InventoryDiscoveryDTO,
  InventorySSHWatchDTO,
  InventorySyncDTO,
} from "./generated/types";

export const inventoryApi = {
  discover: (source: InventorySource, path?: string) =>
    call("inventory_discover", {
      args: { source, ...(path?.trim() ? { path: path.trim() } : {}) },
    }),
  sync: async (args: { source: InventorySource; path?: string; groupId?: string; keys?: string[] }) => {
    const result = await call("inventory_sync", {
      args: {
        source: args.source,
        ...(args.path?.trim() ? { path: args.path.trim() } : {}),
        ...(args.groupId ? { groupId: args.groupId } : {}),
        ...(args.keys ? { keys: args.keys } : {}),
      },
    });
    notifyAssetDataChanged();
    return result;
  },
  sshWatchStatus: () => call("inventory_ssh_watch", { args: {} }),
  setSshWatch: async (request: InventorySSHWatchRequest) => {
    const result = await call("inventory_ssh_watch", { args: request });
    notifyAssetDataChanged();
    return result;
  },
};
