import { pickKeyFile } from "../../ui/dialogs";
import { browserFilesAvailable, pickBrowserFile } from "../../ipc/webFiles";

export interface InlineKeySelection {
  path: string;
  content: string | null;
}

export async function pickInlineKeyFile(): Promise<InlineKeySelection | null> {
  if (browserFilesAvailable()) {
    const picked = await pickBrowserFile();
    if (!picked) return null;
    if (picked.size > 64 * 1024) throw new Error("文件超过 64KB，不是私钥");
    return { path: picked.name, content: await picked.text() };
  }
  const path = await pickKeyFile();
  return path ? { path, content: null } : null;
}

export async function resolveInlineKeyContent(
  selection: InlineKeySelection,
  readKeyFile: (path: string) => Promise<string>,
): Promise<string> {
  return selection.content ?? readKeyFile(selection.path);
}
