import { beforeEach, describe, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({
  web: true,
  kind: "device",
  calls: [] as string[],
  tarSize: 0,
  chunkSize: 0,
  stagedBytes: null as Uint8Array | null,
  receivedChunks: new Set<number>(),
  failBegin: false,
  failChunk: false,
  failExtract: false,
  listEntries: [] as { name: string; kind: string }[],
  localReads: {} as Record<string, string>,
}));

vi.mock("../../ipc/env", () => ({ get WEB() { return h.web; } }));

vi.mock("../../app/store", () => ({
  useUi: { getState: () => ({ sessions: [{ id: "s1", kind: h.kind }] }) },
}));

vi.mock("../../ipc/commands", () => ({
  fsApi: {
    list: async () => {
      h.calls.push("list");
      return h.listEntries;
    },
    uploadBegin: async (args: { remotePath: string; totalSize: number; chunkSize: number }) => {
      h.calls.push(`begin:${args.remotePath}:${args.totalSize}`);
      h.tarSize = args.totalSize;
      h.chunkSize = args.chunkSize;
      if (h.failBegin) throw new Error("begin boom");
      h.stagedBytes = new Uint8Array(args.totalSize);
      return { uploadId: "upload-1", chunkSize: args.chunkSize };
    },
    uploadChunk: async (args: { index: number; size: number; contentBase64: string }) => {
      h.calls.push(`chunk:${args.index}:${args.size}`);
      if (h.failChunk) throw { code: "bad_param", message: "chunk boom" };
      const data = Uint8Array.from(atob(args.contentBase64), (char) => char.charCodeAt(0));
      h.stagedBytes!.set(data, args.index * h.chunkSize);
      h.receivedChunks.add(args.index);
      let received = 0;
      for (const index of h.receivedChunks) {
        received += Math.min(h.chunkSize, h.tarSize - index * h.chunkSize);
      }
      return { received, total: h.tarSize };
    },
    uploadCommit: async () => {
      h.calls.push("commit");
      return h.tarSize;
    },
    uploadAbort: async () => {
      h.calls.push("abort");
    },
    extract: async (_sid: string, path: string) => {
      h.calls.push(`extract:${path}`);
      if (h.failExtract) throw new Error("remote boom");
      return path.replace(/\.tar$/, "");
    },
    delete: async (_sid: string, path: string, isDir: boolean) => {
      h.calls.push(`delete:${path}:${isDir}`);
    },
  },
  localFileApi: {
    readRange: async (path: string, offset: number, length: number) => {
      h.calls.push(`read:${path}:${offset}:${length}`);
      const content = h.localReads[path];
      if (content === undefined) throw new Error(`no read sample for ${path}`);
      const decoded = atob(content);
      const selected = decoded.slice(offset, offset + length);
      return {
        path,
        offset,
        size: decoded.length,
        contentBase64: btoa(selected),
        truncated: offset + selected.length < decoded.length,
      };
    },
  },
}));

import {
  buildFolderTar,
  MAX_PACK_BYTES,
  packExtractSupported,
  packUploadAndExtract,
  type FolderFileEntry,
} from "../../features/files/folderPackUpload";

const MTIME_MS = 1_700_000_000_000;
const MTIME_S = 1_700_000_000;

function entry(
  rel: string,
  content: string | Uint8Array<ArrayBuffer>,
  lastModified = MTIME_MS,
): FolderFileEntry {
  const data = typeof content === "string" ? new TextEncoder().encode(content) : content;
  return {
    file: new File([data], rel.split("/").pop() ?? rel, { lastModified }),
    relativePath: rel,
    localPath: "",
    size: data.length,
    mtimeMs: lastModified,
  };
}

function ascii(block: Uint8Array, offset: number, length: number): string {
  let out = "";
  for (let i = 0; i < length; i++) {
    const b = block[offset + i];
    if (b === 0) break;
    out += String.fromCharCode(b);
  }
  return out;
}

function checksumOf(block: Uint8Array, offset: number): number {
  let sum = 0;
  for (let i = 0; i < 512; i++) sum += i >= 148 && i < 156 ? 0x20 : block[offset + i];
  return sum;
}

beforeEach(() => {
  h.web = true;
  h.kind = "device";
  h.calls.length = 0;
  h.tarSize = 0;
  h.chunkSize = 0;
  h.stagedBytes = null;
  h.receivedChunks.clear();
  h.failBegin = false;
  h.failChunk = false;
  h.failExtract = false;
  h.listEntries = [];
  h.localReads = {};
});

