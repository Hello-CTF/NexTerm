import { WEB } from "./env";
import { pickBrowserFile, saveBrowserBlob } from "./webFiles";

export interface PickedBundleBuffer {
  name: string;
  buffer: ArrayBuffer;
}

export async function pickBundleBuffer(): Promise<PickedBundleBuffer | null> {
  if (!WEB) return null;
  const file = await pickBrowserFile(".nxbm");
  if (!file) return null;
  return { name: file.name, buffer: await file.arrayBuffer() };
}

export async function saveBundleBytes(name: string, data: Uint8Array<ArrayBuffer>): Promise<boolean> {
  if (!WEB) return false;
  return saveBrowserBlob(name, new Blob([data], { type: "application/octet-stream" }));
}
