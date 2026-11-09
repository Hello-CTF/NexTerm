/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, flushMacrotask, mount, setInputValue, waitForRendered, type MountedView } from "./reactTestUtils";

const harness = vi.hoisted(() => ({
  mac: false,
  selection: "",
  findNext: vi.fn(),
  findPrevious: vi.fn(),
  clearSearch: vi.fn(),
  focusTerminal: vi.fn(),
  copyText: vi.fn(),
  registerSearch: undefined as ((api: unknown) => void) | undefined,
  searchResults: undefined as ((results: { resultIndex: number; resultCount: number }) => void) | undefined,
}));

vi.mock("../../ui/clipboard", () => ({
  copyText: (text: string) => harness.copyText(text),
  readText: vi.fn().mockResolvedValue(""),
  isNativeWebKit: () => false,
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
      harness.searchResults = props.onSearchResults as typeof harness.searchResults;
      harness.registerSearch = props.registerSearch as typeof harness.registerSearch;
      useEffect(() => {
        (onHandleRef.current as ((h: unknown) => void) | undefined)?.({
          copyBlock: () => "",
          scrollToBlock: () => {},
          navigateBlock: () => null,
          clearBlocks: () => {},
          clear: () => {},
          getSelection: () => harness.selection,
          focus: harness.focusTerminal,
          fit: () => {},
          dimensions: () => ({ cols: 80, rows: 24 }),
        });
        (registerSearchRef.current as ((api: unknown) => void) | undefined)?.({
          findNext: harness.findNext,
          findPrevious: harness.findPrevious,
          clear: harness.clearSearch,
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
    await waitForRendered(() => expect(harness.findNext).toHaveBeenCalledWith("hello", expect.objectContaining({ regex: false })));
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

  async function openSearchWith(text: string): Promise<HTMLInputElement> {
    await pressFromTerminal("F", SEARCH);
    const input = searchInput();
    if (!input) throw new Error("search input not found");
    setInputValue(input, text);
    // The debounced search as you type runs once the term settles.
    await waitForRendered(() => expect(harness.findNext).toHaveBeenCalledWith(text, expect.anything()));
    harness.findNext.mockClear();
    harness.findPrevious.mockClear();
    return input;
  }

  function searchStatus(): string {
    return mounted?.container.querySelector('[data-testid="terminal-search-status"]')?.textContent ?? "";
  }

  function searchButton(label: string): HTMLButtonElement {
    const button = mounted?.container.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
    if (!button) throw new Error(`button not found: ${label}`);
    return button;
  }

  it("searches as the user types and steps with Enter and Shift+Enter", async () => {
    const input = await openSearchWith("needle");
    keyDown(input, "Enter");
    expect(harness.findNext).toHaveBeenCalledWith("needle", { caseSensitive: false, wholeWord: false, regex: false });
    keyDown(input, "Enter", { shiftKey: true });
    expect(harness.findPrevious).toHaveBeenCalledWith("needle", { caseSensitive: false, wholeWord: false, regex: false });
  });

  it("ignores the Enter that commits an IME composition", async () => {
    const input = await openSearchWith("中文");
    keyDown(input, "Enter", { isComposing: true });
    keyDown(input, "Enter", { keyCode: 229 });
    expect(harness.findNext).not.toHaveBeenCalled();
    expect(harness.findPrevious).not.toHaveBeenCalled();
  });

  it("shows the active match position, the match count and no results", async () => {
    await openSearchWith("needle");
    act(() => harness.searchResults?.({ resultIndex: 1, resultCount: 5 }));
    expect(searchStatus()).toBe("2/5");
    act(() => harness.searchResults?.({ resultIndex: -1, resultCount: 1000 }));
    expect(searchStatus()).toBe("1000+ 个匹配");
    act(() => harness.searchResults?.({ resultIndex: -1, resultCount: 0 }));
    expect(searchStatus()).toBe("无结果");
  });

  it("closes with Escape, clears the highlights and returns focus to the terminal", async () => {
    const input = await openSearchWith("needle");
    harness.clearSearch.mockClear();
    keyDown(input, "Escape");
    await flush();
    expect(searchInput()).toBeNull();
    expect(harness.clearSearch).toHaveBeenCalled();
    expect(harness.focusTerminal).toHaveBeenCalled();
  });

  it("re-runs the search with the case, whole word and regex toggles", async () => {
    await openSearchWith("Needle");
    const caseButton = searchButton("区分大小写");
    click(caseButton);
    expect(caseButton.getAttribute("aria-pressed")).toBe("true");
    await waitForRendered(() =>
      expect(harness.findNext).toHaveBeenCalledWith("Needle", { caseSensitive: true, wholeWord: false, regex: false }),
    );
    click(searchButton("全字匹配"));
    await waitForRendered(() =>
      expect(harness.findNext).toHaveBeenCalledWith("Needle", { caseSensitive: true, wholeWord: true, regex: false }),
    );
    click(searchButton("正则表达式"));
    await waitForRendered(() =>
      expect(harness.findNext).toHaveBeenCalledWith("Needle", { caseSensitive: true, wholeWord: true, regex: true }),
    );
  });

  it("reports an invalid regular expression instead of searching", async () => {
    const input = await openSearchWith("a(");
    click(searchButton("正则表达式"));
    await flush();
    expect(searchStatus()).toBe("正则表达式无效");
    expect(input.getAttribute("aria-invalid")).toBe("true");
    keyDown(input, "Enter");
    await new Promise((resolve) => setTimeout(resolve, 200));
    await flush();
    expect(harness.findNext).not.toHaveBeenCalled();
  });

  it("folds the option toggles into one popover on narrow panes and lets the status truncate", async () => {
    await openSearchWith("needle");
    const inline = mounted?.container.querySelector<HTMLElement>('[data-testid="terminal-search-flags"]');
    expect(inline?.className).toContain("@max-[560px]:hidden");
    const options = searchButton("搜索选项");
    const optionsHost = options.parentElement as HTMLElement;
    expect(optionsHost.className).toContain("hidden");
    expect(optionsHost.className).toContain("@max-[560px]:block");
    expect(options.getAttribute("aria-expanded")).toBe("false");

    click(options);
    const popover = mounted?.container.querySelector<HTMLElement>('[data-testid="terminal-search-options"]');
    expect(options.getAttribute("aria-expanded")).toBe("true");
    const regex = popover?.querySelector<HTMLButtonElement>('button[aria-label="正则表达式"]');
    expect(popover?.querySelectorAll("button[aria-pressed]")).toHaveLength(3);
    if (!regex) throw new Error("regex toggle not found in popover");
    click(regex);
    expect(regex.getAttribute("aria-pressed")).toBe("true");
    await waitForRendered(() =>
      expect(harness.findNext).toHaveBeenCalledWith("needle", { caseSensitive: false, wholeWord: false, regex: true }),
    );

    act(() => {
      document.body.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    });
    expect(mounted?.container.querySelector('[data-testid="terminal-search-options"]')).toBeNull();

    act(() => harness.searchResults?.({ resultIndex: 0, resultCount: 7 }));
    const status = mounted?.container.querySelector<HTMLElement>('[data-testid="terminal-search-status"]');
    expect(status?.className).toContain("truncate");
    expect(status?.className).toContain("min-w-0");
  });

  it("searches a rebuilt terminal again instead of keeping the old count", async () => {
    await openSearchWith("needle");
    act(() => harness.searchResults?.({ resultIndex: 2, resultCount: 12 }));
    expect(searchStatus()).toBe("3/12");
    const rebuilt = { findNext: vi.fn(), findPrevious: vi.fn(), clear: vi.fn() };
    act(() => harness.registerSearch?.(rebuilt));
    expect(rebuilt.findNext).toHaveBeenCalledWith("needle", { caseSensitive: false, wholeWord: false, regex: false });

    keyDown(searchInput() as HTMLInputElement, "Escape");
    await flush();
    act(() => harness.searchResults?.({ resultIndex: 0, resultCount: 4 }));
    const closedRebuild = { findNext: vi.fn(), findPrevious: vi.fn(), clear: vi.fn() };
    act(() => harness.registerSearch?.(closedRebuild));
    expect(closedRebuild.findNext).not.toHaveBeenCalled();
    await pressFromTerminal("F", SEARCH);
    // The reopened bar shows no count until the new terminal reports one.
    expect(searchStatus()).toBe("");
  });

  it("steps to the next match when the selection search repeats the shown term", async () => {
    harness.selection = "hello";
    await pressFromTerminal("F", SEARCH);
    await waitForRendered(() => expect(harness.findNext).toHaveBeenCalledWith("hello", expect.anything()));
    harness.findNext.mockClear();
    harness.clearSearch.mockClear();

    await pressFromTerminal("F", SEARCH);
    expect(harness.findNext).toHaveBeenCalledTimes(1);
    expect(harness.clearSearch).not.toHaveBeenCalled();
  });

  it("shows an error toast when copying the selection fails", async () => {
    harness.selection = "hello";
    harness.copyText.mockRejectedValue(new Error("denied"));
    useUi.setState({ toasts: [] });
    const body = mounted?.container.querySelector<HTMLElement>(".nx-terminal-body > div");
    if (!body) throw new Error("terminal body not found");
    act(() => {
      body.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 20, clientY: 20 }));
    });
    await flush();
    const copy = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
      (item) => item.textContent?.trim().startsWith("复制") && !item.textContent.includes("链接"),
    );
    if (!copy) throw new Error("copy menu item not found");
    click(copy);
    await waitForRendered(() =>
      expect(useUi.getState().toasts.some((toast) => toast.kind === "error" && toast.text === "复制失败，请重试")).toBe(true),
    );
    expect(harness.copyText).toHaveBeenCalledWith("hello");
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
