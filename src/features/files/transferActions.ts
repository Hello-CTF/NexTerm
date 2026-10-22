import { askChoice } from "../../ui/dialogs";
import { fsApi } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { dropStaged, stageFile } from "../../ipc/webFiles";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import type { FileEntryDto } from "../../ipc/types";
import { formatSize } from "./fileTypes";
import { baseName, joinPath, parentOf } from "./pathUtils";
import {
  conflictCaseInsensitive,
  findConflict,
  takenNames,
  uniqueRemoteName,
  type ConflictPolicy,
  type TakenNames,
} from "./transferQueue";
import { enqueueUploadBatch, type UploadItem } from "./transferStore";
import type { FolderFileEntry } from "./folderUpload";

interface PolicyAnswer {
  kind: ConflictPolicy;
  applyAll: boolean;
}

function sessionKindOf(sessionId: string): string {
  return useUi.getState().sessions.find((s) => s.id === sessionId)?.kind ?? "";
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

export interface UploadSource {
  name: string;
  localPath: string;
  // file uploads content instead of a local path (desktop drops expose no OS path); see UploadItem.
  file?: File;
}

export async function enqueueUploadWithConflicts(args: {
  sessionId: string;
  dir: string;
  items: UploadSource[];
  knownSiblings?: FileEntryDto[];
  refreshDir?: string;
}): Promise<boolean> {
  const siblings = args.knownSiblings ?? (await fsApi.list(args.sessionId, args.dir));
  const ci = conflictCaseInsensitive(sessionKindOf(args.sessionId));
  const taken = takenNames(
    siblings.map((e) => e.name),
    ci,
  );
  const pushToast = useUi.getState().pushToast;
  let batchPolicy: ConflictPolicy | null = null;
  const uploads: UploadItem[] = [];
  const skipped: UploadItem[] = [];
  for (const [i, item] of args.items.entries()) {
    const name = item.name;
    if (!taken.has(name)) {
      taken.add(name);
      uploads.push({ localPath: item.localPath, remotePath: joinPath(args.dir, name), file: item.file });
      continue;
    }
    let answer: PolicyAnswer | null;
    if (batchPolicy) {
      answer = { kind: batchPolicy, applyAll: true };
    } else {
      const conflict = findConflict(name, siblings, ci);
      if (!conflict) {
        // Taken only by an earlier item in this batch: keep both without prompting.
        const finalName = uniqueRemoteName(name, taken);
        taken.add(finalName);
        uploads.push({
          localPath: item.localPath,
          remotePath: joinPath(args.dir, finalName),
          file: item.file,
        });
        continue;
      }
      const moreComing = args.items
        .slice(i + 1)
        .map((x) => x.name)
        .some((n) => taken.has(n));
      answer = await askConflictPolicy(name, conflict, moreComing);
    }
    if (!answer) {
      pushToast("info", "已取消上传，所有文件保持原样");
      return false;
    }
    if (answer.applyAll) batchPolicy = answer.kind;
    if (answer.kind === "skip") {
      skipped.push({ localPath: item.localPath, remotePath: joinPath(args.dir, name) });
      continue;
    }
    const finalName = answer.kind === "keepBoth" ? uniqueRemoteName(name, taken) : name;
    taken.add(finalName);
    uploads.push({ localPath: item.localPath, remotePath: joinPath(args.dir, finalName), file: item.file });
  }
  for (const s of skipped) await dropStaged(s.localPath).catch(() => undefined);
  enqueueUploadBatch(args.sessionId, uploads, skipped, args.refreshDir ?? args.dir);
  return true;
}

export async function localPathsFromFiles(files: File[]): Promise<string[] | null> {
  if (files.length === 0) return null;
  const pushToast = useUi.getState().pushToast;
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
  // Desktop webviews expose no OS path for dropped files; callers fall back to content upload.
  return null;
}

export async function localPathsFromDataTransfer(dt: DataTransfer): Promise<string[] | null> {
  return localPathsFromFiles(Array.from(dt.files ?? []));
}

export async function dropLocalFiles(args: {
  sessionId: string;
  dir: string;
  files: File[];
  knownSiblings?: FileEntryDto[];
}): Promise<void> {
  const localPaths = await localPathsFromFiles(args.files);
  if (!localPaths) {
    // Desktop drops carry no OS paths, so the upload goes by file content through the same
    // conflict-policy flow instead of the path-based fs_upload pipeline.
    if (!WEB && args.files.length > 0) {
      const pushToast = useUi.getState().pushToast;
      try {
        await enqueueUploadWithConflicts({
          sessionId: args.sessionId,
          dir: args.dir,
          items: args.files.map((file) => ({ name: file.name, localPath: "", file })),
          knownSiblings: args.knownSiblings,
          refreshDir: args.dir,
        });
      } catch (e) {
        pushToast("error", `上传失败：${describeError(e)}`);
      }
    }
    return;
  }
  const pushToast = useUi.getState().pushToast;
  let enqueued = false;
  try {
    enqueued = await enqueueUploadWithConflicts({
      sessionId: args.sessionId,
      dir: args.dir,
      items: localPaths.map((localPath) => ({ name: baseName(localPath), localPath })),
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

export async function dropUploadFiles(args: {
  sessionId: string;
  dir: string;
  dataTransfer: DataTransfer;
  knownSiblings?: FileEntryDto[];
}): Promise<void> {
  await dropLocalFiles({
    sessionId: args.sessionId,
    dir: args.dir,
    files: Array.from(args.dataTransfer.files ?? []),
    knownSiblings: args.knownSiblings,
  });
}

async function askFolderMergePolicy(folderName: string): Promise<ConflictPolicy | null> {
  const key = await askChoice(
    `目标目录已存在文件夹「${folderName}」，上传会合并进去。其中的同名文件如何处理？`,
    {
      title: "上传文件夹",
      level: "warning",
      choices: [
        { key: "skip", label: "跳过同名文件", hint: "远端同名文件保持原样", primary: true },
        { key: "overwrite", label: "覆盖同名文件", hint: "远端同名文件将被替换", danger: true },
        { key: "keepBoth", label: "同名文件自动重命名", hint: "远端自动重命名（如 a (1).txt）后上传" },
      ],
    },
  );
  return key === "skip" || key === "overwrite" || key === "keepBoth" ? key : null;
}

async function listRemoteSubdirs(
  sessionId: string,
  uploads: UploadItem[],
  caseInsensitive: boolean,
): Promise<Map<string, TakenNames>> {
  const dirs = [
    ...new Set(
      uploads.map((u) => parentOf(u.remotePath)).filter((d): d is string => d !== null),
    ),
  ];
  const taken = new Map<string, TakenNames>();
  for (const dir of dirs) {
    try {
      const list = await fsApi.list(sessionId, dir);
      taken.set(
        dir,
        takenNames(
          list.map((e) => e.name),
          caseInsensitive,
        ),
      );
    } catch (e) {
      if ((e as { code?: string } | null)?.code === "not_found") continue;
      throw e;
    }
  }
  return taken;
}

export async function enqueueFolderUpload(args: {
  sessionId: string;
  dir: string;
  folderName: string;
  files: FolderFileEntry[];
  emptyDirs: string[];
  knownSiblings?: FileEntryDto[];
}): Promise<boolean> {
  const siblings = args.knownSiblings ?? (await fsApi.list(args.sessionId, args.dir));
  const ci = conflictCaseInsensitive(sessionKindOf(args.sessionId));
  const pushToast = useUi.getState().pushToast;
  let folderName = args.folderName;
  const top = findConflict(folderName, siblings, ci);
  if (top && top.kind !== "dir") {
    const key = await askChoice(
      `目标目录已存在同名文件「${folderName}」，无法在其位置创建文件夹。`,
      {
        title: "上传文件夹",
        level: "warning",
        choices: [
          {
            key: "keepBoth",
            label: "保留两者",
            hint: "文件夹自动重命名（如 pics (1)）后上传",
            primary: true,
          },
          { key: "skip", label: "不上传", hint: "远端保持原样" },
        ],
      },
    );
    if (key !== "keepBoth") {
      pushToast(
        "info",
        key === "skip" ? "已跳过文件夹上传，远端保持原样" : "已取消上传，所有文件保持原样",
      );
      return false;
    }
    folderName = uniqueRemoteName(
      folderName,
      takenNames(
        siblings.map((e) => e.name),
        ci,
      ),
    );
  }
  const prefix = joinPath(args.dir, folderName);
  let uploads: UploadItem[] = args.files.map((f) => ({
    localPath: f.localPath,
    remotePath: joinPath(prefix, f.relativePath),
    // Entries without a local path (desktop drops) upload their File content instead.
    ...(f.localPath === "" && f.file ? { file: f.file } : {}),
  }));
  const skipped: UploadItem[] = [];
  if (top && top.kind === "dir" && uploads.length > 0) {
    const policy = await askFolderMergePolicy(folderName);
    if (!policy) {
      pushToast("info", "已取消上传，所有文件保持原样");
      return false;
    }
    if (policy !== "overwrite") {
      const taken = await listRemoteSubdirs(args.sessionId, uploads, ci);
      uploads = uploads.filter((u) => {
        const dir = parentOf(u.remotePath) ?? args.dir;
        const names = taken.get(dir);
        const name = baseName(u.remotePath);
        if (!names || !names.has(name)) return true;
        if (policy === "skip") {
          skipped.push(u);
          return false;
        }
        const finalName = uniqueRemoteName(name, names);
        names.add(finalName);
        u.remotePath = joinPath(dir, finalName);
        return true;
      });
    }
  }
  for (const s of skipped) await dropStaged(s.localPath).catch(() => undefined);
  if (uploads.length > 0 || skipped.length > 0) {
    enqueueUploadBatch(args.sessionId, uploads, skipped, args.dir);
  }
  const emptyTargets = args.emptyDirs.map((rel) => joinPath(prefix, rel));
  if (args.files.length === 0 && !emptyTargets.includes(prefix)) emptyTargets.push(prefix);
  const mkdirFailed: string[] = [];
  for (const target of emptyTargets) {
    try {
      await fsApi.mkdir(args.sessionId, target);
    } catch {
      mkdirFailed.push(target);
    }
  }
  if (args.files.length === 0) {
    if (mkdirFailed.length > 0) {
      pushToast("error", `创建空文件夹失败：${mkdirFailed[0]}`);
      return false;
    }
    pushToast("success", `已创建空文件夹「${folderName}」`);
  } else if (mkdirFailed.length > 0) {
    pushToast(
      "error",
      `部分空目录创建失败：${mkdirFailed[0]}${mkdirFailed.length > 1 ? ` 等 ${mkdirFailed.length} 个` : ""}`,
    );
  }
  return true;
}
