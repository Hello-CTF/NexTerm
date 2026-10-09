import { askChoice } from "../../ui/dialogs";
import { fsApi } from "../../ipc/commands";
import { DEMO, WEB } from "../../ipc/env";
import { dropStaged, stageFile } from "../../ipc/webFiles";
import { isMac } from "../../app/platform";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import type { FileEntryDto } from "../../ipc/types";
import { formatSize } from "./fileTypes";
import { baseName, joinPath } from "./pathUtils";
import { findConflict, uniqueRemoteName, type ConflictPolicy } from "./transferQueue";
import { enqueueUploadBatch, type UploadItem } from "./transferStore";

interface PolicyAnswer {
  kind: ConflictPolicy;
  applyAll: boolean;
}

async function askConflictPolicy(
  name: string,
  conflict: FileEntryDto,
  moreComing: boolean,
): Promise<PolicyAnswer | null> {
  const detail = conflict.kind === "dir" ? "目录" : `文件 · ${formatSize(conflict.size)}`;
  const key = await askChoice(`「${name}」在目标目录已存在（${detail}）。如何处理这个冲突？`, {
    title: "上传冲突",
    level: "warning",
    choices: [
      { key: "skip", label: "跳过", hint: "保留远端现有内容，不上传这个文件", primary: true },
      { key: "overwrite", label: "覆盖", hint: "用本地文件替换远端现有内容", danger: true },
      { key: "keepBoth", label: "保留两者", hint: "远端自动重命名（如 a (1).txt）后上传" },
      ...(moreComing
        ? [
            { key: "skip-all", label: "跳过，并应用到本次队列", hint: "本次队列中其余冲突也全部跳过" },
            {
              key: "overwrite-all",
              label: "覆盖，并应用到本次队列",
              hint: "本次队列中其余冲突也全部覆盖",
              danger: true,
            },
            {
              key: "keepBoth-all",
              label: "保留两者，并应用到本次队列",
              hint: "本次队列中其余冲突也全部自动重命名",
            },
          ]
        : []),
    ],
  });
  if (!key) return null;
  const applyAll = key.endsWith("-all");
  const kind = (applyAll ? key.slice(0, -"-all".length) : key) as ConflictPolicy;
  return { kind, applyAll };
}

export async function enqueueUploadWithConflicts(args: {
  sessionId: string;
  dir: string;
  localPaths: string[];
  knownSiblings?: FileEntryDto[];
  refreshDir?: string;
}): Promise<boolean> {
  const siblings = args.knownSiblings ?? (await fsApi.list(args.sessionId, args.dir));
  const taken = new Set(siblings.map((e) => e.name));
  const pushToast = useUi.getState().pushToast;
  let batchPolicy: ConflictPolicy | null = null;
  const uploads: UploadItem[] = [];
  const skipped: UploadItem[] = [];
  for (const [i, localPath] of args.localPaths.entries()) {
    const name = baseName(localPath);
    if (!taken.has(name)) {
      taken.add(name);
      uploads.push({ localPath, remotePath: joinPath(args.dir, name) });
      continue;
    }
    let answer: PolicyAnswer | null;
    if (batchPolicy) {
      answer = { kind: batchPolicy, applyAll: true };
    } else {
      const moreComing = args.localPaths
        .slice(i + 1)
        .map(baseName)
        .some((n) => taken.has(n));
      answer = await askConflictPolicy(name, findConflict(name, siblings)!, moreComing);
    }
    if (!answer) {
      pushToast("info", "已取消上传，所有文件保持原样");
      return false;
    }
    if (answer.applyAll) batchPolicy = answer.kind;
    if (answer.kind === "skip") {
      skipped.push({ localPath, remotePath: joinPath(args.dir, name) });
      continue;
    }
    const finalName = answer.kind === "keepBoth" ? uniqueRemoteName(name, taken) : name;
    taken.add(finalName);
    uploads.push({ localPath, remotePath: joinPath(args.dir, finalName) });
  }
  for (const s of skipped) await dropStaged(s.localPath).catch(() => undefined);
  enqueueUploadBatch(args.sessionId, uploads, skipped, args.refreshDir ?? args.dir);
  return true;
}

export async function localPathsFromDataTransfer(dt: DataTransfer): Promise<string[] | null> {
  const files = Array.from(dt.files ?? []);
  if (files.length === 0) return null;
  const pushToast = useUi.getState().pushToast;
  if (DEMO) {
    const base = isMac() ? "~/Downloads" : "C:\\Users\\you\\Downloads";
    return files.map((f) => `${base}/${f.name}`);
  }
  if (WEB) {
    const staged: string[] = [];
    try {
      for (const f of files) staged.push((await stageFile(f)).path);
      return staged;
    } catch (e) {
      for (const p of staged) await dropStaged(p).catch(() => undefined);
      pushToast("error", `读取拖入文件失败：${describeError(e)}`);
      return null;
    }
  }
  const paths = files
    .map((f) => (f as File & { path?: string }).path)
    .filter((p): p is string => typeof p === "string" && p.length > 0);
  if (paths.length === 0) {
    pushToast("info", "当前平台无法从拖拽读取本地文件路径，请改用上传按钮选择文件");
    return null;
  }
  return paths;
}

export async function dropUploadFiles(args: {
  sessionId: string;
  dir: string;
  dataTransfer: DataTransfer;
  knownSiblings?: FileEntryDto[];
}): Promise<void> {
  const localPaths = await localPathsFromDataTransfer(args.dataTransfer);
  if (!localPaths) return;
  const pushToast = useUi.getState().pushToast;
  let enqueued = false;
  try {
    enqueued = await enqueueUploadWithConflicts({
      sessionId: args.sessionId,
      dir: args.dir,
      localPaths,
      knownSiblings: args.knownSiblings,
      refreshDir: args.dir,
    });
  } catch (e) {
    pushToast("error", `上传失败：${describeError(e)}`);
  } finally {
    if (!enqueued) {
      for (const p of localPaths) await dropStaged(p).catch(() => undefined);
    }
  }
}
