import { fsApi, localFileApi } from "../../ipc/commands";
import { bytesFromBase64, bytesToBase64 } from "./fileCodec";

export const UPLOAD_CHUNK_SIZE = 1024 * 1024;
const MAX_UPLOAD_CHUNK_SIZE = 4 * 1024 * 1024;

export interface ChunkReadSource {
  readonly size: number;
  read(offset: number, length: number): Promise<Uint8Array<ArrayBuffer>>;
}

export function fileChunkSource(file: File): ChunkReadSource {
  return {
    size: file.size,
    async read(offset, length) {
      const data = new Uint8Array(await file.slice(offset, offset + length).arrayBuffer());
      if (data.length !== length) {
        throw new Error(`文件「${file.name}」的大小在读取期间发生了变化，请重新上传`);
      }
      return data;
    },
  };
}

export function localPathChunkSource(localPath: string, size: number, displayPath = localPath): ChunkReadSource {
  return {
    size,
    async read(offset, length) {
      const result = await localFileApi.readRange(localPath, offset, length);
      const data = bytesFromBase64(result.contentBase64);
      if (result.offset !== offset || result.size !== size || data.length !== length) {
        throw new Error(`文件「${displayPath}」的大小在读取期间发生了变化，请重新上传`);
      }
      return data as Uint8Array<ArrayBuffer>;
    },
  };
}

async function sha256Hex(data: Uint8Array<ArrayBuffer>): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", data.buffer);
  return Array.from(new Uint8Array(digest), (value) => value.toString(16).padStart(2, "0")).join("");
}

function retryableChunkError(error: unknown): boolean {
  const code = (error as { code?: unknown } | null)?.code;
  return code !== "cancelled" && code !== "account_changed" && code !== "bad_param" && code !== "forbidden";
}

async function abortWithRetry(args: { sessionId: string; uploadId: string; taskId: string; attemptId: string }): Promise<void> {
  let lastError: unknown;
  for (let attempt = 0; attempt < 2; attempt += 1) {
    try {
      await fsApi.uploadAbort(args);
      return;
    } catch (error) {
      lastError = error;
    }
  }
  console.warn("[files] 上传临时块清理失败；后端会话保留用于重试或空闲回收", lastError);
}

export async function uploadChunked(args: {
  sessionId: string;
  remotePath: string;
  source: ChunkReadSource;
  taskId: string;
  attemptId: string;
  onProgress?: (transferred: number, total: number) => void;
}): Promise<void> {
  const { sessionId, remotePath, source, taskId, attemptId, onProgress } = args;
  if (!Number.isSafeInteger(source.size) || source.size < 0) {
    throw new Error("上传文件大小无效");
  }
  onProgress?.(0, source.size);
  let uploadId: string | undefined;
  let committed = false;
  try {
    const begun = await fsApi.uploadBegin({
      sessionId,
      remotePath,
      totalSize: source.size,
      chunkSize: UPLOAD_CHUNK_SIZE,
      taskId,
      attemptId,
    });
    uploadId = begun.uploadId;
    if (
      !Number.isSafeInteger(begun.chunkSize) ||
      begun.chunkSize <= 0 ||
      begun.chunkSize > MAX_UPLOAD_CHUNK_SIZE
    ) {
      throw new Error(`服务端返回了无效的上传块大小：${begun.chunkSize}`);
    }
    const totalChunks = Math.ceil(source.size / begun.chunkSize);
    for (let index = 0; index < totalChunks; index += 1) {
      const offset = index * begun.chunkSize;
      const length = Math.min(begun.chunkSize, source.size - offset);
      const data = await source.read(offset, length);
      if (data.length !== length) {
        throw new Error(`读取上传块 ${index} 时返回了 ${data.length} 字节，预期 ${length} 字节`);
      }
      const contentBase64 = bytesToBase64(data);
      const sha256 = await sha256Hex(data);
      let lastError: unknown;
      for (let attempt = 0; attempt < 2; attempt += 1) {
        try {
          const progress = await fsApi.uploadChunk({
            sessionId,
            uploadId,
            taskId,
            attemptId,
            index,
            size: data.length,
            sha256,
            contentBase64,
          });
          if (progress.total !== source.size || progress.received < 0 || progress.received > source.size) {
            throw new Error("服务端返回了无效的上传进度");
          }
          onProgress?.(progress.received, progress.total);
          lastError = undefined;
          break;
        } catch (error) {
          lastError = error;
          if (attempt === 1 || !retryableChunkError(error)) throw error;
        }
      }
      if (lastError !== undefined) throw lastError;
    }
    await fsApi.uploadCommit({ sessionId, uploadId, taskId, attemptId });
    committed = true;
    onProgress?.(source.size, source.size);
  } finally {
    if (uploadId && !committed) {
      await abortWithRetry({ sessionId, uploadId, taskId, attemptId });
    }
  }
}
