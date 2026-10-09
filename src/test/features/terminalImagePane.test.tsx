/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  return {
    flags: { web: false },
    listLive: vi.fn(),
    ask: vi.fn(),
    pickSavePath: vi.fn(),
    fsWrite: vi.fn(),
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
  fsApi: { write: mocks.fsWrite },
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

describe("TerminalPane 图片粘贴编排", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    harness.props = null;
    mocks.flags.web = false;
    mocks.listLive.mockResolvedValue([]);
    mocks.ask.mockResolvedValue(true);
    mocks.fsWrite.mockResolvedValue(undefined);
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("经文件通道写到本会话主机的 /tmp 并插入普通 Markdown 链接, 展示成功状态", async () => {
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
    expect(statusText()).toContain("已上传 1 张图片");
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("多张图片逐张落盘, 链接按顺序空格拼接", async () => {
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
    expect(statusText()).toContain("已上传 2 张图片");
  });

  it("上传失败时展示失败原因, 不插入任何内容", async () => {
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
});
