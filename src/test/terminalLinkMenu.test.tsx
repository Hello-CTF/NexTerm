/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, mount, waitFor, type MountedView } from "./features/reactTestUtils";

const harness = vi.hoisted(() => ({
  handlers: new Map<string, (payload: unknown) => void>(),
  resyncSubs: new Set<() => void>(),
  xtermProps: null as Record<string, unknown> | null,
  closeTab: vi.fn(),
  sessionList: vi.fn(),
  listLive: vi.fn(),
  listDurable: vi.fn(),
  fitChanged: true,
  hoveredLink: null as string | null,
  openLink: vi.fn(),
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
  sessionApi: { connect: vi.fn(), reconnect: vi.fn(), disconnect: vi.fn(), list: harness.sessionList },
  terminalApi: {
    listLive: harness.listLive,
    listDurable: harness.listDurable,
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

vi.mock("../ui/clipboard", () => ({ copyText: harness.copyText, readText: vi.fn() }));

vi.mock("../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

vi.mock("../features/terminal/XtermView", async () => {
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
          fit: () => harness.fitChanged,
          dimensions: () => ({ cols: 80, rows: 24 }),
          hoveredLink: () => harness.hoveredLink,
          openLink: harness.openLink,
        });
        (onAttachRef.current as ((id: string) => void) | undefined)?.("kernel-1");
      }, []);
      return <div className="h-full w-full min-h-0" />;
    },
  };
});

import { TerminalPane } from "../features/terminal/TerminalPane";
import { setTerminalCloseAction } from "../app/preferences";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;

function mountPane(): MountedView {
  return mount(
    createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }),
  );
}





beforeEach(() => {
  vi.clearAllMocks();
  harness.handlers.clear();
  harness.resyncSubs.clear();
  document.body.replaceChildren();
  setTerminalCloseAction("detach");
  harness.closeTab.mockResolvedValue(undefined);
  harness.sessionList.mockImplementation(async () => useUi.getState().sessions);
  harness.listLive.mockResolvedValue([]);
  harness.listDurable.mockResolvedValue([]);
  harness.fitChanged = true;
  harness.hoveredLink = null;
  harness.copyText.mockResolvedValue(undefined);
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
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function menuItems(): string[] {
  return [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].map((item) => item.textContent?.trim() ?? "");
}

function menuItem(label: string): HTMLElement {
  const item = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((candidate) =>
    candidate.textContent?.includes(label),
  );
  if (!item) throw new Error(`menu item ${label} not found`);
  return item;
}

async function mountReadyPane(): Promise<void> {
  mounted = mountPane();
  await act(async () => {
    await flush();
  });
}

describe("terminal link menu", () => {
  it("a touch long-press on a link offers open and copy", async () => {
    await mountReadyPane();
    const onLinkMenu = harness.xtermProps?.onLinkMenu as (request: { uri: string; x: number; y: number }) => void;
    expect(onLinkMenu).toBeTypeOf("function");
    act(() => onLinkMenu({ uri: "https://example.com/docs", x: 30, y: 40 }));
    expect(menuItems()).toEqual(["打开链接", "复制链接"]);

    click(menuItem("打开链接"));
    expect(harness.openLink).toHaveBeenCalledWith("https://example.com/docs");

    act(() => onLinkMenu({ uri: "https://example.com/docs\u200b", x: 30, y: 40 }));
    click(menuItem("复制链接"));
    await waitFor(() => expect(harness.copyText).toHaveBeenCalledWith("https://example.com/docs"));
  });

  it("right-click on a hovered link puts the link actions before the terminal actions", async () => {
    await mountReadyPane();
    harness.hoveredLink = "file://web01/etc/hosts";
    const area = mounted!.container.querySelector(".relative.min-h-0.min-w-0.flex-1");
    expect(area).not.toBeNull();
    act(() => {
      area!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 5, clientY: 5 }));
    });
    const items = menuItems();
    expect(items.slice(0, 2)).toEqual(["打开链接", "复制链接"]);
    expect(items).toContain("粘贴");
  });

  it("right-click elsewhere shows no link actions", async () => {
    await mountReadyPane();
    const area = mounted!.container.querySelector(".relative.min-h-0.min-w-0.flex-1");
    act(() => {
      area!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 5, clientY: 5 }));
    });
    expect(menuItems()).not.toContain("打开链接");
    expect(menuItems()).toContain("粘贴");
  });
});
