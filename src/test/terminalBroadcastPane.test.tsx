/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, type MountedView } from "./features/reactTestUtils";

const harness = vi.hoisted(() => ({
  handlers: new Map<string, (payload: unknown) => void>(),
  resyncSubs: new Set<() => void>(),
  xtermProps: null as Record<string, unknown> | null,
  listLive: vi.fn(),
  write: vi.fn(),
  ask: vi.fn(),
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
  sessionApi: { connect: vi.fn(), reconnect: vi.fn(), disconnect: vi.fn() },
  terminalApi: {
    listLive: harness.listLive,
    write: harness.write,
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
    closeTab: vi.fn().mockResolvedValue(undefined),
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
  DEMO: false,
  WEB: false,
  DESKTOP: true,
}));

vi.mock("../ui/dialogs", () => ({
  describeTarget: () => "",
  finishSave: vi.fn(),
  pickSavePath: vi.fn(),
  promptText: vi.fn(),
  ask: harness.ask,
}));

vi.mock("../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

vi.mock("../features/terminal/XtermView", async () => {
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
          paste: () => {},
          dimensions: () => ({ cols: 80, rows: 24 }),
        });
        (props.onAttach as ((id: string) => void) | undefined)?.("kernel-1");
      }, []);
      return <div className="h-full w-full min-h-0" />;
    },
  };
});

import { TerminalPane } from "../features/terminal/TerminalPane";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;

function session(id: string, kind: string) {
  return { id, assetId: `a-${id}`, name: id, kind, status: "connected" as const, tabs: [], createdAt: 0 };
}

function termTab(id: string, sessionId: string, kernelTabId: string, patch: Record<string, unknown> = {}) {
  return {
    id,
    kind: "terminal" as const,
    title: id,
    sessionId,
    tabId: kernelTabId,
    closable: true,
    ...patch,
  };
}

function seedWorkspace(): void {
  useUi.setState({
    sessions: [session("s1", "ssh"), session("s2", "ssh"), session("s3", "local")],
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
            tabs: [termTab("t1", "s1", "kernel-1"), termTab("t2", "s2", "kernel-2")],
          },
          {
            id: "p2",
            activeTabId: "t3",
            tabs: [termTab("t3", "s3", "kernel-3")],
          },
        ],
        activePaneId: "p1",
        splitRatio: 0.5,
      },
    ],
    activeWorkspaceId: "ws1",
    broadcast: null,
  });
}

function mountPane(): MountedView {
  return mount(createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }));
}

function sendInput(data: string): void {
  act(() => {
    (harness.xtermProps?.onData as ((d: string) => void) | undefined)?.(data);
  });
}

function bodyText(): string {
  return document.body.textContent ?? "";
}

function toastTexts(): string[] {
  return useUi.getState().toasts.map((t) => t.text);
}

function writtenTabIds(): string[] {
  return (harness.write.mock.calls as [string, Uint8Array][]).map(([id]) => id);
}

