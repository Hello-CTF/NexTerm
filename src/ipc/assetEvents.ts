const ASSET_DATA_CHANGED_EVENT = "nexterm:asset-data-changed";

export function notifyAssetDataChanged(): void {
  if (typeof window !== "undefined") window.dispatchEvent(new Event(ASSET_DATA_CHANGED_EVENT));
}

export function onAssetDataChanged(listener: () => void): () => void {
  if (typeof window === "undefined") return () => {};
  window.addEventListener(ASSET_DATA_CHANGED_EVENT, listener);
  return () => window.removeEventListener(ASSET_DATA_CHANGED_EVENT, listener);
}
