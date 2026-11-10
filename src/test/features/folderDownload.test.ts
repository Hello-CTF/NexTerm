/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

interface Entry {
  name: string;
  path: string;
  kind: string;
  size: number;
  mtime: number;
  mode: string;
  owner: string | null;
  group: string | null;
  symlinkTarget: string | null;
}

const META = { mode: "-rw-r--r--", owner: null, group: null, symlinkTarget: null };

interface LocalDir {
  kind: string;
  entries: { name: string; kind: string; size: number; mtimeMs: number }[];
}

const h = vi.hoisted(() => ({
  picked: "/dl" as string | null,
  choice: null as string | null,
  choices: [] as string[],
  remote: {} as Record<string, Entry[]>,
  local: {} as Record<string, LocalDir>,
  created: [] as string[][],
  createFailures: [] as { path: string; error: string }[],
  downloads: [] as string[],
  toasts: [] as { level: string; text: string }[],
  onList: null as ((path: string) => void) | null,
  progress: null as ((event: unknown) => void) | null,
  holdDownloads: false,
  windows: false,
  authorized: [] as { path: string; token: string }[],
  tokens: [] as string[],
  cancel: null as (() => void) | null,
  held: [] as (() => void)[],
}));

vi.mock("../../ipc/env", () => ({ WEB: false, TRANSPORT: "wails" }));
vi.mock("../../app/platform", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../app/platform")>();
  return { ...actual, isMac: () => false, isWindows: () => h.windows };
});

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return {
    ...actual,
    isNativeMobileShell: () => false,
    pickLocalFolder: async () => h.picked,
    askChoice: async (message: string) => {
      h.choices.push(message);
      return h.choice;
    },
    finishSave: async (target: string) => target,
    discardStaged: async () => undefined,
  };
});

vi.mock("../../app/store", () => ({
  useUi: {
    getState: () => ({
      pushToast: (level: string, text: string, action?: { onSelect: () => void }) => {
        h.toasts.push({ level, text });
        if (action) h.cancel = action.onSelect;
      },
      sessions: [{ id: "s1", kind: "ssh", name: "host" }],
      workspaces: [],
    }),
  },
}));

vi.mock("../../ipc/events", () => ({
  EVENTS: { fsProgress: "fs://progress" },
  listenEvent: async (_topic: string, handler: (event: unknown) => void) => {
    h.progress = handler;
    return () => undefined;
  },
}));

vi.mock("../../ipc/commands", () => ({
  fsApi: {
    list: async (_sid: string, path: string) => {
      h.onList?.(path);
      const entries = h.remote[path];
      if (!entries) throw new Error(`no listing for ${path}`);
      return entries;
    },
    download: async (_sid: string, remotePath: string, localPath: string) => {
      h.downloads.push(`${remotePath}->${localPath}`);
      if (h.holdDownloads) await new Promise<void>((resolve) => h.held.push(resolve));
      return 1;
    },
    cancel: async () => true,
  },
  localFileApi: {
    authorizeFolder: async (path: string) => {
      h.authorized.push({ path, token: "tok" });
      return { token: "tok", root: path };
    },
    listDirs: async (_token: string, paths: string[]) => ({
      dirs: paths.map((path) => ({ path, kind: h.local[path]?.kind ?? "missing", entries: h.local[path]?.entries ?? [] })),
    }),
    createDirs: async (token: string, paths: string[]) => {
      h.tokens.push(token);
      h.created.push(paths);
      return { failed: h.createFailures };
    },
  },
}));

import { downloadToFolder, FOLDER_DOWNLOAD_MAX_ENTRIES, startFolderDownload } from "../../features/files/folderDownload";
import { formatEta } from "../../features/files/fileProgress";
import { resetTransferQueue, transferEta, useTransferStore } from "../../features/files/transferStore";

function file(path: string, size: number, mtime: number): Entry {
  return { name: path.slice(path.lastIndexOf("/") + 1), path, kind: "file", size, mtime, ...META };
}

function dir(path: string): Entry {
  return { name: path.slice(path.lastIndexOf("/") + 1), path, kind: "dir", size: 0, mtime: 0, ...META };
}

const proj = dir("/r/proj");
const top = file("/r/top.txt", 7, 1000);

