/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, type MountedView } from "./reactTestUtils";

const harness = vi.hoisted(() => ({
  mac: false,
  selection: "",
  findNext: vi.fn(),
  findPrevious: vi.fn(),
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
  SessionSidebar: (props: { onClose: () => void }) => (
    <aside data-testid="session-sidebar"><button aria-label="stub-close" onClick={props.onClose} /></aside>
  ),
}));
vi.mock("../../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

vi.mock("../../features/terminal/XtermView", async () => {
  const { useEffect, useRef } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
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
          focus: () => {},
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
    await act(async () => mounted!.container.querySelector<HTMLButtonElement>("[aria-label='stub-close']")!.click());
    expect(sidebar()).toBeNull();
  });

  it("shows the sidebar on first render when the preference is already open", async () => {
    setSessionSidebarOpen(true);
    expect(localStorage.getItem(SESSION_SIDEBAR_STORAGE_KEY)).not.toBeNull();
    await mountPane();
    expect(sidebar()).not.toBeNull();
  });
});
