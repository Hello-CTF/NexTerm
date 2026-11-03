/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, type MountedView } from "./reactTestUtils";

const harness = vi.hoisted(() => ({
  mac: false,
  selection: "",
  findNext: vi.fn(),
  findPrevious: vi.fn(),
  focus: vi.fn(),
  xtermRenders: 0,
}));

vi.mock("../../app/platform", () => ({
  isMac: () => harness.mac,
  modHint: () => (harness.mac ? "⌘" : "Ctrl"),
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
}));

vi.mock("../../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../../features/terminal/SessionSidebar", () => ({
  SessionSidebar: (props: { onClose: () => void; storeTabId: string; commandBlocks: unknown }) => (
    <aside data-testid="session-sidebar" data-tab={props.storeTabId}>
      <button aria-label="stub-close" onClick={props.onClose} />
      {props.commandBlocks as never}
    </aside>
  ),
}));
vi.mock("../../features/terminal/TerminalCommandBlocks", () => ({
  TerminalCommandBlocks: (props: { embedded?: boolean }) => (
    <div data-testid="command-blocks" data-embedded={props.embedded ? "true" : "false"} />
  ),
}));
vi.mock("../../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

vi.mock("../../features/terminal/XtermView", async () => {
  const { useEffect, useRef } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
      harness.xtermRenders += 1;
      const onHandleRef = useRef(props.onHandle);
      const registerSearchRef = useRef(props.registerSearch);
      const onAttachRef = useRef(props.onAttach);
      onHandleRef.current = props.onHandle;
      registerSearchRef.current = props.registerSearch;
      onAttachRef.current = props.onAttach;
      useEffect(() => {
        (onHandleRef.current as ((h: unknown) => void) | undefined)?.({
          copyBlock: () => "",
          scrollToBlock: () => {},
          navigateBlock: () => null,
          clearBlocks: () => {},
          clear: () => {},
          getSelection: () => harness.selection,
          focus: harness.focus,
          fit: () => {},
          dimensions: () => ({ cols: 80, rows: 24 }),
        });
        (registerSearchRef.current as ((api: unknown) => void) | undefined)?.({
          findNext: harness.findNext,
          findPrevious: harness.findPrevious,
        });
        (onAttachRef.current as ((id: string) => void) | undefined)?.("kernel-1");
      }, []);
      return (
        <div className="h-full w-full min-h-0">
          <textarea data-testid="xterm-textarea" readOnly />
        </div>
      );
    },
  };
});

import { TerminalPane } from "../../features/terminal/TerminalPane";
import { useUi } from "../../app/store";
import { SESSION_SIDEBAR_STORAGE_KEY, setSessionSidebarOpen } from "../../app/preferences";

let mounted: MountedView | undefined;

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function xtermTextarea(): HTMLTextAreaElement {
  const ta = mounted?.container.querySelector<HTMLTextAreaElement>('[data-testid="xterm-textarea"]');
  if (!ta) throw new Error("xterm textarea not found");
  return ta;
}

async function pressFromTerminal(
  key: string,
  init: KeyboardEventInit = {},
): Promise<{
  event: KeyboardEvent;
  targetSeen: ReturnType<typeof vi.fn>;
  bubbleSeen: ReturnType<typeof vi.fn>;
}> {
  const ta = xtermTextarea();
  act(() => ta.focus());
  const targetSeen = vi.fn();
  const bubbleSeen = vi.fn();
  ta.addEventListener("keydown", targetSeen);
  mounted?.container.addEventListener("keydown", bubbleSeen);
  const event = keyDown(ta, key, init);
  await flush();
  return { event, targetSeen, bubbleSeen };
}

beforeEach(async () => {
  vi.clearAllMocks();
  localStorage.clear();
  document.body.replaceChildren();
  harness.mac = false;
  harness.selection = "";
  harness.xtermRenders = 0;
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

async function mountPane() {
  mounted = mount(createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }));
  await flush();
}

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

const SIDEBAR: KeyboardEventInit = { code: "KeyB", ctrlKey: true, shiftKey: true };
const sidebar = () => mounted?.container.querySelector('[data-testid="session-sidebar"]') ?? null;
const toggleButton = () => mounted?.container.querySelector<HTMLButtonElement>("button[aria-pressed][title*='侧栏']") ?? null;

