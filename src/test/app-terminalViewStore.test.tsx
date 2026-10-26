/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";

const harness = vi.hoisted(() => ({
  eventHandlers: new Map<string, (payload: unknown) => void>(),
  xtermProps: null as Record<string, unknown> | null,
}));

vi.mock("../ipc/commands", () => ({
  layoutApi: { get: vi.fn(), put: vi.fn() },
  assetApi: {},
  dbApi: {},
  vaultApi: {},
  sessionApi: {
    connect: vi.fn(),
    reconnect: vi.fn().mockResolvedValue(true),
    disconnect: vi.fn(),
    list: vi.fn(),
    probeHostKey: vi.fn().mockResolvedValue({ state: "known" }),
  },
  terminalApi: {
    listLive: vi.fn().mockResolvedValue([]),
    closeTab: vi.fn(),
    write: vi.fn().mockResolvedValue(undefined),
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
    attach: vi.fn(),
    attachTab: vi.fn(),
    renameTab: vi.fn(),
    recordingStatus: vi.fn().mockResolvedValue({ tabId: "kernel-1", state: "never-started", bytes: 0, generation: 0 }),
    recordStart: vi.fn().mockResolvedValue(undefined),
    recordStop: vi.fn().mockResolvedValue(0),
  },
  fsApi: {},
  syncApi: {},
}));

vi.mock("../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/events")>();
  return {
    ...actual,
    listenEvent: vi.fn((topic: string, handler: (payload: unknown) => void) => {
      harness.eventHandlers.set(topic, handler);
      return Promise.resolve(() => {});
    }),
    EVENTS: { terminalControl: "terminal://control", terminalThrottled: "terminal://throttled" },
  };
});

vi.mock("../ipc/webTransport", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/webTransport")>();
  return { ...actual, onEventsResync: () => () => {} };
});

vi.mock("../ipc/env", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/env")>();
  return { ...actual, clientId: () => "me", WEB: false };
});

vi.mock("../app/platform", () => ({
  isMac: () => false,
  modHint: () => "Ctrl",
  setMacPlatform: () => {},
  wailsDragRegionStyle: {},
  wailsNoDragRegionStyle: {},
  isWailsDragRegionTarget: () => false,
}));

vi.mock("../ui/dialogs", () => ({
  describeTarget: () => "",
  finishSave: vi.fn(),
  pickSavePath: vi.fn(),
  promptText: vi.fn(),
  ask: vi.fn(),
  askChoice: vi.fn(),
}));

vi.mock("../ui/clipboard", () => ({
  copyText: vi.fn().mockResolvedValue(undefined),
  readText: vi.fn(),
}));

vi.mock("../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

vi.mock("../features/terminal/XtermView", async () => {
  const { useEffect, useRef } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
      harness.xtermProps = props;
      const attachFiredRef = useRef(false);
      useEffect(() => {
        if (attachFiredRef.current) return;
        attachFiredRef.current = true;
        (props.onHandle as ((h: unknown) => void) | undefined)?.({
          copyBlock: () => "",
          scrollToBlock: () => {},
          navigateBlock: () => null,
          clearBlocks: () => {},
          clear: () => {},
          getSelection: () => "",
          focus: () => {},
          fit: () => false,
          paste: () => {},
          dimensions: () => ({ cols: 80, rows: 24 }),
        });
        (props.onAttach as ((id: string) => void) | undefined)?.("kernel-1");
      }, [props.onAttach, props.onHandle]);
      return null;
    },
  };
});

import { sessionApi, terminalApi } from "../ipc/commands";
import { useUi, type AppTab } from "../app/store";
import {
  applyAuthoritativeTabTitle,
  applyRemoteTabTitleToView,
  clearAuthoritativeTabTitle,
  getTerminalLinesBelow,
  getTerminalTabTitle,
  resetTerminalLinesBelow,
  resetTerminalTabTitle,
  resetTerminalView,
  setTerminalLinesBelow,
  setTerminalTabTitle,
  useTerminalLinesBelow,
  useTerminalTabTitle,
} from "../app/terminalViewStore";
import { TerminalPane } from "../features/terminal/TerminalPane";
import { TabStripTitle } from "../app/workspace/tabStrip";
import { flush, mount, type MountedView } from "./features/reactTestUtils";