async function settle(): Promise<void> {
  for (let i = 0; i < 50; i += 1) {
    await Promise.resolve();
    await new Promise((resolve) => setTimeout(resolve, 0));
    const tasks = useTransferStore.getState().tasks;
    if (tasks.every((task) => task.status !== "queued" && task.status !== "running")) return;
  }
}

function taskSummary() {
  return useTransferStore
    .getState()
    .tasks.map((task) => ({ remote: task.remotePath, local: task.localPath, status: task.status, text: task.resultText }));
}

beforeEach(() => {
  resetTransferQueue();
  h.picked = "/dl";
  h.choice = null;
  h.choices = [];
  h.created = [];
  h.createFailures = [];
  h.downloads = [];
  h.toasts = [];
  h.onList = null;
  h.holdDownloads = false;
  h.windows = false;
  h.authorized = [];
  h.tokens = [];
  h.cancel = null;
  h.remote = {
    "/r/proj": [
      dir("/r/proj/sub"),
      dir("/r/proj/empty"),
      file("/r/proj/a.txt", 3, 2000),
      { name: "link", path: "/r/proj/link", kind: "symlink", size: 0, mtime: 0, ...META },
    ],
    "/r/proj/sub": [file("/r/proj/sub/b.txt", 5, 5000)],
    "/r/proj/empty": [],
  };
  h.local = { "/dl": { kind: "dir", entries: [] } };
});

afterEach(async () => {
  vi.useRealTimers();
  h.holdDownloads = false;
  for (const release of h.held.splice(0)) release();
  await settle();
});

