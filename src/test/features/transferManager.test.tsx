/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoice: vi.fn(),
  promptText: vi.fn(),
  pickLocalFile: vi.fn(),
  pickSavePath: vi.fn(),
  finishSave: vi.fn(),
  discardStaged: vi.fn(),
  dropStaged: vi.fn(),
  stageFile: vi.fn(),
  isStagedPath: vi.fn(),
  list: vi.fn(),
  mkdir: vi.fn(),
  write: vi.fn(),
  upload: vi.fn(),
  download: vi.fn(),
  cancel: vi.fn(),
  packDownload: vi.fn(),
  extract: vi.fn(),
  termWrite: vi.fn(),
  toast: vi.fn(),
  listenEvent: vi.fn(),
  listFolder: vi.fn(),
  packSupported: vi.fn(),
  packUpload: vi.fn(),
}));
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return {
    ...actual,
    ask: mocks.ask,
    askChoice: mocks.askChoice,
    promptText: mocks.promptText,
    pickLocalFile: mocks.pickLocalFile,
    pickSavePath: mocks.pickSavePath,
    finishSave: mocks.finishSave,
    discardStaged: mocks.discardStaged,
  };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createFakeApi } = await import("../helpers/createFakeApi");
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return createFakeApi({
    ...actual,
    fsApi: {
      list: mocks.list,
      mkdir: mocks.mkdir,
      write: mocks.write,
      upload: mocks.upload,
      download: mocks.download,
      cancel: mocks.cancel,
      packDownload: mocks.packDownload,
      extract: mocks.extract,
    },
    localFileApi: { listFolder: mocks.listFolder },
    terminalApi: { write: mocks.termWrite },
    sessionApi: {},
  });
});
vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: mocks.listenEvent,
    EVENTS: { fsProgress: "fs://progress", filesDropped: "files://dropped" },
  };
});
vi.mock("../../ipc/webFiles", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/webFiles")>();
  return {
    ...actual,
    dropStaged: mocks.dropStaged,
    stageFile: mocks.stageFile,
    isStagedPath: mocks.isStagedPath,
  };
});
vi.mock("../../features/files/chunkedUpload", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../features/files/chunkedUpload")>();
  return { ...actual, uploadChunked: mocks.write };
});
vi.mock("../../features/files/folderPackUpload", () => ({
  packExtractSupported: mocks.packSupported,
  packUploadAndExtract: mocks.packUpload,
}));
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, i) => ({ index: i, start: i * 30, size: 30, key: i })),
    getTotalSize: () => count * 30,
  }),
}));

import { useUi } from "../../app/store";
import type { SessionInfo } from "../../ipc/commands";
import type { FileEntryDto } from "../../ipc/types";
import { FileTree } from "../../features/files/FileTree";
import { resetNativeDropDedupeForTest } from "../../features/files/nativeDrop";
import {
  enqueueDownload,
  resetTransferQueue,
  useTransferStore,
  type ScopedTransferTask,
} from "../../features/files/transferStore";

const SID = "s1";
const LOCAL_FILE = "/local/nginx-access.log";
const LOCAL_SAVE = "/local/a.txt";

function entry(name: string, kind: string): FileEntryDto {
  return {
    name,
    path: `~/${name}`,
    kind,
    size: kind === "dir" ? 4096 : 42_118,
    mode: kind === "dir" ? "755" : "644",
    owner: "u",
    group: "u",
    mtime: 1,
    symlinkTarget: null,
  };
}

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function buttonByTitle(container: ParentNode, title: string): HTMLButtonElement {
  const button = [...container.querySelectorAll("button")].find(
    (candidate) => candidate.title === title || candidate.textContent?.trim() === title,
  );
  if (!button) throw new Error(`Button not found: ${title}`);
  return button as HTMLButtonElement;
}

function rowByPath(container: HTMLElement, path: string): HTMLElement {
  const row = [...container.querySelectorAll<HTMLElement>("div[title]")].find((d) =>
    (d.getAttribute("title") ?? "").startsWith(path),
  );
  if (!row) throw new Error(`Row not found: ${path}`);
  return row;
}

function mockHomeEntries(names: [string, string][]) {
  mocks.list.mockImplementation((_s: string, p: string) =>
    Promise.resolve(p === "~" ? names.map(([n, k]) => entry(n, k)) : []),
  );
}

function dropEvent(files: File[]): Event {
  const event = new Event("drop", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", {
    value: { types: ["Files"], files, dropEffect: "none" },
  });
  return event;
}

function dropOn(element: Element, files: File[]): void {
  act(() => {
    element.dispatchEvent(dropEvent(files));
  });
}

type FakeFsEntry = {
  name: string;
  isFile: boolean;
  isDirectory: boolean;
  file?: (cb: (f: File) => void) => void;
  createReader?: () => { readEntries: (cb: (entries: FakeFsEntry[]) => void) => void };
};

function fakeFile(name: string, content = "x"): FakeFsEntry {
  const f = new File([content], name);
  return { name, isFile: true, isDirectory: false, file: (cb) => cb(f) };
}

