import { assetApi, type Asset } from "../ipc/commands";
import { copyText } from "../ui/clipboard";
import { openHostDetail } from "../features/hosts/hostDetail";
import { connectAsset, useUi } from "./store";

const DEEP_LINK_PATTERN = /^nexterm:\/\/connect\/([^/?#]+)\/?$/;
const HOST_LINK_PREFIX = "nexterm://host/";
const HOST_LINK_PATTERN = /^nexterm:\/\/host\/([^/?#]+)\/?$/;

export function assetDeepLink(assetId: string): string {
  return `nexterm://connect/${assetId}`;
}

// hostDeepLink opens the host detail page; the id may be a device pseudo asset id (device/<id>), so it is encoded.
export function hostDeepLink(assetId: string): string {
  return `nexterm://host/${encodeURIComponent(assetId)}`;
}

// MAX_HOST_LINK_ID_LENGTH bounds the decoded id of a host link; real asset and device ids are far shorter.
const MAX_HOST_LINK_ID_LENGTH = 256;

function matchId(pattern: RegExp, url: string, maxLength = Infinity): string | null {
  const match = pattern.exec(url.trim());
  if (!match) return null;
  try {
    const id = decodeURIComponent(match[1]);
    return id && id.length <= maxLength ? id : null;
  } catch {
    return null;
  }
}

export function parseDeepLinkAssetId(url: string): string | null {
  return matchId(DEEP_LINK_PATTERN, url);
}

export function parseHostDeepLinkId(url: string): string | null {
  return matchId(HOST_LINK_PATTERN, url, MAX_HOST_LINK_ID_LENGTH);
}

export async function openDeepLink(url: string): Promise<void> {
  const { pushToast } = useUi.getState();
  // A host link only opens the detail page, which shows a missing or inaccessible host itself; it never connects.
  const hostId = parseHostDeepLinkId(url);
  if (hostId) {
    openHostDetail(hostId);
    return;
  }
  if (url.trim().startsWith(HOST_LINK_PREFIX)) {
    pushToast("error", "无效的主机链接");
    return;
  }
  const assetId = parseDeepLinkAssetId(url);
  if (!assetId) {
    pushToast("error", "无法识别的 NexTerm 链接");
    return;
  }
  let asset: Asset;
  try {
    asset = await assetApi.get(assetId);
  } catch {
    pushToast("error", "打开链接对应的资产失败，请确认资产仍存在后重试");
    return;
  }
  if (!asset || asset.deletedAt) {
    pushToast("error", "打开链接对应的资产失败，请确认资产仍存在后重试");
    return;
  }
  await connectAsset({ id: asset.id, name: asset.name, kind: asset.kind, credId: asset.credId });
}

export function copyAssetDeepLink(assetId: string): void {
  copyLink(assetDeepLink(assetId));
}

export function copyHostDeepLink(assetId: string): void {
  copyLink(hostDeepLink(assetId));
}

function copyLink(link: string): void {
  const { pushToast } = useUi.getState();
  void copyText(link)
    .then(() => pushToast("success", "链接已复制"))
    .catch(() => pushToast("error", "复制失败，请重试"));
}
