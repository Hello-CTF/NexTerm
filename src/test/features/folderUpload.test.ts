/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({
  web: false,
  kind: "ssh",
  pickedFolder: "/local/pics" as string | null,
  pickThrows: false,
  listing: { name: "pics", files: [], emptyDirs: [] } as {
    name: string;
    files: { relativePath: string; localPath: string; size: number; mtimeMs: number }[];
    emptyDirs: string[];
  },
  listError: null as { code: string; message: string } | null,
  choice: "perFile" as string | null,
  siblings: [] as { name: string; kind: string }[],
  localReads: {} as Record<string, string>,
  calls: [] as string[],
  toasts: [] as { level: string; text: string }[],
}));

vi.mock("../../ipc/env", () => ({ get WEB() { return h.web; } }));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return {
    ...actual,
    askChoice: async () => h.choice,
    pickLocalFolder: async () => {
      if (h.pickThrows) throw new Error("dialogs unavailable");
      return h.pickedFolder;
    },
  };
});

vi.mock("../../app/store", () => ({
  useUi: {
    getState: () => ({
      pushToast: (level: string, text: string) => h.toasts.push({ level, text }),
      sessions: [{ id: "s1", kind: h.kind }],
    }),
  },
}));

vi.mock("../../ipc/commands", () => ({
  fsApi: {
    list: async (_sid: string, path: string) => {
      h.calls.push(`list:${path}`);
      return h.siblings;
    },
    write: async (_sid: string, path: string, contentBase64: string) => {
      h.calls.push(`write:${path}:${contentBase64}`);
    },
    upload: async (_sid: string, localPath: string, remotePath: string) => {
      h.calls.push(`upload:${localPath}->${remotePath}`);
      return 1;
    },
    mkdir: async (_sid: string, path: string) => {
      h.calls.push(`mkdir:${path}`);
    },
    extract: async (_sid: string, path: string) => {
      h.calls.push(`extract:${path}`);
      return path.replace(/\.tar$/, "");
    },
    delete: async (_sid: string, path: string, isDir: boolean) => {
      h.calls.push(`delete:${path}:${isDir}`);
    },
  },
  localFileApi: {
    listFolder: async (path: string) => {
      h.calls.push(`listFolder:${path}`);
      if (h.listError) throw h.listError;
      return h.listing;
    },
    read: async (path: string) => {
      h.calls.push(`read:${path}`);
      const content = h.localReads[path];
      if (content === undefined) throw new Error(`无读取样本：${path}`);
      return { path, size: atob(content).length, contentBase64: content };
    },
  },
}));

vi.mock("../../ipc/webFiles", () => ({
  stageFile: async (file: File) => {
    h.calls.push(`stage:${file.name}`);
    return { id: "b1", path: `b1/${file.name}` };
  },
  dropStaged: async (path: string) => {
    h.calls.push(`drop:${path}`);
  },
}));

vi.mock("../../ipc/events", () => ({
  EVENTS: { fsProgress: "fs://progress" },
  listenEvent: async () => () => {},
}));

import { dropFolderUpload, pickFolderUpload } from "../../features/files/folderUpload";
import { useTransferStore } from "../../features/files/transferStore";
import type { FileEntryDto } from "../../ipc/types";

const MTIME_MS = 1_700_000_000_000;

function nativeFile(rel: string, localPath: string, size: number) {
  return { relativePath: rel, localPath, size, mtimeMs: MTIME_MS };
}

function fakeFile(rel: string, content: string): File {
  const file = new File([content], rel.split("/").pop() ?? rel, { lastModified: MTIME_MS });
  Object.defineProperty(file, "webkitRelativePath", { value: rel });
  return file;
}

function dirEntry(name: string, children: unknown[]) {
  return {
    isFile: false,
    isDirectory: true,
    name,
    createReader: () => ({
      readEntries: (ok: (entries: unknown[]) => void) => {
        ok(children.splice(0, children.length));
      },
    }),
  };
}

function fileEntry(name: string, file: File) {
  return {
    isFile: true,
    isDirectory: false,
    name,
    file: (ok: (f: File) => void) => ok(file),
  };
}

function dropOf(items: unknown[], files: File[]): DataTransfer {
  return { items, files } as unknown as DataTransfer;
}

async function waitForSettled(): Promise<void> {
  for (let i = 0; i < 400; i++) {
    const { tasks } = useTransferStore.getState();
    if (tasks.length > 0 && tasks.every((t) => t.status !== "queued" && t.status !== "running")) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 2));
  }
  throw new Error("传输任务未结束");
}

function b64(text: string): string {
  return btoa(text);
}

function ascii(bytes: string, offset: number, length: number): string {
  let out = "";
  for (let i = 0; i < length; i++) {
    const code = bytes.charCodeAt(offset + i);
    if (code === 0) break;
    out += String.fromCharCode(code);
  }
  return out;
}

