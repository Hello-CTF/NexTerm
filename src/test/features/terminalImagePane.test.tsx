/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  class ImageUploadError extends Error {
    readonly status: number;
    readonly bodyText: string;
    constructor(status: number, bodyText: string) {
      super(`image upload failed (HTTP ${status})`);
      this.name = "ImageUploadError";
      this.status = status;
      this.bodyText = bodyText;
    }
  }
  return {
    ImageUploadError,
    flags: { web: false },
    listLive: vi.fn(),
    ask: vi.fn(),
    pickSavePath: vi.fn(),
    saveImage: vi.fn(),
    fetchImageService: vi.fn(),
    uploadImage: vi.fn(),
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
  sessionApi: { probeHostKey: vi.fn().mockResolvedValue({ state: "known" }) },
  terminalApi: {
    listLive: mocks.listLive,
    write: vi.fn().mockResolvedValue(undefined),
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
  },
  filesApi: { saveImage: mocks.saveImage },
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

vi.mock("../../ipc/webFiles", () => ({
  ImageUploadError: mocks.ImageUploadError,
  fetchImageService: mocks.fetchImageService,
  uploadImage: mocks.uploadImage,
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
  const { useEffect } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
      harness.props = props;
      useEffect(() => {
        (props.onHandle as ((handle: unknown) => void) | undefined)?.({
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
        (props.onAttach as ((id: string) => void) | undefined)?.("kernel-1");
      }, []);
      return <div data-testid="xterm-mock" />;
    },
  };
});

import { TerminalPane } from "../../features/terminal/TerminalPane";
import { useUi, type AppTab } from "../../app/store";

let mounted: MountedView | undefined;

function seedTab() {
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
        splitRatio: 0.5,
        closable: true,
      },
    ],
    activeWorkspaceId: "ws1",
    sessions: [
      {
        id: "s1",
        assetId: "asset-1",
        name: "web-01",
        kind: "ssh",
        status: "connected",
        tabs: [],
        createdAt: 0,
      },
    ],
    toasts: [],
  });
}

function png(name: string): File {
  return new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], name, { type: "image/png" });
}

async function pasteImages(files: File[]): Promise<void> {
  await act(async () => {
    (harness.props?.onPasteImages as ((files: File[]) => void) | undefined)?.(files);
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

function statusText(): string {
  const close = mounted?.container.querySelector('[aria-label="关闭图片粘贴提示"]');
  return close?.parentElement?.textContent ?? "";
}

function statusOverlay(): Element | null {
  return mounted?.container.querySelector('[aria-label="关闭图片粘贴提示"]') ?? null;
}

describe("TerminalPane 图片粘贴编排", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    harness.props = null;
    mocks.flags.web = false;
    mocks.listLive.mockResolvedValue([]);
    mocks.ask.mockResolvedValue(true);
    mocks.pickSavePath.mockResolvedValue("/tmp/saved shot.png");
    mocks.saveImage.mockResolvedValue({ path: "/tmp/saved shot.png", bytes: 4 });
    mocks.fetchImageService.mockResolvedValue({
      publicBaseURLConfigured: false,
      maxBytes: 20 << 20,
      ownerQuotaBytes: 200 << 20,
      ttlSeconds: 86400,
    });
    mocks.uploadImage.mockResolvedValue({
      id: "01J",
      url: "/files/image/01J",
      mime: "image/png",
      bytes: 4,
      created_at: 1,
      expires_at: 2,
    });
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("有图像服务时上传并插入 Markdown 链接, 展示成功状态", async () => {
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

    await waitFor(() => expect(mocks.paste).toHaveBeenCalledWith("![shot.png](/files/image/01J)"));
    expect(mocks.uploadImage).toHaveBeenCalledTimes(1);
    expect(statusText()).toContain("已插入 1 张图片的 Markdown 链接");
    expect(statusText()).toContain("限时公开链接");
  });

  it("上传失败时展示可操作错误, 不插入任何内容", async () => {
    seedTab();
    mocks.uploadImage.mockRejectedValue(new mocks.ImageUploadError(413, "image exceeds the maximum allowed size"));
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

    await waitFor(() => expect(statusText()).toContain("大小限制"));
    expect(mocks.paste).not.toHaveBeenCalled();
  });

  it("web 端无图像服务时报错且绝不静默上传", async () => {
    seedTab();
    mocks.flags.web = true;
    mocks.fetchImageService.mockResolvedValue(null);
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

    await waitFor(() => expect(statusText()).toContain("服务端未提供图片上传服务"));
    expect(mocks.uploadImage).not.toHaveBeenCalled();
    expect(mocks.saveImage).not.toHaveBeenCalled();
    expect(mocks.paste).not.toHaveBeenCalled();
  });

  it("桌面端无图像服务时经确认后本地保存并插入本地路径", async () => {
    seedTab();
    mocks.fetchImageService.mockResolvedValue(null);
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
      expect(mocks.paste).toHaveBeenCalledWith("![shot.png](</tmp/saved shot.png>)"),
    );
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.saveImage).toHaveBeenCalledWith("/tmp/saved shot.png", expect.any(String));
    expect(mocks.uploadImage).not.toHaveBeenCalled();
    expect(statusText()).toContain("未上传到任何服务器");
  });

  it("用户拒绝本地保存时不插入、不报错遗留", async () => {
    seedTab();
    mocks.fetchImageService.mockResolvedValue(null);
    mocks.ask.mockResolvedValue(false);
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

    await flush();
    expect(mocks.paste).not.toHaveBeenCalled();
    expect(mocks.saveImage).not.toHaveBeenCalled();
    expect(statusOverlay()).toBeNull();
  });

  it("本地保存失败时给出保存专属错误且不插入", async () => {
    seedTab();
    mocks.fetchImageService.mockResolvedValue(null);
    mocks.saveImage.mockRejectedValue({ code: "internal", message: "磁盘只读" });
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

    await waitFor(() => expect(statusText()).toContain("保存图片到本地失败"));
    expect(mocks.paste).not.toHaveBeenCalled();
  });
});
