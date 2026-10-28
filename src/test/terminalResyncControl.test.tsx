/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, mount, deferred, waitForRendered, type MountedView } from "./features/reactTestUtils";
import type { LiveTabInfo } from "../ipc/commands";

const harness = vi.hoisted(() => ({
  handlers: new Map<string, (payload: unknown) => void>(),
  resyncSubs: new Set<() => void>(),
  xtermProps: null as Record<string, unknown> | null,
  listLive: vi.fn(),
  sessionList: vi.fn(),
  sessionReconnect: vi.fn(),
  probeHostKey: vi.fn(),
  recordingStatus: vi.fn(),
  recordStart: vi.fn(),
  recordStop: vi.fn(),
  copyText: vi.fn(),
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
  sessionApi: {
    connect: vi.fn(),
    reconnect: harness.sessionReconnect,
    disconnect: vi.fn(),
    list: harness.sessionList,
    probeHostKey: harness.probeHostKey,
  },
  terminalApi: {
    listLive: harness.listLive,
    write: vi.fn().mockResolvedValue(undefined),
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
    recordingStatus: harness.recordingStatus,
    recordStart: harness.recordStart,
    recordStop: harness.recordStop,
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

vi.mock("../ui/clipboard", () => ({
  copyText: harness.copyText,
  readText: vi.fn(),
}));

vi.mock("../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

vi.mock("../features/terminal/XtermView", async () => {
  const { useEffect, useRef } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
      harness.xtermProps = props;
      const attachFiredRef = useRef(false);
      useEffect(() => {
        if (attachFiredRef.current) return;
        attachFiredRef.current = true;
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
      }, [props.onAttach, props.onHandle]);
      return <div className="h-full w-full min-h-0" />;
    },
  };
});

import { TerminalPane } from "../features/terminal/TerminalPane";
import { useUi } from "../app/store";
import { pickSavePath } from "../ui/dialogs";

let mounted: MountedView | undefined;

function liveTab(patch: Partial<LiveTabInfo> = {}): LiveTabInfo {
  return {
    tabId: "kernel-1",
    sessionId: "s1",
    sessionName: "web-01",
    sessionKind: "ssh",
    cols: 80,
    rows: 24,
    sizeOwner: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
    lastOutputMsAgo: 0,
    ...patch,
  };
}

function overlayText(text: string): HTMLElement | null {
  return (
    [...(mounted?.container.querySelectorAll<HTMLElement>("span") ?? [])].find(
      (el) => el.textContent?.trim() === text,
    ) ?? null
  );
}

function mountPane(): MountedView {
  return mount(
    createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }),
  );
}

function recording(patch: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    tabId: "kernel-1",
    state: "running",
    bytes: 12,
    path: "/tmp/recording.log",
    generation: 1,
    ...patch,
  };
}

function clickTerminalMenuItem(label: string): void {
  const overflow = mounted?.container.querySelector<HTMLButtonElement>(
    'button[aria-label="更多终端操作"]',
  );
  if (!overflow) throw new Error("toolbar overflow button not found");
  click(overflow);
  const item = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((candidate) =>
    candidate.textContent?.includes(label),
  );
  if (!item) throw new Error(`menu item not found: ${label}`);
  click(item);
}