beforeEach(() => {
  h.web = false;
  h.kind = "ssh";
  h.pickedFolder = "/local/pics";
  h.pickThrows = false;
  h.listing = { name: "pics", files: [], emptyDirs: [] };
  h.listError = null;
  h.choice = "perFile";
  h.siblings = [];
  h.localReads = {};
  h.calls = [];
  h.toasts = [];
  useTransferStore.setState({ tasks: [], runningId: null });
});

describe("桌面原生文件夹选择器", () => {
  it("逐文件模式拿到真实 localPath，远端路径用正斜杠拼接", async () => {
    h.listing = {
      name: "pics",
      files: [
        nativeFile("a.txt", "C:\\pics\\a.txt", 1),
        nativeFile("sub/b.txt", "C:\\pics\\sub\\b.txt", 2),
      ],
      emptyDirs: ["empty"],
    };
    await pickFolderUpload({ sessionId: "s1", dir: "/data", knownSiblings: [] });
    await waitForSettled();
    expect(h.calls).toContain("listFolder:/local/pics");
    expect(h.calls).toContain("upload:C:\\pics\\a.txt->/data/pics/a.txt");
    expect(h.calls).toContain("upload:C:\\pics\\sub\\b.txt->/data/pics/sub/b.txt");
    expect(h.calls).toContain("mkdir:/data/pics/empty");
    expect(h.toasts.some((t) => t.level === "error")).toBe(false);
  });

  it("原生对话框取消时不做任何事", async () => {
    h.pickedFolder = null;
    await pickFolderUpload({ sessionId: "s1", dir: "/data", knownSiblings: [] });
    expect(h.calls).toEqual([]);
    expect(h.toasts).toEqual([]);
  });

  it("枚举失败时报错 toast 且不上传", async () => {
    h.listError = { code: "internal", message: "permission denied" };
    await pickFolderUpload({ sessionId: "s1", dir: "/data", knownSiblings: [] });
    expect(h.toasts).toEqual([
      { level: "error", text: expect.stringContaining("permission denied") },
    ]);
    expect(h.calls).toEqual(["listFolder:/local/pics"]);
  });

  it("空文件夹在远端创建目录", async () => {
    await pickFolderUpload({ sessionId: "s1", dir: "/data", knownSiblings: [] });
    expect(h.calls).toEqual(["listFolder:/local/pics", "mkdir:/data/pics"]);
    expect(h.toasts.some((t) => t.level === "success" && t.text.includes("pics"))).toBe(true);
  });

  it("winrm 会话拒绝含反斜杠的名称", async () => {
    h.kind = "winrm";
    h.listing = {
      name: "pics",
      files: [nativeFile("a\\b.txt", "/local/pics/a\\b.txt", 1)],
      emptyDirs: [],
    };
    await pickFolderUpload({ sessionId: "s1", dir: "/data", knownSiblings: [] });
    expect(h.toasts.some((t) => t.level === "error" && t.text.includes("反斜杠"))).toBe(true);
    expect(h.calls.some((c) => c.startsWith("upload:") || c.startsWith("write:"))).toBe(false);
  });

  it("打包模式从 localFileApi.read 读取字节构建 tar", async () => {
    h.choice = "pack";
    h.listing = {
      name: "pics",
      files: [
        nativeFile("a.txt", "/local/pics/a.txt", 3),
        nativeFile("sub/b.txt", "/local/pics/sub/b.txt", 2),
      ],
      emptyDirs: [],
    };
    h.localReads = {
      "/local/pics/a.txt": b64("abc"),
      "/local/pics/sub/b.txt": b64("de"),
    };
    await pickFolderUpload({ sessionId: "s1", dir: "/data", knownSiblings: [] });
    expect(h.calls[1]).toBe("list:/data");
    expect(h.calls).toContain("read:/local/pics/a.txt");
    expect(h.calls).toContain("read:/local/pics/sub/b.txt");
    const write = h.calls.find((c) => c.startsWith("write:"));
    expect(write).toBeDefined();
    const tarB64 = write!.slice("write:/data/pics.tar:".length);
    const tar = atob(tarB64);
    expect(ascii(tar, 0, 100)).toBe("sub/");
    expect(ascii(tar, 512, 100)).toBe("a.txt");
    expect(ascii(tar, 1024, 3)).toBe("abc");
    expect(ascii(tar, 1536, 100)).toBe("sub/b.txt");
    const extractIdx = h.calls.indexOf("extract:/data/pics.tar");
    const deleteIdx = h.calls.indexOf("delete:/data/pics.tar:false");
    expect(extractIdx).toBeGreaterThan(-1);
    expect(deleteIdx).toBeGreaterThan(extractIdx);
    expect(h.toasts.some((t) => t.level === "success")).toBe(true);
  });

  it("打包读取大小与枚举不一致时中止", async () => {
    h.choice = "pack";
    h.listing = { name: "pics", files: [nativeFile("a.txt", "/local/pics/a.txt", 3)], emptyDirs: [] };
    h.localReads = { "/local/pics/a.txt": b64("abcd") };
    await pickFolderUpload({ sessionId: "s1", dir: "/data", knownSiblings: [] });
    expect(h.toasts.some((t) => t.level === "error" && t.text.includes("大小"))).toBe(true);
    expect(h.calls.some((c) => c.startsWith("write:"))).toBe(false);
  });

  it("原生对话框不可用时回退 webkitdirectory 选择器（内容上传）", async () => {
    h.pickThrows = true;
    const realCreate = document.createElement.bind(document);
    let captured: HTMLInputElement | null = null;
    const spy = vi
      .spyOn(document, "createElement")
      .mockImplementation((tag: string, options?: ElementCreationOptions) => {
        const el = realCreate(tag, options);
        if (tag === "input") captured = el as HTMLInputElement;
        return el;
      });
    try {
      const pending = pickFolderUpload({ sessionId: "s1", dir: "/data", knownSiblings: [] });
      for (let i = 0; i < 100 && !captured; i++) await new Promise((r) => setTimeout(r, 2));
      expect(captured).not.toBeNull();
      const input = captured as unknown as HTMLInputElement;
      Object.defineProperty(input, "files", {
        value: [fakeFile("pics/a.txt", "A"), fakeFile("pics/sub/b.txt", "BB")],
        configurable: true,
      });
      input.dispatchEvent(new Event("change"));
      await pending;
      await waitForSettled();
      expect(h.calls).toContain(`write:/data/pics/a.txt:${b64("A")}`);
      expect(h.calls).toContain(`write:/data/pics/sub/b.txt:${b64("BB")}`);
      expect(h.calls.some((c) => c.startsWith("upload:"))).toBe(false);
    } finally {
      spy.mockRestore();
    }
  });
});

