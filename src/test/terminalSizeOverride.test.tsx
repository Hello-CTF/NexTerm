/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, mount, type MountedView } from "./features/reactTestUtils";
import type { LiveTabInfo } from "../ipc/commands";
import { SIZE_OVERRIDE_LIMIT, SIZE_OVERRIDE_WINDOW_MS, SizeOverrideGuard } from "../features/terminal/sizeOverrideGuard";

const harness = vi.hoisted(() => ({
  handlers: new Map<string, (payload: unknown) => void>(),
  xtermProps: null as Record<string, unknown> | null,
  listLive: vi.fn(),
  takeSize: vi.fn(),
  sessionList: vi.fn(),
  recordingStatus: vi.fn(),
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
    reconnect: vi.fn().mockResolvedValue(true),
    disconnect: vi.fn(),
    list: harness.sessionList,
    probeHostKey: vi.fn().mockResolvedValue({ state: "known" }),
  },
  terminalApi: {
    listLive: harness.listLive,
    takeSize: harness.takeSize,
    write: vi.fn().mockResolvedValue(undefined),
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
    recordingStatus: harness.recordingStatus,
  },
}));

vi.mock("../ipc/webTransport", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/webTransport")>();
  return { ...actual, onEventsResync: () => () => {} };
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

let mounted: MountedView | undefined;
let version = 1;

function liveTab(patch: Partial<LiveTabInfo> = {}): LiveTabInfo {
  return {
    tabId: "kernel-1",
    sessionId: "s1",
    sessionName: "web-01",
    sessionKind: "ssh",
    cols: 80,
    rows: 24,
    sizeOwner: "me",
    subscribers: 2,
    viewers: 2,
    exited: false,
    lastOutputMsAgo: 0,
    ...patch,
  };
}

function deliverOwner(sizeOwner: string | null): void {
  const handler = harness.handlers.get("terminal://control");
  expect(handler).toBeDefined();
  version += 1;
  act(() => {
    handler?.({
      tabId: "kernel-1",
      version,
      sizeOwner,
      subscribers: 2,
      viewers: 2,
      exited: false,
      cols: 80,
      rows: 24,
      gridRevision: 0,
    });
  });
}

function notice(): HTMLElement | null {
  return mounted?.container.querySelector<HTMLElement>("[data-follower-contended]") ?? null;
}

beforeEach(() => {
  vi.clearAllMocks();
  harness.handlers.clear();
  harness.xtermProps = null;
  version = 1;
  document.body.replaceChildren();
  harness.listLive.mockResolvedValue([liveTab()]);
  harness.takeSize.mockResolvedValue(undefined);
  harness.sessionList.mockImplementation(() => Promise.resolve(useUi.getState().sessions));
  harness.recordingStatus.mockResolvedValue({ tabId: "kernel-1", state: "never-started", bytes: 0, generation: 0 });
  useUi.setState({
    recordings: {},
    sessions: [{ id: "s1", assetId: "a1", name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
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
            tabs: [{ id: "t1", kind: "terminal", title: "term", sessionId: "s1", tabId: "kernel-1", closable: true }],
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

describe("SizeOverrideGuard", () => {
  it("engages after the limit of overrides within the window and resets on purpose", () => {
    let now = 0;
    const guard = new SizeOverrideGuard(() => now);
    expect(guard.note(true, true)).toBe(false);
    for (let i = 1; i < SIZE_OVERRIDE_LIMIT; i++) {
      now += 1000;
      expect(guard.note(false, true)).toBe(false);
      expect(guard.note(true, true)).toBe(false);
    }
    now += 1000;
    expect(guard.note(false, true)).toBe(true);
    // The backend handing the size back does not lift it.
    expect(guard.note(true, true)).toBe(true);
    guard.reset();
    expect(guard.suppressed()).toBe(false);
  });

  it("forgets overrides older than the window and ignores the gap between owners", () => {
    let now = 0;
    const guard = new SizeOverrideGuard(() => now);
    guard.note(true, true);
    for (let i = 0; i < SIZE_OVERRIDE_LIMIT * 2; i++) {
      now += SIZE_OVERRIDE_WINDOW_MS;
      expect(guard.note(false, true)).toBe(false);
      expect(guard.note(true, true)).toBe(false);
    }
    // Nobody owning the size briefly (an owner left) is not an override.
    for (let i = 0; i < SIZE_OVERRIDE_LIMIT * 2; i++) {
      now += 10;
      expect(guard.note(false, false)).toBe(false);
      expect(guard.note(true, true)).toBe(false);
    }
  });
});

describe("terminal size contention", () => {
  async function mountOwned(): Promise<void> {
    mounted = mount(createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }));
    await act(async () => {
      await flush();
    });
    expect(harness.xtermProps?.canResize).toBe(true);
  }

  it("keeps a single device's size without a notice", async () => {
    await mountOwned();
    deliverOwner("me");
    deliverOwner("me");
    expect(harness.xtermProps?.canResize).toBe(true);
    expect(notice()).toBeNull();
  });

  it("stops sending this view's size after repeated overrides until the user takes it back", async () => {
    await mountOwned();
    for (let i = 1; i < SIZE_OVERRIDE_LIMIT; i++) {
      deliverOwner("phone");
      expect(harness.xtermProps?.canResize).toBe(false);
      deliverOwner("me");
      expect(harness.xtermProps?.canResize).toBe(true);
    }
    deliverOwner("phone");
    expect(notice()?.textContent).toContain("其他设备正在使用此终端的尺寸");
    // The backend hands the size back on its own: the view keeps following and sends nothing.
    deliverOwner("me");
    expect(harness.xtermProps?.canResize).toBe(false);
    expect(notice()).not.toBeNull();
    // Only one button takes the size while the notice shows.
    const buttons = mounted!.container.querySelectorAll<HTMLButtonElement>("[data-follower-take-size]");
    expect(buttons).toHaveLength(1);

    harness.listLive.mockResolvedValue([liveTab({ sizeOwner: "me" })]);
    await act(async () => {
      click(buttons[0]!);
      await flush();
    });
    expect(harness.takeSize).toHaveBeenCalledWith("kernel-1");
    expect(harness.xtermProps?.canResize).toBe(true);
    expect(notice()).toBeNull();

    // The count starts over after taking the size on purpose.
    deliverOwner("phone");
    deliverOwner("me");
    expect(harness.xtermProps?.canResize).toBe(true);
  });

  it("lets a view that follows take the size with one tap while the notice shows", async () => {
    await mountOwned();
    for (let i = 0; i < SIZE_OVERRIDE_LIMIT; i++) {
      deliverOwner("other-device");
      if (i < SIZE_OVERRIDE_LIMIT - 1) deliverOwner("me");
    }
    expect(notice()).not.toBeNull();
    harness.takeSize.mockImplementation(async () => {
      deliverOwner("me");
    });
    harness.listLive.mockResolvedValue([liveTab({ sizeOwner: "me" })]);
    const button = mounted!.container.querySelector<HTMLButtonElement>("[data-follower-contended] [data-follower-take-size]")!;
    await act(async () => {
      click(button);
      await flush();
    });
    expect(harness.xtermProps?.canResize).toBe(true);
    expect(notice()).toBeNull();
  });
});
