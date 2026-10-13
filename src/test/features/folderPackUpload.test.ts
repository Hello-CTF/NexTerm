import { beforeEach, describe, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({
  web: true,
  kind: "device",
  calls: [] as string[],
  tarSize: 0,
  stagedBytes: null as Uint8Array | null,
  failExtract: false,
  listEntries: [] as { name: string; kind: string }[],
  progressHandler: null as
    | null
    | ((e: { taskId: string; transferred: number; total: number; done: boolean }) => void),
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
    upload: async (_sid: string, local: string, remote: string) => {
      h.calls.push(`upload:${local}->${remote}`);
      h.progressHandler?.({ taskId: "t1", transferred: 64, total: h.tarSize, done: false });
      h.progressHandler?.({ taskId: "t1", transferred: h.tarSize, total: h.tarSize, done: true });
      return h.tarSize;
    },
    write: async (_sid: string, path: string, contentBase64: string) => {
      h.calls.push(`write:${path}:${contentBase64}`);
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
}));

vi.mock("../../ipc/webFiles", () => ({
  stageFile: async (file: File) => {
    h.tarSize = file.size;
    h.stagedBytes = new Uint8Array(await file.arrayBuffer());
    h.calls.push(`stage:${file.name}:${file.size}`);
    return { id: "blob-1", path: `blob-1/${file.name}` };
  },
  dropStaged: async (path: string) => {
    h.calls.push(`drop:${path}`);
  },
}));

vi.mock("../../ipc/events", () => ({
  EVENTS: { fsProgress: "fs://progress" },
  listenEvent: async (
    _event: string,
    handler: (e: { taskId: string; transferred: number; total: number; done: boolean }) => void,
  ) => {
    h.progressHandler = handler;
    return () => {
      h.progressHandler = null;
    };
  },
}));

import {
  buildFolderTar,
  MAX_PACK_BYTES,
  MAX_PACK_BYTES_DEVICE,
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
  h.stagedBytes = null;
  h.failExtract = false;
  h.listEntries = [];
  h.progressHandler = null;
});