function fakeDir(name: string, children: FakeFsEntry[]): FakeFsEntry {
  return {
    name,
    isFile: false,
    isDirectory: true,
    createReader: () => {
      let served = false;
      return {
        readEntries: (cb) => {
          if (served) {
            cb([]);
          } else {
            served = true;
            cb(children);
          }
        },
      };
    },
  };
}

function dropFoldersOn(element: Element, entries: FakeFsEntry[]): void {
  const items = entries.map((e) => ({
    kind: "file",
    getAsFile: () => null,
    webkitGetAsEntry: () => e,
  }));
  const event = new Event("drop", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", {
    value: { types: ["Files"], items, files: [], dropEffect: "none" },
  });
  act(() => {
    element.dispatchEvent(event);
  });
}

function sessionOfKind(kind: string): SessionInfo {
  return { id: SID, assetId: null, name: "host", kind, status: "connected", tabs: [], createdAt: 0 };
}

function modeDialogChoices(): string[] {
  const call = mocks.askChoice.mock.calls.find(([msg]) => String(msg).includes("上传文件夹"));
  const options = call?.[1] as { choices?: { key: string }[] } | undefined;
  return (options?.choices ?? []).map((c) => c.key);
}

const progressHandlers: ((e: unknown) => void)[] = [];

function emitProgress(event: unknown): void {
  act(() => {
    for (const handler of [...progressHandlers]) handler(event);
  });
}

const nativeDropHandlers: ((e: unknown) => void)[] = [];

function emitNativeDrop(event: unknown): void {
  act(() => {
    for (const handler of [...nativeDropHandlers]) handler(event);
  });
}

function stubElementFromPoint(el: Element | null): void {
  Object.defineProperty(document, "elementFromPoint", {
    value: () => el,
    configurable: true,
    writable: true,
  });
}

function panelRows(container: HTMLElement): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>("[data-task-key]")];
}

function transferTask(
  id: string,
  sessionId: string,
  hostId: string,
  targetName: string,
  status: ScopedTransferTask["status"],
): ScopedTransferTask {
  return {
    id,
    batchId: id,
    sessionId,
    hostId,
    targetName,
    direction: "upload",
    remotePath: `~/${id}.txt`,
    localPath: `/local/${id}.txt`,
    status,
    transferred: 0,
    total: 0,
  };
}

let mounted: MountedView | undefined;

function remountTree(): void {
  mounted?.unmount();
  mounted = mountWithClient(createElement(FileTree, { sessionId: SID }));
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.ask.mockResolvedValue(true);
  mocks.askChoice.mockResolvedValue(null);
  mocks.promptText.mockResolvedValue(null);
  mocks.pickLocalFile.mockResolvedValue(LOCAL_FILE);
  mocks.pickSavePath.mockResolvedValue(LOCAL_SAVE);
  mocks.finishSave.mockResolvedValue(LOCAL_SAVE);
  mocks.discardStaged.mockResolvedValue(undefined);
  mocks.dropStaged.mockResolvedValue(undefined);
  mocks.stageFile.mockImplementation(async (file: File) => ({
    id: `staged-${file.name}`,
    path: `/staged/${file.name}`,
  }));
  mocks.isStagedPath.mockReturnValue(false);
  mocks.write.mockResolvedValue(undefined);
  mocks.upload.mockReset();
  mocks.upload.mockResolvedValue(100);
  mocks.download.mockResolvedValue(100);
  mocks.cancel.mockResolvedValue(true);
  mocks.mkdir.mockResolvedValue(undefined);
  mocks.packSupported.mockImplementation((kind: string) => kind === "ssh" || kind === "device");
  mocks.packUpload.mockResolvedValue("~/pics");
  mocks.listFolder.mockResolvedValue({ name: "pics", files: [], emptyDirs: [] });
  nativeDropHandlers.length = 0;
  resetNativeDropDedupeForTest();
  mocks.listenEvent.mockImplementation((t: string, cb: (e: unknown) => void) => {
    if (t === "files://dropped") nativeDropHandlers.push(cb);
    else progressHandlers.push(cb);
    return Promise.resolve(() => {});
  });
  mockHomeEntries([["a.txt", "file"], ["sub", "dir"]]);
  useUi.setState({
    pushToast: mocks.toast,
    leftOpen: true,
    leftWidth: 260,
    workspaces: [],
    activeWorkspaceId: null,
    sessions: [],
  });
  useTransferStore.setState({ tasks: [] });
});

afterEach(async () => {
  mounted?.unmount();
  mounted = undefined;
  await flush();
});

