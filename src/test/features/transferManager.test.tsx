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
  upload: vi.fn(),
  download: vi.fn(),
  packDownload: vi.fn(),
  extract: vi.fn(),
  termWrite: vi.fn(),
  toast: vi.fn(),
  listenEvent: vi.fn(),
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
      upload: mocks.upload,
      download: mocks.download,
      packDownload: mocks.packDownload,
      extract: mocks.extract,
    },
    terminalApi: { write: mocks.termWrite },
    sessionApi: {},
  };
});
vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: mocks.listenEvent,
    EVENTS: { fsProgress: "fs://progress" },
  };
});
vi.mock("../../ipc/webFiles", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/webFiles")>();
  return { ...actual, dropStaged: mocks.dropStaged, stageFile: mocks.stageFile };
});
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, i) => ({ index: i, start: i * 30, size: 30, key: i })),
    getTotalSize: () => count * 30,
  }),
}));

import { useUi } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";
import { FileBrowser } from "../../features/files/FileBrowser";
import { FileTree } from "../../features/files/FileTree";
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

const progressHandlers: ((e: unknown) => void)[] = [];

function emitProgress(event: unknown): void {
  act(() => {
    for (const handler of [...progressHandlers]) handler(event);
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
  mocks.listenEvent.mockImplementation((_t: string, cb: (e: unknown) => void) => {
    progressHandlers.push(cb);
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
    expect(panel!.textContent).toContain(LOCAL_SAVE);
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
    expect(mounted!.container.querySelector('[data-testid="transfer-panel"]')).not.toBeNull();
  });
});
