import { askChoice } from "../../ui/dialogs";
import { WEB } from "../../ipc/env";
import { dropStaged, stageFile } from "../../ipc/webFiles";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import type { FileEntryDto } from "../../ipc/types";
import { formatSize } from "./fileTypes";
import { packExtractSupported, packUploadAndExtract } from "./folderPackUpload";
import { dropLocalFiles, dropUploadFiles, enqueueFolderUpload } from "./transferActions";

export interface FolderFileEntry {
  file: File;
  // relativePath is below the folder root (e.g. "sub/a.txt"); the upload target is
  // joinPath(dir, folderName) + relativePath, and folderPackUpload tar entries use it as-is.
  relativePath: string;
  localPath: string;
  size: number;
}

export interface FolderSelection {
  folderName: string;
  files: FolderFileEntry[];
  // relative to the folder root, same convention as FolderFileEntry.relativePath
  emptyDirs: string[];
}

interface PendingFolderFile {
  file: File;
  relativePath: string;
}

interface PendingFolder {
  folderName: string;
  files: PendingFolderFile[];
  emptyDirs: string[];
}

interface FolderFlowArgs {
  sessionId: string;
  dir: string;
  knownSiblings?: FileEntryDto[];
  onSettled?: () => void;
}

async function readAllEntries(dir: FileSystemDirectoryEntry): Promise<FileSystemEntry[]> {
  const reader = dir.createReader();
  const all: FileSystemEntry[] = [];
  for (;;) {
    const batch = await new Promise<FileSystemEntry[]>((resolve, reject) =>
      reader.readEntries(resolve, reject),
    );
    if (batch.length === 0) return all;
    all.push(...batch);
  }
}

async function walkEntry(
  entry: FileSystemEntry,
  prefix: string,
  files: PendingFolderFile[],
  emptyDirs: string[],
): Promise<void> {
  if (entry.isFile) {
    const file = await new Promise<File>((resolve, reject) =>
      (entry as FileSystemFileEntry).file(resolve, reject),
    );
    files.push({ file, relativePath: prefix + entry.name });
    return;
  }
  if (!entry.isDirectory) return;
  const rel = prefix + entry.name;
  const children = await readAllEntries(entry as FileSystemDirectoryEntry);
  for (const child of children) await walkEntry(child, `${rel}/`, files, emptyDirs);
  if (children.length === 0) emptyDirs.push(rel);
}

async function pendingFoldersFromDrop(
  dt: DataTransfer,
): Promise<{ folders: PendingFolder[]; looseFiles: File[] } | null> {
  const items = Array.from(dt.items ?? []);
  if (items.length === 0) return null;
  // DataTransfer items go stale once the drop handler returns; read them synchronously here.
  const extracted = items.map((item) => ({
    entry: typeof item.webkitGetAsEntry === "function" ? item.webkitGetAsEntry() : null,
    file: typeof item.getAsFile === "function" ? item.getAsFile() : null,
  }));
  if (!extracted.some((x) => x.entry?.isDirectory)) return null;
  const folders: PendingFolder[] = [];
  const looseFiles: File[] = [];
  for (const { entry, file } of extracted) {
    if (entry?.isDirectory) {
      const files: PendingFolderFile[] = [];
      const emptyDirs: string[] = [];
      await walkEntry(entry, "", files, emptyDirs);
      folders.push({ folderName: entry.name, files, emptyDirs });
    } else if (file) {
      looseFiles.push(file);
    }
  }
  return { folders, looseFiles };
}

function pendingFolderFromPicker(files: File[]): PendingFolder | null {
  let root: string | null = null;
  const out: PendingFolderFile[] = [];
  for (const file of files) {
    const rel = file.webkitRelativePath || "";
    const slash = rel.indexOf("/");
    if (slash <= 0) return null;
    const segment = rel.slice(0, slash);
    if (root === null) root = segment;
    if (segment !== root) return null;
    out.push({ file, relativePath: rel });
  }
  return root ? { folderName: root, files: out, emptyDirs: [] } : null;
}

function pickFolderInput(): Promise<File[] | null> {
  if (typeof document === "undefined") return Promise.resolve(null);
  return new Promise((resolve) => {
    const input = document.createElement("input");
    input.type = "file";
    input.setAttribute("webkitdirectory", "");
    input.style.position = "fixed";
    input.style.left = "-9999px";
    input.style.width = "1px";
    input.style.height = "1px";
    document.body.appendChild(input);

    let settled = false;
    const finish = (files: File[] | null) => {
      if (settled) return;
      settled = true;
      window.removeEventListener("focus", onFocus);
      input.remove();
      resolve(files);
    };

    input.addEventListener("change", () =>
      finish(input.files && input.files.length > 0 ? Array.from(input.files) : null),
    );

    const onFocus = () => {
      window.setTimeout(() => {
        if (!input.files || input.files.length === 0) finish(null);
      }, 800);
    };
    window.addEventListener("focus", onFocus);

    input.click();
  });
}

async function materializeFolder(pending: PendingFolder): Promise<FolderSelection | null> {
  const pushToast = useUi.getState().pushToast;
  const stripRoot = (rel: string) => rel.slice(pending.folderName.length + 1);
  const emptyDirs = pending.emptyDirs.map(stripRoot).filter((rel) => rel.length > 0);
  if (WEB) {
    const staged: FolderFileEntry[] = [];
    try {
      for (const { file, relativePath } of pending.files) {
        const ref = await stageFile(file);
        staged.push({ file, relativePath: stripRoot(relativePath), localPath: ref.path, size: file.size });
      }
    } catch (e) {
      for (const s of staged) await dropStaged(s.localPath).catch(() => undefined);
      pushToast("error", `读取文件夹内容失败：${describeError(e)}`);
      return null;
    }
    return { folderName: pending.folderName, files: staged, emptyDirs };
  }
  const files: FolderFileEntry[] = [];
  for (const { file, relativePath } of pending.files) {
    const path = (file as File & { path?: string }).path;
    if (!path) {
      pushToast("info", "当前平台无法读取文件夹内文件的路径，无法上传文件夹");
      return null;
    }
    files.push({ file, relativePath: stripRoot(relativePath), localPath: path, size: file.size });
  }
  return { folderName: pending.folderName, files, emptyDirs };
}

