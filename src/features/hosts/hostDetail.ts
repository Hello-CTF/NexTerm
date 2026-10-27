import { dbKindOf, deviceAssetId, useUi, type Workspace } from "../../app/store";
import type { Asset } from "../../ipc/commands";

// One host detail tab exists at a time; opening another host retargets it.
export const HOST_DETAIL_TAB_ID = "host-detail";
export const HOST_DETAIL_TITLE = "主机详情";
export const DATABASE_DETAIL_TITLE = "数据库详情";

// hostDetailTitle is the tab title once the page knows what it shows.
export function hostDetailTitle(name: string, database: boolean): string {
  return `${database ? DATABASE_DETAIL_TITLE : HOST_DETAIL_TITLE} · ${name}`;
}

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
  // Reopening the page that is already showing this host keeps its resolved title.
  const current = findHostDetailTab();
  useUi.getState().addTab({
    id: HOST_DETAIL_TAB_ID,
    kind: "host",
    title: current?.assetId === assetId ? current.title : HOST_DETAIL_TITLE,
    assetId,
    closable: true,
  });
}

function findHostDetailTab(): { assetId?: string; title: string } | undefined {
  for (const workspace of useUi.getState().workspaces) {
    for (const pane of workspace.panes) {
      const tab = pane.tabs.find((item) => item.id === HOST_DETAIL_TAB_ID);
      if (tab) return tab;
    }
  }
  return undefined;
}

// setHostDetailTitle names the open detail tab after the host it shows, once the asset or device has resolved.
export function setHostDetailTitle(assetId: string, title: string): void {
  const tab = findHostDetailTab();
  if (!tab || tab.assetId !== assetId || tab.title === title) return;
  useUi.getState().updateTab(HOST_DETAIL_TAB_ID, { title });
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

function optionText(asset: Pick<Asset, "options">, key: string): string {
  const value = asset.options?.[key];
  return typeof value === "string" ? value.trim() : "";
}

// hasJump is true when connecting to the asset goes through a jump host.
export function hasJump(asset: Pick<Asset, "options">): boolean {
  return optionText(asset, "jumpAssetId") !== "";
}

// hasIndirectRoute is true when the asset's address is not the machine the connection ends on: a jump host, a proxy
// or a ProxyCommand sits in between, so databases at the same address cannot be assumed to live on this host.
function hasIndirectRoute(asset: Pick<Asset, "options">): boolean {
  return ["jumpAssetId", "proxyCommand", "proxy", "proxyJump"].some((key) => optionText(asset, key) !== "");
}

export type DatabaseRelation = "via" | "address";

export const DATABASE_RELATION_LABEL: Record<DatabaseRelation, string> = {
  via: "经由此主机",
  address: "同地址",
};

// databaseRelation says why a database belongs to a host, or null when it does not. Tunnelled databases belong to
// their via host only. Address matches need a directly reached host: loopback addresses only match the local host,
// and a host behind a jump host or proxy never matches by address.
export function databaseRelation(host: Pick<Asset, "id" | "kind" | "host" | "options">, database: Asset): DatabaseRelation | null {
  if (database.deletedAt || database.id === host.id || !dbKindOf(database.kind)) return null;
  const via = viaAssetIdOf(database);
  if (via) return via === host.id ? "via" : null;
  if (hasIndirectRoute(host)) return null;
  const address = normalizedHost(database.host);
  if (!address) return null;
  if (host.kind === "local") return LOOPBACK_HOSTS.has(address) ? "address" : null;
  if (LOOPBACK_HOSTS.has(address)) return null;
  const hostAddr = normalizedHost(host.host);
  return hostAddr !== "" && address === hostAddr ? "address" : null;
}

// relatedDatabases lists the database assets served by a host; see databaseRelation for the matching rules.
export function relatedDatabases(host: Pick<Asset, "id" | "kind" | "host" | "options">, assets: readonly Asset[]): Asset[] {
  return assets.filter((asset) => databaseRelation(host, asset) !== null);
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
