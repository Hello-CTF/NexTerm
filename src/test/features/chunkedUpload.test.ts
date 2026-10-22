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
  fileChunkSource,
  localPathChunkSource,
  UPLOAD_CHUNK_SIZE,
  uploadChunked,
  type ChunkReadSource,
} from "../../features/files/chunkedUpload";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function args(source: ChunkReadSource) {
  return {
    sessionId: "s1",
    remotePath: "/remote/file.bin",
    source,
    taskId: "task-1",
    attemptId: "attempt-1",
  };
}

beforeEach(() => {
  vi.resetAllMocks();
  mocks.begin.mockResolvedValue({ uploadId: "upload-1", chunkSize: UPLOAD_CHUNK_SIZE });
  mocks.chunk.mockImplementation(async ({ index, size }: { index: number; size: number }) => ({
    received: index * UPLOAD_CHUNK_SIZE + size,
    total: UPLOAD_CHUNK_SIZE * 2,
  }));
  mocks.commit.mockResolvedValue(0);
  mocks.abort.mockResolvedValue(undefined);
});

describe("uploadChunked", () => {
  it("reads File slices at chunk boundaries and commits after all chunks", async () => {
    const file = new File([new Uint8Array(UPLOAD_CHUNK_SIZE + 3).fill(7)], "big.bin");
    const slice = vi.spyOn(file, "slice");
    const source = fileChunkSource(file);
    mocks.chunk.mockImplementation(async ({ index, size }: { index: number; size: number }) => ({
      received: index * UPLOAD_CHUNK_SIZE + size,
      total: source.size,
    }));
    const progress: [number, number][] = [];

    await uploadChunked({ ...args(source), onProgress: (received, total) => progress.push([received, total]) });

    expect(slice).toHaveBeenCalledTimes(2);
    expect(slice).toHaveBeenNthCalledWith(1, 0, UPLOAD_CHUNK_SIZE);
    expect(slice).toHaveBeenNthCalledWith(2, UPLOAD_CHUNK_SIZE, source.size);
    expect(mocks.chunk.mock.calls.map(([input]) => [input.index, input.size])).toEqual([
      [0, UPLOAD_CHUNK_SIZE],
      [1, 3],
    ]);
    expect(new Set(mocks.chunk.mock.calls.map(([input]) => input.sha256)).size).toBe(2);
    expect(mocks.commit).toHaveBeenCalledOnce();
    expect(mocks.abort).not.toHaveBeenCalled();
    expect(progress).toEqual([
      [0, source.size],
      [UPLOAD_CHUNK_SIZE, source.size],
      [source.size, source.size],
      [source.size, source.size],
    ]);
  });

  it("does not read the next chunk while the current send is unacknowledged", async () => {
    const pending = deferred<{ received: number; total: number }>();
    mocks.chunk.mockReturnValueOnce(pending.promise);
    let reads = 0;
    const source: ChunkReadSource = {
      size: UPLOAD_CHUNK_SIZE + 1,
      async read(_offset, length) {
        reads += 1;
        return new Uint8Array(length).fill(1);
      },
    };
    mocks.chunk
      .mockImplementationOnce(() => pending.promise)
      .mockResolvedValueOnce({ received: source.size, total: source.size });

    const upload = uploadChunked(args(source));
    await vi.waitFor(() => expect(mocks.chunk).toHaveBeenCalledOnce());
    expect(reads).toBe(1);
    expect(mocks.commit).not.toHaveBeenCalled();

    pending.resolve({ received: UPLOAD_CHUNK_SIZE, total: source.size });
    await upload;
    expect(reads).toBe(2);
    expect(mocks.commit).toHaveBeenCalledOnce();
  });

  it("resends the same acknowledged-size chunk once without rereading", async () => {
    const source: ChunkReadSource = {
      size: 3,
      read: vi.fn(async () => new Uint8Array([1, 2, 3])),
    };
    mocks.chunk
      .mockRejectedValueOnce({ code: "disconnected", message: "lost response" })
      .mockResolvedValueOnce({ received: 3, total: 3 });

    await uploadChunked(args(source));

    expect(source.read).toHaveBeenCalledOnce();
    expect(mocks.chunk).toHaveBeenCalledTimes(2);
    const first = mocks.chunk.mock.calls[0][0];
    const second = mocks.chunk.mock.calls[1][0];
    expect(second).toEqual(first);
    expect(mocks.abort).not.toHaveBeenCalled();
  });

  it("aborts without retrying cancellation", async () => {
    mocks.chunk.mockRejectedValue({ code: "cancelled", message: "cancelled" });
    const source: ChunkReadSource = { size: 1, read: async () => new Uint8Array([1]) };

    await expect(uploadChunked(args(source))).rejects.toMatchObject({ code: "cancelled" });

    expect(mocks.chunk).toHaveBeenCalledOnce();
    expect(mocks.commit).not.toHaveBeenCalled();
    expect(mocks.abort).toHaveBeenCalledWith({
      sessionId: "s1",
      uploadId: "upload-1",
      taskId: "task-1",
      attemptId: "attempt-1",
    });
  });

  it("aborts after the same chunk fails twice", async () => {
    mocks.chunk.mockRejectedValue({ code: "disconnected", message: "offline" });
    const source: ChunkReadSource = { size: 1, read: async () => new Uint8Array([1]) };

    await expect(uploadChunked(args(source))).rejects.toMatchObject({ code: "disconnected" });

    expect(mocks.chunk).toHaveBeenCalledTimes(2);
    expect(mocks.commit).not.toHaveBeenCalled();
    expect(mocks.abort).toHaveBeenCalledOnce();
  });

  it("retries abort and reports persistent cleanup failure without replacing the transfer error", async () => {
    mocks.chunk.mockRejectedValue({ code: "bad_param", message: "invalid chunk" });
    mocks.abort.mockRejectedValue({ code: "io", message: "cleanup failed" });
    const warning = vi.spyOn(console, "warn").mockImplementation(() => {});
    const source: ChunkReadSource = { size: 1, read: async () => new Uint8Array([1]) };

    await expect(uploadChunked(args(source))).rejects.toMatchObject({ code: "bad_param" });

    expect(mocks.abort).toHaveBeenCalledTimes(2);
    expect(warning).toHaveBeenCalledWith(expect.stringContaining("清理失败"), expect.objectContaining({ code: "io" }));
    warning.mockRestore();
  });

  it("commits an empty source without reading or sending a chunk", async () => {
    const source: ChunkReadSource = { size: 0, read: vi.fn() };

    await uploadChunked(args(source));

    expect(source.read).not.toHaveBeenCalled();
    expect(mocks.chunk).not.toHaveBeenCalled();
    expect(mocks.commit).toHaveBeenCalledOnce();
  });
});

describe("localPathChunkSource", () => {
  it("uses bounded range reads and rejects source size changes", async () => {
    mocks.readRange.mockResolvedValue({
      path: "/local/a.bin",
      offset: 4,
      size: 6,
      contentBase64: btoa("ab"),
      truncated: false,
    });
    const source = localPathChunkSource("/local/a.bin", 6, "a.bin");
    await expect(source.read(4, 2)).resolves.toEqual(new Uint8Array([97, 98]));
    expect(mocks.readRange).toHaveBeenCalledWith("/local/a.bin", 4, 2);

    mocks.readRange.mockResolvedValue({
      path: "/local/a.bin",
      offset: 4,
      size: 7,
      contentBase64: btoa("ab"),
      truncated: true,
    });
    await expect(source.read(4, 2)).rejects.toThrow("大小");
  });
});
