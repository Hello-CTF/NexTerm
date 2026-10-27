/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, type MountedView } from "./features/reactTestUtils";

const harness = vi.hoisted(() => ({
  handlers: new Map<string, (payload: unknown) => void>(),
  resyncSubs: new Set<() => void>(),
  sessionList: vi.fn(),
  xtermProps: null as Record<string, unknown> | null,
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
  sessionApi: { connect: vi.fn(), reconnect: vi.fn(), disconnect: vi.fn(), list: harness.sessionList },
  terminalApi: {
    listLive: vi.fn().mockResolvedValue([]),
    write: vi.fn().mockResolvedValue(undefined),
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
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

vi.mock("../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

vi.mock("../features/terminal/XtermView", async () => {
  const { useEffect, useRef } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
      harness.xtermProps = props;
      const onHandleRef = useRef(props.onHandle);
      onHandleRef.current = props.onHandle;
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
      }, []);
      return <div className="h-full w-full min-h-0" />;
    },
  };
});

import { TerminalPane } from "../features/terminal/TerminalPane";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;

function badge(text: string): HTMLElement | null {
  return (
    [...(mounted?.container.querySelectorAll<HTMLElement>(".nx-badge") ?? [])].find((b) =>
      b.textContent?.includes(text),
    ) ?? null
  );
}

// The durable mode is reported on the store tab, which the tab strip marks with the persistence icon.
function durableTab(): boolean {
  return useUi.getState().workspaces[0]?.panes[0]?.tabs.find((tab) => tab.id === "t1")?.durable === true;
}

function control(payload: Record<string, unknown>): void {
  const handler = harness.handlers.get("terminal://control");
  expect(handler).toBeDefined();
  act(() => {
    handler?.(payload);
  });
}

function attach(kernelTabId: string): void {
  act(() => {
    (harness.xtermProps?.onAttach as ((id: string) => void) | undefined)?.(kernelTabId);
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  harness.handlers.clear();
  document.body.replaceChildren();
  harness.sessionList.mockImplementation(async () => useUi.getState().sessions);
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
      },
    ],
    activeWorkspaceId: "ws1",
  });
  mounted = mount(
    createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }),
  );
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("TerminalPane early control state", () => {
  it("applies cwd and durable from a control event that arrives before attach", async () => {
    await act(async () => {
      await flush();
    });
    control({
      tabId: "kernel-9",
      version: 1,
      controller: null,
      subscribers: 1,
      viewers: 1,
      exited: false,
      cols: 80,
      rows: 24,
      gridRevision: 0,
      cwd: "/srv/www/app",
      durable: true,
    });
    expect(badge("/srv/www/app")).toBeNull();
    expect(durableTab()).toBe(false);

    attach("kernel-9");
    await flush();
    expect(badge("/srv/www/app")).not.toBeNull();
    expect(durableTab()).toBe(true);
  });

  it("ignores cached state that belongs to another tab", async () => {
    await act(async () => {
      await flush();
    });
    control({
      tabId: "kernel-8",
      version: 1,
      controller: null,
      subscribers: 1,
      viewers: 1,
      exited: false,
      cols: 80,
      rows: 24,
      gridRevision: 0,
      cwd: "/elsewhere",
      durable: true,
    });

    attach("kernel-9");
    await flush();
    expect(badge("/elsewhere")).toBeNull();
    expect(durableTab()).toBe(false);
  });

  it("prefers the newest cached event per tab", async () => {
    await act(async () => {
      await flush();
    });
    control({
      tabId: "kernel-9",
      version: 1,
      controller: null,
      subscribers: 1,
      viewers: 1,
      exited: false,
      cols: 80,
      rows: 24,
      gridRevision: 0,
      cwd: "/stale",
      durable: false,
    });
    control({
      tabId: "kernel-9",
      version: 2,
      controller: null,
      subscribers: 1,
      viewers: 1,
      exited: false,
      cols: 80,
      rows: 24,
      gridRevision: 0,
      cwd: "/fresh",
      durable: true,
    });

    attach("kernel-9");
    await flush();
    expect(badge("/fresh")).not.toBeNull();
    expect(badge("/stale")).toBeNull();
    expect(durableTab()).toBe(true);
  });
});
