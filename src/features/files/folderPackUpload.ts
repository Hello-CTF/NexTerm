import { fsApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import {
  fileChunkSource,
  localPathChunkSource,
  UPLOAD_CHUNK_SIZE,
  uploadChunked,
  type ChunkReadSource,
} from "./chunkedUpload";
import { formatSize } from "./fileTypes";
import { joinPath } from "./pathUtils";
import type { FolderFileEntry } from "./folderUpload";
import { createTransferTaskId } from "./transferTaskId";

export type { FolderFileEntry };

export const MAX_PACK_BYTES = 256 * 1024 * 1024;

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

interface FolderTarPlan {
  names: string[];
  dirList: string[];
  dirMtime: number;
  size: number;
}

function planFolderTar(files: FolderFileEntry[], rootPrefix: string, emptyDirs: string[]): FolderTarPlan {
  const names = files.map((f) => (rootPrefix ? `${rootPrefix}/${f.relativePath}` : f.relativePath));
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
  const dirMtime = files.reduce((max, f) => Math.max(max, Math.floor(f.mtimeMs / 1000)), 0);
  let size = 2 * BLOCK + dirList.length * BLOCK;
  for (const f of files) size += BLOCK + Math.ceil(f.size / BLOCK) * BLOCK;
  return { names, dirList, dirMtime, size };
}

function tarHeader(name: string, size: number, mtime: number, typeflag: number): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(BLOCK);
  writeHeader(out, 0, encoder.encode(name), size, mtime, typeflag);
  return out;
}

function entrySource(file: FolderFileEntry): ChunkReadSource {
  return file.file
    ? fileChunkSource(file.file)
    : localPathChunkSource(file.localPath, file.size, file.relativePath);
}

async function* folderTarPieces(files: FolderFileEntry[], plan: FolderTarPlan) {
  for (const dir of plan.dirList) {
    yield tarHeader(`${dir}/`, 0, plan.dirMtime, TYPE_DIR);
  }
  for (const [index, file] of files.entries()) {
    yield tarHeader(plan.names[index], file.size, Math.floor(file.mtimeMs / 1000), TYPE_FILE);
    const source = entrySource(file);
    for (let offset = 0; offset < file.size; offset += UPLOAD_CHUNK_SIZE) {
      const length = Math.min(UPLOAD_CHUNK_SIZE, file.size - offset);
      yield await source.read(offset, length);
    }
    const padding = (BLOCK - (file.size % BLOCK)) % BLOCK;
    if (padding > 0) yield new Uint8Array(padding);
  }
  yield new Uint8Array(2 * BLOCK);
}

export function folderTarSource(
  files: FolderFileEntry[],
  rootPrefix = "",
  emptyDirs: string[] = [],
): ChunkReadSource {
  const plan = planFolderTar(files, rootPrefix, emptyDirs);
  const pieces = folderTarPieces(files, plan);
  let position = 0;
  let pending: Uint8Array<ArrayBuffer> | null = null;
  let pendingOffset = 0;
  let finished = false;
  return {
    size: plan.size,
    async read(offset, length) {
      if (offset !== position || length < 0 || offset + length > plan.size) {
        throw new Error("打包读取偏移无效");
      }
      const out = new Uint8Array(length);
      let written = 0;
      while (written < length && !finished) {
        if (!pending) {
          const next = await pieces.next();
          if (next.done) {
            finished = true;
            break;
          }
          pending = next.value;
          pendingOffset = 0;
        }
        const count = Math.min(length - written, pending.length - pendingOffset);
        out.set(pending.subarray(pendingOffset, pendingOffset + count), written);
        written += count;
        pendingOffset += count;
        if (pendingOffset === pending.length) pending = null;
      }
      if (written !== length) throw new Error("打包数据在读取期间提前结束");
      position += written;
      return out;
    },
  };
}

export async function buildFolderTar(
  files: FolderFileEntry[],
  rootPrefix = "",
  emptyDirs: string[] = [],
): Promise<Uint8Array<ArrayBuffer>> {
  const source = folderTarSource(files, rootPrefix, emptyDirs);
  const out = new Uint8Array(source.size);
  for (let offset = 0; offset < out.length; offset += UPLOAD_CHUNK_SIZE) {
    const length = Math.min(UPLOAD_CHUNK_SIZE, out.length - offset);
    out.set(await source.read(offset, length), offset);
  }
  return out;
}

function tempArchiveName(): string {
  const rand = new Uint8Array(6);
  crypto.getRandomValues(rand);
  const suffix = Array.from(rand, (b) => b.toString(16).padStart(2, "0")).join("");
  return `.nexterm-upload-${suffix}.tar`;
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
  const totalBytes = files.reduce((sum, f) => sum + f.size, 0);
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
  const tar = folderTarSource(files, sshDerived ? "" : folderName, emptyDirs);
  const taskId = createTransferTaskId();
  const attemptId = createTransferTaskId();
  try {
    await uploadChunked({ sessionId, remotePath: remoteTemp, source: tar, taskId, attemptId, onProgress });
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
