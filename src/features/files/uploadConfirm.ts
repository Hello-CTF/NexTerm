import { ask } from "../../ui/dialogs";
import { fsApi } from "../../ipc/commands";
import { cachedFsRoots } from "../../ipc/fsRootsCache";
import { useUi } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";
import { baseName } from "./pathUtils";
import { conflictCaseInsensitive, findConflict } from "./transferQueue";
import { formatSize } from "./fileTypes";
import { t } from "../../i18n";

export type UploadOverwriteCheck = "proceed" | "cancelled";

export async function checkUploadOverwrite(
  sessionId: string,
  dir: string,
  remote: string,
  knownSiblings?: FileEntryDto[],
): Promise<UploadOverwriteCheck> {
  const siblings = knownSiblings ?? (await fsApi.list(sessionId, dir));
  const kind = useUi.getState().sessions.find((s) => s.id === sessionId)?.kind ?? "";
  const conflict = findConflict(baseName(remote), siblings, conflictCaseInsensitive(kind, cachedFsRoots(sessionId)));
  if (!conflict) return "proceed";
  const detail = conflict.kind === "dir" ? t("目录") : t("文件 · {value}", { value: formatSize(conflict.size) });
  const ok = await ask(
    t("「{remote}」已存在（{detail}）。继续上传会用本地文件覆盖它，远端现有内容将丢失；取消则保持原样。\n\n覆盖「{remote2}」？", { remote, detail, remote2: remote }),
    { title: t("上传覆盖确认"), kind: "warning" },
  );
  return ok ? "proceed" : "cancelled";
}
