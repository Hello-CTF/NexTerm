import { ask } from "../../ui/dialogs";
import { fsApi } from "../../ipc/commands";
import type { FileEntryDto } from "../../ipc/types";
import { findSibling } from "./fileOps";
import { baseName } from "./pathUtils";
import { formatSize } from "./fileTypes";

export type UploadOverwriteCheck = "proceed" | "cancelled";

export async function checkUploadOverwrite(
  sessionId: string,
  dir: string,
  remote: string,
  knownSiblings?: FileEntryDto[],
): Promise<UploadOverwriteCheck> {
  const siblings = knownSiblings ?? (await fsApi.list(sessionId, dir));
  const conflict = findSibling(siblings, baseName(remote));
  if (!conflict) return "proceed";
  const detail = conflict.kind === "dir" ? "目录" : `文件 · ${formatSize(conflict.size)}`;
  const ok = await ask(
    `「${remote}」已存在（${detail}）。继续上传会用本地文件覆盖它，远端现有内容将丢失；取消则保持原样。\n\n覆盖「${remote}」？`,
    { title: "上传覆盖确认", kind: "warning" },
  );
  return ok ? "proceed" : "cancelled";
}
