import { fsApi } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { EVENTS, listenEvent, type FsProgressEvent } from "../../ipc/events";
import { dropStaged, stageFile } from "../../ipc/webFiles";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { formatSize } from "./fileTypes";
import { joinPath } from "./pathUtils";
import type { FolderFileEntry } from "./folderUpload";

export type { FolderFileEntry };

export const MAX_PACK_BYTES = 256 * 1024 * 1024;
// Mirrors deviceproto.MaxWriteFileSize: the desktop device path writes the tar in one
// WriteFile request, which the device bridge caps at 64 MiB.
export const MAX_PACK_BYTES_DEVICE = 64 * 1024 * 1024;

export interface PackUploadArgs {
  sessionId: string;
  dir: string;
  folderName: string;
  files: FolderFileEntry[];
  // relative to the folder root, same convention as FolderFileEntry.relativePath
  emptyDirs?: string[];
  onProgress: (transferred: number, total: number) => void;
}

export function packExtractSupported(sessionKind: string): boolean {
  return sessionKind === "ssh" || sessionKind === "device";
}

const BLOCK = 512;
const TYPE_FILE = 0x30; // '0'
const TYPE_DIR = 0x35; // '5'
const encoder = new TextEncoder();
const decoder = new TextDecoder();

function putAscii(block: Uint8Array, offset: number, text: string): void {
  for (let i = 0; i < text.length; i++) block[offset + i] = text.charCodeAt(i);
}

function putOctal(block: Uint8Array, offset: number, length: number, value: number): void {
  putAscii(block, offset, Math.floor(value).toString(8).padStart(length - 1, "0"));
}

// ustar long-name split: the name field holds at most 100 bytes; the head (up to 155
// bytes) moves to the prefix field and the remote tar recomposes prefix + "/" + name.
function splitTarName(full: Uint8Array): { name: Uint8Array; prefix: Uint8Array } {
  if (full.length <= 100) return { name: full, prefix: new Uint8Array(0) };
  for (let i = Math.max(0, full.length - 101); i < full.length; i++) {
    if (full[i] !== 0x2f) continue;
    const prefix = full.subarray(0, i);
    const name = full.subarray(i + 1);
    if (prefix.length <= 155 && name.length > 0 && name.length <= 100) return { name, prefix };
  }
  throw new Error(`路径过长，无法打包：${decoder.decode(full)}`);
}

function writeHeader(
  out: Uint8Array,
  offset: number,
  fullName: Uint8Array,
  size: number,
  mtime: number,
  typeflag: number,
): void {
  const h = out.subarray(offset, offset + BLOCK);
  const { name, prefix } = splitTarName(fullName);
  h.set(name, 0);
  putOctal(h, 100, 8, typeflag === TYPE_DIR ? 0o755 : 0o644);
  putOctal(h, 108, 8, 0);
  putOctal(h, 116, 8, 0);
  putOctal(h, 124, 12, size);
  putOctal(h, 136, 12, mtime);
  for (let i = 0; i < 8; i++) h[148 + i] = 0x20;
  h[156] = typeflag;
  putAscii(h, 257, "ustar");
  putAscii(h, 263, "00");
  h.set(prefix, 345);
  let sum = 0;
  for (let i = 0; i < BLOCK; i++) sum += h[i];
  putAscii(h, 148, sum.toString(8).padStart(6, "0"));
  h[154] = 0;
  h[155] = 0x20;
}

export async function buildFolderTar(
  files: FolderFileEntry[],
  rootPrefix = "",
  emptyDirs: string[] = [],
): Promise<Uint8Array<ArrayBuffer>> {
  const names = files.map((f) =>
    rootPrefix ? `${rootPrefix}/${f.relativePath}` : f.relativePath,
  );
  const dirs = new Set<string>();
  for (const name of names) {
    const parts = name.split("/");
    let acc = "";
    for (let i = 0; i < parts.length - 1; i++) {
      acc = acc ? `${acc}/${parts[i]}` : parts[i];
      dirs.add(acc);
    }
  }
  for (const rel of emptyDirs) {
    const name = rootPrefix ? `${rootPrefix}/${rel}` : rel;
    const parts = name.split("/");
    let acc = "";
    for (const part of parts) {
      acc = acc ? `${acc}/${part}` : part;
      dirs.add(acc);
    }
  }
  const dirList = [...dirs].sort();
  const dirMtime = files.reduce((max, f) => Math.max(max, Math.floor(f.file.lastModified / 1000)), 0);

  let total = 2 * BLOCK;
  total += dirList.length * BLOCK;
  for (const f of files) total += BLOCK + Math.ceil(f.file.size / BLOCK) * BLOCK;
  const out = new Uint8Array(total);

  let offset = 0;
  for (const dir of dirList) {
    writeHeader(out, offset, encoder.encode(`${dir}/`), 0, dirMtime, TYPE_DIR);
    offset += BLOCK;
  }
  for (const [i, f] of files.entries()) {
    const data = new Uint8Array(await f.file.arrayBuffer());
    writeHeader(out, offset, encoder.encode(names[i]), data.length, Math.floor(f.file.lastModified / 1000), TYPE_FILE);
    offset += BLOCK;
    out.set(data, offset);
    offset += Math.ceil(data.length / BLOCK) * BLOCK;
  }
  return out;
}