describe("桌面拖拽内容兜底", () => {
  it("松散文件经 fs_write 内容上传", async () => {
    const files = [fakeFile("x.txt", "X"), fakeFile("y.txt", "YY")];
    const dt = dropOf(
      files.map((file) => ({ webkitGetAsEntry: () => null, getAsFile: () => file })),
      files,
    );
    await dropFolderUpload({ sessionId: "s1", dir: "/data", dataTransfer: dt, knownSiblings: [] });
    await waitForSettled();
    expect(h.calls).toContain(`write:/data/x.txt:${b64("X")}`);
    expect(h.calls).toContain(`write:/data/y.txt:${b64("YY")}`);
    expect(h.calls.some((c) => c.startsWith("upload:"))).toBe(false);
  });

  it("文件夹拖拽逐文件模式按文件夹前缀内容上传", async () => {
    const folder = dirEntry("pics", [
      fileEntry("a.txt", fakeFile("pics/a.txt", "A")),
      dirEntry("sub", [fileEntry("b.txt", fakeFile("pics/sub/b.txt", "BB"))]),
    ]);
    const dt = dropOf([{ webkitGetAsEntry: () => folder, getAsFile: () => null }], []);
    await dropFolderUpload({ sessionId: "s1", dir: "/data", dataTransfer: dt, knownSiblings: [] });
    await waitForSettled();
    expect(h.calls).toContain(`write:/data/pics/a.txt:${b64("A")}`);
    expect(h.calls).toContain(`write:/data/pics/sub/b.txt:${b64("BB")}`);
  });

  it("拖拽冲突走保留两者策略自动重命名", async () => {
    h.choice = "keepBoth";
    h.siblings = [{ name: "x.txt", kind: "file" }] as FileEntryDto[];
    const files = [fakeFile("x.txt", "X")];
    const dt = dropOf(
      files.map((file) => ({ webkitGetAsEntry: () => null, getAsFile: () => file })),
      files,
    );
    await dropFolderUpload({ sessionId: "s1", dir: "/data", dataTransfer: dt, knownSiblings: h.siblings as FileEntryDto[] });
    await waitForSettled();
    expect(h.calls).toContain(`write:/data/x (1).txt:${b64("X")}`);
  });
});

describe("WEB 拖拽路径不回归", () => {
  it("web 松散文件仍走暂存 + fs_upload", async () => {
    h.web = true;
    const files = [fakeFile("x.txt", "X")];
    const dt = dropOf(
      files.map((file) => ({ webkitGetAsEntry: () => null, getAsFile: () => file })),
      files,
    );
    await dropFolderUpload({ sessionId: "s1", dir: "/data", dataTransfer: dt, knownSiblings: [] });
    await waitForSettled();
    expect(h.calls).toContain("stage:x.txt");
    expect(h.calls).toContain("upload:b1/x.txt->/data/x.txt");
    expect(h.calls.some((c) => c.startsWith("write:"))).toBe(false);
  });
});
