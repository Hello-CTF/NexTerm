import { assetApi, type Asset } from "../ipc/commands";
import { copyText } from "../ui/clipboard";
import { connectAsset, useUi } from "./store";

const DEEP_LINK_PATTERN = /^nexterm:\/\/connect\/([^/?#]+)\/?$/;

export function assetDeepLink(assetId: string): string {
  return `nexterm://connect/${assetId}`;
}

export function parseDeepLinkAssetId(url: string): string | null {
  const match = DEEP_LINK_PATTERN.exec(url.trim());
  if (!match) return null;
  try {
    const id = decodeURIComponent(match[1]);
    return id ? id : null;
  } catch {
    return null;
  }
}

export async function openDeepLink(url: string): Promise<void> {
  const { pushToast } = useUi.getState();
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
  const { pushToast } = useUi.getState();
  void copyText(assetDeepLink(assetId))
    .then(() => pushToast("success", "链接已复制"))
    .catch(() => pushToast("error", "复制失败，请重试"));
}
