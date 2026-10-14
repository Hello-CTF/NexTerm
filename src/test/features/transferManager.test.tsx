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
  list: vi.fn(),
  mkdir: vi.fn(),
  upload: vi.fn(),
  download: vi.fn(),
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
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    fsApi: {
      list: mocks.list,
      mkdir: mocks.mkdir,
      upload: mocks.upload,
      download: mocks.download,
      packDownload: mocks.packDownload,
      extract: mocks.extract,
    },
    localFileApi: { listFolder: mocks.listFolder },
    terminalApi: { write: mocks.termWrite },
    sessionApi: {},
  };
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
  return { ...actual, dropStaged: mocks.dropStaged, stageFile: mocks.stageFile };
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
import { FileBrowser } from "../../features/files/FileBrowser";
import { FileTree } from "../../features/files/FileTree";
import { resetNativeDropDedupeForTest } from "../../features/files/nativeDrop";
import { useTransferStore } from "../../features/files/transferStore";

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

let mounted: MountedView | undefined;

function remountBrowser(): void {
  mounted?.unmount();
  mounted = mountWithClient(createElement(FileBrowser, { sessionId: SID }));
}

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
  mocks.upload.mockResolvedValue(100);
  mocks.download.mockResolvedValue(100);
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
  useTransferStore.setState({ tasks: [], runningId: null });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("统一传输队列面板（FileBrowser）", () => {
  beforeEach(async () => {
    remountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("上传进入队列：显示方向、远端路径与进行中状态，完成后给出可懂提示", async () => {
    let resolveUpload!: (v: number) => void;
    mocks.upload.mockReturnValueOnce(new Promise<number>((r) => (resolveUpload = r)));

    click(buttonByTitle(mounted!.container, "上传"));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledWith(SID, LOCAL_FILE, "~/nginx-access.log", false));

    const panel = mounted!.container.querySelector('[data-testid="transfer-panel"]');
    expect(panel).not.toBeNull();
    expect(panel!.textContent).toContain("~/nginx-access.log");
    expect(panel!.textContent).toContain(LOCAL_FILE);
    expect((panel!.textContent ?? "").indexOf(LOCAL_FILE)).toBeLessThan(
      (panel!.textContent ?? "").indexOf("~/nginx-access.log"),
    );
    expect(panel!.textContent).toContain("进行中");

    resolveUpload(42_118);
    await waitFor(() => expect(panelRows(mounted!.container)[0]?.textContent).toContain("已完成"));
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("~/nginx-access.log"));
    const successText = mocks.toast.mock.calls.find((c) => c[0] === "success")?.[1] ?? "";
    expect(successText).not.toMatch(/task/i);
  });

  it("进度事件实时更新面板百分比", async () => {
    let resolveUpload!: (v: number) => void;
    mocks.upload.mockReturnValueOnce(new Promise<number>((r) => (resolveUpload = r)));

    click(buttonByTitle(mounted!.container, "上传"));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalled());

    emitProgress({ taskId: "srv-1", transferred: 50, total: 100, done: false });
    await waitFor(() => expect(panelRows(mounted!.container)[0]?.textContent).toContain("50%"));

    emitProgress({ taskId: "srv-1", transferred: 100, total: 100, done: true });
    resolveUpload(100);
    await waitFor(() => expect(panelRows(mounted!.container)[0]?.textContent).toContain("已完成"));
  });

  it("失败任务在面板显示错误，toast 说明路径与原因，不暴露内部 taskId", async () => {
    mocks.upload.mockRejectedValueOnce(new Error("connection reset"));

    click(buttonByTitle(mounted!.container, "上传"));
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

  it("下载进入队列：显示本地落点，完成后交付并提示", async () => {
    let resolveDownload!: (v: number) => void;
    mocks.download.mockReturnValueOnce(new Promise<number>((r) => (resolveDownload = r)));

    click(rowByPath(mounted!.container, "~/a.txt"));
    click(buttonByTitle(mounted!.container, "下载"));
    await waitFor(() => expect(mocks.download).toHaveBeenCalledWith(SID, "~/a.txt", LOCAL_SAVE));

    const panel = mounted!.container.querySelector('[data-testid="transfer-panel"]');
    expect(panel!.textContent).toContain("~/a.txt");
    expect(panel!.textContent).toContain(LOCAL_SAVE);
    expect((panel!.textContent ?? "").indexOf("~/a.txt")).toBeLessThan(
      (panel!.textContent ?? "").indexOf(LOCAL_SAVE),
    );
    expect(panel!.textContent).toContain("进行中");

    resolveDownload(10);
    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining(LOCAL_SAVE)),
    );
    expect(mocks.finishSave).toHaveBeenCalledWith(LOCAL_SAVE, "a.txt");
    await waitFor(() => expect(panelRows(mounted!.container)[0]?.textContent).toContain("已完成"));
  });

  it("排队中的任务可取消；运行中的任务不受影响", async () => {
    let resolveFirst!: (v: number) => void;
    mocks.upload
      .mockReturnValueOnce(new Promise<number>((r) => (resolveFirst = r)))
      .mockResolvedValueOnce(100);

    click(buttonByTitle(mounted!.container, "上传"));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));

    mocks.pickLocalFile.mockResolvedValueOnce("/local/second.txt");
    click(buttonByTitle(mounted!.container, "上传"));
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

  it("取消排队中的上传会清理本地暂存文件，运行中的任务不受影响", async () => {
    mocks.upload.mockReset();
    mocks.upload.mockResolvedValue(100);
    let resolveFirst!: (v: number) => void;
    mocks.upload.mockReturnValueOnce(new Promise<number>((r) => (resolveFirst = r)));

    click(buttonByTitle(mounted!.container, "上传"));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));

    mocks.pickLocalFile.mockResolvedValueOnce("/local/staged-second.txt");
    click(buttonByTitle(mounted!.container, "上传"));
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

  it("进度条具备完整的 aria 数值属性", async () => {
    mocks.upload.mockReset();
    mocks.upload.mockResolvedValue(100);
    let resolveUpload!: (v: number) => void;
    mocks.upload.mockReturnValueOnce(new Promise<number>((r) => (resolveUpload = r)));

    click(buttonByTitle(mounted!.container, "上传"));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalled());

    const bar = mounted!.container.querySelector('[role="progressbar"]');
    expect(bar?.getAttribute("aria-valuemin")).toBe("0");
    expect(bar?.getAttribute("aria-valuemax")).toBe("100");
    expect(bar?.getAttribute("aria-valuenow")).toBe("0");

    emitProgress({ taskId: "srv-1", transferred: 50, total: 100, done: false });
    await waitFor(() =>
      expect(
        mounted!.container.querySelector('[role="progressbar"]')?.getAttribute("aria-valuenow"),
      ).toBe("50"),
    );

    resolveUpload(100);
    await flush();
  });

  it("清除已结束后面板收起", async () => {
    click(buttonByTitle(mounted!.container, "上传"));
    await waitFor(() => expect(panelRows(mounted!.container)[0]?.textContent).toContain("已完成"));

    click(buttonByTitle(mounted!.container, "清除已结束"));
    await waitFor(() =>
      expect(mounted!.container.querySelector('[data-testid="transfer-panel"]')).toBeNull(),
    );
  });
});

