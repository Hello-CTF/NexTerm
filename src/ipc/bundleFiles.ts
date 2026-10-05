import { syncApi } from "./commands";
import { DEMO, WEB } from "./env";
import { pickBrowserFile, saveBrowserBlob, baseName } from "./webFiles";
import { openWailsFile, saveWailsFile } from "./wails";

export interface PickedBundle {
  name: string;
  text: string;
}

function browserFileApis(): boolean {
  return WEB || DEMO;
}

export async function pickBundleFile(): Promise<PickedBundle | null> {
  if (browserFileApis()) {
    const file = await pickBrowserFile(".json,application/json");
    if (!file) return null;
    return { name: file.name, text: await file.text() };
  }
  const path = await openWailsFile([
    { name: "NexTerm 资产包", extensions: ["json"] },
    { name: "所有文件", extensions: ["*"] },
  ]);
  if (!path) return null;
  const text = await syncApi.readBundleFile(path);
  return { name: baseName(path), text };
}

export async function saveBundleFile(name: string, text: string): Promise<boolean> {
  if (browserFileApis()) {
    return saveBrowserBlob(name, new Blob([text], { type: "application/json" }));
  }
  const path = await saveWailsFile(name);
  if (!path) return false;
  await syncApi.writeBundleFile(path, text);
  return true;
}
