/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, flushMacrotask, mount, waitFor, type MountedView } from "./reactTestUtils";

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

let mounted: MountedView | undefined;

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

// The default terminal search chord off macOS.
const SEARCH: KeyboardEventInit = { code: "KeyF", ctrlKey: true, shiftKey: true };

function xtermTextarea(): HTMLTextAreaElement {
  const ta = mounted?.container.querySelector<HTMLTextAreaElement>('[data-testid="xterm-textarea"]');
  if (!ta) throw new Error("xterm textarea not found");
  return ta;
}

function searchInput(): HTMLInputElement | null {
  return mounted?.container.querySelector<HTMLInputElement>('input[placeholder="搜索终端内容…"]') ?? null;
}

async function flushTimers(): Promise<void> {
  await act(async () => {
    await flushMacrotask();
  });
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
  mounted = mount(
    createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }),
  );
  await flush();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("terminal search shortcut", () => {
  it("opens and focuses terminal search with Ctrl+Shift+F on non-mac platforms", async () => {
    const { event, targetSeen, bubbleSeen } = await pressFromTerminal("F", SEARCH);

    expect(event.defaultPrevented).toBe(true);
    expect(targetSeen).not.toHaveBeenCalled();
    expect(bubbleSeen).not.toHaveBeenCalled();
    const input = searchInput();
    expect(input).not.toBeNull();
    expect(document.activeElement).toBe(input);
    expect(
      mounted?.container.querySelector('button[title="搜索终端内容 (Ctrl+Shift+F)"]'),
    ).not.toBeNull();
  });

  it("uses Cmd+F on macOS and leaves Ctrl+F to the terminal", async () => {
    harness.mac = true;
    mounted?.unmount();
    mounted = mount(
      createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }),
    );
    await flush();

    const ctrl = await pressFromTerminal("f", { ctrlKey: true });
    expect(ctrl.event.defaultPrevented).toBe(false);
    expect(ctrl.targetSeen).toHaveBeenCalledTimes(1);
    expect(searchInput()).toBeNull();

    const meta = await pressFromTerminal("f", { metaKey: true });
    expect(meta.event.defaultPrevented).toBe(true);
    expect(meta.targetSeen).not.toHaveBeenCalled();
    const input = searchInput();
    expect(input).not.toBeNull();
    expect(document.activeElement).toBe(input);
    expect(
      mounted?.container.querySelector('button[title="搜索终端内容 (⌘+F)"]'),
    ).not.toBeNull();
  });

  it("leaves Ctrl+F (readline forward-char) to the terminal and ignores Alt variants, key repeats and IME composition", async () => {
    const ctrlF = await pressFromTerminal("f", { ctrlKey: true });
    expect(ctrlF.event.defaultPrevented).toBe(false);
    expect(ctrlF.targetSeen).toHaveBeenCalledTimes(1);
    expect(searchInput()).toBeNull();

    const alt = await pressFromTerminal("F", { ...SEARCH, altKey: true });
    expect(alt.event.defaultPrevented).toBe(false);
    expect(searchInput()).toBeNull();

    const repeat = await pressFromTerminal("F", { ...SEARCH, repeat: true });
    expect(repeat.event.defaultPrevented).toBe(false);
    expect(searchInput()).toBeNull();

    const composing = await pressFromTerminal("F", { ...SEARCH, isComposing: true });
    expect(composing.event.defaultPrevented).toBe(false);
    expect(composing.targetSeen).toHaveBeenCalledTimes(1);
    expect(searchInput()).toBeNull();
  });

  it("prefills the terminal selection and jumps to the first match", async () => {
    harness.selection = "hello\nworld";
    await pressFromTerminal("F", SEARCH);

    const input = searchInput();
    expect(input).not.toBeNull();
    expect(input?.value).toBe("hello");
    await waitFor(() => expect(harness.findNext).toHaveBeenCalledWith("hello"));
  });

  it("refocuses the search input when the shortcut fires while search is open", async () => {
    await pressFromTerminal("F", SEARCH);
    const input = searchInput();
    expect(input).not.toBeNull();

    const ta = xtermTextarea();
    act(() => ta.focus());
    expect(document.activeElement).toBe(ta);

    keyDown(ta, "F", SEARCH);
    await flushTimers();
    expect(searchInput()).toBe(input);
    expect(document.activeElement).toBe(input);
  });

  it("does not intercept plain keys, other shortcuts or the search Escape flow", async () => {
    const plain = await pressFromTerminal("f");
    expect(plain.event.defaultPrevented).toBe(false);
    expect(plain.targetSeen).toHaveBeenCalledTimes(1);

    const palette = await pressFromTerminal("k", { ctrlKey: true });
    expect(palette.event.defaultPrevented).toBe(false);
    expect(searchInput()).toBeNull();

    await pressFromTerminal("F", SEARCH);
    const input = searchInput();
    expect(input).not.toBeNull();
    keyDown(input as HTMLInputElement, "Escape");
    await flush();
    expect(searchInput()).toBeNull();
  });

  it("shows the platform modifier in the terminal context menu accel", async () => {
    harness.selection = "hello";
    const body = mounted?.container.querySelector<HTMLElement>(".nx-terminal-body > div");
    if (!body) throw new Error("terminal body not found");
    act(() => {
      body.dispatchEvent(
        new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 20, clientY: 20 }),
      );
    });
    await flush();
    const menu = document.querySelector<HTMLElement>('[role="menu"]');
    expect(menu?.textContent).toContain("Ctrl+Shift+F");
  });
});