describe("拖拽上传（FileBrowser）", () => {
  beforeEach(async () => {
    remountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("拖入文件进入当前目录并进入队列", async () => {
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropOn(list, [new File(["x"], "d.txt")]);

    await waitFor(() =>
      expect(mocks.upload).toHaveBeenCalledWith(SID, expect.anything(), "~/d.txt", false),
    );
    const localPath = mocks.upload.mock.calls[0]?.[1] as string;
    expect(localPath).toContain("d.txt");
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("拖到目录行上则上传到该目录", async () => {
    dropOn(rowByPath(mounted!.container, "~/sub"), [new File(["x"], "d.txt")]);
    await waitFor(() =>
      expect(mocks.upload).toHaveBeenCalledWith(SID, expect.anything(), "~/sub/d.txt", false),
    );
  });

  it("冲突时选择保留两者：自动重命名后上传", async () => {
    mockHomeEntries([["a.txt", "file"], ["d.txt", "file"]]);
    remountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/d.txt")).toBeTruthy());

    mocks.askChoice.mockResolvedValueOnce("keepBoth");
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropOn(list, [new File(["x"], "d.txt")]);

    await waitFor(() =>
      expect(mocks.upload).toHaveBeenCalledWith(SID, expect.anything(), "~/d (1).txt", false),
    );
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.askChoice).toHaveBeenCalledWith(
      expect.stringContaining("d.txt"),
      expect.objectContaining({ title: "上传冲突" }),
    );
  });

  it("逐冲突询问：覆盖走原路径，保留两者自动重命名", async () => {
    mockHomeEntries([
      ["a.txt", "file"],
      ["d.txt", "file"],
      ["e.txt", "file"],
    ]);
    remountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/e.txt")).toBeTruthy());

    mocks.askChoice.mockResolvedValueOnce("overwrite").mockResolvedValueOnce("keepBoth");
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropOn(list, [new File(["1"], "d.txt"), new File(["2"], "e.txt")]);

    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));
    expect(mocks.upload).toHaveBeenNthCalledWith(1, SID, expect.anything(), "~/d.txt", false);
    expect(mocks.upload).toHaveBeenNthCalledWith(2, SID, expect.anything(), "~/e (1).txt", false);
    expect(mocks.askChoice).toHaveBeenCalledTimes(2);
  });

  it("同批大小写变体重名（winrm）：自动保留两者，不弹冲突也不报错", async () => {
    useUi.setState({ sessions: [sessionOfKind("winrm")] });
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropOn(list, [new File(["1"], "x.txt"), new File(["2"], "X.TXT")]);

    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));
    expect(mocks.upload).toHaveBeenNthCalledWith(1, SID, expect.anything(), "~/x.txt", false);
    expect(mocks.upload).toHaveBeenNthCalledWith(2, SID, expect.anything(), "~/X (1).TXT", false);
    expect(mocks.askChoice).not.toHaveBeenCalled();
    expect(mocks.toast).not.toHaveBeenCalledWith("error", expect.anything());
  });

  it("同批完全重名（ssh）：自动保留两者，不弹冲突也不报错", async () => {
    useUi.setState({ sessions: [sessionOfKind("ssh")] });
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropOn(list, [new File(["1"], "x.txt"), new File(["2"], "x.txt")]);

    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));
    expect(mocks.upload).toHaveBeenNthCalledWith(1, SID, expect.anything(), "~/x.txt", false);
    expect(mocks.upload).toHaveBeenNthCalledWith(2, SID, expect.anything(), "~/x (1).txt", false);
    expect(mocks.askChoice).not.toHaveBeenCalled();
    expect(mocks.toast).not.toHaveBeenCalledWith("error", expect.anything());
  });

  it("策略可应用到本次队列：skip-all 只问一次，其余冲突同样跳过", async () => {
    mockHomeEntries([
      ["a.txt", "file"],
      ["d.txt", "file"],
      ["e.txt", "file"],
    ]);
    remountBrowser();
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

  it("冲突对话框取消则整批不上传，远端保持原样", async () => {
    mockHomeEntries([["a.txt", "file"], ["d.txt", "file"]]);
    remountBrowser();
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

describe("拖拽上传（FileTree）", () => {
  beforeEach(async () => {
    remountTree();
    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
  });

  it("拖入文件上传到当前目录并进入队列", async () => {
    const tree = mounted!.container.querySelector("[role='tree']")!;
    dropOn(tree, [new File(["x"], "d.txt")]);

    await waitFor(() =>
      expect(mocks.upload).toHaveBeenCalledWith(SID, expect.anything(), "~/d.txt", false),
    );
    expect(mounted!.container.querySelector('[data-testid="transfer-panel"]')).toBeNull();
    expect(
      useTransferStore.getState().tasks.some((t) => t.remotePath === "~/d.txt"),
    ).toBe(true);
  });
});

describe("文件夹上传（FileBrowser）", () => {
  beforeEach(async () => {
    remountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("工具栏提供上传文件夹入口", () => {
    expect(buttonByTitle(mounted!.container, "上传本地文件夹到当前目录")).toBeTruthy();
  });

  it("逐文件上传（默认）：遍历文件夹按相对路径入队，WEB 暂存与相对路径一一对应", async () => {
    let seq = 0;
    mocks.stageFile.mockImplementation(async (f: File) => {
      seq += 1;
      return { id: `st-${seq}`, path: `/staged/${seq}-${f.name}` };
    });
    mocks.askChoice.mockResolvedValueOnce("perFile");
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropFoldersOn(list, [fakeDir("pics", [fakeFile("a.jpg"), fakeDir("sub", [fakeFile("a.jpg")])])]);

    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));
    expect(mocks.upload).toHaveBeenNthCalledWith(1, SID, "/staged/1-a.jpg", "~/pics/a.jpg", false);
    expect(mocks.upload).toHaveBeenNthCalledWith(
      2,
      SID,
      "/staged/2-a.jpg",
      "~/pics/sub/a.jpg",
      false,
    );
    expect(mocks.askChoice).toHaveBeenCalledTimes(1);
    expect(mocks.askChoice).toHaveBeenCalledWith(
      expect.stringContaining("「pics」"),
      expect.objectContaining({ title: "上传文件夹" }),
    );
    expect(mocks.askChoice.mock.calls[0]?.[0]).toContain("共 2 个文件");
    expect(modeDialogChoices()).toEqual(["perFile"]);
  });

  it("winrm 会话不提供打包上传选项", async () => {
    useUi.setState({ sessions: [sessionOfKind("winrm")] });
    mocks.askChoice.mockResolvedValueOnce("perFile");
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropFoldersOn(list, [fakeDir("pics", [fakeFile("a.jpg")])]);

    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    expect(modeDialogChoices()).toEqual(["perFile"]);
    expect(mocks.packUpload).not.toHaveBeenCalled();
  });

  it("winrm 会话拒绝名称含反斜杠的条目：报错、清理暂存、不上传", async () => {
    useUi.setState({ sessions: [sessionOfKind("winrm")] });
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

  it("ssh 会话可选择打包上传：调用 packUploadAndExtract 并清理暂存", async () => {
    useUi.setState({ sessions: [sessionOfKind("ssh")] });
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

  it("取消方式对话框则整批不上传并清理暂存", async () => {
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

  it("空文件夹不弹方式对话框，直接创建远端目录", async () => {
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

  it("目标已有同名文件夹：选择跳过同名文件后只上传不冲突的文件", async () => {
    mocks.list.mockImplementation((_s: string, p: string) => {
      if (p === "~")
        return Promise.resolve([entry("a.txt", "file"), entry("pics", "dir")]);
      if (p === "~/pics")
        return Promise.resolve([{ ...entry("a.jpg", "file"), path: "~/pics/a.jpg" }]);
      return Promise.resolve([]);
    });
    remountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/pics")).toBeTruthy());

    mocks.askChoice.mockResolvedValueOnce("perFile").mockResolvedValueOnce("skip");
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropFoldersOn(list, [fakeDir("pics", [fakeFile("a.jpg"), fakeDir("sub", [fakeFile("b.jpg")])])]);

    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    expect(mocks.upload).toHaveBeenCalledWith(SID, "/staged/b.jpg", "~/pics/sub/b.jpg", false);
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

describe("文件夹上传（FileTree）", () => {
  beforeEach(async () => {
    remountTree();
    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
  });

  it("工具栏提供上传文件夹入口", () => {
    expect(buttonByTitle(mounted!.container, "上传文件夹到当前目录")).toBeTruthy();
  });

  it("拖入文件夹遍历后按相对路径上传到当前目录", async () => {
    mocks.askChoice.mockResolvedValueOnce("perFile");
    const tree = mounted!.container.querySelector("[role='tree']")!;
    dropFoldersOn(tree, [fakeDir("pics", [fakeFile("a.jpg"), fakeDir("sub", [fakeFile("b.jpg")])])]);

    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));
    expect(mocks.upload).toHaveBeenNthCalledWith(1, SID, "/staged/a.jpg", "~/pics/a.jpg", false);
    expect(mocks.upload).toHaveBeenNthCalledWith(2, SID, "/staged/b.jpg", "~/pics/sub/b.jpg", false);
  });
});

describe("原生拖拽上传（wails native drop）", () => {
  beforeEach(async () => {
    remountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("FileBrowser 与 FileTree 的放置面都带 data-file-drop-target", async () => {
    expect(mounted!.container.querySelector("[data-file-drop-target]")).not.toBeNull();
    remountTree();
    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mounted!.container.querySelector("[data-file-drop-target]")).not.toBeNull();
  });

  it("原生文件拖放按真实路径入队上传到当前目录", async () => {
    stubElementFromPoint(mounted!.container.querySelector("[data-file-drop-target]"));
    emitNativeDrop({ paths: [{ path: "/local/d.txt", dir: false }], x: 5, y: 5 });

    await waitFor(() =>
      expect(mocks.upload).toHaveBeenCalledWith(SID, "/local/d.txt", "~/d.txt", false),
    );
    expect(useTransferStore.getState().tasks.some((t) => t.remotePath === "~/d.txt")).toBe(true);
    expect(mocks.listFolder).not.toHaveBeenCalled();
  });

  it("原生拖放落到目录行时上传进该行目录", async () => {
    const row = mounted!.container.querySelector("[data-drop-path]")!;
    expect(row.getAttribute("data-drop-path")).toBe("~/sub");
    stubElementFromPoint(row);
    emitNativeDrop({ paths: [{ path: "/local/d.txt", dir: false }], x: 5, y: 5 });

    await waitFor(() =>
      expect(mocks.upload).toHaveBeenCalledWith(SID, "/local/d.txt", "~/sub/d.txt", false),
    );
  });

  it("原生文件夹拖放走 local_folder_list 枚举后逐文件上传", async () => {
    mocks.listFolder.mockResolvedValue({
      name: "pics",
      files: [{ relativePath: "a.jpg", localPath: "/local/pics/a.jpg", size: 5, mtimeMs: 1 }],
      emptyDirs: [],
    });
    mocks.askChoice.mockResolvedValueOnce("perFile");
    stubElementFromPoint(mounted!.container.querySelector("[data-file-drop-target]"));
    emitNativeDrop({ paths: [{ path: "/local/pics", dir: true }], x: 5, y: 5 });

    await waitFor(() =>
      expect(mocks.upload).toHaveBeenCalledWith(SID, "/local/pics/a.jpg", "~/pics/a.jpg", false),
    );
    expect(mocks.listFolder).toHaveBeenCalledWith("/local/pics");
  });

  it("HTML5 拖放与原生事件同源到达时去重，一次放置只上传一次", async () => {
    stubElementFromPoint(mounted!.container.querySelector("[data-file-drop-target]"));
    const list = mounted!.container.querySelector("[role='tree']")!;
    dropOn(list, [new File(["x"], "e.txt")]);
    emitNativeDrop({ paths: [{ path: "/local/e.txt", dir: false }], x: 5, y: 5 });

    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    await flush();
    expect(mocks.upload).toHaveBeenCalledTimes(1);
    expect(mocks.upload).toHaveBeenCalledWith(SID, "/staged/e.txt", "~/e.txt", false);
  });

  it("放置点在面板放置面之外时忽略事件", async () => {
    const outside = document.createElement("div");
    document.body.appendChild(outside);
    stubElementFromPoint(outside);
    emitNativeDrop({ paths: [{ path: "/local/f.txt", dir: false }], x: 5, y: 5 });

    await flush();
    expect(mocks.upload).not.toHaveBeenCalled();
    expect(mocks.listFolder).not.toHaveBeenCalled();
  });

  it("畸形负载被忽略", async () => {
    stubElementFromPoint(mounted!.container.querySelector("[data-file-drop-target]"));
    emitNativeDrop({ taskId: "srv-1" });
    emitNativeDrop({ paths: [], x: 1, y: 1 });

    await flush();
    expect(mocks.upload).not.toHaveBeenCalled();
    expect(mocks.listFolder).not.toHaveBeenCalled();
  });

  it("FileTree 原生文件拖放按真实路径入队上传", async () => {
    remountTree();
    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
    stubElementFromPoint(mounted!.container.querySelector("[data-file-drop-target]"));
    emitNativeDrop({ paths: [{ path: "/local/g.txt", dir: false }], x: 5, y: 5 });

    await waitFor(() =>
      expect(mocks.upload).toHaveBeenCalledWith(SID, "/local/g.txt", "~/g.txt", false),
    );
  });
});
