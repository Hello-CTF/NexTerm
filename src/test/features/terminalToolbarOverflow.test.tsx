/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const harness = vi.hoisted(() => ({
  mac: false,
  switchEncoding: vi.fn(),
  listLive: vi.fn(),
  recordStart: vi.fn(),
  recordStop: vi.fn(),
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
    listLive: harness.listLive,
    write: vi.fn().mockResolvedValue(undefined),
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
    switchEncoding: harness.switchEncoding,
    claim: vi.fn().mockResolvedValue(null),
    recordStart: harness.recordStart,
    recordStop: harness.recordStop,
    exportLog: vi.fn().mockResolvedValue(0),
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

vi.mock("../../ipc/env", () => ({ clientId: () => "me" }));

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
        (props.registerSearch as ((api: unknown) => void) | undefined)?.({
          findNext: () => {},
          findPrevious: () => {},
        });
        (props.onAttach as ((id: string) => void) | undefined)?.("kernel-1");
      }, []);
      return <div className="h-full w-full min-h-0" />;
    },
  };
});

import { TerminalPane } from "../../features/terminal/TerminalPane";
import { useUi } from "../../app/store";
import { finishSave, pickSavePath } from "../../ui/dialogs";

let mounted: MountedView | undefined;

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function overflowButton(): HTMLButtonElement {
  const button = mounted?.container.querySelector<HTMLButtonElement>(
    'button[aria-label="更多终端操作"]',
  );
  if (!button) throw new Error("toolbar overflow button not found");
  return button;
}

function menuLabels(): string[] {
  return [...document.querySelectorAll('[role="menuitem"]')]
    .filter((el) => el.closest(".nx-submenu") === null)
    .map((b) => b.querySelector(".nx-menu-label")?.textContent ?? b.textContent ?? "");
}

function clickMenuItem(label: string): void {
  const item = [...document.querySelectorAll('[role="menuitem"]')].find((b) =>
    b.querySelector(".nx-menu-label")?.textContent?.includes(label),
  );
  if (!item) throw new Error(`menu item not found: ${label}`);
  click(item);
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  harness.mac = false;
  harness.listLive.mockResolvedValue([]);
  harness.switchEncoding.mockResolvedValue(undefined);
  harness.recordStart.mockResolvedValue(undefined);
  harness.recordStop.mockResolvedValue(0);
  vi.mocked(pickSavePath).mockResolvedValue(null);
  vi.mocked(finishSave).mockResolvedValue(null);
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
});

describe("terminal toolbar overflow", () => {
  it("keeps every command in the overflow menu on narrow screens", async () => {
    await flush();
    click(overflowButton());
    await flush();

    expect(menuLabels()).toEqual([
      "搜索终端内容",
      "连续录制到文件…",
      "定位到上一条命令",
      "定位到下一条命令",
      "命令块",
      "广播到当前分屏",
      "广播到全部终端",
      "选择广播目标…",
      "字符编码",
    ]);
    await waitFor(() =>
      expect(document.activeElement?.textContent).toContain("搜索终端内容"),
    );
  });

  it("keeps the wide toolbar free of the encoding select and the record button", async () => {
    await flush();
    const toolbar = mounted!.container.querySelector(".nx-toolbar");
    if (!toolbar) throw new Error("toolbar not found");
    expect(toolbar.querySelector("select")).toBeNull();
    const record = [...toolbar.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
      b.textContent?.includes("录制"),
    );
    expect(record).toBeUndefined();
    const encodingBadge = [...toolbar.querySelectorAll(".nx-badge")].find((b) =>
      /utf-8|gbk|gb18030|big5|latin1/.test(b.textContent ?? ""),
    );
    expect(encodingBadge).toBeUndefined();
    const search = toolbar.querySelector<HTMLButtonElement>('button[title^="搜索终端内容"]');
    expect(search?.className).toContain("max-[560px]:hidden");
    const blocks = [...toolbar.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
      b.textContent?.includes("命令块"),
    );
    expect(blocks?.className).toContain("max-[560px]:hidden");
    expect(overflowButton().className).not.toContain("hidden");
  });

  it("opens terminal search from the overflow menu", async () => {
    await flush();
    click(overflowButton());
    await flush();
    clickMenuItem("搜索终端内容");
    await flush();
    const input = mounted!.container.querySelector<HTMLInputElement>(
      'input[placeholder="搜索终端内容…"]',
    );
    expect(input).not.toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(input));
    expect(document.querySelector('[role="menu"]')).toBeNull();
  });

  it("switches encoding through the overflow submenu", async () => {
    await flush();
    click(overflowButton());
    await flush();
    clickMenuItem("字符编码");
    await flush();

    const submenu = document.querySelector(".nx-submenu");
    expect(submenu).not.toBeNull();
    const labels = [...submenu!.querySelectorAll('[role="menuitem"]')].map(
      (b) => b.querySelector(".nx-menu-label")?.textContent,
    );
    expect(labels).toEqual(["utf-8", "gbk", "gb18030", "big5", "latin1"]);
    const current = [...submenu!.querySelectorAll('[role="menuitem"]')].find((b) =>
      b.textContent?.includes("当前"),
    );
    expect(current?.textContent).toContain("utf-8");

    clickMenuItem("gbk");
    await waitFor(() =>
      expect(harness.switchEncoding).toHaveBeenCalledWith("kernel-1", "gbk"),
    );
    expect(document.querySelector('[role="menu"]')).toBeNull();

    const badge = [...mounted!.container.querySelectorAll(".nx-toolbar .nx-badge")].find((b) =>
      b.textContent?.includes("gbk"),
    );
    expect(badge).not.toBeUndefined();
  });

  it("navigates the overflow menu with arrow keys and closes with Escape", async () => {
    await flush();
    click(overflowButton());
    await flush();
    await waitFor(() =>
      expect(document.activeElement?.textContent).toContain("搜索终端内容"),
    );

    keyDown(document.activeElement as Element, "ArrowDown");
    await flush();
    expect(document.activeElement?.textContent).toContain("连续录制到文件…");

    keyDown(document.activeElement as Element, "ArrowUp");
    await flush();
    expect(document.activeElement?.textContent).toContain("搜索终端内容");

    keyDown(document.activeElement as Element, "Escape");
    await flush();
    expect(document.querySelector('[role="menu"]')).toBeNull();
  });

  it("shows a persistent recording indicator with a direct stop action while recording", async () => {
    vi.mocked(pickSavePath).mockResolvedValue("/tmp/term-1.log");
    vi.mocked(finishSave).mockResolvedValue("/tmp/term-1.log");
    await flush();
    click(overflowButton());
    await flush();
    clickMenuItem("连续录制到文件…");
    await waitFor(() =>
      expect(harness.recordStart).toHaveBeenCalledWith("kernel-1", "/tmp/term-1.log"),
    );

    const indicator = [
      ...mounted!.container.querySelectorAll<HTMLButtonElement>(".nx-toolbar button"),
    ].find((b) => b.textContent?.includes("录制中"));
    expect(indicator).not.toBeUndefined();
    expect(indicator!.getAttribute("aria-label")).toBe("停止录制");

    click(indicator!);
    await waitFor(() => expect(harness.recordStop).toHaveBeenCalledWith("kernel-1"));
    await flush();
    expect(
      [...mounted!.container.querySelectorAll(".nx-toolbar button")].some((b) =>
        b.textContent?.includes("录制中"),
      ),
    ).toBe(false);
  });
});
