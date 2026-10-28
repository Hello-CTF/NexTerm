import { useQuery } from "@tanstack/react-query";
import { useCallback } from "react";
import { fleetClient } from "../fleet/clients";
import { useFleetAccount } from "../fleet/useFleetAccount";
import { assetApi, type Asset } from "../../ipc/commands";
import { deviceIdOfHost } from "./hostDetail";

export const BUILTIN_LOCAL_HOST_ID = "01J0NEXTERMLOCALDEVICE0001";

export interface HostLabelSources {
  assets: readonly Pick<Asset, "id" | "name" | "deletedAt">[] | undefined;
  devices: readonly { id: string; name: string }[] | undefined;
  // True while the asset list or the signed-in account's device list is still loading.
  loading: boolean;
}

// resolveHostLabel names a host id found in a rule or a status row. It returns "" while the answer is not known yet,
// so callers show nothing instead of a wrong "unknown" label. Unresolvable ids keep a short tail to stay distinguishable.
export function resolveHostLabel(id: string | undefined, src: HostLabelSources): string {
  if (!id) return "";
  const asset = src.assets?.find((item) => item.id === id);
  if (asset) {
    const name = asset.name.trim();
    if (asset.deletedAt) return name ? `${name}（已删除）` : "已删除的主机";
    if (name) return name;
  }
  if (id === BUILTIN_LOCAL_HOST_ID) return "本机";
  const deviceId = deviceIdOfHost(id);
  if (deviceId) {
    const name = src.devices?.find((item) => item.id === deviceId)?.name.trim();
    if (name) return name;
  }
  if (src.loading) return "";
  return `未命名主机 …${id.slice(-4)}`;
}

// useHostLabeler resolves host ids against assets and, when signed in, managed devices (shared fleet-devices cache).
export function useHostLabeler(): (id: string | undefined) => string {
  const account = useFleetAccount();
  const userId = account.userId;
  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), retry: false });
  const devicesQuery = useQuery({
    queryKey: ["fleet-devices", userId],
    queryFn: () => fleetClient().devices(),
    enabled: Boolean(userId),
    refetchOnWindowFocus: false,
  });
  const loading = assetsQuery.isLoading || account.loading || (Boolean(userId) && devicesQuery.isLoading);
  const assets = assetsQuery.data;
  const devices = devicesQuery.data?.devices;
  return useCallback(
    (id: string | undefined) => resolveHostLabel(id, { assets, devices, loading }),
    [assets, devices, loading],
  );
}
