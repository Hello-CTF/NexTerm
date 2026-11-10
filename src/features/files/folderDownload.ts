// Downloading remote files and folders into one picked local folder, keeping their structure. Desktop only: the
// browser has no writable folder target (WebKitGTK lacks the File System Access API), so the browser keeps per-file
// downloads and the tar.gz folder download.

import { isMac, isWindows } from "../../app/platform";
import { useUi } from "../../app/store";
import { fsApi, localFileApi } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import type { FileEntryDto } from "../../ipc/types";
import { askChoice, isNativeMobileShell, pickLocalFolder } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { t } from "../../i18n";
import { takenNames, uniqueRemoteName, type TakenNames } from "./transferQueue";
import { enqueueDownloadBatch, type DownloadItem, type SkippedItem } from "./transferStore";

export type DownloadConflictPolicy = "skip" | "overwrite" | "newer" | "rename";

// The backend accepts 20000 folders per request and the picked folder itself is one of them, so a batch holds at
// most one entry fewer.
export const FOLDER_DOWNLOAD_MAX_ENTRIES = 19999;

interface RemoteFile {
  remotePath: string;
  // Path segments below the picked local folder.
  segments: string[];
  size: number;
  mtime: number;
}

interface RemoteTree {
  files: RemoteFile[];
  // Every folder to create, as segments below the picked local folder (empty folders included).
  dirs: string[][];
  skipped: SkippedItem[];
}

export type FolderDownloadOutcome =
  | { status: "cancelled" }
  | { status: "failed"; error: string }
  | { status: "queued"; queued: number; skipped: number; batchId: string | null };

export function folderDownloadAvailable(): boolean {
  return !WEB && !isNativeMobileShell();
}

function localCaseInsensitive(): boolean {
  return isWindows() || isMac();
}