function fireResync(): void {
  act(() => {
    for (const cb of [...harness.resyncSubs]) cb();
  });
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
  harness.listLive.mockResolvedValue([]);
  harness.sessionList.mockImplementation(() => Promise.resolve(useUi.getState().sessions));
  harness.sessionReconnect.mockResolvedValue(true);
  harness.probeHostKey.mockResolvedValue({ state: "known" });
  harness.recordingStatus.mockResolvedValue({
    tabId: "kernel-1",
    state: "never-started",
    bytes: 0,
    generation: 0,
  });
  harness.recordStart.mockResolvedValue(undefined);
  harness.recordStop.mockResolvedValue(0);
  harness.copyText.mockResolvedValue(undefined);
  vi.mocked(pickSavePath).mockResolvedValue(null);
  useUi.setState({
    recordings: {},
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
      },
    ],
    activeWorkspaceId: "ws1",
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("terminal control resync compensation", () => {
  it("re-fetches live control state through listLive when events resync", async () => {
    harness.listLive
      .mockResolvedValueOnce([liveTab()])
      .mockResolvedValue([liveTab({ sizeOwner: "other", subscribers: 2, viewers: 2 })]);
    mounted = mountPane();
    await act(async () => {
      await flush();
    });
    expect(harness.listLive).toHaveBeenCalledTimes(1);
    expect(harness.xtermProps?.canResize).toBe(true);

    fireResync();
    await act(async () => {
      await flush();
    });

    expect(harness.listLive).toHaveBeenCalledTimes(2);
    // Another device typed last: this view renders the PTY size.
    expect(harness.xtermProps?.canResize).toBe(false);
  });

  it("marks the tab exited when the resync refresh finds the process gone", async () => {
    harness.listLive
      .mockResolvedValueOnce([liveTab()])
      .mockResolvedValue([liveTab({ exited: true })]);
    mounted = mountPane();
    await act(async () => {
      await flush();
    });
    expect(overlayText("终端进程已结束")).toBeNull();

    fireResync();
    await act(async () => {
      await flush();
    });

    expect(overlayText("终端进程已结束")).not.toBeNull();
    const tab = useUi
      .getState()
      .workspaces[0]?.panes[0]?.tabs.find((candidate) => candidate.id === "t1");
    expect(tab?.exited).toBe(true);
  });

  it("treats a missing listLive entry as unknown instead of exited", async () => {
    harness.listLive
      .mockResolvedValueOnce([liveTab({ exited: true })])
      .mockResolvedValue([]);
    mounted = mountPane();
    await act(async () => {
      await flush();
    });
    expect(overlayText("终端进程已结束")).not.toBeNull();

    fireResync();
    await act(async () => {
      await flush();
    });

    expect(overlayText("终端进程已结束")).toBeNull();
    const tab = useUi
      .getState()
      .workspaces[0]?.panes[0]?.tabs.find((candidate) => candidate.id === "t1");
    expect(tab?.exited).toBeUndefined();
    expect(tab?.dead).not.toBe(true);
  });

  it("keeps a newer control event when the stale listLive snapshot returns later", async () => {
    const stale = deferred<LiveTabInfo[]>();
    harness.listLive
      .mockResolvedValueOnce([liveTab()])
      .mockReturnValueOnce(stale.promise);
    mounted = mountPane();
    await act(async () => {
      await flush();
    });

    fireResync();
    deliverControl({
      tabId: "kernel-1",
      version: 2,
      sizeOwner: "other",
      subscribers: 2,
      viewers: 2,
      exited: false,
      cols: 80,
      rows: 24,
      gridRevision: 0,
    });
    expect(harness.xtermProps?.canResize).toBe(false);

    stale.resolve([liveTab({ sizeOwner: "me" })]);
    await act(async () => {
      await flush();
    });

    expect(harness.xtermProps?.canResize).toBe(false);
  });

  it("drops the listLive response when the kernel tab switched before it returned", async () => {
    const stale = deferred<LiveTabInfo[]>();
    harness.listLive
      .mockResolvedValueOnce([liveTab()])
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValue([liveTab({ tabId: "kernel-2" })]);
    mounted = mountPane();
    await act(async () => {
      await flush();
    });

    fireResync();
    act(() => {
      (harness.xtermProps?.onAttach as ((id: string) => void) | undefined)?.("kernel-2");
    });
    await act(async () => {
      await flush();
    });

    stale.resolve([liveTab({ sizeOwner: "other", subscribers: 2, viewers: 2 })]);
    await act(async () => {
      await flush();
    });

    expect(harness.xtermProps?.canResize).toBe(true);
    expect(overlayText("终端进程已结束")).toBeNull();
  });

  it("applies a backend custom tab title from control events to the tab strip", async () => {
    harness.listLive.mockResolvedValue([liveTab()]);
    mounted = mountPane();
    await act(async () => {
      await flush();
    });

    deliverControl({
      tabId: "kernel-1",
      version: 2,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      cols: 80,
      rows: 24,
      gridRevision: 0,
      title: "构建机",
      titleCustom: true,
    });

    const tab = useUi
      .getState()
      .workspaces[0]?.panes[0]?.tabs.find((candidate) => candidate.id === "t1");
    expect(tab?.title).toBe("构建机");
    expect(tab?.renamedByUser).toBe(true);
  });

  it("clears the recording badge and preserves a copyable failure and path after a real-time event", async () => {
    harness.recordingStatus.mockResolvedValue(recording());
    mounted = mountPane();
    await act(async () => {
      await flush();
    });
    expect(overlayText("录制中")).not.toBeNull();

    harness.recordingStatus.mockResolvedValue(
      recording({ state: "failed", bytes: 7, safeError: "disk full", generation: 1 }),
    );
    deliverControl({
      tabId: "kernel-1",
      version: 2,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      cols: 80,
      rows: 24,
      gridRevision: 0,
    });
    await act(async () => {
      await flush();
    });

    expect(overlayText("录制中")).toBeNull();
    expect(mounted.container.textContent).toContain("disk full");
    expect(mounted.container.textContent).toContain("/tmp/recording.log");
    expect(harness.recordStart).not.toHaveBeenCalled();
    const copyError = mounted.container.querySelector<HTMLButtonElement>(
      'button[title="复制录制错误"]',
    );
    const copyPath = mounted.container.querySelector<HTMLButtonElement>(
      'button[title="复制录制路径"]',
    );
    expect(copyError).not.toBeNull();
    expect(copyPath).not.toBeNull();
    click(copyError!);
    click(copyPath!);
    expect(harness.copyText).toHaveBeenNthCalledWith(1, "disk full");
    expect(harness.copyText).toHaveBeenNthCalledWith(2, "/tmp/recording.log");
  });

  it("recovers a missed recording failure through resync", async () => {
    harness.recordingStatus.mockResolvedValue(recording());
    mounted = mountPane();
    await act(async () => {
      await flush();
    });
    expect(harness.recordingStatus).toHaveBeenCalledTimes(1);
    expect(overlayText("录制中")).not.toBeNull();

    harness.recordingStatus.mockResolvedValue(
      recording({ state: "failed", bytes: 9, safeError: "write failed", generation: 1 }),
    );
    fireResync();
    await act(async () => {
      await flush();
    });

    expect(harness.recordingStatus).toHaveBeenCalledTimes(2);
    expect(overlayText("录制中")).toBeNull();
    expect(mounted.container.textContent).toContain("write failed");
    expect(mounted.container.textContent).toContain("/tmp/recording.log");
  });

  it("clears an old failure only after the user explicitly restarts recording", async () => {
    harness.recordingStatus.mockResolvedValue(
      recording({ state: "failed", bytes: 9, safeError: "old failure", generation: 1 }),
    );
    mounted = mountPane();
    await act(async () => {
      await flush();
    });
    expect(overlayText("录制中")).toBeNull();
    expect(mounted.container.textContent).toContain("old failure");
    expect(harness.recordStart).not.toHaveBeenCalled();

    vi.mocked(pickSavePath).mockResolvedValue("/tmp/restarted.log");
    harness.recordStart.mockImplementation(async () => {
      harness.recordingStatus.mockResolvedValue(
        recording({ state: "running", bytes: 0, path: "/tmp/restarted.log", generation: 2 }),
      );
    });
    clickTerminalMenuItem("重新开始录制…");
    await act(async () => {
      await flush();
    });

    expect(harness.recordStart).toHaveBeenCalledWith("kernel-1", "/tmp/restarted.log");
    expect(overlayText("录制中")).not.toBeNull();
    expect(mounted.container.textContent).not.toContain("old failure");
    expect(
      mounted.container.querySelector('.nx-toolbar .nx-badge[title="/tmp/restarted.log"]'),
    ).not.toBeNull();
  });

  it("shows stale configuration and reconnects a connected session with the current asset", async () => {
    useUi.setState({
      sessions: [{ ...useUi.getState().sessions[0], staleConfig: true }],
    });
    let reconnectRequested = false;
    harness.sessionReconnect.mockImplementation(() => {
      reconnectRequested = true;
      return Promise.resolve(true);
    });
    // The manual reconnect must observe the session engage before the connected frame counts as fresh.
    let engaged = false;
    harness.sessionList.mockImplementation(() => {
      if (!reconnectRequested) return Promise.resolve(useUi.getState().sessions);
      if (!engaged) {
        engaged = true;
        return Promise.resolve(
          useUi.getState().sessions.map((session) => ({ ...session, status: "reconnecting" as const })),
        );
      }
      return Promise.resolve(useUi.getState().sessions.map((session) => ({ ...session, staleConfig: false })));
    });
    mounted = mountPane();
    await act(async () => {
      await flush();
    });

    expect(overlayText("资产配置已修改，重新连接后生效")).not.toBeNull();
    const reconnect = mounted.container.querySelector<HTMLButtonElement>('button[title="用新的资产配置重新连接，当前终端保留"]');
    expect(reconnect).not.toBeNull();
    expect(reconnect?.disabled).toBe(false);
    click(reconnect!);

    await waitForRendered(() => expect(useUi.getState().sessions[0]?.staleConfig).toBe(false), 3_000);
    expect(harness.sessionReconnect).toHaveBeenCalledWith("s1");
  });
});