describe("session sidebar in the terminal pane", () => {
  it("is closed by default", async () => {
    await mountPane();
    expect(sidebar()).toBeNull();
    expect(toggleButton()?.getAttribute("aria-pressed")).toBe("false");
  });

  it("toggles with Ctrl+Shift+B inside the pane", async () => {
    await mountPane();
    const first = await pressFromTerminal("B", SIDEBAR);
    expect(first.event.defaultPrevented).toBe(true);
    expect(first.targetSeen).not.toHaveBeenCalled();
    expect(sidebar()).not.toBeNull();
    await pressFromTerminal("B", SIDEBAR);
    expect(sidebar()).toBeNull();
  });

  it("ignores key repeats", async () => {
    await mountPane();
    await pressFromTerminal("B", { ...SIDEBAR, repeat: true });
    expect(sidebar()).toBeNull();
  });

  it("toggles from the toolbar button", async () => {
    await mountPane();
    await act(async () => toggleButton()!.click());
    expect(sidebar()).not.toBeNull();
    expect(toggleButton()?.getAttribute("aria-pressed")).toBe("true");
    await act(async () => toggleButton()!.click());
    expect(sidebar()).toBeNull();
  });

  it("closes through the sidebar close handler", async () => {
    await mountPane();
    await act(async () => toggleButton()!.click());
    harness.focus.mockClear();
    await act(async () => mounted!.container.querySelector<HTMLButtonElement>("[aria-label='stub-close']")!.click());
    await flush();
    expect(sidebar()).toBeNull();
    expect(harness.focus).toHaveBeenCalled();
  });

  it("shows the sidebar on first render when the preference is already open", async () => {
    setSessionSidebarOpen(true);
    expect(localStorage.getItem(SESSION_SIDEBAR_STORAGE_KEY)).not.toBeNull();
    await mountPane();
    expect(sidebar()).not.toBeNull();
  });
});

// A ResizeObserver that reports the given width as soon as an element is observed.
function stubPaneWidth(width: number) {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(private cb: ResizeObserverCallback) {}
      observe(target: Element) {
        this.cb([{ target, contentRect: { width, height: 400 } } as ResizeObserverEntry], this as never);
      }
      unobserve() {}
      disconnect() {}
    },
  );
}

const tab = (id: string, kernel: string) => ({
  id, kind: "terminal" as const, title: id, sessionId: "s1", tabId: kernel, closable: true,
});

describe("session sidebar placement", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("does not re-render a collapsed pane when a sibling tab changes", async () => {
    await mountPane();
    const before = harness.xtermRenders;
    act(() => {
      useUi.setState((s) => ({
        workspaces: s.workspaces.map((w) => ({
          ...w,
          panes: w.panes.map((p) => ({ ...p, tabs: [...p.tabs, { ...tab("t9", "kernel-9"), kind: "files" as const }] })),
        })),
      }));
    });
    await flush();
    expect(harness.xtermRenders).toBe(before);
  });

  it("mounts the sidebar only in the active pane of a split workspace", async () => {
    setSessionSidebarOpen(true);
    useUi.setState((s) => ({
      workspaces: s.workspaces.map((w) => ({
        ...w,
        panes: [...w.panes, { id: "p2", activeTabId: "t2", tabs: [tab("t2", "kernel-2")] }],
      })),
    }));
    mounted = mount(
      createElement(
        "div",
        null,
        createElement(TerminalPane, { sessionId: "s1", title: "one", storeTabId: "t1" }),
        createElement(TerminalPane, { sessionId: "s1", title: "two", storeTabId: "t2" }),
      ),
    );
    await flush();
    const tabs = () => Array.from(mounted!.container.querySelectorAll("[data-testid='session-sidebar']")).map((e) => e.getAttribute("data-tab"));
    expect(tabs()).toEqual(["t1"]);
    act(() => {
      useUi.setState((s) => ({ workspaces: s.workspaces.map((w) => ({ ...w, activePaneId: "p2" })) }));
    });
    await flush();
    expect(tabs()).toEqual(["t2"]);
  });

  it("does not mount the sidebar in a narrow pane and explains the shortcut", async () => {
    stubPaneWidth(400);
    const pushToast = vi.fn();
    useUi.setState({ pushToast } as never);
    await mountPane();
    await pressFromTerminal("B", SIDEBAR);
    expect(sidebar()).toBeNull();
    expect(pushToast).toHaveBeenCalledWith("info", "窗格太窄，无法显示侧栏");
    expect(localStorage.getItem(SESSION_SIDEBAR_STORAGE_KEY)).not.toBe("1");
  });

  it("does not mount an open sidebar in a narrow pane", async () => {
    setSessionSidebarOpen(true);
    stubPaneWidth(400);
    await mountPane();
    expect(sidebar()).toBeNull();
  });

  it("shows a single copy of the command blocks when the sidebar is open", async () => {
    setSessionSidebarOpen(true);
    await mountPane();
    const list = Array.from(mounted!.container.querySelectorAll("button")).find((b) => b.textContent?.includes("命令列表"));
    await act(async () => list!.click());
    expect(mounted!.container.querySelectorAll("[data-testid='command-blocks']")).toHaveLength(1);
    expect(sidebar()?.querySelector("[data-testid='command-blocks']")).not.toBeNull();
  });
});