function bytesToBase64(data: Uint8Array): string {
  let binary = "";
  const chunk = 0x8000;
  for (let i = 0; i < data.length; i += chunk) {
    binary += String.fromCharCode(...data.subarray(i, i + chunk));
  }
  return btoa(binary);
}

function tempArchiveName(): string {
  const rand = new Uint8Array(6);
  crypto.getRandomValues(rand);
  const suffix = Array.from(rand, (b) => b.toString(16).padStart(2, "0")).join("");
  return `.nexterm-upload-${suffix}.tar`;
}

async function uploadTar(
  sessionId: string,
  remotePath: string,
  tar: Uint8Array<ArrayBuffer>,
  archiveName: string,
  onProgress: (transferred: number, total: number) => void,
): Promise<void> {
  if (WEB) {
    const staged = await stageFile(new File([tar], archiveName));
    // The progress task id is assigned server-side per call; matching on the exact tar
    // size keeps events from other transfers out of this report.
    const unlisten = await listenEvent<FsProgressEvent>(EVENTS.fsProgress, (event) => {
      if (event.total === tar.length) onProgress(event.transferred, event.total);
    });
    try {
      await fsApi.upload(sessionId, staged.path, remotePath, false);
    } finally {
      unlisten();
      await dropStaged(staged.path).catch(() => undefined);
    }
    onProgress(tar.length, tar.length);
    return;
  }
  // Desktop has no blob staging and no IPC turning browser bytes into a local file, so
  // the tar goes straight to the remote through fs_write (base64, single-shot progress).
  onProgress(0, tar.length);
  await fsApi.write(sessionId, remotePath, bytesToBase64(tar), false);
  onProgress(tar.length, tar.length);
}

export async function packUploadAndExtract(args: PackUploadArgs): Promise<string> {
  const { sessionId, dir, folderName, files, emptyDirs, onProgress } = args;
  if (files.length === 0) throw new Error("所选文件夹里没有可上传的文件");
  if (!folderName || folderName.includes("/") || folderName === "." || folderName === "..") {
    throw new Error(`文件夹名称无效：${folderName}`);
  }
  const kind = useUi.getState().sessions.find((s) => s.id === sessionId)?.kind ?? "";
  if (!packExtractSupported(kind)) {
    throw new Error(`当前会话类型（${kind || "未知"}）不支持打包上传`);
  }
  const totalBytes = files.reduce((sum, f) => sum + f.file.size, 0);
  // The 64MB cap applies only to the desktop fs_write path (device WriteFile is buffered and capped);
  // WEB streams through staged upload (OpenWrite) and is uncapped.
  if (kind === "device" && !WEB && totalBytes > MAX_PACK_BYTES_DEVICE) {
    throw new Error(
      `文件夹总大小 ${formatSize(totalBytes)} 超过设备连接的单次写入上限 ${formatSize(MAX_PACK_BYTES_DEVICE)}，无法打包上传，请改用逐文件上传`,
    );
  }
  if (totalBytes > MAX_PACK_BYTES) {
    throw new Error(
      `文件夹总大小 ${formatSize(totalBytes)} 超过打包上传上限 ${formatSize(MAX_PACK_BYTES)}，请改用普通上传逐文件传输`,
    );
  }
  // The two backends extract differently. ssh extracts into a directory derived from the
  // archive name (<parent>/<name minus extension>/), so the temp archive must be named
  // after the folder with root-less entries, and a pre-check refuses to clobber a
  // same-named existing file or link. device extracts in place into the archive's
  // parent directory, so entries carry the folder prefix and the temp archive stays
  // hidden and random.
  const sshDerived = kind === "ssh";
  const archiveName = sshDerived ? `${folderName}.tar` : tempArchiveName();
  const remoteTemp = joinPath(dir, archiveName);
  if (sshDerived) {
    const siblings = await fsApi.list(sessionId, dir);
    if (siblings.some((e) => e.kind !== "dir" && e.name === archiveName)) {
      throw new Error(`无法打包上传：目标目录已存在 ${archiveName}，请先移走或重命名它`);
    }
  }
  const tar = await buildFolderTar(files, sshDerived ? "" : folderName, emptyDirs);
  try {
    await uploadTar(sessionId, remoteTemp, tar, archiveName, onProgress);
  } catch (e) {
    await fsApi.delete(sessionId, remoteTemp, false).catch(() => undefined);
    throw new Error(`上传打包文件失败：${describeError(e)}`);
  }
  try {
    await fsApi.extract(sessionId, remoteTemp);
  } catch (e) {
    throw new Error(`远端解压失败：${describeError(e)}`);
  } finally {
    await fsApi.delete(sessionId, remoteTemp, false).catch(() => undefined);
  }
  return joinPath(dir, folderName);
}