describe("downloadToFolder", () => {
  it("queues every file under the picked folder and recreates the remote structure", async () => {
    const outcome = await downloadToFolder({ sessionId: "s1", entries: [proj, top] });
    await settle();
    expect(outcome).toMatchObject({ status: "queued", queued: 3, skipped: 1 });
    expect(h.choices).toEqual([]);
    expect(h.created).toHaveLength(1);
    expect([...h.created[0]].sort()).toEqual(["/dl/proj", "/dl/proj/empty", "/dl/proj/sub"]);
    expect([...h.downloads].sort()).toEqual([
      "/r/proj/a.txt->/dl/proj/a.txt",
      "/r/proj/sub/b.txt->/dl/proj/sub/b.txt",
      "/r/top.txt->/dl/top.txt",
    ]);
    const link = taskSummary().find((task) => task.remote === "/r/proj/link");
    expect(link?.status).toBe("skipped");
    expect(link?.text).toContain("符号链接");
  });

  it("asks once for the whole batch and overwrites only newer or resized files", async () => {
    h.local["/dl/proj"] = { kind: "dir", entries: [{ name: "a.txt", kind: "file", size: 3, mtimeMs: 1000 }] };
    h.local["/dl/proj/sub"] = { kind: "dir", entries: [{ name: "b.txt", kind: "file", size: 5, mtimeMs: 9000 }] };
    h.choice = "newer";
    await downloadToFolder({ sessionId: "s1", entries: [proj] });
    await settle();
    expect(h.choices).toHaveLength(1);
    expect(h.downloads).toEqual(["/r/proj/a.txt->/dl/proj/a.txt"]);
    const b = taskSummary().find((task) => task.remote === "/r/proj/sub/b.txt");
    expect(b?.status).toBe("skipped");
    expect(b?.text).toContain("已是最新");
    expect(h.created[0]).toEqual(["/dl/proj/empty"]);
  });

  it("renames conflicting files when asked to keep both", async () => {
    h.local["/dl/proj"] = {
      kind: "dir",
      entries: [
        { name: "a.txt", kind: "file", size: 3, mtimeMs: 1000 },
        { name: "a (1).txt", kind: "file", size: 1, mtimeMs: 1000 },
      ],
    };
    h.choice = "rename";
    await downloadToFolder({ sessionId: "s1", entries: [proj] });
    await settle();
    expect(h.downloads).toContain("/r/proj/a.txt->/dl/proj/a (2).txt");
    expect(h.downloads).toContain("/r/proj/sub/b.txt->/dl/proj/sub/b.txt");
  });

  it("skips or overwrites every conflict with the single answer", async () => {
    h.local["/dl"] = { kind: "dir", entries: [{ name: "top.txt", kind: "file", size: 1, mtimeMs: 1 }] };
    h.choice = "skip";
    await downloadToFolder({ sessionId: "s1", entries: [top] });
    await settle();
    expect(h.downloads).toEqual([]);
    expect(taskSummary()).toEqual([expect.objectContaining({ remote: "/r/top.txt", status: "skipped" })]);

    resetTransferQueue();
    h.choice = "overwrite";
    await downloadToFolder({ sessionId: "s1", entries: [top] });
    await settle();
    expect(h.downloads).toEqual(["/r/top.txt->/dl/top.txt"]);
  });

  it("never overwrites a local folder or link that has a remote file's name", async () => {
    h.local["/dl"] = { kind: "dir", entries: [{ name: "top.txt", kind: "dir", size: 0, mtimeMs: 1 }] };
    h.choice = "overwrite";
    await downloadToFolder({ sessionId: "s1", entries: [top] });
    await settle();
    expect(h.downloads).toEqual([]);
    expect(taskSummary()[0].text).toContain("不是普通文件");
  });

  it("leaves everything untouched when the conflict question is dismissed", async () => {
    h.local["/dl"] = { kind: "dir", entries: [{ name: "top.txt", kind: "file", size: 1, mtimeMs: 1 }] };
    h.choice = null;
    const outcome = await downloadToFolder({ sessionId: "s1", entries: [proj, top] });
    expect(outcome).toEqual({ status: "cancelled" });
    expect(h.created).toEqual([]);
    expect(useTransferStore.getState().tasks).toEqual([]);
    expect(h.toasts.at(-1)).toMatchObject({ level: "info" });
  });

  it("stops listing and queues nothing when aborted", async () => {
    const controller = new AbortController();
    h.onList = (path) => {
      if (path === "/r/proj") controller.abort();
    };
    const outcome = await downloadToFolder({ sessionId: "s1", entries: [proj, top], signal: controller.signal });
    expect(outcome).toEqual({ status: "cancelled" });
    expect(h.created).toEqual([]);
    expect(useTransferStore.getState().tasks).toEqual([]);
  });

  it("does nothing when no local folder is picked", async () => {
    h.picked = null;
    expect(await downloadToFolder({ sessionId: "s1", entries: [top] })).toEqual({ status: "cancelled" });
    expect(h.remote["/r/proj"]).toBeDefined();
    expect(useTransferStore.getState().tasks).toEqual([]);
  });

  it("skips files below a local file that occupies a needed folder", async () => {
    h.local["/dl/proj"] = { kind: "file", entries: [] };
    await downloadToFolder({ sessionId: "s1", entries: [proj, top] });
    await settle();
    expect(h.downloads).toEqual(["/r/top.txt->/dl/top.txt"]);
    expect(h.created.flat()).toEqual([]);
    const a = taskSummary().find((task) => task.remote === "/r/proj/a.txt");
    expect(a?.status).toBe("skipped");
    expect(a?.text).toContain("不是文件夹");
  });

  it("skips remote names that would leave or reshape the local folder", async () => {
    h.remote["/r/proj"] = [file("/r/proj/..", 1, 1), { ...file("/r/proj/x", 1, 1), name: "a\\..\\..\\evil" }, file("/r/proj/ok", 1, 1)];
    await downloadToFolder({ sessionId: "s1", entries: [proj] });
    await settle();
    expect(h.downloads).toEqual(["/r/proj/ok->/dl/proj/ok"]);
    expect(taskSummary().filter((task) => task.status === "skipped")).toHaveLength(2);
  });

  it("skips Windows device names even with an extension and says why", async () => {
    h.windows = true;
    h.remote["/r/proj"] = [file("/r/proj/NUL.txt", 1, 1), file("/r/proj/com1.tar.gz", 1, 1), file("/r/proj/com\u00b9", 1, 1), file("/r/proj/console.txt", 1, 1)];
    await downloadToFolder({ sessionId: "s1", entries: [proj] });
    await settle();
    expect(h.downloads).toEqual(["/r/proj/console.txt->/dl/proj/console.txt"]);
    const skipped = taskSummary().filter((task) => task.status === "skipped");
    expect(skipped).toHaveLength(3);
    expect(skipped.every((task) => task.text?.includes("保留设备名"))).toBe(true);
  });

  it("keeps device names on non-Windows hosts", async () => {
    h.remote["/r/proj"] = [file("/r/proj/nul.txt", 1, 1)];
    await downloadToFolder({ sessionId: "s1", entries: [proj] });
    await settle();
    expect(h.downloads).toEqual(["/r/proj/nul.txt->/dl/proj/nul.txt"]);
  });

  it("presents the grant for the picked folder to every local call", async () => {
    await downloadToFolder({ sessionId: "s1", entries: [proj] });
    await settle();
    expect(h.authorized).toEqual([{ path: "/dl", token: "tok" }]);
    expect(h.tokens).toEqual(["tok"]);
  });

  it("does not write below a local symlink and says so", async () => {
    h.local["/dl/proj"] = { kind: "symlink", entries: [] };
    await downloadToFolder({ sessionId: "s1", entries: [proj, top] });
    await settle();
    expect(h.downloads).toEqual(["/r/top.txt->/dl/top.txt"]);
    expect(h.created.flat()).toEqual([]);
    const a = taskSummary().find((task) => task.remote === "/r/proj/a.txt");
    expect(a?.status).toBe("skipped");
    expect(a?.text).toContain("符号链接");
  });

  it("refuses a batch that, with the picked folder, exceeds the backend folder limit", async () => {
    h.remote["/r/proj"] = Array.from({ length: FOLDER_DOWNLOAD_MAX_ENTRIES + 1 }, (_, i) => file(`/r/proj/f${i}`, 1, 1));
    const outcome = await downloadToFolder({ sessionId: "s1", entries: [proj] });
    expect(outcome).toMatchObject({ status: "failed" });
    expect(h.created).toEqual([]);
  });

  it("offers a cancel button while the remote tree is listed", async () => {
    let release: () => void = () => undefined;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    h.onList = () => { release(); };
    const running = startFolderDownload("s1", [proj, top]);
    await gate;
    expect(h.cancel).not.toBeNull();
    h.cancel?.();
    expect(await running).toEqual({ status: "cancelled" });
    expect(useTransferStore.getState().tasks).toEqual([]);
  });

  it("marks files below a folder that could not be created as skipped", async () => {
    h.createFailures = [{ path: "/dl/proj/sub", error: "permission denied" }];
    await downloadToFolder({ sessionId: "s1", entries: [proj] });
    await settle();
    expect(h.downloads).toEqual(["/r/proj/a.txt->/dl/proj/a.txt"]);
    const b = taskSummary().find((task) => task.remote === "/r/proj/sub/b.txt");
    expect(b?.status).toBe("skipped");
    expect(b?.text).toContain("permission denied");
  });

  it("lets queued folder downloads be cancelled", async () => {
    h.holdDownloads = true;
    await downloadToFolder({ sessionId: "s1", entries: [proj, top] });
    await Promise.resolve();
    const { cancelTransferTask } = await import("../../features/files/transferStore");
    const queued = useTransferStore.getState().tasks.filter((task) => task.status === "queued");
    expect(queued.length).toBeGreaterThan(0);
    for (const task of queued) cancelTransferTask(task.id);
    expect(useTransferStore.getState().tasks.filter((task) => task.status === "queued")).toEqual([]);
    expect(h.downloads).toHaveLength(1);
  });
});

describe("transfer rate", () => {
  it("derives speed and remaining time from progress events", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(0);
    h.holdDownloads = true;
    await downloadToFolder({ sessionId: "s1", entries: [top] });
    await Promise.resolve();
    const task = useTransferStore.getState().tasks[0];
    expect(task.status).toBe("running");
    h.progress?.({ taskId: task.id, transferred: 0, total: 10_000_000, done: false });
    vi.setSystemTime(1000);
    h.progress?.({ taskId: task.id, transferred: 2_000_000, total: 10_000_000, done: false });
    const running = useTransferStore.getState().tasks[0];
    expect(running.rateBps).toBe(2_000_000);
    expect(transferEta(running)).toBe(4);
    expect(formatEta(4)).toBe("0:04");
    expect(formatEta(3725)).toBe("1:02:05");
  });
});
