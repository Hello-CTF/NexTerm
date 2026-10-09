/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  listLive: vi.fn(),
  write: vi.fn(),
  promptText: vi.fn(),
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
  sessionApi: { probeHostKey: vi.fn().mockResolvedValue({ state: "known" }) },
  terminalApi: {
    listLive: mocks.listLive,
    write: mocks.write,
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
  },
  fsApi: { write: vi.fn() },
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

vi.mock("../../ipc/env", () => ({
  clientId: () => "me",
  WEB: false,
}));

vi.mock("../../ui/dialogs", () => ({
  describeTarget: () => "",
  finishSave: vi.fn(),
  pickSavePath: vi.fn(),
  promptText: mocks.promptText,
  ask: vi.fn(),
}));

vi.mock("../../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

vi.mock("../../features/terminal/XtermView", async () => {
  const { useEffect } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
      useEffect(() => {
        (props.onHandle as ((handle: unknown) => void) | undefined)?.({
          copyBlock: () => "",
          scrollToBlock: () => {},
          navigateBlock: () => null,
          clearBlocks: () => {},
          clear: () => {},
          getSelection: () => "",
          focus: () => {},
          fit: () => {},
          paste: () => {},
          dimensions: () => ({ cols: 80, rows: 24 }),
        });
        (props.onAttach as ((id: string) => void) | undefined)?.("kernel-1");
      }, []);
      return <div data-testid="xterm-mock" />;
    },
  };
});

import { TerminalPane } from "../../features/terminal/TerminalPane";
import { useUi, type AppTab } from "../../app/store";

let mounted: MountedView | undefined;

function seedTab() {
  const tab: AppTab = {
    id: "t1",
    kind: "terminal",
    title: "web-01",
    sessionId: "s1",
    tabId: "kernel-1",
    closable: true,
  };
  useUi.setState({
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "web-01",
        sessionId: "s1",
        assetId: "asset-1",
        assetKind: "ssh",
        panes: [{ id: "p1", tabs: [tab], activeTabId: tab.id }],
        activePaneId: "p1",
        splitRatio: 0.5,
        closable: true,
      },
    ],
    activeWorkspaceId: "ws1",
    sessions: [
      {
        id: "s1",
        assetId: "asset-1",
        name: "web-01",
        kind: "ssh",
        status: "connected",
        tabs: [],
        createdAt: 0,
      },
    ],
    toasts: [],
  });
}

function toastTexts(): string[] {
  return useUi.getState().toasts.map((t) => `${t.kind}:${t.text}`);
}

async function clickMenuItem(label: string): Promise<void> {
  const body = document.querySelector<HTMLElement>(".nx-terminal-body .relative");
  if (!body) throw new Error("terminal body not found");
  act(() => {
    body.dispatchEvent(
      new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 20, clientY: 20 }),
    );
  });
  await flush();
  const item = [...document.querySelectorAll<HTMLElement>(".nx-menu .nx-menu-item")].find(
    (candidate) => candidate.textContent?.includes(label),
  );
  if (!item) throw new Error(`menu item ${label} not found`);
  act(() => item.dispatchEvent(new MouseEvent("click", { bubbles: true })));
}

describe("TerminalPane 命令输入的真实发送反馈", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    mocks.listLive.mockResolvedValue([]);
    mocks.write.mockResolvedValue(undefined);
    mocks.promptText.mockResolvedValue("echo hi");
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
    useUi.setState({ toasts: [] });
  });

  it("真正写出后才提示命令已发送", async () => {
    seedTab();
    mounted = mount(
      createElement(TerminalPane, {
        sessionId: "s1",
        title: "web-01",
        storeTabId: "t1",
        resumeTabId: "kernel-1",
      }),
    );
    await flush();

    await clickMenuItem("命令输入");
    await waitFor(() => expect(toastTexts()).toContain("success:命令已发送"));

    expect(mocks.write).toHaveBeenCalledTimes(1);
    const [tabId, data] = mocks.write.mock.calls[0] as [string, Uint8Array];
    expect(tabId).toBe("kernel-1");
    expect(new TextDecoder().decode(data)).toBe("echo hi\n");
  });

  it("观察者发送时不提示已发送，只提示接管", async () => {
    seedTab();
    mocks.listLive.mockResolvedValue([
      {
        tabId: "kernel-1",
        sessionId: "s1",
        sessionName: "web-01",
        sessionKind: "ssh",
        cols: 80,
        rows: 24,
        controller: "other-device",
        subscribers: 2,
        viewers: 2,
        exited: false,
        lastOutputMsAgo: 0,
      },
    ]);
    mounted = mount(
      createElement(TerminalPane, {
        sessionId: "s1",
        title: "web-01",
        storeTabId: "t1",
        resumeTabId: "kernel-1",
      }),
    );
    await flush();

    await clickMenuItem("命令输入");
    await waitFor(() =>
      expect(toastTexts().some((t) => t.includes("终端正由其他设备操作"))).toBe(true),
    );

    expect(mocks.write).not.toHaveBeenCalled();
    expect(toastTexts()).not.toContain("success:命令已发送");
  });

  it("写出失败时展示真实错误，不提示已发送", async () => {
    seedTab();
    mocks.write.mockRejectedValue(new Error("boom"));
    mounted = mount(
      createElement(TerminalPane, {
        sessionId: "s1",
        title: "web-01",
        storeTabId: "t1",
        resumeTabId: "kernel-1",
      }),
    );
    await flush();

    await clickMenuItem("命令输入");
    await waitFor(() =>
      expect(toastTexts().some((t) => t.startsWith("error:写入终端失败："))).toBe(true),
    );

    expect(toastTexts().some((t) => t.includes("boom"))).toBe(true);
    expect(toastTexts()).not.toContain("success:命令已发送");
  });
});
