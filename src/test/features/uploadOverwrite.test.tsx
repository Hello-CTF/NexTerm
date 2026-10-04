/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  pickLocalFile: vi.fn(),
  discardStaged: vi.fn(),
  promptText: vi.fn(),
  pickSavePath: vi.fn(),
  finishSave: vi.fn(),
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
    pickLocalFile: mocks.pickLocalFile,
    discardStaged: mocks.discardStaged,
    promptText: mocks.promptText,
    pickSavePath: mocks.pickSavePath,
    finishSave: mocks.finishSave,
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

const SID = "s1";
const LOCAL_FILE = "/local/nginx-access.log";

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

let mounted: MountedView | undefined;

function remountBrowser(): void {
  mounted?.unmount();
  mounted = mountWithClient(createElement(FileBrowser, { sessionId: SID }));
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.ask.mockResolvedValue(true);
  mocks.promptText.mockResolvedValue(null);
  mocks.pickLocalFile.mockResolvedValue(LOCAL_FILE);
  mocks.discardStaged.mockResolvedValue(undefined);
  mocks.upload.mockResolvedValue(42_118);
  mocks.listenEvent.mockReturnValue(Promise.resolve(() => {}));
  mockHomeEntries([["a.txt", "file"], ["sub", "dir"]]);
  useUi.setState({
    pushToast: mocks.toast,
    leftOpen: true,
    leftWidth: 260,
    workspaces: [],
    activeWorkspaceId: null,
    sessions: [],
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("上传覆盖确认（FileBrowser）", () => {
  beforeEach(async () => {
    remountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("无同名文件时不询问，直接上传", async () => {
    click(buttonByTitle(mounted!.container, "上传"));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledWith(SID, LOCAL_FILE, "~/nginx-access.log", false));
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("~/nginx-access.log"));
  });

  it("同名文件时确认框点名远端路径；取消则不上传、远端保持原样", async () => {
    mockHomeEntries([["a.txt", "file"], ["nginx-access.log", "file"]]);
    remountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/nginx-access.log")).toBeTruthy());

    mocks.ask.mockResolvedValueOnce(false);
    click(buttonByTitle(mounted!.container, "上传"));
    await waitFor(() => expect(mocks.ask).toHaveBeenCalled());

    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("~/nginx-access.log"),
      expect.objectContaining({ kind: "warning", title: "上传覆盖确认" }),
    );
    expect(mocks.ask).toHaveBeenCalledWith(expect.stringContaining("保持原样"), expect.anything());
    expect(mocks.upload).not.toHaveBeenCalled();
    await waitFor(() => expect(mocks.discardStaged).toHaveBeenCalledWith(LOCAL_FILE));
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("保持原样"));
  });

  it("确认后按原路径覆盖上传", async () => {
    mockHomeEntries([["a.txt", "file"], ["nginx-access.log", "file"]]);
    remountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/nginx-access.log")).toBeTruthy());

    mocks.ask.mockResolvedValueOnce(true);
    click(buttonByTitle(mounted!.container, "上传"));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledWith(SID, LOCAL_FILE, "~/nginx-access.log", false));
  });

  it("同名目录同样拦截并标明是目录", async () => {
    mockHomeEntries([["a.txt", "file"], ["nginx-access.log", "dir"]]);
    remountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/nginx-access.log")).toBeTruthy());

    mocks.ask.mockResolvedValueOnce(false);
    click(buttonByTitle(mounted!.container, "上传"));
    await waitFor(() => expect(mocks.ask).toHaveBeenCalled());

    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("目录"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.upload).not.toHaveBeenCalled();
  });

  it("预检（stat）失败则中止上传，不盲目覆盖", async () => {
    mocks.list.mockImplementation((_s: string, p: string) =>
      p === "~"
        ? Promise.resolve([entry("a.txt", "file"), entry("sub", "dir")])
        : Promise.reject(new Error("stat boom")),
    );
    remountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/sub")).toBeTruthy());

    act(() => {
      rowByPath(mounted!.container, "~/sub").dispatchEvent(
        new MouseEvent("contextmenu", { bubbles: true, cancelable: true }),
      );
    });
    const item = [...mounted!.container.querySelectorAll('[role="menuitem"]')].find((b) =>
      b.textContent?.includes("上传到该目录"),
    );
    if (!item) throw new Error("Menu item not found: 上传到该目录");
    click(item);
    await waitFor(() => expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("stat boom")));

    expect(mocks.upload).not.toHaveBeenCalled();
    await waitFor(() => expect(mocks.discardStaged).toHaveBeenCalledWith(LOCAL_FILE));
  });
});

describe("上传覆盖确认（FileTree）", () => {
  function remountTree(): void {
    mounted?.unmount();
    mounted = mountWithClient(createElement(FileTree, { sessionId: SID }));
  }

  beforeEach(async () => {
    remountTree();
    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
  });

  it("无同名文件时不询问，直接上传", async () => {
    click(buttonByTitle(mounted!.container, "上传到当前目录"));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledWith(SID, LOCAL_FILE, "~/nginx-access.log", false));
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("同名文件取消则不上传，确认才覆盖", async () => {
    mockHomeEntries([["a.txt", "file"], ["nginx-access.log", "file"]]);
    remountTree();
    await waitFor(() => expect(mounted!.container.textContent).toContain("nginx-access.log"));

    mocks.ask.mockResolvedValueOnce(false);
    click(buttonByTitle(mounted!.container, "上传到当前目录"));
    await waitFor(() => expect(mocks.ask).toHaveBeenCalled());
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("~/nginx-access.log"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.upload).not.toHaveBeenCalled();
    await waitFor(() => expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("保持原样")));

    mocks.ask.mockResolvedValueOnce(true);
    click(buttonByTitle(mounted!.container, "上传到当前目录"));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledWith(SID, LOCAL_FILE, "~/nginx-access.log", false));
  });
});
