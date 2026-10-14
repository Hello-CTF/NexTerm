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
      if (content === undefined) throw new Error(`no read sample for ${path}`);
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
  throw new Error("transfer tasks did not settle");
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

describe("desktop native folder picker", () => {
  it("per-file mode uses real localPath, remote paths joined with forward slashes", async () => {
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

  it("native dialog cancel does nothing", async () => {
    h.pickedFolder = null;
    await pickFolderUpload({ sessionId: "s1", dir: "/data", knownSiblings: [] });
    expect(h.calls).toEqual([]);
    expect(h.toasts).toEqual([]);
  });

  it("enumeration failure shows an error toast and uploads nothing", async () => {
    h.listError = { code: "internal", message: "permission denied" };
    await pickFolderUpload({ sessionId: "s1", dir: "/data", knownSiblings: [] });
    expect(h.toasts).toEqual([
      { level: "error", text: expect.stringContaining("permission denied") },
    ]);
    expect(h.calls).toEqual(["listFolder:/local/pics"]);
  });

  it("empty folder creates the directory on the remote", async () => {
    await pickFolderUpload({ sessionId: "s1", dir: "/data", knownSiblings: [] });
    expect(h.calls).toEqual(["listFolder:/local/pics", "mkdir:/data/pics"]);
    expect(h.toasts.some((t) => t.level === "success" && t.text.includes("pics"))).toBe(true);
  });

  it("winrm sessions reject names containing backslashes", async () => {
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

  it("pack mode builds the tar from bytes read via localFileApi.read", async () => {
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

  it("pack aborts when the read size differs from the enumeration", async () => {
    h.choice = "pack";
    h.listing = { name: "pics", files: [nativeFile("a.txt", "/local/pics/a.txt", 3)], emptyDirs: [] };
    h.localReads = { "/local/pics/a.txt": b64("abcd") };
    await pickFolderUpload({ sessionId: "s1", dir: "/data", knownSiblings: [] });
    expect(h.toasts.some((t) => t.level === "error" && t.text.includes("大小"))).toBe(true);
    expect(h.calls.some((c) => c.startsWith("write:"))).toBe(false);
  });

  it("falls back to the webkitdirectory picker when the native dialog is unavailable (content upload)", async () => {
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

describe("desktop drag-drop content fallback", () => {
  it("loose files upload via fs_write content upload", async () => {
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

  it("folder drag per-file mode uploads content under the folder prefix", async () => {
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

  it("drag conflicts use the keep-both strategy with auto rename", async () => {
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

describe("WEB drag-drop path does not regress", () => {
  it("web loose files still go through staging + fs_upload", async () => {
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

describe("device session drag size guard", () => {
  const OVERSIZE = 65 * 1024 * 1024;

  function oversizedFile(rel: string): File {
    const file = fakeFile(rel, "x");
    Object.defineProperty(file, "size", { value: OVERSIZE });
    return file;
  }

  it("oversized loose files are rejected in the frontend: toast points to the upload button, zero IPC zero tasks", async () => {
    h.kind = "device";
    const big = oversizedFile("big.bin");
    const dt = dropOf([{ webkitGetAsEntry: () => null, getAsFile: () => big }], [big]);
    await dropFolderUpload({ sessionId: "s1", dir: "/data", dataTransfer: dt, knownSiblings: [] });
    expect(h.toasts).toEqual([{ level: "error", text: expect.stringContaining("单次写入上限") }]);
    expect(h.toasts[0]!.text).toContain("big.bin");
    expect(h.toasts[0]!.text).toContain("上传按钮");
    expect(h.calls).toEqual([]);
    expect(useTransferStore.getState().tasks).toEqual([]);
  });

  it("folder drags containing oversized files are likewise rejected before mode selection", async () => {
    h.kind = "device";
    const folder = dirEntry("pics", [
      fileEntry("a.txt", fakeFile("pics/a.txt", "A")),
      fileEntry("big.bin", oversizedFile("pics/big.bin")),
    ]);
    const dt = dropOf([{ webkitGetAsEntry: () => folder, getAsFile: () => null }], []);
    await dropFolderUpload({ sessionId: "s1", dir: "/data", dataTransfer: dt, knownSiblings: [] });
    expect(h.toasts).toEqual([{ level: "error", text: expect.stringContaining("单次写入上限") }]);
    expect(h.toasts[0]!.text).toContain("big.bin");
    expect(
      h.calls.some((c) => c.startsWith("list") || c.startsWith("write:") || c.startsWith("upload:") || c.startsWith("mkdir:")),
    ).toBe(false);
    expect(useTransferStore.getState().tasks).toEqual([]);
  });

  it("large files via the device native picker are unaffected (path streaming upload)", async () => {
    h.kind = "device";
    h.listing = {
      name: "pics",
      files: [nativeFile("big.bin", "D:\\pics\\big.bin", OVERSIZE)],
      emptyDirs: [],
    };
    await pickFolderUpload({ sessionId: "s1", dir: "/data", knownSiblings: [] });
    await waitForSettled();
    expect(h.calls).toContain("upload:D:\\pics\\big.bin->/data/pics/big.bin");
    expect(h.toasts.some((t) => t.level === "error")).toBe(false);
  });

  it("oversized files on non-device sessions can still drag-upload content", async () => {
    h.kind = "ssh";
    const big = oversizedFile("big.bin");
    const dt = dropOf([{ webkitGetAsEntry: () => null, getAsFile: () => big }], [big]);
    await dropFolderUpload({ sessionId: "s1", dir: "/data", dataTransfer: dt, knownSiblings: [] });
    await waitForSettled();
    expect(h.calls.some((c) => c.startsWith("write:/data/big.bin:"))).toBe(true);
    expect(h.toasts.some((t) => t.level === "error")).toBe(false);
  });
});

describe("drag-drop on platforms without webkitGetAsEntry", () => {
  function pseudoFolderFile(name: string): File {
    return new File([], name, { lastModified: MTIME_MS });
  }

  it("directory pseudo-files are filtered with an explicit notice, zero IPC zero tasks", async () => {
    const pseudo = pseudoFolderFile("pics");
    const dt = dropOf([{ getAsFile: () => pseudo }], [pseudo]);
    await dropFolderUpload({ sessionId: "s1", dir: "/data", dataTransfer: dt, knownSiblings: [] });
    expect(h.toasts).toEqual([
      {
        level: "info",
        text: "已跳过 1 个无法识别的条目（pics）：当前平台不支持拖拽文件夹，请使用上传文件夹按钮",
      },
    ]);
    expect(h.calls).toEqual([]);
    expect(useTransferStore.getState().tasks).toEqual([]);
  });

  it("mixed drags upload real files as usual and skip directory pseudo-files", async () => {
    const pseudo = pseudoFolderFile("pics");
    const real = fakeFile("real.txt", "R");
    const dt = dropOf(
      [{ getAsFile: () => pseudo }, { getAsFile: () => real }],
      [pseudo, real],
    );
    await dropFolderUpload({ sessionId: "s1", dir: "/data", dataTransfer: dt, knownSiblings: [] });
    await waitForSettled();
    expect(h.toasts).toContainEqual({
      level: "info",
      text: "已跳过 1 个无法识别的条目（pics）：当前平台不支持拖拽文件夹，请使用上传文件夹按钮",
    });
    expect(h.toasts.some((t) => t.level === "error")).toBe(false);
    expect(h.calls).toContain(`write:/data/real.txt:${b64("R")}`);
    expect(h.calls.some((c) => c.includes("pics"))).toBe(false);
  });

  it("the notice truncates to the first 3 names plus the total when multiple entries are skipped", async () => {
    const pseudos = ["a", "b", "c", "d", "e"].map(pseudoFolderFile);
    const dt = dropOf(pseudos.map((file) => ({ getAsFile: () => file })), pseudos);
    await dropFolderUpload({ sessionId: "s1", dir: "/data", dataTransfer: dt, knownSiblings: [] });
    expect(h.toasts).toEqual([
      {
        level: "info",
        text: "已跳过 5 个无法识别的条目（a、b、c、等 5 个）：当前平台不支持拖拽文件夹，请使用上传文件夹按钮",
      },
    ]);
    expect(h.calls).toEqual([]);
  });

  it("no notice when only real files are present, uploaded as loose files", async () => {
    const files = [fakeFile("x.txt", "X")];
    const dt = dropOf(files.map((file) => ({ getAsFile: () => file })), files);
    await dropFolderUpload({ sessionId: "s1", dir: "/data", dataTransfer: dt, knownSiblings: [] });
    await waitForSettled();
    expect(h.toasts.some((t) => t.text.includes("当前平台不支持"))).toBe(false);
    expect(h.calls).toContain(`write:/data/x.txt:${b64("X")}`);
  });
});