describe("unified transfer queue panel (FileTree)", () => {
  beforeEach(async () => {
    remountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("queues uploads with direction, remote path, and in-progress status, then shows a clear completion toast", async () => {
    act(() => useUi.setState({ sessions: [{ ...sessionOfKind("ssh"), assetId: "asset-a", name: "Alpha" }] }));
    let resolveUpload!: (v: number) => void;
    mocks.upload.mockReturnValueOnce(new Promise<number>((r) => (resolveUpload = r)));

    click(buttonByTitle(mounted!.container, "上传到当前目录"));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledWith(SID, LOCAL_FILE, "~/nginx-access.log", false, expect.any(String), expect.any(String)));

    const panel = mounted!.container.querySelector('[data-testid="transfer-panel"]');
    expect(panel).not.toBeNull();
    expect(panel!.textContent).toContain("~/nginx-access.log");
    expect(panel!.textContent).toContain(LOCAL_FILE);
    expect(panel!.textContent).toContain("目标：Alpha · 会话 s1");
    expect((panel!.textContent ?? "").indexOf(LOCAL_FILE)).toBeLessThan(
      (panel!.textContent ?? "").indexOf("~/nginx-access.log"),
    );
    expect(panel!.textContent).toContain("1 个未结束");

    resolveUpload(42_118);
    await waitFor(() => expect(panelRows(mounted!.container)[0]?.textContent).toContain("已完成"));
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("~/nginx-access.log"));
    const successText = mocks.toast.mock.calls.find((c) => c[0] === "success")?.[1] ?? "";
    expect(successText).not.toMatch(/task/i);
  });

  it("updates the panel percentage in real time from progress events", async () => {
    let resolveUpload!: (v: number) => void;
    mocks.upload.mockReturnValueOnce(new Promise<number>((r) => (resolveUpload = r)));

    click(buttonByTitle(mounted!.container, "上传到当前目录"));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalled());

    emitProgress({ taskId: useTransferStore.getState().tasks[0].id, transferred: 50, total: 100, done: false });
    await waitFor(() => expect(panelRows(mounted!.container)[0]?.textContent).toContain("50%"));

    emitProgress({ taskId: useTransferStore.getState().tasks[0].id, transferred: 100, total: 100, done: true });
    resolveUpload(100);
    await waitFor(() => expect(panelRows(mounted!.container)[0]?.textContent).toContain("已完成"));
  });

  it("shows task errors in the panel and includes the path and reason in the toast without exposing the internal taskId", async () => {
    mocks.upload.mockRejectedValueOnce(new Error("connection reset"));

    click(buttonByTitle(mounted!.container, "上传到当前目录"));
    await waitFor(() => expect(panelRows(mounted!.container)[0]?.textContent).toContain("失败"));

    const row = panelRows(mounted!.container)[0];
    expect(row?.textContent).toContain("connection reset");
    const errorText = mocks.toast.mock.calls.find((c) => c[0] === "error")?.[1] ?? "";
    expect(errorText).toContain("上传失败");
    expect(errorText).toContain("~/nginx-access.log");
    expect(errorText).toContain("connection reset");
    expect(errorText).not.toContain("srv-");
    await waitFor(() => expect(mocks.discardStaged).toHaveBeenCalledWith(LOCAL_FILE));
  });

  it("queues downloads with a local destination, then delivers the file and notifies on completion", async () => {
    let resolveDownload!: (v: number) => void;
    mocks.download.mockReturnValueOnce(new Promise<number>((r) => (resolveDownload = r)));

    click(rowByPath(mounted!.container, "~/a.txt"));
    act(() => {
      rowByPath(mounted!.container, "~/a.txt").dispatchEvent(
        new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 5, clientY: 5 }),
      );
    });
    click(
      [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((b) =>
        b.textContent?.includes("下载当前文件"),
      )!,
    );
    await waitFor(() =>
      expect(mocks.download).toHaveBeenCalledWith(SID, "~/a.txt", LOCAL_SAVE, false, expect.any(String), expect.any(String)),
    );

    const panel = mounted!.container.querySelector('[data-testid="transfer-panel"]');
    expect(panel!.textContent).toContain("~/a.txt");
    expect(panel!.textContent).toContain(LOCAL_SAVE);
    expect((panel!.textContent ?? "").indexOf("~/a.txt")).toBeLessThan(
      (panel!.textContent ?? "").indexOf(LOCAL_SAVE),
    );
    expect(panel!.textContent).toContain("1 个未结束");

    resolveDownload(10);
    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining(LOCAL_SAVE)),
    );
    expect(mocks.finishSave).toHaveBeenCalledWith(LOCAL_SAVE, "a.txt");
    await waitFor(() => expect(panelRows(mounted!.container)[0]?.textContent).toContain("已完成"));
  });

  it("allows cancelling queued tasks without affecting the running task", async () => {
    let resolveFirst!: (v: number) => void;
    mocks.upload
      .mockReturnValueOnce(new Promise<number>((r) => (resolveFirst = r)))
      .mockResolvedValueOnce(100);

    click(buttonByTitle(mounted!.container, "上传到当前目录"));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));

    mocks.pickLocalFile.mockResolvedValueOnce("/local/second.txt");
    click(buttonByTitle(mounted!.container, "上传到当前目录"));
    await waitFor(() => expect(panelRows(mounted!.container).length).toBe(2));
    expect(panelRows(mounted!.container)[1]?.textContent).toContain("排队中");

    const cancelButton = [...panelRows(mounted!.container)[1].querySelectorAll("button")].find((b) =>
      b.title.includes("取消"),
    );
    expect(cancelButton).toBeTruthy();
    click(cancelButton!);
    await waitFor(() => expect(panelRows(mounted!.container)[1]?.textContent).toContain("已取消"));
    expect(mocks.upload).toHaveBeenCalledTimes(1);

    resolveFirst(100);
    await waitFor(() => expect(panelRows(mounted!.container)[0]?.textContent).toContain("已完成"));
    await flush();
    expect(mocks.upload).toHaveBeenCalledTimes(1);
  });

  it("cleans the local staged file when cancelling a queued upload without affecting the running task", async () => {
    mocks.upload.mockReset();
    mocks.upload.mockResolvedValue(100);
    let resolveFirst!: (v: number) => void;
    mocks.upload.mockReturnValueOnce(new Promise<number>((r) => (resolveFirst = r)));

    click(buttonByTitle(mounted!.container, "上传到当前目录"));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));

    mocks.pickLocalFile.mockResolvedValueOnce("/local/staged-second.txt");
    click(buttonByTitle(mounted!.container, "上传到当前目录"));
    await waitFor(() => expect(panelRows(mounted!.container).length).toBe(2));

    const cancelButton = [...panelRows(mounted!.container)[1].querySelectorAll("button")].find((b) =>
      b.title.includes("取消"),
    );
    expect(cancelButton).toBeTruthy();
    click(cancelButton!);
    await waitFor(() => expect(panelRows(mounted!.container)[1]?.textContent).toContain("已取消"));
    await waitFor(() =>
      expect(mocks.discardStaged).toHaveBeenCalledWith("/local/staged-second.txt"),
    );
    expect(mocks.discardStaged).not.toHaveBeenCalledWith(LOCAL_FILE);
    expect(mocks.upload).toHaveBeenCalledTimes(1);

    resolveFirst(100);
    await waitFor(() => expect(panelRows(mounted!.container)[0]?.textContent).toContain("已完成"));
  });

  it("exposes complete aria numeric attributes on the progress bar", async () => {
    mocks.upload.mockReset();
    mocks.upload.mockResolvedValue(100);
    let resolveUpload!: (v: number) => void;
    mocks.upload.mockReturnValueOnce(new Promise<number>((r) => (resolveUpload = r)));

    click(buttonByTitle(mounted!.container, "上传到当前目录"));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalled());

    const bar = mounted!.container.querySelector('[role="progressbar"]');
    expect(bar?.getAttribute("aria-valuemin")).toBe("0");
    expect(bar?.getAttribute("aria-valuemax")).toBe("100");
    expect(bar?.getAttribute("aria-valuenow")).toBe("0");

    emitProgress({ taskId: useTransferStore.getState().tasks[0].id, transferred: 50, total: 100, done: false });
    await waitFor(() =>
      expect(
        mounted!.container.querySelector('[role="progressbar"]')?.getAttribute("aria-valuenow"),
      ).toBe("50"),
    );

    resolveUpload(100);
    await flush();
  });

  it("collapses the panel after clearing finished tasks", async () => {
    click(buttonByTitle(mounted!.container, "上传到当前目录"));
    await waitFor(() => expect(panelRows(mounted!.container)[0]?.textContent).toContain("已完成"));

    click(buttonByTitle(mounted!.container, "清除已结束"));
    await waitFor(() =>
      expect(mounted!.container.querySelector('[data-testid="transfer-panel"]')).toBeNull(),
    );
  });

  it("defaults to the current host, including an earlier session of that host, and keeps other hosts when clearing", async () => {
    act(() => useUi.setState({ sessions: [{ ...sessionOfKind("ssh"), assetId: "asset-a", name: "Alpha" }] }));
    act(() => {
      useTransferStore.setState({
        tasks: [
          transferTask("current", SID, "asset-a", "Alpha", "done"),
          transferTask("previous", "s-previous", "asset-a", "Alpha", "failed"),
          transferTask("other", "s-other", "asset-b", "Beta", "done"),
        ],
      });
    });

    expect(panelRows(mounted!.container).map((row) => row.dataset.taskKey)).toEqual([
      "current",
      "previous",
    ]);
    expect(mounted!.container.textContent).toContain("目标：Alpha · 会话 s1");
    expect(mounted!.container.textContent).not.toContain("Beta");

    click(buttonByTitle(mounted!.container, "清除已结束"));
    expect(useTransferStore.getState().tasks.map((task) => task.id)).toEqual(["other"]);
    expect(panelRows(mounted!.container)).toHaveLength(0);
    expect(mounted!.container.textContent).toContain("当前主机没有传输任务");

    click(buttonByTitle(mounted!.container, "当前主机"));
    expect(panelRows(mounted!.container).map((row) => row.dataset.taskKey)).toEqual(["other"]);
    expect(mounted!.container.textContent).toContain("目标：Beta · 会话 s-other");
  });

  it("global view cancels only the selected queued task and clears finished tasks in that view", async () => {
    act(() => useUi.setState({ sessions: [{ ...sessionOfKind("ssh"), assetId: "asset-a", name: "Alpha" }] }));
    act(() => {
      useTransferStore.setState({
        tasks: [
          transferTask("current", SID, "asset-a", "Alpha", "queued"),
          transferTask("other", "s-other", "asset-b", "Beta", "queued"),
          transferTask("finished", "s-third", "asset-c", "Gamma", "done"),
        ],
      });
    });
    click(buttonByTitle(mounted!.container, "当前主机"));
    expect(panelRows(mounted!.container)).toHaveLength(3);
    const otherRow = mounted!.container.querySelector<HTMLElement>('[data-task-key="other"]');
    expect(otherRow).not.toBeNull();
    click(buttonByTitle(otherRow!, "取消这个排队任务"));
    expect(useTransferStore.getState().tasks.find((task) => task.id === "other")?.status).toBe("cancelled");
    expect(useTransferStore.getState().tasks.find((task) => task.id === "current")?.status).toBe("queued");

    click(buttonByTitle(mounted!.container, "清除已结束"));
    expect(useTransferStore.getState().tasks.map((task) => task.id)).toEqual(["current"]);
  });

  it("account reset hides old paths, discards queued staging, and ignores late completion", async () => {
    let resolveUpload!: (bytes: number) => void;
    mocks.upload.mockReturnValueOnce(new Promise<number>((resolve) => (resolveUpload = resolve)));
    click(buttonByTitle(mounted!.container, "上传到当前目录"));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));

    mocks.pickLocalFile.mockResolvedValueOnce("/local/queued.txt");
    click(buttonByTitle(mounted!.container, "上传到当前目录"));
    await waitFor(() => expect(panelRows(mounted!.container)).toHaveLength(2));

    act(() => resetTransferQueue());
    expect(useTransferStore.getState().tasks).toEqual([]);
    expect(mounted!.container.querySelector('[data-testid="transfer-panel"]')).toBeNull();
    await waitFor(() => expect(mocks.discardStaged).toHaveBeenCalledWith("/local/queued.txt"));

    resolveUpload(100);
    await flush();
    expect(useTransferStore.getState().tasks).toEqual([]);
    expect(mocks.upload).toHaveBeenCalledTimes(1);
    expect(mocks.toast).not.toHaveBeenCalledWith("success", expect.anything());
  });

  it("account reset discards an in-flight download before it can be saved in the next account", async () => {
    let resolveOldDownload!: (bytes: number) => void;
    mocks.download.mockReturnValueOnce(new Promise<number>((resolve) => (resolveOldDownload = resolve)));
    act(() => {
      enqueueDownload(SID, { remotePath: "~/old-account.txt", localPath: "/staged/old-account.txt" });
    });
    await waitFor(() => expect(mocks.download).toHaveBeenCalledTimes(1));

    act(() => resetTransferQueue());
    expect(useTransferStore.getState().tasks).toEqual([]);
    expect(mocks.discardStaged).not.toHaveBeenCalledWith("/staged/old-account.txt");

    act(() => {
      enqueueDownload(SID, { remotePath: "~/new-account.txt", localPath: "/staged/new-account.txt" });
    });
    resolveOldDownload(100);
    await waitFor(() => expect(mocks.finishSave).toHaveBeenCalledWith("/staged/new-account.txt", "new-account.txt"));
    expect(mocks.finishSave).not.toHaveBeenCalledWith("/staged/old-account.txt", "old-account.txt");
    expect(mocks.discardStaged).toHaveBeenCalledWith("/staged/old-account.txt");
    expect(useTransferStore.getState().tasks.every((task) => task.remotePath !== "~/old-account.txt")).toBe(true);
    expect(mocks.toast).not.toHaveBeenCalledWith("success", expect.stringContaining("old-account.txt"));
  });
});

