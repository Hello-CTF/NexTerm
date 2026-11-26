/** @vitest-environment jsdom */

// The terminal pane marks input the AI typed and sends a selected range to the AI composer.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, mount, type MountedView } from "./reactTestUtils";

const harness = vi.hoisted(() => ({
  handlers: new Map<string, (payload: unknown) => void>(),
  resyncSubs: new Set<() => void>(),
  sessionList: vi.fn(),
  xtermProps: null as Record<string, unknown> | null,
  selection: "",
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
    EVENTS: {
      terminalControl: "terminal://control",
      terminalThrottled: "terminal://throttled",
      terminalInputSource: "terminal://input-source",
    },
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
      const onChannelRef = useRef(props.onChannel);
      onChannelRef.current = props.onChannel;
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
        (onAttachRef.current as ((id: string) => void) | undefined)?.("kernel-1");
        (onChannelRef.current as ((id: string) => void) | undefined)?.("a-1");
      }, []);
      return <div className="h-full w-full min-h-0" />;
    },
  };
});

import { AI_INPUT_MARK_MS, TerminalPane } from "../../features/terminal/TerminalPane";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;

function aiMark(): HTMLElement | null {
  return mounted?.container.querySelector<HTMLElement>("[data-ai-input-mark]") ?? null;
}

function terminalBody(): HTMLElement {
  const body = mounted?.container.querySelector<HTMLElement>(".nx-terminal-body > div");
  if (!body) throw new Error("terminal body not found");
  return body;
}

beforeEach(async () => {
  vi.clearAllMocks();
  harness.handlers.clear();
  harness.selection = "";
  document.body.replaceChildren();
  harness.sessionList.mockImplementation(async () => useUi.getState().sessions);
  useUi.setState({
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
    aiPendingRefs: [],
    rightOpen: false,
    toasts: [],
  });
  mounted = mount(createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }));
  await flush();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
});

describe("AI input marker", () => {
  it("marks AI input on this terminal only, and clears after a short while", async () => {
    vi.useFakeTimers();
    const inputSource = harness.handlers.get("terminal://input-source");
    expect(inputSource).toBeDefined();

    act(() => inputSource?.({ tabId: "kernel-2", source: "ai" }));
    expect(aiMark()).toBeNull();

    act(() => inputSource?.({ tabId: "kernel-1", source: "ai" }));
    expect(aiMark()?.textContent).toContain("AI 正在输入");

    await act(async () => {
      vi.advanceTimersByTime(AI_INPUT_MARK_MS - 1_000);
    });
    // Further AI input keeps the marker up.
    act(() => inputSource?.({ tabId: "kernel-1", source: "ai" }));
    await act(async () => {
      vi.advanceTimersByTime(AI_INPUT_MARK_MS - 1_000);
    });
    expect(aiMark()).not.toBeNull();

    await act(async () => {
      vi.advanceTimersByTime(1_000);
    });
    expect(aiMark()).toBeNull();
  });

  it("ignores other input sources", () => {
    const inputSource = harness.handlers.get("terminal://input-source");
    act(() => inputSource?.({ tabId: "kernel-1", source: "guest" }));
    expect(aiMark()).toBeNull();
  });
});

describe("ask AI about the selection", () => {
  it("adds the selection from the context menu and opens the AI sidebar", async () => {
    harness.selection = "  Permission denied\n";
    act(() => {
      terminalBody().dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 20, clientY: 20 }));
    });
    await flush();
    const item = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) =>
      el.textContent?.includes("向 AI 询问选中内容"),
    );
    if (!item) throw new Error("ask AI menu item not found");
    expect(item.textContent).toContain("Ctrl+Shift+Alt+A");
    click(item);
    await flush();
    const refs = useUi.getState().aiPendingRefs;
    expect(refs).toHaveLength(1);
    expect(refs[0]).toMatchObject({ kind: "text", text: "Permission denied", selection: true, sourceTabId: "t1" });
    expect(useUi.getState().rightOpen).toBe(true);
  });

  it("adds the selection with the keyboard shortcut, and says so when nothing is selected", () => {
    const chord: KeyboardEventInit = {
      key: "A",
      code: "KeyA",
      ctrlKey: true,
      shiftKey: true,
      altKey: true,
      bubbles: true,
      cancelable: true,
    };
    act(() => {
      terminalBody().dispatchEvent(new KeyboardEvent("keydown", chord));
    });
    expect(useUi.getState().aiPendingRefs).toEqual([]);
    expect(useUi.getState().toasts.some((toast) => toast.text === "请先在终端中选中要询问的内容")).toBe(true);

    harness.selection = "exit status 127";
    const event = new KeyboardEvent("keydown", chord);
    act(() => {
      terminalBody().dispatchEvent(event);
    });
    expect(event.defaultPrevented).toBe(true);
    expect(useUi.getState().aiPendingRefs.map((ref) => ref.text)).toEqual(["exit status 127"]);
  });
});
