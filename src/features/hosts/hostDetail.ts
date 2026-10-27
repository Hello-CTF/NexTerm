import { dbKindOf, deviceAssetId, useUi, type Workspace } from "../../app/store";
import type { Asset } from "../../ipc/commands";

// One host detail tab exists at a time; opening another host retargets it.
export const HOST_DETAIL_TAB_ID = "host-detail";
export const HOST_DETAIL_TITLE = "主机详情";

const DEVICE_PREFIX = deviceAssetId("");

// Asset kinds that are hosts: the page lists their sessions, containers and databases.
const HOST_ASSET_KINDS = new Set(["ssh", "winrm", "local"]);

export function isHostAssetKind(kind: string): boolean {
  return HOST_ASSET_KINDS.has(kind);
}

// hostDetailLabel names the menu entry that opens the page for an asset, or null when the asset has no detail page.
export function hostDetailLabel(kind: string): string | null {
  if (isHostAssetKind(kind)) return "主机详情";
  if (dbKindOf(kind)) return "数据库详情";
  return null;
}

// deviceIdOfHost returns the fleet device id behind a device workspace's pseudo asset id (device/<id>).
export function deviceIdOfHost(assetId: string): string | null {
  if (!assetId.startsWith(DEVICE_PREFIX)) return null;
  const id = assetId.slice(DEVICE_PREFIX.length);
  return id || null;
}

// openHostDetail opens the host detail tab for an asset id or a device pseudo asset id. It never connects or opens a
// terminal: the page offers that as an explicit action.
export function openHostDetail(assetId: string): void {
  if (!assetId) return;
  useUi.getState().addTab({
    id: HOST_DETAIL_TAB_ID,
    kind: "host",
    title: HOST_DETAIL_TITLE,
    assetId,
    closable: true,
  });
}

// hostWorkspaceOf finds the open host workspace of an asset; database workspaces are not host workspaces.
export function hostWorkspaceOf(workspaces: readonly Workspace[], assetId: string): Workspace | undefined {
  return workspaces.find((workspace) => workspace.kind === "session" && workspace.assetId === assetId);
}

// databaseWorkspaceOf finds the open workspace of a database asset.
export function databaseWorkspaceOf(workspaces: readonly Workspace[], assetId: string): Workspace | undefined {
  return workspaces.find((workspace) => workspace.kind === "db" && workspace.assetId === assetId);
}

const LOOPBACK_HOSTS = new Set(["localhost", "127.0.0.1", "::1", "[::1]"]);

function normalizedHost(host: string | null | undefined): string {
  return (host ?? "").trim().toLowerCase();
}

function viaAssetIdOf(asset: Asset): string {
  const via = asset.options?.viaAssetId;
  return typeof via === "string" ? via : "";
}

// relatedDatabases lists the database assets served by a host: those tunnelled through it, and, without a tunnel,
// those whose address is the host's address (a loopback address for the local host).
export function relatedDatabases(host: Pick<Asset, "id" | "kind" | "host">, assets: readonly Asset[]): Asset[] {
  const hostAddress = normalizedHost(host.host);
  return assets.filter((asset) => {
    if (asset.deletedAt || asset.id === host.id || !dbKindOf(asset.kind)) return false;
    const via = viaAssetIdOf(asset);
    if (via) return via === host.id;
    const address = normalizedHost(asset.host);
    if (!address) return false;
    if (host.kind === "local") return LOOPBACK_HOSTS.has(address);
    return hostAddress !== "" && address === hostAddress;
  });
}

// tunnelHostOf returns the host a database asset is tunnelled through, when it is a known asset.
export function tunnelHostOf(database: Asset, assets: readonly Asset[]): Asset | undefined {
  const via = viaAssetIdOf(database);
  return via ? assets.find((asset) => asset.id === via && !asset.deletedAt) : undefined;
}

// hostAddress formats user@host:port for display; empty when the asset has no address.
export function hostAddress(asset: Pick<Asset, "host" | "port" | "username">): string {
  const host = (asset.host ?? "").trim();
  if (!host) return "";
  const user = (asset.username ?? "").trim();
  const port = asset.port ? `:${asset.port}` : "";
  const bracketed = port && host.includes(":") && !host.startsWith("[") ? `[${host}]` : host;
  return `${user ? `${user}@` : ""}${bracketed}${port}`;
}
