import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  begin: vi.fn(),
  chunk: vi.fn(),
  commit: vi.fn(),
  abort: vi.fn(),
  readRange: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  fsApi: {
    uploadBegin: mocks.begin,
    uploadChunk: mocks.chunk,
    uploadCommit: mocks.commit,
    uploadAbort: mocks.abort,
  },
  localFileApi: { readRange: mocks.readRange },
}));

import {
  localPathChunkSource,
  UPLOAD_CHUNK_SIZE,
  uploadChunked,
  type ChunkReadSource,
} from "../../features/files/chunkedUpload";

beforeEach(() => {
  vi.resetAllMocks();
  mocks.begin.mockResolvedValue({ uploadId: "upload-1", chunkSize: 4 });
  mocks.chunk.mockResolvedValue({ received: 4, total: 4 });
  mocks.commit.mockResolvedValue(4);
  mocks.abort.mockResolvedValue(null);
});

describe("chunked upload IPC consumer contract", () => {
  it("keeps content bytes, chunk hash and task-attempt identity stable through commit", async () => {
    const bytes = new Uint8Array([0, 1, 2, 255]);
    const source: ChunkReadSource = { size: bytes.length, read: vi.fn(async () => bytes) };

    await uploadChunked({
      sessionId: "session-1",
      remotePath: "/remote/a.bin",
      source,
      taskId: "task-1",
      attemptId: "attempt-1",
    });

    expect(mocks.begin).toHaveBeenCalledWith({
      sessionId: "session-1",
      remotePath: "/remote/a.bin",
      totalSize: 4,
      chunkSize: UPLOAD_CHUNK_SIZE,
      taskId: "task-1",
      attemptId: "attempt-1",
    });
    expect(mocks.chunk).toHaveBeenCalledWith({
      sessionId: "session-1",
      uploadId: "upload-1",
      taskId: "task-1",
      attemptId: "attempt-1",
      index: 0,
      size: 4,
      sha256: "3d1f57c984978ef98a18378c8166c1cb8ede02c03eeb6aee7e2f121dfeee3e56",
      contentBase64: "AAEC/w==",
    });
    expect(mocks.commit).toHaveBeenCalledWith({
      sessionId: "session-1",
      uploadId: "upload-1",
      taskId: "task-1",
      attemptId: "attempt-1",
    });
    expect(mocks.abort).not.toHaveBeenCalled();
  });

  it("preserves native paths and accepts a truncated range when the requested bytes match", async () => {
    mocks.readRange.mockResolvedValue({
      path: "C:\\pics\\a.bin",
      offset: 2,
      size: 8,
      contentBase64: "AAEC/w==",
      truncated: true,
    });
    const source = localPathChunkSource("C:\\pics\\a.bin", 8);

    await expect(source.read(2, 4)).resolves.toEqual(new Uint8Array([0, 1, 2, 255]));
    expect(mocks.readRange).toHaveBeenCalledWith("C:\\pics\\a.bin", 2, 4);
  });
});