describe("buildFolderTar (ustar byte layout)", () => {
  it("single file header fields, content, and trailing zero blocks", async () => {
    const tar = await buildFolderTar([entry("hello.txt", "hello")]);
    expect(tar.length).toBe(512 + 512 + 1024);
    expect(ascii(tar, 0, 100)).toBe("hello.txt");
    expect(ascii(tar, 100, 8)).toBe("0000644");
    expect(ascii(tar, 108, 8)).toBe("0000000");
    expect(ascii(tar, 116, 8)).toBe("0000000");
    expect(ascii(tar, 124, 12)).toBe("00000000005");
    expect(ascii(tar, 136, 12)).toBe(MTIME_S.toString(8).padStart(11, "0"));
    expect(tar[156]).toBe(0x30);
    expect(ascii(tar, 257, 6)).toBe("ustar");
    expect(tar[262]).toBe(0);
    expect(ascii(tar, 263, 2)).toBe("00");
    expect(ascii(tar, 148, 6)).toBe(checksumOf(tar, 0).toString(8).padStart(6, "0"));
    expect(tar[154]).toBe(0);
    expect(tar[155]).toBe(0x20);
    expect(ascii(tar, 512, 5)).toBe("hello");
    expect(tar[517]).toBe(0);
    expect(tar.slice(1024).every((b) => b === 0)).toBe(true);
  });

  it("content padded to 512-byte blocks", async () => {
    const tar = await buildFolderTar([entry("a.bin", new Uint8Array(513).fill(7))]);
    expect(tar.length).toBe(512 + 1024 + 1024);
    expect(tar[512]).toBe(7);
    expect(tar[512 + 512]).toBe(7);
    expect(tar[512 + 513]).toBe(0);
    const empty = await buildFolderTar([entry("e.bin", new Uint8Array(0))]);
    expect(empty.length).toBe(512 + 0 + 1024);
    expect(ascii(empty, 124, 12)).toBe("00000000000");
  });

  it("paths longer than 100 bytes split into the prefix field", async () => {
    const a = "a".repeat(60);
    const b = "b".repeat(60);
    const tar = await buildFolderTar([entry(`${a}/${b}`, "x")]);
    expect(ascii(tar, 0, 100)).toBe(`${a}/`);
    expect(tar[156]).toBe(0x35);
    expect(ascii(tar, 512, 100)).toBe(b);
    expect(ascii(tar, 512 + 345, 155)).toBe(a);
    expect(ascii(tar, 148, 6)).toBe(checksumOf(tar, 0).toString(8).padStart(6, "0"));
    expect(ascii(tar, 512 + 148, 6)).toBe(checksumOf(tar, 512).toString(8).padStart(6, "0"));
  });

  it("unsplittable long paths throw", async () => {
    await expect(buildFolderTar([entry("x".repeat(120), "x")])).rejects.toThrow("路径过长");
  });

  it("directory entries come from deduped path prefixes and sort before files", async () => {
    const tar = await buildFolderTar([
      entry("sub/one.txt", "1"),
      entry("sub/two.txt", "2"),
      entry("sub/deep/three.txt", "3"),
    ]);
    expect(tar.length).toBe(2 * 512 + 3 * 1024 + 1024);
    expect(ascii(tar, 0, 100)).toBe("sub/");
    expect(tar[156]).toBe(0x35);
    expect(ascii(tar, 512, 100)).toBe("sub/deep/");
    expect(tar[512 + 156]).toBe(0x35);
    expect(ascii(tar, 124, 12)).toBe("00000000000");
    expect(ascii(tar, 136, 12)).toBe(MTIME_S.toString(8).padStart(11, "0"));
    expect(ascii(tar, 1024, 100)).toBe("sub/one.txt");
    expect(ascii(tar, 2048, 100)).toBe("sub/two.txt");
    expect(ascii(tar, 3072, 100)).toBe("sub/deep/three.txt");
  });

  it("rootPrefix controls whether entries carry the root directory prefix", async () => {
    const files = () => [entry("sub/one.txt", "1"), entry("two.txt", "2")];
    const prefixed = await buildFolderTar(files(), "photos");
    expect(ascii(prefixed, 0, 100)).toBe("photos/");
    expect(ascii(prefixed, 512, 100)).toBe("photos/sub/");
    expect(ascii(prefixed, 1024, 100)).toBe("photos/sub/one.txt");
    expect(ascii(prefixed, 2048, 100)).toBe("photos/two.txt");
    const rootless = await buildFolderTar(files());
    expect(ascii(rootless, 0, 100)).toBe("sub/");
    expect(ascii(rootless, 512, 100)).toBe("sub/one.txt");
    expect(ascii(rootless, 1536, 100)).toBe("two.txt");
  });

  it("directory headers use mode 0755 and file headers 0644", async () => {
    const tar = await buildFolderTar([entry("sub/one.txt", "1")]);
    expect(tar[156]).toBe(0x35);
    expect(ascii(tar, 100, 8)).toBe("0000755");
    expect(tar[512 + 156]).toBe(0x30);
    expect(ascii(tar, 512 + 100, 8)).toBe("0000644");
  });

  it("empty dirs are written as directory entries and deduped with path prefixes", async () => {
    const tar = await buildFolderTar([entry("sub/one.txt", "1")], "", [
      "empty",
      "sub/deep",
      "sub",
    ]);
    expect(tar.length).toBe(3 * 512 + 1024 + 1024);
    expect(ascii(tar, 0, 100)).toBe("empty/");
    expect(tar[156]).toBe(0x35);
    expect(ascii(tar, 100, 8)).toBe("0000755");
    expect(ascii(tar, 512, 100)).toBe("sub/");
    expect(ascii(tar, 1024, 100)).toBe("sub/deep/");
    expect(ascii(tar, 1536, 100)).toBe("sub/one.txt");
    expect(tar[1536 + 156]).toBe(0x30);
  });
});