describe("terminal view store selectors", () => {
  let renders: string[];
  let mounted: MountedView | undefined;

  function Probe({ tabId }: { tabId: string }) {
    const linesBelow = useTerminalLinesBelow(tabId);
    const title = useTerminalTabTitle(tabId);
    renders.push(`${linesBelow}|${title ?? ""}`);
    return null;
  }

  beforeEach(() => {
    mounted?.unmount();
    act(() => resetTerminalView());
    renders = [];
    mounted = mount(createElement(Probe, { tabId: "t1" }));
  });

  it("renders the initial zero state without lines below or title", () => {
    expect(renders).toEqual(["0|"]);
    expect(getTerminalLinesBelow("t1")).toBe(0);
    expect(getTerminalTabTitle("t1")).toBeUndefined();
  });

  it("re-renders only when the subscribed tab values change", () => {
    act(() => setTerminalLinesBelow("t1", 5));
    expect(renders).toEqual(["0|", "5|"]);

    // Another tab's scrolling must not re-render this subscriber.
    act(() => {
      setTerminalLinesBelow("t2", 9);
      setTerminalTabTitle("t2", "htop");
    });
    expect(renders).toEqual(["0|", "5|"]);

    act(() => setTerminalTabTitle("t1", "vim"));
    expect(renders).toEqual(["0|", "5|", "5|vim"]);
    expect(getTerminalTabTitle("t1")).toBe("vim");
  });

  it("treats unchanged values as no-ops so output bursts do not re-render", () => {
    act(() => setTerminalLinesBelow("t1", 5));
    const before = renders.length;
    act(() => {
      setTerminalLinesBelow("t1", 5);
      setTerminalLinesBelow("t1", 5);
    });
    expect(renders).toHaveLength(before);
  });

  it("clears every tracked value on reset", () => {
    act(() => {
      setTerminalLinesBelow("t1", 5);
      setTerminalTabTitle("t1", "vim");
    });
    act(() => resetTerminalView());
    // React batches both sets inside one act() into a single re-render.
    expect(renders).toEqual(["0|", "5|vim", "0|"]);
    expect(getTerminalLinesBelow("t1")).toBe(0);
    expect(getTerminalTabTitle("t1")).toBeUndefined();
  });

  it("drops only the reset tab when a tab id is given", () => {
    act(() => {
      setTerminalLinesBelow("t1", 5);
      setTerminalLinesBelow("t2", 9);
    });
    act(() => resetTerminalView("t2"));
    expect(getTerminalLinesBelow("t1")).toBe(5);
    expect(getTerminalLinesBelow("t2")).toBe(0);
  });
});

function terminalTab(id = "t1", extra: Partial<AppTab> = {}): AppTab {
  return {
    id,
    kind: "terminal",
    title: "Terminal 1",
    sessionId: "s",
    tabId: `kernel-${id}`,
    closable: true,
    ...extra,
  };
}

function seedWorkspaces(tabs: AppTab[]) {
  useUi.setState({
    workspaces: [
      {
        id: "ws",
        kind: "session",
        title: "ws",
        panes: [{ id: "pane", tabs, activeTabId: tabs[0]?.id ?? null }],
        activePaneId: "pane",
        closable: true,
      },
    ],
    activeWorkspaceId: "ws",
    sessions: [],
    toasts: [],
  });
}

function workspaceTitleOf(id = "t1"): string | undefined {
  for (const w of useUi.getState().workspaces) {
    for (const p of w.panes) {
      const t = p.tabs.find((x) => x.id === id);
      if (t) return t.title;
    }
  }
  return undefined;
}