function packProgressToast(folderName: string): (transferred: number, total: number) => void {
  const pushToast = useUi.getState().pushToast;
  let last = 0;
  return (transferred, total) => {
    if (total <= 0) return;
    const pct = Math.floor((transferred / total) * 100);
    if (pct >= 100 || pct < last + 25) return;
    last = pct;
    pushToast(
      "info",
      `打包上传「${folderName}」：${pct}%（${formatSize(transferred)} / ${formatSize(total)}）`,
    );
  };
}

async function chooseUploadMode(
  sessionId: string,
  selection: FolderSelection,
): Promise<"perFile" | "pack" | null> {
  const total = selection.files.reduce((sum, f) => sum + f.size, 0);
  const kind = useUi.getState().sessions.find((s) => s.id === sessionId)?.kind ?? "";
  const key = await askChoice(
    `上传文件夹「${selection.folderName}」：共 ${selection.files.length} 个文件，合计 ${formatSize(total)}`,
    {
      title: "上传文件夹",
      choices: [
        {
          key: "perFile",
          label: "逐文件上传",
          hint: "默认方式；失败的任务可单独重试续传",
          primary: true,
        },
        ...(packExtractSupported(kind)
          ? [
              {
                key: "pack",
                label: "打包为 tar 上传并自动解包",
                hint: "更快；同名文件将被覆盖，同名的文件与目录会相互替换（tar 语义）；需要远端 tar",
              },
            ]
          : []),
      ],
    },
  );
  return key === "perFile" || key === "pack" ? key : null;
}

async function uploadFolderSelection(
  args: FolderFlowArgs & { selection: FolderSelection },
): Promise<void> {
  const { selection } = args;
  const pushToast = useUi.getState().pushToast;
  const releaseStaged = () =>
    Promise.all(selection.files.map((f) => dropStaged(f.localPath).catch(() => undefined)));

  const kind = useUi.getState().sessions.find((s) => s.id === args.sessionId)?.kind ?? "";
  if (kind === "winrm") {
    const bad = [
      selection.folderName,
      ...selection.files.map((f) => f.relativePath),
      ...selection.emptyDirs,
    ].find((n) => n.includes("\\"));
    if (bad) {
      await releaseStaged();
      pushToast(
        "error",
        `无法上传到 Windows 会话：名称「${bad}」包含反斜杠，远端会把它当作路径分隔符，请重命名后再上传`,
      );
      return;
    }
  }

  if (selection.files.length > 0) {
    const mode = await chooseUploadMode(args.sessionId, selection);
    if (!mode) {
      await releaseStaged();
      pushToast("info", "已取消上传，所有文件保持原样");
      return;
    }
    if (mode === "pack") {
      try {
        pushToast("info", `正在打包上传「${selection.folderName}」…`);
        const target = await packUploadAndExtract({
          sessionId: args.sessionId,
          dir: args.dir,
          folderName: selection.folderName,
          files: selection.files,
          emptyDirs: selection.emptyDirs,
          onProgress: packProgressToast(selection.folderName),
        });
        pushToast(
          "success",
          `已上传并解包到 ${target}（${selection.files.length} 个文件）`,
        );
      } catch (e) {
        pushToast("error", `打包上传失败：${describeError(e)}`);
      } finally {
        await releaseStaged();
        args.onSettled?.();
      }
      return;
    }
  }

  let handed = false;
  try {
    handed = await enqueueFolderUpload({
      sessionId: args.sessionId,
      dir: args.dir,
      folderName: selection.folderName,
      files: selection.files,
      emptyDirs: selection.emptyDirs,
      knownSiblings: args.knownSiblings,
    });
  } catch (e) {
    pushToast("error", `上传失败：${describeError(e)}`);
  } finally {
    if (!handed) await releaseStaged();
    args.onSettled?.();
  }
}

export async function pickFolderUpload(args: FolderFlowArgs): Promise<void> {
  const picked = await pickFolderInput();
  if (!picked) return;
  const pending = pendingFolderFromPicker(picked);
  if (!pending) {
    useUi.getState().pushToast("info", "当前平台不支持选择文件夹，请改用拖拽文件夹上传");
    return;
  }
  const selection = await materializeFolder(pending);
  if (!selection) return;
  if (selection.files.length === 0) {
    useUi.getState().pushToast("info", "所选文件夹里没有可上传的文件");
    return;
  }
  await uploadFolderSelection({ ...args, selection });
}

export async function dropFolderUpload(
  args: FolderFlowArgs & { dataTransfer: DataTransfer },
): Promise<void> {
  const extracted = await pendingFoldersFromDrop(args.dataTransfer);
  if (!extracted) {
    await dropUploadFiles(args);
    return;
  }
  for (const pending of extracted.folders) {
    const selection = await materializeFolder(pending);
    if (!selection) continue;
    await uploadFolderSelection({ ...args, selection });
  }
  if (extracted.looseFiles.length > 0) {
    await dropLocalFiles({ ...args, files: extracted.looseFiles });
  }
}
