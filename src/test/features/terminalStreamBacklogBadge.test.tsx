/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, type MountedView } from "./reactTestUtils";

const harness = vi.hoisted(() => ({
  handlers: new Map<string, (payload: unknown) => void>(),
  resyncSubs: new Set<() => void>(),
  sessionList: vi.fn(),
  xtermProps: null as Record<string, unknown> | null,
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

vi.mock("../../ipc/webTransport", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/webTransport")>();
  return {
    ...actual,
    onEventsResync: (cb: () => void) => {
      harness.resyncSubs.add(cb);
      return () => harness.resyncSubs.delete(cb);
    },
  };
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: vi.fn((topic: string, handler: (payload: unknown) => void) => {
      harness.handlers.set(topic, handler);
      return Promise.resolve(() => {});
    }),
    EVENTS: { terminalControl: "terminal://control", terminalThrottled: "terminal://throttled" },
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
      harness.xtermProps = props;
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
          fit: () => true,
          paste: () => {},
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

let mounted: MountedView | undefined;

function badge(text: string): HTMLElement | null {
  return (
    [...(mounted?.container.querySelectorAll<HTMLElement>(".nx-badge") ?? [])].find((b) =>
      b.textContent?.includes(text),
    ) ?? null
  );
}

function emitBacklog(backlog: { paused: boolean; pendingBytes: number; oldestPendingAgeMs: number } | null) {
  act(() => {
    (harness.xtermProps?.onStreamBacklog as ((b: unknown) => void) | undefined)?.(backlog);
  });
}

beforeEach(async () => {
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
        splitRatio: 0.5,
      },
    ],
    activeWorkspaceId: "ws1",
  });
  mounted = mount(
    createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }),
  );
  await flush();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("terminal stream parse-backlog badge", () => {
  it("shows while the stream writer is paused with pending byte visibility", () => {
    expect(badge("解析积压")).toBeNull();
    emitBacklog({ paused: true, pendingBytes: 4096, oldestPendingAgeMs: 12 });
    const el = badge("解析积压");
    expect(el).not.toBeNull();
    expect(el?.getAttribute("title")).toContain("4096");
  });

  it("clears when the writer resumes", () => {
    emitBacklog({ paused: true, pendingBytes: 4096, oldestPendingAgeMs: 12 });
    expect(badge("解析积压")).not.toBeNull();
    emitBacklog(null);
    expect(badge("解析积压")).toBeNull();
  });

  it("clears stale backlog state when a new attach reports info", () => {
    emitBacklog({ paused: true, pendingBytes: 8192, oldestPendingAgeMs: 3 });
    expect(badge("解析积压")).not.toBeNull();
    act(() => {
      (harness.xtermProps?.onAttachInfo as ((info: unknown) => void) | undefined)?.({
        tabId: "kernel-1",
        controller: "me",
        subscribers: 1,
        viewers: 1,
        exited: false,
      });
    });
    expect(badge("解析积压")).toBeNull();
  });
});