beforeEach(() => {
  vi.clearAllMocks();
  harness.handlers.clear();
  harness.resyncSubs.clear();
  document.body.replaceChildren();
  harness.listLive.mockResolvedValue([]);
  harness.write.mockResolvedValue(undefined);
  harness.ask.mockResolvedValue(true);
  seedWorkspace();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("terminal command broadcast", () => {
  it("sends input to a single terminal by default", async () => {
    mounted = mountPane();
    await flush();
    sendInput("ls\n");
    await flush();
    expect(writtenTabIds()).toEqual(["kernel-1"]);
    expect(bodyText()).not.toContain("广播");
  });

  it("fans input out to every broadcast target and shows the persistent indicators", async () => {
    useUi.getState().setBroadcast({ workspaceId: "ws1", targetIds: ["t1", "t2", "t3"] });
    mounted = mountPane();
    await flush();
    expect(bodyText()).toContain("广播 3");
    expect(bodyText()).toContain("输入将同时发送到 3 个终端");
    sendInput("ls\n");
    await flush();
    expect(writtenTabIds()).toEqual(["kernel-1", "kernel-2", "kernel-3"]);
  });

  it("skips targets controlled by other devices and reports them", async () => {
    harness.write.mockImplementation((tabId: string) =>
      tabId === "kernel-2" ? Promise.reject({ code: "not_controller" }) : Promise.resolve(),
    );
    useUi.getState().setBroadcast({ workspaceId: "ws1", targetIds: ["t1", "t2"] });
    mounted = mountPane();
    await flush();
    sendInput("ls\n");
    await flush();
    expect(harness.write).toHaveBeenCalledTimes(2);
    expect(toastTexts().some((t) => t.includes("跳过 1 个") && t.includes("正被其他设备控制"))).toBe(true);
  });

  it("skips dead targets without writing to them", async () => {
    useUi.getState().setBroadcast({ workspaceId: "ws1", targetIds: ["t1", "t2"] });
    useUi.getState().updateTab("t2", { dead: true });
    mounted = mountPane();
    await flush();
    expect(bodyText()).toContain("1 个目标当前不可写");
    sendInput("ls\n");
    await flush();
    expect(writtenTabIds()).toEqual(["kernel-1"]);
    expect(toastTexts().some((t) => t.includes("终端已失效"))).toBe(true);
  });

  it("does not broadcast when typing in a tab outside the target set", async () => {
    useUi.getState().setBroadcast({ workspaceId: "ws1", targetIds: ["t2", "t3"] });
    mounted = mountPane();
    await flush();
    expect(bodyText()).toContain("广播 2");
    expect(bodyText()).not.toContain("输入将同时发送到");
    sendInput("ls\n");
    await flush();
    expect(writtenTabIds()).toEqual(["kernel-1"]);
  });

  it("restores single-terminal input after broadcast is turned off", async () => {
    useUi.getState().setBroadcast({ workspaceId: "ws1", targetIds: ["t1", "t2"] });
    mounted = mountPane();
    await flush();
    const badge = [...document.querySelectorAll<HTMLElement>("button")].find(
      (el) => el.textContent?.trim() === "广播 2",
    );
    expect(badge).toBeDefined();
    act(() => {
      badge?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await flush();
    expect(useUi.getState().broadcast).toBeNull();
    expect(bodyText()).not.toContain("广播 2");
    sendInput("ls\n");
    await flush();
    expect(writtenTabIds()).toEqual(["kernel-1"]);
  });

  it("does not broadcast from a terminal controlled by another device", async () => {
    useUi.getState().setBroadcast({ workspaceId: "ws1", targetIds: ["t1", "t2"] });
    mounted = mountPane();
    await flush();
    const handler = harness.handlers.get("terminal://control");
    expect(handler).toBeDefined();
    act(() => {
      handler?.({
        tabId: "kernel-1",
        version: 2,
        controller: "other",
        subscribers: 2,
        viewers: 2,
        exited: false,
        cols: 80,
        rows: 24,
        gridRevision: 0,
      });
    });
    sendInput("ls\n");
    await flush();
    expect(harness.write).not.toHaveBeenCalled();
  });
});

describe("broadcast enable flow", () => {
  async function openOverflowMenu(): Promise<void> {
    const overflow = [...document.querySelectorAll<HTMLElement>("button")].find(
      (el) => el.getAttribute("aria-label") === "更多终端操作",
    );
    expect(overflow).toBeDefined();
    act(() => {
      overflow?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await flush();
  }

  function clickMenuItem(label: string): void {
    const labelEl = [...document.querySelectorAll<HTMLElement>(".nx-menu-label")].find(
      (el) => el.textContent?.trim() === label,
    );
    const item = labelEl?.closest("button");
    expect(item).toBeDefined();
    act(() => {
      item?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
  }

  it("asks for risk confirmation before enabling and stays off when declined", async () => {
    harness.ask.mockResolvedValue(false);
    mounted = mountPane();
    await flush();
    await openOverflowMenu();
    clickMenuItem("广播到全部终端");
    await flush();
    expect(harness.ask).toHaveBeenCalledTimes(1);
    expect(harness.ask.mock.calls[0]?.[0]).toContain("3 个终端");
    expect(harness.ask.mock.calls[0]?.[0]).toContain("本地终端（1 个）与远程终端（2 个）");
    expect(useUi.getState().broadcast).toBeNull();
  });

  it("enables broadcast for the whole workspace after confirmation", async () => {
    mounted = mountPane();
    await flush();
    await openOverflowMenu();
    clickMenuItem("广播到全部终端");
    await flush();
    expect(useUi.getState().broadcast).toEqual({
      workspaceId: "ws1",
      targetIds: ["t1", "t2", "t3"],
    });
  });

  it("enables broadcast for the current pane only via quick pick", async () => {
    mounted = mountPane();
    await flush();
    await openOverflowMenu();
    clickMenuItem("广播到当前分屏");
    await flush();
    expect(useUi.getState().broadcast).toEqual({
      workspaceId: "ws1",
      targetIds: ["t1", "t2"],
    });
  });

  it("refuses quick pick when fewer than two terminals are eligible", async () => {
    useUi.setState({
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
              tabs: [termTab("t1", "s1", "kernel-1")],
            },
          ],
          activePaneId: "p1",
          splitRatio: 0.5,
        },
      ],
    });
    mounted = mountPane();
    await flush();
    await openOverflowMenu();
    clickMenuItem("广播到全部终端");
    await flush();
    expect(harness.ask).not.toHaveBeenCalled();
    expect(useUi.getState().broadcast).toBeNull();
    expect(toastTexts().some((t) => t.includes("没有至少 2 个可广播的终端"))).toBe(true);
  });

  it("opens the picker, quick-selects all terminals and applies them", async () => {
    mounted = mountPane();
    await flush();
    await openOverflowMenu();
    clickMenuItem("选择广播目标…");
    await flush();
    expect(bodyText()).toContain("命令广播目标");
    const allButton = [...document.querySelectorAll<HTMLElement>("button")].find(
      (el) => el.textContent?.trim() === "全部终端",
    );
    expect(allButton).toBeDefined();
    act(() => {
      allButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await flush();
    expect(bodyText()).toContain("已选 3 个");
    const applyButton = [...document.querySelectorAll<HTMLElement>("button")].find((el) =>
      el.textContent?.trim().startsWith("开启广播"),
    );
    expect(applyButton?.textContent?.trim()).toBe("开启广播（3）");
    act(() => {
      applyButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await flush();
    expect(harness.ask).toHaveBeenCalledTimes(1);
    expect(useUi.getState().broadcast).toEqual({
      workspaceId: "ws1",
      targetIds: ["t1", "t2", "t3"],
    });
  });
});

describe("broadcast input serialization", () => {
  interface WriteGate {
    id: string;
    data: string;
    settled: boolean;
    resolve: () => void;
  }

  function gateWrites(): WriteGate[] {
    const gates: WriteGate[] = [];
    harness.write.mockImplementation((id: string, bytes: Uint8Array) => {
      const gate: WriteGate = { id, data: new TextDecoder().decode(bytes), settled: false, resolve: () => {} };
      gates.push(gate);
      return new Promise<void>((resolve) => {
        gate.resolve = () => {
          gate.settled = true;
          resolve();
        };
      });
    });
    return gates;
  }

  async function releaseOldest(gates: WriteGate[]): Promise<void> {
    const gate = gates.find((g) => !g.settled);
    if (!gate) throw new Error("no pending write to release");
    gate.resolve();
    await flush();
  }

  async function drainWrites(gates: WriteGate[]): Promise<void> {
    for (let i = 0; i < 50; i++) {
      await flush();
      if (gates.every((g) => g.settled)) return;
      await releaseOldest(gates);
    }
    throw new Error("writes did not drain");
  }

  function gatePairs(gates: WriteGate[]): [string, string][] {
    return gates.map((g) => [g.id, g.data]);
  }

  it("delivers consecutive keystrokes to every target in the same order", async () => {
    useUi.getState().setBroadcast({ workspaceId: "ws1", targetIds: ["t1", "t2", "t3"] });
    const gates = gateWrites();
    mounted = mountPane();
    await flush();
    sendInput("a");
    await flush();
    expect(gates.map((g) => g.id)).toEqual(["kernel-1"]);
    sendInput("b");
    await flush();
    expect(gates).toHaveLength(1);
    while (gates.length < 6) {
      await releaseOldest(gates);
    }
    expect(gatePairs(gates)).toEqual([
      ["kernel-1", "a"],
      ["kernel-2", "a"],
      ["kernel-3", "a"],
      ["kernel-1", "b"],
      ["kernel-2", "b"],
      ["kernel-3", "b"],
    ]);
  });

  it("drops queued input when broadcast is closed and does not block later input", async () => {
    useUi.getState().setBroadcast({ workspaceId: "ws1", targetIds: ["t1", "t2"] });
    const gates = gateWrites();
    mounted = mountPane();
    await flush();
    sendInput("a");
    await flush();
    expect(gatePairs(gates)).toEqual([["kernel-1", "a"]]);
    useUi.getState().setBroadcast(null);
    sendInput("b");
    await flush();
    expect(gatePairs(gates)).toEqual([
      ["kernel-1", "a"],
      ["kernel-1", "b"],
    ]);
    await drainWrites(gates);
    expect(gates.map((g) => g.data)).toEqual(["a", "b", "a"]);
    useUi.getState().setBroadcast({ workspaceId: "ws1", targetIds: ["t1", "t2"] });
    sendInput("c");
    await drainWrites(gates);
    expect(gatePairs(gates)).toEqual([
      ["kernel-1", "a"],
      ["kernel-1", "b"],
      ["kernel-2", "a"],
      ["kernel-1", "c"],
      ["kernel-2", "c"],
    ]);
  });

  it("drops queued input when the target set changes and sends new input to the new targets", async () => {
    useUi.getState().setBroadcast({ workspaceId: "ws1", targetIds: ["t1", "t2", "t3"] });
    const gates = gateWrites();
    mounted = mountPane();
    await flush();
    sendInput("a");
    await flush();
    expect(gatePairs(gates)).toEqual([["kernel-1", "a"]]);
    sendInput("b");
    useUi.getState().setBroadcast({ workspaceId: "ws1", targetIds: ["t1", "t2"] });
    await flush();
    expect(gates).toHaveLength(1);
    await drainWrites(gates);
    expect(gates.map((g) => g.data)).toEqual(["a", "a", "a"]);
    sendInput("c");
    await drainWrites(gates);
    expect(gatePairs(gates)).toEqual([
      ["kernel-1", "a"],
      ["kernel-2", "a"],
      ["kernel-3", "a"],
      ["kernel-1", "c"],
      ["kernel-2", "c"],
    ]);
  });

  it("keeps non-broadcast writes unserialized", async () => {
    const gates = gateWrites();
    mounted = mountPane();
    await flush();
    sendInput("a");
    sendInput("b");
    await flush();
    expect(gatePairs(gates)).toEqual([
      ["kernel-1", "a"],
      ["kernel-1", "b"],
    ]);
    await drainWrites(gates);
    expect(gates).toHaveLength(2);
  });
});
