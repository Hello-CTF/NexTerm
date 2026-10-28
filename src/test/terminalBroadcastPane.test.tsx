/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, StrictMode } from "react";
import { click, flush, flushMacrotask, flushUntil, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const harness = vi.hoisted(() => ({
  handlers: new Map<string, (payload: unknown) => void>(),
  resyncSubs: new Set<() => void>(),
  xtermProps: null as Record<string, unknown> | null,
  listLive: vi.fn(),
  listSessions: vi.fn(),
  prepareBroadcast: vi.fn(),
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
  sessionApi: { connect: vi.fn(), reconnect: vi.fn(), disconnect: vi.fn(), list: harness.listSessions },
  terminalApi: {
    listLive: harness.listLive,
    prepareBroadcast: harness.prepareBroadcast,
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
  const { useEffect, useRef } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
      harness.xtermProps = props;
      const propsRef = useRef(props);
      propsRef.current = props;
      useEffect(() => {
        const current = propsRef.current;
        (current.onHandle as ((handle: unknown) => void) | undefined)?.({
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
        const suffix = String(current.sessionId).replace(/\D/g, "") || "1";
        (current.onAttach as ((id: string) => void) | undefined)?.(`kernel-${suffix}`);
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
      },
    ],
    activeWorkspaceId: "ws1",
    broadcast: null,
  });
}

function addSecondWorkspace(): void {
  const state = useUi.getState();
  useUi.setState({
    sessions: [...state.sessions, session("s4", "ssh")],
    workspaces: [
      ...state.workspaces,
      {
        id: "ws2",
        kind: "session",
        title: "ws-4",
        closable: true,
        sessionId: "s4",
        panes: [
          {
            id: "p4",
            activeTabId: "t4",
            tabs: [termTab("t4", "s4", "kernel-4")],
          },
        ],
        activePaneId: "p4",
      },
    ],
  });
}

function enableBroadcast(targetIds: string[], origin = "t1"): void {
  useUi.getState().setBroadcast({
    origin: { workspaceId: "ws1", storeTabId: origin },
    targetIds,
  });
}

function expectBroadcast(targetIds: string[], origin = "t1"): void {
  expect(useUi.getState().broadcast).toMatchObject({
    origin: { workspaceId: "ws1", storeTabId: origin },
    targetIds,
  });
}

function mountPane(storeTabId = "t1"): MountedView {
  const tab = useUi
    .getState()
    .workspaces.flatMap((workspace) => workspace.panes)
    .flatMap((pane) => pane.tabs)
    .find((candidate) => candidate.id === storeTabId);
  return mount(createElement(TerminalPane, {
    sessionId: tab?.sessionId ?? "s1",
    title: tab?.title ?? storeTabId,
    storeTabId,
  }));
}

function sendInput(data: string): void {
  act(() => {
    (harness.xtermProps?.onData as ((value: string) => void) | undefined)?.(data);
  });
}

function bodyText(): string {
  return document.body.textContent ?? "";
}

function toastTexts(): string[] {
  return useUi.getState().toasts.map((toast) => toast.text);
}

function writtenTabIds(): string[] {
  return (harness.write.mock.calls as [string, Uint8Array][]).map(([id]) => id);
}

function broadcastChallengeError() {
  return {
    code: "needs_confirm",
    message: "生产环境广播需要显式确认",
    detail: {
      kind: "broadcast_production_confirmation",
      targetSetHash: "a".repeat(64),
      inputHash: "b".repeat(64),
      requiredEnvironmentNames: ["production"],
      productionTargets: [
        { tabId: "kernel-1", sessionId: "s1", sessionName: "one", hostname: "one.production.internal", environment: "production" },
        { tabId: "kernel-2", sessionId: "s2", sessionName: "two", hostname: "two.production.internal", environment: "production" },
      ],
    },
  };
}

function broadcastProductionDialog(): HTMLElement {
  const dialog = document.body.querySelector<HTMLElement>('[role="dialog"][aria-label="确认生产环境广播"]');
  if (!dialog) throw new Error("broadcast production dialog not found");
  return dialog;
}

beforeEach(() => {
  vi.clearAllMocks();
  harness.handlers.clear();
  harness.resyncSubs.clear();
  document.body.replaceChildren();
  harness.listLive.mockResolvedValue([]);
  harness.listSessions.mockImplementation(() => Promise.resolve(useUi.getState().sessions));
  harness.prepareBroadcast.mockResolvedValue(undefined);
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

  it("fans input out across two workspaces and lists persistent per-target status", async () => {
    addSecondWorkspace();
    enableBroadcast(["t1", "t4"]);
    mounted = mountPane();
    await flush();
    expect(bodyText()).toContain("广播 2");
    expect(bodyText()).toContain("输入将发送到 2 个目标");
    expect(bodyText()).toContain("ws-4");
    expect(bodyText()).toContain("等待输入");
    sendInput("ls\n");
    await flush();
    expect(new Set(writtenTabIds())).toEqual(new Set(["kernel-1", "kernel-4"]));
    expect(document.querySelectorAll('[data-status="sent"]')).toHaveLength(2);
    expect(bodyText()).toContain("已发送");
  });

  it("remains usable after StrictMode effect replay", async () => {
    enableBroadcast(["t1", "t2"]);
    mounted = mount(createElement(
      StrictMode,
      null,
      createElement(TerminalPane, { sessionId: "s1", title: "t1", storeTabId: "t1" }),
    ));
    await flush();
    expect(harness.xtermProps?.tabId).toBe("kernel-1");
    sendInput("x");
    await flush();
    expect(new Set(writtenTabIds())).toEqual(new Set(["kernel-1", "kernel-2"]));
  });

  it("keeps a failed target visible in the persistent strip", async () => {
    harness.write.mockImplementation((tabId: string) =>
      tabId === "kernel-2" ? Promise.reject(new Error("boom")) : Promise.resolve(),
    );
    enableBroadcast(["t1", "t2"]);
    mounted = mountPane();
    await flush();
    sendInput("ls\n");
    await flush();
    expect(harness.write).toHaveBeenCalledTimes(2);
    expect(document.querySelectorAll('[data-status="sent"]')).toHaveLength(1);
    expect(document.querySelectorAll('[data-status="failed"]')).toHaveLength(1);
    expect(bodyText()).toContain("失败");
  });

  it("skips dead targets without writing to them and records the reason", async () => {
    enableBroadcast(["t1", "t2"]);
    useUi.getState().updateTab("t2", { dead: true });
    mounted = mountPane();
    await flush();
    expect(bodyText()).toContain("1 个当前不可写");
    sendInput("ls\n");
    await flush();
    expect(writtenTabIds()).toEqual(["kernel-1"]);
    const skipped = document.querySelector('[data-status="skipped"]');
    expect(skipped?.getAttribute("title")).toBe("终端已失效");
    expect(bodyText()).toContain("已跳过");
  });

  it("activates broadcast only from the origin terminal", async () => {
    enableBroadcast(["t1", "t2"], "t2");
    mounted = mountPane("t1");
    await flush();
    expect(bodyText()).not.toContain("输入将发送到");
    sendInput("ls\n");
    await flush();
    expect(writtenTabIds()).toEqual(["kernel-1"]);
  });

  it("does not implicitly write to an origin outside the target set", async () => {
    enableBroadcast(["t2"], "t1");
    mounted = mountPane("t1");
    await flush();
    sendInput("ls\n");
    await flush();
    expect(writtenTabIds()).toEqual(["kernel-2"]);
  });

  it("restores single-terminal input after broadcast is turned off", async () => {
    enableBroadcast(["t1", "t2"]);
    mounted = mountPane();
    await flush();
    const badge = [...document.querySelectorAll<HTMLElement>("button")].find(
      (element) => element.textContent?.trim() === "广播 2",
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

  it("broadcasts from a terminal whose size another device owns", async () => {
    enableBroadcast(["t1", "t2"]);
    mounted = mountPane();
    await flush();
    const handler = harness.handlers.get("terminal://control");
    expect(handler).toBeDefined();
    act(() => {
      handler?.({
        tabId: "kernel-1",
        version: 2,
        sizeOwner: "other",
        subscribers: 2,
        viewers: 2,
        exited: false,
        cols: 80,
        rows: 24,
        gridRevision: 0,
      });
    });
    sendInput("ls\n");
    await flushUntil(() => harness.write.mock.calls.length === 2);
    expect(harness.write).toHaveBeenCalledTimes(2);
  });

  it("shows backend production targets and retries preflight and writes with the confirmed hashes", async () => {
    harness.prepareBroadcast.mockRejectedValueOnce(broadcastChallengeError()).mockResolvedValue(undefined);
    enableBroadcast(["t1", "t2"]);
    mounted = mountPane();
    await flush();
    sendInput("deploy --safe\n");
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境广播"]') !== null);

    const dialog = broadcastProductionDialog();
    expect(dialog.textContent).toContain("one.production.internal");
    expect(dialog.textContent).toContain("two.production.internal");
    expect(dialog.textContent).toContain("deploy --safe");
    expect(harness.write).not.toHaveBeenCalled();
    expect(harness.prepareBroadcast.mock.calls[0][2]).toBeUndefined();
    expect(new TextDecoder().decode(harness.prepareBroadcast.mock.calls[0][1])).toBe("deploy --safe\n");

    const firstEnvironment = dialog.querySelector<HTMLInputElement>('input[aria-label="输入环境名 one.production.internal"]');
    const secondEnvironment = dialog.querySelector<HTMLInputElement>('input[aria-label="输入环境名 two.production.internal"]');
    const confirm = [...dialog.querySelectorAll<HTMLButtonElement>("button")].find(
      (button) => button.textContent?.trim() === "确认广播（2）",
    );
    if (!firstEnvironment || !secondEnvironment || !confirm) throw new Error("broadcast confirmation controls not found");
    expect(confirm.disabled).toBe(true);
    setInputValue(firstEnvironment, "production");
    setInputValue(secondEnvironment, "production");
    expect(confirm.disabled).toBe(false);
    click(confirm);
    await flushUntil(
      () => harness.write.mock.calls.length === 2 &&
        document.body.querySelector('[role="dialog"][aria-label="确认生产环境广播"]') === null,
    );

    const productionConfirmation = {
      confirmed: true,
      targetSetHash: "a".repeat(64),
      inputHash: "b".repeat(64),
      environmentNames: ["production"],
    };
    expect(harness.prepareBroadcast).toHaveBeenCalledTimes(2);
    expect(harness.prepareBroadcast.mock.calls[1][2]).toEqual(productionConfirmation);
    for (const call of harness.write.mock.calls) {
      expect(call[2]).toEqual({ targetTabIds: ["kernel-1", "kernel-2"], productionConfirmation });
    }
  });

  it("canceling production confirmation sends nothing and leaves ordinary input unaffected", async () => {
    harness.prepareBroadcast.mockRejectedValueOnce(broadcastChallengeError());
    enableBroadcast(["t1", "t2"]);
    mounted = mountPane();
    await flush();
    sendInput("deploy --unsafe\n");
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境广播"]') !== null);

    const cancel = [...broadcastProductionDialog().querySelectorAll<HTMLButtonElement>("button")].find(
      (button) => button.textContent?.trim() === "取消",
    );
    if (!cancel) throw new Error("broadcast cancel button not found");
    click(cancel);
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境广播"]') === null);
    expect(harness.write).not.toHaveBeenCalled();
    expect(toastTexts()).toContain("已取消生产广播，待确认输入未发送");

    act(() => useUi.getState().setBroadcast(null));
    await flush();
    sendInput("ordinary\n");
    await flush();
    expect(harness.write).toHaveBeenCalledTimes(1);
    expect(harness.write.mock.calls[0][0]).toBe("kernel-1");
    expect(harness.write.mock.calls[0]).toHaveLength(2);
    expect(harness.prepareBroadcast).toHaveBeenCalledTimes(1);
  });

  it("does not release a confirmed retry when cancel happens while its preflight is pending", async () => {
    let resolveRetry!: () => void;
    const retry = new Promise<void>((resolve) => {
      resolveRetry = resolve;
    });
    harness.prepareBroadcast.mockRejectedValueOnce(broadcastChallengeError()).mockReturnValueOnce(retry);
    enableBroadcast(["t1", "t2"]);
    mounted = mountPane();
    await flush();
    sendInput("deploy --safe\n");
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境广播"]') !== null);

    const dialog = broadcastProductionDialog();
    const firstEnvironment = dialog.querySelector<HTMLInputElement>('input[aria-label="输入环境名 one.production.internal"]');
    const secondEnvironment = dialog.querySelector<HTMLInputElement>('input[aria-label="输入环境名 two.production.internal"]');
    const confirm = [...dialog.querySelectorAll<HTMLButtonElement>("button")].find(
      (button) => button.textContent?.trim() === "确认广播（2）",
    );
    if (!firstEnvironment || !secondEnvironment || !confirm) throw new Error("broadcast confirmation controls not found");
    setInputValue(firstEnvironment, "production");
    setInputValue(secondEnvironment, "production");
    click(confirm);
    await flushUntil(() => harness.prepareBroadcast.mock.calls.length === 2);
    const cancel = [...dialog.querySelectorAll<HTMLButtonElement>("button")].find(
      (button) => button.textContent?.trim() === "取消",
    );
    if (!cancel) throw new Error("broadcast cancel button not found");
    click(cancel);
    resolveRetry();
    await flush();

    expect(harness.write).not.toHaveBeenCalled();
    expect(document.body.querySelector('[role="dialog"][aria-label="确认生产环境广播"]')).toBeNull();
  });
});

describe("broadcast enable flow", () => {
  async function openOverflowMenu(): Promise<void> {
    const overflow = [...document.querySelectorAll<HTMLElement>("button")].find(
      (element) => element.getAttribute("aria-label") === "更多终端操作",
    );
    expect(overflow).toBeDefined();
    act(() => {
      overflow?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await flush();
  }

  function clickMenuItem(label: string): void {
    const labelElement = [...document.querySelectorAll<HTMLElement>(".nx-menu-label")].find(
      (element) => element.textContent?.trim() === label,
    );
    const item = labelElement?.closest("button");
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

  it("enables broadcast for every open workspace after confirmation", async () => {
    addSecondWorkspace();
    mounted = mountPane();
    await flush();
    await openOverflowMenu();
    clickMenuItem("广播到全部终端");
    await flush();
    expectBroadcast(["t1", "t2", "t3", "t4"]);
  });

  it("enables broadcast for the current pane only via quick pick", async () => {
    mounted = mountPane();
    await flush();
    await openOverflowMenu();
    clickMenuItem("广播到当前分屏");
    await flush();
    expectBroadcast(["t1", "t2"]);
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
    expect(toastTexts().some((text) => text.includes("可广播终端不足 2 个"))).toBe(true);
  });

  it("groups unselected terminals by host and workspace in the picker", async () => {
    addSecondWorkspace();
    mounted = mountPane();
    await flush();
    await openOverflowMenu();
    clickMenuItem("选择广播目标…");
    await flush();
    expect(bodyText()).toContain("命令广播目标");
    expect(bodyText()).toContain("工作区：ws-4");
    expect(bodyText()).toContain("t4");
    expect(bodyText()).toContain("已选 2 个");
    const allButton = [...document.querySelectorAll<HTMLElement>("button")].find(
      (element) => element.textContent?.trim() === "全部终端",
    );
    expect(allButton).toBeDefined();
    act(() => {
      allButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await flush();
    expect(bodyText()).toContain("已选 4 个");
    const applyButton = [...document.querySelectorAll<HTMLElement>("button")].find((element) =>
      element.textContent?.trim().startsWith("开启广播"),
    );
    expect(applyButton?.textContent?.trim()).toBe("开启广播（4）");
    act(() => {
      applyButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await flush();
    expect(harness.ask).toHaveBeenCalledTimes(1);
    expectBroadcast(["t1", "t2", "t3", "t4"]);
  });
});

describe("broadcast input invalidation", () => {
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
    const gate = gates.find((item) => !item.settled);
    if (!gate) throw new Error("no pending write to release");
    gate.resolve();
    await flush();
  }

  async function drainWrites(gates: WriteGate[]): Promise<void> {
    for (let index = 0; index < 50; index += 1) {
      await flush();
      if (gates.every((gate) => gate.settled)) return;
      await releaseOldest(gates);
    }
    throw new Error("writes did not drain");
  }

  function gatePairs(gates: WriteGate[]): [string, string][] {
    return gates.map((gate) => [gate.id, gate.data]);
  }

  it("drops queued input when broadcast is closed and does not block later input", async () => {
    enableBroadcast(["t1", "t2"]);
    const gates = gateWrites();
    mounted = mountPane();
    await flush();
    sendInput("a");
    await flush();
    expect(gatePairs(gates)).toEqual([["kernel-1", "a"], ["kernel-2", "a"]]);
    sendInput("b");
    await flush();
    expect(gates).toHaveLength(2);
    act(() => useUi.getState().setBroadcast(null));
    await flush();
    sendInput("b");
    await flush();
    expect(gatePairs(gates)).toEqual([
      ["kernel-1", "a"],
      ["kernel-2", "a"],
      ["kernel-1", "b"],
    ]);
    await drainWrites(gates);
    expect(gates.filter((gate) => gate.data === "b").map((gate) => gate.id)).toEqual(["kernel-1"]);

    act(() => {
      enableBroadcast(["t1", "t2"]);
      (harness.xtermProps?.onData as ((value: string) => void) | undefined)?.("c");
    });
    await drainWrites(gates);
    expect(gatePairs(gates)).toEqual([
      ["kernel-1", "a"],
      ["kernel-2", "a"],
      ["kernel-1", "b"],
      ["kernel-1", "c"],
      ["kernel-2", "c"],
    ]);
  });

  it("drops queued input when the target set changes and sends new input to the new targets", async () => {
    enableBroadcast(["t1", "t2", "t3"]);
    const gates = gateWrites();
    mounted = mountPane();
    await flush();
    sendInput("a");
    await flush();
    expect(gatePairs(gates)).toEqual([
      ["kernel-1", "a"],
      ["kernel-2", "a"],
      ["kernel-3", "a"],
    ]);
    sendInput("b");
    act(() => enableBroadcast(["t1", "t2"]));
    await flush();
    await drainWrites(gates);
    expect(gates.map((gate) => gate.data)).toEqual(["a", "a", "a"]);
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

  async function expectOriginSwitchToDropQueuedInput(
    switchOrigin: () => void,
    withSecondWorkspace = false,
  ): Promise<void> {
    if (withSecondWorkspace) addSecondWorkspace();
    enableBroadcast(["t1", "t2"]);
    const gates = gateWrites();
    mounted = mountPane();
    await flush();
    sendInput("a");
    await flush();
    sendInput("b");
    await flush();
    expect(gatePairs(gates)).toEqual([["kernel-1", "a"], ["kernel-2", "a"]]);
    await act(async () => {
      switchOrigin();
      for (const gate of gates) gate.resolve();
      await flushMacrotask();
    });
    expect(gatePairs(gates)).toEqual([["kernel-1", "a"], ["kernel-2", "a"]]);
  }

  it("drops queued input as soon as the origin tab is no longer active", async () => {
    await expectOriginSwitchToDropQueuedInput(() => useUi.getState().setActiveTab("t2"));
  });

  it("drops queued input as soon as the origin workspace is no longer active", async () => {
    await expectOriginSwitchToDropQueuedInput(
      () => useUi.getState().setActiveWorkspace("ws2"),
      true,
    );
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