describe("packExtractSupported", () => {
  it("only ssh and device support pack extraction", () => {
    expect(packExtractSupported("ssh")).toBe(true);
    expect(packExtractSupported("device")).toBe(true);
    expect(packExtractSupported("winrm")).toBe(false);
    expect(packExtractSupported("local")).toBe(false);
    expect(packExtractSupported("remote")).toBe(false);
    expect(packExtractSupported("docker")).toBe(false);
  });
});

describe("packUploadAndExtract (device flow, in-place extraction)", () => {
  const files = () => [entry("a.txt", "abc")];
  const tarSize = 512 + 512 + 512 + 1024;

  it("streams pack chunks -> commit -> extract -> delete in order and reports progress", async () => {
    const progress: [number, number][] = [];
    const result = await packUploadAndExtract({
      sessionId: "s1",
      dir: "/data",
      folderName: "photos",
      files: files(),
      onProgress: (t, total) => progress.push([t, total]),
    });
    expect(result).toBe("/data/photos");
    expect(h.calls[0]).toMatch(/^begin:\/data\/\.nexterm-upload-[0-9a-f]{12}\.tar:2560$/);
    const temp = h.calls[0].slice("begin:".length, -`:${tarSize}`.length);
    expect(h.calls.slice(1)).toEqual([
      "chunk:0:2560",
      "commit",
      `extract:${temp}`,
      `delete:${temp}:false`,
    ]);
    expect(progress).toEqual([
      [0, tarSize],
      [tarSize, tarSize],
      [tarSize, tarSize],
    ]);
    // In-place extraction semantics: entries retain the photos/ root prefix
    expect(h.stagedBytes).not.toBeNull();
    expect(ascii(h.stagedBytes!, 0, 100)).toBe("photos/");
    expect(h.stagedBytes![156]).toBe(0x35);
    expect(ascii(h.stagedBytes!, 512, 100)).toBe("photos/a.txt");
  });

  it("extract failure still deletes the temp package and rethrows the remote error text", async () => {
    h.failExtract = true;
    await expect(
      packUploadAndExtract({
        sessionId: "s1",
        dir: "/data",
        folderName: "photos",
        files: files(),
        onProgress: () => {},
      }),
    ).rejects.toThrow("remote boom");
    const extractIdx = h.calls.findIndex((c) => c.startsWith("extract:"));
    const deleteIdx = h.calls.findIndex((c) => c.startsWith("delete:"));
    expect(extractIdx).toBeGreaterThan(-1);
    expect(deleteIdx).toBeGreaterThan(extractIdx);
    expect(h.calls[deleteIdx].endsWith(":false")).toBe(true);
  });

  it("aborts the chunked session when a pack chunk fails", async () => {
    h.failChunk = true;
    await expect(
      packUploadAndExtract({
        sessionId: "s1",
        dir: "/data",
        folderName: "photos",
        files: files(),
        onProgress: () => {},
      }),
    ).rejects.toThrow("chunk boom");
    expect(h.calls).toContain("abort");
    expect(h.calls).not.toContain("commit");
    expect(h.calls.some((call) => call.startsWith("extract:"))).toBe(false);
  });

  it("empty dirs upload with the package: the tar contains root-prefixed empty dir entries", async () => {
    await packUploadAndExtract({
      sessionId: "s1",
      dir: "/data",
      folderName: "photos",
      files: files(),
      emptyDirs: ["empty"],
      onProgress: () => {},
    });
    expect(h.stagedBytes).not.toBeNull();
    expect(ascii(h.stagedBytes!, 0, 100)).toBe("photos/");
    expect(ascii(h.stagedBytes!, 512, 100)).toBe("photos/empty/");
    expect(h.stagedBytes![512 + 156]).toBe(0x35);
    expect(ascii(h.stagedBytes!, 1024, 100)).toBe("photos/a.txt");
    expect(h.stagedBytes![1024 + 156]).toBe(0x30);
  });
});