describe("drag-and-drop upload (FileTree)", () => {
  beforeEach(async () => {
    remountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("queues dropped files in the current directory", async () => {
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropOn(list, [new File(["x"], "d.txt")]);

    await waitFor(() =>
      expect(mocks.upload).toHaveBeenCalledWith(SID, expect.anything(), "~/d.txt", false, expect.any(String), expect.any(String)),
    );
    const localPath = mocks.upload.mock.calls[0]?.[1] as string;
    expect(localPath).toContain("d.txt");
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("uploads into a directory when files are dropped on its row", async () => {
    dropOn(rowByPath(mounted!.container, "~/sub"), [new File(["x"], "d.txt")]);
    await waitFor(() =>
      expect(mocks.upload).toHaveBeenCalledWith(SID, expect.anything(), "~/sub/d.txt", false, expect.any(String), expect.any(String)),
    );
  });

  it("renames automatically and uploads after choosing keep both for a conflict", async () => {
    mockHomeEntries([["a.txt", "file"], ["d.txt", "file"]]);
    remountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/d.txt")).toBeTruthy());

    mocks.askChoice.mockResolvedValueOnce("keepBoth");
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropOn(list, [new File(["x"], "d.txt")]);

    await waitFor(() =>
      expect(mocks.upload).toHaveBeenCalledWith(SID, expect.anything(), "~/d (1).txt", false, expect.any(String), expect.any(String)),
    );
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.askChoice).toHaveBeenCalledWith(
      expect.stringContaining("d.txt"),
      expect.objectContaining({ title: "上传冲突" }),
    );
  });

  it("asks per conflict: overwrites the original path or renames to keep both", async () => {
    mockHomeEntries([
      ["a.txt", "file"],
      ["d.txt", "file"],
      ["e.txt", "file"],
    ]);
    remountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/e.txt")).toBeTruthy());

    mocks.askChoice.mockResolvedValueOnce("overwrite").mockResolvedValueOnce("keepBoth");
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropOn(list, [new File(["1"], "d.txt"), new File(["2"], "e.txt")]);

    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));
    expect(mocks.upload).toHaveBeenNthCalledWith(1, SID, expect.anything(), "~/d.txt", false, expect.any(String), expect.any(String));
    expect(mocks.upload).toHaveBeenNthCalledWith(2, SID, expect.anything(), "~/e (1).txt", false, expect.any(String), expect.any(String));
    expect(mocks.askChoice).toHaveBeenCalledTimes(2);
  });

  it("automatically keeps both case-variant names in one winrm batch without a conflict prompt or error", async () => {
    act(() => useUi.setState({ sessions: [sessionOfKind("winrm")] }));
    // WinRM trees start at "/" instead of "~".
    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "/"));
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropOn(list, [new File(["1"], "x.txt"), new File(["2"], "X.TXT")]);

    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));
    expect(mocks.upload).toHaveBeenNthCalledWith(1, SID, expect.anything(), "/x.txt", false, expect.any(String), expect.any(String));
    expect(mocks.upload).toHaveBeenNthCalledWith(2, SID, expect.anything(), "/X (1).TXT", false, expect.any(String), expect.any(String));
    expect(mocks.askChoice).not.toHaveBeenCalled();
    expect(mocks.toast).not.toHaveBeenCalledWith("error", expect.anything());
  });

  it("automatically keeps both identical names in one ssh batch without a conflict prompt or error", async () => {
    act(() => useUi.setState({ sessions: [sessionOfKind("ssh")] }));
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropOn(list, [new File(["1"], "x.txt"), new File(["2"], "x.txt")]);

    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));
    expect(mocks.upload).toHaveBeenNthCalledWith(1, SID, expect.anything(), "~/x.txt", false, expect.any(String), expect.any(String));
    expect(mocks.upload).toHaveBeenNthCalledWith(2, SID, expect.anything(), "~/x (1).txt", false, expect.any(String), expect.any(String));
    expect(mocks.askChoice).not.toHaveBeenCalled();
    expect(mocks.toast).not.toHaveBeenCalledWith("error", expect.anything());
  });

  it("applies skip-all to the current queue: asks once and skips every remaining conflict", async () => {
    mockHomeEntries([
      ["a.txt", "file"],
      ["d.txt", "file"],
      ["e.txt", "file"],
    ]);
    remountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/e.txt")).toBeTruthy());

    mocks.askChoice.mockResolvedValueOnce("skip-all");
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropOn(list, [new File(["1"], "d.txt"), new File(["2"], "e.txt")]);

    await waitFor(() => expect(panelRows(mounted!.container).length).toBe(2));
    expect(mocks.askChoice).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(mocks.dropStaged).toHaveBeenCalledTimes(2));
    await flush();
    expect(mocks.upload).not.toHaveBeenCalled();
    for (const row of panelRows(mounted!.container)) {
      expect(row.textContent).toContain("已跳过");
    }
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("跳过"));
  });

  it("cancels the entire batch without uploading when the conflict dialog is cancelled and leaves the remote unchanged", async () => {
    mockHomeEntries([["a.txt", "file"], ["d.txt", "file"]]);
    remountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/d.txt")).toBeTruthy());

    mocks.askChoice.mockResolvedValueOnce(null);
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropOn(list, [new File(["x"], "d.txt")]);

    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("已取消上传")),
    );
    await waitFor(() =>
      expect(mocks.dropStaged).toHaveBeenCalledWith(expect.stringContaining("d.txt")),
    );
    await flush();
    expect(mocks.upload).not.toHaveBeenCalled();
  });
});

