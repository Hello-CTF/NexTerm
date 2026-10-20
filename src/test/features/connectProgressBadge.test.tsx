/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, type MountedView } from "./reactTestUtils";

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
  sessionApi: { connect: vi.fn(), reconnect: vi.fn(), disconnect: vi.fn(), list: vi.fn() },
  terminalApi: {
    listLive: vi.fn().mockResolvedValue([]),
    write: vi.fn().mockResolvedValue(undefined),
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
  },
}));

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: vi.fn(() => Promise.resolve(() => {})),
  };
});

vi.mock("../../ipc/env", () => ({
  clientId: () => "me",
  TRANSPORT: "desktop",
  WEB: false,
  DESKTOP: true,
}));

vi.mock("../../ui/dialogs", () => ({
  describeTarget: () => "",
  finishSave: vi.fn(),
  pickSavePath: vi.fn(),
  promptText: vi.fn(),
  ask: vi.fn(),
}));

vi.mock("../../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

vi.mock("../../features/terminal/XtermView", async () => {
  const { useEffect, useRef } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
      const onHandleRef = useRef(props.onHandle);
      const onAttachRef = useRef(props.onAttach);
      onHandleRef.current = props.onHandle;
      onAttachRef.current = props.onAttach;
      useEffect(() => {
        (onHandleRef.current as ((h: unknown) => void) | undefined)?.({
          copyBlock: () => "",
          scrollToBlock: () => {},
          navigateBlock: () => null,
          clearBlocks: () => {},
          clear: () => {},
          getSelection: () => "",
          focus: () => {},
          fit: () => {},
          dimensions: () => ({ cols: 80, rows: 24 }),
        });
        (onAttachRef.current as ((id: string) => void) | undefined)?.("kernel-1");
      }, []);
      return <div className="h-full w-full min-h-0" />;
    },
  };
});

import { TerminalPane } from "../../features/terminal/TerminalPane";
import { useUi } from "../../app/store";
import { sessionApi, type SessionInfo } from "../../ipc/commands";

let mounted: MountedView | undefined;

function badge(text: string): HTMLElement | null {
  return (
    [...(mounted?.container.querySelectorAll<HTMLElement>(".nx-badge") ?? [])].find((b) =>
      b.textContent?.includes(text),
    ) ?? null
  );
}

function seedSession(status: SessionInfo["status"]): void {
  useUi.setState({
    sessions: [
      { id: "s1", assetId: "a1", name: "web-01", kind: "ssh", status, tabs: [], createdAt: 0 },
    ],
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "ws-1",
        closable: true,
        sessionId: "s1",
        panes: [
          {
            id: "p1",
            activeTabId: "t1",
            tabs: [
              { id: "t1", kind: "terminal", title: "term", sessionId: "s1", tabId: "kernel-1", closable: true },
            ],
          },
        ],
        activePaneId: "p1",
        splitRatio: 0.5,
      },
    ],
    activeWorkspaceId: "ws1",
    connectProgress: {},
  });
}

beforeEach(async () => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  seedSession("reconnecting");
  vi.mocked(sessionApi.list).mockImplementation(async () => useUi.getState().sessions);
  mounted = mount(
    createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }),
  );
  await flush();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useUi.getState().setConnectProgress("a1", null);
});

describe("session://connect-progress badge", () => {
  it("shows the hop number and stage while reconnecting, with host and port in the badge title", async () => {
    await act(async () => {
      await flush();
    });
    expect(badge("重连中")?.textContent).toBe("重连中");

    act(() => {
      useUi.getState().setConnectProgress("a1", {
        assetId: "a1",
        stage: "handshake",
        hop: 1,
        hops: 2,
        host: "10.0.0.8",
        port: 22,
      });
    });
    const jumpBadge = badge("重连中");
    expect(jumpBadge?.textContent).toBe("重连中 · 跳板 1/2 · SSH 握手认证");
    expect(jumpBadge?.title).toBe("10.0.0.8:22");

    act(() => {
      useUi.getState().setConnectProgress("a1", {
        assetId: "a1",
        stage: "channel",
        hop: 0,
        hops: 2,
        host: "10.0.0.9",
        port: 2222,
      });
    });
    expect(badge("重连中")?.textContent).toBe("重连中 · 目标主机 · 经跳板打开通道");
  });

  it("shows the target-host stage while connecting", async () => {
    act(() => {
      seedSession("connecting");
    });
    await act(async () => {
      await flush();
    });
    act(() => {
      useUi.getState().setConnectProgress("a1", {
        assetId: "a1",
        stage: "dial",
        hop: 0,
        hops: 0,
        host: "10.0.0.8",
        port: 22,
      });
    });
    expect(badge("连接中")?.textContent).toBe("连接中 · 目标主机 · TCP 拨号");
  });

  it("returns to plain status text after cleanup and shows no leftover progress once connected", async () => {
    await act(async () => {
      await flush();
    });
    act(() => {
      useUi.getState().setConnectProgress("a1", {
        assetId: "a1",
        stage: "handshake",
        hop: 2,
        hops: 2,
        host: "10.0.0.8",
        port: 22,
      });
    });
    expect(badge("重连中")?.textContent).toBe("重连中 · 跳板 2/2 · SSH 握手认证");

    act(() => {
      useUi.getState().setConnectProgress("a1", null);
    });
    expect(badge("重连中")?.textContent).toBe("重连中");

    act(() => {
      useUi.getState().setConnectProgress("a1", {
        assetId: "a1",
        stage: "handshake",
        hop: 0,
        hops: 2,
        host: "10.0.0.9",
        port: 22,
      });
      seedSession("connected");
    });
    expect(badge("已连接")?.textContent).toBe("已连接");
  });

  it("does not let progress from other assets affect this session's badge", async () => {
    await act(async () => {
      await flush();
    });
    act(() => {
      useUi.getState().setConnectProgress("other-asset", {
        assetId: "other-asset",
        stage: "handshake",
        hop: 1,
        hops: 1,
        host: "10.0.0.7",
        port: 22,
      });
    });
    expect(badge("重连中")?.textContent).toBe("重连中");
  });
});