describe("packUploadAndExtract (ssh flow, derived directory extraction)", () => {
  const files = () => [entry("a.txt", "abc")];
  const tarSize = 512 + 512 + 1024;

  it("temp package named after the folder, entries carry no root prefix", async () => {
    h.kind = "ssh";
    const result = await packUploadAndExtract({
      sessionId: "s1",
      dir: "/data",
      folderName: "photos",
      files: files(),
      onProgress: () => {},
    });
    expect(result).toBe("/data/photos");
    expect(h.calls).toEqual([
      "list",
      `begin:/data/photos.tar:${tarSize}`,
      `chunk:0:${tarSize}`,
      "commit",
      "extract:/data/photos.tar",
      "delete:/data/photos.tar:false",
    ]);
    expect(h.stagedBytes).not.toBeNull();
    expect(ascii(h.stagedBytes!, 0, 100)).toBe("a.txt");
    expect(h.stagedBytes![156]).toBe(0x30);
  });

  it("rejects when a same-name file or symlink exists in the target dir and performs no writes", async () => {
    h.kind = "ssh";
    for (const kind of ["file", "symlink"]) {
      h.calls.length = 0;
      h.listEntries = [{ name: "photos.tar", kind }];
      await expect(
        packUploadAndExtract({
          sessionId: "s1",
          dir: "/data",
          folderName: "photos",
          files: files(),
          onProgress: () => {},
        }),
      ).rejects.toThrow("photos.tar");
      expect(h.calls).toEqual(["list"]);
    }
  });

  it("a same-name directory is not treated as a conflict", async () => {
    h.kind = "ssh";
    h.listEntries = [{ name: "photos.tar", kind: "dir" }];
    await packUploadAndExtract({
      sessionId: "s1",
      dir: "/data",
      folderName: "photos",
      files: files(),
      onProgress: () => {},
    });
    expect(h.calls[1]).toBe(`begin:/data/photos.tar:${tarSize}`);
  });
});

describe("packUploadAndExtract (input validation)", () => {
  const files = () => [entry("a.txt", "abc")];

  it("empty folder, invalid name, and unsupported session kind", async () => {
    await expect(
      packUploadAndExtract({
        sessionId: "s1",
        dir: "/data",
        folderName: "photos",
        files: [],
        onProgress: () => {},
      }),
    ).rejects.toThrow("没有可上传的文件");
    await expect(
      packUploadAndExtract({
        sessionId: "s1",
        dir: "/data",
        folderName: "a/b",
        files: files(),
        onProgress: () => {},
      }),
    ).rejects.toThrow("无效");
    h.kind = "winrm";
    await expect(
      packUploadAndExtract({
        sessionId: "s1",
        dir: "/data",
        folderName: "photos",
        files: files(),
        onProgress: () => {},
      }),
    ).rejects.toThrow("不支持打包上传");
    expect(h.calls).toEqual([]);
  });

  it("ssh sessions over the 256 MB total limit fail early and suggest per-file upload", async () => {
    h.kind = "ssh";
    const big = new File([new Uint8Array(0)], "big.bin");
    Object.defineProperty(big, "size", { value: MAX_PACK_BYTES + 1 });
    await expect(
      packUploadAndExtract({
        sessionId: "s1",
        dir: "/data",
        folderName: "photos",
        files: [{ file: big, relativePath: "big.bin", localPath: "", size: big.size, mtimeMs: MTIME_MS }],
        onProgress: () => {},
      }),
    ).rejects.toThrow("逐文件");
    expect(h.calls).toEqual([]);
  });

  it("device packs above the former 64 MB single-write limit enter chunked begin", async () => {
    h.kind = "device";
    h.web = false;
    h.failBegin = true;
    const big = new File([new Uint8Array(0)], "big.bin");
    Object.defineProperty(big, "size", { value: 65 * 1024 * 1024 });
    await expect(
      packUploadAndExtract({
        sessionId: "s1",
        dir: "/data",
        folderName: "photos",
        files: [{ file: big, relativePath: "big.bin", localPath: "", size: big.size, mtimeMs: MTIME_MS }],
        onProgress: () => {},
      }),
    ).rejects.toThrow("begin boom");
    expect(h.calls[0]).toMatch(/^begin:\/data\/\.nexterm-upload-[0-9a-f]{12}\.tar:/);
    expect(h.calls[0].endsWith(`:${big.size + 2048}`)).toBe(true);
    expect(h.calls.some((call) => call.includes("单次写入上限"))).toBe(false);
  });

  it("100 MB on ssh sessions remains below the pack limit and enters chunked begin", async () => {
    h.kind = "ssh";
    h.failBegin = true;
    const big = new File([new Uint8Array(0)], "big.bin");
    Object.defineProperty(big, "size", { value: 100 * 1024 * 1024 });
    await expect(
      packUploadAndExtract({
        sessionId: "s1",
        dir: "/data",
        folderName: "photos",
        files: [{ file: big, relativePath: "big.bin", localPath: "", size: big.size, mtimeMs: MTIME_MS }],
        onProgress: () => {},
      }),
    ).rejects.toThrow("begin boom");
    expect(h.calls[0]).toBe("list");
    expect(h.calls[1]).toBe(`begin:/data/photos.tar:${big.size + 1536}`);
  });
});