// A remote name that is not a single plain local path segment would escape or reshape the target folder.
function unusableLocalName(name: string): boolean {
  if (name === "" || name === "." || name === ".." || /[\\/\0]/.test(name)) return true;
  if (isWindows() && (/[<>:"|?*]/.test(name) || /[. ]$/.test(name))) return true;
  if (isWindows() && WINDOWS_RESERVED_STEM.test(name.split(".")[0])) return true;
  return false;
}

// Device names Windows resolves to the device even with an extension (con.txt, NUL.tar.gz).
const WINDOWS_RESERVED_STEM = /^(con|prn|aux|nul|com[1-9\u00b9\u00b2\u00b3]|lpt[1-9\u00b9\u00b2\u00b3])$/i;

function unusableNameReason(name: string): string {
  if (isWindows() && WINDOWS_RESERVED_STEM.test(name.split(".")[0]) && !/[\\/\0<>:"|?*]/.test(name)) {
    return t("名称是 Windows 保留设备名");
  }
  return t("名称无法在本机使用");
}

function localSeparator(root: string): string {
  return root.includes("\\") && !root.includes("/") ? "\\" : "/";
}

function joinLocal(root: string, segments: string[]): string {
  if (segments.length === 0) return root;
  const sep = localSeparator(root);
  return [root.replace(/[\\/]+$/, ""), ...segments].join(sep);
}

function aborted(signal?: AbortSignal): boolean {
  return signal?.aborted === true;
}

class FolderDownloadAborted extends Error {}

class FolderDownloadTooLarge extends Error {}

async function collectRemoteTree(sessionId: string, entries: FileEntryDto[], signal?: AbortSignal): Promise<RemoteTree> {
  const tree: RemoteTree = { files: [], dirs: [], skipped: [] };
  const skip = (remotePath: string, reason: string) => tree.skipped.push({ remotePath, localPath: "", reason });
  const visit = async (entry: FileEntryDto, prefix: string[]): Promise<void> => {
    if (aborted(signal)) throw new FolderDownloadAborted();
    if (tree.files.length + tree.dirs.length >= FOLDER_DOWNLOAD_MAX_ENTRIES) throw new FolderDownloadTooLarge();
    if (unusableLocalName(entry.name)) {
      skip(entry.path, unusableNameReason(entry.name));
      return;
    }
    const segments = [...prefix, entry.name];
    if (entry.kind === "file") {
      tree.files.push({ remotePath: entry.path, segments, size: entry.size, mtime: entry.mtime });
      return;
    }
    if (entry.kind === "symlink") {
      skip(entry.path, t("符号链接不会被跟随"));
      return;
    }
    if (entry.kind !== "dir") {
      skip(entry.path, t("不是普通文件"));
      return;
    }
    let children: FileEntryDto[];
    try {
      children = await fsApi.list(sessionId, entry.path);
    } catch (error) {
      skip(entry.path, t("无法列出目录：{value}", { value: describeError(error) }));
      return;
    }
    tree.dirs.push(segments);
    for (const child of children) await visit(child, segments);
  };
  for (const entry of entries) await visit(entry, []);
  return tree;
}

interface LocalEntry {
  name: string;
  kind: string;
  size: number;
  mtimeMs: number;
}

interface LocalDir {
  kind: string;
  entries: LocalEntry[];
}

async function inspectLocalDirs(token: string, paths: string[]): Promise<Map<string, LocalDir>> {
  const listing = await localFileApi.listDirs(token, paths);
  return new Map(listing.dirs.map((dir) => [dir.path, { kind: dir.kind, entries: dir.entries }]));
}

async function askDownloadConflictPolicy(conflicts: number, sample: string): Promise<DownloadConflictPolicy | null> {
  const key = await askChoice(
    conflicts === 1
      ? t("本机目标文件夹中已存在「{sample}」。如何处理？", { sample })
      : t("本机目标文件夹中已有 {conflicts} 个同名项（如「{sample}」）。对本次下载统一如何处理？", { conflicts, sample }),
    {
      title: t("下载冲突"),
      level: "warning",
      choices: [
        { key: "skip", label: t("跳过"), hint: t("保留本机现有文件"), primary: true },
        { key: "newer", label: t("较新或大小不同时覆盖"), hint: t("远端修改时间更新或大小不同才替换本机文件") },
        { key: "overwrite", label: t("覆盖"), hint: t("用远端文件替换本机同名文件"), danger: true },
        { key: "rename", label: t("保留两者"), hint: t("本机自动重命名（如 a (1).txt）后下载") },
      ],
    },
  );
  return key === "skip" || key === "newer" || key === "overwrite" || key === "rename" ? key : null;
}

function findLocal(entries: LocalEntry[] | undefined, name: string, caseInsensitive: boolean): LocalEntry | undefined {
  if (!entries) return undefined;
  const folded = caseInsensitive ? name.toLowerCase() : name;
  return entries.find((entry) => (caseInsensitive ? entry.name.toLowerCase() : entry.name) === folded);
}

// planFolderDownload maps the remote tree onto the picked local folder, settling every conflict with one answer.
async function planFolderDownload(root: string, token: string, tree: RemoteTree): Promise<{ ready: DownloadItem[]; skipped: SkippedItem[]; dirs: string[] } | null> {
  const ci = localCaseInsensitive();
  const skipped = [...tree.skipped];
  const dirPath = (segments: string[]) => joinLocal(root, segments);
  const neededDirs = new Map<string, string[]>();
  neededDirs.set(root, []);
  for (const segments of tree.dirs) {
    for (let depth = 1; depth <= segments.length; depth += 1) {
      const prefix = segments.slice(0, depth);
      neededDirs.set(dirPath(prefix), prefix);
    }
  }
  const local = await inspectLocalDirs(token, [...neededDirs.keys()]);
  // A folder the download needs is occupied by a file (or link) in the local tree: nothing below it can be written.
  const blocked = new Map<string, string>();
  for (const [path] of neededDirs) {
    const kind = local.get(path)?.kind;
    if (kind !== undefined && kind !== "dir" && kind !== "missing") blocked.set(path, kind);
  }
  const blockedAncestor = (segments: string[]) => {
    for (let depth = 0; depth < segments.length; depth += 1) {
      const path = dirPath(segments.slice(0, depth));
      if (blocked.has(path)) return path;
    }
    return null;
  };
  const taken = new Map<string, TakenNames>();
  const takenIn = (dir: string) => {
    let names = taken.get(dir);
    if (!names) {
      names = takenNames(
        (local.get(dir)?.entries ?? []).map((entry) => entry.name),
        ci,
      );
      taken.set(dir, names);
    }
    return names;
  };
  interface Conflict {
    file: RemoteFile;
    dir: string;
    existing: LocalEntry;
  }
  const ready: DownloadItem[] = [];
  const conflicts: Conflict[] = [];
  const claimed = new Map<string, TakenNames>();
  const claimedIn = (dir: string) => {
    let names = claimed.get(dir);
    if (!names) {
      names = takenNames([], ci);
      claimed.set(dir, names);
    }
    return names;
  };
  const claim = (dir: string, name: string) => {
    claimedIn(dir).add(name);
    takenIn(dir).add(name);
  };
  const renameInto = (dir: string, name: string) => {
    const names = takenIn(dir);
    const finalName = uniqueRemoteName(name, names);
    claim(dir, finalName);
    return joinLocal(dir, [finalName]);
  };
  for (const file of tree.files) {
    const parentSegments = file.segments.slice(0, -1);
    const name = file.segments[file.segments.length - 1];
    const dir = dirPath(parentSegments);
    const blocker = blockedAncestor(file.segments);
    if (blocker) {
      const reason = blocked.get(blocker) === "symlink"
        ? t("本机「{blocker}」是符号链接，不会写入其中", { blocker })
        : t("本机「{blocker}」不是文件夹", { blocker });
      skipped.push({ remotePath: file.remotePath, localPath: "", reason });
      continue;
    }
    if (claimedIn(dir).has(name)) {
      // Two remote names that fold to one local name (case-insensitive disk): keep both without asking.
      ready.push({ remotePath: file.remotePath, localPath: renameInto(dir, name) });
      continue;
    }
    const existing = findLocal(local.get(dir)?.entries, name, ci);
    if (existing) {
      conflicts.push({ file, dir, existing });
      claimedIn(dir).add(name);
      continue;
    }
    claim(dir, name);
    ready.push({ remotePath: file.remotePath, localPath: joinLocal(dir, [name]) });
  }
  if (conflicts.length > 0) {
    const policy = await askDownloadConflictPolicy(conflicts.length, conflicts[0].file.segments.join("/"));
    if (!policy) return null;
    for (const { file, dir, existing } of conflicts) {
      const name = file.segments[file.segments.length - 1];
      const target = joinLocal(dir, [name]);
      if (policy === "rename") {
        ready.push({ remotePath: file.remotePath, localPath: renameInto(dir, name) });
        continue;
      }
      if (policy === "skip") {
        skipped.push({ remotePath: file.remotePath, localPath: target, reason: t("本机已存在") });
        continue;
      }
      if (existing.kind !== "file") {
        skipped.push({ remotePath: file.remotePath, localPath: target, reason: t("本机同名项不是普通文件") });
        continue;
      }
      if (policy === "newer" && file.mtime <= existing.mtimeMs && file.size === existing.size) {
        skipped.push({ remotePath: file.remotePath, localPath: target, reason: t("本机文件已是最新") });
        continue;
      }
      ready.push({ remotePath: file.remotePath, localPath: joinLocal(dir, [existing.name]) });
    }
  }
  const dirs = [...neededDirs.entries()]
    .filter(([path, segments]) => local.get(path)?.kind !== "dir" && blockedAncestor([...segments, ""]) === null)
    .map(([path]) => path);
  return { ready, skipped, dirs };
}

function parentLocal(path: string): string {
  const index = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  return index > 0 ? path.slice(0, index) : path;
}

// downloadToFolder asks for one local folder and queues every selected file, and every file below the selected
// folders, as a download that keeps the remote structure.
export async function downloadToFolder(args: {
  sessionId: string;
  entries: FileEntryDto[];
  signal?: AbortSignal;
  // Offered as the toast button while the remote tree is being listed; it should abort signal.
  onCancel?: () => void;
}): Promise<FolderDownloadOutcome> {
  const pushToast = useUi.getState().pushToast;
  let root: string | null;
  try {
    root = await pickLocalFolder();
  } catch (error) {
    const message = t("无法选择本机文件夹：{value}", { value: describeError(error) });
    pushToast("error", message);
    return { status: "failed", error: message };
  }
  if (!root) return { status: "cancelled" };
  let token: string;
  try {
    const grant = await localFileApi.authorizeFolder(root);
    token = grant.token;
    root = grant.root;
  } catch (error) {
    const message = t("无法使用所选本机文件夹：{value}", { value: describeError(error) });
    pushToast("error", message);
    return { status: "failed", error: message };
  }
  pushToast("info", t("正在列出要下载的文件…"), args.onCancel ? { label: t("取消"), onSelect: args.onCancel } : undefined);
  let tree: RemoteTree;
  try {
    tree = await collectRemoteTree(args.sessionId, args.entries, args.signal);
  } catch (error) {
    if (error instanceof FolderDownloadAborted) {
      pushToast("info", t("已取消下载，本机文件保持原样"));
      return { status: "cancelled" };
    }
    const message = error instanceof FolderDownloadTooLarge
      ? t("所选内容超过 {limit} 项，请分批下载", { limit: FOLDER_DOWNLOAD_MAX_ENTRIES })
      : t("列出远端文件失败：{value}", { value: describeError(error) });
    pushToast("error", message);
    return { status: "failed", error: message };
  }
  if (aborted(args.signal)) {
    pushToast("info", t("已取消下载，本机文件保持原样"));
    return { status: "cancelled" };
  }
  let plan: Awaited<ReturnType<typeof planFolderDownload>>;
  try {
    plan = await planFolderDownload(root, token, tree);
  } catch (error) {
    const message = t("无法读取本机文件夹：{value}", { value: describeError(error) });
    pushToast("error", message);
    return { status: "failed", error: message };
  }
  if (!plan || aborted(args.signal)) {
    pushToast("info", t("已取消下载，本机文件保持原样"));
    return { status: "cancelled" };
  }
  let { ready } = plan;
  const skipped = plan.skipped;
  if (plan.dirs.length > 0) {
    let failed: { path: string; error: string }[];
    try {
      failed = (await localFileApi.createDirs(token, plan.dirs)).failed;
    } catch (error) {
      failed = plan.dirs.map((path) => ({ path, error: describeError(error) }));
    }
    if (failed.length > 0) {
      const failedPaths = failed.map((item) => item.path);
      const under = (path: string) => failedPaths.find((dir) => path === dir || path.startsWith(dir + localSeparator(root)));
      ready = ready.filter((item) => {
        const dir = under(parentLocal(item.localPath));
        if (!dir) return true;
        const reason = failed.find((entry) => entry.path === dir)?.error ?? "";
        skipped.push({ ...item, reason: t("无法创建本机文件夹「{dir}」：{reason}", { dir, reason }) });
        return false;
      });
      pushToast("error", t("部分本机文件夹创建失败：{value}", { value: failedPaths[0] }));
    }
  }
  if (ready.length === 0 && skipped.length === 0) {
    pushToast("info", t("所选内容里没有可下载的文件，已在 {root} 创建对应文件夹", { root }));
    return { status: "queued", queued: 0, skipped: 0, batchId: null };
  }
  const batchId = enqueueDownloadBatch(args.sessionId, ready, skipped);
  if (ready.length > 0) {
    pushToast(
      "success",
      skipped.length > 0
        ? t("已加入 {queued} 个下载任务到 {root}，{skipped} 项已跳过", { queued: ready.length, root, skipped: skipped.length })
        : t("已加入 {queued} 个下载任务到 {root}", { queued: ready.length, root }),
    );
  }
  return { status: "queued", queued: ready.length, skipped: skipped.length, batchId };
}

// startFolderDownload is downloadToFolder with a cancel button on the listing toast, for menu call sites.
export function startFolderDownload(sessionId: string, entries: FileEntryDto[]): Promise<FolderDownloadOutcome> {
  const controller = new AbortController();
  return downloadToFolder({ sessionId, entries, signal: controller.signal, onCancel: () => controller.abort() });
}