describe("folder upload (FileTree)", () => {
  beforeEach(async () => {
    remountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("provides a folder upload entry in the toolbar", () => {
    expect(buttonByTitle(mounted!.container, "上传文件夹到当前目录")).toBeTruthy();
  });

  it("per-file upload (default): traverses the folder and queues relative paths with matching WEB staging paths", async () => {
    let seq = 0;
    mocks.stageFile.mockImplementation(async (f: File) => {
      seq += 1;
      return { id: `st-${seq}`, path: `/staged/${seq}-${f.name}` };
    });
    mocks.askChoice.mockResolvedValueOnce("perFile");
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropFoldersOn(list, [fakeDir("pics", [fakeFile("a.jpg"), fakeDir("sub", [fakeFile("a.jpg")])])]);

    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));
    expect(mocks.upload).toHaveBeenNthCalledWith(1, SID, "/staged/1-a.jpg", "~/pics/a.jpg", false, expect.any(String), expect.any(String));
    expect(mocks.upload).toHaveBeenNthCalledWith(
      2,
      SID,
      "/staged/2-a.jpg",
      "~/pics/sub/a.jpg",
      false,
      expect.any(String),
      expect.any(String),
    );
    expect(mocks.askChoice).toHaveBeenCalledTimes(1);
    expect(mocks.askChoice).toHaveBeenCalledWith(
      expect.stringContaining("「pics」"),
      expect.objectContaining({ title: "上传文件夹" }),
    );
    expect(mocks.askChoice.mock.calls[0]?.[0]).toContain("共 2 个文件");
    expect(modeDialogChoices()).toEqual(["perFile"]);
  });

  it("does not offer packed upload for winrm sessions", async () => {
    act(() => useUi.setState({ sessions: [sessionOfKind("winrm")] }));
    mocks.askChoice.mockResolvedValueOnce("perFile");
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropFoldersOn(list, [fakeDir("pics", [fakeFile("a.jpg")])]);

    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    expect(modeDialogChoices()).toEqual(["perFile"]);
    expect(mocks.packUpload).not.toHaveBeenCalled();
  });

  it("rejects entries with backslashes for winrm: reports an error, cleans staging, and skips upload", async () => {
    act(() => useUi.setState({ sessions: [sessionOfKind("winrm")] }));
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropFoldersOn(list, [fakeDir("pics", [fakeFile("a\\b.jpg")])]);

    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("反斜杠")),
    );
    const errorText = mocks.toast.mock.calls.find((c) => c[0] === "error")?.[1] ?? "";
    expect(errorText).toContain("a\\b.jpg");
    expect(mocks.askChoice).not.toHaveBeenCalled();
    expect(mocks.upload).not.toHaveBeenCalled();
    expect(mocks.mkdir).not.toHaveBeenCalled();
    expect(mocks.dropStaged).toHaveBeenCalledWith("/staged/a\\b.jpg");
  });

  it("allows packed upload for ssh: calls packUploadAndExtract and cleans staging", async () => {
    act(() => useUi.setState({ sessions: [sessionOfKind("ssh")] }));
    mocks.askChoice.mockResolvedValueOnce("pack");
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropFoldersOn(list, [
      fakeDir("pics", [
        fakeFile("a.jpg"),
        fakeDir("sub", [fakeFile("b.jpg")]),
        fakeDir("empty", []),
      ]),
    ]);

    await waitFor(() => expect(mocks.packUpload).toHaveBeenCalledTimes(1));
    expect(modeDialogChoices()).toEqual(["perFile", "pack"]);
    const arg = mocks.packUpload.mock.calls[0]?.[0] as {
      sessionId: string;
      dir: string;
      folderName: string;
      files: { relativePath: string; localPath: string; size: number }[];
      emptyDirs?: string[];
      onProgress?: unknown;
    };
    expect(arg.sessionId).toBe(SID);
    expect(arg.dir).toBe("~");
    expect(arg.folderName).toBe("pics");
    expect(arg.files.map((f) => [f.relativePath, f.localPath])).toEqual([
      ["a.jpg", "/staged/a.jpg"],
      ["sub/b.jpg", "/staged/b.jpg"],
    ]);
    expect(arg.emptyDirs).toEqual(["empty"]);
    expect(typeof arg.onProgress).toBe("function");
    expect(mocks.upload).not.toHaveBeenCalled();
    await waitFor(() => expect(mocks.dropStaged).toHaveBeenCalledWith("/staged/a.jpg"));
    expect(mocks.dropStaged).toHaveBeenCalledWith("/staged/b.jpg");
    expect(mocks.toast).toHaveBeenCalledWith(
      "success",
      expect.stringContaining("已上传并解包"),
    );
  });

  it("cancels the mode dialog without uploading the batch and cleans staging", async () => {
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropFoldersOn(list, [fakeDir("pics", [fakeFile("a.jpg"), fakeDir("sub", [fakeFile("b.jpg")])])]);

    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("已取消上传")),
    );
    expect(mocks.upload).not.toHaveBeenCalled();
    expect(mocks.packUpload).not.toHaveBeenCalled();
    expect(mocks.dropStaged).toHaveBeenCalledWith("/staged/a.jpg");
    expect(mocks.dropStaged).toHaveBeenCalledWith("/staged/b.jpg");
  });

  it("creates remote directories for an empty folder without showing the mode dialog", async () => {
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropFoldersOn(list, [fakeDir("pics", [fakeDir("empty", [])])]);

    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith(
        "success",
        expect.stringContaining("已创建空文件夹"),
      ),
    );
    expect(mocks.askChoice).not.toHaveBeenCalled();
    expect(mocks.mkdir).toHaveBeenCalledWith(SID, "~/pics/empty");
    expect(mocks.mkdir).toHaveBeenCalledWith(SID, "~/pics");
    expect(mocks.upload).not.toHaveBeenCalled();
  });

  it("uploads only non-conflicting files after choosing to skip same-named files in an existing target folder", async () => {
    mocks.list.mockImplementation((_s: string, p: string) => {
      if (p === "~")
        return Promise.resolve([entry("a.txt", "file"), entry("pics", "dir")]);
      if (p === "~/pics")
        return Promise.resolve([{ ...entry("a.jpg", "file"), path: "~/pics/a.jpg" }]);
      return Promise.resolve([]);
    });
    remountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/pics")).toBeTruthy());

    mocks.askChoice.mockResolvedValueOnce("perFile").mockResolvedValueOnce("skip");
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropFoldersOn(list, [fakeDir("pics", [fakeFile("a.jpg"), fakeDir("sub", [fakeFile("b.jpg")])])]);

    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    expect(mocks.upload).toHaveBeenCalledWith(SID, "/staged/b.jpg", "~/pics/sub/b.jpg", false, expect.any(String), expect.any(String));
    expect(mocks.askChoice).toHaveBeenCalledTimes(2);
    expect(mocks.askChoice).toHaveBeenNthCalledWith(
      2,
      expect.stringContaining("同名文件"),
      expect.objectContaining({ title: "上传文件夹" }),
    );
    await waitFor(() => expect(mocks.dropStaged).toHaveBeenCalledWith("/staged/a.jpg"));
    await waitFor(() =>
      expect(panelRows(mounted!.container).some((r) => r.textContent?.includes("已跳过"))).toBe(
        true,
      ),
    );
  });
});

