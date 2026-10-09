/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, type MountedView } from "./reactTestUtils";
import { THROTTLE_RECOVERED_MS } from "../../features/terminal/terminalThrottle";

const harness = vi.hoisted(() => ({
  handlers: new Map<string, (payload: unknown) => void>(),
  resyncSubs: new Set<() => void>(),
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
  sessionApi: { connect: vi.fn(), reconnect: vi.fn(), disconnect: vi.fn() },
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

beforeEach(() => {
  vi.clearAllMocks();
  harness.handlers.clear();
  document.body.replaceChildren();
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
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
});

describe("terminal://throttled badge", () => {
  it("shows active on entry, stays active without recovery, recovered only on drain event", async () => {
    vi.useFakeTimers();
    await act(async () => {
      await flush();
    });
    const throttled = harness.handlers.get("terminal://throttled");
    expect(throttled).toBeDefined();

    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "a-1", inflightBytes: 8192, version: 1 });
    });
    expect(badge("输出积压")).not.toBeNull();

    await act(async () => {
      vi.advanceTimersByTime(60_000);
    });
    expect(badge("输出积压")).not.toBeNull();
    expect(badge("输出已恢复")).toBeNull();

    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "a-1", inflightBytes: 128, recovered: true, version: 2 });
    });
    expect(badge("输出积压")).toBeNull();
    expect(badge("输出已恢复")).not.toBeNull();

    await act(async () => {
      vi.advanceTimersByTime(THROTTLE_RECOVERED_MS + 100);
    });
    expect(badge("输出已恢复")).toBeNull();
  });

  it("recovers on the single tab-level recovered after both channels drain (real production sequence)", async () => {
    vi.useFakeTimers();
    await act(async () => {
      await flush();
    });
    const throttled = harness.handlers.get("terminal://throttled");

    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "a-1", inflightBytes: 4096, version: 1 });
    });
    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "b-1", inflightBytes: 8192, version: 2 });
    });
    expect(badge("输出积压")).not.toBeNull();

    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "b-1", inflightBytes: 64, recovered: true, version: 3 });
    });
    expect(badge("输出积压")).toBeNull();
    expect(badge("输出已恢复")).not.toBeNull();
  });

  it("clears the whole set on discard/detach-style tab-level recovered", async () => {
    vi.useFakeTimers();
    await act(async () => {
      await flush();
    });
    const throttled = harness.handlers.get("terminal://throttled");

    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "a-1", inflightBytes: 4096, version: 1 });
    });
    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "b-1", inflightBytes: 8192, version: 2 });
    });
    expect(badge("输出积压")).not.toBeNull();

    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "a-1", inflightBytes: 0, recovered: true, version: 3 });
    });
    expect(badge("输出积压")).toBeNull();
    expect(badge("输出已恢复")).not.toBeNull();
  });

  it("re-arms active on a new episode and ignores stale replayed events", async () => {
    vi.useFakeTimers();
    await act(async () => {
      await flush();
    });
    const throttled = harness.handlers.get("terminal://throttled");

    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "a-1", inflightBytes: 8192, version: 1 });
    });
    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "a-1", inflightBytes: 128, recovered: true, version: 2 });
    });
    expect(badge("输出已恢复")).not.toBeNull();

    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "a-1", inflightBytes: 16384, version: 3 });
    });
    expect(badge("输出已恢复")).toBeNull();
    expect(badge("输出积压")).not.toBeNull();

    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "a-1", inflightBytes: 128, recovered: true, version: 2 });
    });
    expect(badge("输出积压")).not.toBeNull();
    expect(badge("输出已恢复")).toBeNull();
  });

  it("clears stale backlog on events resync and on channel reattach", async () => {
    vi.useFakeTimers();
    await act(async () => {
      await flush();
    });
    const throttled = harness.handlers.get("terminal://throttled");

    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "a-1", inflightBytes: 8192, version: 1 });
    });
    expect(badge("输出积压")).not.toBeNull();

    act(() => {
      for (const cb of [...harness.resyncSubs]) cb();
    });
    expect(badge("输出积压")).toBeNull();
    expect(badge("输出已恢复")).toBeNull();

    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "a-2", inflightBytes: 8192, version: 2 });
    });
    expect(badge("输出积压")).not.toBeNull();

    act(() => {
      (harness.xtermProps?.onAttachInfo as ((info: unknown) => void) | undefined)?.({
        controller: "me",
        subscribers: 1,
        viewers: 1,
        exited: false,
      });
    });
    expect(badge("输出积压")).toBeNull();
    expect(badge("输出已恢复")).toBeNull();
  });

  it("accepts a genuine rewound episode after resync resets the version gate", async () => {
    vi.useFakeTimers();
    await act(async () => {
      await flush();
    });
    const throttled = harness.handlers.get("terminal://throttled");

    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "a-1", inflightBytes: 4096, version: 1 });
    });
    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "a-1", inflightBytes: 128, recovered: true, version: 2 });
    });
    expect(badge("输出已恢复")).not.toBeNull();

    act(() => {
      for (const cb of [...harness.resyncSubs]) cb();
    });
    expect(badge("输出已恢复")).toBeNull();

    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "a-1", inflightBytes: 8192, version: 1 });
    });
    expect(badge("输出积压")).not.toBeNull();

    act(() => {
      throttled?.({ tabId: "kernel-1", channelId: "a-1", inflightBytes: 64, recovered: true, version: 2 });
    });
    expect(badge("输出积压")).toBeNull();
    expect(badge("输出已恢复")).not.toBeNull();
  });

  it("ignores throttle events for other tabs", async () => {
    vi.useFakeTimers();
    await act(async () => {
      await flush();
    });
    const throttled = harness.handlers.get("terminal://throttled");

    act(() => {
      throttled?.({ tabId: "someone-else", channelId: "a-1", inflightBytes: 8192, version: 1 });
    });
    expect(badge("输出积压")).toBeNull();
    expect(badge("输出已恢复")).toBeNull();
  });
});