describe("buildFolderTar（ustar 字节布局）", () => {
  it("单文件头部字段、内容、结尾零块", async () => {
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

  it("内容按 512 整块补齐", async () => {
    const tar = await buildFolderTar([entry("a.bin", new Uint8Array(513).fill(7))]);
    expect(tar.length).toBe(512 + 1024 + 1024);
    expect(tar[512]).toBe(7);
    expect(tar[512 + 512]).toBe(7);
    expect(tar[512 + 513]).toBe(0);
    const empty = await buildFolderTar([entry("e.bin", new Uint8Array(0))]);
    expect(empty.length).toBe(512 + 0 + 1024);
    expect(ascii(empty, 124, 12)).toBe("00000000000");
  });

  it("超过 100 字节的路径拆到 prefix 字段", async () => {
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

  it("无法拆分的长路径直接报错", async () => {
    await expect(buildFolderTar([entry("x".repeat(120), "x")])).rejects.toThrow("路径过长");
  });

  it("目录项来自去重后的路径前缀且排在文件前", async () => {
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

  it("rootPrefix 决定条目是否携带根目录前缀", async () => {
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

  it("目录头部权限为 0755，文件头部为 0644", async () => {
    const tar = await buildFolderTar([entry("sub/one.txt", "1")]);
    expect(tar[156]).toBe(0x35);
    expect(ascii(tar, 100, 8)).toBe("0000755");
    expect(tar[512 + 156]).toBe(0x30);
    expect(ascii(tar, 512 + 100, 8)).toBe("0000644");
  });

  it("空目录写成目录项并与路径前缀去重", async () => {
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
  it("仅 ssh 与 device 支持打包解压", () => {
    expect(packExtractSupported("ssh")).toBe(true);
    expect(packExtractSupported("device")).toBe(true);
    expect(packExtractSupported("winrm")).toBe(false);
    expect(packExtractSupported("local")).toBe(false);
    expect(packExtractSupported("remote")).toBe(false);
    expect(packExtractSupported("docker")).toBe(false);
  });
});

describe("packUploadAndExtract（device 流程，就地解压）", () => {
  const files = () => [entry("a.txt", "abc")];
  const tarSize = 512 + 512 + 512 + 1024;

  it("按 打包 -> 暂存上传 -> 解压 -> 删除 顺序执行并回报进度", async () => {
    const progress: [number, number][] = [];
    const result = await packUploadAndExtract({
      sessionId: "s1",
      dir: "/data",
      folderName: "photos",
      files: files(),
      onProgress: (t, total) => progress.push([t, total]),
    });
    expect(result).toBe("/data/photos");
    expect(h.calls[0]).toMatch(/^stage:\.nexterm-upload-[0-9a-f]{12}\.tar:2560$/);
    expect(h.calls[1]).toMatch(
      /^upload:blob-1\/\.nexterm-upload-[0-9a-f]{12}\.tar->\/data\/\.nexterm-upload-[0-9a-f]{12}\.tar$/,
    );
    const temp = h.calls[1].split("->")[1];
    expect(h.calls.slice(2)).toEqual([
      `drop:blob-1/${temp.split("/").pop()}`,
      `extract:${temp}`,
      `delete:${temp}:false`,
    ]);
    expect(progress).toEqual([
      [64, tarSize],
      [tarSize, tarSize],
      [tarSize, tarSize],
    ]);
    // 就地解压语义：条目保留 photos/ 根前缀
    expect(h.stagedBytes).not.toBeNull();
    expect(ascii(h.stagedBytes!, 0, 100)).toBe("photos/");
    expect(h.stagedBytes![156]).toBe(0x35);
    expect(ascii(h.stagedBytes!, 512, 100)).toBe("photos/a.txt");
  });

  it("解压失败仍删除临时包并抛出远端错误文本", async () => {
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

  it("空目录随包上传：tar 内含带根前缀的空目录项", async () => {
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

describe("packUploadAndExtract（ssh 流程，派生目录解压）", () => {
  const files = () => [entry("a.txt", "abc")];
  const tarSize = 512 + 512 + 1024;

  it("临时包以文件夹命名，条目不带根前缀", async () => {
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
      `stage:photos.tar:${tarSize}`,
      "upload:blob-1/photos.tar->/data/photos.tar",
      "drop:blob-1/photos.tar",
      "extract:/data/photos.tar",
      "delete:/data/photos.tar:false",
    ]);
    expect(h.stagedBytes).not.toBeNull();
    expect(ascii(h.stagedBytes!, 0, 100)).toBe("a.txt");
    expect(h.stagedBytes![156]).toBe(0x30);
  });

  it("目标目录已存在同名文件或链接时拒绝，不做任何写操作", async () => {
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

  it("同名目录不视为冲突", async () => {
    h.kind = "ssh";
    h.listEntries = [{ name: "photos.tar", kind: "dir" }];
    await packUploadAndExtract({
      sessionId: "s1",
      dir: "/data",
      folderName: "photos",
      files: files(),
      onProgress: () => {},
    });
    expect(h.calls[1]).toBe(`stage:photos.tar:${tarSize}`);
  });
});

describe("packUploadAndExtract（入参校验）", () => {
  const files = () => [entry("a.txt", "abc")];

  it("空文件夹、非法名称、不支持的会话类型", async () => {
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

  it("ssh 会话总量超过 256 MB 提前失败并建议逐文件上传", async () => {
    h.kind = "ssh";
    const big = new File([new Uint8Array(0)], "big.bin");
    Object.defineProperty(big, "size", { value: MAX_PACK_BYTES + 1 });
    await expect(
      packUploadAndExtract({
        sessionId: "s1",
        dir: "/data",
        folderName: "photos",
        files: [{ file: big, relativePath: "big.bin", localPath: "", size: big.size }],
        onProgress: () => {},
      }),
    ).rejects.toThrow("逐文件");
    expect(h.calls).toEqual([]);
  });

  it("device 会话超过 64 MB 单次写入上限，在任何 IPC 前拒绝并建议逐文件上传", async () => {
    h.kind = "device";
    const attempt = () => {
      const big = new File([new Uint8Array(0)], "big.bin");
      Object.defineProperty(big, "size", { value: MAX_PACK_BYTES_DEVICE + 1 });
      return packUploadAndExtract({
        sessionId: "s1",
        dir: "/data",
        folderName: "photos",
        files: [{ file: big, relativePath: "big.bin", localPath: "", size: big.size }],
        onProgress: () => {},
      });
    };
    await expect(attempt()).rejects.toThrow("单次写入上限 64.0 MB");
    await expect(attempt()).rejects.toThrow("逐文件");
    expect(h.calls).toEqual([]);
  });

  it("ssh 会话 100 MB 不受 device 上限影响，正常进入打包流程", async () => {
    h.kind = "ssh";
    const big = new File([new Uint8Array(0)], "big.bin");
    Object.defineProperty(big, "size", { value: 100 * 1024 * 1024 });
    await packUploadAndExtract({
      sessionId: "s1",
      dir: "/data",
      folderName: "photos",
      files: [{ file: big, relativePath: "big.bin", localPath: "", size: big.size }],
      onProgress: () => {},
    });
    expect(h.calls[0]).toBe("list");
    expect(h.calls.some((c) => c.startsWith("stage:photos.tar:"))).toBe(true);
  });
});

describe("packUploadAndExtract（桌面端流程）", () => {
  it("走 fs_write 直写远端，不经过暂存", async () => {
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
    expect(h.calls.some((c) => c.startsWith("stage:"))).toBe(false);
    const write = h.calls.find((c) => c.startsWith("write:"));
    expect(write).toMatch(/^write:\/data\/\.nexterm-upload-[0-9a-f]{12}\.tar:/);
    const b64 = write!.slice(write!.indexOf(":", "write:".length) + 1);
    const expected = await buildFolderTar([entry("a.txt", "abc")], "photos");
    const decoded = Uint8Array.from(atob(b64), (ch) => ch.charCodeAt(0));
    expect(decoded).toEqual(expected);
    const temp = write!.slice("write:".length, write!.indexOf(":", "write:".length));
    expect(h.calls[h.calls.length - 2]).toBe(`extract:${temp}`);
    expect(h.calls[h.calls.length - 1]).toBe(`delete:${temp}:false`);
    expect(progress).toEqual([
      [0, expected.length],
      [expected.length, expected.length],
    ]);
  });
});
