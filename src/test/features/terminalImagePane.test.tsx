/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, flushMacrotask, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  return {
    flags: { web: false },
    listLive: vi.fn(),
    ask: vi.fn(),
    pickSavePath: vi.fn(),
    fsWrite: vi.fn(),
    fsMkdir: vi.fn(),
    uploadChunked: vi.fn(),
    paste: vi.fn(),
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
  assetApi: {},
  dbApi: {},
  vaultApi: {},
  sessionApi: {
    probeHostKey: vi.fn().mockResolvedValue({ state: "known" }),
    list: vi.fn().mockResolvedValue([]),
  },
  terminalApi: {
    listLive: mocks.listLive,
    write: vi.fn().mockResolvedValue(undefined),
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
  },
  fsApi: { write: mocks.fsWrite, mkdir: mocks.fsMkdir },
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

function mountPane(kind: "ssh" | "winrm" | "docker" = "ssh") {
  seedTab(kind);
  mounted = mount(
    createElement(TerminalPane, { sessionId: "s1", title: "web-01", storeTabId: "t1", resumeTabId: "kernel-1" }),
  );
}

function png(name: string): File {
  return new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], name, { type: "image/png" });
}

async function pasteImages(files: File[]): Promise<void> {
  await act(async () => {
    (harness.props?.onPasteFiles as ((files: File[]) => void) | undefined)?.(files);
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
    mocks.fsMkdir.mockResolvedValue(undefined);
    mocks.uploadChunked.mockResolvedValue(undefined);
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

    await waitFor(() =>
      expect(mocks.paste).toHaveBeenCalledWith(
        expect.stringMatching(/^\[shot\.png\]\(\/tmp\/nexterm-paste-\d+-1\.png\)$/),
      ),
    );
    expect(mocks.fsWrite).toHaveBeenCalledTimes(1);
    expect(mocks.fsWrite).toHaveBeenCalledWith(
      "s1",
      expect.stringMatching(/^\/tmp\/nexterm-paste-\d+-1\.png$/),
      "iVBORw==",
      false,
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

    await waitFor(() =>
      expect(mocks.paste).toHaveBeenCalledWith(
        expect.stringMatching(
          /^\[a\.png\]\(\/tmp\/nexterm-paste-\d+-1\.png\) \[b\.png\]\(\/tmp\/nexterm-paste-\d+-2\.png\)$/,
        ),
      ),
    );
    expect(mocks.fsWrite).toHaveBeenCalledTimes(2);
    expect(mocks.fsWrite).toHaveBeenNthCalledWith(
      2,
      "s1",
      expect.stringMatching(/^\/tmp\/nexterm-paste-\d+-2\.png$/),
      "iVBORw==",
      false,
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

    await waitFor(() => expect(statusText()).toContain("磁盘只读"));
    expect(statusText()).toContain("/tmp");
    expect(mocks.paste).not.toHaveBeenCalled();
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
    mocks.fsMkdir.mockResolvedValue(undefined);
    mocks.uploadChunked.mockResolvedValue(undefined);
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("uploads a non-image file into a per-paste directory under its own name and links it", async () => {
    mountPane();
    await flush();

    await pasteImages([new File(["hello"], "my report (v1).pdf", { type: "application/pdf" })]);

    await waitFor(() =>
      expect(mocks.paste).toHaveBeenCalledWith(
        expect.stringMatching(/^\[my report \(v1\)\.pdf\]\(<\/tmp\/nexterm-paste-\d+\/my report \(v1\)\.pdf>\)$/),
      ),
    );
    expect(mocks.fsMkdir).toHaveBeenCalledWith("s1", expect.stringMatching(/^\/tmp\/nexterm-paste-\d+$/));
    expect(mocks.fsWrite).toHaveBeenCalledWith(
      "s1",
      expect.stringMatching(/^\/tmp\/nexterm-paste-\d+\/my report \(v1\)\.pdf$/),
      "aGVsbG8=",
      false,
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

    await waitFor(() => expect(mocks.paste).toHaveBeenCalled());
    const text = mocks.paste.mock.calls[0]?.[0] as string;
    expect(text).toMatch(/\[shot\.png\]\(\/tmp\/nexterm-paste-\d+-1\.png\)/);
    const paths = mocks.fsWrite.mock.calls.map((c) => c[1] as string);
    expect(paths).toHaveLength(4);
    expect(new Set(paths).size).toBe(4);
    for (const path of paths.slice(1)) {
      expect(path).toMatch(/^\/tmp\/nexterm-paste-\d+\/[^/]+$/);
    }
    expect(paths.some((p) => p.endsWith("/_a.txt"))).toBe(true);
    expect(paths.some((p) => p.endsWith("/_a-2.txt"))).toBe(true);
    expect(paths.some((p) => p.endsWith("/env"))).toBe(true);
    expect(mocks.fsMkdir).toHaveBeenCalledTimes(1);
    expect(statusText()).toContain("已上传 4 个文件");
  });

  it("uses the chunked upload for files larger than one chunk", async () => {
    mountPane();
    await flush();

    await pasteImages([sized("big.iso", 5 * 1024 * 1024)]);

    await waitFor(() => expect(mocks.uploadChunked).toHaveBeenCalledTimes(1));
    expect(mocks.uploadChunked.mock.calls[0]?.[0]).toMatchObject({
      sessionId: "s1",
      remotePath: expect.stringMatching(/^\/tmp\/nexterm-paste-\d+\/big\.iso$/),
    });
    expect(mocks.fsWrite).not.toHaveBeenCalled();
    await waitFor(() => expect(mocks.paste).toHaveBeenCalled());
  });

  it("skips files over 20 MB with a visible message and still uploads the rest", async () => {
    mountPane();
    await flush();

    await pasteImages([sized("huge.bin", 20 * 1024 * 1024 + 1), new File(["ok"], "ok.txt")]);

    await waitFor(() => expect(mocks.paste).toHaveBeenCalledTimes(1));
    expect(mocks.paste.mock.calls[0]?.[0]).toContain("ok.txt");
    expect(mocks.paste.mock.calls[0]?.[0]).not.toContain("huge.bin");
    expect(statusText()).toContain("已跳过超过 20 MB 的文件：huge.bin");
    expect(useUi.getState().toasts.at(-1)?.text).toContain("huge.bin");
  });

  it("uploads a file of exactly 20 MB", async () => {
    mountPane();
    await flush();

    await pasteImages([sized("edge.bin", 20 * 1024 * 1024)]);

    await waitFor(() => expect(mocks.uploadChunked).toHaveBeenCalledTimes(1));
  });

  it("reports an error and uploads nothing when every file is too large", async () => {
    mountPane();
    await flush();

    await pasteImages([sized("huge.bin", 21 * 1024 * 1024)]);

    await waitFor(() => expect(statusText()).toContain("huge.bin"));
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
});