describe("packUploadAndExtract (desktop flow)", () => {
  it("streams the tar through chunk commit without staging", async () => {
    h.web = false;
    const progress: [number, number][] = [];
    const result = await packUploadAndExtract({
      sessionId: "s1",
      dir: "/data",
      folderName: "photos",
      files: [entry("a.txt", "abc")],
      onProgress: (t, total) => progress.push([t, total]),
    });
    expect(result).toBe("/data/photos");
    const expected = await buildFolderTar([entry("a.txt", "abc")], "photos");
    expect(h.stagedBytes).toEqual(expected);
    expect(h.calls[0]).toMatch(/^begin:\/data\/\.nexterm-upload-[0-9a-f]{12}\.tar:/);
    const temp = h.calls[0].slice("begin:".length, -`:${expected.length}`.length);
    expect(h.calls.slice(1)).toEqual([
      `chunk:0:${expected.length}`,
      "commit",
      `extract:${temp}`,
      `delete:${temp}:false`,
    ]);
    expect(progress).toEqual([
      [0, expected.length],
      [expected.length, expected.length],
      [expected.length, expected.length],
    ]);
  });
});

describe("buildFolderTar (desktop native picker entries without File)", () => {
  function nativeEntry(rel: string, localPath: string, size: number): FolderFileEntry {
    return { relativePath: rel, localPath, size, mtimeMs: MTIME_MS };
  }

  it("packs bytes read via localFileApi.readRange with correct header mtime and size", async () => {
    h.localReads = { "C:\\pics\\hello.txt": btoa("hello") };
    const tar = await buildFolderTar([nativeEntry("hello.txt", "C:\\pics\\hello.txt", 5)]);
    expect(h.calls).toEqual(["read:C:\\pics\\hello.txt:0:5"]);
    expect(tar.length).toBe(512 + 512 + 1024);
    expect(ascii(tar, 0, 100)).toBe("hello.txt");
    expect(ascii(tar, 124, 12)).toBe("00000000005");
    expect(ascii(tar, 136, 12)).toBe(MTIME_S.toString(8).padStart(11, "0"));
    expect(ascii(tar, 512, 5)).toBe("hello");
  });

  it("errors when the read size differs from the enumerated size, producing no partial tar", async () => {
    h.localReads = { "/local/pics/a.txt": btoa("abcd") };
    await expect(buildFolderTar([nativeEntry("a.txt", "/local/pics/a.txt", 3)])).rejects.toThrow(
      "大小",
    );
  });

  it("desktop pack upload without File entries streams native range reads", async () => {
    h.web = false;
    h.kind = "ssh";
    h.localReads = { "/local/pics/a.txt": btoa("abc") };
    const result = await packUploadAndExtract({
      sessionId: "s1",
      dir: "/data",
      folderName: "pics",
      files: [nativeEntry("a.txt", "/local/pics/a.txt", 3)],
      onProgress: () => {},
    });
    expect(result).toBe("/data/pics");
    expect(h.calls).toContain("read:/local/pics/a.txt:0:3");
    expect(h.stagedBytes).not.toBeNull();
    expect(ascii(h.stagedBytes!, 0, 100)).toBe("a.txt");
    expect(ascii(h.stagedBytes!, 512, 3)).toBe("abc");
    expect(h.calls).toContain("commit");
    expect(h.calls.some((c) => c.startsWith("stage:") || c.startsWith("write:"))).toBe(false);
  });
});
