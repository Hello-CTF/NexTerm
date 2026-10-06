/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { clickButton, flush, mount, waitFor, type MountedView } from "./features/reactTestUtils";

const harness = vi.hoisted(() => ({
  handlers: new Map<string, (payload: unknown) => void>(),
  resyncSubs: new Set<() => void>(),
  xtermProps: null as Record<string, unknown> | null,
  closeTab: vi.fn(),
}));

vi.mock("../app/platform", () => ({
  isMac: () => false,
  modHint: () => "Ctrl",
  setMacPlatform: () => {},
  wailsDragRegionStyle: {},
  wailsNoDragRegionStyle: {},
  isWailsDragRegionTarget: () => false,
}));

vi.mock("../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  vaultApi: {},
  sessionApi: { connect: vi.fn(), reconnect: vi.fn(), disconnect: vi.fn() },
  terminalApi: {
    listLive: vi.fn().mockResolvedValue([]),
    write: vi.fn().mockResolvedValue(undefined),
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
    closeTab: harness.closeTab,
  },
}));

vi.mock("../ipc/webTransport", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/webTransport")>();
  return {
    ...actual,
    onEventsResync: (cb: () => void) => {
      harness.resyncSubs.add(cb);
      return () => harness.resyncSubs.delete(cb);
    },
  };
});

vi.mock("../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/events")>();
  return {
    ...actual,
    listenEvent: vi.fn((topic: string, handler: (payload: unknown) => void) => {
      harness.handlers.set(topic, handler);
      return Promise.resolve(() => {});
    }),
    EVENTS: { terminalControl: "terminal://control", terminalThrottled: "terminal://throttled" },
  };
});

vi.mock("../ipc/env", () => ({
  clientId: () => "me",
  TRANSPORT: "desktop",
  DEMO: false,
  WEB: false,
  DESKTOP: true,
}));

vi.mock("../ui/dialogs", () => ({
  describeTarget: () => "",
  finishSave: vi.fn(),
  pickSavePath: vi.fn(),
  promptText: vi.fn(),
  ask: vi.fn(),
}));

vi.mock("../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

vi.mock("../features/terminal/XtermView", async () => {
  const { useEffect } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
      harness.xtermProps = props;
      useEffect(() => {
        (props.onHandle as ((h: unknown) => void) | undefined)?.({
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
        (props.onAttach as ((id: string) => void) | undefined)?.("kernel-1");
      }, []);
      return <div className="h-full w-full min-h-0" />;
    },
  };
});

import { TerminalPane } from "../features/terminal/TerminalPane";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;

function mountPane(): MountedView {
  return mount(
    createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }),
  );
}

function alert(): HTMLElement | null {
  return mounted?.container.querySelector<HTMLElement>('[role="alert"]') ?? null;
}

function alertButton(text: string): HTMLButtonElement | undefined {
  return [...(alert()?.querySelectorAll("button") ?? [])].find(
    (candidate) => candidate.textContent?.trim() === text,
  );
}

function deliverControl(payload: Record<string, unknown>): void {
  const handler = harness.handlers.get("terminal://control");
  expect(handler).toBeDefined();
  act(() => {
    handler?.(payload);
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  harness.handlers.clear();
  harness.resyncSubs.clear();
  document.body.replaceChildren();
  harness.closeTab.mockResolvedValue(undefined);
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

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("terminal overlay accessibility", () => {
  it("announces the process-ended overlay and focuses its close action", async () => {
    mounted = mountPane();
    await act(async () => {
      await flush();
    });
    deliverControl({
      tabId: "kernel-1",
      version: 1,
      controller: "me",
      subscribers: 1,
      viewers: 1,
      exited: true,
      cols: 80,
      rows: 24,
      gridRevision: 0,
    });

    expect(alert()?.textContent).toContain("终端进程已结束");
    const close = alertButton("关闭标签");
    expect(close).toBeDefined();
    expect(document.activeElement).toBe(close);
  });

  it("closes the dead tab from the focused process-ended action", async () => {
    mounted = mountPane();
    await act(async () => {
      await flush();
    });
    deliverControl({
      tabId: "kernel-1",
      version: 1,
      controller: "me",
      subscribers: 1,
      viewers: 1,
      exited: true,
      cols: 80,
      rows: 24,
      gridRevision: 0,
    });
    expect(alertButton("关闭标签")).toBeDefined();

    clickButton(mounted.container, "关闭标签");

    await waitFor(() => expect(harness.closeTab).toHaveBeenCalledWith("kernel-1", undefined));
    const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
    expect(tabs.find((candidate) => candidate.id === "t1")).toBeUndefined();
  });

  it("announces the invalid-session overlay and focuses reconnect", async () => {
    mounted = mountPane();
    await act(async () => {
      await flush();
    });
    act(() => {
      (harness.xtermProps?.onAttachFailed as ((code: string) => void) | undefined)?.("not_found");
    });
    await act(async () => {
      await flush();
    });

    expect(alert()?.textContent).toContain("这个终端已失效");
    const reconnect = alertButton("重新连接这台主机");
    expect(reconnect).toBeDefined();
    expect(document.activeElement).toBe(reconnect);
  });
});
