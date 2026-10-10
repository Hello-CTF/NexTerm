/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const harness = vi.hoisted(() => ({
  listAssets: vi.fn(),
  listLive: vi.fn(),
  switchEncoding: vi.fn(),
  attachInfo: null as
    | ((info: {
        tabId: string;
        controller: string | null;
        subscribers: number;
        viewers: number;
        exited: boolean;
        encoding?: string;
      }) => void)
    | null,
}));

vi.mock("../../app/platform", () => ({
  isMac: () => false,
  modHint: () => "Ctrl",
  setMacPlatform: () => {},
  wailsDragRegionStyle: {},
  wailsNoDragRegionStyle: {},
  isWailsDragRegionTarget: () => false,
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: { list: harness.listAssets },
  dbApi: {},
  vaultApi: {},
  sessionApi: { connect: vi.fn(), reconnect: vi.fn(), disconnect: vi.fn() },
  terminalApi: {
    listLive: harness.listLive,
    write: vi.fn().mockResolvedValue(undefined),
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
    switchEncoding: harness.switchEncoding,
    claim: vi.fn().mockResolvedValue(null),
    exportLog: vi.fn().mockResolvedValue(0),
  },
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

vi.mock("../../ipc/env", () => ({ clientId: () => "me", WEB: false }));

vi.mock("../../ui/dialogs", () => ({
  describeTarget: () => "",
  finishSave: vi.fn(),
  pickSavePath: vi.fn(),
  promptText: vi.fn(),
  ask: vi.fn(),
  askChoice: vi.fn(),
}));

vi.mock("../../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

vi.mock("../../features/terminal/XtermView", async () => {
  const { useEffect } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
      harness.attachInfo = props.onAttachInfo as typeof harness.attachInfo;
      useEffect(() => {
        (props.onAttach as ((id: string) => void) | undefined)?.("kernel-1");
      }, []);
      return <div className="h-full w-full min-h-0" />;
    },
  };
});

import { TerminalPane } from "../../features/terminal/TerminalPane";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;

function badgeText(): string | null {
  const badge = [...document.querySelectorAll(".nx-badge-amber")].find((el) =>
    ["gbk", "big5", "gb18030", "latin1"].includes(el.textContent?.trim() ?? ""),
  );
  return badge?.textContent?.trim() ?? null;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  harness.attachInfo = null;
  harness.listLive.mockResolvedValue([]);
  harness.switchEncoding.mockResolvedValue(undefined);
  harness.listAssets.mockResolvedValue([
    { id: "a1", kind: "ssh", name: "web-01", options: { encoding: "gbk" } },
  ]);
  useUi.setState({
    sessions: [
      {
        id: "s1",
        assetId: "a1",
        name: "web-01",
        kind: "ssh",
        status: "connected",
        tabs: [],
        createdAt: 0,
      },
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
              {
                id: "t1",
                kind: "terminal",
                title: "term",
                sessionId: "s1",
                tabId: "kernel-1",
                closable: true,
              },
            ],
          },
        ],
        activePaneId: "p1",
        splitRatio: 0.5,
      },
    ],
    activeWorkspaceId: "ws1",
  });
});

function mountPane(): void {
  mounted = mount(
    createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }),
  );
}

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("terminal encoding badge", () => {
  it("shows the asset-configured encoding for a fresh terminal", async () => {
    mountPane();
    await flush();
    await waitFor(() => expect(badgeText()).toBe("gbk"));
  });

  it("shows no badge for utf-8 assets", async () => {
    harness.listAssets.mockResolvedValue([
      { id: "a1", kind: "ssh", name: "web-01", options: {} },
    ]);
    mountPane();
    await flush();
    expect(badgeText()).toBeNull();
  });

  it("follows the actual tab encoding reported on attach", async () => {
    harness.listAssets.mockResolvedValue([
      { id: "a1", kind: "ssh", name: "web-01", options: {} },
    ]);
    mountPane();
    await flush();
    expect(badgeText()).toBeNull();
    act(() => {
      harness.attachInfo?.({
        tabId: "kernel-1",
        controller: "me",
        subscribers: 1,
        viewers: 1,
        exited: false,
        encoding: "big5",
      });
    });
    await flush();
    expect(badgeText()).toBe("big5");
  });
});