describe("native drag-and-drop upload (wails native drop)", () => {
  beforeEach(async () => {
    remountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("marks both FileTree drop surface with data-file-drop-target", async () => {
    expect(mounted!.container.querySelector("[data-file-drop-target]")).not.toBeNull();
    remountTree();
    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mounted!.container.querySelector("[data-file-drop-target]")).not.toBeNull();
  });

  it("queues native file drops by their real paths in the current directory", async () => {
    stubElementFromPoint(mounted!.container.querySelector("[data-file-drop-target]"));
    emitNativeDrop({ paths: [{ path: "/local/d.txt", dir: false }], x: 5, y: 5 });

    await waitFor(() =>
      expect(mocks.upload).toHaveBeenCalledWith(SID, "/local/d.txt", "~/d.txt", false, expect.any(String), expect.any(String)),
    );
    expect(useTransferStore.getState().tasks.some((t) => t.remotePath === "~/d.txt")).toBe(true);
    expect(mocks.listFolder).not.toHaveBeenCalled();
  });

  it("uploads native drops into the directory row they land on", async () => {
    const row = mounted!.container.querySelector("[data-drop-path]")!;
    expect(row.getAttribute("data-drop-path")).toBe("~/sub");
    stubElementFromPoint(row);
    emitNativeDrop({ paths: [{ path: "/local/d.txt", dir: false }], x: 5, y: 5 });

    await waitFor(() =>
      expect(mocks.upload).toHaveBeenCalledWith(SID, "/local/d.txt", "~/sub/d.txt", false, expect.any(String), expect.any(String)),
    );
  });

  it("enumerates native folder drops through local_folder_list and uploads files individually", async () => {
    mocks.listFolder.mockResolvedValue({
      name: "pics",
      files: [{ relativePath: "a.jpg", localPath: "/local/pics/a.jpg", size: 5, mtimeMs: 1 }],
      emptyDirs: [],
    });
    mocks.askChoice.mockResolvedValueOnce("perFile");
    stubElementFromPoint(mounted!.container.querySelector("[data-file-drop-target]"));
    emitNativeDrop({ paths: [{ path: "/local/pics", dir: true }], x: 5, y: 5 });

    await waitFor(() =>
      expect(mocks.upload).toHaveBeenCalledWith(SID, "/local/pics/a.jpg", "~/pics/a.jpg", false, expect.any(String), expect.any(String)),
    );
    expect(mocks.listFolder).toHaveBeenCalledWith("/local/pics");
  });

  it("deduplicates HTML5 and native drops from the same source and uploads only once per drop", async () => {
    stubElementFromPoint(mounted!.container.querySelector("[data-file-drop-target]"));
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropOn(list, [new File(["x"], "e.txt")]);
    emitNativeDrop({ paths: [{ path: "/local/e.txt", dir: false }], x: 5, y: 5 });

    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    await flush();
    expect(mocks.upload).toHaveBeenCalledTimes(1);
    expect(mocks.upload).toHaveBeenCalledWith(SID, "/staged/e.txt", "~/e.txt", false, expect.any(String), expect.any(String));
  });

  it("ignores events when the drop point is outside the panel drop surface", async () => {
    const outside = document.createElement("div");
    document.body.appendChild(outside);
    stubElementFromPoint(outside);
    emitNativeDrop({ paths: [{ path: "/local/f.txt", dir: false }], x: 5, y: 5 });

    await flush();
    expect(mocks.upload).not.toHaveBeenCalled();
    expect(mocks.listFolder).not.toHaveBeenCalled();
  });

  it("ignores malformed payloads", async () => {
    stubElementFromPoint(mounted!.container.querySelector("[data-file-drop-target]"));
    emitNativeDrop({ taskId: "srv-1" });
    emitNativeDrop({ paths: [], x: 1, y: 1 });

    await flush();
    expect(mocks.upload).not.toHaveBeenCalled();
    expect(mocks.listFolder).not.toHaveBeenCalled();
  });

  it("queues native file drops in FileTree by their real paths", async () => {
    remountTree();
    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
    stubElementFromPoint(mounted!.container.querySelector("[data-file-drop-target]"));
    emitNativeDrop({ paths: [{ path: "/local/g.txt", dir: false }], x: 5, y: 5 });

    await waitFor(() =>
      expect(mocks.upload).toHaveBeenCalledWith(SID, "/local/g.txt", "~/g.txt", false, expect.any(String), expect.any(String)),
    );
  });
});
