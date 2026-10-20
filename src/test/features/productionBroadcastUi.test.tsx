/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const harness = vi.hoisted(() => ({
  handlers: new Map<string, (payload: unknown) => void>(),
  resyncSubs: new Set<() => void>(),
  xtermProps: null as Record<string, unknown> | null,
  listLive: vi.fn(),
  listSessions: vi.fn(),
  prepareBroadcast: vi.fn(),
  write: vi.fn(),
  ask: vi.fn(),
  assetList: vi.fn(),
  call: vi.fn(),
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
  assetApi: { list: harness.assetList },
  dbApi: {},
  vaultApi: {},
  call: harness.call,
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
    EVENTS: { terminalControl: "terminal://control", terminalThrottled: "terminal://throttled" },
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
  ask: harness.ask,
}));

vi.mock("../../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

vi.mock("../../features/terminal/XtermView", async () => {
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

import { TerminalPane } from "../../features/terminal/TerminalPane";
import { resetProductionClassificationCache } from "../../features/production/productionSettings";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;

const SETTINGS_DTO = {
  productionEnvironments: [] as string[],
  cronConnectOptIn: false,
  cronConnectProductionOptIn: false,
  broadcastConnectOptIn: false,
  broadcastConnectProductionOptIn: false,
  targetSetConnectOptIn: false,
  targetSetConnectProductionOptIn: false,
};

function session(id: string, kind: string) {
  return { id, assetId: `a-${id}`, name: id, kind, status: "connected" as const, tabs: [], createdAt: 0 };
}

function termTab(id: string, sessionId: string, kernelTabId: string) {
  return {
    id,
    kind: "terminal" as const,
    title: id,
    sessionId,
    tabId: kernelTabId,
    closable: true,
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
    accountEpoch: 0,
  });
}

function enableBroadcast(targetIds: string[], origin = "t1"): void {
  useUi.getState().setBroadcast({
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

function typedBroadcastChallenge() {
  return {
    code: "needs_confirm",
    message: "生产环境广播需要输入实际环境名确认",
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

function broadcastDialog(): HTMLElement {
  const dialog = document.body.querySelector<HTMLElement>('[role="dialog"][aria-label="确认生产环境广播"]');
  if (!dialog) throw new Error("broadcast production dialog not found");
  return dialog;
}

function environmentInput(label: string): HTMLInputElement {
  const input = broadcastDialog().querySelector<HTMLInputElement>(`input[aria-label="输入环境名 ${label}"]`);
  if (!input) throw new Error(`environment input for ${label} not found`);
  return input;
}

function confirmBroadcastButton(): HTMLButtonElement {
  const button = [...broadcastDialog().querySelectorAll<HTMLButtonElement>("button")].find(
    (candidate) => candidate.textContent?.trim() === "确认广播（2）",
  );
  if (!button) throw new Error("broadcast confirm button not found");
  return button;
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
  harness.assetList.mockResolvedValue([{ id: "a-s1", tags: "env:production" }]);
  harness.call.mockImplementation((cmd: string) => {
    if (cmd === "production_settings_get") return Promise.resolve(SETTINGS_DTO);
    return Promise.reject(new Error(`unexpected command ${cmd}`));
  });
  resetProductionClassificationCache();
  seedWorkspace();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  resetProductionClassificationCache();
});

describe("terminal broadcast typed production confirmation", () => {
  it("retries preflight and writes with the typed environment names", async () => {
    harness.prepareBroadcast.mockRejectedValueOnce(typedBroadcastChallenge()).mockResolvedValue(undefined);
    enableBroadcast(["t1", "t2"]);
    mounted = mountPane();
    await flush();
    sendInput("deploy --safe\n");
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境广播"]') !== null);

    const confirm = confirmBroadcastButton();
    expect(confirm.disabled).toBe(true);
    setInputValue(environmentInput("one.production.internal"), "staging");
    setInputValue(environmentInput("two.production.internal"), "production");
    expect(confirm.disabled).toBe(true);
    setInputValue(environmentInput("one.production.internal"), "production");
    expect(confirm.disabled).toBe(false);
    click(confirm);

    await flushUntil(() => harness.write.mock.calls.length === 2);
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

  it("canceling the typed confirmation sends nothing", async () => {
    harness.prepareBroadcast.mockRejectedValueOnce(typedBroadcastChallenge());
    enableBroadcast(["t1", "t2"]);
    mounted = mountPane();
    await flush();
    sendInput("deploy --unsafe\n");
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境广播"]') !== null);

    const cancel = [...broadcastDialog().querySelectorAll<HTMLButtonElement>("button")].find(
      (button) => button.textContent?.trim() === "取消",
    );
    if (!cancel) throw new Error("broadcast cancel button not found");
    click(cancel);
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境广播"]') === null);
    expect(harness.write).not.toHaveBeenCalled();
    expect(useUi.getState().toasts.map((toast) => toast.text)).toContain("已取消生产广播，待确认输入未发送");
  });
});

describe("terminal production warning", () => {
  it("shows the write-risk notice for a production-classified asset and clears it on account switch", async () => {
    mounted = mountPane();
    await flushUntil(() => document.body.querySelector('[data-testid="terminal-production-warning"]') !== null);
    const warning = document.body.querySelector<HTMLElement>('[data-testid="terminal-production-warning"]');
    expect(warning?.textContent).toContain("production");
    expect(warning?.textContent).toContain("不会记住授权");

    // Account switch: the next owner has no production assets, so the warning clears.
    harness.assetList.mockResolvedValue([]);
    act(() => {
      useUi.getState().advanceAccountEpoch();
    });
    await flush();
    expect(document.body.querySelector('[data-testid="terminal-production-warning"]')).toBeNull();
  });
});