describe("applyRemoteTabTitleToView (OSC title chain)", () => {
  let renders: string[];
  let mounted: MountedView | undefined;

  function Probe({ tabId }: { tabId: string }) {
    const title = useTerminalTabTitle(tabId);
    renders.push(title ?? "");
    return null;
  }

  beforeEach(() => {
    localStorage.clear();
    mounted?.unmount();
    act(() => resetTerminalView());
    seedWorkspaces([terminalTab()]);
    renders = [];
    mounted = mount(createElement(Probe, { tabId: "t1" }));
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("lands an accepted remote title in the workspace tab and the view store", () => {
    act(() => {
      expect(applyRemoteTabTitleToView("t1", "user@host: ~/proj")).toBe(true);
    });
    expect(workspaceTitleOf()).toBe("user@host: ~/proj");
    expect(getTerminalTabTitle("t1")).toBe("user@host: ~/proj");
    expect(renders).toEqual(["", "user@host: ~/proj"]);
  });

  it("keeps a user-renamed tab authoritative in both stores", () => {
    seedWorkspaces([terminalTab("t1", { title: "my terminal", renamedByUser: true, customTitleAt: 1 })]);
    act(() => {
      expect(applyRemoteTabTitleToView("t1", "remote title")).toBe(false);
    });
    expect(workspaceTitleOf()).toBe("my terminal");
    expect(getTerminalTabTitle("t1")).toBeUndefined();
    expect(renders).toEqual([""]);
  });

  it("writes nothing for unknown tabs", () => {
    act(() => {
      expect(applyRemoteTabTitleToView("missing", "title")).toBe(false);
    });
    expect(getTerminalTabTitle("missing")).toBeUndefined();
  });

  it("dedups repeated identical OSC titles so a burst re-renders nothing", () => {
    act(() => {
      applyRemoteTabTitleToView("t1", "vim");
    });
    expect(renders).toEqual(["", "vim"]);
    act(() => {
      applyRemoteTabTitleToView("t1", "vim");
      applyRemoteTabTitleToView("t1", "vim");
    });
    expect(renders).toEqual(["", "vim"]);
  });

  it("stores the sanitized title in both stores", () => {
    act(() => {
      applyRemoteTabTitleToView("t1", "\x07evil\x00 title\nnext\tline");
    });
    expect(workspaceTitleOf()).toBe("evil title next line");
    expect(getTerminalTabTitle("t1")).toBe("evil title next line");
  });

  it("a rename after a remote title clears the view entry and locks out later OSC titles", () => {
    act(() => {
      applyRemoteTabTitleToView("t1", "vim");
      setTerminalLinesBelow("t1", 90);
    });
    act(() => {
      applyAuthoritativeTabTitle("t1", "locked");
    });
    expect(getTerminalTabTitle("t1")).toBeUndefined();
    // The rename clears only the OSC view title: the badge state survives.
    expect(getTerminalLinesBelow("t1")).toBe(90);
    expect(renders).toEqual(["", "vim", ""]);
    act(() => {
      expect(applyRemoteTabTitleToView("t1", "other")).toBe(false);
    });
    expect(workspaceTitleOf()).toBe("locked");
    expect(getTerminalTabTitle("t1")).toBeUndefined();
    expect(getTerminalLinesBelow("t1")).toBe(90);
  });

  it("narrow resets touch only their own kind; resetTerminalView stays the whole-tab teardown", () => {
    act(() => {
      setTerminalLinesBelow("t1", 90);
      setTerminalTabTitle("t1", "vim");
    });
    act(() => resetTerminalTabTitle("t1"));
    expect(getTerminalTabTitle("t1")).toBeUndefined();
    expect(getTerminalLinesBelow("t1")).toBe(90);
    act(() => {
      setTerminalTabTitle("t1", "vim");
      resetTerminalLinesBelow("t1");
    });
    expect(getTerminalLinesBelow("t1")).toBe(0);
    expect(getTerminalTabTitle("t1")).toBe("vim");
    act(() => resetTerminalView("t1"));
    expect(getTerminalLinesBelow("t1")).toBe(0);
    expect(getTerminalTabTitle("t1")).toBeUndefined();
  });

  it("account-switch cleanup leaves no view state and rejects a late OSC title for a gone tab", () => {
    act(() => {
      applyRemoteTabTitleToView("t1", "vim");
      setTerminalLinesBelow("kernel-t1", 42);
    });
    act(() => {
      // The account switch clears the workspaces; the per-tab unmount cleanups
      // aggregate to a full view-store reset.
      useUi.getState().resetAccountWorkspaces();
      resetTerminalView();
    });
    expect(getTerminalTabTitle("t1")).toBeUndefined();
    expect(getTerminalLinesBelow("kernel-t1")).toBe(0);
    act(() => {
      expect(applyRemoteTabTitleToView("t1", "late title")).toBe(false);
    });
    expect(getTerminalTabTitle("t1")).toBeUndefined();
  });
});

describe("TerminalPane attach/control title wiring (real onAttachInfo)", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    document.body.replaceChildren();
    act(() => resetTerminalView());
    useUi.setState({
      recordings: {},
      sessions: [
        { id: "s1", assetId: "a1", name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 0 },
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
                { id: "t1", kind: "terminal", title: "term", sessionId: "s1", tabId: "kernel-1", closable: true },
              ],
            },
          ],
          activePaneId: "p1",
        },
      ],
      activeWorkspaceId: "ws1",
      toasts: [],
      connectProgress: {},
      helperProgress: {},
    });
    vi.mocked(sessionApi.list).mockImplementation(() => Promise.resolve(useUi.getState().sessions));
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  function fireAttachInfo(info: Record<string, unknown>) {
    act(() => {
      (harness.xtermProps?.onAttachInfo as ((payload: unknown) => void) | undefined)?.(info);
    });
  }

  function deliverControl(payload: Record<string, unknown>) {
    const handler = harness.eventHandlers.get("terminal://control");
    expect(handler).toBeDefined();
    act(() => {
      handler?.(payload);
    });
  }

  it("pre-response linesBelow survives an attach response carrying a custom title", async () => {
    // The pane's pre-response scroll state, written under its stable viewTabId.
    act(() => setTerminalLinesBelow("t1", 90));
    mounted = mount(createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }));
    await flush();
    fireAttachInfo({
      tabId: "kernel-1",
      controller: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "posix",
      retainable: true,
      ephemeral: false,
      attachmentId: 11,
      title: "构建机",
      titleCustom: true,
    });
    // The custom title lands on the workspace tab and clears only the OSC view
    // title; the badge state under the same key survives untouched.
    expect(workspaceTitleOf()).toBe("构建机");
    expect(getTerminalTabTitle("t1")).toBeUndefined();
    expect(getTerminalLinesBelow("t1")).toBe(90);
    // Custom-title precedence: a later OSC title is rejected, badge still untouched.
    expect(applyRemoteTabTitleToView("t1", "osc title")).toBe(false);
    expect(workspaceTitleOf()).toBe("构建机");
    expect(getTerminalLinesBelow("t1")).toBe(90);
  });

  it("same-title control snapshots never clear the badge state", async () => {
    act(() => setTerminalLinesBelow("t1", 90));
    mounted = mount(createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }));
    await flush();
    const control = (version: number) => ({
      tabId: "kernel-1",
      version,
      controller: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      title: "构建机",
      titleCustom: true,
    });
    deliverControl(control(1));
    expect(workspaceTitleOf()).toBe("构建机");
    expect(getTerminalLinesBelow("t1")).toBe(90);
    deliverControl(control(2));
    expect(workspaceTitleOf()).toBe("构建机");
    expect(getTerminalLinesBelow("t1")).toBe(90);
  });

  function tabOf(id = "t1"): AppTab | undefined {
    for (const w of useUi.getState().workspaces) {
      for (const p of w.panes) {
        const t = p.tabs.find((x) => x.id === id);
        if (t) return t;
      }
    }
    return undefined;
  }

  const attachInfo = (extra: Record<string, unknown>) => ({
    tabId: "kernel-1",
    controller: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
    shell: "posix",
    ...extra,
  });

  async function mountWithRename(renamed?: { title: string; at: number }) {
    vi.mocked(terminalApi.renameTab).mockReset();
    vi.mocked(terminalApi.renameTab).mockResolvedValue(attachInfo({}) as never);
    if (renamed) act(() => applyAuthoritativeTabTitle("t1", renamed.title, renamed.at));
    mounted = mount(createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }));
    await flush();
  }

  it("a reconnect attach carrying the backend's OSC program title keeps a manual rename", async () => {
    await mountWithRename({ title: "deploy", at: 1_000 });
    // The re-created kernel tab only knows the shell's OSC title and no title decision.
    fireAttachInfo(attachInfo({ title: "root@web-01: ~" }));
    await flush();
    expect(workspaceTitleOf()).toBe("deploy");
    expect(tabOf()?.renamedByUser).toBe(true);
    // The custom title is written back so the backend, other devices and AI tools agree.
    expect(terminalApi.renameTab).toHaveBeenCalledWith("kernel-1", "deploy", 1_000);
    // Later OSC-derived control snapshots and xterm titles never replace it.
    deliverControl({ ...attachInfo({ title: "vim", titleCustom: false }), version: 1 });
    expect(workspaceTitleOf()).toBe("deploy");
    expect(applyRemoteTabTitleToView("t1", "top")).toBe(false);
    expect(workspaceTitleOf()).toBe("deploy");
    expect(getTerminalTabTitle("t1")).toBeUndefined();
  });

  it("a legacy rename lock follows OSC titles and is never written back", async () => {
    await mountWithRename();
    // Older builds stored backend OSC titles as renames, without a decision time.
    act(() => useUi.getState().updateTab("t1", { title: "user@host: ~", renamedByUser: true }));
    fireAttachInfo(attachInfo({ title: "root@web-01: ~" }));
    await flush();
    expect(terminalApi.renameTab).not.toHaveBeenCalled();
    expect(workspaceTitleOf()).toBe("root@web-01: ~");
    expect(getTerminalTabTitle("t1")).toBe("root@web-01: ~");
  });

  it("an OSC program title from the backend stays an OSC title on an unrenamed tab", async () => {
    await mountWithRename();
    fireAttachInfo(attachInfo({ title: "root@web-01: ~" }));
    deliverControl({ ...attachInfo({ title: "vim" }), version: 1 });
    expect(workspaceTitleOf()).toBe("vim");
    expect(getTerminalTabTitle("t1")).toBe("vim");
    expect(tabOf()?.renamedByUser).toBeUndefined();
    expect(terminalApi.renameTab).not.toHaveBeenCalled();
    // Not locked: the next OSC title still lands.
    act(() => {
      expect(applyRemoteTabTitleToView("t1", "top")).toBe(true);
    });
    expect(workspaceTitleOf()).toBe("top");
  });

  it("a clear made on another device after this device's rename wins, and a reattach does not resurrect it", async () => {
    await mountWithRename({ title: "deploy", at: 1_000 });
    deliverControl({ ...attachInfo({ title: "vim", titleCustom: false, titleChangedAt: 2_000 }), version: 1 });
    expect(workspaceTitleOf()).toBe("vim");
    expect(tabOf()).toMatchObject({ renamedByUser: undefined, customTitleAt: 2_000 });
    fireAttachInfo(attachInfo({ title: "vim", titleChangedAt: 2_000 }));
    await flush();
    expect(terminalApi.renameTab).not.toHaveBeenCalled();
    act(() => {
      expect(applyRemoteTabTitleToView("t1", "top")).toBe(true);
    });
  });

  it("a stale backend custom title never replaces a newer local rename and is overwritten on attach", async () => {
    await mountWithRename({ title: "newer", at: 3_000 });
    deliverControl({ ...attachInfo({ title: "older", titleCustom: true, titleChangedAt: 2_000 }), version: 1 });
    expect(workspaceTitleOf()).toBe("newer");
    fireAttachInfo(attachInfo({ title: "older", titleCustom: true, titleChangedAt: 2_000 }));
    await flush();
    expect(terminalApi.renameTab).toHaveBeenCalledWith("kernel-1", "newer", 3_000);
  });

  it("a newer rename from another device replaces an older local rename", async () => {
    await mountWithRename({ title: "older", at: 1_000 });
    deliverControl({ ...attachInfo({ title: "newer", titleCustom: true, titleChangedAt: 2_000 }), version: 1 });
    expect(workspaceTitleOf()).toBe("newer");
    expect(tabOf()).toMatchObject({ renamedByUser: true, customTitleAt: 2_000 });
    fireAttachInfo(attachInfo({ title: "newer", titleCustom: true, titleChangedAt: 2_000 }));
    await flush();
    expect(terminalApi.renameTab).not.toHaveBeenCalled();
  });

  it("a newer local clear is written back over an older backend custom title", async () => {
    await mountWithRename();
    act(() => clearAuthoritativeTabTitle("t1", "", 3_000));
    fireAttachInfo(attachInfo({ title: "deploy", titleCustom: true, titleChangedAt: 2_000 }));
    await flush();
    expect(terminalApi.renameTab).toHaveBeenCalledWith("kernel-1", "", 3_000);
    expect(tabOf()?.renamedByUser).toBeUndefined();
  });

  it("a custom title from another device wins over a cached OSC title on the tab strip", () => {
    act(() => {
      applyRemoteTabTitleToView("t1", "vim");
    });
    // A layout sync lands the rename without going through this device's title path.
    act(() => useUi.getState().updateTab("t1", { title: "deploy", renamedByUser: true, customTitleAt: 1 }));
    mounted = mount(createElement(TabStripTitle, { tab: tabOf()! }));
    expect(document.body.textContent).toBe("deploy");
  });

  it("clearing a custom title returns the tab to OSC titles", () => {
    act(() => applyAuthoritativeTabTitle("t1", "deploy"));
    expect(applyRemoteTabTitleToView("t1", "vim")).toBe(false);
    act(() => clearAuthoritativeTabTitle("t1", "root@web-01: ~"));
    expect(workspaceTitleOf()).toBe("root@web-01: ~");
    expect(tabOf()?.renamedByUser).toBeUndefined();
    act(() => {
      expect(applyRemoteTabTitleToView("t1", "vim")).toBe(true);
    });
    expect(workspaceTitleOf()).toBe("vim");
  });

  it("clearing without a program title falls back to the session name", () => {
    act(() => applyAuthoritativeTabTitle("t1", "deploy"));
    act(() => clearAuthoritativeTabTitle("t1", ""));
    expect(workspaceTitleOf()).toBe("web-01");
  });
});
